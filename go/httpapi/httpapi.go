// Package httpapi adapts the agentkit Runner to net/http handlers a host mounts
// under its own authenticated routes. Streaming handlers own the SSE lifecycle.
// See docs/superpowers/specs/2026-06-03-agentkit-integration-design.md.
package httpapi

import (
	"errors"
	"net/http"

	"github.com/binocarlos/badcode-agent-orange"
	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/artifacts"
)

// Identity is the authenticated principal the host extracts from a request. The
// handlers merge it onto decoded request bodies (identity wins for tenancy).
type Identity struct {
	UserEmail string
	Customer  string // the tenant the principal may act as
}

// IdentityFunc resolves the principal from a request. The host reads its own
// JWT/session here. Returning an error makes the handler respond 401.
type IdentityFunc func(*http.Request) (Identity, error)

// Config constructs the handler set.
type Config struct {
	Runner     agentkit.Runner
	Store      agentkit.RunnerStore
	Artifacts  artifacts.ArtifactStore // optional; artifact routes 501 if nil
	Identity   IdentityFunc
	Endpoints  Endpoints           // zero value -> DefaultEndpoints
	AgentDB    *agentdb.Store      // optional; when set, ListSessions/Messages/QueryEvents/SearchMessages use real DB queries
	ChatClient agentkit.ChatClient // optional; enables titlebot

	// ProjectSettings backs GET/PUT /agent/project-settings. Left nil it is
	// filled from AgentDB by New(); with neither set those routes answer 501.
	ProjectSettings ProjectSettingsStore
	// Events backs the event/subscription/delivery routes (spec §8). Leave nil
	// and it defaults to AgentDB when that is set; nil with no AgentDB makes
	// those routes 501.
	Events EventStore

	// ProjectTokenIssuer, when set, enables POST /agent/project-token — the
	// long-lived project credential a headless poster uses to reach
	// POST /agent/events without a browser login (§8.5). Nil → 501.
	ProjectTokenIssuer ProjectTokenIssuer

	// ImageResolver, when set, maps a host "installation" name
	// (createSessionBody.Installation, "" = host default) to a launch image
	// reference, set as CreateSessionRequest.Image.
	// Optional: nil preserves the existing CustomImageID/Policy.BaseImage behavior.
	ImageResolver func(installation string) (imageRef string, err error)

	// Workers backs the /agent/workers CRUD routes. Left nil it is auto-filled
	// from AgentDB in New(); nil with no AgentDB (the SQLite fallback) makes the
	// routes 501. Set it explicitly to substitute a host store.
	Workers WorkersStore

	// Schedules backs the /agent/schedules CRUD routes (§8.6). Same defaulting
	// rule as Workers: auto-filled from AgentDB, 501 without one.
	Schedules ScheduleStore

	// ConfigLog backs GET /agent/config-events, the §15.10 changelog's read
	// path. Same defaulting rule as Workers: auto-filled from AgentDB, 501
	// without one. There is no write counterpart by design — config events are
	// written only as the shadow of a real mutation (§15.4).
	ConfigLog ConfigLogStore

	// Attention backs GET /agent/attention-requests, the Desk's Asks stack
	// (design B1). Same defaulting rule as Workers: auto-filled from AgentDB,
	// 501 without one. Read-only by design — see attention.go.
	Attention AttentionStore

	// Topologies backs the /agent/topologies routes (T2): the built-in
	// catalogue, preview, and the atomic apply. Same defaulting rule as
	// Workers: auto-filled from AgentDB, 501 without one.
	Topologies TopologyStore
}

// Tenancy contract
//
// Handlers that act on an existing session by ID — Status, Cancel, GetSession,
// DeleteSession, Restore, Messages, QueryEvents, Stream, Reconnect, and the
// artifact routes — do NOT verify that the authenticated principal owns that
// session. The host owns the durable session catalog and MUST authorize the
// session ID for the principal (e.g. in its auth middleware or route layer)
// before the request reaches these handlers. This is why ListSessions and
// SearchMessages return an empty array here: the library has no catalog to
// enumerate or authorize against, so the host overrides those routes.
//
// Identity is instead *stamped* onto writes — CreateSession and SendMessage —
// where the principal's Customer/UserEmail win for tenancy. GetSession adds a
// cheap defense-in-depth tenant check because it already loads the row, but that
// is a backstop, not the primary authorization boundary.

// Handlers is the mountable handler set.
type Handlers struct {
	cfg Config
}

// New validates config and returns the handler set.
func New(cfg Config) (*Handlers, error) {
	if cfg.Runner == nil {
		return nil, errors.New("httpapi: Runner is required")
	}
	if cfg.Store == nil {
		return nil, errors.New("httpapi: Store is required")
	}
	if cfg.Identity == nil {
		return nil, errors.New("httpapi: Identity func is required")
	}
	if cfg.Endpoints == (Endpoints{}) {
		cfg.Endpoints = DefaultEndpoints
	}
	// Optional stores default to the AgentDB when the host supplied one. The
	// explicit nil check matters: assigning a nil *agentdb.Store would produce a
	// non-nil interface and defeat the 501 guard.
	if cfg.ProjectSettings == nil && cfg.AgentDB != nil {
		cfg.ProjectSettings = cfg.AgentDB
	}
	if cfg.Workers == nil && cfg.AgentDB != nil {
		cfg.Workers = cfg.AgentDB
	}
	if cfg.Schedules == nil && cfg.AgentDB != nil {
		cfg.Schedules = cfg.AgentDB
	}
	if cfg.ConfigLog == nil && cfg.AgentDB != nil {
		cfg.ConfigLog = cfg.AgentDB
	}
	if cfg.Attention == nil && cfg.AgentDB != nil {
		cfg.Attention = cfg.AgentDB
	}
	if cfg.Topologies == nil && cfg.AgentDB != nil {
		cfg.Topologies = cfg.AgentDB
	}
	// The event routes ride on the same database as the rich read paths unless
	// a host deliberately supplies its own store.
	if cfg.Events == nil && cfg.AgentDB != nil {
		cfg.Events = cfg.AgentDB
	}
	return &Handlers{cfg: cfg}, nil
}

// humanEdit is the ConfigWrite every configuration mutation made over HTTP
// carries: no acting worker and no acting session. That emptiness is the
// record, not an omission — §15.2 says a human/UI/API edit logs no actor,
// because who was at the keyboard is the login audit's business, not the config
// log's. Workers acting through their MCP tools supply their own actor.
//
// Rationale stays empty here too: no request body on these routes carries one.
// The two writes that REQUIRE a rationale (§15.5) are the prompt writes, which
// have their own route set (H1) and must thread it from the body.
func humanEdit() agentdb.ConfigWrite { return agentdb.ConfigWrite{} }

// identify runs the host's extractor; on error writes 401 and returns ok=false.
func (h *Handlers) identify(w http.ResponseWriter, r *http.Request) (Identity, bool) {
	id, err := h.cfg.Identity(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return Identity{}, false
	}
	return id, true
}

// Endpoints holds the go1.22 ServeMux route patterns. Override to remount under a
// different shape. Patterns use {id} wildcards read via r.PathValue("id").
type Endpoints struct {
	CreateSession  string // "POST /agent/session"
	SendMessage    string // "POST /agent/session/{id}/message"
	Stream         string // "GET /agent/session/{id}/stream"
	Reconnect      string // "GET /agent/session/{id}/reconnect"
	Cancel         string // "POST /agent/session/{id}/cancel"
	Status         string // "GET /agent/session/{id}/status"
	GetSession     string // "GET /agent/session/{id}"
	DeleteSession  string // "DELETE /agent/session/{id}"
	Restore        string // "POST /agent/session/{id}/restore"
	Messages       string // "GET /agent/session/{id}/messages"
	QueryEvents    string // "GET /agent/session/{id}/query-events"
	ListSessions   string // "GET /agent/sessions"
	SearchMessages string // "GET /agent/messages/search"
	Artifacts      string // "GET /agent/session/{id}/artifacts"
	CreateArtifact string // "POST /agent/session/{id}/artifacts"
	Upload         string // "POST /agent/session/{id}/upload"
	Snapshot       string // "POST /agent/session/{id}/snapshot"
	Archive        string // "POST /agent/session/{id}/archive"
	// Project settings (§5) — project inferred from the JWT, never the path.
	GetProjectSettings string // "GET /agent/project-settings"
	PutProjectSettings string // "PUT /agent/project-settings"
	// Events & routing (§8). Subscriptions/Subscription and Events are
	// multi-method: the handler switches on r.Method.
	IngestEvent   string // "POST /agent/events"
	ListEvents    string // "GET /agent/events"
	Subscriptions string // "/agent/subscriptions"       (GET list, POST create)
	Subscription  string // "/agent/subscriptions/{id}"  (GET, PUT, DELETE)
	Deliveries    string // "GET /agent/deliveries"
	ProjectToken  string // "POST /agent/project-token"
	// Schedules (§8.6). Both are multi-method: the handler switches on r.Method.
	Schedules string // "/agent/schedules"      (GET list, POST create)
	Schedule  string // "/agent/schedules/{id}" (GET, PUT, DELETE)
	// The config log (§15.10) — read-only; the project comes from the JWT.
	ConfigEvents string // "GET /agent/config-events"
	// Attention requests (design B1) — read-only; the project comes from the JWT.
	AttentionRequests string // "GET /agent/attention-requests"
	// Topologies (T2). The catalogue is read-only; preview computes and writes
	// nothing; apply is the one write, atomic in the store.
	ListTopologies  string // "GET /agent/topologies"
	PreviewTopology string // "POST /agent/topologies/preview"
	ApplyTopology   string // "POST /agent/topologies/apply"
	// TODO: an artifact download route (GET by artifact ID, backed by
	// ArtifactStore.Load) is intentionally deferred — add it here when needed.
	ListWorkers  string // "GET /agent/workers"
	GetWorker    string // "GET /agent/workers/{name}"
	PutWorker    string // "PUT /agent/workers/{name}"
	DeleteWorker string // "DELETE /agent/workers/{name}"
}

// DefaultEndpoints is the canonical route layout.
var DefaultEndpoints = Endpoints{
	CreateSession:  "POST /agent/session",
	SendMessage:    "POST /agent/session/{id}/message",
	Stream:         "GET /agent/session/{id}/stream",
	Reconnect:      "GET /agent/session/{id}/reconnect",
	Cancel:         "POST /agent/session/{id}/cancel",
	Status:         "GET /agent/session/{id}/status",
	GetSession:     "GET /agent/session/{id}",
	DeleteSession:  "DELETE /agent/session/{id}",
	Restore:        "POST /agent/session/{id}/restore",
	Messages:       "GET /agent/session/{id}/messages",
	QueryEvents:    "GET /agent/session/{id}/query-events",
	ListSessions:   "GET /agent/sessions",
	SearchMessages: "GET /agent/messages/search",
	Artifacts:      "GET /agent/session/{id}/artifacts",
	CreateArtifact: "POST /agent/session/{id}/artifacts",
	Upload:         "POST /agent/session/{id}/upload",
	Snapshot:       "POST /agent/session/{id}/snapshot",
	Archive:        "POST /agent/session/{id}/archive",
	ListWorkers:    "GET /agent/workers",
	GetWorker:      "GET /agent/workers/{name}",
	PutWorker:      "PUT /agent/workers/{name}",
	DeleteWorker:   "DELETE /agent/workers/{name}",

	GetProjectSettings: "GET /agent/project-settings",
	PutProjectSettings: "PUT /agent/project-settings",
	IngestEvent:        "POST /agent/events",
	ListEvents:         "GET /agent/events",
	Subscriptions:      "/agent/subscriptions",
	Subscription:       "/agent/subscriptions/{id}",
	Deliveries:         "GET /agent/deliveries",
	ProjectToken:       "POST /agent/project-token",
	Schedules:          "/agent/schedules",
	Schedule:           "/agent/schedules/{id}",
	ConfigEvents:       "GET /agent/config-events",
	AttentionRequests:  "GET /agent/attention-requests",
	ListTopologies:     "GET /agent/topologies",
	PreviewTopology:    "POST /agent/topologies/preview",
	ApplyTopology:      "POST /agent/topologies/apply",
}

// Mux registers every handler on a fresh *http.ServeMux. Mount it under your
// auth middleware: mux.Handle("/", authMW(api.Mux())) or framework equivalent.
func (h *Handlers) Mux() *http.ServeMux {
	m := http.NewServeMux()
	e := h.cfg.Endpoints
	// Session lifecycle
	m.HandleFunc(e.CreateSession, h.CreateSession)
	m.HandleFunc(e.SendMessage, h.SendMessage)
	m.HandleFunc(e.Status, h.Status)
	m.HandleFunc(e.Cancel, h.Cancel)
	m.HandleFunc(e.GetSession, h.GetSession)
	m.HandleFunc(e.DeleteSession, h.DeleteSession)
	m.HandleFunc(e.Restore, h.Restore)
	// Streaming
	m.HandleFunc(e.Stream, h.Stream)
	m.HandleFunc(e.Reconnect, h.Reconnect)
	// History
	m.HandleFunc(e.Messages, h.Messages)
	m.HandleFunc(e.QueryEvents, h.QueryEvents)
	m.HandleFunc(e.ListSessions, h.ListSessions)
	m.HandleFunc(e.SearchMessages, h.SearchMessages)
	// Artifacts
	m.HandleFunc(e.Artifacts, h.Artifacts)
	m.HandleFunc(e.CreateArtifact, h.CreateArtifact)
	m.HandleFunc(e.Upload, h.Upload)
	// Snapshot (archive)
	if e.Snapshot != "" {
		m.HandleFunc(e.Snapshot, h.Snapshot)
	}
	if e.Archive != "" {
		m.HandleFunc(e.Archive, h.Archive)
	}
	// Project settings
	if e.GetProjectSettings != "" {
		m.HandleFunc(e.GetProjectSettings, h.GetProjectSettings)
	}
	if e.PutProjectSettings != "" {
		m.HandleFunc(e.PutProjectSettings, h.PutProjectSettings)
	}
	// Workers (spec 02-workers §6.5). Guarded like Snapshot/Archive so a host
	// that overrides Endpoints without these fields does not panic on an empty
	// route pattern.
	if e.ListWorkers != "" {
		m.HandleFunc(e.ListWorkers, h.ListWorkers)
	}
	if e.GetWorker != "" {
		m.HandleFunc(e.GetWorker, h.GetWorker)
	}
	if e.PutWorker != "" {
		m.HandleFunc(e.PutWorker, h.PutWorker)
	}
	if e.DeleteWorker != "" {
		m.HandleFunc(e.DeleteWorker, h.DeleteWorker)
	}
	// Events & routing — each guarded so a host can unmount one by blanking it.
	for pattern, handler := range map[string]http.HandlerFunc{
		e.IngestEvent:       h.IngestEvent,
		e.ListEvents:        h.ListEvents,
		e.Subscriptions:     h.Subscriptions,
		e.Subscription:      h.Subscription,
		e.Deliveries:        h.ListDeliveries,
		e.ProjectToken:      h.ProjectToken,
		e.Schedules:         h.Schedules,
		e.Schedule:          h.Schedule,
		e.ConfigEvents:      h.ListConfigEvents,
		e.AttentionRequests: h.ListAttentionRequests,
		e.ListTopologies:    h.ListTopologies,
		e.PreviewTopology:   h.PreviewTopology,
		e.ApplyTopology:     h.ApplyTopologyHandler,
	} {
		if pattern != "" {
			m.HandleFunc(pattern, handler)
		}
	}
	return m
}
