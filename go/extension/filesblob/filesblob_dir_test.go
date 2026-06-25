package filesblob

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/artifacts"
)

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

func TestFilesblobSave_DirWritesPrefixedBlobs(t *testing.T) {
	dir := t.TempDir()
	bs := NewBlobStore(dir)
	as := NewArtifactStore(bs)

	art := &artifacts.Artifact{SessionID: "s1", FilePath: "skills/demo", IsDir: true, Status: artifacts.StatusLive}
	saved, err := as.Save(context.Background(), art, tarOf(t, map[string]string{"./SKILL.md": "hi", "./files/x.py": "y"}))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !saved.IsDir || saved.Status != artifacts.StatusExtracted {
		t.Fatalf("unexpected saved: %+v", saved)
	}
	// Blobs are written under the prefix (BlobPath) one per file.
	keys, err := bs.List(context.Background(), saved.BlobPath)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 blobs under prefix %q, got %v", saved.BlobPath, keys)
	}
}

func TestFilesblobLoad_DirReturnsNilReader(t *testing.T) {
	dir := t.TempDir()
	bs := NewBlobStore(dir)
	as := NewArtifactStore(bs)

	art := &artifacts.Artifact{SessionID: "s1", FilePath: "skills/demo", IsDir: true, Status: artifacts.StatusLive}
	saved, err := as.Save(context.Background(), art, tarOf(t, map[string]string{"./SKILL.md": "hi"}))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !saved.IsDir {
		t.Fatalf("expected saved.IsDir true, got %+v", saved)
	}

	loaded, rc, err := as.Load(context.Background(), saved.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded == nil {
		t.Fatalf("expected non-nil artifact")
	}
	if rc != nil {
		_ = rc.Close()
		t.Fatalf("expected nil ReadCloser for dir artifact, got %v", rc)
	}
}
