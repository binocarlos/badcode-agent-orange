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
// Read-only: memories are append-only (§7.1) and are written by workers through
// their tools, so there is no POST counterpart.
//
// Postgres-only, like the store itself (jsonb selectors + tsvector). On the
// SQLite fallback the store returns ErrMemoryRequiresPostgres and this route
// answers 501 — the same "not configured on this host" posture as
// POST /agent/project-token, rather than a 500 that reads like a bug.

import (
	"context"
	"errors"
	"net/http"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// MemoryStore is the slice of agentdb.Store this route needs. Note what is not
// in it: no create and no delete, so the append-only invariant survives the seam
// exactly as it does for the MCP tools. *agentdb.Store satisfies it.
type MemoryStore interface {
	SearchMemories(ctx context.Context, q *agentdb.MemorySearchQuery) ([]*agentdb.MemorySearchResult, error)
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

// ListMemories serves GET /agent/memories — the memory browser's read path.
func (h *Handlers) ListMemories(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	if h.cfg.Memories == nil {
		http.Error(w, "the memory store is not configured on this host", http.StatusNotImplemented)
		return
	}
	if id.Customer == "" {
		http.Error(w, "no project in token", http.StatusForbidden)
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
