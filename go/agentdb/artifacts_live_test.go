package agentdb

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// Live-Postgres coverage for the durable artifact index. The sqlite unit tests
// in artifacts_durable_test.go run on an AutoMigrated schema; only these prove
// the numbered migrations produce a table these methods can actually use —
// migration 033's jsonb `meta` column, the FK to agent_sessions, and the
// customer scoping under real SQL.
//
// Skips unless AGENTKIT_TEST_POSTGRES_URL is set (see openLivePG).

func TestLivePG_ArtifactMetaColumnExists(t *testing.T) {
	s := openLivePG(t)
	var cols []string
	if err := s.DB().Raw(
		`SELECT column_name FROM information_schema.columns
		 WHERE table_name = 'agent_artifacts' AND column_name = 'meta'`).
		Scan(&cols).Error; err != nil {
		t.Fatalf("introspect: %v", err)
	}
	if len(cols) != 1 {
		t.Fatalf("migration 033 did not add agent_artifacts.meta: %v", cols)
	}

	var idx []string
	if err := s.DB().Raw(
		`SELECT indexname FROM pg_indexes WHERE tablename = 'agent_artifacts'`).
		Scan(&idx).Error; err != nil {
		t.Fatalf("introspect indexes: %v", err)
	}
	want := map[string]bool{
		"idx_agent_artifacts_session_path": false,
		"idx_agent_artifacts_customer":     false,
	}
	for _, name := range idx {
		if _, ok := want[name]; ok {
			want[name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Fatalf("missing index %s (have %v)", name, idx)
		}
	}
}

func TestLivePG_SaveArtifactRecordRoundTrip(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	customer := "artifacts-" + uuid.New().String()[:8]
	sess := newLiveSession(t, s, customer, "a@b.c")

	saved, err := s.SaveArtifactRecord(ctx, &Artifact{
		SessionID: sess.ID, FilePath: "skills/demo", Customer: customer,
		Status: "extracted", AzureBlobPath: "_artifacts/dirs/x", Source: "tool",
		IsDir: true, FileSize: 42, Meta: JSONMap{"dirDigest": "sha256:abc"},
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := s.GetArtifact(ctx, saved.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Meta["dirDigest"] != "sha256:abc" {
		t.Fatalf("jsonb meta did not round-trip: %+v", got.Meta)
	}
	if !got.IsDir || got.FileSize != 42 || got.Customer != customer {
		t.Fatalf("row did not round-trip: %+v", got)
	}

	// Upsert on the dedup key: same row, status does not regress, source is
	// write-once, blob path preserved when the caller supplies none.
	again, err := s.SaveArtifactRecord(ctx, &Artifact{
		SessionID: sess.ID, FilePath: "skills/demo", Status: "live",
		Source: "upload", Label: "renamed",
	})
	if err != nil {
		t.Fatalf("re-save: %v", err)
	}
	switch {
	case again.ID != saved.ID:
		t.Fatalf("dedup failed: %q != %q", again.ID, saved.ID)
	case again.Status != "extracted":
		t.Fatalf("status regressed to %q", again.Status)
	case again.Source != "tool":
		t.Fatalf("source is write-once, got %q", again.Source)
	case again.AzureBlobPath != "_artifacts/dirs/x":
		t.Fatalf("blob path lost: %q", again.AzureBlobPath)
	case again.Customer != customer:
		t.Fatalf("customer blanked: %q", again.Customer)
	case again.Label != "renamed":
		t.Fatalf("ordinary fields must update: %q", again.Label)
	}

	rows, err := s.ListArtifacts(ctx, sess.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("expected one row after upsert, got %d (%v)", len(rows), err)
	}
}

// TestLivePG_ArtifactProjectIsolation is the §12 negative test on real SQL: two
// projects, two sessions, and neither can see the other's artifacts.
func TestLivePG_ArtifactProjectIsolation(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	acme := "acme-" + uuid.New().String()[:8]
	globex := "globex-" + uuid.New().String()[:8]
	acmeSess := newLiveSession(t, s, acme, "a@b.c")
	globexSess := newLiveSession(t, s, globex, "x@y.z")

	secret, err := s.SaveArtifactRecord(ctx, &Artifact{
		SessionID: acmeSess.ID, FilePath: "/secret", Customer: acme,
	})
	if err != nil {
		t.Fatalf("save acme: %v", err)
	}
	if _, err := s.SaveArtifactRecord(ctx, &Artifact{
		SessionID: globexSess.ID, FilePath: "/own", Customer: globex,
	}); err != nil {
		t.Fatalf("save globex: %v", err)
	}

	// NEGATIVE: globex asking for acme's session gets an empty list, not an
	// error — a foreign session ID must not be probeable for existence.
	cross, err := s.ListArtifactsForCustomer(ctx, globex, acmeSess.ID)
	if err != nil {
		t.Fatalf("cross list: %v", err)
	}
	if len(cross) != 0 {
		t.Fatalf("globex saw %d of acme's artifacts: %+v", len(cross), cross)
	}

	// NEGATIVE: globex asking for acme's artifact ID reads as not-found.
	if got, err := s.GetArtifactForCustomer(ctx, globex, secret.ID); err == nil {
		t.Fatalf("globex read acme's artifact: %+v", got)
	}

	// NEGATIVE: a project-wide listing shows only that project's rows.
	mine, err := s.ListArtifactsForCustomer(ctx, globex, "")
	if err != nil {
		t.Fatalf("own list: %v", err)
	}
	if len(mine) != 1 || mine[0].FilePath != "/own" {
		t.Fatalf("globex should see exactly its own artifact: %+v", mine)
	}

	// POSITIVE control.
	own, err := s.ListArtifactsForCustomer(ctx, acme, acmeSess.ID)
	if err != nil || len(own) != 1 {
		t.Fatalf("acme should see its own artifact: %d (%v)", len(own), err)
	}
	if _, err := s.GetArtifactForCustomer(ctx, acme, secret.ID); err != nil {
		t.Fatalf("acme should read its own artifact: %v", err)
	}
}

// TestLivePG_ArtifactRowsCascadeWithTheSession documents a consequence of
// putting the index in this table: `session_id` has REFERENCES
// agent_sessions(id) ON DELETE CASCADE since migration 002, so deleting a
// session deletes its artifact ROWS while the BYTES stay in the blob store.
// That is the pre-existing design of the table, not something this change
// introduced — the in-process index lost the same rows on the same delete —
// but it is the remaining orphan path and it is pinned here so it is a known
// fact rather than a surprise.
func TestLivePG_ArtifactRowsCascadeWithTheSession(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	customer := "cascade-" + uuid.New().String()[:8]
	sess := newLiveSession(t, s, customer, "a@b.c")

	if _, err := s.SaveArtifactRecord(ctx, &Artifact{
		SessionID: sess.ID, FilePath: "/doomed", Customer: customer,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := s.DeleteSession(ctx, sess.ID); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	rows, err := s.ListArtifacts(ctx, sess.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected the FK cascade to remove artifact rows, got %d", len(rows))
	}
}

// TestLivePG_SaveArtifactRecordRejectsUnknownSession pins a behaviour the
// in-process store did not have: the FK means an artifact for a session that
// does not exist is refused, rather than written into an index nothing can
// reach. Callers reach Save through a session that was created first.
func TestLivePG_SaveArtifactRecordRejectsUnknownSession(t *testing.T) {
	s := openLivePG(t)
	if _, err := s.SaveArtifactRecord(context.Background(), &Artifact{
		SessionID: uuid.New().String(), FilePath: "/nowhere",
	}); err == nil {
		t.Fatal("expected the agent_sessions FK to reject an unknown session")
	}
}
