package artifacts

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"testing"
)

// makeTar builds an in-memory tar of relPath->content (entries may be prefixed "./").
func makeTar(t *testing.T, files map[string]string) io.Reader {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}

func TestWriteTarToBlobs_WritesEachFileAndManifest(t *testing.T) {
	written := map[string]string{}
	write := func(relPath string, r io.Reader) error {
		b, _ := io.ReadAll(r)
		written[relPath] = string(b)
		return nil
	}
	entries, err := WriteTarToBlobs(context.Background(), makeTar(t, map[string]string{
		"./SKILL.md":     "hello",
		"./files/run.py": "print(1)",
	}), write)
	if err != nil {
		t.Fatalf("WriteTarToBlobs: %v", err)
	}
	// Leading "./" stripped.
	if written["SKILL.md"] != "hello" || written["files/run.py"] != "print(1)" {
		t.Fatalf("unexpected written files: %#v", written)
	}
	// Manifest is sorted by RelPath.
	if len(entries) != 2 || entries[0].RelPath != "SKILL.md" || entries[1].RelPath != "files/run.py" {
		t.Fatalf("unexpected manifest order: %#v", entries)
	}
	if entries[0].Size != 5 {
		t.Fatalf("expected size 5, got %d", entries[0].Size)
	}
}

func TestWriteTarToBlobs_SkipsTraversalButKeepsDotDotPrefixedNames(t *testing.T) {
	written := map[string]string{}
	write := func(relPath string, r io.Reader) error {
		b, _ := io.ReadAll(r)
		written[relPath] = string(b)
		return nil
	}
	entries, err := WriteTarToBlobs(context.Background(), makeTar(t, map[string]string{
		"../escape.txt":      "evil",    // path traversal -> skipped
		"files/..hidden.txt": "keep me", // legitimate name -> kept
	}), write)
	if err != nil {
		t.Fatalf("WriteTarToBlobs: %v", err)
	}
	if _, ok := written["../escape.txt"]; ok {
		t.Fatalf("traversal entry must be skipped, got written: %#v", written)
	}
	if written["files/..hidden.txt"] != "keep me" {
		t.Fatalf("dotdot-prefixed filename must be written, got: %#v", written)
	}
	if len(entries) != 1 || entries[0].RelPath != "files/..hidden.txt" {
		t.Fatalf("unexpected manifest: %#v", entries)
	}
}

func TestDirDigest_StableAcrossOrder(t *testing.T) {
	a := []DirEntry{{RelPath: "b", SHA256: "22"}, {RelPath: "a", SHA256: "11"}}
	b := []DirEntry{{RelPath: "a", SHA256: "11"}, {RelPath: "b", SHA256: "22"}}
	if DirDigest(a) != DirDigest(b) {
		t.Fatal("digest must be independent of input order")
	}
	if DirDigest(a) == DirDigest([]DirEntry{{RelPath: "a", SHA256: "99"}}) {
		t.Fatal("digest must change when content changes")
	}
}
