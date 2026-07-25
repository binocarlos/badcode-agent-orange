package main

// attention.go — `request_human_attention` (spec §9) and the sweep that turns a
// lapsed request into a `human.attention.timeout` event (§8.2).
//
// The whole primitive, in one sentence from the spec: agentd posts
// `{message, session_url}` to the project's attention channel, stamps the
// session and the `worker.finished` envelope with `attention_requested`, echoes
// the permalink in the tool result, and the worker ends its turn.
//
// What that deliberately is NOT: an approval gate, a draft queue, a
// pending-items UI, or any state a human must clear. The human clicks the link,
// lands in the ORDINARY chat UI, and whatever they type is the next message —
// "post it" grants permission, "change the tone" starts a conversation. Staged
// autonomy (§8.8.3) is then one sentence in a worker's prompt, and granting full
// autonomy is deleting that sentence. Do not grow machinery here.
//
// # The attention_channel shape (this file owns it — B1 stores it as opaque jsonb)
//
//	{}                                          unset → log-only fallback
//	{"kind":"webhook","url":"https://…"}        POST {message, session_url}
//	{"kind":"webhook","url":"…","headers":{"Authorization":"${SLACK_TOKEN}"}}
//
// `kind` is the discriminator and `webhook` is the only one in v1; an unknown
// kind is reported loudly (in the log and in the tool result) but never fails
// the worker's turn. Header VALUES may be whole-value `${VAR}` references
// resolved from agentd's own environment — the §4.4 rule, applied here so a
// channel credential never lands in a settings row that the UI displays. An
// unset variable is a delivery failure, not a header sent with a literal
// `${VAR}` in it.
//
// # Surfaces
//
// The reusable core is `attentionService.Request`. Today it is reachable over
// HTTP (`POST /agent/attention`, project from the JWT); when the host MCP server
// lands (D3), the `request_human_attention` tool handler is a five-line adapter
// onto the same method — the mechanics must not be implemented twice.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// ── The channel ─────────────────────────────────────────────────────────────

const (
	// attentionChannelWebhook is the only channel kind in v1.
	attentionChannelWebhook = "webhook"
	// attentionChannelNone is recorded when no channel is configured: the tool
	// still succeeds and only logs (§9).
	attentionChannelNone = "none"
)

// attentionChannel is the parsed `attention_channel` setting.
type attentionChannel struct {
	Kind    string            `json:"kind"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

// parseAttentionChannel reads the opaque jsonb column. An empty/absent config is
// not an error — it is the documented log-only fallback, so it returns a zero
// channel with a nil error.
func parseAttentionChannel(raw agentdb.JSONMap) (attentionChannel, error) {
	if len(raw) == 0 {
		return attentionChannel{}, nil
	}
	b, err := json.Marshal(map[string]any(raw))
	if err != nil {
		return attentionChannel{}, fmt.Errorf("attention_channel: %w", err)
	}
	var ch attentionChannel
	if err := json.Unmarshal(b, &ch); err != nil {
		return attentionChannel{}, fmt.Errorf("attention_channel is not an object of {kind,url,headers}: %w", err)
	}
	ch.Kind = strings.TrimSpace(strings.ToLower(ch.Kind))
	ch.URL = strings.TrimSpace(ch.URL)
	// A url with no kind is unambiguous, and refusing it would only lose a
	// notification over a missing discriminator.
	if ch.Kind == "" && ch.URL != "" {
		ch.Kind = attentionChannelWebhook
	}
	if ch.Kind == "" {
		return attentionChannel{}, nil
	}
	if ch.Kind != attentionChannelWebhook {
		return ch, fmt.Errorf("attention_channel kind %q is not supported (v1 has only %q)", ch.Kind, attentionChannelWebhook)
	}
	if ch.URL == "" {
		return ch, fmt.Errorf("attention_channel: a webhook needs a url")
	}
	if !strings.HasPrefix(ch.URL, "http://") && !strings.HasPrefix(ch.URL, "https://") {
		return ch, fmt.Errorf("attention_channel: url %q must be http(s)", ch.URL)
	}
	return ch, nil
}

// configured reports whether anything was set at all.
func (c attentionChannel) configured() bool { return c.Kind != "" }

// envRefRe matches a WHOLE-value ${VAR} reference — the §4.4 rule: a header is
// either a literal or entirely one variable, never a template with a secret
// embedded in a sentence.
var envRefRe = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)\}$`)

// resolveHeaders substitutes whole-value ${VAR} references from env. An unset or
// empty variable fails loudly: sending the header with the literal `${VAR}` in
// it would authenticate as nobody and look like a working channel.
func (c attentionChannel) resolveHeaders(env func(string) string) (map[string]string, error) {
	if len(c.Headers) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(c.Headers))
	for name, value := range c.Headers {
		m := envRefRe.FindStringSubmatch(strings.TrimSpace(value))
		if m == nil {
			out[name] = value
			continue
		}
		resolved := env(m[1])
		if strings.TrimSpace(resolved) == "" {
			return nil, fmt.Errorf("attention_channel header %q references ${%s}, which is unset in agentd's environment", name, m[1])
		}
		out[name] = resolved
	}
	return out, nil
}

// ── The service ─────────────────────────────────────────────────────────────

// attentionStore is the narrow slice of *agentdb.Store the tool and the sweep
// need. An interface so both are testable with no database.
type attentionStore interface {
	GetSession(ctx context.Context, id string) (*agentdb.Session, error)
	GetProjectSettings(ctx context.Context, project string) (*agentdb.ProjectSettings, error)
	CreateAttentionRequest(ctx context.Context, req *agentdb.AttentionRequest) (*agentdb.AttentionRequest, error)
	ListExpiredAttentionRequests(ctx context.Context, now int64, limit int) ([]*agentdb.AttentionRequest, error)
	CountUserMessagesSince(ctx context.Context, sessionID string, since int64) (int64, error)
	MarkAttentionAnswered(ctx context.Context, id string, at int64) error
	MarkAttentionTimedOut(ctx context.Context, id string, at int64) error
	CreateProjectEvent(ctx context.Context, ev *agentdb.ProjectEvent) (*agentdb.ProjectEvent, error)
}

var _ attentionStore = (*agentdb.Store)(nil)

// attentionRequestInput is one `request_human_attention(message, expires_in?)`
// call. Project is the JWT's customer claim — never a caller-supplied field.
type attentionRequestInput struct {
	Project   string
	SessionID string
	Message   string
	// ExpiresIn is the optional deadline, in seconds. 0 = no deadline: the
	// request simply waits (§9).
	ExpiresIn int64
}

// attentionResult is what the tool echoes back. `session_url` is the exact key
// every consumer of a permalink emits (F3) — the human clicks it and lands in
// the ordinary chat UI.
type attentionResult struct {
	SessionURL string `json:"session_url"`
	Message    string `json:"message"`
	Channel    string `json:"channel"`
	Delivered  bool   `json:"delivered"`
	// DeliveryError explains a channel that was configured but could not be
	// reached. The tool still succeeds — a broken webhook must not fail a
	// worker's turn — but the model is told, so its prompt can react.
	DeliveryError string `json:"delivery_error,omitempty"`
	ExpiresAt     int64  `json:"expires_at,omitempty"`
	RequestID     string `json:"request_id"`
}

// attentionService implements the §9 mechanics once, for every surface.
type attentionService struct {
	store      attentionStore
	permalinks permalinker
	// post sends the webhook; swapped in tests. Defaults to postAttentionWebhook.
	post func(ctx context.Context, ch attentionChannel, headers map[string]string, body attentionPayload) error
	env  func(string) string
	now  func() time.Time
	logf func(format string, v ...any)
}

// attentionPayload is the wire body of §9, and exactly that: `{message,
// session_url}`. Nothing else is added — a channel that wants more context
// follows the link.
type attentionPayload struct {
	Message    string `json:"message"`
	SessionURL string `json:"session_url"`
}

func newAttentionService(store attentionStore, permalinks permalinker) *attentionService {
	return &attentionService{
		store:      store,
		permalinks: permalinks,
		post:       postAttentionWebhook,
		env:        os.Getenv,
		now:        time.Now,
		logf:       log.Printf,
	}
}

// Request performs the whole of `request_human_attention` (§9).
func (a *attentionService) Request(ctx context.Context, in attentionRequestInput) (*attentionResult, error) {
	if strings.TrimSpace(in.Project) == "" {
		return nil, fmt.Errorf("no project in token")
	}
	if strings.TrimSpace(in.SessionID) == "" {
		return nil, fmt.Errorf("session_id is required")
	}
	message := strings.TrimSpace(in.Message)
	if message == "" {
		return nil, fmt.Errorf("message is required — say what you need a human for")
	}
	if in.ExpiresIn < 0 {
		return nil, fmt.Errorf("expires_in must not be negative")
	}

	// Tenancy: the session must belong to the caller's project. A session in
	// another project looks like a missing one.
	sess, err := a.store.GetSession(ctx, in.SessionID)
	if err != nil || sess == nil || sess.Customer != in.Project {
		return nil, fmt.Errorf("session not found")
	}

	sessionURL := a.permalinks.SessionURL(in.Project, in.SessionID)

	channel := attentionChannelNone
	delivered := false
	deliveryErr := ""

	settings, err := a.store.GetProjectSettings(ctx, in.Project)
	if err != nil {
		return nil, fmt.Errorf("read project settings: %w", err)
	}
	ch, parseErr := parseAttentionChannel(settings.AttentionChannel)
	switch {
	case parseErr != nil:
		// A misconfigured channel is the operator's problem, not the worker's:
		// log it, tell the model, carry on.
		deliveryErr = parseErr.Error()
		a.logf("[attention] %s: %v", in.Project, parseErr)
	case !ch.configured():
		// The documented fallback: no channel, so the request is logged and the
		// permalink still comes back. Nothing is lost — the session is stamped and
		// the thread is the review surface.
		a.logf("[attention] %s has no attention_channel — request from session %s logged only: %s (%s)",
			in.Project, in.SessionID, message, sessionURL)
	default:
		channel = ch.Kind
		headers, herr := ch.resolveHeaders(a.env)
		if herr != nil {
			deliveryErr = herr.Error()
			a.logf("[attention] %s: %v", in.Project, herr)
			break
		}
		if perr := a.post(ctx, ch, headers, attentionPayload{Message: message, SessionURL: sessionURL}); perr != nil {
			deliveryErr = perr.Error()
			a.logf("[attention] %s: webhook delivery failed: %v", in.Project, perr)
			break
		}
		delivered = true
	}

	var expiresAt int64
	if in.ExpiresIn > 0 {
		expiresAt = a.now().Unix() + in.ExpiresIn
	}
	// Recorded last so `delivered` is the truth, and in one transaction with the
	// session stamp (agentdb.CreateAttentionRequest).
	req, err := a.store.CreateAttentionRequest(ctx, &agentdb.AttentionRequest{
		Project:    in.Project,
		SessionID:  in.SessionID,
		Worker:     sess.Worker,
		Message:    message,
		SessionURL: sessionURL,
		Channel:    channel,
		Delivered:  delivered,
		ExpiresAt:  expiresAt,
	})
	if err != nil {
		return nil, fmt.Errorf("record attention request: %w", err)
	}

	return &attentionResult{
		SessionURL:    sessionURL,
		Message:       message,
		Channel:       channel,
		Delivered:     delivered,
		DeliveryError: deliveryErr,
		ExpiresAt:     expiresAt,
		RequestID:     req.ID,
	}, nil
}

// attentionWebhookTimeout bounds the POST. A slow channel must not hold a
// worker's turn open.
const attentionWebhookTimeout = 10 * time.Second

func postAttentionWebhook(ctx context.Context, ch attentionChannel, headers map[string]string, body attentionPayload) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, attentionWebhookTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ch.URL, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("attention webhook returned %s", resp.Status)
	}
	return nil
}

// ── The HTTP surface ────────────────────────────────────────────────────────

type attentionBody struct {
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
	ExpiresIn int64  `json:"expires_in"`
}

// attentionHandler serves POST /agent/attention. The project comes from the
// verified token; the session id from the body is checked against it.
func attentionHandler(svc *attentionService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id, err := identityFromRequest(r)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if id.Customer == "" {
			http.Error(w, "no project in token", http.StatusForbidden)
			return
		}
		var body attentionBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		res, err := svc.Request(r.Context(), attentionRequestInput{
			Project:   id.Customer,
			SessionID: strings.TrimSpace(body.SessionID),
			Message:   body.Message,
			ExpiresIn: body.ExpiresIn,
		})
		if err != nil {
			status := http.StatusBadRequest
			if strings.Contains(err.Error(), "not found") {
				status = http.StatusNotFound
			}
			http.Error(w, err.Error(), status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(res)
	}
}

// ── The sweep ───────────────────────────────────────────────────────────────

// attentionSweepInterval is how often lapsed requests are checked. A minute is
// plenty: `expires_in` is a fallback timer, not a deadline anyone races.
const attentionSweepInterval = time.Minute

// attentionSweeper turns lapsed requests into `human.attention.timeout` events
// (§8.2) so the *worker's prompt* decides the fallback — which is how staged
// autonomy stays a prompt pattern and no approval machinery grows (L30).
type attentionSweeper struct {
	store attentionStore
	now   func() time.Time
	logf  func(format string, v ...any)
}

func newAttentionSweeper(store attentionStore) *attentionSweeper {
	return &attentionSweeper{store: store, now: time.Now, logf: log.Printf}
}

// Run drives the sweep until ctx is cancelled.
func (s *attentionSweeper) Run(ctx context.Context) {
	t := time.NewTicker(attentionSweepInterval)
	defer t.Stop()
	s.logf("[attention] sweep running (every %s)", attentionSweepInterval)
	for {
		select {
		case <-ctx.Done():
			s.logf("[attention] sweep stopped")
			return
		case <-t.C:
			if err := s.Sweep(ctx); err != nil {
				s.logf("[attention] sweep: %v", err)
			}
		}
	}
}

// Sweep resolves every request whose deadline has passed.
//
// "Answered" needs no state machine: §9 says whatever the human types is the
// next message, so a human turn after the request IS the answer. Anything else
// lapses.
//
// A lapsed request is marked timed out BEFORE its event is emitted. That is
// at-most-once on purpose: emitting first and crashing would wake the worker
// twice for one lapse, and a duplicated "nobody answered" is worse than a
// missed one, which the next request will produce again anyway.
func (s *attentionSweeper) Sweep(ctx context.Context) error {
	now := s.now().Unix()
	due, err := s.store.ListExpiredAttentionRequests(ctx, now, 0)
	if err != nil {
		return err
	}
	for _, req := range due {
		replies, err := s.store.CountUserMessagesSince(ctx, req.SessionID, req.CreatedAt)
		if err != nil {
			s.logf("[attention] request %s: %v", req.ID, err)
			continue
		}
		if replies > 0 {
			if err := s.store.MarkAttentionAnswered(ctx, req.ID, now); err != nil {
				s.logf("[attention] request %s: %v", req.ID, err)
			}
			continue
		}
		if err := s.store.MarkAttentionTimedOut(ctx, req.ID, now); err != nil {
			s.logf("[attention] request %s: %v", req.ID, err)
			continue
		}
		if _, err := s.store.CreateProjectEvent(ctx, &agentdb.ProjectEvent{
			Project:    req.Project,
			Type:       agentdb.EventTypeHumanAttentionTimeout,
			Text:       attentionTimeoutText(req),
			OccurredAt: now,
			Envelope: agentdb.EventEnvelope{
				Source:    agentdb.EventSourceCore,
				Depth:     0,
				Worker:    req.Worker,
				SessionID: req.SessionID,
			},
		}); err != nil {
			s.logf("[attention] request %s: could not emit %s: %v", req.ID, agentdb.EventTypeHumanAttentionTimeout, err)
			continue
		}
		s.logf("[attention] request %s lapsed unanswered (session %s) — emitted %s",
			req.ID, req.SessionID, agentdb.EventTypeHumanAttentionTimeout)
	}
	return nil
}

// attentionTimeoutText is what the woken worker reads. Two facts and the
// original ask, verbatim — the prompt decides what to do about it.
func attentionTimeoutText(req *agentdb.AttentionRequest) string {
	return fmt.Sprintf(
		"No human answered a request for attention before it expired.\n\nSession: %s\nThe request was:\n%s",
		req.SessionURL, req.Message)
}
