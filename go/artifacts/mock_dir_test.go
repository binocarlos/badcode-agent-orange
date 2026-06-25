package artifacts

import (
	"context"
	"testing"
)

func TestMockSave_DirUntarsToPerFileBlobs(t *testing.T) {
	m := NewMock()
	art := &Artifact{SessionID: "s1", FilePath: "skills/my-skill", IsDir: true, Status: StatusLive, Source: "auto"}
	tarStream := makeTar(t, map[string]string{"./SKILL.md": "hi", "./files/x.py": "y"})

	saved, err := m.Save(context.Background(), art, tarStream)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !saved.IsDir {
		t.Fatal("expected IsDir true on saved artifact")
	}
	if saved.Status != StatusExtracted {
		t.Fatalf("expected extracted, got %s", saved.Status)
	}
	if saved.FileSize != 3 { // "hi"(2) + "y"(1)
		t.Fatalf("expected total size 3, got %d", saved.FileSize)
	}
	if saved.Meta["dirDigest"] == "" {
		t.Fatal("expected dirDigest in Meta")
	}
	entries := m.DirEntries(saved.ID)
	if len(entries) != 2 {
		t.Fatalf("expected 2 dir entries, got %d", len(entries))
	}
}
