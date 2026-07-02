package orchestrator

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestChannelConnectorParameterizedAndDeferred(t *testing.T) {
	ctx := context.Background()

	// Unconfigured → a clear config error (never a silent no-op, never a nil-panic).
	empty := NewChannelConnector("", "")
	if _, err := empty.Publish(ctx, Post{Channel: "x", Text: "hi"}); err == nil {
		t.Fatalf("unconfigured connector must error")
	}

	// Configured (endpoint+token injected — NOT from the board) → the channel is
	// deferred, so it fails loud with ErrChannelTODO rather than half-publishing.
	c := NewChannelConnector("https://example.invalid/api", "secret-token")
	if c.HTTP == nil {
		t.Fatalf("expected a default HTTP client")
	}
	if _, err := c.Publish(ctx, Post{Channel: "x", Text: "hi"}); !errors.Is(err, ErrChannelTODO) {
		t.Fatalf("expected ErrChannelTODO, got %v", err)
	}

	// It satisfies the Connector seam and is a *http.Client host (isolation check).
	var _ Connector = &ChannelConnector{}
	var _ *http.Client = c.HTTP
}
