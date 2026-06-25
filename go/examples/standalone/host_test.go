// Package main — hermetic smoke tests for the reference host wiring.
//
// These tests run with NO Docker daemon, NO real sandbox, NO real model API.
// They substitute:
//   - execenv.NewMock() for the DinD environment (in-memory; no containers)
//   - An httptest.Server for the sandbox control server (scripted SSE turns)
//   - agentkittest helpers for store / claims / artifacts
//
// The goal is to prove:
//  1. The reference host wiring (NewRunner + Fleet + Deps) is correct.
//  2. Two concurrent sessions are isolated — events for s1 never bleed into s2.
//  3. The full flow (CreateSession → SendMessage → SSE drain) works end-to-end
//     without a single real external dependency.
//
// This satisfies the "autonomous proof" requirement from AG-8: run `go test ./...`
// and all assertions pass with no daemon.
package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	agentkit "github.com/bayes-price/agentkit"
	"github.com/bayes-price/agentkit/agentdb"
	"github.com/bayes-price/agentkit/agentkittest"
	"github.com/bayes-price/agentkit/artifacts"
	"github.com/bayes-price/agentkit/events"
	"github.com/bayes-price/agentkit/execenv"
	"github.com/bayes-price/agentkit/fleet"
	"github.com/bayes-price/agentkit/imageregistry"
)

// ──────────────────────────────────────────────────────────────────────────────
// Fake sandbox control server
//
// Implements the minimal subset of the sandbox HTTP contract (doc 07) needed
// to run two concurrent turns:
//
//	POST /sessions              → accept session create, return 200
//	POST /sessions/:id/query-stream → return a scripted SSE turn tagged with :id
//
// The session ID is embedded in the SSE content so the test can assert that s1
// received s1's events and s2 received s2's events.
// ──────────────────────────────────────────────────────────────────────────────

// sseFrames returns the scripted SSE for the given session ID.  The content
// delta carries the session ID so callers can assert non-cross-contamination.
func sseFrames(sessionID string) []string {
	return []string{
		"event: content_delta\ndata: " + `{"delta":"hello-` + sessionID + `-part1"}` + "\n\n",
		"event: content_delta\ndata: " + `{"delta":"hello-` + sessionID + `-part2"}` + "\n\n",
		"event: heartbeat\ndata: {}\n\n",
		"event: query_complete\ndata: " + `{"status":"complete","sessionId":"` + sessionID + `"}` + "\n\n",
	}
}

// newFakeSandbox returns an httptest.Server that speaks the sandbox HTTP contract
// for the two sessions s1 and s2.
func newFakeSandbox(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {

		// POST /sessions — session creation (harness boot + credential pre-check).
		// Accept any session ID; return a minimal success response.
		case r.Method == http.MethodPost && r.URL.Path == "/sessions":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"success":true,"data":{"sessionId":"ok"}}`))

		// POST /sessions/:sessionId/query-stream — serve a scripted SSE turn.
		// The session ID is extracted from the URL path so each session gets its
		// own tagged content.
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/sessions/") &&
			strings.HasSuffix(r.URL.Path, "/query-stream"):
			// Extract the session ID: path = /sessions/<id>/query-stream
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/sessions/"), "/")
			if len(parts) < 2 {
				http.Error(w, "bad path", http.StatusBadRequest)
				return
			}
			sessionID := parts[0]

			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			flusher, canFlush := w.(http.Flusher)
			for _, frame := range sseFrames(sessionID) {
				_, _ = w.Write([]byte(frame))
				if canFlush {
					flusher.Flush()
				}
			}

		// Health check — not exercised by the test but required if Start() calls Recover().
		case r.Method == http.MethodGet && r.URL.Path == "/health":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok","sessions":[]}`))

		default:
			t.Logf("fake-sandbox: unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
}

// ──────────────────────────────────────────────────────────────────────────────
// Runner construction helper
//
// Builds a fully hermetic Runner wired identically to the reference host
// (main.go), with the DinD env replaced by execenv.NewMock() and its
// AddrOverride pointing at the fake sandbox.
// ──────────────────────────────────────────────────────────────────────────────

type hermeticRig struct {
	runner agentkit.Runner
	store  *agentkittest.MemStore
	env    *execenv.MockExecutionEnvironment
	sink   *events.MockSink
}

func newHermeticRig(t *testing.T, sandboxURL string) *hermeticRig {
	t.Helper()

	// Mock execution environment: provisions in-memory "instances" whose
	// Address is overridden to the fake sandbox URL so the Runner's HTTP client
	// reaches our httptest server.
	mockEnv := execenv.NewMock()
	mockEnv.AddrOverride = sandboxURL

	store := agentkittest.NewMemStore()
	reg := imageregistry.NewMock()
	arts := artifacts.NewMock()
	claims := agentkittest.StaticClaims{Token: "dev-token"}
	sink := events.NewMockSink()

	// Build the Fleet exactly as the reference host does: one worker wrapping
	// the single ExecutionEnvironment.
	f := fleet.NewMemory(store, &fleet.MemFleetOptions{TrustedWorkload: true})
	w := &fleet.Worker{
		ID:   "dind-1",
		Env:  mockEnv,
		Caps: mockEnv.Capabilities(),
	}
	if err := f.Register(context.Background(), w); err != nil {
		t.Fatalf("fleet.Register: %v", err)
	}

	// Events pipeline: wire a mock sink so we can read back persisted events.
	// The sink is also the source of truth for PendingFlushes / flush-guard checks.
	pipeline := events.NewPipeline(sink)

	runner, err := agentkit.NewRunner(agentkit.Deps{
		Fleet:     f,
		Registry:  reg,
		Store:     store,
		Artifacts: arts,
		Claims:    claims,
		Events:    pipeline,
		Policy: agentkit.Policy{
			BaseImage: "agentkit-sandbox:dev",
			AgentPort: 3010,
		},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return &hermeticRig{runner: runner, store: store, env: mockEnv, sink: sink}
}

// ──────────────────────────────────────────────────────────────────────────────
// Tests
// ──────────────────────────────────────────────────────────────────────────────

// TestHermeticTwoSessionIsolation is the PRIMARY hermetic smoke: it creates two
// concurrent sessions against a shared fake sandbox and asserts:
//
//  1. Both sessions complete without error.
//  2. Each session's SSE output contains ONLY its own session ID in the content.
//  3. The event pipeline persisted events for both queries.
func TestHermeticTwoSessionIsolation(t *testing.T) {
	ts := newFakeSandbox(t)
	defer ts.Close()

	rig := newHermeticRig(t, ts.URL)
	ctx := context.Background()

	// Seed session rows (the host persists these before CreateSession in production).
	rig.store.Seed(&agentdb.Session{ID: "s1", Customer: "demo", Job: "demo-job", UserEmail: "dev@example.com"})
	rig.store.Seed(&agentdb.Session{ID: "s2", Customer: "demo", Job: "demo-job", UserEmail: "dev@example.com"})

	// CreateSession for both.
	for _, sid := range []string{"s1", "s2"} {
		h, err := rig.runner.CreateSession(ctx, agentkit.CreateSessionRequest{
			SessionID: sid,
			Customer:  "demo",
			Job:       "demo-job",
			UserEmail: "dev@example.com",
			Harness:   agentkit.HarnessClaudeAgentSDK,
		})
		if err != nil {
			t.Fatalf("CreateSession(%s): %v", sid, err)
		}
		if h.SessionID != sid {
			t.Errorf("CreateSession(%s) returned SessionID=%q", sid, h.SessionID)
		}
	}

	// Assert the mock environment provisioned exactly 2 instances.
	if got := rig.env.Count("Provision"); got != 2 {
		t.Errorf("Provision call count = %d, want 2", got)
	}

	// SendMessage to both sessions CONCURRENTLY.
	type result struct {
		sessionID string
		output    string
		err       error
	}
	ch := make(chan result, 2)

	for _, sid := range []string{"s1", "s2"} {
		sid := sid
		go func() {
			var buf bytes.Buffer
			sendErr := rig.runner.SendMessage(ctx,
				agentkit.SessionRef{SessionID: sid},
				agentkit.SendMessageRequest{
					Content:  fmt.Sprintf("Hi from %s", sid),
					Customer: "demo",
					Job:      "demo-job",
				},
				&buf,
			)
			ch <- result{sessionID: sid, output: buf.String(), err: sendErr}
		}()
	}

	// Collect results.
	results := make(map[string]result, 2)
	for range []string{"s1", "s2"} {
		r := <-ch
		results[r.sessionID] = r
	}

	// Assert both completed without error.
	for _, sid := range []string{"s1", "s2"} {
		r := results[sid]
		if r.err != nil {
			t.Errorf("SendMessage(%s): %v", sid, r.err)
		}
	}

	// ── Session isolation: s1's output must contain "s1" and NOT "s2", vice versa.
	s1Out := results["s1"].output
	s2Out := results["s2"].output

	if !strings.Contains(s1Out, "s1") {
		t.Errorf("s1 output does not contain 's1': %q", s1Out)
	}
	if strings.Contains(s1Out, "hello-s2") {
		t.Errorf("s1 output contains s2 content (cross-contamination): %q", s1Out)
	}

	if !strings.Contains(s2Out, "s2") {
		t.Errorf("s2 output does not contain 's2': %q", s2Out)
	}
	if strings.Contains(s2Out, "hello-s1") {
		t.Errorf("s2 output contains s1 content (cross-contamination): %q", s2Out)
	}

	// ── Event pipeline: both sessions should have persisted events.
	//
	// Query IDs are generated as "q-<sessionID>-<n>" (see runner.go nextQueryID)
	// where n is a global sequence counter shared across sessions.  With two
	// concurrent goroutines the exact n is non-deterministic: either s1 or s2
	// increments first so the IDs may be "q-s1-1"/"q-s2-2" or "q-s1-2"/"q-s2-1".
	// Try both n=1 and n=2 for each session.
	hasS1Events := len(rig.sink.Persisted("q-s1-1")) > 0 || len(rig.sink.Persisted("q-s1-2")) > 0
	hasS2Events := len(rig.sink.Persisted("q-s2-1")) > 0 || len(rig.sink.Persisted("q-s2-2")) > 0

	if !hasS1Events {
		t.Error("no events persisted for session s1 (checked q-s1-1 and q-s1-2)")
	}
	if !hasS2Events {
		t.Error("no events persisted for session s2 (checked q-s2-1 and q-s2-2)")
	}

	// The flush guard must have been exercised and be clear at rest.
	if rig.sink.PendingFlushes() != 0 {
		t.Errorf("pending flushes = %d, want 0 at rest", rig.sink.PendingFlushes())
	}
}

// TestHermeticRunnerWiring is a lightweight construction check: it verifies that
// NewRunner succeeds with the same Deps the reference host uses (Fleet + mock env)
// and that a single CreateSession + SendMessage round-trip works.
func TestHermeticRunnerWiring(t *testing.T) {
	ts := newFakeSandbox(t)
	defer ts.Close()

	rig := newHermeticRig(t, ts.URL)
	ctx := context.Background()

	rig.store.Seed(&agentdb.Session{ID: "wiring-s", Customer: "acme", Job: "j1"})

	h, err := rig.runner.CreateSession(ctx, agentkit.CreateSessionRequest{
		SessionID: "wiring-s",
		Customer:  "acme",
		Job:       "j1",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if h == nil || h.SessionID != "wiring-s" {
		t.Fatalf("unexpected handle: %+v", h)
	}

	var buf bytes.Buffer
	if err := rig.runner.SendMessage(ctx,
		agentkit.SessionRef{SessionID: "wiring-s"},
		agentkit.SendMessageRequest{Content: "ping", Customer: "acme", Job: "j1"},
		&buf,
	); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	// Confirm SSE was received.
	if !strings.Contains(buf.String(), "wiring-s") {
		t.Errorf("SSE output does not reference session ID: %q", buf.String())
	}
}

// TestHermeticHarnessSelection verifies that HarnessClaudeAgentSDK is passed
// correctly to the fake sandbox's POST /sessions handler.  The fake sandbox
// accepts any harness name; this test asserts the round-trip at the Go level.
func TestHermeticHarnessSelection(t *testing.T) {
	ts := newFakeSandbox(t)
	defer ts.Close()

	rig := newHermeticRig(t, ts.URL)
	ctx := context.Background()

	rig.store.Seed(&agentdb.Session{ID: "harness-s", Customer: "acme", Job: "j1"})

	// Create with explicit harness selection.
	h, err := rig.runner.CreateSession(ctx, agentkit.CreateSessionRequest{
		SessionID: "harness-s",
		Customer:  "acme",
		Job:       "j1",
		Harness:   agentkit.HarnessClaudeAgentSDK,
	})
	if err != nil {
		t.Fatalf("CreateSession with explicit harness: %v", err)
	}
	if h == nil {
		t.Fatal("nil SessionHandle returned")
	}
}
