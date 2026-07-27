package blobartifacts

import (
	"context"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/artifacts"
)

// TestIndexIsNotDurableAcrossRestart pins what this store IS: bytes in the
// BlobStore, index in an in-process map. A second store over the same blobs is
// the simulated process restart — the bytes are still there, the rows are not.
//
// This was the reproduction for the artifact-durability bug. agentd no longer
// wires this store when DATABASE_URL is set (extension/dbartifacts, whose
// TestMetadataSurvivesRestart is the same scenario passing); it is still the
// sqlite-fallback store, so the loss is pinned here rather than left implied by
// a package comment. If this test ever starts FAILING because the index became
// durable, delete it — do not "fix" it.
func TestIndexIsNotDurableAcrossRestart(t *testing.T) {
	ctx := context.Background()
	blobs := newMemBlobs()

	before := New(blobs)
	saved, err := before.Save(ctx, &artifacts.Artifact{
		SessionID:    "s1",
		FilePath:     "/workspace/report.md",
		ArtifactType: "file",
		Label:        "Report",
		Status:       artifacts.StatusLive,
		Source:       "tool",
	}, strings.NewReader("hello world"))
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	// ---- restart: a new process, same blob backend ----
	after := New(blobs)

	// The bytes survive...
	ok, err := blobs.Exists(ctx, "_artifacts/bytes/"+saved.ID)
	if err != nil || !ok {
		t.Fatalf("bytes should outlive the process: exists=%v err=%v", ok, err)
	}

	// ...and the index does not: the artifact is unlistable and unloadable,
	// its bytes orphaned in the bucket.
	list, err := after.List(ctx, "s1")
	if err != nil {
		t.Fatalf("list after restart: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("in-process index unexpectedly survived a restart: %+v", list)
	}
	if _, _, err := after.Load(ctx, saved.ID); err == nil {
		t.Fatalf("in-process index unexpectedly survived a restart for %s", saved.ID)
	}

	// Worse: the ID counter restarts too, so the next artifact written takes
	// the previous one's blob key and overwrites those orphaned bytes.
	next, err := after.Save(ctx, &artifacts.Artifact{
		SessionID: "s1", FilePath: "/other.md", Status: artifacts.StatusLive,
	}, strings.NewReader("clobber"))
	if err != nil {
		t.Fatalf("save after restart: %v", err)
	}
	if next.ID != saved.ID {
		t.Fatalf("expected the ID counter to restart and collide; got %q vs %q", next.ID, saved.ID)
	}
}
