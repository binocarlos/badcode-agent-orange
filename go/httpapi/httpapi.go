// Package httpapi adapts the agentkit Runner to net/http handlers a host mounts
// under its own authenticated routes. Streaming handlers own the SSE lifecycle.
// See docs/superpowers/specs/2026-06-03-agentkit-integration-design.md.
package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/binocarlos/badcode-agent-orange"
	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/artifacts"
)

// Identity is the authenticated principal the host extracts from a request. The
// handlers merge it onto decoded request bodies (identity wins for tenancy).
type Identity struct {
	UserEmail string
	Customer  string // the tenant the principal may act as
	// SessionScope, when non-empty, narrows this credential to a single
	// session id. It is what an embed token carries: a browser inside a
	// third-party page may stream and message exactly the session it was
	// minted for, and nothing else in the project. Empty means unrestricted
	// within Customer — the shape every console JWT and API key has.
	SessionScope string
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

	// Memories backs GET /agent/memories, the §7.6 memory read path. Same
	// defaulting rule as Workers: auto-filled from AgentDB, 501 without one.
	// Read-only by design — memories are appended by workers, never by the UI.
	Memories MemoryStore

	// MemoryEmbedder, when set, supplies the query-side embedding for that
	// route's semantic leg. Nil is a supported deployment: search degrades to
	// keyword+recency with the same result shape (§7.6.5).
	MemoryEmbedder MemoryEmbedder

	// Topologies backs the /agent/topologies routes (T2): the built-in
	// catalogue, preview, and the atomic apply. Same defaulting rule as
	// Workers: auto-filled from AgentDB, 501 without one.
	Topologies TopologyStore

	// SessionNames backs the optional `name` on POST /agent/session and the
	// by-name lookup routes (sessions_byname.go). Same defaulting rule as
	// Workers: auto-filled from AgentDB, and 501 without one — the sqlite
	// fallback's store has no name column and no unique index to enforce with.
	//
	// A host that sets this explicitly must point it at the SAME store as Store:
	// a named create inserts the row through here and everything afterwards
	// reads it through Store.
	SessionNames SessionNameStore

	// ArtifactPaths backs the ?path= leg of the by-name artifact routes
	// (artifacts_download.go). Same defaulting rule as Workers: auto-filled
	// from AgentDB, 501 without one — the sqlite fallback's artifact index is
	// an in-process map with nothing to query by path.
	ArtifactPaths ArtifactPathStore

	// Catalogue backs GET /agent/images and GET /agent/skills (design B4) —
	// read-only, the browser-side mirror of the image/skill MCP tools. Same
	// defaulting rule as Workers: auto-filled from AgentDB, 501 without one.
	Catalogue CatalogueStore
}

// Tenancy contract
//
// Every handler that acts on an existing session by ID — Status, Cancel,
// GetSession, DeleteSession, Restore, Snapshot, Archive, Messages, QueryEvents,
// Stream, Reconnect, SendMessage and the artifact routes — now verifies that the
// authenticated principal owns that session, via ownsSession. It loads the row
// from AgentDB (or Store) and answers 404 — never 403, which would confirm the
// session exists — when Identity.Customer does not match.
//
// This used to be delegated entirely to the host, and the shipped host (agentd)
// did not do it: seven of those routes called identify() for authentication and
// then discarded the identity. The check moved in here because it is a
// precondition for issuing a second kind of credential (project API keys and
// session-scoped embed tokens), and because a host cannot enforce it without
// duplicating this package's route table. Hosts that already authorize session
// IDs upstream keep working: the check is idempotent and skipped whenever either
// side's customer is empty, which is also what keeps dev-open mode alive.
//
// Identity.SessionScope is enforced in the same place: a credential carrying it
// may touch only that one session id, whatever its Customer says.
//
// The by-name routes (sessions_byname.go) reach the same two rules through
// resolveSessionByName instead: the store query is scoped to Identity.Customer
// so a foreign name never matches, and the scope leg is applied to the resolved
// id. Absent, malformed and foreign names are one indistinguishable 404 — a
// name is chosen by whoever created it and is far more guessable than a uuid,
// so a distinguishable answer here would be a project-membership oracle.
//
// ListSessions and SearchMessages still return an empty array without an
// AgentDB: the library has no catalog to enumerate, so the host overrides those.
// Identity is also *stamped* onto writes — CreateSession and SendMessage — where
// the principal's Customer/UserEmail win for tenancy.

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
	if cfg.Memories == nil && cfg.AgentDB != nil {
		cfg.Memories = cfg.AgentDB
	}
	if cfg.Topologies == nil && cfg.AgentDB != nil {
		cfg.Topologies = cfg.AgentDB
	}
	if cfg.Catalogue == nil && cfg.AgentDB != nil {
		cfg.Catalogue = cfg.AgentDB
	}
	if cfg.SessionNames == nil && cfg.AgentDB != nil {
		cfg.SessionNames = cfg.AgentDB
	}
	if cfg.ArtifactPaths == nil && cfg.AgentDB != nil {
		cfg.ArtifactPaths = cfg.AgentDB
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
// Rationale stays empty here: humanEdit is the write for a request that carried
// no reason. A request that DID carry one goes through humanEditBecause.
func humanEdit() agentdb.ConfigWrite { return agentdb.ConfigWrite{} }

// humanEditBecause is humanEdit plus the reason the operator typed — the
// optional `rationale` every configuration route now accepts (design B3 / K2),
// in the body on writes and as `?rationale=` on the deletes, which carry none.
//
// Optional, not required: §15.5 demands a rationale only of the two prompt
// writes, which have their own tool path. Empty stays empty rather than
// becoming a placeholder — "(no reason given)" is a thing the changelog says,
// not a thing the config log stores.
func humanEditBecause(rationale string) agentdb.ConfigWrite {
	cw := humanEdit()
	cw.Rationale = strings.TrimSpace(rationale)
	return cw
}

// rationaleParam reads `?rationale=` — the only way a DELETE, which has no
// body, can say why.
func rationaleParam(r *http.Request) string { return r.URL.Query().Get("rationale") }

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

	// The by-name lookup (T7). A separate pattern rather than an overload of
	// GetSession: a name and a uuid must never be interchangeable in one path
	// segment, or "hypothesis-a" and a mistyped id become the same request.
	GetSessionByName string // "GET /agent/sessions/by-name/{name}"
	// The by-name artifact routes (T8): the same two reads as Artifacts and
	// the download route, addressed by the name the integrator chose.
	SessionArtifactsByName    string // "GET /agent/sessions/by-name/{name}/artifacts"
	SessionArtifactFileByName string // "GET /agent/sessions/by-name/{name}/artifacts/file"

	Artifacts        string // "GET /agent/session/{id}/artifacts"
	CreateArtifact   string // "POST /agent/session/{id}/artifacts"
	Upload           string // "POST /agent/session/{id}/upload"
	DownloadArtifact string // "GET /agent/artifacts/{id}/download"
	Snapshot         string // "POST /agent/session/{id}/snapshot"
	Archive          string // "POST /agent/session/{id}/archive"
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
	// Memory (§7.6) — read-only; the project comes from the JWT.
	ListMemories string // "GET /agent/memories"
	// Topologies (T2). The catalogue is read-only; preview computes and writes
	// nothing; apply is the one write, atomic in the store.
	ListTopologies  string // "GET /agent/topologies"
	PreviewTopology string // "POST /agent/topologies/preview"
	ApplyTopology   string // "POST /agent/topologies/apply"
	// The image/skill catalogues (B4) — read-only; the project comes from the
	// JWT. There is no write counterpart: both catalogues are append-only and
	// are written only from inside a session (§13.4, §14.2).
	ListImages   string // "GET /agent/images"
	ListSkills   string // "GET /agent/skills"
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

	GetSessionByName:          "GET /agent/sessions/by-name/{name}",
	SessionArtifactsByName:    "GET /agent/sessions/by-name/{name}/artifacts",
	SessionArtifactFileByName: "GET /agent/sessions/by-name/{name}/artifacts/file",

	Artifacts:        "GET /agent/session/{id}/artifacts",
	CreateArtifact:   "POST /agent/session/{id}/artifacts",
	Upload:           "POST /agent/session/{id}/upload",
	DownloadArtifact: "GET /agent/artifacts/{id}/download",
	Snapshot:         "POST /agent/session/{id}/snapshot",
	Archive:          "POST /agent/session/{id}/archive",
	ListWorkers:      "GET /agent/workers",
	GetWorker:        "GET /agent/workers/{name}",
	PutWorker:        "PUT /agent/workers/{name}",
	DeleteWorker:     "DELETE /agent/workers/{name}",

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
	ListMemories:       "GET /agent/memories",
	ListTopologies:     "GET /agent/topologies",
	PreviewTopology:    "POST /agent/topologies/preview",
	ApplyTopology:      "POST /agent/topologies/apply",
	ListImages:         "GET /agent/images",
	ListSkills:         "GET /agent/skills",
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
		e.ListMemories:      h.ListMemories,
		e.ListTopologies:    h.ListTopologies,
		e.PreviewTopology:   h.PreviewTopology,
		e.ApplyTopology:     h.ApplyTopologyHandler,
		e.ListImages:        h.ListImages,
		e.ListSkills:        h.ListSkills,
		e.GetSessionByName:  h.GetSessionByName,

		e.DownloadArtifact:          h.DownloadArtifact,
		e.SessionArtifactsByName:    h.SessionArtifactsByName,
		e.SessionArtifactFileByName: h.SessionArtifactFileByName,
	} {
		if pattern != "" {
			m.HandleFunc(pattern, handler)
		}
	}
	return m
}
