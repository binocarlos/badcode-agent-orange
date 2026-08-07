package httpapi

// The memory read route (spec 03-memory §7.6, design 15-operator-console B2).
//
//	GET /agent/memories
//	  query: ?selector=<k8s label selector>&query=<free text>&limit=<n>
//	  auth : the ordinary session JWT; the project comes from the Customer claim,
//	         never from the query (P5) — same posture as GET /agent/config-events.
//	  200  : {"memories": [MemorySearchResult, …]}
//	  400  : a malformed selector, reported with the parser's own message
//	  501  : no store wired, or a store that is not Postgres
//
// This is the same §7.6 relevance contract the memory_search MCP tool calls, and
// deliberately the same one: selector filter, free text fused by RRF, recency as
// a tiebreak, limit defaulting to 20 and capped at 100 IN THE STORE. There are no
// weights, no modes and no extra knobs here — a second, subtly different search
// would be a second contract to keep true.
//
// Beside it, the two FULL-CONTENT reads (T18 of
// design/2026-08-06-embeddable-agent-orange.md):
//
//	GET /agent/memories/{id}
//	GET /agent/memories/current?name=<n>
//	  200  : one memory in full — content, not a snippet
//	  400  : `current` with no name, or a name that is not a legal label value
//	  404  : no such memory in this project, WHATEVER the reason
//
// They exist because the search result above carries a `snippet` cut at 500
// bytes (agentdb/memories.go:35,259-262) and has no Content field at all, so
// until now the only way to read a memory whole was from inside a container
// through the memory_get / memory_current MCP tools. An embedding application
// rendering project state from memory — the reason this plan exists — cannot
// live on 500 bytes.
//
// They are the SAME two store calls those tools make (Store.GetMemory and
// Store.NewestMemory, agentdb/memories.go:152,190), deliberately: a second query
// path would be a second set of scoping rules to keep true, and the scoping is
// the whole security story here.
//
// Read-only: memories are append-only (§7.1) and are written by workers through
// their tools, so there is no POST counterpart — and no PUT or DELETE anywhere
// on this file's routes either.
//
// Postgres-only, like the store itself (jsonb selectors + tsvector). On the
// SQLite fallback the store returns ErrMemoryRequiresPostgres and these routes
// answer 501 — the same "not configured on this host" posture as
// POST /agent/project-token, rather than a 500 that reads like a bug.

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// MemoryStore is the slice of agentdb.Store these routes need. Note what is not
// in it: no create and no delete, so the append-only invariant survives the seam
// exactly as it does for the MCP tools (cmd/agentd/mcp_memory.go:46-52, whose
// memoryStore is this set plus CreateMemory). *agentdb.Store satisfies it.
//
// GetMemory and NewestMemory joined SearchMemories here rather than arriving as
// a second Config field: one field cannot be wired to two different stores by
// accident, and a host that has a memory store at all has these.
type MemoryStore interface {
	SearchMemories(ctx context.Context, q *agentdb.MemorySearchQuery) ([]*agentdb.MemorySearchResult, error)
	// GetMemory takes the project as an argument, not as a filter the caller
	// applies afterwards: a memory of another project is simply not found.
	GetMemory(ctx context.Context, project, id string) (*agentdb.Memory, error)
	// NewestMemory answers the newest match for a selector, in full. The `name=`
	// KV convention (§7.1) is what /agent/memories/current asks it.
	NewestMemory(ctx context.Context, project, selector string) (*agentdb.Memory, error)
}

// The concrete store must always satisfy the seam.
var _ MemoryStore = (*agentdb.Store)(nil)

// MemoryEmbedder supplies the query-side embedding for the semantic leg. It is
// optional and returns nil freely: a nil embedder, or a nil return from a
// provider that failed, costs this one query its semantic leg and nothing else
// (§7.6.5 — the result shape never changes). The host wires it from whatever
// embedding provider it already built, which is why the seam is a func rather
// than the provider type: httpapi stays free of the extension packages.
type MemoryEmbedder func(ctx context.Context, text string) []float32

// memoryReadable is the gate every memory route shares: a store must be wired,
// and the credential must name a project. Both answers are about the request's
// world rather than about any particular memory, so neither can leak one — which
// is why they run before the id or name is even looked at.
//
// It writes the error response and returns ok=false; the caller just returns.
func (h *Handlers) memoryReadable(w http.ResponseWriter, id Identity) bool {
	if h.cfg.Memories == nil {
		http.Error(w, "the memory store is not configured on this host", http.StatusNotImplemented)
		return false
	}
	if id.Customer == "" {
		// 403 and not 404: no memory is being hidden — the question cannot be
		// asked at all, because memories are namespaced by project and this
		// credential names none.
		http.Error(w, "no project in token", http.StatusForbidden)
		return false
	}
	return true
}

// ListMemories serves GET /agent/memories — the memory browser's read path.
func (h *Handlers) ListMemories(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	if !h.memoryReadable(w, id) {
		return
	}
	q := r.URL.Query()
	text := q.Get("query")
	// The embedding is computed only when there is text to embed: an empty query
	// is a recency question, not a relevance one.
	var vec []float32
	if text != "" && h.cfg.MemoryEmbedder != nil {
		vec = h.cfg.MemoryEmbedder(r.Context(), text)
	}
	rows, err := h.cfg.Memories.SearchMemories(r.Context(), &agentdb.MemorySearchQuery{
		Project:        id.Customer, // from the claim, always — never a parameter
		LabelSelector:  q.Get("selector"),
		Query:          text,
		QueryEmbedding: vec,
		Limit:          queryInt(r, "limit", 0),
	})
	if err != nil {
		if errors.Is(err, agentdb.ErrMemoryRequiresPostgres) {
			http.Error(w, err.Error(), http.StatusNotImplemented)
			return
		}
		// Everything else this call can fail with is the caller's selector, and
		// the parser's own message is the most useful thing to hand back.
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if rows == nil {
		rows = []*agentdb.MemorySearchResult{}
	}
	writeJSON(w, map[string]any{"memories": rows})
}

// memoryNotFound is the one answer for absent, malformed and "belongs to another
// project". A memory id is a uuid and hard to guess, but `name=` values are
// chosen by whoever wrote them and are not — so both routes answer with this
// single string, and a caller cannot use either one as an existence oracle for a
// project it is not in. Same rule, same words as resolveSessionByName's 404
// (sessions_byname.go:124-129).
const memoryNotFound = "memory not found"

// memoryRecordResp is one memory in full. It mirrors the MCP tools' memoryRecord
// (cmd/agentd/mcp_memory.go:73-81) key for key so the two surfaces describe a
// memory identically, minus `session_url`: building a permalink needs the
// externally-reachable base URL, which the tool layer has and this package does
// not. `created_by_session` is here, so a client that has a base URL can build
// the same link itself.
//
// It is a distinct type rather than agentdb.Memory because that struct carries
// `project`, which is already the caller's own token claim and is noise in a
// per-project response.
type memoryRecordResp struct {
	ID               string            `json:"id"`
	Labels           map[string]string `json:"labels"`
	Content          string            `json:"content"`
	CreatedByWorker  string            `json:"created_by_worker"`
	CreatedBySession string            `json:"created_by_session"`
	CreatedAt        int64             `json:"created_at"`
}

func memoryRecordOf(mem *agentdb.Memory) memoryRecordResp {
	// Labels are copied into a plain map so an absent label set renders as {}
	// rather than null — a client should not have to branch on that.
	labels := make(map[string]string, len(mem.Labels))
	for k, v := range mem.Labels {
		labels[k] = v
	}
	return memoryRecordResp{
		ID:               mem.ID,
		Labels:           labels,
		Content:          mem.Content,
		CreatedByWorker:  mem.CreatedByWorker,
		CreatedBySession: mem.CreatedBySession,
		CreatedAt:        mem.CreatedAt,
	}
}

// GetMemory serves GET /agent/memories/{id} — one memory, whole.
func (h *Handlers) GetMemory(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	if !h.memoryReadable(w, id) {
		return
	}
	memID := strings.TrimSpace(r.PathValue("id"))
	if memID == "" {
		// Belt and braces: the wildcard should never match an empty segment, and
		// an empty id reaching the store is an argument error (a 500) rather than
		// the 404 it plainly is.
		http.Error(w, memoryNotFound, http.StatusNotFound)
		return
	}
	// The project is the store call's first argument — tenancy decided in the
	// query, never by filtering a row that was already read. Same posture as the
	// memory_get tool (cmd/agentd/mcp_memory.go:357-365).
	mem, err := h.cfg.Memories.GetMemory(r.Context(), id.Customer, memID)
	if err != nil {
		writeMemoryReadError(w, err)
		return
	}
	writeJSON(w, memoryRecordOf(mem))
}

// CurrentMemory serves GET /agent/memories/current?name=<n> — the newest memory
// labelled name=<n>, in full. It is the HTTP twin of the memory_current tool
// (§7.3), and answers the same question an embedding app asks on every page
// render: "what is the current state of this thing".
//
// The literal `current` segment wins over {id} in Go's ServeMux precedence, so
// this route is reachable however a memory id happens to be spelled.
func (h *Handlers) CurrentMemory(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	if !h.memoryReadable(w, id) {
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		// Not defaulted to "everything": a bare `name=` selector would match
		// every row with no name label and hand back an arbitrary memory.
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	// The name is interpolated into selector text, so it must be a legal label
	// value first — which is also what stops a comma, '=', '!' or parenthesis
	// smuggling a second term into the query (agentdb/labels.go:28-34). The tool
	// applies exactly this guard for exactly this reason (mcp_memory.go:384-389).
	if err := agentdb.ValidateLabelValue(name); err != nil {
		http.Error(w, "name: "+err.Error(), http.StatusBadRequest)
		return
	}
	mem, err := h.cfg.Memories.NewestMemory(r.Context(), id.Customer, "name="+name)
	if err != nil {
		writeMemoryReadError(w, err)
		return
	}
	writeJSON(w, memoryRecordOf(mem))
}

// writeMemoryReadError maps a single-memory store failure onto a status.
//
// The default is 500, NOT 404: a database that is refusing connections is not a
// memory that is missing, and answering "not found" would send an operator
// hunting for a row that is sitting right there. The 404 is reserved for the one
// error that genuinely means it.
func writeMemoryReadError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, agentdb.ErrMemoryNotFound):
		http.Error(w, memoryNotFound, http.StatusNotFound)
	case errors.Is(err, agentdb.ErrMemoryRequiresPostgres):
		http.Error(w, err.Error(), http.StatusNotImplemented)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
