package blobartifacts

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/artifacts"
)

// memBlobs is a tiny in-memory extension.BlobStore for tests.
type memBlobs struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemBlobs() *memBlobs { return &memBlobs{data: map[string][]byte{}} }

func (b *memBlobs) Write(_ context.Context, key string, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.data[key] = data
	b.mu.Unlock()
	return nil
}

func (b *memBlobs) Read(_ context.Context, key string) (io.ReadCloser, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	data, ok := b.data[key]
	if !ok {
		return nil, io.EOF
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (b *memBlobs) Exists(_ context.Context, key string) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.data[key]
	return ok, nil
}

func (b *memBlobs) Delete(_ context.Context, key string) error {
	b.mu.Lock()
	delete(b.data, key)
	b.mu.Unlock()
	return nil
}

func (b *memBlobs) List(_ context.Context, prefix string) ([]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for k := range b.data {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out, nil
}

func TestSaveLoad(t *testing.T) {
	as := New(newMemBlobs())
	ctx := context.Background()

	art := &artifacts.Artifact{SessionID: "s1", FilePath: "report.txt", ArtifactType: "file", Status: artifacts.StatusLive, Source: "tool"}
	saved, err := as.Save(ctx, art, bytes.NewBufferString("artifact content"))
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID == "" {
		t.Fatal("expected an ID")
	}
	if saved.Status != artifacts.StatusExtracted {
		t.Fatalf("expected Extracted, got %q", saved.Status)
	}
	if saved.FileSize != int64(len("artifact content")) {
		t.Fatalf("FileSize = %d, want %d", saved.FileSize, len("artifact content"))
	}

	list, err := as.List(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != saved.ID {
		t.Fatalf("List = %+v", list)
	}

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
	if meta.SessionID != "s1" {
		t.Fatalf("meta.SessionID = %q", meta.SessionID)
	}
}

func TestDedupKeepsIDAndLatestBytes(t *testing.T) {
	as := New(newMemBlobs())
	ctx := context.Background()
	art := &artifacts.Artifact{SessionID: "s", FilePath: "f.txt", ArtifactType: "file", Status: artifacts.StatusLive}

	first, err := as.Save(ctx, art, bytes.NewBufferString("v1"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := as.Save(ctx, art, bytes.NewBufferString("v2"))
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("dedup failed: %q != %q", first.ID, second.ID)
	}
	_, rc, _ := as.Load(ctx, first.ID)
	defer rc.Close()
	data, _ := io.ReadAll(rc)
	if string(data) != "v2" {
		t.Fatalf("expected v2, got %q", data)
	}
}

func TestMarkLostPromotesExtractedAndLosesMetaOnly(t *testing.T) {
	as := New(newMemBlobs())
	ctx := context.Background()

	withBytes := &artifacts.Artifact{SessionID: "s2", FilePath: "has_bytes.txt", ArtifactType: "file", Status: artifacts.StatusLive}
	saved1, _ := as.Save(ctx, withBytes, bytes.NewBufferString("data"))

	metaOnly := &artifacts.Artifact{SessionID: "s2", FilePath: "meta_only.txt", ArtifactType: "file", Status: artifacts.StatusLive}
	saved2, _ := as.Save(ctx, metaOnly, nil)

	if err := as.MarkLost(ctx, "s2"); err != nil {
		t.Fatal(err)
	}
	if m, _, _ := as.Load(ctx, saved1.ID); m.Status != artifacts.StatusExtracted {
		t.Fatalf("saved1 status = %q, want Extracted", m.Status)
	}
	if m, _, _ := as.Load(ctx, saved2.ID); m.Status != artifacts.StatusLost {
		t.Fatalf("saved2 status = %q, want Lost", m.Status)
	}
}

func TestSourceIsWriteOnce(t *testing.T) {
	as := New(newMemBlobs())
	ctx := context.Background()
	art := &artifacts.Artifact{SessionID: "s", FilePath: "f", Status: artifacts.StatusLive, Source: "tool"}
	if _, err := as.Save(ctx, art, nil); err != nil {
		t.Fatal(err)
	}
	art2 := &artifacts.Artifact{SessionID: "s", FilePath: "f", Status: artifacts.StatusLive, Source: "upload"}
	got, err := as.Save(ctx, art2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "tool" {
		t.Fatalf("Source = %q, want write-once 'tool'", got.Source)
	}
}

func TestCaptureFolder(t *testing.T) {
	as := New(newMemBlobs())
	ctx := context.Background()
	saved, err := as.CaptureFolder(ctx, "s3", "workspace.tar", bytes.NewBufferString("tar-bytes"))
	if err != nil {
		t.Fatal(err)
	}
	if saved.ArtifactType != "folder-capture" {
		t.Fatalf("type = %q", saved.ArtifactType)
	}
	_, rc, _ := as.Load(ctx, saved.ID)
	defer rc.Close()
	data, _ := io.ReadAll(rc)
	if string(data) != "tar-bytes" {
		t.Fatalf("got %q", data)
	}
}

func TestSaveDirWritesPrefixedBlobsAndNilReaderLoad(t *testing.T) {
	blobs := newMemBlobs()
	as := New(blobs)
	ctx := context.Background()

	art := &artifacts.Artifact{SessionID: "s1", FilePath: "skills/demo", IsDir: true, Status: artifacts.StatusLive}
	saved, err := as.Save(ctx, art, tarOf(t, map[string]string{"./SKILL.md": "hi", "./files/x.py": "yy"}))
	if err != nil {
		t.Fatal(err)
	}
	if !saved.IsDir || saved.Status != artifacts.StatusExtracted {
		t.Fatalf("unexpected: %+v", saved)
	}
	if saved.FileSize != int64(len("hi")+len("yy")) {
		t.Fatalf("FileSize = %d, want 4", saved.FileSize)
	}
	if saved.Meta["dirDigest"] == "" {
		t.Fatal("expected dirDigest in Meta")
	}
	keys, _ := blobs.List(ctx, saved.BlobPath)
	if len(keys) != 2 {
		t.Fatalf("expected 2 blobs under %q, got %v", saved.BlobPath, keys)
	}
	// Dir artifacts Load to metadata + nil reader.
	loaded, rc, err := as.Load(ctx, saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rc != nil {
		_ = rc.Close()
		t.Fatal("expected nil reader for dir artifact")
	}
	if !loaded.IsDir {
		t.Fatal("expected loaded.IsDir")
	}
}

func tarOf(t *testing.T, files map[string]string) io.Reader {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range files {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	return &buf
}
