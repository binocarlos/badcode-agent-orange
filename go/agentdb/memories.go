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
func (s *Store) CreateMemory(ctx context.Context, m *Memory, embedding []float32) (*Memory, error) {
	if err := s.requirePostgres(); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, fmt.Errorf("agentdb: memory is required")
	}
	if m.Project == "" {
		return nil, fmt.Errorf("agentdb: memory project is required")
	}
	if strings.TrimSpace(m.Content) == "" {
		return nil, fmt.Errorf("agentdb: memory content is required")
	}
	if err := ValidateLabels(m.Labels); err != nil {
		return nil, fmt.Errorf("agentdb: memory labels: %w", err)
	}
	if embedding != nil && len(embedding) != MemoryEmbeddingDim {
		return nil, fmt.Errorf("agentdb: memory embedding must have %d dimensions, got %d", MemoryEmbeddingDim, len(embedding))
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
		return nil, fmt.Errorf("agentdb: encode memory labels: %w", err)
	}

	cols := "id, project, labels, content, created_by_worker, created_by_session, created_at"
	vals := "?, ?, ?::jsonb, ?, ?, ?, ?"
	args := []any{m.ID, m.Project, string(labelsJSON), m.Content, m.CreatedByWorker, m.CreatedBySession, m.CreatedAt}
	if embedding != nil && s.memoryHasVectorColumn(ctx) {
		cols += ", content_embedding"
		vals += ", ?::vector"
		args = append(args, FormatVector(embedding))
	}

	sql := "INSERT INTO memories (" + cols + ") VALUES (" + vals + ")"
	if err := s.gdb.WithContext(ctx).Exec(sql, args...).Error; err != nil {
		return nil, fmt.Errorf("agentdb: create memory: %w", err)
	}
	return s.GetMemory(ctx, m.Project, m.ID)
}

// GetMemory returns one memory in full. The project is a parameter, not a
// filter the caller may forget: a memory from another project is not found.
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

	snippet := fmt.Sprintf(
		"CASE WHEN length(%[1]s.content) > %[2]d THEN substring(%[1]s.content, 1, %[2]d) ELSE %[1]s.content END AS snippet",
		"f", memorySnippetLen)

	// 2. No query text ⇒ a recency question, not a relevance one.
	if strings.TrimSpace(q.Query) == "" {
		sql := `SELECT f.id, f.labels, ` + snippet + `,
				0::float8 AS score, f.created_by_worker, f.created_by_session, f.created_at
			FROM memories f
			WHERE ` + where + `
			ORDER BY f.created_at DESC, f.id DESC
			LIMIT ?`
		return s.scanMemoryResults(ctx, sql, append(append([]any{}, whereArgs...), limit))
	}

	// 3. Hybrid retrieval, fused. Both legs run over the same filtered set.
	semantic := q.QueryEmbedding != nil && s.memoryHasVectorColumn(ctx)
	if q.QueryEmbedding != nil && len(q.QueryEmbedding) != MemoryEmbeddingDim {
		return nil, fmt.Errorf("agentdb: query embedding must have %d dimensions, got %d", MemoryEmbeddingDim, len(q.QueryEmbedding))
	}

	var b strings.Builder
	args := []any{}

	b.WriteString(`WITH filtered AS (
			SELECT id, labels, content, content_tsv, created_by_worker, created_by_session, created_at`)
	if semantic {
		b.WriteString(`, content_embedding`)
	}
	b.WriteString(`
			FROM memories WHERE ` + where + `
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

// memoryHasVectorColumn reports whether migration 022 was able to add the
// pgvector column (it only does so where the extension is available). Detected
// once per Store: a Postgres without pgvector is a deployment fact, not a
// per-query one.
func (s *Store) memoryHasVectorColumn(ctx context.Context) bool {
	s.memVecOnce.Do(func() {
		var n int64
		err := s.gdb.WithContext(ctx).Raw(`
			SELECT COUNT(*) FROM information_schema.columns
			WHERE table_schema = current_schema()
			  AND table_name = 'memories' AND column_name = 'content_embedding'`).Scan(&n).Error
		s.memVecOK = err == nil && n > 0
		if !s.memVecOK {
			log.Printf("[agentdb] memories.content_embedding absent — memory search degrades to keyword-only")
		}
	})
	return s.memVecOK
}

func nonNilLabels(l LabelSet) map[string]string {
	if l == nil {
		return map[string]string{}
	}
	return l
}
