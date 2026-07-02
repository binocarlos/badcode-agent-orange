package orchestrator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnthropicModelRunHappyPath(t *testing.T) {
	var gotKey, gotVersion, gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Role, Content string
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotModel = req.Model
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"clever plan"}],` +
			`"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`))
	}))
	defer srv.Close()

	m := &AnthropicModel{APIKey: "sk-test", ModelID: "claude-opus-4-8", BaseURL: srv.URL}
	out, usage, err := m.Run(context.Background(), "Goal: grow the brand")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out != "clever plan" {
		t.Fatalf("output = %q, want clever plan", out)
	}
	// §10c §A: real usage is surfaced from the response frame.
	if usage.InputTokens != 10 || usage.OutputTokens != 5 || usage.Total() != 15 {
		t.Fatalf("usage = %+v (total %d), want {10 5} total 15", usage, usage.Total())
	}
	if gotKey != "sk-test" || gotVersion != "2023-06-01" || gotModel != "claude-opus-4-8" {
		t.Fatalf("headers/body wrong: key=%q version=%q model=%q", gotKey, gotVersion, gotModel)
	}
}

func TestAnthropicModelRunErrors(t *testing.T) {
	// Non-200.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer bad.Close()
	m := &AnthropicModel{APIKey: "k", ModelID: "claude-opus-4-8", BaseURL: bad.URL}
	if _, _, err := m.Run(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("expected 500 error, got %v", err)
	}

	// Refusal.
	refuse := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"content":[],"stop_reason":"refusal","usage":{"input_tokens":1,"output_tokens":0}}`))
	}))
	defer refuse.Close()
	m2 := &AnthropicModel{APIKey: "k", ModelID: "claude-opus-4-8", BaseURL: refuse.URL}
	if _, _, err := m2.Run(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "refus") {
		t.Fatalf("expected refusal error, got %v", err)
	}
}

// recordingMeter is a SpendMeter double for the §10c I-5 post-call path: it
// ALWAYS records, and returns errOnCharge for real (non-probe) charges — the
// "another model spent us past the ceiling between probe and charge" scenario.
type recordingMeter struct {
	tokens      int64
	usd         float64
	errOnCharge error
}

func (m *recordingMeter) Charge(_ context.Context, tokens int64, usd float64) error {
	m.tokens += tokens
	m.usd += usd
	if tokens == 0 && usd == 0 {
		return nil // probe passes
	}
	return m.errOnCharge
}

func (m *recordingMeter) Spent(context.Context) (float64, error) { return m.usd, nil }

// §10c I-5: the post-call charge is never discarded. When the charge that
// records the spend crosses/lands past the ceiling (ErrSpendCeiling), the run
// still succeeds and the spend IS recorded; any other charge failure fails loud.
func TestAnthropicModelPostCallChargeAlwaysRecorded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],` +
			`"stop_reason":"end_turn","usage":{"input_tokens":100,"output_tokens":50}}`))
	}))
	defer srv.Close()
	ctx := context.Background()

	// Ceiling crossed at post-call charge: EXPECTED — response stays valid.
	over := &recordingMeter{errOnCharge: ErrSpendCeiling}
	m := &AnthropicModel{
		APIKey: "k", ModelID: "claude-opus-4-8", BaseURL: srv.URL,
		Meter: over, Pricing: Pricing{InputPerMTok: 5, OutputPerMTok: 25},
	}
	out, usage, err := m.Run(ctx, "x")
	if err != nil || out != "ok" {
		t.Fatalf("run past ceiling must succeed: out=%q err=%v", out, err)
	}
	if usage.Total() != 150 {
		t.Fatalf("usage = %+v, want total 150", usage)
	}
	if over.tokens != 150 || over.usd == 0 {
		t.Fatalf("spend not recorded: tokens=%d usd=%v", over.tokens, over.usd)
	}

	// Any OTHER charge failure means spend went uncounted → fail loud.
	broken := &recordingMeter{errOnCharge: context.DeadlineExceeded}
	m2 := &AnthropicModel{
		APIKey: "k", ModelID: "claude-opus-4-8", BaseURL: srv.URL,
		Meter: broken, Pricing: Pricing{InputPerMTok: 5, OutputPerMTok: 25},
	}
	if _, _, err := m2.Run(ctx, "x"); err == nil || !strings.Contains(err.Error(), "record spend") {
		t.Fatalf("non-ceiling charge failure must fail loud, got %v", err)
	}
}

func TestAnthropicModelMeteredDispatch(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],` +
			`"stop_reason":"end_turn","usage":{"input_tokens":100,"output_tokens":50}}`))
	}))
	defer srv.Close()

	ctx := context.Background()

	// A live call records actual spend: (100*5 + 50*25)/1e6 = 0.00175 USD.
	meter := NewMemSpendMeter(1.0)
	m := &AnthropicModel{
		APIKey: "k", ModelID: "claude-opus-4-8", BaseURL: srv.URL,
		Meter: meter, Pricing: Pricing{InputPerMTok: 5, OutputPerMTok: 25},
	}
	if _, _, err := m.Run(ctx, "x"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if spent, _ := meter.Spent(ctx); spent < 0.00174 || spent > 0.00176 {
		t.Fatalf("spent = %v, want ~0.00175", spent)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}

	// A pre-exhausted meter halts BEFORE dispatch — no HTTP call.
	exhausted := &AnthropicModel{
		APIKey: "k", ModelID: "claude-opus-4-8", BaseURL: srv.URL,
		Meter: NewMemSpendMeter(0.0), Pricing: Pricing{InputPerMTok: 5, OutputPerMTok: 25},
	}
	if _, _, err := exhausted.Run(ctx, "x"); err == nil {
		t.Fatalf("expected spend-ceiling halt, got nil")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want still 1 (dispatch halted before HTTP)", calls)
	}
}

// TestAnthropicModelConfigFallbacks pins the zero-value config defaults against
// the explicitly-configured values, both via the pure accessors and — for the
// on-the-wire fields — via a capturing httptest server (Mission A: the config
// fallback branches).
func TestAnthropicModelConfigFallbacks(t *testing.T) {
	custom := &http.Client{}
	cases := []struct {
		name        string
		m           *AnthropicModel
		wantBase    string
		wantVersion string
		wantMaxTok  int
		wantClient  *http.Client
	}{
		{
			name:        "all unset falls back to documented defaults",
			m:           &AnthropicModel{ModelID: "m"},
			wantBase:    "https://api.anthropic.com",
			wantVersion: "2023-06-01",
			wantMaxTok:  1024,
			wantClient:  http.DefaultClient,
		},
		{
			name: "all set wins over defaults",
			m: &AnthropicModel{ModelID: "m", BaseURL: "http://example.test",
				APIVersion: "2030-01-01", MaxTokens: 42, HTTPClient: custom},
			wantBase:    "http://example.test",
			wantVersion: "2030-01-01",
			wantMaxTok:  42,
			wantClient:  custom,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.m.baseURL(); got != tc.wantBase {
				t.Fatalf("baseURL = %q, want %q", got, tc.wantBase)
			}
			if got := tc.m.apiVersion(); got != tc.wantVersion {
				t.Fatalf("apiVersion = %q, want %q", got, tc.wantVersion)
			}
			if got := tc.m.maxTokens(); got != tc.wantMaxTok {
				t.Fatalf("maxTokens = %d, want %d", got, tc.wantMaxTok)
			}
			if got := tc.m.httpClient(); got != tc.wantClient {
				t.Fatalf("httpClient = %p, want %p", got, tc.wantClient)
			}
		})
	}

	// On the wire: with APIVersion/MaxTokens/HTTPClient unset the request must
	// carry the defaults (BaseURL points at the capture server).
	var gotVersion string
	var gotMaxTokens int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotVersion = r.Header.Get("anthropic-version")
		var req struct {
			MaxTokens int `json:"max_tokens"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotMaxTokens = req.MaxTokens
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn",` +
			`"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()

	m := &AnthropicModel{APIKey: "sk-test", ModelID: "m", BaseURL: srv.URL}
	if _, _, err := m.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if gotVersion != "2023-06-01" || gotMaxTokens != 1024 {
		t.Fatalf("wire defaults wrong: version=%q max_tokens=%d", gotVersion, gotMaxTokens)
	}
}
