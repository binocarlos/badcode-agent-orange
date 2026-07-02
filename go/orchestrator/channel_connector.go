package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// ErrChannelTODO fails loud until the v1 social channel is chosen
// (deployment-plan Open Decision #1: Bluesky / Mastodon are the low-friction
// candidates). Choosing it is the only work left in this file.
var ErrChannelTODO = errors.New("channel connector: target channel not yet chosen (deployment-plan Open Decision #1)")

// ChannelConnector is the ONE real Connector: a thin, isolated adapter to a
// single social channel. ALL network code in the slice lives here and nowhere
// else. It is parameterized by Endpoint + Token (injected from Secret Manager in
// Slice F — NEVER read from the board/fragments, which are versioned content).
// The specific platform is deferred: no channel is hardcoded.
type ChannelConnector struct {
	Endpoint string       // channel API base
	Token    string       // channel credential (secret; injected)
	HTTP     *http.Client // reused; overridable in tests
}

// NewChannelConnector builds the adapter with a sane default HTTP client.
func NewChannelConnector(endpoint, token string) *ChannelConnector {
	return &ChannelConnector{
		Endpoint: endpoint,
		Token:    token,
		HTTP:     &http.Client{Timeout: 15 * time.Second},
	}
}

// Publish sends one Post to the configured channel and returns the channel's
// post id/url as ref.
func (c *ChannelConnector) Publish(ctx context.Context, p Post) (string, error) {
	if c.Endpoint == "" || c.Token == "" {
		return "", fmt.Errorf("channel connector not configured (endpoint/token must be injected)")
	}
	// TODO(channel): once Open Decision #1 is made, implement the chosen channel's
	// create-post call here and ONLY here:
	//   1. marshal p.Text (v1 is text-only; p.Media deferred — Contract gap G6)
	//      into the channel's create-post request body;
	//   2. authenticate with c.Token (bearer / app-password / OAuth per channel);
	//   3. POST to c.Endpoint via c.HTTP with the ctx;
	//   4. parse the response and return the new post's id/url as ref;
	//   5. treat a non-2xx as an error (the approval flow keeps the ticket
	//      Needs-Human for retry — see approval.go).
	// Idempotency: pass p.DedupeKey (= the ticket id, §10b E-5) as the channel's
	// idempotency key when the channel offers one, so a publish retry can't
	// double-post; the ticket-state guard in ApprovalService is the backstop.
	_ = ctx
	return "", ErrChannelTODO
}

var _ Connector = &ChannelConnector{}
