package agentdb

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// The §7.6 relevance contract, proven against a real Postgres (pgvector).
// Run with:
//
//	AGENTKIT_TEST_POSTGRES_URL=postgres://... go test ./agentdb/ -run TestMemories
//
// Without the env var these skip (see openLivePG in live_pg_test.go). The
// embeddings here are a deterministic offline stand-in for the D2 provider:
// orthogonal unit vectors, so "same topic" is cosine distance 0 and "different
// topic" is 1 — which lets a test say exactly what the semantic leg believes.
// ---------------------------------------------------------------------------

// unitVector returns the one-hot vector at dim — orthogonal to every other
// unitVector, identical to itself. No hashing, so no accidental collisions.
func unitVector(dim int) []float32 {
	v := make([]float32, MemoryEmbeddingDim)
	v[dim%MemoryEmbeddingDim] = 1
	return v
}

// newLiveProject returns a fresh project namespace and cleans its rows up.
// Memories have no delete in the store API by design (§7.1); tests reach past
// it with raw SQL, which is exactly the distinction being enforced.
func newLiveProject(t *testing.T, s *Store) string {
	t.Helper()
	p := "proj-" + uuid.New().String()
	t.Cleanup(func() { s.DB().Exec("DELETE FROM memories WHERE project = ?", p) })
	return p
}

func mustCreateMemory(t *testing.T, s *Store, m *Memory, emb []float32) *Memory {
	t.Helper()
	got, err := s.CreateMemory(context.Background(), m, emb)
	if err != nil {
		t.Fatalf("create memory %q: %v", m.Content, err)
	}
	return got
}

func resultIDs(res []*MemorySearchResult) []string {
	out := make([]string, len(res))
	for i, r := range res {
		out[i] = r.ID
	}
	return out
}

func requireVectorColumn(t *testing.T, s *Store) {
	t.Helper()
	if !s.memoryHasVectorColumn(context.Background()) {
		t.Skip("pgvector column absent on this Postgres — semantic-leg test not applicable")
	}
}

func TestMemoriesLiveCreateAndGet(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	project := newLiveProject(t, s)

	mem := mustCreateMemory(t, s, &Memory{
		Project: project,
		Labels: LabelSet{
			"kind": "conversation-summary", "worker": "email-answerer", "thread": "cust-4711",
		},
		Content:          "The customer asked about refund windows; policy is 30 days.",
		CreatedByWorker:  "email-answerer",
		CreatedBySession: "sess-1",
	}, unitVector(3))

	if mem.ID == "" || mem.CreatedAt == 0 {
		t.Fatalf("create must stamp id and created_at: %+v", mem)
	}
	// The returned row is read back from the database, not echoed.
	got, err := s.GetMemory(ctx, project, mem.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Content != mem.Content || got.Labels["thread"] != "cust-4711" ||
		got.CreatedByWorker != "email-answerer" || got.CreatedBySession != "sess-1" ||
		got.CreatedAt != mem.CreatedAt {
		t.Fatalf("round trip lost data: %+v", got)
	}

	if _, err := s.GetMemory(ctx, project, uuid.New().String()); !errors.Is(err, ErrMemoryNotFound) {
		t.Fatalf("unknown id: want ErrMemoryNotFound, got %v", err)
	}
}

func TestMemoriesLiveCreateValidation(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	project := newLiveProject(t, s)

	tests := []struct {
		name    string
		mem     *Memory
		emb     []float32
		wantErr string
	}{
		{"nil memory", nil, nil, "memory is required"},
		{"no project", &Memory{Content: "x"}, nil, "project is required"},
		{"no content", &Memory{Project: project}, nil, "content is required"},
		{"blank content", &Memory{Project: project, Content: "   "}, nil, "content is required"},
		{"bad label key", &Memory{Project: project, Content: "x", Labels: LabelSet{"a b": "c"}}, nil, "labels"},
		{"too many labels", &Memory{Project: project, Content: "x", Labels: tooManyLabels()}, nil, "too many labels"},
		{"wrong embedding width", &Memory{Project: project, Content: "x"}, []float32{1, 2, 3}, "1536 dimensions"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.CreateMemory(ctx, tc.mem, tc.emb)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}

	// A nil embedding is a first-class case, not an error (§7.5/§7.6.5).
	if _, err := s.CreateMemory(ctx, &Memory{Project: project, Content: "no embedder configured"}, nil); err != nil {
		t.Fatalf("nil embedding must be allowed: %v", err)
	}
}

func tooManyLabels() LabelSet {
	l := LabelSet{}
	for i := 0; i <= MaxLabelsPerObject; i++ {
		l["k"+string(rune('a'+i%26))+string(rune('a'+i/26))] = "v"
	}
	return l
}

// §7.6.2: no query text ⇒ the filtered set, newest first. This is the shape the
// briefing lookup (C4) and the name= convention (§7.1) both rely on.
func TestMemoriesLiveBareSelectorIsNewestFirst(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	project := newLiveProject(t, s)

	base := int64(1_700_000_000_000)
	var ids []string
	for i, body := range []string{"oldest summary", "middle summary", "newest summary"} {
		m := mustCreateMemory(t, s, &Memory{
			Project:   project,
			Labels:    LabelSet{"kind": "rolling-summary", "worker": "archivist"},
			Content:   body,
			CreatedAt: base + int64(i)*1000,
		}, nil)
		ids = append(ids, m.ID)
	}
	// A decoy the selector must exclude.
	mustCreateMemory(t, s, &Memory{
		Project: project, Labels: LabelSet{"kind": "lesson", "worker": "archivist"},
		Content: "not a summary", CreatedAt: base + 9000,
	}, nil)

	res, err := s.SearchMemories(ctx, &MemorySearchQuery{
		Project: project, LabelSelector: "kind=rolling-summary,worker=archivist",
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	want := []string{ids[2], ids[1], ids[0]}
	if !equalStrings(resultIDs(res), want) {
		t.Fatalf("bare selector must be newest-first: got %v want %v", resultIDs(res), want)
	}
	for _, r := range res {
		if r.Score != 0 {
			t.Fatalf("a bare selector is a recency question, not a relevance one: score %v", r.Score)
		}
	}

	// limit=1 is the "current value of this name" read (memory_current, §7.3).
	res, err = s.SearchMemories(ctx, &MemorySearchQuery{
		Project: project, LabelSelector: "kind=rolling-summary,worker=archivist", Limit: 1,
	})
	if err != nil || len(res) != 1 || res[0].ID != ids[2] {
		t.Fatalf("limit 1 must give the newest: %v err=%v", resultIDs(res), err)
	}

	// No selector at all: everything in the project, newest first.
	res, err = s.SearchMemories(ctx, &MemorySearchQuery{Project: project})
	if err != nil || len(res) != 4 {
		t.Fatalf("empty selector must list the project: %d rows err=%v", len(res), err)
	}
}

// The jsonb translation must agree with the in-memory evaluator on real rows —
// every operator of the grammar, over the same seeded set.
func TestMemoriesLiveSelectorOperators(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	project := newLiveProject(t, s)

	rows := []struct {
		key    string
		labels LabelSet
	}{
		{"summary-a", LabelSet{"kind": "summary", "worker": "answerer", "thread": "t1"}},
		{"summary-b", LabelSet{"kind": "summary", "worker": "reviewer"}},
		{"lesson", LabelSet{"kind": "lesson", "worker": "answerer", "archived": "true"}},
		{"raw", LabelSet{"kind": "raw-transcript", "worker": "answerer", "thread": "t2"}},
		{"unlabeled", LabelSet{}},
	}
	byKey := map[string]string{} // key -> id
	labelsByID := map[string]LabelSet{}
	for _, r := range rows {
		m := mustCreateMemory(t, s, &Memory{Project: project, Labels: r.labels, Content: r.key}, nil)
		byKey[r.key] = m.ID
		labelsByID[m.ID] = r.labels
	}

	selectors := []string{
		"",
		"kind=summary",
		"kind!=summary",
		"kind in (summary, lesson)",
		"kind notin (raw-transcript)",
		"exists thread",
		"thread",
		"!thread",
		"!archived",
		"kind=summary,worker=answerer",
		"worker=answerer,!archived",
		"kind in (summary,lesson),thread",
		"kind=nonexistent",
	}
	for _, selText := range selectors {
		t.Run("selector="+selText, func(t *testing.T) {
			sel, err := ParseLabelSelector(selText)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			// Expectation computed independently, in Go.
			var want []string
			for id, labels := range labelsByID {
				if sel.Matches(labels) {
					want = append(want, id)
				}
			}
			sort.Strings(want)

			res, err := s.SearchMemories(ctx, &MemorySearchQuery{Project: project, LabelSelector: selText})
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			got := resultIDs(res)
			sort.Strings(got)
			if !equalStrings(got, want) {
				t.Fatalf("selector %q: SQL and Matches disagree\n got %v\nwant %v", selText, got, want)
			}
		})
	}

	// A malformed selector is a loud error, never a silently unfiltered read.
	if _, err := s.SearchMemories(ctx, &MemorySearchQuery{Project: project, LabelSelector: "kind in ("}); err == nil {
		t.Fatalf("malformed selector must error")
	}
}

// §7.6.3 — the ranking contract itself.
func TestMemoriesLiveRankingContract(t *testing.T) {
	s := openLivePG(t)
	requireVectorColumn(t, s)
	ctx := context.Background()

	t.Run("exact jargon beats a paraphrase the embedder prefers", func(t *testing.T) {
		project := newLiveProject(t, s)
		// The jargon hit: contains the project term, but its embedding is
		// unrelated to the query's.
		jargon := mustCreateMemory(t, s, &Memory{
			Project: project,
			Content: "the Zorblatt runbook lives in the orange repository",
		}, unitVector(0))
		// The paraphrase: the embedder thinks it is a perfect match, but it
		// does not contain the term.
		paraphrase := mustCreateMemory(t, s, &Memory{
			Project: project,
			Content: "instructions for shipping our software to customers",
		}, unitVector(1))

		res, err := s.SearchMemories(ctx, &MemorySearchQuery{
			Project: project, Query: "Zorblatt", QueryEmbedding: unitVector(1),
		})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if len(res) != 2 {
			t.Fatalf("expected both legs' hits, got %v", resultIDs(res))
		}
		if res[0].ID != jargon.ID {
			t.Fatalf("the exact jargon term must win: got %v (jargon=%s paraphrase=%s)",
				resultIDs(res), jargon.ID, paraphrase.ID)
		}
		// RRF, k=60: the jargon hit is ranked by both legs (1/61 + 1/62), the
		// paraphrase only by the semantic one (1/61).
		if !approx(res[0].Score, 1.0/61+1.0/62) || !approx(res[1].Score, 1.0/61) {
			t.Fatalf("RRF scores off contract: %v / %v", res[0].Score, res[1].Score)
		}
	})

	t.Run("a paraphrase is found with zero word overlap", func(t *testing.T) {
		project := newLiveProject(t, s)
		paraphrase := mustCreateMemory(t, s, &Memory{
			Project: project,
			Content: "instructions for shipping our software to customers",
		}, unitVector(1))
		mustCreateMemory(t, s, &Memory{
			Project: project,
			Content: "the Zorblatt runbook lives in the orange repository",
		}, unitVector(0))

		// No lexeme in common with the paraphrase — the keyword leg is empty
		// for it, so only the semantic leg can find it.
		query := "delivering programs to buyers"
		kwOnly, err := s.SearchMemories(ctx, &MemorySearchQuery{Project: project, Query: query})
		if err != nil {
			t.Fatalf("keyword search: %v", err)
		}
		if len(kwOnly) != 0 {
			t.Fatalf("test premise broken: query shares words with the corpus: %v", resultIDs(kwOnly))
		}

		res, err := s.SearchMemories(ctx, &MemorySearchQuery{
			Project: project, Query: query, QueryEmbedding: unitVector(1),
		})
		if err != nil {
			t.Fatalf("hybrid search: %v", err)
		}
		if len(res) == 0 || res[0].ID != paraphrase.ID {
			t.Fatalf("semantic leg must surface the paraphrase first: %v", resultIDs(res))
		}
		if !approx(res[0].Score, 1.0/61) {
			t.Fatalf("semantic-only hit should score 1/(60+1), got %v", res[0].Score)
		}
	})

	t.Run("recency breaks equal fused scores", func(t *testing.T) {
		// Two rows that each win exactly one leg: equal RRF, so the newer wins.
		// Run it twice with the timestamps swapped, so the ordering cannot be
		// an artefact of insertion order or of the id tiebreak.
		for _, newerIsKeyword := range []bool{true, false} {
			project := newLiveProject(t, s)
			base := int64(1_700_000_000_000)
			kwAt, semAt := base, base+1000
			if newerIsKeyword {
				kwAt, semAt = base+1000, base
			}
			kw := mustCreateMemory(t, s, &Memory{
				Project: project, Content: "quokka sighting log", CreatedAt: kwAt,
			}, nil) // no embedding: keyword leg only
			sem := mustCreateMemory(t, s, &Memory{
				Project: project, Content: "small hopping mammal notes", CreatedAt: semAt,
			}, unitVector(5)) // exact embedding match, no shared words

			res, err := s.SearchMemories(ctx, &MemorySearchQuery{
				Project: project, Query: "quokka", QueryEmbedding: unitVector(5),
			})
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			if len(res) != 2 || !approx(res[0].Score, res[1].Score) {
				t.Fatalf("premise: expected two equally scored hits, got %+v", res)
			}
			wantFirst := sem.ID
			if newerIsKeyword {
				wantFirst = kw.ID
			}
			if res[0].ID != wantFirst {
				t.Fatalf("newerIsKeyword=%v: recency tiebreak failed: %v", newerIsKeyword, resultIDs(res))
			}
		}
	})
}

// §7.6.5 — degradation. No query-side embedding (or a row with none) must not
// change the shape of anything; the semantic leg simply does not run.
func TestMemoriesLiveKeywordOnlyDegradation(t *testing.T) {
	s := openLivePG(t)
	requireVectorColumn(t, s)
	ctx := context.Background()
	project := newLiveProject(t, s)

	// A row with NO embedding at all (the "no provider configured" case) —
	// reachable by the keyword leg and nothing else, forever.
	nullRow := mustCreateMemory(t, s, &Memory{
		Project: project, Content: "the wombat burrow inspection checklist",
	}, nil)
	// A row both legs can reach.
	both := mustCreateMemory(t, s, &Memory{
		Project: project, Content: "wombat burrow hazards",
	}, unitVector(7))
	// A row the semantic leg alone can reach.
	semOnly := mustCreateMemory(t, s, &Memory{
		Project: project, Content: "notes on marsupial tunnels",
	}, unitVector(7))

	// Without a query embedding: keyword leg only. The paraphrase is invisible,
	// and the NULL-embedding row is perfectly findable.
	res, err := s.SearchMemories(ctx, &MemorySearchQuery{Project: project, Query: "wombat burrow"})
	if err != nil {
		t.Fatalf("keyword-only search: %v", err)
	}
	got := map[string]bool{}
	for _, r := range res {
		got[r.ID] = true
		if r.Snippet == "" || r.Score <= 0 {
			t.Fatalf("result shape must not change under degradation: %+v", r)
		}
	}
	if len(res) != 2 || !got[nullRow.ID] || !got[both.ID] {
		t.Fatalf("keyword-only degradation: got %v want the two keyword hits", resultIDs(res))
	}
	if got[semOnly.ID] {
		t.Fatalf("the semantic leg must not run without a query embedding: %v", resultIDs(res))
	}

	// With one: both legs, same result shape. The row ranked by BOTH legs
	// outranks every row ranked by only one — that is what fusion buys.
	res, err = s.SearchMemories(ctx, &MemorySearchQuery{
		Project: project, Query: "wombat burrow", QueryEmbedding: unitVector(7),
	})
	if err != nil {
		t.Fatalf("hybrid search: %v", err)
	}
	if len(res) != 3 {
		t.Fatalf("expected every row either leg reaches: %v", resultIDs(res))
	}
	if res[0].ID != both.ID {
		t.Fatalf("the two-leg hit must come first: %v (both=%s)", resultIDs(res), both.ID)
	}
	if res[0].Score <= res[1].Score {
		t.Fatalf("fused score must strictly beat single-leg scores: %+v", res)
	}

	// A wrong-width query embedding is a loud error, not a silent degradation.
	if _, err := s.SearchMemories(ctx, &MemorySearchQuery{
		Project: project, Query: "wombat", QueryEmbedding: []float32{1, 2},
	}); err == nil || !strings.Contains(err.Error(), "1536 dimensions") {
		t.Fatalf("want a dimension error, got %v", err)
	}
}

// The project namespace is hardwired: it binds before the selector, before both
// legs, and before the fusion — in code, never by the caller (§7.1, §7.6.1).
func TestMemoriesLiveProjectIsolation(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	mine := newLiveProject(t, s)
	theirs := newLiveProject(t, s)

	labels := LabelSet{"kind": "summary", "worker": "archivist"}
	content := "the quarterly zebra report is due on Friday"

	m := mustCreateMemory(t, s, &Memory{Project: mine, Labels: labels, Content: content}, unitVector(11))
	other := mustCreateMemory(t, s, &Memory{Project: theirs, Labels: labels, Content: content}, unitVector(11))

	// Bare selector.
	res, err := s.SearchMemories(ctx, &MemorySearchQuery{Project: mine, LabelSelector: "kind=summary"})
	if err != nil || len(res) != 1 || res[0].ID != m.ID {
		t.Fatalf("selector leak across projects: %v err=%v", resultIDs(res), err)
	}
	// Keyword leg.
	res, err = s.SearchMemories(ctx, &MemorySearchQuery{Project: mine, Query: "zebra report"})
	if err != nil || len(res) != 1 || res[0].ID != m.ID {
		t.Fatalf("keyword leak across projects: %v err=%v", resultIDs(res), err)
	}
	// Semantic leg (identical embeddings on both sides — only the project
	// filter can keep them apart).
	res, err = s.SearchMemories(ctx, &MemorySearchQuery{
		Project: mine, Query: "unrelated words entirely", QueryEmbedding: unitVector(11),
	})
	if err != nil || len(res) != 1 || res[0].ID != m.ID {
		t.Fatalf("semantic leak across projects: %v err=%v", resultIDs(res), err)
	}
	// Direct get: another project's id is not found, it is not "forbidden" —
	// no existence leak.
	if _, err := s.GetMemory(ctx, mine, other.ID); !errors.Is(err, ErrMemoryNotFound) {
		t.Fatalf("cross-project get: want ErrMemoryNotFound, got %v", err)
	}
	// A project is never optional.
	if _, err := s.SearchMemories(ctx, &MemorySearchQuery{Query: "zebra"}); err == nil {
		t.Fatalf("search without a project must error")
	}
}

// §7.1 — the name= convention: append a newer memory with the same name label,
// the newest match is the current value, the older ones remain as history.
func TestMemoriesLiveNameConventionIsAKV(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	project := newLiveProject(t, s)

	base := int64(1_700_000_000_000)
	var newest *Memory
	for i, body := range []string{"v1: kind, worker, thread", "v2: kind, worker, thread, ticket"} {
		newest = mustCreateMemory(t, s, &Memory{
			Project: project, Labels: LabelSet{"name": "label-registry"},
			Content: body, CreatedAt: base + int64(i)*1000,
		}, nil)
	}

	res, err := s.SearchMemories(ctx, &MemorySearchQuery{
		Project: project, LabelSelector: "name=label-registry", Limit: 1,
	})
	if err != nil || len(res) != 1 || res[0].ID != newest.ID {
		t.Fatalf("current value must be the newest match: %v err=%v", resultIDs(res), err)
	}
	// The archive lens: the same selector without a limit is the full history.
	all, err := s.SearchMemories(ctx, &MemorySearchQuery{Project: project, LabelSelector: "name=label-registry"})
	if err != nil || len(all) != 2 {
		t.Fatalf("history must survive the update: %v err=%v", resultIDs(all), err)
	}
}

// Search returns snippets; get returns everything (§7.3).
func TestMemoriesLiveSnippetsAndLimits(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	project := newLiveProject(t, s)

	long := "prologue " + strings.Repeat("transcript ", 400)
	m := mustCreateMemory(t, s, &Memory{Project: project, Content: long}, nil)

	res, err := s.SearchMemories(ctx, &MemorySearchQuery{Project: project, Query: "prologue"})
	if err != nil || len(res) != 1 {
		t.Fatalf("search: %v err=%v", resultIDs(res), err)
	}
	if len(res[0].Snippet) != memorySnippetLen {
		t.Fatalf("snippet should be capped at %d, got %d", memorySnippetLen, len(res[0].Snippet))
	}
	full, err := s.GetMemory(ctx, project, m.ID)
	if err != nil || full.Content != long {
		t.Fatalf("get must return the whole content (%d vs %d) err=%v", len(full.Content), len(long), err)
	}

	// Limit defaults and caps.
	for i := 0; i < 3; i++ {
		mustCreateMemory(t, s, &Memory{Project: project, Content: "prologue extra"}, nil)
	}
	res, err = s.SearchMemories(ctx, &MemorySearchQuery{Project: project, Query: "prologue", Limit: 2})
	if err != nil || len(res) != 2 {
		t.Fatalf("limit 2: %v err=%v", resultIDs(res), err)
	}
	res, err = s.SearchMemories(ctx, &MemorySearchQuery{Project: project, Query: "prologue", Limit: 9999})
	if err != nil || len(res) != 4 {
		t.Fatalf("oversized limit is capped, not rejected: %v err=%v", resultIDs(res), err)
	}
	// No hits ⇒ empty, non-nil.
	res, err = s.SearchMemories(ctx, &MemorySearchQuery{Project: project, Query: "xylophonectomy"})
	if err != nil || res == nil || len(res) != 0 {
		t.Fatalf("no-hit search: %#v err=%v", res, err)
	}
}

// TestMemoriesLiveNewestMemory covers the one query behind both `memory_current`
// (D3) and every briefing-section lookup (C4): the newest match of a selector,
// in FULL, project-scoped, ErrMemoryNotFound when there is nothing.
func TestMemoriesLiveNewestMemory(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	project := newLiveProject(t, s)
	other := newLiveProject(t, s)

	long := strings.Repeat("summary ", 400) // far longer than a search snippet
	base := int64(1_700_000_000_000)
	older := mustCreateMemory(t, s, &Memory{
		Project: project, Labels: LabelSet{"kind": "rolling-summary", "worker": "email-answerer"},
		Content: "the old summary", CreatedAt: base,
	}, nil)
	newest := mustCreateMemory(t, s, &Memory{
		Project: project, Labels: LabelSet{"kind": "rolling-summary", "worker": "email-answerer"},
		Content: long, CreatedAt: base + 1000,
	}, nil)
	// A same-labelled memory in another project must be invisible from here.
	mustCreateMemory(t, s, &Memory{
		Project: other, Labels: LabelSet{"kind": "rolling-summary", "worker": "email-answerer"},
		Content: "someone else's summary", CreatedAt: base + 5000,
	}, nil)

	got, err := s.NewestMemory(ctx, project, "kind=rolling-summary,worker=email-answerer")
	if err != nil {
		t.Fatalf("NewestMemory: %v", err)
	}
	if got.ID != newest.ID || got.ID == older.ID {
		t.Fatalf("got %s, want the newest match %s", got.ID, newest.ID)
	}
	// Full content, not a snippet — the whole reason this is not SearchMemories.
	if got.Content != long {
		t.Fatalf("content is %d bytes, want the full %d", len(got.Content), len(long))
	}

	// A bare (empty) selector is the newest memory in the project, full stop.
	bare, err := s.NewestMemory(ctx, project, "")
	if err != nil || bare.ID != newest.ID {
		t.Fatalf("bare selector: got %v err=%v, want %s", bare, err, newest.ID)
	}

	// Nothing matching is not an error condition, it is an answer.
	if _, err := s.NewestMemory(ctx, project, "kind=rolling-summary,worker=nobody"); !errors.Is(err, ErrMemoryNotFound) {
		t.Fatalf("missing selector err = %v, want ErrMemoryNotFound", err)
	}
	// Project isolation from the other side too.
	if _, err := s.NewestMemory(ctx, other, "name=nothing-here"); !errors.Is(err, ErrMemoryNotFound) {
		t.Fatalf("cross-project: %v", err)
	}
	// A malformed selector fails loudly rather than degrading to "no filter".
	if _, err := s.NewestMemory(ctx, project, "kind=="); err == nil {
		t.Fatalf("a malformed selector must error, not match everything")
	}
	if _, err := s.NewestMemory(ctx, "", "kind=rolling-summary"); err == nil {
		t.Fatalf("an empty project must error: the namespace is never optional")
	}
}

func approx(a, b float64) bool {
	d := a - b
	return d < 1e-9 && d > -1e-9
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
