package agentdb

import (
	"context"
	"testing"
)

func TestCreateArtifact(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.CreateArtifact(ctx, &Artifact{FilePath: "/x"}); err == nil {
		t.Fatalf("expected error for missing session_id")
	}

	a, err := s.CreateArtifact(ctx, &Artifact{SessionID: "s1", FilePath: "/workspace/a.txt", FileSize: 12})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if a.ID == "" {
		t.Fatalf("expected generated ID")
	}

	got, err := s.GetArtifact(ctx, a.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.FilePath != "/workspace/a.txt" || got.FileSize != 12 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestGetArtifact_Errors(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.GetArtifact(ctx, ""); err == nil {
		t.Fatalf("expected error for empty id")
	}
	if _, err := s.GetArtifact(ctx, "missing"); err == nil {
		t.Fatalf("expected not-found error")
	}
}

func TestUpdateArtifact(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.UpdateArtifact(ctx, &Artifact{}); err == nil {
		t.Fatalf("expected error for empty id")
	}

	a, err := s.CreateArtifact(ctx, &Artifact{SessionID: "s1", FilePath: "/a", Status: "live"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	a.Status = "extracted"
	a.AzureBlobPath = "blobs/a"
	if _, err := s.UpdateArtifact(ctx, a); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := s.GetArtifact(ctx, a.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != "extracted" || got.AzureBlobPath != "blobs/a" {
		t.Fatalf("update not persisted: %+v", got)
	}
}

func TestCreateArtifacts_Batch(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateArtifacts(ctx, nil); err != nil {
		t.Fatalf("empty batch must be a no-op: %v", err)
	}

	batch := []*Artifact{
		{SessionID: "s1", FilePath: "/a"},
		{SessionID: "s1", FilePath: "/b"},
	}
	if err := s.CreateArtifacts(ctx, batch); err != nil {
		t.Fatalf("batch create: %v", err)
	}
	for i, a := range batch {
		if a.ID == "" {
			t.Fatalf("artifact %d: expected generated ID", i)
		}
	}
	rows, err := s.ListArtifacts(ctx, "s1")
	if err != nil || len(rows) != 2 {
		t.Fatalf("expected 2 artifacts, got %d (err %v)", len(rows), err)
	}
}

func TestMarkArtifactsExtracted(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.MarkArtifactsExtracted(ctx, ""); err == nil {
		t.Fatalf("expected error for empty session_id")
	}

	seed := []*Artifact{
		{ID: "live1", SessionID: "s1", FilePath: "/a", Status: "live"},
		{ID: "lost1", SessionID: "s1", FilePath: "/b", Status: "lost"},   // non-live untouched
		{ID: "live2", SessionID: "s2", FilePath: "/c", Status: "live"},   // other session untouched
	}
	if err := s.CreateArtifacts(ctx, seed); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := s.MarkArtifactsExtracted(ctx, "s1"); err != nil {
		t.Fatalf("mark: %v", err)
	}
	wantStatus := map[string]string{"live1": "extracted", "lost1": "lost", "live2": "live"}
	for id, want := range wantStatus {
		got, err := s.GetArtifact(ctx, id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if got.Status != want {
			t.Fatalf("%s: want status %q, got %q", id, want, got.Status)
		}
	}
}

func TestMarkArtifactsLost(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.MarkArtifactsLost(ctx, ""); err == nil {
		t.Fatalf("expected error for empty session_id")
	}

	seed := []*Artifact{
		{ID: "backed", SessionID: "s1", FilePath: "/a", Status: "live", AzureBlobPath: "blobs/a"},
		{ID: "naked", SessionID: "s1", FilePath: "/b", Status: "live", AzureBlobPath: ""},
		{ID: "done", SessionID: "s1", FilePath: "/c", Status: "extracted", AzureBlobPath: ""},
		{ID: "other", SessionID: "s2", FilePath: "/d", Status: "live", AzureBlobPath: ""},
	}
	if err := s.CreateArtifacts(ctx, seed); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// A live row with a NULL blob path (legacy rows) counts as lost too.
	if err := s.gdb.Exec(`INSERT INTO agent_artifacts (id, session_id, file_path, status, azure_blob_path) VALUES ('legacy', 's1', '/e', 'live', NULL)`).Error; err != nil {
		t.Fatalf("seed legacy: %v", err)
	}

	if err := s.MarkArtifactsLost(ctx, "s1"); err != nil {
		t.Fatalf("mark lost: %v", err)
	}
	wantStatus := map[string]string{
		"backed": "extracted", // blob-backed live rows are safe → extracted
		"naked":  "lost",      // no blob copy → lost
		"legacy": "lost",      // NULL blob path → lost
		"done":   "extracted", // already extracted, untouched
		"other":  "live",      // other session untouched
	}
	for id, want := range wantStatus {
		got, err := s.GetArtifact(ctx, id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if got.Status != want {
			t.Fatalf("%s: want status %q, got %q", id, want, got.Status)
		}
	}
}

// TestMarkArtifactsLost_PropagatesExtractedUpdateError fault-injects a failure
// into the FIRST statement of MarkArtifactsLost (the live+blob → extracted
// update) via a sqlite trigger, while the second statement succeeds. The
// function must NOT report success when half its work failed — otherwise a
// blob-backed artifact silently stays "live" for a session that is gone.
func TestMarkArtifactsLost_PropagatesExtractedUpdateError(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateArtifacts(ctx, []*Artifact{
		{ID: "backed", SessionID: "s1", FilePath: "/a", Status: "live", AzureBlobPath: "blobs/a"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.gdb.Exec(`
		CREATE TRIGGER block_extracted BEFORE UPDATE ON agent_artifacts
		WHEN NEW.status = 'extracted'
		BEGIN SELECT RAISE(ABORT, 'injected: extracted update rejected'); END
	`).Error; err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	err := s.MarkArtifactsLost(ctx, "s1")
	if err == nil {
		t.Fatalf("MarkArtifactsLost reported success although the extracted-update failed (artifact left 'live')")
	}
	got, gerr := s.GetArtifact(ctx, "backed")
	if gerr != nil {
		t.Fatalf("get: %v", gerr)
	}
	if got.Status != "live" {
		t.Fatalf("sanity: injected failure should have left status live, got %q", got.Status)
	}
}
