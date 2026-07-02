# Slice B — Real Model Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Put a real model behind the frozen `Model` seam. Slice B ships three things the v1 build needs to run against Anthropic instead of the `ScriptedModel` double: a `ModelRouter` that resolves a `ModelTier` → a `Model` using **configurable** model IDs; an Anthropic `Model` implementation that calls the Messages API over HTTP with **API-key auth** (subscription OAuth is disallowed for automation); and a `SpendMeter` that enforces a monthly USD ceiling in mechanism — `Charge` errors when the ceiling is hit, halting dispatch. The `ScriptedModel` (Slice 0) stays as the deterministic offline test double.

**Architecture:** Everything lands in the existing `go/orchestrator` package alongside Slice 0. A tiny `net/http` + `encoding/json` client (`AnthropicModel`) implements `Model.Run(ctx, prompt) (string, error)`. A `TierRouter` holds one `Model` per `ModelTier` and answers `For(tier) Model`. A `MemSpendMeter` tracks running USD spend against a ceiling and is wired *inside* `AnthropicModel` (metering needs the response's token usage, which `Model.Run` does not surface, so a generic decorator can't do it). Model IDs and pricing are config/env-driven with documented current defaults. Network tests never touch the real API — they run against an `httptest.Server`; the real endpoint is exercised only behind a `//go:build anthropic_smoke` tag / a manual step.

**Tech Stack:** Go 1.25, **standard library only** (`net/http`, `encoding/json`, `context`, `sync`, `errors`, `os`) — the repo's `go.mod` vendors no Anthropic SDK and the Liftability invariant discourages adding a heavy dependency for a seam this thin. Reuses the Slice-0 `orchestrator` package (`Model`, `ScriptedModel`).

## Global Constraints

- Module path `github.com/binocarlos/badcode-agent-orange`; all new code under `go/orchestrator/`, matching Slice 0's idiom (stdlib + `agentdb`; table-driven tests). One line.
- **Consume the §4/§5 contract types verbatim** — never redefine or renegotiate `Model`, `ModelTier`, `SpendMeter`, `ModelRouter`; a needed contract change is a stop-and-escalate event, not a local edit. One line.
- `go build ./...` and `go vet ./...` must stay green; add tests with every change (heavily-tested codebase, table-test patterns). One line.
- **No heavy external dependencies** — the real `Model` impl is a thin `net/http` + `encoding/json` client; do not add an Anthropic SDK unless the repo already vendors one (it does not). One line.
- **Auth is API-key only** (`x-api-key` header); subscription OAuth is disallowed for automation. Needs `ANTHROPIC_API_KEY`. One line.
- **Network tests must not hit the real API** — `Model.Run` is exercised offline against an `httptest.Server`; the live endpoint runs only behind the `anthropic_smoke` build tag or a manual smoke step. One line.
- **Model IDs are configurable** (env/config), never hardcoded from memory; confirm current IDs via the `claude-api` skill before wiring defaults (Task 1). One line.
- **The spend ceiling is enforced in mechanism** (contracts §7.2): `SpendMeter.Charge` errors at the monthly ceiling and dispatch halts — sibling to the depth/spawn floors. One line.
- `ScriptedModel` (Slice 0) stays as the deterministic, offline test double behind `Model`. One line.

---

## File Structure

| Path | What |
| --- | --- |
| `go/orchestrator/tier.go` | `ModelTier` type + `TierFull`/`TierMid`/`TierCheap` constants (frozen strings, contracts §3). |
| `go/orchestrator/spendmeter.go` | `SpendMeter` interface (§5) + `MemSpendMeter` impl + `ErrSpendCeiling`. |
| `go/orchestrator/anthropic.go` | `AnthropicModel` — the real `Model` over the Messages API; `Pricing` helper. |
| `go/orchestrator/router.go` | `ModelRouter` interface (§5) + `TierRouter` + `errModel`; config wiring (`RouterConfig`, `DefaultModelIDs`, `ModelIDsFromEnv`, `DefaultPricing`, `NewAnthropicRouter`). |
| `go/orchestrator/*_test.go` | Table/httptest tests per file. |
| `go/orchestrator/anthropic_smoke_test.go` | Real-API smoke test behind `//go:build anthropic_smoke`. |

`go/orchestrator/model.go` (Slice 0: `Model`, `ScriptedModel`) is unchanged and reused.

---

### Task 1: Confirm current model IDs + pricing (prep, no code)

**Files:** none — this is a research/verification step whose output feeds Tasks 4 and 7.

**Interfaces:**
- Consumes: the `claude-api` skill.
- Produces: the confirmed tier → model-ID map and per-tier pricing that Task 7's `DefaultModelIDs()` / `DefaultPricing()` will encode.

- [ ] **Step 1: Confirm via the `claude-api` skill**

Invoke the `claude-api` skill and read `shared/models.md` (do **not** rely on training-data memory — model IDs drift). Record the current defaults:

| Tier | Meaning (contracts §3) | Current model ID | Input $/MTok | Output $/MTok |
| --- | --- | --- | --- | --- |
| `full` | manager / reasoning (Opus) | `claude-opus-4-8` | 5.00 | 25.00 |
| `mid` | workers (Sonnet) | `claude-sonnet-5` | 3.00 | 15.00 |
| `cheap` | summary / leaf (Haiku) | `claude-haiku-4-5` | 1.00 | 5.00 |

(Values above are the confirmed defaults as of this plan's date; re-confirm at build time — the whole point of making them config/env-driven is that they change.)

- [ ] **Step 2: Record the API shape**

From the same skill, confirm the Messages API contract used in Task 4: `POST https://api.anthropic.com/v1/messages`, headers `x-api-key`, `anthropic-version: 2023-06-01`, `content-type: application/json`; request body `{"model","max_tokens","messages":[{"role":"user","content":...}]}`; response `{"content":[{"type":"text","text":...}],"stop_reason":...,"usage":{"input_tokens","output_tokens"}}`. No commit for this task.

---

### Task 2: `ModelTier` vocabulary (frozen strings)

**Files:**
- Create: `go/orchestrator/tier.go`
- Test: `go/orchestrator/tier_test.go`

**Interfaces:**
- Consumes: contracts §3 frozen strings.
- Produces: `type ModelTier string`; `TierFull = "full"`, `TierMid = "mid"`, `TierCheap = "cheap"`.

- [ ] **Step 1: Write the failing test**

```go
package orchestrator

import "testing"

func TestModelTierFrozenStrings(t *testing.T) {
	cases := map[ModelTier]string{
		TierFull:  "full",
		TierMid:   "mid",
		TierCheap: "cheap",
	}
	for tier, want := range cases {
		if string(tier) != want {
			t.Fatalf("tier %v = %q, want %q (frozen by contracts §3)", tier, string(tier), want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/ -run TestModelTierFrozenStrings -v`
Expected: FAIL — `undefined: ModelTier` (and the constants).

- [ ] **Step 3: Write minimal implementation**

```go
package orchestrator

// ModelTier is the per-scope cost/capability tier (contracts §3). It downgrades
// toward the leaves of a goal-tree: managers reason on TierFull, workers on
// TierMid, summary/leaf scopes on TierCheap. Frozen strings — do not rename.
type ModelTier string

const (
	TierFull  ModelTier = "full"  // manager / reasoning (Opus by default)
	TierMid   ModelTier = "mid"   // workers (Sonnet by default)
	TierCheap ModelTier = "cheap" // summary / leaf (Haiku by default)
)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/ -run TestModelTierFrozenStrings -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/tier.go go/orchestrator/tier_test.go
git commit -m "feat(orchestrator): ModelTier vocabulary (frozen strings, contracts §3)"
```

---

### Task 3: `SpendMeter` seam + in-memory ceiling

**Files:**
- Create: `go/orchestrator/spendmeter.go`
- Test: `go/orchestrator/spendmeter_test.go`

**Interfaces:**
- Consumes: `context`.
- Produces: `type SpendMeter interface { Charge(ctx, tokens int64, usd float64) error; Spent(ctx) (float64, error) }` (verbatim, contracts §5); `MemSpendMeter` with a USD ceiling; `ErrSpendCeiling`. Semantics: `Charge` errors iff the meter is **already at/over** the ceiling (so a `Charge(ctx,0,0)` probe halts the *next* dispatch; the charge that first crosses the ceiling still records). This makes "Charge errors when the ceiling is hit → halts dispatch" (§7.2) literal using only the two contract methods.

- [ ] **Step 1: Write the failing test**

```go
package orchestrator

import (
	"context"
	"errors"
	"testing"
)

func TestMemSpendMeterHaltsAtCeiling(t *testing.T) {
	ctx := context.Background()
	m := NewMemSpendMeter(10.0)

	if err := m.Charge(ctx, 100, 6.0); err != nil {
		t.Fatalf("charge under ceiling: %v", err)
	}
	if spent, _ := m.Spent(ctx); spent != 6.0 {
		t.Fatalf("spent = %v, want 6.0", spent)
	}

	// The charge that first crosses the ceiling still records (6 < 10).
	if err := m.Charge(ctx, 100, 6.0); err != nil {
		t.Fatalf("crossing charge should record: %v", err)
	}
	if spent, _ := m.Spent(ctx); spent != 12.0 {
		t.Fatalf("spent = %v, want 12.0", spent)
	}

	// Now over ceiling: even a zero-amount probe halts the next dispatch.
	if err := m.Charge(ctx, 0, 0); !errors.Is(err, ErrSpendCeiling) {
		t.Fatalf("probe over ceiling = %v, want ErrSpendCeiling", err)
	}
}

func TestMemSpendMeterZeroCeilingHaltsImmediately(t *testing.T) {
	m := NewMemSpendMeter(0.0)
	if err := m.Charge(context.Background(), 0, 0); !errors.Is(err, ErrSpendCeiling) {
		t.Fatalf("zero-ceiling probe = %v, want ErrSpendCeiling", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/ -run TestMemSpendMeter -v`
Expected: FAIL — `undefined: NewMemSpendMeter` / `ErrSpendCeiling`.

- [ ] **Step 3: Write minimal implementation**

```go
package orchestrator

import (
	"context"
	"errors"
	"sync"
)

// SpendMeter is the monthly spend ceiling (contracts §5). Charge errors when the
// ceiling is hit, which HALTS dispatch (the cost floor, sibling to the
// depth/spawn caps — §7.2). Enforced in mechanism.
type SpendMeter interface {
	Charge(ctx context.Context, tokens int64, usd float64) error
	Spent(ctx context.Context) (usd float64, err error)
}

// ErrSpendCeiling is returned by Charge once the running spend has reached the
// configured ceiling. Callers treat it as a hard halt on dispatch.
var ErrSpendCeiling = errors.New("spend ceiling reached")

// MemSpendMeter is an in-memory SpendMeter with a USD ceiling. Charge errors iff
// the meter is already at/over the ceiling — so a Charge(ctx,0,0) probe is a
// cheap "may I dispatch?" check, and the charge that first crosses the ceiling
// still records (the over-shoot is bounded by one dispatch, and the next probe
// halts). A Postgres-backed impl can swap in behind SpendMeter later (Slice A).
type MemSpendMeter struct {
	mu         sync.Mutex
	ceilingUSD float64
	spentUSD   float64
	tokens     int64
}

// NewMemSpendMeter returns a meter that halts dispatch once spend reaches ceilingUSD.
func NewMemSpendMeter(ceilingUSD float64) *MemSpendMeter {
	return &MemSpendMeter{ceilingUSD: ceilingUSD}
}

func (m *MemSpendMeter) Charge(_ context.Context, tokens int64, usd float64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.spentUSD >= m.ceilingUSD {
		return ErrSpendCeiling
	}
	m.spentUSD += usd
	m.tokens += tokens
	return nil
}

func (m *MemSpendMeter) Spent(_ context.Context) (float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.spentUSD, nil
}

var _ SpendMeter = (*MemSpendMeter)(nil)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/ -run TestMemSpendMeter -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/spendmeter.go go/orchestrator/spendmeter_test.go
git commit -m "feat(orchestrator): SpendMeter seam + in-memory monthly ceiling (§7.2)"
```

---

### Task 4: `AnthropicModel` — real `Model` over the Messages API (offline)

**Files:**
- Create: `go/orchestrator/anthropic.go`
- Test: `go/orchestrator/anthropic_test.go`

**Interfaces:**
- Consumes: `Model` (Slice 0), `SpendMeter` (Task 3), `net/http`, `encoding/json`.
- Produces: `type Pricing struct { InputPerMTok, OutputPerMTok float64 }`; `type AnthropicModel struct { APIKey, ModelID string; MaxTokens int; BaseURL, APIVersion string; HTTPClient *http.Client; Meter SpendMeter; Pricing Pricing }` implementing `Model.Run`. Run: (1) if `Meter != nil`, probe with `Charge(ctx,0,0)` and halt on error **before** dispatch; (2) POST the Messages request with `x-api-key` auth; (3) non-200 → error; (4) `stop_reason == "refusal"` → error; (5) concatenate `text` content blocks; (6) if `Meter != nil`, `Charge` the actual token usage × `Pricing`. Metering lives here because `Model.Run` returns only a string — a generic decorator can't see token usage.

- [ ] **Step 1: Write the failing test**

```go
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
	out, err := m.Run(context.Background(), "Goal: grow the brand")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out != "clever plan" {
		t.Fatalf("output = %q, want clever plan", out)
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
	if _, err := m.Run(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("expected 500 error, got %v", err)
	}

	// Refusal.
	refuse := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"content":[],"stop_reason":"refusal","usage":{"input_tokens":1,"output_tokens":0}}`))
	}))
	defer refuse.Close()
	m2 := &AnthropicModel{APIKey: "k", ModelID: "claude-opus-4-8", BaseURL: refuse.URL}
	if _, err := m2.Run(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "refus") {
		t.Fatalf("expected refusal error, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/ -run TestAnthropicModelRun -v`
Expected: FAIL — `undefined: AnthropicModel`.

- [ ] **Step 3: Write minimal implementation**

```go
package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const defaultAnthropicBaseURL = "https://api.anthropic.com"

// Pricing is the per-million-token cost of a model, used to convert token usage
// into the USD amount charged to the SpendMeter.
type Pricing struct {
	InputPerMTok  float64
	OutputPerMTok float64
}

func (p Pricing) usd(inTok, outTok int64) float64 {
	return (float64(inTok)*p.InputPerMTok + float64(outTok)*p.OutputPerMTok) / 1_000_000
}

// AnthropicModel is the real Model (contracts §5): prompt in, text out, backed by
// the Anthropic Messages API over HTTP with API-key auth (subscription OAuth is
// disallowed for automation). A thin stdlib client — no SDK dependency. When Meter
// is set, Run halts before dispatch if the spend ceiling is hit and records actual
// token spend after each call (metering lives here because Model.Run surfaces only
// a string, so a generic decorator cannot see usage).
type AnthropicModel struct {
	APIKey     string
	ModelID    string
	MaxTokens  int
	BaseURL    string // default https://api.anthropic.com
	APIVersion string // default 2023-06-01
	HTTPClient *http.Client
	Meter      SpendMeter // optional; halts dispatch at the ceiling
	Pricing    Pricing    // used only when Meter is set
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicReq struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	Messages  []anthropicMessage `json:"messages"`
}

type anthropicRespBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicResp struct {
	Content    []anthropicRespBlock `json:"content"`
	StopReason string               `json:"stop_reason"`
	Usage      struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
}

func (m *AnthropicModel) baseURL() string {
	if m.BaseURL != "" {
		return m.BaseURL
	}
	return defaultAnthropicBaseURL
}

func (m *AnthropicModel) apiVersion() string {
	if m.APIVersion != "" {
		return m.APIVersion
	}
	return "2023-06-01"
}

func (m *AnthropicModel) maxTokens() int {
	if m.MaxTokens > 0 {
		return m.MaxTokens
	}
	return 1024
}

func (m *AnthropicModel) httpClient() *http.Client {
	if m.HTTPClient != nil {
		return m.HTTPClient
	}
	return http.DefaultClient
}

// Run sends the prompt as a single user turn and returns the concatenated text.
func (m *AnthropicModel) Run(ctx context.Context, prompt string) (string, error) {
	// Cost floor: halt before dispatch if the ceiling is already hit (§7.2).
	if m.Meter != nil {
		if err := m.Meter.Charge(ctx, 0, 0); err != nil {
			return "", fmt.Errorf("anthropic %s: %w", m.ModelID, err)
		}
	}

	body, err := json.Marshal(anthropicReq{
		Model:     m.ModelID,
		MaxTokens: m.maxTokens(),
		Messages:  []anthropicMessage{{Role: "user", Content: prompt}},
	})
	if err != nil {
		return "", fmt.Errorf("anthropic %s: marshal: %w", m.ModelID, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL()+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("anthropic %s: request: %w", m.ModelID, err)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-api-key", m.APIKey)
	req.Header.Set("anthropic-version", m.apiVersion())

	resp, err := m.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("anthropic %s: do: %w", m.ModelID, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("anthropic %s: read: %w", m.ModelID, err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("anthropic %s: status %d: %s", m.ModelID, resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var parsed anthropicResp
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("anthropic %s: decode: %w", m.ModelID, err)
	}
	if parsed.StopReason == "refusal" {
		return "", fmt.Errorf("anthropic %s: request refused", m.ModelID)
	}

	var sb strings.Builder
	for _, b := range parsed.Content {
		if b.Type == "text" {
			sb.WriteString(b.Text)
		}
	}

	if m.Meter != nil {
		// Record actual spend. An ErrSpendCeiling here means this call pushed us
		// over — the response is still valid; the next dispatch's probe halts.
		_ = m.Meter.Charge(ctx, parsed.Usage.InputTokens+parsed.Usage.OutputTokens,
			m.Pricing.usd(parsed.Usage.InputTokens, parsed.Usage.OutputTokens))
	}
	return sb.String(), nil
}

var _ Model = (*AnthropicModel)(nil)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/ -run TestAnthropicModelRun -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/anthropic.go go/orchestrator/anthropic_test.go
git commit -m "feat(orchestrator): Anthropic Model over the Messages API (stdlib HTTP, offline-tested)"
```

---

### Task 5: Metered dispatch (probe halts, charge records)

**Files:**
- Test: `go/orchestrator/anthropic_test.go` (extend — no new production code)

**Interfaces:**
- Consumes: `AnthropicModel`, `MemSpendMeter`, `Pricing`. Asserts the §7.2 behaviour end-to-end: a live call records actual spend, and a pre-exhausted meter halts dispatch **before** the HTTP call is made.

- [ ] **Step 1: Write the failing test**

```go
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
	if _, err := m.Run(ctx, "x"); err != nil {
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
	if _, err := exhausted.Run(ctx, "x"); err == nil {
		t.Fatalf("expected spend-ceiling halt, got nil")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want still 1 (dispatch halted before HTTP)", calls)
	}
}
```

- [ ] **Step 2: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/ -run TestAnthropicModelMeteredDispatch -v`
Expected: PASS (the metering hooks were implemented in Task 4). If it fails, fix the implementation, not the test.

- [ ] **Step 3: Commit**

```bash
git add go/orchestrator/anthropic_test.go
git commit -m "test(orchestrator): metered dispatch — probe halts before HTTP, charge records actual spend"
```

---

### Task 6: `ModelRouter` seam + `TierRouter`

**Files:**
- Create: `go/orchestrator/router.go`
- Test: `go/orchestrator/router_test.go`

**Interfaces:**
- Consumes: `Model`, `ModelTier`, `ScriptedModel` (as offline fakes).
- Produces: `type ModelRouter interface { For(tier ModelTier) Model }` (verbatim, §5); `type TierRouter struct{...}` + `NewTierRouter(map[ModelTier]Model) *TierRouter`; `For` returns the mapped `Model`, or a loud `errModel` for an unmapped tier (the interface can't return an error, so a mis-tier fails loudly on `Run` rather than panicking).

- [ ] **Step 1: Write the failing test**

```go
package orchestrator

import (
	"context"
	"testing"
)

func TestTierRouterResolves(t *testing.T) {
	ctx := context.Background()
	r := NewTierRouter(map[ModelTier]Model{
		TierFull:  &ScriptedModel{Default: "opus"},
		TierCheap: &ScriptedModel{Default: "haiku"},
	})

	if got, _ := r.For(TierFull).Run(ctx, "x"); got != "opus" {
		t.Fatalf("TierFull -> %q, want opus", got)
	}
	if got, _ := r.For(TierCheap).Run(ctx, "x"); got != "haiku" {
		t.Fatalf("TierCheap -> %q, want haiku", got)
	}

	// Unmapped tier: For returns a Model whose Run errors loudly (never nil).
	m := r.For(TierMid)
	if m == nil {
		t.Fatalf("For(TierMid) returned nil Model")
	}
	if _, err := m.Run(ctx, "x"); err == nil {
		t.Fatalf("expected error for unmapped tier")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/ -run TestTierRouterResolves -v`
Expected: FAIL — `undefined: NewTierRouter`.

- [ ] **Step 3: Write minimal implementation**

```go
package orchestrator

import (
	"context"
	"fmt"
)

// ModelRouter resolves a tier to a Model (contracts §5). Lets a Scope pick its
// cost/capability tier without knowing which concrete Model backs it.
type ModelRouter interface {
	For(tier ModelTier) Model
}

// TierRouter maps each ModelTier to one Model. An unmapped tier resolves to a
// loud errModel — For cannot return an error, so a mis-tier fails on Run rather
// than panicking on a nil Model.
type TierRouter struct {
	models map[ModelTier]Model
}

// NewTierRouter builds a router over the given tier->Model map.
func NewTierRouter(models map[ModelTier]Model) *TierRouter {
	return &TierRouter{models: models}
}

func (r *TierRouter) For(tier ModelTier) Model {
	if m, ok := r.models[tier]; ok {
		return m
	}
	return errModel{tier: tier}
}

// errModel is the fail-loud placeholder for an unmapped tier.
type errModel struct{ tier ModelTier }

func (e errModel) Run(context.Context, string) (string, error) {
	return "", fmt.Errorf("no model configured for tier %q", e.tier)
}

var (
	_ ModelRouter = (*TierRouter)(nil)
	_ Model       = errModel{}
)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/ -run TestTierRouterResolves -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/router.go go/orchestrator/router_test.go
git commit -m "feat(orchestrator): ModelRouter seam + TierRouter (fail-loud on unmapped tier)"
```

---

### Task 7: Config-driven Anthropic router (defaults + env + pricing)

**Files:**
- Edit: `go/orchestrator/router.go`
- Test: `go/orchestrator/router_test.go` (extend)

**Interfaces:**
- Consumes: Task 1's confirmed model IDs/pricing, `AnthropicModel`, `SpendMeter`, `os`.
- Produces: `RouterConfig`; `DefaultModelIDs()`, `ModelIDsFromEnv()` (env `AGENTKIT_MODEL_FULL`/`_MID`/`_CHEAP` override defaults), `DefaultPricing()`; `NewAnthropicRouter(cfg) *TierRouter` — builds one `AnthropicModel` per tier from configurable IDs, wiring the shared `SpendMeter` and per-tier `Pricing`. **Re-confirm the default IDs/pricing against Task 1 before committing.**

- [ ] **Step 1: Write the failing test**

```go
func TestModelIDsFromEnvOverrides(t *testing.T) {
	t.Setenv("AGENTKIT_MODEL_FULL", "custom-opus")
	ids := ModelIDsFromEnv()
	if ids[TierFull] != "custom-opus" {
		t.Fatalf("TierFull = %q, want custom-opus", ids[TierFull])
	}
	// Unset tiers fall back to documented defaults.
	if ids[TierCheap] != DefaultModelIDs()[TierCheap] {
		t.Fatalf("TierCheap = %q, want default", ids[TierCheap])
	}
}

func TestNewAnthropicRouterWiresModelIDs(t *testing.T) {
	// Fake API echoes the requested model id back as the text, proving the router
	// wired the right ModelID into the AnthropicModel for that tier.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"` + req.Model + `"}],` +
			`"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()

	r := NewAnthropicRouter(RouterConfig{
		APIKey:   "k",
		BaseURL:  srv.URL,
		ModelIDs: map[ModelTier]string{TierMid: "claude-sonnet-5"},
	})
	out, err := r.For(TierMid).Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out != "claude-sonnet-5" {
		t.Fatalf("router used model %q, want claude-sonnet-5", out)
	}
}
```

(Add `"encoding/json"`, `"net/http"`, `"net/http/httptest"` to `router_test.go`'s imports.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./orchestrator/ -run 'TestModelIDsFromEnvOverrides|TestNewAnthropicRouterWiresModelIDs' -v`
Expected: FAIL — `undefined: ModelIDsFromEnv` / `NewAnthropicRouter`.

- [ ] **Step 3: Write minimal implementation**

Append to `go/orchestrator/router.go` (add `"os"` to the import block):

```go
// DefaultModelIDs returns the current default model ID per tier. CONFIRM these
// against the claude-api skill (Task 1) before relying on them — model IDs drift.
// Current defaults: full=Opus, mid=Sonnet, cheap=Haiku.
func DefaultModelIDs() map[ModelTier]string {
	return map[ModelTier]string{
		TierFull:  "claude-opus-4-8",
		TierMid:   "claude-sonnet-5",
		TierCheap: "claude-haiku-4-5",
	}
}

// ModelIDsFromEnv starts from DefaultModelIDs and lets each tier be overridden by
// AGENTKIT_MODEL_FULL / _MID / _CHEAP (config-driven, never hardcoded at a call site).
func ModelIDsFromEnv() map[ModelTier]string {
	ids := DefaultModelIDs()
	if v := os.Getenv("AGENTKIT_MODEL_FULL"); v != "" {
		ids[TierFull] = v
	}
	if v := os.Getenv("AGENTKIT_MODEL_MID"); v != "" {
		ids[TierMid] = v
	}
	if v := os.Getenv("AGENTKIT_MODEL_CHEAP"); v != "" {
		ids[TierCheap] = v
	}
	return ids
}

// DefaultPricing returns current per-tier pricing (USD per million tokens).
// CONFIRM against the claude-api skill (Task 1) — pricing drifts.
func DefaultPricing() map[ModelTier]Pricing {
	return map[ModelTier]Pricing{
		TierFull:  {InputPerMTok: 5, OutputPerMTok: 25},
		TierMid:   {InputPerMTok: 3, OutputPerMTok: 15},
		TierCheap: {InputPerMTok: 1, OutputPerMTok: 5},
	}
}

// RouterConfig configures a router of real Anthropic models. ModelIDs/Pricing
// default to the env/documented values when nil; Meter (optional) is shared
// across every tier so the monthly ceiling spans the whole fleet.
type RouterConfig struct {
	APIKey     string
	ModelIDs   map[ModelTier]string
	Pricing    map[ModelTier]Pricing
	Meter      SpendMeter
	BaseURL    string
	HTTPClient *http.Client
	MaxTokens  int
}

// NewAnthropicRouter builds one AnthropicModel per configured tier.
func NewAnthropicRouter(cfg RouterConfig) *TierRouter {
	ids := cfg.ModelIDs
	if ids == nil {
		ids = ModelIDsFromEnv()
	}
	pricing := cfg.Pricing
	if pricing == nil {
		pricing = DefaultPricing()
	}
	models := make(map[ModelTier]Model, len(ids))
	for tier, id := range ids {
		models[tier] = &AnthropicModel{
			APIKey:     cfg.APIKey,
			ModelID:    id,
			BaseURL:    cfg.BaseURL,
			HTTPClient: cfg.HTTPClient,
			Meter:      cfg.Meter,
			Pricing:    pricing[tier],
			MaxTokens:  cfg.MaxTokens,
		}
	}
	return NewTierRouter(models)
}
```

Add `"net/http"` to `router.go`'s import block (used by `RouterConfig.HTTPClient`).

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./orchestrator/ -run 'TestModelIDsFromEnvOverrides|TestNewAnthropicRouterWiresModelIDs' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/orchestrator/router.go go/orchestrator/router_test.go
git commit -m "feat(orchestrator): config-driven Anthropic router (env model IDs + pricing + shared meter)"
```

---

### Task 8: Real-API smoke test (build tag) + final verify

**Files:**
- Create: `go/orchestrator/anthropic_smoke_test.go`

**Interfaces:**
- Consumes: `AnthropicModel`, `DefaultModelIDs`, `ANTHROPIC_API_KEY`. This is the ONLY code that touches the live endpoint; it is excluded from the default build by a tag and skipped without a key.

- [ ] **Step 1: Write the smoke test (behind a build tag)**

```go
//go:build anthropic_smoke

package orchestrator

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestAnthropicSmoke hits the real Anthropic API. Excluded from the default build
// by the anthropic_smoke tag; skipped without ANTHROPIC_API_KEY. Run manually:
//
//	cd go && ANTHROPIC_API_KEY=sk-ant-... go test -tags anthropic_smoke ./orchestrator/ -run TestAnthropicSmoke -v
func TestAnthropicSmoke(t *testing.T) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		t.Skip("set ANTHROPIC_API_KEY to run the live smoke test")
	}
	m := &AnthropicModel{APIKey: key, ModelID: DefaultModelIDs()[TierCheap], MaxTokens: 64}
	out, err := m.Run(context.Background(), "Reply with the single word: pong")
	if err != nil {
		t.Fatalf("live run: %v", err)
	}
	if !strings.Contains(strings.ToLower(out), "pong") {
		t.Fatalf("unexpected reply: %q", out)
	}
}
```

- [ ] **Step 2: Confirm the tagged test is excluded by default**

Run: `cd go && go vet ./orchestrator/... && go test ./orchestrator/...`
Expected: all PASS, and `TestAnthropicSmoke` does **not** run (no network hit) because the `anthropic_smoke` tag is not set.

- [ ] **Step 3: (Manual, optional) run the live smoke test**

With a real key available:
Run: `cd go && ANTHROPIC_API_KEY=sk-ant-... go test -tags anthropic_smoke ./orchestrator/ -run TestAnthropicSmoke -v`
Expected: PASS (or a clear API error). Do not commit any key.

- [ ] **Step 4: Verify the whole package + commit**

Run: `cd go && go build ./... && go vet ./orchestrator/... && go test ./orchestrator/...`
Expected: all PASS.

```bash
git add go/orchestrator/anthropic_smoke_test.go
git commit -m "test(orchestrator): live Anthropic smoke test behind anthropic_smoke build tag"
```

---

## Self-Review notes

- **Spec coverage (contracts §9, Slice B row):** `ModelRouter` (Task 6 + config Task 7) ✓; Anthropic `Model` impl behind the existing `Model` interface (Task 4, offline-tested) ✓; `SpendMeter` monthly ceiling halting dispatch (Task 3, wired Task 5) ✓; `ModelTier` consumed as a frozen type (Task 2) ✓; model IDs configurable via env/config with documented defaults confirmed via the claude-api skill (Tasks 1, 7) ✓; `ScriptedModel` retained as the deterministic double (Slice 0, reused in Task 6's fakes) ✓. **Deferred / out of scope:** the tick-driven manager exchange that *invokes* the router (Slice C), the Postgres-backed `SpendMeter`/`Telemetry` (Slice A), the `WorkerRuntime`/`Connector` seams (Slices C/D/F).
- **Offline guarantee:** every network test (Tasks 4, 5, 7) runs against `httptest.Server`; the only real-API code is the `anthropic_smoke`-tagged Task 8, excluded from `go test ./...`.
- **No new deps:** stdlib only — verified against `go.mod` (no Anthropic SDK vendored; adding one would burden the Liftability invariant for a seam this thin).
- **Type consistency:** `Model.Run(ctx, string) (string, error)` (Slice 0), `ModelTier`/`TierFull|Mid|Cheap`, `SpendMeter.Charge/Spent`, `ModelRouter.For` are used identically across tasks and are consumed verbatim from contracts §3/§5.

## Contract gaps found

1. **Shared-vocabulary ownership (§3).** The frozen string types (`ModelTier`, plus `TicketStatus`/`FragmentKind`/`ResultStatus`) have **no owning slice** in the §9 map — every slice "consumes" them but none is named their producer. Slice B is the first consumer of `ModelTier`, so this plan declares it (Task 2, `tier.go`). **Coordination risk:** Slices A/C also need `TicketStatus`/`ResultStatus`/`FragmentKind`; whoever lands first should agree on a single home (e.g. a shared `vocab.go`) to avoid duplicate `const` declarations. Flag before Slice A/C start.
2. **`SpendMeter` has no pre-dispatch probe.** The contract exposes only `Charge` and `Spent`, but §7.2 requires the ceiling to *halt dispatch*. This plan uses a zero-amount `Charge(ctx, 0, 0)` as the pre-flight probe (errors iff already at/over ceiling). Consequence, worth escalating if unacceptable: it is **charge-after** semantics — the call that first crosses the ceiling still completes; the *next* dispatch is halted. Overshoot is bounded to one call. If v1 needs a hard pre-charge reservation, the contract needs a `Reserve`/`CanSpend` method (a contract change → stop-and-escalate).
3. **Interface declaration ownership.** The contract *shapes* `SpendMeter` and `ModelRouter` but does not say which Go file declares them. As the producing slice, Slice B declares both (verbatim). Not a semantic gap, but noted so a later slice doesn't re-declare them.
4. **`ModelRouter.For(tier) Model` cannot signal an unknown tier** — the signature has no `error`. The plan returns a fail-loud `errModel` (errors on `Run`) rather than `nil`/panic. If v1 wants strict tier validation at wiring time, that's a call-site concern, not a contract change.
5. **Metering is coupled to the HTTP impl.** Because `Model.Run` returns only a string, token usage isn't visible to a generic decorator, so the `SpendMeter` is wired *inside* `AnthropicModel`. Consequence: `ScriptedModel` and any future non-HTTP `Model` are **unmetered**. Acceptable for v1 (only the real model spends money), but flag if a metered test double is later needed.
6. **Model IDs and pricing are absent from the contracts** (correctly deferred to config). Resolved here via the claude-api skill + `AGENTKIT_MODEL_*` env overrides + `DefaultPricing()`. Not a gap — recorded so the deferral is explicit.
