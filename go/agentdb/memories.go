package agentdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ---------------------------------------------------------------------------
// The memory substrate (spec §7).
//
// Append-only, labeled, project-scoped. The surface here is deliberately
// create / get / search and NOTHING else: there is no UpdateMemory and no
// DeleteMemory, because a memory is a record of a moment that happened and is
// never rewritten (§7.1). Anything that looks like "changing" a memory is a
// newer memory; readers take the newest match.
//
// Embeddings are passed IN, never computed here — the provider seam lives
// outside agentdb, and a nil embedding is a first-class case (search then
// degrades to the keyword leg, §7.6.5).
// ---------------------------------------------------------------------------

// MemoryEmbeddingDim is the vector width of content_embedding (§7.1).
const MemoryEmbeddingDim = 1536

const (
	memorySnippetLen         = 500 // search returns snippets; MemoryGet returns everything
	defaultMemorySearchLimit = 20
	maxMemorySearchLimit     = 100
	// memoryRRFK is the k of Reciprocal Rank Fusion, fixed at 60 by the
	// relevance contract (§7.6.3). It is not a tunable: the whole point of
	// the contract is that callers supply words, never weights.
	memoryRRFK = 60
	// memoryCandidateLimit caps how deep each leg is ranked before fusion.
	memoryCandidateLimit = 200
)

// ErrMemoryRequiresPostgres is returned when the memory store is used against a
// non-Postgres dialect. The memory tables lean on jsonb containment, tsvector
// full-text and (optionally) pgvector — none of which the sqlite dev store has.
// Failing loudly beats silently pretending to remember things.
var ErrMemoryRequiresPostgres = errors.New("agentdb: memory requires Postgres (jsonb + tsvector; pgvector for the semantic leg)")

// ErrMemoryEmbeddingUnstorable is returned by CreateMemory when an embedding
// was supplied but this database has no `memories.content_embedding` column —
// migration 022 adds it only where the pgvector extension is available, and
// swallows the failure otherwise (managed Postgres, where the app role often
// cannot CREATE EXTENSION). Storing the row anyway would be permanent: memories
// are append-only, so it could never be embedded later and semantic search
// would degrade forever without saying so.
var ErrMemoryEmbeddingUnstorable = errors.New(
	"agentdb: this database has no memories.content_embedding column (pgvector was unavailable when migration 022 ran), " +
		"so the embedding cannot be stored and the memory was NOT written — memories are append-only and could never be embedded later. " +
		"Install pgvector and re-run migration 022, or store this memory without an embedding")

// ErrMemoryNotFound is returned by GetMemory for an unknown id — or for an id
// that exists in another project (no existence leak across projects).
var ErrMemoryNotFound = errors.New("agentdb: memory not found")

// Memory is one append-only memory row.
type Memory struct {
	ID               string   `json:"id" gorm:"primaryKey;type:varchar(36)"`
	Project          string   `json:"project" gorm:"type:text;not null"`
	Labels           LabelSet `json:"labels" gorm:"type:jsonb"`
	Content          string   `json:"content" gorm:"type:text"`
	CreatedByWorker  string   `json:"created_by_worker" gorm:"type:text"`
	CreatedBySession string   `json:"created_by_session" gorm:"type:text"`
	CreatedAt        int64    `json:"created_at"`
}

func (Memory) TableName() string { return "memories" }

// MemorySearchQuery is the whole input to the relevance contract (§7.6).
// There are no weights, modes or tuning knobs by design.
type MemorySearchQuery struct {
	// Project is the hardwired namespace. Required; every leg filters on it.
	Project string
	// LabelSelector is Kubernetes-style selector text (§7.2). Optional.
	LabelSelector string
	// Query is free text. Empty ⇒ the filtered set, newest first.
	Query string
	// QueryEmbedding is the embedding of Query. Nil ⇒ keyword-only.
	QueryEmbedding []float32
	// Limit defaults to 20, capped at 100.
	Limit int
	// Since is an inclusive lower bound on created_at, in unix MILLISECONDS
	// (the unit this table is stamped in — the event spine uses seconds).
	// Zero means unbounded.
	Since int64
	// Until is an inclusive upper bound on created_at, unix MILLISECONDS.
	// Zero means unbounded.
	Until int64
	// LatestPer is a label KEY. When set, the candidate set is reduced to the
	// newest memory per distinct value of that label BEFORE ranking, and rows
	// that do not carry the key are excluded. Empty means no reduction.
	//
	// It is the set-valued form of NewestMemory: `memory_current` answers "the
	// current value of x" for one name, this answers it for every name at once.
	LatestPer string
}

// MemorySearchResult is one hit. Provenance is part of the result, not an
// extra (§7.3) — the session permalink is built from CreatedBySession by the
// tool layer, which knows the externally-reachable base URL.
type MemorySearchResult struct {
	ID               string   `json:"id"`
	Labels           LabelSet `json:"labels"`
	Snippet          string   `json:"snippet"`
	Score            float64  `json:"score"`
	CreatedByWorker  string   `json:"created_by_worker"`
	CreatedBySession string   `json:"created_by_session"`
	CreatedAt        int64    `json:"created_at"`
}

// CreateMemory appends a memory. The embedding is optional: pass nil when no
// provider is configured and the row simply has no semantic leg. The stored
// row is read back and returned (never the caller's struct) so what the caller
// sees is what the database actually holds.
//
// The second return says whether the row LANDED WITH AN EMBEDDING — read back
// from the database, not inferred from the argument. It is a separate return
// rather than a field on Memory because it is only knowable at the moment of
// the write: the other read paths cannot answer it, and a field that is
// truthful on one path and zero on the others is the same silent lie in a
// different place.
//
// A non-nil embedding that CANNOT be stored is an error
// (ErrMemoryEmbeddingUnstorable), not a quiet downgrade. Memories are
// append-only with no update path (§7.1), so a row written without its vector
// can never gain one: the degradation would be permanent and invisible. This
// matches the write path's existing fail-hard-on-embedder-error decision — a
// caller that would rather have a keyword-only memory can pass nil and say so.
func (s *Store) CreateMemory(ctx context.Context, m *Memory, embedding []float32) (*Memory, bool, error) {
	if err := s.requirePostgres(); err != nil {
		return nil, false, err
	}
	if m == nil {
		return nil, false, fmt.Errorf("agentdb: memory is required")
	}
	if m.Project == "" {
		return nil, false, fmt.Errorf("agentdb: memory project is required")
	}
	if strings.TrimSpace(m.Content) == "" {
		return nil, false, fmt.Errorf("agentdb: memory content is required")
	}
	// Before any database round-trip, so an oversized write costs nothing and
	// says something useful. Applies to EVERY writer — the memory tool, prompt
	// revisions, and topology seeds — because the ceiling is the database's,
	// not any one caller's.
	if len(m.Content) > MaxMemoryBytes {
		return nil, false, fmt.Errorf(
			"agentdb: memory content is %d bytes, over the %d-byte ceiling — store the document as an artifact and keep a memory that points at it",
			len(m.Content), MaxMemoryBytes)
	}
	if err := ValidateLabels(m.Labels); err != nil {
		return nil, false, fmt.Errorf("agentdb: memory labels: %w", err)
	}
	if embedding != nil && len(embedding) != MemoryEmbeddingDim {
		return nil, false, fmt.Errorf("agentdb: memory embedding must have %d dimensions, got %d", MemoryEmbeddingDim, len(embedding))
	}

	// Ask BEFORE the insert, and refuse rather than drop the vector silently.
	hasVector := false
	if embedding != nil {
		ok, err := s.MemoryVectorColumn(ctx)
		if err != nil {
			return nil, false, fmt.Errorf("agentdb: create memory: %w", err)
		}
		if !ok {
			return nil, false, ErrMemoryEmbeddingUnstorable
		}
		hasVector = true
	}

	if m.ID == "" {
		m.ID = uuid.New().String()
	}
	if m.CreatedAt == 0 {
		// Milliseconds: newest-first is load-bearing for the name= convention
		// and briefing lookups, and second granularity ties too easily.
		m.CreatedAt = time.Now().UnixMilli()
	}
	labelsJSON, err := json.Marshal(nonNilLabels(m.Labels))
	if err != nil {
		return nil, false, fmt.Errorf("agentdb: encode memory labels: %w", err)
	}

	cols := "id, project, labels, content, created_by_worker, created_by_session, created_at"
	vals := "?, ?, ?::jsonb, ?, ?, ?, ?"
	args := []any{m.ID, m.Project, string(labelsJSON), m.Content, m.CreatedByWorker, m.CreatedBySession, m.CreatedAt}
	if hasVector {
		cols += ", content_embedding"
		vals += ", ?::vector"
		args = append(args, FormatVector(embedding))
	}

	sql := "INSERT INTO memories (" + cols + ") VALUES (" + vals + ")"
	if err := s.gdb.WithContext(ctx).Exec(sql, args...).Error; err != nil {
		return nil, false, fmt.Errorf("agentdb: create memory: %w", err)
	}

	// What the store actually WROTE, asked of the store. Not `embedding != nil`:
	// that was RD3 — the caller was told "embedded" because the *embedder* had
	// produced a vector, whatever became of it afterwards.
	embedded := false
	if hasVector {
		var n int64
		if err := s.gdb.WithContext(ctx).Raw(
			"SELECT COUNT(*) FROM memories WHERE id = ? AND content_embedding IS NOT NULL", m.ID,
		).Scan(&n).Error; err != nil {
			return nil, false, fmt.Errorf("agentdb: read back memory embedding: %w", err)
		}
		embedded = n > 0
	}

	stored, err := s.GetMemory(ctx, m.Project, m.ID)
	if err != nil {
		return nil, false, err
	}
	return stored, embedded, nil
}

// Size ceilings. Neither is a storage limit in the ordinary sense — `content`
// is an uncapped TEXT column and memory_get returns whatever was written. They
// exist because two things downstream of the column DO have limits, and both
// otherwise fail with somebody else's error message.
const (
	// MaxEmbeddedMemoryBytes is the largest content that may be sent to an
	// embedding provider. Sized for prose at roughly 4 bytes/token against the
	// 8191-token limit of text-embedding-3-small, with headroom — because bytes
	// are NOT tokens: dense content (JSON, code, non-Latin scripts) can run
	// closer to 2 bytes/token, so a caller near this ceiling with dense content
	// can still be refused by the provider. That refusal is caught and
	// re-worded at the tool boundary rather than guessed at here.
	//
	// Exceeding it is not a reason to fail a write — it is a reason to write
	// the memory WITHOUT a vector. See the `embed` argument on memory_create.
	MaxEmbeddedMemoryBytes = 24576

	// MaxMemoryBytes is the ceiling for any memory whatsoever. It buys margin
	// under the Postgres tsvector limit for ordinary text; it is NOT a
	// guarantee, because that limit applies to the tsvector REPRESENTATION
	// (lexemes plus positions) rather than to the input string, so
	// high-entropy content — logs, hex ids, base64 — can still exceed it and
	// fail in the generated column. What this constant guarantees is that the
	// ordinary case fails with OUR message, naming the limit and the actual
	// size, instead of an opaque one from the database.
	MaxMemoryBytes = 1048576
)

// RetractionLabel is the label a memory carries to withdraw another one. Its
// value is the retracted memory's id (§7.1).
//
// Retraction is how an append-only store takes something back. The wrong fact's
// row is never touched — it stays attributed, timestamped and auditable — but it
// stops being SELECTED: it can no longer reach a briefing or a search result,
// and so can no longer influence a job. The withdrawal is itself an ordinary
// memory, which means it has an author, a time and a body explaining why, and it
// appears in the config-log-like history of the project's knowledge.
//
// Without this, a project had no way to correct itself. A memory written from a
// bad source, a hallucinated fact, or text injected through an event stayed
// selectable for ever, and the only remedies were to write a louder contradiction
// beside it or to abandon the label.
const RetractionLabel = "retracts"

// notRetractedSQL is the clause that hides retracted memories, for a query whose
// `memories` table is called alias. It is a correlated NOT EXISTS rather than a
// join so it cannot duplicate rows or disturb ordering, and it binds no extra
// arguments — the project is taken from the row being tested, which keeps
// retraction strictly project-local (P5) without the caller having to remember.
//
// Deliberately NOT applied to GetMemory: fetching a specific id is an explicit
// request for that row, and being able to read what was withdrawn — and the
// retraction that withdrew it — is the point of not deleting anything.
func notRetractedSQL(alias string) string {
	return fmt.Sprintf(
		"NOT EXISTS (SELECT 1 FROM memories r WHERE r.project = %[1]s.project AND r.labels->>'%[2]s' = %[1]s.id)",
		alias, RetractionLabel)
}

// GetMemory returns one memory in full. The project is a parameter, not a
// filter the caller may forget: a memory from another project is not found.
//
// A retracted memory IS returned here — see notRetractedSQL.
func (s *Store) GetMemory(ctx context.Context, project, id string) (*Memory, error) {
	if err := s.requirePostgres(); err != nil {
		return nil, err
	}
	if project == "" {
		return nil, fmt.Errorf("agentdb: memory project is required")
	}
	if id == "" {
		return nil, fmt.Errorf("agentdb: memory id is required")
	}
	var mem Memory
	err := s.gdb.WithContext(ctx).Model(&Memory{}).
		Where("project = ? AND id = ?", project, id).First(&mem).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrMemoryNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("agentdb: get memory: %w", err)
	}
	return &mem, nil
}

// NewestMemory returns the newest memory matching selector within project, in
// FULL — content, not the search snippet. Nothing matching is ErrMemoryNotFound:
// absence is a normal answer here, not a failure.
//
// It exists because two callers both mean "the current value of this selector"
// and both need the whole body:
//
//   - the `name=` KV convention (§7.1), read by the `memory_current` tool (§7.3);
//   - each briefing-section lookup at job composition (§7.4) — the ONLY memory
//     reads core itself ever performs, one fixed newest-match query per selector.
//
// It is deliberately NOT SearchMemories with limit 1: search returns 500-byte
// snippets, and a briefing section that silently stopped mid-sentence would be a
// lie about what the worker remembers. Ordering is the §7.6.2 bare-selector rule
// (newest first, id as the tiebreak) — the same recency question, asked for one
// row.
func (s *Store) NewestMemory(ctx context.Context, project, selector string) (*Memory, error) {
	if err := s.requirePostgres(); err != nil {
		return nil, err
	}
	if project == "" {
		return nil, fmt.Errorf("agentdb: memory project is required")
	}
	// The project filter binds first and in code, exactly as in SearchMemories:
	// a selector may narrow the set, never widen it past the project.
	where := "project = ?"
	args := []any{project}
	labelSQL, labelArgs, err := LabelSelectorSQL(selector, "labels")
	if err != nil {
		return nil, fmt.Errorf("agentdb: newest memory selector: %w", err)
	}
	if labelSQL != "" {
		where += " AND " + labelSQL
		args = append(args, labelArgs...)
	}
	// A retracted memory is not the current value of anything — which matters
	// most precisely here, because this is the query that fills briefings.
	where += " AND " + notRetractedSQL("memories")

	var mem Memory
	err = s.gdb.WithContext(ctx).Model(&Memory{}).
		Where(where, args...).
		Order("created_at DESC, id DESC").
		First(&mem).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrMemoryNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("agentdb: newest memory: %w", err)
	}
	return &mem, nil
}

// SearchMemories implements the §7.6 relevance contract in one query:
//
//  1. hard filter on project (always, in code), then the label selector;
//  2. no query text ⇒ the filtered set, newest first;
//  3. with query text ⇒ two legs over the filtered set — Postgres full-text and
//     pgvector cosine — fused by Reciprocal Rank Fusion (1/(60+rank) summed);
//  4. recency is a tiebreak only;
//  5. no embedding (query-side or row-side) ⇒ the semantic leg is skipped and
//     the shape of the result never changes.
func (s *Store) SearchMemories(ctx context.Context, q *MemorySearchQuery) ([]*MemorySearchResult, error) {
	if err := s.requirePostgres(); err != nil {
		return nil, err
	}
	if q == nil || q.Project == "" {
		return nil, fmt.Errorf("agentdb: memory search project is required")
	}
	limit := q.Limit
	if limit <= 0 {
		limit = defaultMemorySearchLimit
	}
	if limit > maxMemorySearchLimit {
		limit = maxMemorySearchLimit
	}

	// 1. Hard filter. project binds first and in code; the selector is ANDed.
	where := "project = ?"
	whereArgs := []any{q.Project}
	labelSQL, labelArgs, err := LabelSelectorSQL(q.LabelSelector, "labels")
	if err != nil {
		return nil, fmt.Errorf("agentdb: memory search selector: %w", err)
	}
	if labelSQL != "" {
		where += " AND " + labelSQL
		whereArgs = append(whereArgs, labelArgs...)
	}
	// Retraction is part of the hard filter, so it applies identically to the
	// recency leg and to both relevance legs below — a withdrawn memory cannot
	// come back by scoring well.
	where += " AND " + notRetractedSQL("f")

	// Time bounds join the SAME hard filter, for the same reason: the string
	// built here is interpolated into the recency query and into the `filtered`
	// CTE that both relevance legs read from, so one clause governs every leg.
	if q.Since > 0 && q.Until > 0 && q.Since > q.Until {
		return nil, fmt.Errorf("agentdb: memory search since (%d) is after until (%d) — that range matches nothing", q.Since, q.Until)
	}
	if q.Since > 0 {
		where += " AND f.created_at >= ?"
		whereArgs = append(whereArgs, q.Since)
	}
	if q.Until > 0 {
		where += " AND f.created_at <= ?"
		whereArgs = append(whereArgs, q.Until)
	}

	// LatestPer reduces the candidate set to the newest row per distinct value
	// of one label key, BEFORE ranking. Two clauses are needed, and the second
	// is easy to forget: labels->>'k' is NULL for a row without the key, and
	// DISTINCT ON groups NULLs together, so without an explicit existence test
	// exactly one arbitrary keyless row would survive the reduction.
	//
	// jsonb_exists(...) rather than `labels ? 'k'` because the jsonb `?`
	// operator collides with the SQL placeholder (see labels.go).
	latestPer := strings.TrimSpace(q.LatestPer)
	if latestPer != "" {
		if err := ValidateLabelKey(latestPer); err != nil {
			return nil, fmt.Errorf("agentdb: memory search latest_per: %w", err)
		}
		where += " AND jsonb_exists(f.labels, ?)"
		whereArgs = append(whereArgs, latestPer)
	}

	// distinctOn is the reduction clause itself, empty when unused. The key is
	// validated above, so it is safe to interpolate; it cannot be a placeholder
	// because Postgres will not take one in DISTINCT ON / ORDER BY position.
	distinctOn := ""
	latestOrder := ""
	if latestPer != "" {
		distinctOn = fmt.Sprintf("DISTINCT ON (f.labels->>'%s') ", latestPer)
		latestOrder = fmt.Sprintf("ORDER BY f.labels->>'%s', f.created_at DESC, f.id DESC", latestPer)
	}

	snippet := fmt.Sprintf(
		"CASE WHEN length(%[1]s.content) > %[2]d THEN substring(%[1]s.content, 1, %[2]d) ELSE %[1]s.content END AS snippet",
		"f", memorySnippetLen)

	// 2. No query text ⇒ a recency question, not a relevance one.
	if strings.TrimSpace(q.Query) == "" {
		inner := `SELECT ` + distinctOn + `f.id, f.labels, ` + snippet + `,
				0::float8 AS score, f.created_by_worker, f.created_by_session, f.created_at
			FROM memories f
			WHERE ` + where
		if latestPer == "" {
			return s.scanMemoryResults(ctx,
				inner+"\n\t\t\tORDER BY f.created_at DESC, f.id DESC\n\t\t\tLIMIT ?",
				append(append([]any{}, whereArgs...), limit))
		}
		// DISTINCT ON requires ORDER BY to lead with its own expression, which
		// is not the order the caller wants back — so reduce inside, then
		// re-order newest-first outside.
		sql := `SELECT * FROM (` + inner + "\n\t\t\t" + latestOrder + `) d
			ORDER BY d.created_at DESC, d.id DESC
			LIMIT ?`
		return s.scanMemoryResults(ctx, sql, append(append([]any{}, whereArgs...), limit))
	}

	// 3. Hybrid retrieval, fused. Both legs run over the same filtered set.
	// READ path: degrade, never fail (§7.6.5) — but say so. A probe error costs
	// this query its semantic leg and nothing more; it is not cached, so the
	// next query asks again rather than inheriting a wrong deployment fact.
	semantic := false
	if q.QueryEmbedding != nil {
		ok, err := s.MemoryVectorColumn(ctx)
		if err != nil {
			log.Printf("[agentdb] could not determine whether memories.content_embedding exists (%v) — this search degrades to keyword-only", err)
		}
		semantic = ok
	}
	if q.QueryEmbedding != nil && len(q.QueryEmbedding) != MemoryEmbeddingDim {
		return nil, fmt.Errorf("agentdb: query embedding must have %d dimensions, got %d", MemoryEmbeddingDim, len(q.QueryEmbedding))
	}

	var b strings.Builder
	args := []any{}

	b.WriteString(`WITH filtered AS (
			SELECT ` + distinctOn + `f.id, f.labels, f.content, f.content_tsv, f.created_by_worker, f.created_by_session, f.created_at`)
	if semantic {
		b.WriteString(`, f.content_embedding`)
	}
	// The reduction lands INSIDE the CTE, so kw, sem and the final join all read
	// the same reduced set. The CTE has no ORDER BY of its own otherwise, and
	// adding one here does not affect how the legs rank: each re-sorts.
	b.WriteString(`
			FROM memories f WHERE ` + where + `
			` + latestOrder + `
		), kw AS (
			SELECT id, ROW_NUMBER() OVER (ORDER BY kscore DESC, created_at DESC, id DESC) AS rnk
			FROM (
				SELECT id, created_at, ts_rank_cd(content_tsv, plainto_tsquery('english', ?)) AS kscore
				FROM filtered
				WHERE content_tsv @@ plainto_tsquery('english', ?)
				ORDER BY kscore DESC, created_at DESC, id DESC
				LIMIT ` + strconv.Itoa(memoryCandidateLimit) + `
			) k
		)`)
	args = append(args, whereArgs...)
	args = append(args, q.Query, q.Query)

	if semantic {
		b.WriteString(`, sem AS (
			SELECT id, ROW_NUMBER() OVER (ORDER BY dist ASC, created_at DESC, id DESC) AS rnk
			FROM (
				SELECT id, created_at, content_embedding <=> ?::vector AS dist
				FROM filtered
				WHERE content_embedding IS NOT NULL
				ORDER BY dist ASC, created_at DESC, id DESC
				LIMIT ` + strconv.Itoa(memoryCandidateLimit) + `
			) v
		)`)
		args = append(args, FormatVector(q.QueryEmbedding))
	}

	k := strconv.Itoa(memoryRRFK)
	score := `(COALESCE(1.0/(` + k + ` + kw.rnk), 0)`
	joins := "LEFT JOIN kw ON kw.id = f.id"
	hits := "kw.id IS NOT NULL"
	if semantic {
		score += ` + COALESCE(1.0/(` + k + ` + sem.rnk), 0)`
		joins += "\n\t\t\tLEFT JOIN sem ON sem.id = f.id"
		hits += " OR sem.id IS NOT NULL"
	}
	score += `)::float8 AS score`

	b.WriteString(`
		SELECT f.id, f.labels, ` + snippet + `,
			` + score + `, f.created_by_worker, f.created_by_session, f.created_at
		FROM filtered f
		` + joins + `
		WHERE ` + hits + `
		ORDER BY score DESC, f.created_at DESC, f.id DESC
		LIMIT ?`)
	args = append(args, limit)

	return s.scanMemoryResults(ctx, b.String(), args)
}

func (s *Store) scanMemoryResults(ctx context.Context, sql string, args []any) ([]*MemorySearchResult, error) {
	var out []*MemorySearchResult
	if err := s.gdb.WithContext(ctx).Raw(sql, args...).Scan(&out).Error; err != nil {
		return nil, fmt.Errorf("agentdb: search memories: %w", err)
	}
	if out == nil {
		out = []*MemorySearchResult{}
	}
	return out, nil
}

// FormatVector renders a float32 slice as a pgvector literal.
func FormatVector(v []float32) string {
	parts := make([]string, len(v))
	for i, f := range v {
		parts[i] = strconv.FormatFloat(float64(f), 'g', -1, 32)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func (s *Store) requirePostgres() error {
	if s == nil || s.gdb == nil || s.gdb.Dialector == nil || s.gdb.Dialector.Name() != "postgres" {
		return ErrMemoryRequiresPostgres
	}
	return nil
}

// MemoryVectorColumn reports whether migration 022 was able to add the pgvector
// column (it only does so where the extension is available, and swallows the
// failure with a RAISE NOTICE where it is not — the managed-Postgres case where
// the app role may not CREATE EXTENSION).
//
// The answer is cached once ANSWERED, never once ASKED: a probe that errors
// returns (false, err) and caches nothing, so the next caller asks again. The
// predecessor was a sync.Once over `err == nil && n > 0`, which turned one
// transient error into a process-lifetime claim that the column was absent
// (RD3, second route).
//
// Callers must decide what an error means for them: the write path refuses
// (a memory is append-only — storing it unembedded is permanent), the read path
// degrades to the keyword leg (§7.6.5).
func (s *Store) MemoryVectorColumn(ctx context.Context) (bool, error) {
	s.memVecMu.Lock()
	defer s.memVecMu.Unlock()
	if s.memVecKnown {
		return s.memVecOK, nil
	}
	var n int64
	if err := s.gdb.WithContext(ctx).Raw(`
			SELECT COUNT(*) FROM information_schema.columns
			WHERE table_schema = current_schema()
			  AND table_name = 'memories' AND column_name = 'content_embedding'`).Scan(&n).Error; err != nil {
		return false, fmt.Errorf("agentdb: probe memories.content_embedding: %w", err)
	}
	s.memVecKnown = true
	s.memVecOK = n > 0
	if !s.memVecOK {
		log.Printf("[agentdb] memories.content_embedding absent — memory search degrades to keyword-only")
	}
	return s.memVecOK, nil
}

func nonNilLabels(l LabelSet) map[string]string {
	if l == nil {
		return map[string]string{}
	}
	return l
}
