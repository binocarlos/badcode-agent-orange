package filesblob

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/artifacts"
)

// ---- BlobStore tests ----

func TestBlobRoundTrip(t *testing.T) {
	bs := NewBlobStore(t.TempDir())
	ctx := context.Background()
	if err := bs.Write(ctx, "c1/a/b.txt", bytes.NewBufferString("hello")); err != nil {
		t.Fatal(err)
	}
	ok, err := bs.Exists(ctx, "c1/a/b.txt")
	if err != nil || !ok {
		t.Fatalf("exists=%v err=%v", ok, err)
	}
	rc, err := bs.Read(ctx, "c1/a/b.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != "hello" {
		t.Fatalf("got %q", got)
	}
	if err := bs.Delete(ctx, "c1/a/b.txt"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := bs.Exists(ctx, "c1/a/b.txt"); ok {
		t.Fatal("expected deleted")
	}
}

func TestBlobPathEscapeGuard(t *testing.T) {
	bs := NewBlobStore(t.TempDir())
	ctx := context.Background()
	err := bs.Write(ctx, "../../etc/passwd", bytes.NewBufferString("bad"))
	if err == nil {
		t.Fatal("expected path-escape error")
	}
}

func TestBlobExistsNotFound(t *testing.T) {
	bs := NewBlobStore(t.TempDir())
	ctx := context.Background()
	ok, err := bs.Exists(ctx, "c1/nonexistent.txt")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected not found")
	}
}

func TestBlobList(t *testing.T) {
	bs := NewBlobStore(t.TempDir())
	ctx := context.Background()
	_ = bs.Write(ctx, "prefix/a.txt", bytes.NewBufferString("a"))
	_ = bs.Write(ctx, "prefix/b.txt", bytes.NewBufferString("b"))
	_ = bs.Write(ctx, "other/c.txt", bytes.NewBufferString("c"))
	keys, err := bs.List(ctx, "prefix/")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys under prefix/, got %d: %v", len(keys), keys)
	}
}

// ---- ArtifactStore tests ----

func TestArtifactSaveLoad(t *testing.T) {
	bs := NewBlobStore(t.TempDir())
	as := NewArtifactStore(bs)
	ctx := context.Background()

	// Save an artifact with bytes.
	art := &artifacts.Artifact{
		SessionID:    "sess1",
		FilePath:     "report.txt",
		ArtifactType: "file",
		Status:       artifacts.StatusLive,
		Source:       "tool",
	}
	saved, err := as.Save(ctx, art, bytes.NewBufferString("artifact content"))
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID == "" {
		t.Fatal("expected an ID to be assigned")
	}
	if saved.Status != artifacts.StatusExtracted {
		t.Fatalf("expected Extracted after save with content, got %q", saved.Status)
	}

	// List shows it.
	list, err := as.List(ctx, "sess1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(list))
	}
	if list[0].ID != saved.ID {
		t.Fatalf("listed ID %q != saved ID %q", list[0].ID, saved.ID)
	}

	// Load returns bytes.
	meta, rc, err := as.Load(ctx, saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rc == nil {
		t.Fatal("expected non-nil reader")
	}
	defer rc.Close()
	data, _ := io.ReadAll(rc)
	if string(data) != "artifact content" {
		t.Fatalf("got %q", data)
	}
	if meta.SessionID != "sess1" {
		t.Fatalf("meta.SessionID = %q", meta.SessionID)
	}
}

func TestArtifactDedup(t *testing.T) {
	bs := NewBlobStore(t.TempDir())
	as := NewArtifactStore(bs)
	ctx := context.Background()

	art := &artifacts.Artifact{SessionID: "s", FilePath: "f.txt", ArtifactType: "file", Status: artifacts.StatusLive}
	first, err := as.Save(ctx, art, bytes.NewBufferString("v1"))
	if err != nil {
		t.Fatal(err)
	}
	// Save again with same session+path — should upsert, keeping same ID.
	second, err := as.Save(ctx, art, bytes.NewBufferString("v2"))
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("dedup failed: first=%q second=%q", first.ID, second.ID)
	}

	// Bytes should be the latest version.
	_, rc, err := as.Load(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	data, _ := io.ReadAll(rc)
	if string(data) != "v2" {
		t.Fatalf("expected v2, got %q", data)
	}
}

func TestArtifactMarkLost(t *testing.T) {
	bs := NewBlobStore(t.TempDir())
	as := NewArtifactStore(bs)
	ctx := context.Background()

	// Save one with bytes (will be Extracted, so MarkLost should NOT lose it).
	withBytes := &artifacts.Artifact{SessionID: "s2", FilePath: "has_bytes.txt", ArtifactType: "file", Status: artifacts.StatusLive}
	saved1, _ := as.Save(ctx, withBytes, bytes.NewBufferString("data"))

	// Save one metadata-only (Live, no bytes → should become Lost).
	metaOnly := &artifacts.Artifact{SessionID: "s2", FilePath: "meta_only.txt", ArtifactType: "file", Status: artifacts.StatusLive}
	saved2, _ := as.Save(ctx, metaOnly, nil)

	if err := as.MarkLost(ctx, "s2"); err != nil {
		t.Fatal(err)
	}

	// saved1 was Extracted (bytes present) — MarkLost should preserve it.
	meta1, _, _ := as.Load(ctx, saved1.ID)
	if meta1.Status != artifacts.StatusExtracted {
		t.Fatalf("expected Extracted for saved1, got %q", meta1.Status)
	}

	// saved2 was Live with no bytes — should be Lost.
	meta2, _, _ := as.Load(ctx, saved2.ID)
	if meta2.Status != artifacts.StatusLost {
		t.Fatalf("expected Lost for saved2, got %q", meta2.Status)
	}
}

func TestArtifactCaptureFolder(t *testing.T) {
	bs := NewBlobStore(t.TempDir())
	as := NewArtifactStore(bs)
	ctx := context.Background()

	// CaptureFolder saves bytes as a folder-capture artifact.
	saved, err := as.CaptureFolder(ctx, "sess3", "workspace.tar", bytes.NewBufferString("tar-bytes"))
	if err != nil {
		t.Fatal(err)
	}
	if saved.ArtifactType != "folder-capture" {
		t.Fatalf("expected folder-capture, got %q", saved.ArtifactType)
	}
	_, rc, err := as.Load(ctx, saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	data, _ := io.ReadAll(rc)
	if string(data) != "tar-bytes" {
		t.Fatalf("got %q", data)
	}
}
