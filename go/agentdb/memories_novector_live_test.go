package agentdb

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// RD3 — the Postgres that CANNOT hold a vector.
//
// Every other live memory test runs against a database where pgvector is
// installed, so it exercises the healthy half of the branch and proves nothing
// about the defect: `memory_create` answered `"embedded": true` while the
// INSERT had silently dropped the vector, and memories are append-only, so
// that row could never be embedded afterwards.
//
// The state is not hypothetical. Migration 022 wraps its pgvector setup in a
// DO block that swallows any failure with RAISE NOTICE — which is exactly what
// managed Postgres does when the app role may not CREATE EXTENSION (the GCP
// deployment direction). These tests force that state deliberately rather than
// hoping to meet it.
//
// How it is forced, without touching the shared database: a throwaway schema
// whose search_path excludes `public`, so the `vector` TYPE does not resolve
// and migration 022 takes its own exception path — the real mechanism, not a
// simulation of it. The column is then dropped explicitly as a belt-and-braces
// guard, because a test that quietly ran against the healthy schema would be a
// second silent success, which is the whole subject of this item.
// ---------------------------------------------------------------------------

// openLivePGNoVectorColumn returns a Store in a private schema of the test
// database with `memories.content_embedding` guaranteed absent.
func openLivePGNoVectorColumn(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("AGENTKIT_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("AGENTKIT_TEST_POSTGRES_URL not set — skipping live Postgres test")
	}
	schema := "novec_" + strings.ReplaceAll(uuid.New().String(), "-", "")[:16]

	admin, err := Open(url)
	if err != nil {
		t.Fatalf("open live postgres: %v", err)
	}
	if err := admin.DB().Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatalf("create schema %s: %v", schema, err)
	}
	t.Cleanup(func() { _ = admin.DB().Exec("DROP SCHEMA " + schema + " CASCADE").Error })

	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	s, err := Open(url + sep + "search_path=" + schema)
	if err != nil {
		t.Fatalf("open live postgres in schema %s: %v", schema, err)
	}

	// Did migration 022's own exception path fire? Recorded, not asserted: the
	// mechanism is what this setup reproduces, but the STATE is what the tests
	// need, and the drop below guarantees the state either way.
	var natural int64
	_ = s.DB().Raw(`SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'memories'
		  AND column_name = 'content_embedding'`).Scan(&natural).Error
	t.Logf("schema %s: migration 022 %s add content_embedding (search_path excludes public, so the vector type does not resolve)",
		schema, map[bool]string{true: "DID", false: "did NOT"}[natural > 0])

	// Belt and braces: whatever migration 022 managed here, this schema holds
	// no vector column by the time any test looks.
	if err := s.DB().Exec("ALTER TABLE memories DROP COLUMN IF EXISTS content_embedding").Error; err != nil {
		t.Fatalf("drop content_embedding: %v", err)
	}
	var n int64
	if err := s.DB().Raw(`SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'memories'
		  AND column_name = 'content_embedding'`).Scan(&n).Error; err != nil {
		t.Fatalf("verify column absent: %v", err)
	}
	if n != 0 {
		t.Fatalf("setup failed to produce the absent-column state (%d matching columns) — "+
			"this test would otherwise pass by testing the healthy path", n)
	}
	return s
}

// TestLivePG_MemoryCreateWithoutVectorColumn is RD3's headline: with the column
// absent, a create carrying an embedding must FAIL rather than store a row that
// can never be embedded — and must never report itself embedded.
func TestLivePG_MemoryCreateWithoutVectorColumn(t *testing.T) {
	s := openLivePGNoVectorColumn(t)
	ctx := context.Background()
	project := "proj-" + uuid.New().String()

	ok, err := s.MemoryVectorColumn(ctx)
	if err != nil || ok {
		t.Fatalf("MemoryVectorColumn = %v, %v; want false, nil", ok, err)
	}

	// A memory with no embedding is a first-class case and still works: this is
	// a supported deployment (§7.6.5), just a keyword-only one.
	stored, embedded, err := s.CreateMemory(ctx, &Memory{
		Project: project, Content: "the refund window is 30 days",
		Labels: LabelSet{"kind": "fact"},
	}, nil)
	if err != nil {
		t.Fatalf("nil embedding must still be storable here: %v", err)
	}
	if embedded {
		t.Fatalf("a row stored with no vector must not report itself embedded")
	}
	if stored.Content != "the refund window is 30 days" {
		t.Fatalf("read-back content = %q", stored.Content)
	}

	// The defect: an embedding was produced, and this database cannot hold it.
	_, embedded, err = s.CreateMemory(ctx, &Memory{
		Project: project, Content: "customers ask about refunds before anything else",
	}, unitVector(7))
	if !errors.Is(err, ErrMemoryEmbeddingUnstorable) {
		t.Fatalf("want ErrMemoryEmbeddingUnstorable, got err=%v embedded=%v — "+
			"before RD3 this stored the row without its vector and answered embedded:true, permanently",
			err, embedded)
	}
	if embedded {
		t.Fatalf("a refused create must never report embedded:true")
	}

	// …and it really refused: nothing was written.
	var count int64
	if err := s.DB().Raw("SELECT COUNT(*) FROM memories WHERE project = ?", project).Scan(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("want exactly the one keyword-only row, got %d — the refused create wrote something", count)
	}

	// The READ path still degrades rather than failing (§7.6.5): a query
	// embedding against a database with no vector column is a keyword search,
	// not an error.
	hits, err := s.SearchMemories(ctx, &MemorySearchQuery{
		Project: project, Query: "refund", QueryEmbedding: unitVector(7),
	})
	if err != nil {
		t.Fatalf("search must degrade, not fail: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("keyword leg should still find the row, got %d hits", len(hits))
	}
}

// TestLivePG_MemoryVectorProbeDoesNotLatchOnError is RD3's second route: the
// probe used to be a sync.Once over `err == nil && n > 0`, so ONE transient
// query error pinned the process to "no vector column" for its whole lifetime —
// and then blamed a deployment fact for it.
func TestLivePG_MemoryVectorProbeDoesNotLatchOnError(t *testing.T) {
	s := openLivePG(t)

	// A cancelled context is the cheapest honest transient failure: the query
	// never reaches the server, exactly as it would not during a blip.
	dead, cancel := context.WithCancel(context.Background())
	cancel()
	ok, err := s.MemoryVectorColumn(dead)
	if err == nil {
		t.Fatalf("a failed probe must return an error, not a confident false (got ok=%v)", ok)
	}
	if ok {
		t.Fatalf("a failed probe must not claim the column exists either")
	}

	// The next caller asks again, and gets the truth. Against the pre-fix
	// sync.Once this returns false forever.
	ok, err = s.MemoryVectorColumn(context.Background())
	if err != nil {
		t.Fatalf("probe after a transient failure: %v", err)
	}
	if !ok {
		t.Fatalf("the probe latched on a transient error: this database HAS content_embedding " +
			"(every other live memory test in this package relies on it) but the Store now says it does not")
	}
}

// TestLivePG_MemoryCreateReportsWhatWasStored: on a healthy database, the
// reported flag is read back out of the row rather than inferred from the
// argument — the two agree here, and the point is where the answer comes from.
func TestLivePG_MemoryCreateReportsWhatWasStored(t *testing.T) {
	s := openLivePG(t)
	requireVectorColumn(t, s)
	ctx := context.Background()
	project := newLiveProject(t, s)

	withVec, embedded, err := s.CreateMemory(ctx, &Memory{Project: project, Content: "embedded row"}, unitVector(3))
	if err != nil {
		t.Fatalf("create with embedding: %v", err)
	}
	if !embedded {
		t.Fatalf("a row stored WITH a vector must report embedded:true")
	}
	withoutVec, embedded, err := s.CreateMemory(ctx, &Memory{Project: project, Content: "keyword-only row"}, nil)
	if err != nil {
		t.Fatalf("create without embedding: %v", err)
	}
	if embedded {
		t.Fatalf("a row stored with NO vector must report embedded:false")
	}

	for _, tc := range []struct {
		id       string
		wantNull bool
	}{{withVec.ID, false}, {withoutVec.ID, true}} {
		var isNull bool
		if err := s.DB().Raw(
			"SELECT content_embedding IS NULL FROM memories WHERE id = ?", tc.id,
		).Scan(&isNull).Error; err != nil {
			t.Fatalf("read back %s: %v", tc.id, err)
		}
		if isNull != tc.wantNull {
			t.Fatalf("row %s: content_embedding IS NULL = %v, want %v", tc.id, isNull, tc.wantNull)
		}
	}
}
