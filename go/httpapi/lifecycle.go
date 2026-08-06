package httpapi

import (
	"context"
	"net/http"

	"github.com/binocarlos/badcode-agent-orange"
)

// statusResp is the JSON shape the frontend useAgentSession hook expects.
// Fields: sandboxState (container state polling), activeQuery (reconnection),
// sandboxAddress (workspace file proxying by hosts).
type statusResp struct {
	SessionID      string               `json:"sessionId"`
	SandboxState   string               `json:"sandboxState"`
	HasSnapshot    bool                 `json:"has_snapshot"`
	ActiveQuery    *activeQuery         `json:"activeQuery"`
	SandboxAddress string               `json:"sandboxAddress,omitempty"`
	Progress       *agentkit.OpProgress `json:"progress,omitempty"`
}

type activeQuery struct {
	QueryID string `json:"queryId"`
}

// sessionResp is the JSON shape the frontend useAgentSession.resumeSession expects.
type sessionResp struct {
	ID       string `json:"id"`
	Customer string `json:"customer"`
	Job      string `json:"job"`
	Persona  string `json:"persona"`
	Status   string `json:"status"`
	// CreateError is why the session failed to start; omitted when it didn't.
	// A `status: "error"` with nothing beside it is the same unreachable
	// diagnostic the SSE path had.
	CreateError string `json:"create_error,omitempty"`
}

// Status reports combined runtime + durable state for a session.
func (h *Handlers) Status(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	sid := r.PathValue("id")
	if !h.ownsSession(w, r, id, sid) {
		return
	}
	ref := agentkit.SessionRef{SessionID: sid}
	status, err := h.cfg.Runner.Status(r.Context(), ref)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if status == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	resp := statusResp{
		SessionID:      status.SessionID,
		SandboxState:   status.RuntimeState,
		HasSnapshot:    status.HasSnapshot,
		SandboxAddress: status.SandboxAddress,
	}
	if status.ActiveQueryID != "" {
		resp.ActiveQuery = &activeQuery{QueryID: status.ActiveQueryID}
	}
	resp.Progress = status.Progress
	writeJSON(w, resp)
}

// Cancel cancels the in-flight query without tearing the instance down.
func (h *Handlers) Cancel(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	sid := r.PathValue("id")
	if !h.ownsSession(w, r, id, sid) {
		return
	}
	ref := agentkit.SessionRef{SessionID: sid}
	if err := h.cfg.Runner.Stop(r.Context(), ref); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "cancelled"})
}

// GetSession fetches the persisted session row from the store.
func (h *Handlers) GetSession(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	sid := r.PathValue("id")
	// GetSession does its own row-loading tenancy check below rather than going
	// through ownsSession (it would double-load the row), so the session-scope
	// leg has to be applied explicitly here.
	if !scopeAllows(id, sid) {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	// When AgentDB is set, return the full session row (includes title, metadata, etc.)
	if h.cfg.AgentDB != nil {
		session, err := h.cfg.AgentDB.GetSession(r.Context(), sid)
		if err != nil {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		// Same defense-in-depth tenancy check as the Store branch below: the row
		// now carries `composed_prompt`, which contains the project's system
		// prompt and its memory briefings — never serve it across projects.
		// 404, not 403, so existence does not leak.
		if session.Customer != "" && id.Customer != "" && session.Customer != id.Customer {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]any{
			"id":             session.ID,
			"created_at":     session.CreatedAt,
			"updated_at":     session.UpdatedAt,
			"user_email":     session.UserEmail,
			"customer":       session.Customer,
			"job":            session.Job,
			"title":          session.Title,
			"workflow_id":    session.WorkflowID,
			"persona":        session.Persona,
			"status":         session.Status,
			"current_node":   session.CurrentNode,
			"metadata":       session.Metadata,
			"snapshot_state": session.SnapshotState,
			// Why this session failed to start, when it did. A UI showing
			// `status: "error"` with no explanation is the same defect one
			// layer up from the SSE one — the operator can see that something
			// broke and not what. "" whenever the create succeeded.
			"create_error": session.CreateError,
			// Composition provenance (§6.2, C2). `worker` names the worker this
			// job ran as ("" for a plain human session) and `composed_prompt` is
			// the EXACT system prompt ComposeJob produced for it — preamble +
			// project prompt + worker prompt + memory briefing sections. It is
			// the only way to prove from outside that a memory written by one
			// job reached the next job's prompt (§7.4), which is what G1 asserts.
			"worker":          session.Worker,
			"composed_prompt": session.ComposedPrompt,
		})
		return
	}
	sess, err := h.cfg.Store.GetSession(r.Context(), sid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if sess == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	// Defense-in-depth tenancy check: the host is the authoritative gate (see the
	// package doc in httpapi.go), but since we already loaded the row, reject a
	// cross-tenant read here. Respond 404 (not 403) so we don't leak existence.
	if sess.Customer != "" && id.Customer != "" && sess.Customer != id.Customer {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	// Return camelCase JSON so the browser useAgentSession hook can parse it.
	writeJSON(w, sessionResp{
		ID:          sess.ID,
		Customer:    sess.Customer,
		Job:         sess.Job,
		Persona:     sess.Persona,
		Status:      sess.Status,
		CreateError: sess.CreateError,
	})
}

// DeleteSession destroys the runtime instance for a session. Tenancy: the
// caller's customer must own the session (same check as GetSession).
func (h *Handlers) DeleteSession(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	sid := r.PathValue("id")
	if !h.ownsSession(w, r, id, sid) {
		return
	}
	ref := agentkit.SessionRef{SessionID: sid}
	if err := h.cfg.Runner.Destroy(r.Context(), ref); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Remove the session row too — a deleted session must not linger in
	// listings. Runner.Destroy only tears down the runtime instance.
	if h.cfg.AgentDB != nil {
		_ = h.cfg.AgentDB.DeleteSession(r.Context(), sid)
	} else if del, ok := h.cfg.Store.(interface {
		DeleteSession(ctx context.Context, id string) error
	}); ok {
		_ = del.DeleteSession(r.Context(), sid)
	}
	w.WriteHeader(http.StatusNoContent)
}

// ownsSession loads the session row and enforces the caller's customer scope,
// mirroring the GetSession tenancy check. Writes 404 and returns false on a
// missing or cross-tenant session (404 not 403, so existence isn't leaked).
//
// It is the single authorization gate for every session-by-ID route; see the
// tenancy contract in httpapi.go.
func (h *Handlers) ownsSession(w http.ResponseWriter, r *http.Request, id Identity, sid string) bool {
	// A session-scoped credential (an embed token) is confined to one id before
	// we even look the row up — no store round-trip, and no way to probe which
	// sibling sessions exist by timing the response.
	if !scopeAllows(id, sid) {
		http.Error(w, "not found", http.StatusNotFound)
		return false
	}
	var customer string
	if h.cfg.AgentDB != nil {
		sess, err := h.cfg.AgentDB.GetSession(r.Context(), sid)
		if err != nil || sess == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return false
		}
		customer = sess.Customer
	} else if h.cfg.Store != nil {
		sess, err := h.cfg.Store.GetSession(r.Context(), sid)
		if err != nil || sess == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return false
		}
		customer = sess.Customer
	}
	if customer != "" && id.Customer != "" && customer != id.Customer {
		http.Error(w, "not found", http.StatusNotFound)
		return false
	}
	return true
}

// scopeAllows reports whether a credential may act on this session id. An empty
// SessionScope is unrestricted (within Customer); a non-empty one must match
// exactly.
func scopeAllows(id Identity, sid string) bool {
	return id.SessionScope == "" || id.SessionScope == sid
}

// snapshotResp is the JSON shape returned by the Snapshot handler.
type snapshotResp struct {
	Kind string            `json:"kind"`
	Ref  string            `json:"ref"`
	Meta map[string]string `json:"meta,omitempty"`
}

// Snapshot forces an archive of the session now and returns the durable handle.
// This is the explicit archive endpoint for integration tests and app publishing.
func (h *Handlers) Snapshot(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	sid := r.PathValue("id")
	if !h.ownsSession(w, r, id, sid) {
		return
	}
	ref := agentkit.SessionRef{SessionID: sid}
	handle, err := h.cfg.Runner.Snapshot(r.Context(), ref)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, snapshotResp{
		Kind: handle.Kind,
		Ref:  handle.Ref,
		Meta: handle.Meta,
	})
}

// Archive snapshots the session to the registry and then destroys its running
// container (a cold stop). Owner-accessible; restore later via Restore. Returns
// the durable snapshot handle.
func (h *Handlers) Archive(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	sid := r.PathValue("id")
	if !h.ownsSession(w, r, id, sid) {
		return
	}
	ref := agentkit.SessionRef{SessionID: sid}
	handle, err := h.cfg.Runner.Snapshot(r.Context(), ref)
	if err != nil {
		http.Error(w, "snapshot: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.cfg.Runner.Destroy(r.Context(), ref); err != nil {
		http.Error(w, "destroy: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, snapshotResp{Kind: handle.Kind, Ref: handle.Ref, Meta: handle.Meta})
}

// Restore brings a cold/archived session back to running (restoring from its
// snapshot) and returns the runtime handle.
func (h *Handlers) Restore(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	sid := r.PathValue("id")
	if !h.ownsSession(w, r, id, sid) {
		return
	}
	ref := agentkit.SessionRef{SessionID: sid}
	handle, err := h.cfg.Runner.Resume(r.Context(), ref)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if handle == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, handle)
}
