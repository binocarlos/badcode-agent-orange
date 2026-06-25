package artifacts

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

// ctx is shared across all tests — no deadline required for in-memory ops.
var ctx = context.Background()

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// saveNoContent calls Save with nil content (metadata-only).
func saveNoContent(t *testing.T, m *MockArtifactStore, art *Artifact) *Artifact {
	t.Helper()
	out, err := m.Save(ctx, art, nil)
	if err != nil {
		t.Fatalf("Save (no content): %v", err)
	}
	return out
}

// saveWithContent calls Save with the given string as the content body.
func saveWithContent(t *testing.T, m *MockArtifactStore, art *Artifact, body string) *Artifact {
	t.Helper()
	out, err := m.Save(ctx, art, strings.NewReader(body))
	if err != nil {
		t.Fatalf("Save (with content): %v", err)
	}
	return out
}

// listByID returns the artifact from List whose ID matches, or nil.
func listByID(t *testing.T, m *MockArtifactStore, sessionID, id string) *Artifact {
	t.Helper()
	arts, err := m.List(ctx, sessionID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, a := range arts {
		if a.ID == id {
			return a
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Dedup: two Saves to the same session+path → ONE record, same ID
// ---------------------------------------------------------------------------

func TestDedupSameSessionAndPath(t *testing.T) {
	m := NewMock()

	first := saveNoContent(t, m, &Artifact{
		SessionID: "s1", FilePath: "report.html",
		Status: StatusLive, Label: "First",
	})

	second := saveNoContent(t, m, &Artifact{
		SessionID: "s1", FilePath: "report.html",
		Status: StatusLive, Label: "Second",
	})

	if first.ID != second.ID {
		t.Errorf("IDs diverged on upsert: first=%q second=%q — should be same record", first.ID, second.ID)
	}

	arts, err := m.List(ctx, "s1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(arts) != 1 {
		t.Errorf("List len = %d, want 1 (dedup)", len(arts))
	}
}

// Two different paths → two distinct records.
func TestDedupDifferentPathsAreSeparate(t *testing.T) {
	m := NewMock()
	saveNoContent(t, m, &Artifact{SessionID: "s1", FilePath: "a.html", Status: StatusLive})
	saveNoContent(t, m, &Artifact{SessionID: "s1", FilePath: "b.html", Status: StatusLive})

	arts, err := m.List(ctx, "s1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(arts) != 2 {
		t.Errorf("List len = %d, want 2 (different paths)", len(arts))
	}
}

// ---------------------------------------------------------------------------
// Never-regress: a Save with StatusLive after Extracted keeps Extracted
// ---------------------------------------------------------------------------

func TestNeverRegressExtractedToLive(t *testing.T) {
	m := NewMock()

	// First save: with content → Status becomes Extracted.
	first := saveWithContent(t, m, &Artifact{
		SessionID: "s1", FilePath: "data.csv", Status: StatusLive,
	}, "col1,col2\n1,2\n")

	if first.Status != StatusExtracted {
		t.Fatalf("after Save-with-content status = %q, want %q", first.Status, StatusExtracted)
	}

	// Second save: same key, Status=Live (caller hasn't noticed the upgrade).
	second := saveNoContent(t, m, &Artifact{
		SessionID: "s1", FilePath: "data.csv", Status: StatusLive,
	})

	if second.Status != StatusExtracted {
		t.Errorf("never-regress violated: status after re-save = %q, want %q", second.Status, StatusExtracted)
	}

	// Verify via List as well.
	listed := listByID(t, m, "s1", first.ID)
	if listed == nil {
		t.Fatal("artifact not found in List")
	}
	if listed.Status != StatusExtracted {
		t.Errorf("List shows status = %q, want %q", listed.Status, StatusExtracted)
	}
}

// ---------------------------------------------------------------------------
// Write-once Source: a later Save with a different Source does NOT overwrite
// ---------------------------------------------------------------------------

func TestWriteOnceSource(t *testing.T) {
	m := NewMock()

	saveNoContent(t, m, &Artifact{
		SessionID: "s1", FilePath: "chart.json",
		Status: StatusLive, Source: "tool",
	})

	// Second save attempts to change the source.
	second := saveNoContent(t, m, &Artifact{
		SessionID: "s1", FilePath: "chart.json",
		Status: StatusLive, Source: "upload",
	})

	if second.Source != "tool" {
		t.Errorf("Source overwritten on second Save: got %q, want %q", second.Source, "tool")
	}
}

// If no source was set on the first save, the second save can set it.
func TestWriteOnceSourceFirstSetWins(t *testing.T) {
	m := NewMock()

	saveNoContent(t, m, &Artifact{
		SessionID: "s1", FilePath: "chart.json",
		Status: StatusLive,
	})

	second := saveNoContent(t, m, &Artifact{
		SessionID: "s1", FilePath: "chart.json",
		Status: StatusLive, Source: "auto",
	})

	if second.Source != "auto" {
		t.Errorf("Source not accepted when previously unset: got %q, want %q", second.Source, "auto")
	}
}

// ---------------------------------------------------------------------------
// Save with content → Status=Extracted, BlobPath non-empty, FileSize correct
// ---------------------------------------------------------------------------

func TestSaveWithContent(t *testing.T) {
	m := NewMock()
	body := "hello, world"

	out := saveWithContent(t, m, &Artifact{
		SessionID: "s1", FilePath: "hello.txt", Status: StatusLive,
	}, body)

	if out.Status != StatusExtracted {
		t.Errorf("Status = %q, want %q", out.Status, StatusExtracted)
	}
	if out.BlobPath == "" {
		t.Error("BlobPath is empty after Save-with-content")
	}
	if out.FileSize != int64(len(body)) {
		t.Errorf("FileSize = %d, want %d", out.FileSize, len(body))
	}
}

// ---------------------------------------------------------------------------
// Load: round-trip bytes; nil reader for StatusLost
// ---------------------------------------------------------------------------

func TestLoadRoundTrip(t *testing.T) {
	m := NewMock()
	body := "artifact content bytes"

	saved := saveWithContent(t, m, &Artifact{
		SessionID: "s1", FilePath: "output.bin", Status: StatusLive,
	}, body)

	art, rc, err := m.Load(ctx, saved.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if art == nil {
		t.Fatal("Load returned nil artifact")
	}
	if rc == nil {
		t.Fatal("Load returned nil reader for extracted artifact")
	}
	defer rc.Close()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll from Load reader: %v", err)
	}
	if string(got) != body {
		t.Errorf("loaded bytes = %q, want %q", got, body)
	}
}

func TestLoadLostReturnsNilReader(t *testing.T) {
	m := NewMock()

	// Save metadata-only (no content, no BlobPath) so MarkLost will flip it to Lost.
	saved := saveNoContent(t, m, &Artifact{
		SessionID: "s1", FilePath: "missing.txt", Status: StatusLive,
	})

	if err := m.MarkLost(ctx, "s1"); err != nil {
		t.Fatalf("MarkLost: %v", err)
	}

	art, rc, err := m.Load(ctx, saved.ID)
	if err != nil {
		t.Fatalf("Load after MarkLost: %v", err)
	}
	if art == nil {
		t.Fatal("Load returned nil artifact (want metadata)")
	}
	if art.Status != StatusLost {
		t.Errorf("Status = %q, want %q", art.Status, StatusLost)
	}
	if rc != nil {
		t.Errorf("reader = non-nil for Lost artifact, want nil")
	}
}

// ---------------------------------------------------------------------------
// Load: unknown ID returns error
// ---------------------------------------------------------------------------

func TestLoadUnknownID(t *testing.T) {
	m := NewMock()
	_, _, err := m.Load(ctx, "no-such-id")
	if err == nil {
		t.Error("Load of unknown ID should return error, got nil")
	}
}

// ---------------------------------------------------------------------------
// List filters by session
// ---------------------------------------------------------------------------

func TestListFiltersToSession(t *testing.T) {
	m := NewMock()

	// Seed two sessions.
	saveNoContent(t, m, &Artifact{SessionID: "sess-A", FilePath: "a1.html", Status: StatusLive})
	saveNoContent(t, m, &Artifact{SessionID: "sess-A", FilePath: "a2.html", Status: StatusLive})
	saveNoContent(t, m, &Artifact{SessionID: "sess-B", FilePath: "b1.html", Status: StatusLive})

	listA, err := m.List(ctx, "sess-A")
	if err != nil {
		t.Fatalf("List sess-A: %v", err)
	}
	if len(listA) != 2 {
		t.Errorf("sess-A list len = %d, want 2", len(listA))
	}
	for _, a := range listA {
		if a.SessionID != "sess-A" {
			t.Errorf("unexpected SessionID %q in sess-A list", a.SessionID)
		}
	}

	listB, err := m.List(ctx, "sess-B")
	if err != nil {
		t.Fatalf("List sess-B: %v", err)
	}
	if len(listB) != 1 {
		t.Errorf("sess-B list len = %d, want 1", len(listB))
	}
	for _, a := range listB {
		if a.SessionID != "sess-B" {
			t.Errorf("unexpected SessionID %q in sess-B list", a.SessionID)
		}
	}
}

// List for an unknown session returns empty slice (not error).
func TestListEmptyForUnknownSession(t *testing.T) {
	m := NewMock()
	arts, err := m.List(ctx, "nobody")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(arts) != 0 {
		t.Errorf("List len = %d, want 0 for unknown session", len(arts))
	}
}

// ---------------------------------------------------------------------------
// MarkLost: Live+noBlobPath → Lost; Live+BlobPath → Extracted (promoted)
// ---------------------------------------------------------------------------

func TestMarkLostFlipsLiveNoBlobPath(t *testing.T) {
	m := NewMock()

	saved := saveNoContent(t, m, &Artifact{
		SessionID: "s1", FilePath: "gone.txt", Status: StatusLive,
	})
	// Confirm no BlobPath was set (metadata-only, no content upload).
	if saved.BlobPath != "" {
		t.Fatalf("precondition: BlobPath should be empty, got %q", saved.BlobPath)
	}

	if err := m.MarkLost(ctx, "s1"); err != nil {
		t.Fatalf("MarkLost: %v", err)
	}

	listed := listByID(t, m, "s1", saved.ID)
	if listed == nil {
		t.Fatal("artifact not found after MarkLost")
	}
	if listed.Status != StatusLost {
		t.Errorf("Status = %q, want %q", listed.Status, StatusLost)
	}
}

func TestMarkLostPromotesLiveWithBlobPath(t *testing.T) {
	m := NewMock()

	// Save with content so it gets a BlobPath.
	saved := saveWithContent(t, m, &Artifact{
		SessionID: "s1", FilePath: "safe.json", Status: StatusLive,
	}, `{"key":"value"}`)

	if saved.BlobPath == "" {
		t.Fatal("precondition: BlobPath must be non-empty after content upload")
	}

	// Temporarily downgrade status to Live for the test to exercise the promotion path.
	// This simulates a record that was uploaded but whose status was reset (e.g. race).
	// We do this by saving again without content but keeping status Live; since never-regress
	// already enforces Extracted, we need to verify from the MarkLost perspective directly.
	// Instead: create a fresh artifact that was saved Live WITH a BlobPath set explicitly.
	m2 := NewMock()
	liveWithBlob := saveNoContent(t, m2, &Artifact{
		SessionID: "s1", FilePath: "safe.json",
		Status:   StatusLive,
		BlobPath: "mock-blob/already-uploaded",
	})
	if liveWithBlob.Status != StatusLive {
		t.Fatalf("precondition: Status = %q, want Live for promotion test", liveWithBlob.Status)
	}
	if liveWithBlob.BlobPath == "" {
		t.Fatal("precondition: BlobPath must be non-empty")
	}

	if err := m2.MarkLost(ctx, "s1"); err != nil {
		t.Fatalf("MarkLost: %v", err)
	}

	listed := listByID(t, m2, "s1", liveWithBlob.ID)
	if listed == nil {
		t.Fatal("artifact not found after MarkLost")
	}
	if listed.Status != StatusExtracted {
		t.Errorf("MarkLost-with-blob: Status = %q, want %q (bytes safe — promoted, not lost)", listed.Status, StatusExtracted)
	}
}

// MarkLost must not touch already-Extracted or already-Lost rows.
func TestMarkLostIgnoresNonLiveRows(t *testing.T) {
	m := NewMock()

	extracted := saveWithContent(t, m, &Artifact{
		SessionID: "s1", FilePath: "already-extracted.txt", Status: StatusLive,
	}, "bytes")
	if extracted.Status != StatusExtracted {
		t.Fatalf("precondition: expected Extracted, got %q", extracted.Status)
	}

	if err := m.MarkLost(ctx, "s1"); err != nil {
		t.Fatalf("MarkLost: %v", err)
	}

	listed := listByID(t, m, "s1", extracted.ID)
	if listed == nil {
		t.Fatal("artifact not found")
	}
	if listed.Status != StatusExtracted {
		t.Errorf("MarkLost changed already-Extracted status to %q", listed.Status)
	}
}

// MarkLost on a different session does not affect the session in question.
func TestMarkLostIsolatedToSession(t *testing.T) {
	m := NewMock()

	artA := saveNoContent(t, m, &Artifact{SessionID: "sess-A", FilePath: "f.txt", Status: StatusLive})
	artB := saveNoContent(t, m, &Artifact{SessionID: "sess-B", FilePath: "f.txt", Status: StatusLive})

	// Only mark sess-B lost.
	if err := m.MarkLost(ctx, "sess-B"); err != nil {
		t.Fatalf("MarkLost: %v", err)
	}

	listedA := listByID(t, m, "sess-A", artA.ID)
	if listedA == nil || listedA.Status != StatusLive {
		t.Errorf("sess-A artifact status = %v after marking sess-B lost — should be unaffected", listedA)
	}

	listedB := listByID(t, m, "sess-B", artB.ID)
	if listedB == nil || listedB.Status != StatusLost {
		t.Errorf("sess-B artifact status = %v, want Lost", listedB)
	}
}

// ---------------------------------------------------------------------------
// CaptureFolder: generalised folder/file-set capture
// ---------------------------------------------------------------------------

func TestCaptureFolderSavesContent(t *testing.T) {
	m := NewMock()
	content := "file1.txt: hello\nfile2.txt: world"

	art, err := m.CaptureFolder(ctx, "sess-1", "my-folder.tar", strings.NewReader(content))
	if err != nil {
		t.Fatalf("CaptureFolder: %v", err)
	}
	if art == nil {
		t.Fatal("CaptureFolder returned nil artifact")
	}
	if art.SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want %q", art.SessionID, "sess-1")
	}
	if art.FilePath != "my-folder.tar" {
		t.Errorf("FilePath = %q, want %q", art.FilePath, "my-folder.tar")
	}
	if art.ArtifactType != "folder-capture" {
		t.Errorf("ArtifactType = %q, want folder-capture", art.ArtifactType)
	}
	if art.Status != StatusExtracted {
		t.Errorf("Status = %q, want %q", art.Status, StatusExtracted)
	}
	if art.FileSize != int64(len(content)) {
		t.Errorf("FileSize = %d, want %d", art.FileSize, len(content))
	}

	// Verify bytes round-trip through Load.
	_, rc, err := m.Load(ctx, art.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if rc == nil {
		t.Fatal("Load returned nil reader for captured folder artifact")
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != content {
		t.Errorf("loaded bytes = %q, want %q", got, content)
	}
}

func TestCaptureFolderDeduplicatesOnSessionAndName(t *testing.T) {
	m := NewMock()

	first, err := m.CaptureFolder(ctx, "s1", "cap.tar", strings.NewReader("v1"))
	if err != nil {
		t.Fatalf("CaptureFolder first: %v", err)
	}
	second, err := m.CaptureFolder(ctx, "s1", "cap.tar", strings.NewReader("v2"))
	if err != nil {
		t.Fatalf("CaptureFolder second: %v", err)
	}
	// Same name → same ID (dedup on session+path).
	if first.ID != second.ID {
		t.Errorf("IDs diverged: first=%q second=%q — should be same record (dedup)", first.ID, second.ID)
	}
}

// ---------------------------------------------------------------------------
// MockArtifactStore satisfies ArtifactStore at compile time
// (also asserted in mock.go, but belt-and-suspenders here)
// ---------------------------------------------------------------------------

var _ ArtifactStore = (*MockArtifactStore)(nil)

// ---------------------------------------------------------------------------
// Load returns bytes with non-empty content reader (bytes.NewReader check)
// ---------------------------------------------------------------------------

func TestLoadContentReaderBytes(t *testing.T) {
	m := NewMock()
	body := "the quick brown fox"

	saved := saveWithContent(t, m, &Artifact{
		SessionID: "s1", FilePath: "fox.txt", Status: StatusLive,
	}, body)

	_, rc, err := m.Load(ctx, saved.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if rc == nil {
		t.Fatal("reader is nil for extracted artifact")
	}
	defer rc.Close()

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, rc); err != nil {
		t.Fatalf("io.Copy: %v", err)
	}
	if buf.String() != body {
		t.Errorf("content = %q, want %q", buf.String(), body)
	}
}
