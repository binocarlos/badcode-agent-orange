package main

// mcp_memory.go — the memory MCP tools (spec 03-memory §7.3), registered onto
// the host MCP server in mcpserver.go.
//
// The whole surface is four tools:
//
//	memory_create(content, labels?)                    → the stored record
//	memory_search(label_selector?, query?, limit?)     → ranked hits with provenance
//	memory_get(id)                                     → one memory in full
//	memory_current(name)                               → the newest `name=<name>`
//
// There is no memory_update and no memory_delete, and their absence is a
// design decision rather than an omission (§7.1): a memory is a record of a
// moment that happened, so "changing" one means appending a newer one and
// letting readers take the newest match. The store has no mutating method to
// call even if a tool wanted to.
//
// Two things every result carries, because §7.3 insists they are part of the
// answer and not an extra:
//
//   - provenance — which worker wrote this, in which session;
//   - a session permalink — so a worker can say "we worked this out already,
//     here is the conversation" and put the human one click from the thread.
//
// Embeddings follow the D2 asymmetry exactly. On the WRITE path
// embedding.Embed's error is propagated and the create FAILS: memories are
// append-only and never re-embedded, so a row written with a NULL embedding
// because a token expired for thirty seconds is permanently invisible to
// semantic search. On the READ path embedding.EmbedOrDegrade swallows the same
// failure: one keyword-only answer to one question is a far smaller loss.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/extension/embedding"
)

// memoryStore is the narrow slice of *agentdb.Store the tools need. Note what
// is NOT in it: there is no update and no delete to inject, so the append-only
// invariant survives the seam (§7.1).
type memoryStore interface {
	CreateMemory(ctx context.Context, m *agentdb.Memory, embedding []float32) (*agentdb.Memory, error)
	GetMemory(ctx context.Context, project, id string) (*agentdb.Memory, error)
	SearchMemories(ctx context.Context, q *agentdb.MemorySearchQuery) ([]*agentdb.MemorySearchResult, error)
	NewestMemory(ctx context.Context, project, selector string) (*agentdb.Memory, error)
}

// memoryTools is the tool set. embedder may be nil (no embedding endpoint
// configured — the documented deployment where search is keyword+recency,
// §7.6.5).
type memoryTools struct {
	store      memoryStore
	embedder   embedding.Provider
	permalinks permalinker
}

func newMemoryTools(store memoryStore, embedder embedding.Provider, permalinks permalinker) *memoryTools {
	return &memoryTools{store: store, embedder: embedder, permalinks: permalinks}
}

// ---------------------------------------------------------------------------
// Result shapes
// ---------------------------------------------------------------------------

// memoryRecord is a memory in full — what memory_get and memory_current return.
// The JSON key for the permalink is exactly `session_url` (F3's finding: every
// consumer emits that key, so a worker's prompt can name it once).
type memoryRecord struct {
	ID               string            `json:"id"`
	Labels           map[string]string `json:"labels"`
	Content          string            `json:"content"`
	CreatedByWorker  string            `json:"created_by_worker"`
	CreatedBySession string            `json:"created_by_session"`
	SessionURL       string            `json:"session_url"`
	CreatedAt        int64             `json:"created_at"`
}

// memoryHit is one search result: a snippet, not the whole body (§7.3 — search
// returns snippets, get returns everything), plus the fused score and the same
// provenance.
type memoryHit struct {
	ID               string            `json:"id"`
	Labels           map[string]string `json:"labels"`
	Snippet          string            `json:"snippet"`
	Score            float64           `json:"score"`
	CreatedByWorker  string            `json:"created_by_worker"`
	CreatedBySession string            `json:"created_by_session"`
	SessionURL       string            `json:"session_url"`
	CreatedAt        int64             `json:"created_at"`
}

func (m *memoryTools) record(project string, mem *agentdb.Memory) memoryRecord {
	return memoryRecord{
		ID:               mem.ID,
		Labels:           labelMap(mem.Labels),
		Content:          mem.Content,
		CreatedByWorker:  mem.CreatedByWorker,
		CreatedBySession: mem.CreatedBySession,
		SessionURL:       m.permalinks.SessionURL(project, mem.CreatedBySession),
		CreatedAt:        mem.CreatedAt,
	}
}

func labelMap(l agentdb.LabelSet) map[string]string {
	out := make(map[string]string, len(l))
	for k, v := range l {
		out[k] = v
	}
	return out
}

// ---------------------------------------------------------------------------
// Tool descriptions
//
// These are prompt, not documentation: they are the only thing standing between
// a model and a misuse of the store, and they are read on every job. Each says
// what the tool does, what it costs, and the one thing that is easy to get
// wrong.
// ---------------------------------------------------------------------------

const memoryCreateDescription = `Append a memory to this project's shared, permanent memory store.

Memories are append-only and immutable: there is no way to edit or delete one, ` +
	`ever. To "update" something, write a NEW memory with the same labels — readers ` +
	`take the newest match, and the older versions remain as an honest record.

Labels are how memories are found again. They are identifiers, not prose: keys ` +
	`and values must be alphanumeric with '-', '_' or '.', at most 63 characters, ` +
	`at most 32 per memory. Content is content; do not try to put an email subject ` +
	`or a sentence in a label. Follow whatever label conventions this project ` +
	`records (often a memory labelled name=label-registry).

Conventions worth knowing: kind=<something> says what sort of memory this is, ` +
	`worker=<name> says who it is about, and name=<x> means "this is the current ` +
	`value of x" (write a new one to change it, read it back with memory_current).`

const memorySearchDescription = `Search this project's memory store. Search before ` +
	`making decisions that earlier work might inform — that is what it is for.

Two independent filters, both optional:
  label_selector — Kubernetes-style, ANDed: "kind=lesson,worker=email-answerer", ` +
	`"kind in (summary, lesson)", "kind!=raw-transcript", "exists thread", "!archived". ` +
	`No OR and no nesting: if you need OR, run two searches.
  query          — free text, ranked by relevance over whatever the selector left.

With no query text you get the filtered set NEWEST FIRST — that is a recency ` +
	`question, and the score is not meaningful. With query text you get a hybrid of ` +
	`exact-word and meaning-based matching, fused into one ranking.

IMPORTANT — read the scores. The ranking has no relevance threshold: it always ` +
	`returns up to 'limit' rows, so the tail of a result list is filled with the ` +
	`least-bad matches even when nothing in the store is actually relevant. Scores ` +
	`are small fusion numbers (roughly 0.016 for a top hit, halving as rank falls). ` +
	`A low score means "nothing good", NOT "here is a weak but real answer". If the ` +
	`best hit does not visibly answer your question, treat the store as empty on ` +
	`that subject and say so, rather than building on a poor match.

Results are snippets. Use memory_get to read one in full. Every hit carries who ` +
	`wrote it, in which session, and a session_url link to that conversation — quote ` +
	`the link when telling a human "we already worked this out".`

const memoryGetDescription = `Read one memory in full by id, with its labels and ` +
	`provenance. memory_search returns truncated snippets; this returns the entire ` +
	`content, which may be large (transcripts are stored whole).`

const memoryCurrentDescription = `Read the current value of a named memory: the ` +
	`newest memory labelled name=<name>, in full.

This is the project's key/value convention (§ the name= convention). Values are ` +
	`updated by APPENDING a new memory with the same name label, so "current" always ` +
	`means "most recent". Equivalent to memory_search with label_selector "name=<name>" ` +
	`and limit 1, then memory_get on the result — but one call, and it returns the ` +
	`whole content.

If nothing has ever been written under that name the result is {"found": false}: ` +
	`that is a normal answer, not an error. Do not invent a value.`

// tools returns the four memory tools, in the order the model sees them.
func (m *memoryTools) tools() []*mcpTool {
	return []*mcpTool{
		{
			Name:        "memory_create",
			Description: memoryCreateDescription,
			InputSchema: objectSchema(map[string]any{
				"content": map[string]any{
					"type":        "string",
					"description": "The memory body. Arbitrary text; may be large.",
				},
				"labels": map[string]any{
					"type":                 "object",
					"description":          "Flat string→string labels. Identifiers only: [A-Za-z0-9] with '-', '_', '.', ≤63 chars, ≤32 labels.",
					"additionalProperties": map[string]any{"type": "string"},
				},
			}, []string{"content"}),
			Handler: m.create,
		},
		{
			Name:        "memory_search",
			Description: memorySearchDescription,
			InputSchema: objectSchema(map[string]any{
				"label_selector": map[string]any{
					"type":        "string",
					"description": "Kubernetes-style label selector, comma-ANDed. Optional.",
				},
				"query": map[string]any{
					"type":        "string",
					"description": "Free-text relevance query. Optional; omit for newest-first.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum hits (default 20, maximum 100).",
				},
			}, nil),
			Handler: m.search,
		},
		{
			Name:        "memory_get",
			Description: memoryGetDescription,
			InputSchema: objectSchema(map[string]any{
				"id": map[string]any{"type": "string", "description": "The memory id, as returned by memory_search or memory_create."},
			}, []string{"id"}),
			Handler: m.get,
		},
		{
			Name:        "memory_current",
			Description: memoryCurrentDescription,
			InputSchema: objectSchema(map[string]any{
				"name": map[string]any{"type": "string", "description": "The value of the name label, e.g. \"label-registry\"."},
			}, []string{"name"}),
			Handler: m.current,
		},
	}
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

type memoryCreateArgs struct {
	Content string            `json:"content"`
	Labels  map[string]string `json:"labels"`
}

func (m *memoryTools) create(ctx context.Context, caller mcpCaller, raw json.RawMessage) (any, error) {
	var args memoryCreateArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if strings.TrimSpace(args.Content) == "" {
		return nil, errors.New("content is required and must not be blank")
	}
	// Validate labels here as well as in the store, so the model gets the
	// specific complaint ("label value ... is invalid") rather than a wrapped
	// database error, and so nothing reaches the embedding provider that the
	// INSERT was going to reject anyway.
	if err := agentdb.ValidateLabels(args.Labels); err != nil {
		return nil, fmt.Errorf("labels: %w", err)
	}

	// WRITE path: strict. A configured provider that fails fails the create.
	vec, err := embedding.Embed(ctx, m.embedder, args.Content)
	if err != nil {
		return nil, fmt.Errorf("could not embed this memory, so it was NOT stored (retry rather than continue): %w", err)
	}

	stored, err := m.store.CreateMemory(ctx, &agentdb.Memory{
		Project:          caller.Project,
		Labels:           agentdb.LabelSet(args.Labels),
		Content:          args.Content,
		CreatedByWorker:  caller.Worker,
		CreatedBySession: caller.SessionID,
	}, vec)
	if err != nil {
		return nil, err
	}
	// §9 read-back: CreateMemory returns the row as the database holds it, and
	// that — not the caller's struct — is what is echoed.
	rec := m.record(caller.Project, stored)
	return struct {
		memoryRecord
		Embedded bool `json:"embedded"`
	}{memoryRecord: rec, Embedded: vec != nil}, nil
}

type memorySearchArgs struct {
	LabelSelector string `json:"label_selector"`
	Query         string `json:"query"`
	Limit         int    `json:"limit"`
}

func (m *memoryTools) search(ctx context.Context, caller mcpCaller, raw json.RawMessage) (any, error) {
	var args memorySearchArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if args.Limit < 0 {
		return nil, fmt.Errorf("limit must not be negative, got %d", args.Limit)
	}

	// READ path: degrade. A provider outage costs this query its semantic leg,
	// not its answer (§7.6.5).
	var queryVec []float32
	if strings.TrimSpace(args.Query) != "" {
		queryVec = embedding.EmbedOrDegrade(ctx, m.embedder, args.Query)
	}

	hits, err := m.store.SearchMemories(ctx, &agentdb.MemorySearchQuery{
		Project:        caller.Project, // in code, always — never an argument
		LabelSelector:  args.LabelSelector,
		Query:          args.Query,
		QueryEmbedding: queryVec,
		Limit:          args.Limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]memoryHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, memoryHit{
			ID:               h.ID,
			Labels:           labelMap(h.Labels),
			Snippet:          h.Snippet,
			Score:            h.Score,
			CreatedByWorker:  h.CreatedByWorker,
			CreatedBySession: h.CreatedBySession,
			SessionURL:       m.permalinks.SessionURL(caller.Project, h.CreatedBySession),
			CreatedAt:        h.CreatedAt,
		})
	}
	return map[string]any{
		"results": out,
		"count":   len(out),
		// Repeated next to the numbers because that is where it is needed: the
		// description is read once, this is read with every result set.
		"note": "Scores are rank-fusion values with no relevance floor: a low score means nothing good matched, not a weak match worth using.",
	}, nil
}

type memoryGetArgs struct {
	ID string `json:"id"`
}

func (m *memoryTools) get(ctx context.Context, caller mcpCaller, raw json.RawMessage) (any, error) {
	var args memoryGetArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if strings.TrimSpace(args.ID) == "" {
		return nil, errors.New("id is required")
	}
	// The project is a parameter of the store call, not a filter applied after:
	// a memory of another project is simply not found, with no existence leak.
	mem, err := m.store.GetMemory(ctx, caller.Project, strings.TrimSpace(args.ID))
	if err != nil {
		if errors.Is(err, agentdb.ErrMemoryNotFound) {
			return nil, fmt.Errorf("no memory with id %q in this project", args.ID)
		}
		return nil, err
	}
	return m.record(caller.Project, mem), nil
}

type memoryCurrentArgs struct {
	Name string `json:"name"`
}

// current is sugar for memory_search("name=<name>", limit 1) with memory_get
// semantics (§7.3) — one obvious word in a prompt for the KV read.
func (m *memoryTools) current(ctx context.Context, caller mcpCaller, raw json.RawMessage) (any, error) {
	var args memoryCurrentArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return nil, errors.New("name is required")
	}
	// The name is interpolated into a selector, so it must be a legal label
	// value — which also means no comma, '=', '!' or parenthesis can smuggle a
	// second term into the query.
	if err := agentdb.ValidateLabelValue(name); err != nil {
		return nil, fmt.Errorf("name: %w", err)
	}

	mem, err := m.store.NewestMemory(ctx, caller.Project, "name="+name)
	if err != nil {
		if errors.Is(err, agentdb.ErrMemoryNotFound) {
			// Absence is an answer, not a failure: nothing has been written
			// under this name yet. Said plainly so the model does not invent one.
			return map[string]any{"found": false, "name": name}, nil
		}
		return nil, err
	}
	return struct {
		Found bool   `json:"found"`
		Name  string `json:"name"`
		memoryRecord
	}{Found: true, Name: name, memoryRecord: m.record(caller.Project, mem)}, nil
}
