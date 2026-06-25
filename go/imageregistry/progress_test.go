// agent-library/go/imageregistry/progress_test.go
package imageregistry

import (
	"context"
	"testing"
)

type capSink struct{ done, total int64 }

func (c *capSink) Bytes(done, total int64, _ []LayerProgress) { c.done, c.total = done, total }

func TestProgressSinkContextRoundTrip(t *testing.T) {
	if got := ProgressSinkFromContext(context.Background()); got != nil {
		t.Fatalf("expected nil sink on bare context, got %v", got)
	}
	s := &capSink{}
	ctx := WithProgressSink(context.Background(), s)
	got := ProgressSinkFromContext(ctx)
	if got == nil {
		t.Fatal("expected sink from context, got nil")
	}
	got.Bytes(7, 10, nil)
	if s.done != 7 || s.total != 10 {
		t.Fatalf("sink not wired: done=%d total=%d", s.done, s.total)
	}
}
