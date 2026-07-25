package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// ── Fake store ──────────────────────────────────────────────────────────────

type fakeAttentionStore struct {
	sessions map[string]*agentdb.Session
	settings map[string]*agentdb.ProjectSettings
	requests []*agentdb.AttentionRequest
	messages map[string][]int64 // session id → unix seconds of each USER message
	events   []*agentdb.ProjectEvent

	createErr error
	seq       int
}

func newFakeAttentionStore() *fakeAttentionStore {
	return &fakeAttentionStore{
		sessions: map[string]*agentdb.Session{},
		settings: map[string]*agentdb.ProjectSettings{},
		messages: map[string][]int64{},
	}
}

func (f *fakeAttentionStore) addSession(id, project, worker string) *agentdb.Session {
	s := &agentdb.Session{ID: id, Customer: project, Worker: worker}
	f.sessions[id] = s
	return s
}

func (f *fakeAttentionStore) GetSession(_ context.Context, id string) (*agentdb.Session, error) {
	s, ok := f.sessions[id]
	if !ok {
		return nil, fmt.Errorf("session not found")
	}
	return s, nil
}

func (f *fakeAttentionStore) GetProjectSettings(_ context.Context, project string) (*agentdb.ProjectSettings, error) {
	if ps, ok := f.settings[project]; ok {
		return ps, nil
	}
	return agentdb.DefaultProjectSettings(project), nil
}

func (f *fakeAttentionStore) CreateAttentionRequest(_ context.Context, req *agentdb.AttentionRequest) (*agentdb.AttentionRequest, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.seq++
	req.ID = fmt.Sprintf("att-%d", f.seq)
	if req.CreatedAt == 0 {
		req.CreatedAt = 1000
	}
	if s, ok := f.sessions[req.SessionID]; ok {
		s.AttentionRequested = true
	}
	f.requests = append(f.requests, req)
	return req, nil
}

func (f *fakeAttentionStore) ListExpiredAttentionRequests(_ context.Context, now int64, _ int) ([]*agentdb.AttentionRequest, error) {
	out := []*agentdb.AttentionRequest{}
	for _, r := range f.requests {
		if r.ExpiresAt > 0 && r.ExpiresAt <= now && r.AnsweredAt == 0 && r.TimedOutAt == 0 {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeAttentionStore) CountUserMessagesSince(_ context.Context, sessionID string, since int64) (int64, error) {
	var n int64
	for _, ts := range f.messages[sessionID] {
		if ts >= since {
			n++
		}
	}
	return n, nil
}

func (f *fakeAttentionStore) resolve(id string, answered bool, at int64) error {
	for _, r := range f.requests {
		if r.ID != id {
			continue
		}
		if r.AnsweredAt != 0 || r.TimedOutAt != 0 {
			return nil
		}
		if answered {
			r.AnsweredAt = at
		} else {
			r.TimedOutAt = at
		}
		if s, ok := f.sessions[r.SessionID]; ok {
			s.AttentionRequested = false
		}
		return nil
	}
	return fmt.Errorf("attention request not found")
}

func (f *fakeAttentionStore) MarkAttentionAnswered(_ context.Context, id string, at int64) error {
	return f.resolve(id, true, at)
}

func (f *fakeAttentionStore) MarkAttentionTimedOut(_ context.Context, id string, at int64) error {
	return f.resolve(id, false, at)
}

func (f *fakeAttentionStore) CreateProjectEvent(_ context.Context, ev *agentdb.ProjectEvent) (*agentdb.ProjectEvent, error) {
	f.seq++
	ev.ID = fmt.Sprintf("ev-%d", f.seq)
	f.events = append(f.events, ev)
	return ev, nil
}

// newTestAttentionService wires the service with a captured webhook and a pinned
// clock. The permalinker is the real one, so the emitted URL is the real format.
func newTestAttentionService(store attentionStore, env map[string]string) (*attentionService, *[]attentionPayload, *[]map[string]string) {
	var posts []attentionPayload
	var headers []map[string]string
	svc := newAttentionService(store, permalinker{base: "https://orange.example.com"})
	svc.now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	svc.logf = func(string, ...any) {}
	svc.env = func(k string) string { return env[k] }
	svc.post = func(_ context.Context, _ attentionChannel, h map[string]string, body attentionPayload) error {
		posts = append(posts, body)
		headers = append(headers, h)
		return nil
	}
	return svc, &posts, &headers
}

// ── The tool (§9) ───────────────────────────────────────────────────────────

// TestRequestHumanAttentionPostsToTheChannel pins the whole §9 mechanic: the
// webhook body is EXACTLY {message, session_url}, the session is stamped, and
// the tool result echoes the permalink under the key `session_url` (F3).
func TestRequestHumanAttentionPostsToTheChannel(t *testing.T) {
	store := newFakeAttentionStore()
	sess := store.addSession("s-1", "acme", "tweet-author")
	settings := agentdb.DefaultProjectSettings("acme")
	settings.AttentionChannel = agentdb.JSONMap{
		"kind": "webhook", "url": "https://hooks.example.com/badcode",
		"headers": map[string]any{"Authorization": "${ATTENTION_TOKEN}", "X-Fixed": "literal"},
	}
	store.settings["acme"] = settings

	svc, posts, headers := newTestAttentionService(store, map[string]string{"ATTENTION_TOKEN": "shhh"})
	res, err := svc.Request(context.Background(), attentionRequestInput{
		Project: "acme", SessionID: "s-1", Message: "sign off on this draft before I post it",
	})
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	if res.SessionURL != "https://orange.example.com/p/acme/s/s-1" {
		t.Fatalf("permalink: got %q", res.SessionURL)
	}
	if !res.Delivered || res.Channel != attentionChannelWebhook || res.DeliveryError != "" {
		t.Fatalf("expected a delivered webhook: %+v", res)
	}
	if len(*posts) != 1 {
		t.Fatalf("expected one post, got %d", len(*posts))
	}
	if (*posts)[0].Message != "sign off on this draft before I post it" ||
		(*posts)[0].SessionURL != res.SessionURL {
		t.Fatalf("payload: %+v", (*posts)[0])
	}
	// §9 says {message, session_url} — and that is all that goes on the wire.
	b, _ := json.Marshal((*posts)[0])
	var keys map[string]any
	_ = json.Unmarshal(b, &keys)
	if len(keys) != 2 || keys["message"] == nil || keys["session_url"] == nil {
		t.Fatalf("the webhook body must be exactly {message, session_url}: %s", b)
	}
	// ${VAR} headers resolve from agentd's environment; literals pass through.
	if (*headers)[0]["Authorization"] != "shhh" || (*headers)[0]["X-Fixed"] != "literal" {
		t.Fatalf("headers: %+v", (*headers)[0])
	}

	// The session carries the §9 stamp that §8.2 copies onto worker.finished.
	if !sess.AttentionRequested {
		t.Fatalf("the session must be stamped attention_requested")
	}
	if len(store.requests) != 1 || store.requests[0].Worker != "tweet-author" {
		t.Fatalf("the request must be recorded with its worker: %+v", store.requests)
	}
	if store.requests[0].ExpiresAt != 0 {
		t.Fatalf("expires_in is optional; without it there is no deadline: %+v", store.requests[0])
	}
}

// TestRequestHumanAttentionLogsOnlyWithoutAChannel is the documented fallback:
// no channel configured, the tool still succeeds, the permalink still comes
// back, and the session is still stamped (§9).
func TestRequestHumanAttentionLogsOnlyWithoutAChannel(t *testing.T) {
	store := newFakeAttentionStore()
	sess := store.addSession("s-1", "acme", "tweet-author")

	var logged []string
	svc, posts, _ := newTestAttentionService(store, nil)
	svc.logf = func(format string, v ...any) { logged = append(logged, fmt.Sprintf(format, v...)) }

	res, err := svc.Request(context.Background(), attentionRequestInput{
		Project: "acme", SessionID: "s-1", Message: "please look at this",
	})
	if err != nil {
		t.Fatalf("an unset channel must not fail the tool: %v", err)
	}
	if len(*posts) != 0 {
		t.Fatalf("nothing to post to")
	}
	if res.Channel != attentionChannelNone || res.Delivered {
		t.Fatalf("expected the log-only fallback: %+v", res)
	}
	if res.SessionURL == "" {
		t.Fatalf("the permalink is the point — it must come back even with no channel")
	}
	if !sess.AttentionRequested {
		t.Fatalf("the stamp does not depend on the channel")
	}
	if len(logged) == 0 || !strings.Contains(strings.Join(logged, "\n"), "logged only") {
		t.Fatalf("the fallback must log: %v", logged)
	}
}

// TestRequestHumanAttentionSurvivesABrokenChannel: a channel that is
// misconfigured or unreachable is reported, never fatal — a worker's turn must
// not fail because an operator typo'd a URL.
func TestRequestHumanAttentionSurvivesABrokenChannel(t *testing.T) {
	tests := []struct {
		name    string
		channel agentdb.JSONMap
		env     map[string]string
		post    error
		wantErr string
	}{
		{
			name:    "unknown kind",
			channel: agentdb.JSONMap{"kind": "carrier-pigeon", "url": "https://x"},
			wantErr: "not supported",
		},
		{
			name:    "webhook with no url",
			channel: agentdb.JSONMap{"kind": "webhook"},
			wantErr: "needs a url",
		},
		{
			name:    "non-http url",
			channel: agentdb.JSONMap{"kind": "webhook", "url": "ftp://nope"},
			wantErr: "must be http(s)",
		},
		{
			name:    "unset credential",
			channel: agentdb.JSONMap{"kind": "webhook", "url": "https://x", "headers": map[string]any{"Authorization": "${MISSING}"}},
			wantErr: "unset in agentd's environment",
		},
		{
			name:    "post failed",
			channel: agentdb.JSONMap{"kind": "webhook", "url": "https://x"},
			post:    fmt.Errorf("connection refused"),
			wantErr: "connection refused",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeAttentionStore()
			sess := store.addSession("s-1", "acme", "tweet-author")
			settings := agentdb.DefaultProjectSettings("acme")
			settings.AttentionChannel = tc.channel
			store.settings["acme"] = settings

			svc, _, _ := newTestAttentionService(store, tc.env)
			if tc.post != nil {
				svc.post = func(context.Context, attentionChannel, map[string]string, attentionPayload) error { return tc.post }
			}
			res, err := svc.Request(context.Background(), attentionRequestInput{
				Project: "acme", SessionID: "s-1", Message: "look at this",
			})
			if err != nil {
				t.Fatalf("a broken channel must not fail the worker's turn: %v", err)
			}
			if res.Delivered {
				t.Fatalf("nothing was delivered: %+v", res)
			}
			if !strings.Contains(res.DeliveryError, tc.wantErr) {
				t.Fatalf("want %q in delivery_error, got %q", tc.wantErr, res.DeliveryError)
			}
			// The stamp and the permalink are unconditional: the thread is the
			// review surface even when the ping never arrived.
			if !sess.AttentionRequested || res.SessionURL == "" {
				t.Fatalf("stamp/permalink must be unconditional: %+v", res)
			}
		})
	}
}

// TestRequestHumanAttentionValidates covers §9's fail-loudly rules and the
// tenancy boundary (project from the token, never a request field).
func TestRequestHumanAttentionValidates(t *testing.T) {
	store := newFakeAttentionStore()
	store.addSession("s-1", "acme", "tweet-author")
	store.addSession("s-other", "globex", "tweet-author")
	svc, _, _ := newTestAttentionService(store, nil)

	tests := []struct {
		name    string
		in      attentionRequestInput
		wantErr string
	}{
		{"no project", attentionRequestInput{SessionID: "s-1", Message: "m"}, "no project"},
		{"no session", attentionRequestInput{Project: "acme", Message: "m"}, "session_id is required"},
		{"empty message", attentionRequestInput{Project: "acme", SessionID: "s-1", Message: "   "}, "message is required"},
		{"negative expiry", attentionRequestInput{Project: "acme", SessionID: "s-1", Message: "m", ExpiresIn: -5}, "must not be negative"},
		{"unknown session", attentionRequestInput{Project: "acme", SessionID: "nope", Message: "m"}, "session not found"},
		// The isolation case: a real session, in another project.
		{"cross-project session", attentionRequestInput{Project: "acme", SessionID: "s-other", Message: "m"}, "session not found"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.Request(context.Background(), tc.in); err == nil ||
				!strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want %q, got %v", tc.wantErr, err)
			}
		})
	}
	if len(store.requests) != 0 {
		t.Fatalf("a refused call must record nothing: %+v", store.requests)
	}
}

// TestRequestHumanAttentionExpiresIn stores the deadline the sweep reads.
func TestRequestHumanAttentionExpiresIn(t *testing.T) {
	store := newFakeAttentionStore()
	store.addSession("s-1", "acme", "tweet-author")
	svc, _, _ := newTestAttentionService(store, nil)

	res, err := svc.Request(context.Background(), attentionRequestInput{
		Project: "acme", SessionID: "s-1", Message: "sign off", ExpiresIn: 3600,
	})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	want := int64(1_800_000_000 + 3600)
	if res.ExpiresAt != want || store.requests[0].ExpiresAt != want {
		t.Fatalf("expires_at: want %d, got %d / %d", want, res.ExpiresAt, store.requests[0].ExpiresAt)
	}
}

// TestRequestHumanAttentionHTTP covers the route: project from the JWT, the
// permalink echoed under `session_url`.
func TestRequestHumanAttentionHTTP(t *testing.T) {
	store := newFakeAttentionStore()
	store.addSession("s-1", "acme", "tweet-author")
	svc, _, _ := newTestAttentionService(store, nil)
	h := attentionHandler(svc)

	req := httptest.NewRequest("POST", "/agent/attention",
		strings.NewReader(`{"session_id":"s-1","message":"sign off","expires_in":60}`))
	req = req.WithContext(contextWithPrincipal(req.Context(), principal{email: "u@x.com", customer: "acme"}))
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d body=%s", rec.Code, rec.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body)
	}
	if body["session_url"] != "https://orange.example.com/p/acme/s/s-1" {
		t.Fatalf("the JSON key must be exactly session_url: %s", rec.Body)
	}

	// Another project's session is a 404, not a 403 and never a write.
	req = httptest.NewRequest("POST", "/agent/attention", strings.NewReader(`{"session_id":"s-1","message":"m"}`))
	req = req.WithContext(contextWithPrincipal(req.Context(), principal{email: "e@x.com", customer: "globex"}))
	rec = httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-project: want 404, got %d (%s)", rec.Code, rec.Body)
	}

	// A principal with no project cannot ask for attention at all.
	req = httptest.NewRequest("POST", "/agent/attention", strings.NewReader(`{"session_id":"s-1","message":"m"}`))
	req = req.WithContext(contextWithPrincipal(req.Context(), principal{email: "e@x.com"}))
	rec = httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("no project: want 403, got %d", rec.Code)
	}
}

// ── The sweep (§8.2) ────────────────────────────────────────────────────────

func newTestSweeper(store attentionStore, now int64) *attentionSweeper {
	s := newAttentionSweeper(store)
	s.now = func() time.Time { return time.Unix(now, 0) }
	s.logf = func(string, ...any) {}
	return s
}

// TestAttentionSweepEmitsTimeoutEvent: a request that lapses unanswered becomes
// a `human.attention.timeout` event with the §8.2 envelope, so the worker's
// PROMPT decides the fallback — no approval machinery.
func TestAttentionSweepEmitsTimeoutEvent(t *testing.T) {
	store := newFakeAttentionStore()
	sess := store.addSession("s-1", "acme", "tweet-author")
	store.requests = append(store.requests, &agentdb.AttentionRequest{
		ID: "att-1", Project: "acme", SessionID: "s-1", Worker: "tweet-author",
		Message: "sign off on this draft", SessionURL: "https://orange.example.com/p/acme/s/s-1",
		CreatedAt: 1000, ExpiresAt: 2000,
	})
	sess.AttentionRequested = true

	if err := newTestSweeper(store, 2001).Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if len(store.events) != 1 {
		t.Fatalf("expected one timeout event, got %d", len(store.events))
	}
	ev := store.events[0]
	if ev.Type != agentdb.EventTypeHumanAttentionTimeout {
		t.Fatalf("event type: %q", ev.Type)
	}
	if ev.Project != "acme" {
		t.Fatalf("event project: %q", ev.Project)
	}
	// §8.2: source core, depth 0, plus the worker and session of the paused job.
	if ev.Envelope.Source != agentdb.EventSourceCore || ev.Envelope.Depth != 0 ||
		ev.Envelope.Worker != "tweet-author" || ev.Envelope.SessionID != "s-1" {
		t.Fatalf("envelope: %+v", ev.Envelope)
	}
	// The woken worker reads the original ask verbatim.
	if !strings.Contains(ev.Text, "sign off on this draft") || !strings.Contains(ev.Text, "s-1") {
		t.Fatalf("event text: %q", ev.Text)
	}
	if store.requests[0].TimedOutAt != 2001 {
		t.Fatalf("the request must be marked timed out: %+v", store.requests[0])
	}
	if sess.AttentionRequested {
		t.Fatalf("resolving must clear the session stamp")
	}
}

// TestAttentionSweepIgnoresAnsweredRequests: §9 has no approval state machine —
// whatever the human typed IS the answer, so a human turn after the request
// closes it silently.
func TestAttentionSweepIgnoresAnsweredRequests(t *testing.T) {
	store := newFakeAttentionStore()
	store.addSession("s-1", "acme", "tweet-author")
	store.requests = append(store.requests, &agentdb.AttentionRequest{
		ID: "att-1", Project: "acme", SessionID: "s-1", Worker: "tweet-author",
		Message: "sign off", CreatedAt: 1000, ExpiresAt: 2000,
	})
	// "post it", typed at 1500.
	store.messages["s-1"] = []int64{500, 1500}

	if err := newTestSweeper(store, 2001).Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(store.events) != 0 {
		t.Fatalf("an answered request must emit nothing: %+v", store.events)
	}
	if store.requests[0].AnsweredAt != 2001 || store.requests[0].TimedOutAt != 0 {
		t.Fatalf("request should be answered, not timed out: %+v", store.requests[0])
	}
}

// TestAttentionSweepLeavesOpenAndDeadlineFreeRequestsAlone.
func TestAttentionSweepLeavesOpenRequestsAlone(t *testing.T) {
	store := newFakeAttentionStore()
	store.addSession("s-1", "acme", "w")
	store.addSession("s-2", "acme", "w")
	store.requests = append(store.requests,
		// Not due yet.
		&agentdb.AttentionRequest{ID: "att-1", Project: "acme", SessionID: "s-1", CreatedAt: 1000, ExpiresAt: 5000},
		// No deadline at all: expires_in is optional and this one simply waits.
		&agentdb.AttentionRequest{ID: "att-2", Project: "acme", SessionID: "s-2", CreatedAt: 1000},
	)

	if err := newTestSweeper(store, 2001).Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(store.events) != 0 {
		t.Fatalf("nothing has lapsed: %+v", store.events)
	}
	for _, r := range store.requests {
		if r.TimedOutAt != 0 || r.AnsweredAt != 0 {
			t.Fatalf("request %s was resolved early: %+v", r.ID, r)
		}
	}
}

// TestAttentionSweepDoesNotDoubleEmit: the sweep runs every minute and is
// at-least-once over the same rows; a lapsed request must wake its worker once.
func TestAttentionSweepDoesNotDoubleEmit(t *testing.T) {
	store := newFakeAttentionStore()
	store.addSession("s-1", "acme", "tweet-author")
	store.requests = append(store.requests, &agentdb.AttentionRequest{
		ID: "att-1", Project: "acme", SessionID: "s-1", Worker: "tweet-author",
		Message: "sign off", CreatedAt: 1000, ExpiresAt: 2000,
	})

	sweeper := newTestSweeper(store, 2001)
	for i := 0; i < 3; i++ {
		if err := sweeper.Sweep(context.Background()); err != nil {
			t.Fatalf("sweep %d: %v", i, err)
		}
	}
	if len(store.events) != 1 {
		t.Fatalf("three sweeps must emit one event, got %d", len(store.events))
	}
}

// ── Channel parsing ─────────────────────────────────────────────────────────

func TestAttentionChannelParsing(t *testing.T) {
	tests := []struct {
		name     string
		raw      agentdb.JSONMap
		wantKind string
		wantErr  string
	}{
		{"empty is the log-only fallback", agentdb.JSONMap{}, "", ""},
		{"nil is the log-only fallback", nil, "", ""},
		{"webhook", agentdb.JSONMap{"kind": "webhook", "url": "https://x"}, attentionChannelWebhook, ""},
		{"kind is case-insensitive", agentdb.JSONMap{"kind": "WebHook", "url": "https://x"}, attentionChannelWebhook, ""},
		{"a bare url implies a webhook", agentdb.JSONMap{"url": "https://x"}, attentionChannelWebhook, ""},
		{"unknown kind", agentdb.JSONMap{"kind": "email", "url": "x"}, "email", "not supported"},
		{"webhook without url", agentdb.JSONMap{"kind": "webhook"}, attentionChannelWebhook, "needs a url"},
		{"non-http url", agentdb.JSONMap{"kind": "webhook", "url": "ftp://x"}, attentionChannelWebhook, "must be http(s)"},
		{"wrong shape", agentdb.JSONMap{"kind": 42}, "", "not an object"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ch, err := parseAttentionChannel(tc.raw)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want %q, got %v", tc.wantErr, err)
			}
			if ch.Kind != tc.wantKind {
				t.Fatalf("kind: want %q, got %q", tc.wantKind, ch.Kind)
			}
		})
	}
}

func TestAttentionChannelHeaderResolution(t *testing.T) {
	ch := attentionChannel{Headers: map[string]string{
		"Authorization": "${TOKEN}",
		"X-Literal":     "plain",
		// A partial reference is NOT a reference (§4.4: whole-value only) and is
		// passed through as the literal it is.
		"X-Partial": "Bearer ${TOKEN}",
	}}
	got, err := ch.resolveHeaders(func(k string) string {
		if k == "TOKEN" {
			return "shhh"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got["Authorization"] != "shhh" || got["X-Literal"] != "plain" || got["X-Partial"] != "Bearer ${TOKEN}" {
		t.Fatalf("headers: %+v", got)
	}

	// An unset variable fails loudly rather than sending "${TOKEN}" as a
	// credential that authenticates as nobody.
	if _, err := ch.resolveHeaders(func(string) string { return "" }); err == nil ||
		!strings.Contains(err.Error(), "unset") {
		t.Fatalf("want an unset-variable error, got %v", err)
	}
}

// TestAttentionWebhookPostShape proves the real poster sends the §9 body and
// treats a non-2xx as a delivery failure.
func TestAttentionWebhookPostShape(t *testing.T) {
	var gotBody []byte
	var gotAuth, gotType string
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody = make([]byte, r.ContentLength)
		_, _ = r.Body.Read(gotBody)
		gotAuth = r.Header.Get("Authorization")
		gotType = r.Header.Get("Content-Type")
		w.WriteHeader(status)
	}))
	defer srv.Close()

	ch := attentionChannel{Kind: attentionChannelWebhook, URL: srv.URL}
	err := postAttentionWebhook(context.Background(), ch, map[string]string{"Authorization": "shhh"},
		attentionPayload{Message: "look", SessionURL: "https://x/p/acme/s/s-1"})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if gotAuth != "shhh" || gotType != "application/json" {
		t.Fatalf("headers: auth=%q type=%q", gotAuth, gotType)
	}
	if !strings.Contains(string(gotBody), `"session_url":"https://x/p/acme/s/s-1"`) {
		t.Fatalf("body: %s", gotBody)
	}

	status = http.StatusInternalServerError
	if err := postAttentionWebhook(context.Background(), ch, nil, attentionPayload{Message: "look"}); err == nil {
		t.Fatalf("a non-2xx must be a delivery failure")
	}
}
