package main

// mcpserver.go — the HOST MCP server (spec 03-memory §7.3).
//
// Sessions get their tools from two places. Most come from MCP servers that run
// *inside* the container (stdio) or out on the internet (http) — that is Track A,
// and core has no opinion about them. A few tools are core's own: the memory
// substrate (§7.3), the management tools (§9), image (§13) and skill (§14)
// tools, and the config-log reader (§15.9). Those cannot live in the image:
// they read and write the host's database, and the tenancy boundary that keeps
// one project's memories away from another's is exactly what a session must not
// be trusted to enforce for itself.
//
// So agentd serves them itself, as ONE http MCP server mounted at /mcp:
//
//	session container ──http──▶ agentd /mcp ──▶ agentdb (scoped to the caller's project)
//	                  Authorization: ${SESSION_TOKEN}
//
// # How a session token becomes a project
//
// The Runner mints a per-session JWT at provision time (runner.go issueToken →
// extension.ScopedClaimsIssuer) and injects it as SESSION_TOKEN in the container
// environment. Its claims are `sid` (the session id) and `customer` (the
// tenancy namespace, which the product layer calls the *project*). The session
// MCP config references it as a whole-value `${SESSION_TOKEN}` header (§4.4), so
// the sandbox resolves it at spawn and every tool call arrives bearing it.
//
// agentd verifies that token here and derives the caller:
//
//	claim `customer` → project    — the hard scope; every store call is filtered
//	                                on it, in code, never from a tool argument
//	claim `sid`      → session    — provenance, and the permalink in results
//	sessions.worker  → worker     — provenance, read from the session row (C1)
//
// A session therefore *physically cannot* name another project: there is no
// project parameter on any tool. That is the whole point of doing this in the
// host rather than shipping a database client into the image.
//
// # Where the other tracks plug in
//
// This file is transport and auth only — it knows nothing about memory. Tools
// are registered onto the server at boot:
//
//	srv := newMCPServer(coreMCPServerName, auth)
//	srv.register(newMemoryTools(...).tools()...)   // D3, mcp_memory.go
//	srv.register(newImageTools(...).tools()...)    // I2, mcp_images.go
//	srv.register(newSkillTools(...).tools()...)    // I3, mcp_skills.go
//	srv.register(newManagementTools(...).tools()...) // E4, mcp_management.go
//	srv.register(newConfigLogTools(...).tools()...)  // J3, mcp_config_log.go
//
// Each of those files owns one constructor returning []*mcpTool and nothing
// else; register panics on a duplicate tool name, so two tracks cannot silently
// shadow one another. Adding a tool never touches this file.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/golang-jwt/jwt/v5"
)

const (
	// coreMCPServerName is the name sessions see the core tools under, so the
	// harness derives tool names `mcp__core__memory_search` and friends. It is
	// non-overridable: ComposeJob applies CoreMCP last (§6.2 step 3).
	coreMCPServerName = "core"
	// coreMCPPath is where agentd mounts the server.
	coreMCPPath = "/mcp"
	// mcpProtocolVersion is the version agentd advertises when a client sends
	// none. Our surface is tools-only, which every revision of the protocol
	// spells the same way, so a client that asks for a different version gets
	// its own echoed back rather than a negotiation failure over a difference
	// that cannot affect us.
	mcpProtocolVersion = "2025-06-18"
	// maxMCPRequestBytes bounds one JSON-RPC request. Memory content is allowed
	// to be large (full transcripts, §7.1), so this is generous — but not
	// unbounded: the body is read into memory before it is parsed.
	maxMCPRequestBytes = 8 << 20 // 8 MiB
)

// JSON-RPC 2.0 error codes (the subset an MCP server can produce).
const (
	rpcParseError     = -32700
	rpcInvalidRequest = -32600
	rpcMethodNotFound = -32601
	rpcInvalidParams  = -32602
	rpcInternalError  = -32603
)

// mcpCaller is who is calling — resolved from the session token, never from a
// tool argument. Project is the hard scope; SessionID and Worker are provenance
// (§7.3: "provenance is part of the result, not an extra").
type mcpCaller struct {
	Project   string
	SessionID string
	Worker    string
}

// mcpTool is one registered tool. Handler returns the tool's result value
// (marshalled into the MCP result) or an error, which is reported to the model
// as an `isError` result rather than a transport failure — a tool the model
// called wrongly is something the model can fix on its next turn, and hiding it
// behind a protocol error would just look like the tool vanished.
type mcpTool struct {
	Name        string
	Description string
	InputSchema map[string]any
	Handler     func(ctx context.Context, caller mcpCaller, args json.RawMessage) (any, error)
}

// mcpAuthFunc resolves the caller from the request, or returns an error.
// errMCPUnauthorized maps to 401; anything else to 403.
type mcpAuthFunc func(r *http.Request) (mcpCaller, error)

// errMCPUnauthorized is "no usable credential" — a missing, malformed or
// unverifiable token.
var errMCPUnauthorized = errors.New("unauthorized")

// mcpServer is the HTTP transport plus a tool registry.
type mcpServer struct {
	name  string
	auth  mcpAuthFunc
	tools map[string]*mcpTool
	order []string // registration order, so tools/list is stable
}

func newMCPServer(name string, auth mcpAuthFunc) *mcpServer {
	return &mcpServer{name: name, auth: auth, tools: map[string]*mcpTool{}}
}

// register adds tools. A duplicate name panics at boot: two tracks quietly
// shadowing one another's tool would be a silently wrong system, and this is a
// programming error visible the first time agentd starts.
func (s *mcpServer) register(tools ...*mcpTool) {
	for _, t := range tools {
		if t == nil || t.Name == "" || t.Handler == nil {
			panic("mcpserver: tool needs a name and a handler")
		}
		if _, dup := s.tools[t.Name]; dup {
			panic(fmt.Sprintf("mcpserver: duplicate tool %q", t.Name))
		}
		s.tools[t.Name] = t
		s.order = append(s.order, t.Name)
	}
}

// toolNames lists the registered tools in registration order (for boot logging
// and tests).
func (s *mcpServer) toolNames() []string {
	out := make([]string, len(s.order))
	copy(out, s.order)
	return out
}

// ServeHTTP implements the MCP streamable-HTTP transport, JSON responses only:
// one JSON-RPC request per POST, one JSON-RPC response back. agentd never
// initiates anything towards a session, so there is no server→client SSE stream
// to open and GET is honestly refused rather than left hanging.
func (s *mcpServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		// 405 with Allow is what a well-behaved client uses to conclude "no
		// server-initiated stream here" and carry on with POSTs alone.
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	caller, err := s.auth(r)
	if err != nil {
		status := http.StatusForbidden
		if errors.Is(err, errMCPUnauthorized) {
			status = http.StatusUnauthorized
			w.Header().Set("WWW-Authenticate", "Bearer")
		}
		http.Error(w, err.Error(), status)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxMCPRequestBytes+1))
	if err != nil {
		writeRPC(w, nil, nil, &jsonRPCError{Code: rpcParseError, Message: "read request: " + err.Error()})
		return
	}
	if len(body) > maxMCPRequestBytes {
		writeRPC(w, nil, nil, &jsonRPCError{
			Code:    rpcInvalidRequest,
			Message: fmt.Sprintf("request exceeds %d bytes", maxMCPRequestBytes),
		})
		return
	}

	var req jsonRPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeRPC(w, nil, nil, &jsonRPCError{Code: rpcParseError, Message: "parse error: " + err.Error()})
		return
	}
	// A request without an id is a notification: acknowledge with no body.
	// `notifications/initialized` is the only one a tools-only server sees.
	if len(req.ID) == 0 || string(req.ID) == "null" {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	result, rpcErr := s.dispatch(r.Context(), caller, req)
	writeRPC(w, req.ID, result, rpcErr)
}

// dispatch routes one JSON-RPC method.
func (s *mcpServer) dispatch(ctx context.Context, caller mcpCaller, req jsonRPCRequest) (any, *jsonRPCError) {
	switch req.Method {
	case "initialize":
		return s.initializeResult(req.Params), nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": s.listTools()}, nil
	case "tools/call":
		return s.callTool(ctx, caller, req.Params)
	default:
		return nil, &jsonRPCError{Code: rpcMethodNotFound, Message: "unknown method " + req.Method}
	}
}

func (s *mcpServer) initializeResult(params json.RawMessage) any {
	version := mcpProtocolVersion
	if len(params) > 0 {
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if err := json.Unmarshal(params, &p); err == nil && p.ProtocolVersion != "" {
			version = p.ProtocolVersion
		}
	}
	return map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": s.name, "version": "1"},
	}
}

func (s *mcpServer) listTools() []map[string]any {
	out := make([]map[string]any, 0, len(s.order))
	for _, name := range s.order {
		t := s.tools[name]
		schema := t.InputSchema
		if schema == nil {
			schema = objectSchema(nil, nil)
		}
		out = append(out, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": schema,
		})
	}
	return out
}

// callTool runs one tool. The split between a JSON-RPC error and an `isError`
// result is deliberate: naming a tool that does not exist is a client bug (RPC
// error), while a tool that ran and refused — bad labels, an unknown id, a
// selector that will not parse — is information the *model* needs, so it comes
// back as a normal result flagged isError with the message in it.
func (s *mcpServer) callTool(ctx context.Context, caller mcpCaller, params json.RawMessage) (any, *jsonRPCError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &jsonRPCError{Code: rpcInvalidParams, Message: "invalid params: " + err.Error()}
		}
	}
	tool, ok := s.tools[p.Name]
	if !ok {
		return nil, &jsonRPCError{
			Code:    rpcInvalidParams,
			Message: fmt.Sprintf("unknown tool %q (have: %s)", p.Name, strings.Join(s.toolNames(), ", ")),
		}
	}
	result, err := tool.Handler(ctx, caller, p.Arguments)
	if err != nil {
		return mcpToolResult(map[string]any{"error": err.Error()}, err.Error(), true), nil
	}
	text, mErr := json.MarshalIndent(result, "", "  ")
	if mErr != nil {
		// The tool worked and we cannot say so — that is ours, not the model's.
		return nil, &jsonRPCError{Code: rpcInternalError, Message: "encode tool result: " + mErr.Error()}
	}
	return mcpToolResult(result, string(text), false), nil
}

// mcpToolResult builds the MCP tool result envelope. Both shapes are filled:
// `content` for clients that read text, `structuredContent` for those that
// read JSON — the model sees the same object either way.
func mcpToolResult(structured any, text string, isError bool) map[string]any {
	out := map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	}
	if structured != nil {
		out["structuredContent"] = structured
	}
	return out
}

// ---------------------------------------------------------------------------
// JSON-RPC wire types
// ---------------------------------------------------------------------------

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

func writeRPC(w http.ResponseWriter, id json.RawMessage, result any, rpcErr *jsonRPCError) {
	w.Header().Set("Content-Type", "application/json")
	// JSON-RPC transport errors still ride a 200: the error is in the envelope,
	// and an HTTP status would make the client retry a request that will fail
	// identically every time.
	_ = json.NewEncoder(w).Encode(jsonRPCResponse{JSONRPC: "2.0", ID: id, Result: result, Error: rpcErr})
}

// objectSchema builds a JSON Schema object for a tool's inputSchema.
func objectSchema(properties map[string]any, required []string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

// decodeArgs unmarshals a tool's arguments strictly: an unknown field is an
// error rather than a silent no-op. A model that calls `memory_search` with
// `selector` instead of `label_selector` must be told, not quietly handed the
// whole project newest-first (§9: malformed input fails loudly).
func decodeArgs(raw json.RawMessage, dst any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Session-token authentication
// ---------------------------------------------------------------------------

// mcpSessionLookup is the narrow read seam the authenticator needs: the session
// row behind the token, for its worker (provenance) and its customer (a
// cross-check). *agentdb.Store satisfies it. nil is legal — provenance then
// carries the session id but no worker name.
type mcpSessionLookup interface {
	GetSession(ctx context.Context, id string) (*agentdb.Session, error)
}

// sessionTokenAuth verifies the per-session JWT and resolves the caller.
//
// secret is the secret the Runner's claims issuer MINTS with, which in agentd is
// AGENTKIT_JWT_SECRET-or-"dev-secret" — deliberately not the (possibly empty)
// API-auth secret. The API's dev-open mode exists so a human can open the demo
// UI without a token; it must not become a way for anything that can reach
// agentd to read a project's memories.
type sessionTokenAuth struct {
	secret   []byte
	sessions mcpSessionLookup
}

func newSessionTokenAuth(secret []byte, sessions mcpSessionLookup) *sessionTokenAuth {
	return &sessionTokenAuth{secret: secret, sessions: sessions}
}

// authenticate implements mcpAuthFunc.
func (a *sessionTokenAuth) authenticate(r *http.Request) (mcpCaller, error) {
	raw := bearerToken(r.Header.Get("Authorization"))
	if raw == "" {
		return mcpCaller{}, fmt.Errorf("%w: no session token (expected the Authorization header to carry ${SESSION_TOKEN})", errMCPUnauthorized)
	}
	claims := jwt.MapClaims{}
	tok, err := jwt.ParseWithClaims(raw, claims, func(*jwt.Token) (any, error) {
		return a.secret, nil
	}, jwt.WithValidMethods([]string{"HS256"}))

	// Expiry is handled below, once we know whether the session still exists.
	// Everything else — a bad signature, a mangled token, the wrong algorithm —
	// is fatal here and now.
	expired := false
	if err != nil || !tok.Valid {
		switch {
		case err != nil && errors.Is(err, jwt.ErrTokenExpired) &&
			!errors.Is(err, jwt.ErrTokenSignatureInvalid) && !errors.Is(err, jwt.ErrTokenMalformed):
			expired = true
		default:
			return mcpCaller{}, fmt.Errorf("%w: session token rejected", errMCPUnauthorized)
		}
	}

	caller := mcpCaller{}
	if v, ok := claims["customer"].(string); ok {
		caller.Project = strings.TrimSpace(v)
	}
	if v, ok := claims["sid"].(string); ok {
		caller.SessionID = strings.TrimSpace(v)
	}
	if caller.Project == "" {
		// A token with no tenancy claim has no project to scope to, and the
		// tools have no project parameter to fall back on. Refuse.
		return mcpCaller{}, fmt.Errorf("%w: session token carries no project", errMCPUnauthorized)
	}

	// Provenance, and the reason an expired token can still be honoured: the
	// session row is the live authority on whether this caller is a real,
	// still-known job of this project.
	sessionKnown := false
	if a.sessions != nil && caller.SessionID != "" {
		sess, err := a.sessions.GetSession(r.Context(), caller.SessionID)
		switch {
		case err != nil:
			// Unknown or unreadable session: not fatal on its own (the store may
			// be the SQLite fallback, which knows nothing of workers), but it
			// cannot rescue an expired token either.
			log.Printf("[mcp] session %s not readable for provenance: %v", caller.SessionID, err)
		case sess.Customer != "" && sess.Customer != caller.Project:
			// The token says one project, the session row says another. Something
			// is badly wrong; refuse rather than pick a side.
			return mcpCaller{}, fmt.Errorf("session token project %q does not match session %q", caller.Project, caller.SessionID)
		default:
			sessionKnown = true
			caller.Worker = sess.Worker
		}
	}

	if expired && !sessionKnown {
		return mcpCaller{}, fmt.Errorf("%w: session token expired", errMCPUnauthorized)
	}
	return caller, nil
}

// bearerToken extracts the credential from an Authorization header value. Both
// "Bearer <jwt>" and a bare "<jwt>" are accepted, because MCP header values in
// session config may only be WHOLE-value ${VAR} references (§4.4) — there is no
// way to write "Bearer ${SESSION_TOKEN}", so the bare form is what actually
// arrives and refusing it would make the core tools unreachable.
func bearerToken(header string) string {
	h := strings.TrimSpace(header)
	if h == "" {
		return ""
	}
	if len(h) >= 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return h
}

// ---------------------------------------------------------------------------
// Session-facing config
// ---------------------------------------------------------------------------

// coreMCPServers is the core tool config handed to every job as
// ComposeJobInput.CoreMCP (§6.2 step 3 — core ∪ project ∪ worker, core
// non-overridable). selfURL is how a session container reaches agentd
// (AGENTKIT_SELF_URL), NOT the public base URL a human browses.
//
// The Authorization value is the whole-value reference `${SESSION_TOKEN}`,
// resolved by the sandbox from the container environment at spawn time (§4.4) —
// no secret is stored, displayed or persisted anywhere in this config.
func coreMCPServers(selfURL string) agentdb.MCPServers {
	base := strings.TrimRight(strings.TrimSpace(selfURL), "/")
	return agentdb.MCPServers{
		coreMCPServerName: {
			URL:     base + coreMCPPath,
			Headers: map[string]string{"Authorization": "${SESSION_TOKEN}"},
		},
	}
}

// sortedStrings is a tiny helper for deterministic log lines.
func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
