package agentdb

import (
	"context"
	"testing"
)

// TestArtifactStatusConstantsMatchTheInterface pins the two status strings
// SaveArtifactRecord's never-regress rule branches on. agentdb cannot import
// the artifacts package (it is the lower layer), so the values are restated
// there; if artifacts.StatusExtracted ever changes, this literal is the tripwire.
func TestArtifactStatusConstantsMatchTheInterface(t *testing.T) {
	if string(statusLive) != "live" || string(statusExtracted) != "extracted" {
		t.Fatalf("status constants drifted: %q %q", statusLive, statusExtracted)
	}
}

func TestGetArtifactByPath(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Absent is (nil, nil), not an error — Save uses it to pick insert vs update.
	got, err := s.GetArtifactByPath(ctx, "s1", "/a.txt")
	if err != nil || got != nil {
		t.Fatalf("absent should be (nil, nil), got (%v, %v)", got, err)
	}
	// Empty inputs are absent, not an error.
	if got, err := s.GetArtifactByPath(ctx, "", "/a.txt"); err != nil || got != nil {
		t.Fatalf("empty session: (%v, %v)", got, err)
	}
	if got, err := s.GetArtifactByPath(ctx, "s1", ""); err != nil || got != nil {
		t.Fatalf("empty path: (%v, %v)", got, err)
	}

	saved, err := s.SaveArtifactRecord(ctx, &Artifact{SessionID: "s1", FilePath: "/a.txt"})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err = s.GetArtifactByPath(ctx, "s1", "/a.txt")
	if err != nil || got == nil || got.ID != saved.ID {
		t.Fatalf("expected the saved row back, got (%v, %v)", got, err)
	}
	// Another session's identical path is a different artifact.
	if got, err := s.GetArtifactByPath(ctx, "s2", "/a.txt"); err != nil || got != nil {
		t.Fatalf("path is scoped by session: (%v, %v)", got, err)
	}
}

func TestSaveArtifactRecord_Validation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.SaveArtifactRecord(ctx, nil); err == nil {
		t.Fatal("expected an error for a nil artifact")
	}
	if _, err := s.SaveArtifactRecord(ctx, &Artifact{FilePath: "/a"}); err == nil {
		t.Fatal("expected an error for a missing session_id")
	}
	if _, err := s.SaveArtifactRecord(ctx, &Artifact{SessionID: "s1"}); err == nil {
		t.Fatal("expected an error for a missing file_path")
	}
}

// TestSaveArtifactRecord_Invariants is the table-level statement of the
// ArtifactStore contract: dedup, no status regression, blob path preserved,
// write-once source, and a tenancy stamp that a later write cannot blank.
func TestSaveArtifactRecord_Invariants(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	first, err := s.SaveArtifactRecord(ctx, &Artifact{
		SessionID: "s1", FilePath: "/x", Customer: "acme", Status: "extracted",
		AzureBlobPath: "_artifacts/bytes/x", Source: "tool", Label: "one",
	})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if first.ID == "" {
		t.Fatal("expected a generated ID")
	}

	second, err := s.SaveArtifactRecord(ctx, &Artifact{
		SessionID: "s1", FilePath: "/x", Status: "live", Source: "upload", Label: "two",
	})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	switch {
	case second.ID != first.ID:
		t.Fatalf("dedup failed: %q != %q", second.ID, first.ID)
	case second.Status != "extracted":
		t.Fatalf("status regressed to %q", second.Status)
	case second.AzureBlobPath != "_artifacts/bytes/x":
		t.Fatalf("blob path lost: %q", second.AzureBlobPath)
	case second.Source != "tool":
		t.Fatalf("source is write-once: got %q", second.Source)
	case second.Customer != "acme":
		t.Fatalf("customer blanked by a later write: %q", second.Customer)
	case second.Label != "two":
		t.Fatalf("ordinary fields must update: %q", second.Label)
	}

	rows, err := s.ListArtifacts(ctx, "s1")
	if err != nil || len(rows) != 1 {
		t.Fatalf("expected one row after upsert, got %d (%v)", len(rows), err)
	}
}

// TestSaveArtifactRecord_MetaRoundTrip proves migration 033's column carries
// the one field of the portable type that has no column of its own.
func TestSaveArtifactRecord_MetaRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	saved, err := s.SaveArtifactRecord(ctx, &Artifact{
		SessionID: "s1", FilePath: "skills/demo", IsDir: true,
		Meta: JSONMap{"dirDigest": "sha256:abc"},
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := s.GetArtifact(ctx, saved.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Meta["dirDigest"] != "sha256:abc" {
		t.Fatalf("meta did not round-trip: %+v", got.Meta)
	}
}

// TestArtifactProjectIsolation is the §12 negative test on `agent_artifacts`:
// one project must not be able to list or get another project's artifacts.
func TestArtifactProjectIsolation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	acme, err := s.SaveArtifactRecord(ctx, &Artifact{
		SessionID: "s-acme", FilePath: "/secret", Customer: "acme",
	})
	if err != nil {
		t.Fatalf("save acme: %v", err)
	}
	if _, err := s.SaveArtifactRecord(ctx, &Artifact{
		SessionID: "s-globex", FilePath: "/own", Customer: "globex",
	}); err != nil {
		t.Fatalf("save globex: %v", err)
	}

	// NEGATIVE: another project's session lists empty — not an error, so a
	// caller cannot probe for the existence of a foreign session ID.
	cross, err := s.ListArtifactsForCustomer(ctx, "globex", "s-acme")
	if err != nil {
		t.Fatalf("cross list: %v", err)
	}
	if len(cross) != 0 {
		t.Fatalf("globex saw %d of acme's artifacts: %+v", len(cross), cross)
	}

	// NEGATIVE: another project's artifact ID reads as not-found.
	if got, err := s.GetArtifactForCustomer(ctx, "globex", acme.ID); err == nil {
		t.Fatalf("globex read acme's artifact: %+v", got)
	}

	// NEGATIVE: a project-wide listing shows only that project's rows.
	mine, err := s.ListArtifactsForCustomer(ctx, "globex", "")
	if err != nil {
		t.Fatalf("own list: %v", err)
	}
	if len(mine) != 1 || mine[0].FilePath != "/own" {
		t.Fatalf("globex should see exactly its own artifact: %+v", mine)
	}

	// POSITIVE control.
	own, err := s.ListArtifactsForCustomer(ctx, "acme", "s-acme")
	if err != nil || len(own) != 1 {
		t.Fatalf("acme should see its own artifact: %d (%v)", len(own), err)
	}
	if _, err := s.GetArtifactForCustomer(ctx, "acme", acme.ID); err != nil {
		t.Fatalf("acme should read its own artifact: %v", err)
	}
	if _, err := s.GetArtifactForCustomer(ctx, "acme", ""); err == nil {
		t.Fatal("expected an error for an empty ID")
	}
}
