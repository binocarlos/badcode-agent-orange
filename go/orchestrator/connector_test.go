package orchestrator

import (
	"context"
	"errors"
	"testing"
)

func TestFakeConnectorRecordsAndCanFail(t *testing.T) {
	ctx := context.Background()

	// Happy path: records the post and returns a deterministic ref.
	f := &FakeConnector{}
	ref, err := f.Publish(ctx, Post{Channel: "demo", Text: "hello world"})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if ref != "fake://post/1" {
		t.Fatalf("ref = %q, want fake://post/1", ref)
	}
	if len(f.Published) != 1 || f.Published[0].Text != "hello world" || f.Calls != 1 {
		t.Fatalf("did not record would-publish: %+v calls=%d", f.Published, f.Calls)
	}

	// Failure path: configured error surfaces, nothing recorded, call still counted.
	boom := errors.New("channel down")
	fail := &FakeConnector{Err: boom}
	if _, err := fail.Publish(ctx, Post{Channel: "demo", Text: "x"}); !errors.Is(err, boom) {
		t.Fatalf("expected configured error, got %v", err)
	}
	if len(fail.Published) != 0 || fail.Calls != 1 {
		t.Fatalf("failed publish must not record: %+v calls=%d", fail.Published, fail.Calls)
	}

	// It satisfies the Connector seam.
	var _ Connector = &FakeConnector{}
}
