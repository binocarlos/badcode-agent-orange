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
	Runner    agentkit.Runner
	Store     agentkit.RunnerStore
	Artifacts artifacts.ArtifactStore // optional; artifact routes 501 if nil
	Identity  IdentityFunc
	Endpoints Endpoints          // zero value -> DefaultEndpoints
	AgentDB   *agentdb.Store     // optional; when set, ListSessions/Messages/QueryEvents/SearchMessages use real DB queries
	ChatClient agentkit.ChatClient // optional; enables titlebot

	// ImageResolver, when set, maps a host "installation" name
	// (createSessionBody.Installation, "" = host default) to a launch image
	// reference, set as CreateSessionRequest.Image.
	// Optional: nil preserves the existing CustomImageID/Policy.BaseImage behavior.
	ImageResolver func(installation string) (imageRef string, err error)
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
	return &Handlers{cfg: cfg}, nil
}

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
	// TODO: an artifact download route (GET by artifact ID, backed by
	// ArtifactStore.Load) is intentionally deferred — add it here when needed.
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
	return m
}
