package httpapi

// Session names (T7 of design/2026-08-06-embeddable-agent-orange.md).
//
//	GET /agent/sessions/by-name/{name}
//	  auth : project API key or console JWT, and — deliberately — an embed token
//	  200  : the session row, minus its composed prompt (see sessionByNameResp)
//	  403  : the credential carries no project, so the name cannot be scoped
//	  404  : no such name in this project, WHATEVER the reason
//	  501  : no name store wired (the sqlite fallback)
//
// The point of names is that an embedding application never has to persist a
// session uuid it did not mint: it chose `hypothesis-a`, it keeps
// `hypothesis-a`, and it resolves it here. Everything else in this package
// addresses sessions by id and stays that way — this is a lookup, not a second
// addressing scheme.

import (
	"context"
	"errors"
	"net/http"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// SessionNameStore is the slice of agentdb.Store the naming routes need.
//
// It carries the INSERT as well as the lookup, and that pairing is the whole
// design rather than convenience. A name can only be written when the row is
// created (agentdb.Session.Name is tagged `<-:create`, so no UPDATE this store
// emits carries the column), and only an INSERT can trip migration 035's unique
// index — which is the authority on whether a name is free, because two racing
// creates would both pass any prior SELECT. Routing a named create through the
// ordinary UpdateSession upsert would therefore lose the name on one class of
// store and surface a duplicate as an untyped error on the other.
//
// Note what is NOT here: no rename and no by-name delete. A name handed to a
// third party is a promise — an iframe URL, a schedule target, a row in someone
// else's database — and the seam is shaped so no handler can break it.
type SessionNameStore interface {
	CreateSession(ctx context.Context, sess *agentdb.Session) (*agentdb.Session, error)
	GetSessionByName(ctx context.Context, customer, name string) (*agentdb.Session, error)
}

// The concrete store must always satisfy the seam.
var _ SessionNameStore = (*agentdb.Store)(nil)

// sessionByNameResp is deliberately NARROWER than GET /agent/session/{id}'s
// body: it omits `composed_prompt`. This is the one session route an embed token
// is meant to reach (T12's page must resolve a name to an id before it can mount
// a chat), and a composed prompt is the project's system prompt plus its memory
// briefings — not something to hand a browser sitting inside somebody else's
// page. A console client that wants the full row still has the by-id route.
type sessionByNameResp struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Customer string `json:"customer"`
	Job      string `json:"job,omitempty"`
	Persona  string `json:"persona,omitempty"`
	Title    string `json:"title,omitempty"`
	Status   string `json:"status"`
	// Why this session failed to start, when it did — the same diagnostic
	// GET /agent/session/{id} carries, for the same reason: a client rendering
	// `status: "error"` with nothing beside it can see that something broke and
	// not what.
	CreateError string `json:"create_error,omitempty"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

// GetSessionByName serves GET /agent/sessions/by-name/{name}.
func (h *Handlers) GetSessionByName(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	sess, ok := h.resolveSessionByName(w, r, id, r.PathValue("name"))
	if !ok {
		return
	}
	writeJSON(w, sessionByNameResp{
		ID:          sess.ID,
		Name:        sess.Name,
		Customer:    sess.Customer,
		Job:         sess.Job,
		Persona:     sess.Persona,
		Title:       sess.Title,
		Status:      sess.Status,
		CreateError: sess.CreateError,
		CreatedAt:   sess.CreatedAt,
		UpdatedAt:   sess.UpdatedAt,
	})
}

// resolveSessionByName is the single name→row gate. Every by-name route goes
// through it, so the tenancy answer is written once: T8's artifact-by-name
// routes take a session **id** into the artifact store with no customer
// parameter, and their whole tenancy rides on this function having resolved the
// name first.
//
// It writes the error response and returns ok=false; the caller just returns.
func (h *Handlers) resolveSessionByName(w http.ResponseWriter, r *http.Request, id Identity, name string) (*agentdb.Session, bool) {
	if h.cfg.SessionNames == nil {
		http.Error(w, "session names are not configured on this host", http.StatusNotImplemented)
		return nil, false
	}
	if id.Customer == "" {
		// Names live in the (customer, name) index, so an unscoped credential has
		// nothing to resolve against. 403 and not 404, for the same reason
		// GET /agent/memories answers 403: no session is being hidden here — the
		// question cannot be asked at all.
		http.Error(w, "no project in token", http.StatusForbidden)
		return nil, false
	}
	sess, err := h.cfg.SessionNames.GetSessionByName(r.Context(), id.Customer, name)
	if err != nil || sess == nil {
		if err != nil && !errors.Is(err, agentdb.ErrSessionNotFound) {
			// A store outage is not a missing session, and reporting it as one
			// sends an operator hunting for a name that is sitting right there.
			// Nothing about the request leaks: the message is the store's.
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return nil, false
		}
		// Absent, malformed and "belongs to another project" are ONE answer. The
		// store query is already scoped to id.Customer, so a foreign name simply
		// does not match — and the caller must not be able to tell the three
		// apart, or the route becomes a project-membership oracle.
		http.Error(w, "session not found", http.StatusNotFound)
		return nil, false
	}
	// The embed-token leg, checked after resolution rather than before because
	// the scope is an id and the request carried a name. A scoped credential can
	// therefore look up exactly the name that resolves to its own session; every
	// other name is indistinguishable from absent, so it cannot enumerate the
	// project's names.
	if !scopeAllows(id, sess.ID) {
		http.Error(w, "session not found", http.StatusNotFound)
		return nil, false
	}
	return sess, true
}
