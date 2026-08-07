package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/binocarlos/badcode-agent-orange"
	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

type createSessionBody struct {
	SessionID     string   `json:"sessionId"`
	Job           string   `json:"job"`
	Persona       string   `json:"persona"`
	Model         string   `json:"model"`
	SystemPrompt  string   `json:"systemPrompt"`
	Tools         []string `json:"tools"`
	Harness       string   `json:"harness"`
	CustomImageID string   `json:"customImageId"`
	Installation  string   `json:"installation"`
	// Name is the OPTIONAL project-unique handle this session may be addressed
	// by afterwards (T7). Set once, here, and never again: there is no rename
	// route and no rename store method — see agentdb.Session.Name.
	Name string `json:"name"`
}

type createSessionResp struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	WorkflowID string `json:"workflowId"`
}

// newID returns a random 32-hex-char id (no external dep).
func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// CreateSession persists the session row (state=creating), provisions via the
// Runner, and returns {id,status,workflowId}. On Runner error it marks the row
// state=error and responds 500 (host owns durable delete via its own store).
func (h *Handlers) CreateSession(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	var body createSessionBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	sid := body.SessionID
	if sid == "" {
		sid = newID()
	}
	// Everything that can reject a name is decided HERE, before MarkCreating
	// registers a create attempt with the Runner: a refused name must leave no
	// progress op, no create guard and no row behind. The duplicate check below
	// is only the fast path — the unique index at insert time is what actually
	// decides, since two racing creates of one name would both pass a SELECT.
	name := strings.TrimSpace(body.Name)
	if name != "" {
		if h.cfg.SessionNames == nil {
			// The sqlite fallback's store has no name column and cannot enforce
			// uniqueness. Refusing is the honest answer; silently creating an
			// unnamed session would hand the caller a name that resolves to
			// nothing.
			http.Error(w, "session names are not configured on this host", http.StatusNotImplemented)
			return
		}
		if id.Customer == "" {
			http.Error(w, "no project in token", http.StatusForbidden)
			return
		}
		if err := agentdb.ValidateSessionName(name); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if _, err := h.cfg.SessionNames.GetSessionByName(r.Context(), id.Customer, name); err == nil {
			http.Error(w, "session name already taken", http.StatusConflict)
			return
		}
	}
	// Resolve installation → image reference when the host has wired an ImageResolver,
	// but only when no explicit CustomImageID is present. When both arrive (the frontend
	// always auto-sends an installation), the caller's custom image must win: leave
	// Image empty so resolveLaunchImage (runner.go) ranks CustomImageID above Image.
	var resolvedImage string
	if h.cfg.ImageResolver != nil && body.CustomImageID == "" {
		ref, err := h.cfg.ImageResolver(body.Installation)
		if err != nil {
			http.Error(w, "installation not available: "+err.Error(), http.StatusBadRequest)
			return
		}
		resolvedImage = ref
	}
	// Provisioning includes a force-pull of the launch image, which can take from
	// seconds to minutes. Rather than block the POST for that whole window, return
	// immediately with status "creating" and provision in the background; the
	// frontend polls GET /session/{id}/status to render download progress (the
	// runner streams image-pull bytes into the per-session progress store).
	//
	// MarkCreating pre-registers the "create" progress op synchronously (before we
	// background the work) so a status poll that races ahead of the goroutine still
	// sees an active op and keeps polling instead of treating the not-yet-running
	// session as settled. Capability-probed so non-runner stubs stay compatible.
	//
	// It runs BEFORE the row is written, and that order is load-bearing: it also
	// registers the create with the Runner so a DELETE landing mid-create can
	// abort it. A DELETE cannot reach us until the row exists (the ownership
	// check 404s without it), so registering first leaves no window in which a
	// delete is possible but invisible to the create.
	if mc, ok := h.cfg.Runner.(interface{ MarkCreating(string) }); ok {
		mc.MarkCreating(sid)
	}

	// Persist the row before provisioning (Runner contract).
	row := &agentdb.Session{
		ID: sid, Customer: id.Customer, Job: body.Job,
		UserEmail: id.UserEmail, Persona: body.Persona, Status: "creating",
		WorkflowID: "agent", Installation: body.Installation,
		CustomImageID: body.CustomImageID,
		Name:          name,
	}
	if name == "" {
		// The unchanged path every existing caller takes: an upsert, which is
		// what makes re-POSTing a session id idempotent.
		_, _ = h.cfg.Store.UpdateSession(r.Context(), row)
	} else if _, err := h.cfg.SessionNames.CreateSession(r.Context(), row); err != nil {
		// A named session is an INSERT, not an upsert: only an INSERT writes
		// Session.Name (`<-:create`) and only an INSERT can trip migration 035's
		// unique index. That also means a named create can never re-label an
		// existing row, which is how "no route renames a session" is enforced
		// rather than merely intended.
		//
		// Reaching the taken branch here means we lost the race the fast-path
		// check above usually wins; the create attempt registered by
		// MarkCreating is left behind, which is a bounded leak for a rare event
		// and strictly better than provisioning a container for a name we cannot
		// keep.
		switch {
		case errors.Is(err, agentdb.ErrSessionNameTaken):
			http.Error(w, "session name already taken", http.StatusConflict)
		case errors.Is(err, agentdb.ErrSessionNameInvalid):
			http.Error(w, err.Error(), http.StatusBadRequest)
		default:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	createReq := agentkit.CreateSessionRequest{
		SessionID:     sid,
		Persona:       body.Persona,
		Customer:      id.Customer,
		Job:           body.Job,
		UserEmail:     id.UserEmail,
		Model:         body.Model,
		SystemPrompt:  body.SystemPrompt,
		Harness:       agentkit.Harness(body.Harness),
		CustomImageID: body.CustomImageID,
		Image:         resolvedImage,
	}
	// There is one kind of session: a chat session launches with the same core
	// tools a dispatched job gets. The project's system prompt, the project ∪
	// worker MCP config and the project base image already reach this create
	// through the Runner's SessionContextProvider (runner.go:366,484-495); the
	// host's core tool server is the one thing that never did, because the host
	// mounts it and the provider knows nothing about it.
	createReq.MCPServers = mergeCoreMCPServers(createReq.MCPServers, h.cfg.CoreMCP)

	// Detach from the request context: it is cancelled when this handler returns,
	// which would abort provisioning the instant we respond.
	bg := context.WithoutCancel(r.Context())
	go func() {
		if _, err := h.cfg.Runner.CreateSession(bg, createReq); err != nil {
			// The session was deleted while we were provisioning it. That is not
			// a failure and there is no row left to mark: the Runner has already
			// destroyed whatever container the create had built (and released its
			// host port), which is the whole point of the abort. Touching the row
			// here would only risk stamping "error" on a re-created session of
			// the same id.
			if errors.Is(err, agentkit.ErrSessionDeleted) {
				log.Printf("httpapi: session %s was deleted while it was being created; create aborted", sid)
				return
			}
			// This goroutine used to be where every create diagnostic died: the
			// error was neither logged nor persisted, and `status = "error"` was
			// all that survived — so the caller's next message took the
			// no-instance-and-no-snapshot path and was told the session was
			// LOST. The Runner now records the reason on the session row
			// (agent_sessions.create_error) before returning, and reads it back
			// on exactly that path. Log it here too: this is the request that
			// asked for the session, and an operator reading agentd's log needs
			// the session id next to the failure.
			log.Printf("httpapi: background create of session %s failed: %v", sid, err)
			// Get-patch-write so we only flip Status and don't clobber the rest of
			// the row (stores do a full replace) — in particular not the
			// create_error the Runner has just written.
			if sess, getErr := h.cfg.Store.GetSession(bg, sid); getErr == nil && sess != nil {
				sess.Status = "error"
				_, _ = h.cfg.Store.UpdateSession(bg, sess)
			}
			return
		}
		if sess, getErr := h.cfg.Store.GetSession(bg, sid); getErr == nil && sess != nil {
			sess.Status = "running"
			_, _ = h.cfg.Store.UpdateSession(bg, sess)
		}
	}()

	// The library has no separate workflow concept, so WorkflowID echoes the
	// session id (the frontend's useAgentSession expects a workflowId field).
	writeJSON(w, createSessionResp{ID: sid, Status: "creating", WorkflowID: sid})
}

// mergeCoreMCPServers layers the host's core tool servers over whatever MCP
// config the create request already carries. Core is written LAST, so it cannot
// be shadowed — the same ordering composition uses for dispatched jobs
// (go/compose.go:436-441, project → worker → core).
//
// The project's and worker's own servers are deliberately NOT merged here. They
// reach the session further down, inside the Runner, from the host's
// SessionContextProvider, and that merge lets the request win a name collision
// (mergeSessionMCPServers, go/runner.go:484-495). Putting core on the request is
// therefore precisely what makes it non-overridable: a project cannot reroute
// memory_search by writing its own `agentkit-core` entry into
// project_settings.mcp_config.
//
// No core servers configured returns req untouched — nil, for every caller
// today — which keeps the sqlite fallback on exactly the old path: the Runner
// treats an empty map as "no MCP config" and never persists one.
//
// The result is always a fresh map: Config.CoreMCP is built once at boot and
// shared by every create, and the Runner is free to hand what it gets to a
// store.
func mergeCoreMCPServers(req, core agentdb.MCPServers) agentdb.MCPServers {
	if len(core) == 0 {
		return req
	}
	merged := make(agentdb.MCPServers, len(req)+len(core))
	for name, cfg := range req {
		merged[name] = cfg
	}
	for name, cfg := range core {
		merged[name] = cfg
	}
	return merged
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
