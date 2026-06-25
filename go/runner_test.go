package agentkit

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bayes-price/agentkit/agentdb"
	"github.com/bayes-price/agentkit/agentkittest"
	"github.com/bayes-price/agentkit/artifacts"
	"github.com/bayes-price/agentkit/events"
	"github.com/bayes-price/agentkit/execenv"
	"github.com/bayes-price/agentkit/fleet"
	"github.com/bayes-price/agentkit/imageregistry"
)

// newTestRunner wires the Runner entirely from in-memory mocks — no Docker, no
// registry, no blob backend, no auth server. This is the hermetic-test recipe
// from docs/10-extension-points.md.
func newTestRunner(t *testing.T) (*runnerImpl, *execenv.MockExecutionEnvironment, *imageregistry.MockImageRegistry, *agentkittest.MemStore, *artifacts.MockArtifactStore, *events.MockSink) {
	t.Helper()
	env := execenv.NewMock()
	reg := imageregistry.NewMock()
	store := agentkittest.NewMemStore()
	arts := artifacts.NewMock()
	sink := events.NewMockSink()
	runner, err := NewRunner(Deps{
		Env:       env,
		Registry:  reg,
		Store:     store,
		Artifacts: arts,
		Claims:    agentkittest.StaticClaims{Token: "test-token"},
		Events:    events.NewPipeline(sink),
		Policy:    Policy{BaseImage: "agentkit-sandbox:test"},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	r := runner.(*runnerImpl)
	return r, env, reg, store, arts, sink
}

// TestSessionEnv_ForwardsPolicySessionEnv locks the env-passthrough: a host's
// Policy.SessionEnv (model-provider config the in-image agent requires) must be
// injected into the session env, with session-specific keys winning on collision.
func TestSessionEnv_ForwardsPolicySessionEnv(t *testing.T) {
	runner, err := NewRunner(Deps{
		Env:       execenv.NewMock(),
		Registry:  imageregistry.NewMock(),
		Store:     agentkittest.NewMemStore(),
		Artifacts: artifacts.NewMock(),
		Claims:    agentkittest.StaticClaims{Token: "test-token"},
		Events:    events.NewPipeline(events.NewMockSink()),
		Policy: Policy{
			BaseImage: "agentkit-sandbox:test",
			SessionEnv: map[string]string{
				"ANTHROPIC_BASE_URL": "http://proxy:4000",
				"SESSION_ID":         "host-should-lose", // session-specific must win
			},
		},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	got := runner.(*runnerImpl).sessionEnv("s1", "tok", "model-x")

	if got["ANTHROPIC_BASE_URL"] != "http://proxy:4000" {
		t.Errorf("Policy.SessionEnv not forwarded: ANTHROPIC_BASE_URL=%q", got["ANTHROPIC_BASE_URL"])
	}
	if got["SESSION_ID"] != "s1" {
		t.Errorf("session-specific SESSION_ID must override host value, got %q", got["SESSION_ID"])
	}
	if got["SESSION_TOKEN"] != "tok" {
		t.Errorf("SESSION_TOKEN=%q, want tok", got["SESSION_TOKEN"])
	}
	if got["DEFAULT_MODEL"] != "model-x" {
		t.Errorf("DEFAULT_MODEL=%q, want model-x", got["DEFAULT_MODEL"])
	}
}

// TestSessionEnv_NilPolicySessionEnv: the merge is nil-safe (no Policy.SessionEnv).
func TestSessionEnv_NilPolicySessionEnv(t *testing.T) {
	r, _, _, _, _, _ := newTestRunner(t)
	got := r.sessionEnv("s1", "tok", "")
	if got["SESSION_ID"] != "s1" || got["SESSION_TOKEN"] != "tok" {
		t.Errorf("base session keys wrong: %v", got)
	}
	if _, ok := got["DEFAULT_MODEL"]; ok {
		t.Errorf("DEFAULT_MODEL should be absent when model is empty, got %q", got["DEFAULT_MODEL"])
	}
}

func TestRunnerLifecycleWithMocks(t *testing.T) {
	ctx := context.Background()
	r, env, reg, store, arts, _ := newTestRunner(t)
	store.Seed(&agentdb.Session{ID: "s1", Customer: "acme", Job: "j1", UserEmail: "u@example.com"})

	// Create → provisions an instance from the base image.
	if _, err := r.CreateSession(ctx, CreateSessionRequest{SessionID: "s1", Customer: "acme", Job: "j1", UserEmail: "u@example.com"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if got := reg.Count("EnsurePresent"); got != 1 {
		t.Errorf("EnsurePresent calls = %d, want 1", got)
	}
	if got := env.Count("Provision"); got != 1 {
		t.Errorf("Provision calls = %d, want 1", got)
	}

	// Snapshot → commit image, persist handle, store it durably.
	h, err := r.Snapshot(ctx, SessionRef{SessionID: "s1"})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if h.Ref == "" {
		t.Fatal("Snapshot returned empty handle")
	}
	if env.Count("Snapshot") != 1 || reg.Count("Persist") != 1 {
		t.Errorf("expected one Snapshot + one Persist, got %d/%d", env.Count("Snapshot"), reg.Count("Persist"))
	}
	if got, ok, _ := store.GetSnapshotHandle(ctx, "s1"); !ok || got.Ref != h.Ref {
		t.Errorf("snapshot handle not persisted to store")
	}

	// Destroy → tears down instance, marks artifacts lost.
	if err := r.Destroy(ctx, SessionRef{SessionID: "s1"}); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if arts.Count("MarkLost") != 1 {
		t.Errorf("MarkLost calls = %d, want 1", arts.Count("MarkLost"))
	}

	// Resume after destroy → restores from the snapshot handle (Materialize + Provision).
	if _, err := r.Resume(ctx, SessionRef{SessionID: "s1"}); err != nil {
		t.Fatalf("Resume after destroy: %v", err)
	}
	if reg.Count("Materialize") != 1 {
		t.Errorf("Materialize calls = %d, want 1 (restore-from-snapshot)", reg.Count("Materialize"))
	}
	if env.Count("Provision") != 2 {
		t.Errorf("Provision calls = %d, want 2 (create + restore)", env.Count("Provision"))
	}
}

func TestSendMessageStreamsAndPersistsCompacted(t *testing.T) {
	ctx := context.Background()
	r, env, _, store, _, sink := newTestRunner(t)
	store.Seed(&agentdb.Session{ID: "s1", Customer: "acme", Job: "j1"})

	// A fake in-image agent serving the session-scoped HTTP contract.
	// POST /sessions → 200 (session created)
	// POST /sessions/:id/query-stream → SSE turn
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/sessions":
			// Session creation: accept any payload and return 200.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"success":true,"data":{"sessionId":"s1"}}`))
			return
		case req.Method == http.MethodPost && req.URL.Path == "/sessions/s1/query-stream":
			// Session-scoped query-stream: serve an SSE turn.
			w.Header().Set("Content-Type", "text/event-stream")
			fl, _ := w.(http.Flusher)
			frames := []string{
				"event: content_delta\ndata: {\"delta\":\"Hello \"}\n\n",
				"event: content_delta\ndata: {\"delta\":\"world\"}\n\n",
				"event: heartbeat\ndata: {}\n\n",
				"event: query_complete\ndata: {\"status\":\"complete\"}\n\n",
			}
			for _, f := range frames {
				_, _ = w.Write([]byte(f))
				if fl != nil {
					fl.Flush()
				}
			}
			return
		default:
			http.NotFound(w, req)
		}
	}))
	defer ts.Close()
	env.AddrOverride = ts.URL

	if _, err := r.CreateSession(ctx, CreateSessionRequest{SessionID: "s1", Customer: "acme", Job: "j1"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	var buf bytes.Buffer
	if err := r.SendMessage(ctx, SessionRef{SessionID: "s1", ScopedToken: "test-token"}, SendMessageRequest{Content: "hi"}, &buf); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	// The raw SSE was teed to the client.
	if !strings.Contains(buf.String(), "Hello ") || !strings.Contains(buf.String(), "world") {
		t.Errorf("client did not receive streamed content: %q", buf.String())
	}
	// The user_message is persisted, not streamed — the live client renders the
	// prompt optimistically, so teeing it would produce a duplicate bubble.
	if strings.Contains(buf.String(), "user_message") {
		t.Errorf("user_message must not be teed to the client: %q", buf.String())
	}

	// The events were persisted COMPACTED: the user_message for the prompt is
	// seeded ahead of the streamed events, two content_delta merged into one, the
	// heartbeat dropped, query_complete kept → 3 events.
	persisted := sink.Persisted("q-s1-1")
	if len(persisted) != 3 {
		t.Fatalf("persisted %d events, want 3 (user_message + merged delta + query_complete); got %#v", len(persisted), persisted)
	}
	if persisted[0].Type != events.UserMessage {
		t.Errorf("first persisted event = %q, want user_message", persisted[0].Type)
	}
	if c, _ := persisted[0].Data["content"].(string); c != "hi" {
		t.Errorf("user_message content = %q, want %q", c, "hi")
	}
	if persisted[1].Type != events.ContentDelta {
		t.Errorf("second persisted event = %q, want content_delta", persisted[1].Type)
	}
	if d, _ := persisted[1].Data["delta"].(string); d != "Hello world" {
		t.Errorf("merged delta = %q, want %q", d, "Hello world")
	}

	// The flush guard ran (BeginFlush/EndFlush around the persist) and is now clear.
	if sink.MaxConcurrentFlushes() < 1 {
		t.Errorf("expected at least one tracked flush")
	}
	if sink.PendingFlushes() != 0 {
		t.Errorf("pending flushes = %d, want 0 at rest", sink.PendingFlushes())
	}
}

func TestStreamUsesSessionScopedPathForBothNormalAndReconnect(t *testing.T) {
	// Issue 2 fix: Stream() must use /sessions/:sessionId/stream/:queryId for
	// BOTH normal attach and reconnect (IsReconnect=true). The /reconnect flat
	// path does not exist in the HTTP contract (doc 07).
	ctx := context.Background()
	r, env, _, store, _, _ := newTestRunner(t)
	store.Seed(&agentdb.Session{ID: "s2", Customer: "acme", Job: "j1"})

	// Capture the paths hit by Stream calls.
	var hitPaths []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/sessions":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"success":true,"data":{"sessionId":"s2"}}`))
		case req.Method == http.MethodGet:
			hitPaths = append(hitPaths, req.URL.Path)
			w.Header().Set("Content-Type", "text/event-stream")
			// Return minimal SSE then close.
			_, _ = w.Write([]byte("event: query_complete\ndata: {}\n\n"))
		default:
			http.NotFound(w, req)
		}
	}))
	defer ts.Close()
	env.AddrOverride = ts.URL

	if _, err := r.CreateSession(ctx, CreateSessionRequest{SessionID: "s2", Customer: "acme", Job: "j1"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	ref := SessionRef{SessionID: "s2"}
	expectedPath := "/sessions/s2/stream/q-abc"

	// Normal stream attach.
	var buf1 bytes.Buffer
	if err := r.Stream(ctx, ref, StreamOptions{QueryID: "q-abc", IsReconnect: false}, &buf1); err != nil {
		t.Fatalf("Stream (normal): %v", err)
	}

	// Reconnect path — must use the same session-scoped URL (not /reconnect).
	var buf2 bytes.Buffer
	if err := r.Stream(ctx, ref, StreamOptions{QueryID: "q-abc", IsReconnect: true}, &buf2); err != nil {
		t.Fatalf("Stream (reconnect): %v", err)
	}

	if len(hitPaths) != 2 {
		t.Fatalf("expected 2 GET requests, got %d: %v", len(hitPaths), hitPaths)
	}
	for i, p := range hitPaths {
		if p != expectedPath {
			t.Errorf("Stream call %d: got path %q, want %q", i+1, p, expectedPath)
		}
	}
}

// --- Fleet-aware runner tests -----------------------------------------------

// newTwoWorkerRunner builds a Runner with a two-worker fleet. Both workers use
// separate MockExecutionEnvironments so we can assert which one provisioned what.
func newTwoWorkerRunner(t *testing.T) (*runnerImpl, *execenv.MockExecutionEnvironment, *execenv.MockExecutionEnvironment, *agentkittest.MemStore, *imageregistry.MockImageRegistry) {
	t.Helper()
	env1 := execenv.NewMock()
	env2 := execenv.NewMock()
	store := agentkittest.NewMemStore()
	reg := imageregistry.NewMock()
	arts := artifacts.NewMock()
	sink := events.NewMockSink()

	// Build a two-worker fleet directly (not the single-env shim).
	f := fleet.NewMemory(store, &fleet.MemFleetOptions{TrustedWorkload: false})
	ctx := context.Background()
	w1 := &fleet.Worker{ID: "w1", Env: env1, Caps: env1.Capabilities()}
	w2 := &fleet.Worker{ID: "w2", Env: env2, Caps: env2.Capabilities()}
	if err := f.Register(ctx, w1); err != nil {
		t.Fatalf("Register w1: %v", err)
	}
	if err := f.Register(ctx, w2); err != nil {
		t.Fatalf("Register w2: %v", err)
	}

	runner, err := NewRunner(Deps{
		Fleet:     f,
		Registry:  reg,
		Store:     store,
		Artifacts: arts,
		Claims:    agentkittest.StaticClaims{Token: "test-token"},
		Events:    events.NewPipeline(sink),
		Policy:    Policy{BaseImage: "agentkit-sandbox:test"},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return runner.(*runnerImpl), env1, env2, store, reg
}

// TestWorkerLoss_WithSnapshot tests that when the bound worker is gone but a
// snapshot exists, ensureRunning rebinds to a healthy worker and restores (Materialize + Provision).
func TestWorkerLoss_WithSnapshot(t *testing.T) {
	ctx := context.Background()
	r, env1, env2, store, reg := newTwoWorkerRunner(t)

	store.Seed(&agentdb.Session{ID: "s-loss", Customer: "acme", Job: "j1"})

	// Step 1: CreateSession → lands on w1 (the first PlaceForSession picks it; with default
	// LeastLoaded and equal load, it will pick one of them deterministically).
	if _, err := r.CreateSession(ctx, CreateSessionRequest{SessionID: "s-loss", Customer: "acme", Job: "j1"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// Determine which worker was chosen.
	boundWorkerID := r.getWorkerID("s-loss")
	if boundWorkerID == "" {
		t.Fatal("expected worker to be tracked after CreateSession")
	}

	// Step 2: Snapshot the session (persists a handle).
	if _, err := r.Snapshot(ctx, SessionRef{SessionID: "s-loss"}); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	h, ok, _ := store.GetSnapshotHandle(ctx, "s-loss")
	if !ok || h.Ref == "" {
		t.Fatal("expected snapshot handle in store")
	}

	// Step 3: Deregister the bound worker.
	if err := r.deps.Fleet.Deregister(ctx, boundWorkerID, fleet.DrainImmediate); err != nil {
		t.Fatalf("Deregister: %v", err)
	}

	// Step 4: Simulate that the runner lost the in-memory instance for this session
	// (the worker crashed, so the in-memory instance is stale). We simulate this by
	// changing the tracked workerID to the dead worker (already the case since Deregister
	// doesn't touch instanceWorkers) and calling ensureRunning.
	//
	// ensureRunning should:
	//   1. WorkerForSession("s-loss") → binding to dead worker → error
	//   2. PlaceForSession → picks the remaining worker
	//   3. ensurePerSessionInstance detects workerChanged → clears stale instance
	//   4. restoreToWorker → Materialize + Provision on the new worker

	provBefore1 := env1.Count("Provision")
	provBefore2 := env2.Count("Provision")
	matBefore := reg.Count("Materialize")

	inst, err := r.ensureRunning(ctx, "s-loss")
	if err != nil {
		t.Fatalf("ensureRunning after worker loss (with snapshot): %v", err)
	}
	if inst == nil {
		t.Fatal("ensureRunning returned nil instance")
	}

	// Exactly one more Materialize + one more Provision across the two workers combined.
	if reg.Count("Materialize") != matBefore+1 {
		t.Errorf("Materialize calls: got %d, want %d", reg.Count("Materialize"), matBefore+1)
	}
	totalNewProv := (env1.Count("Provision") - provBefore1) + (env2.Count("Provision") - provBefore2)
	if totalNewProv != 1 {
		t.Errorf("expected 1 new Provision (restore), got %d", totalNewProv)
	}
}

// TestWorkerLoss_NoSnapshot tests that when the bound worker is gone and no
// snapshot exists, ensureRunning returns a clear unrecoverable error.
func TestWorkerLoss_NoSnapshot(t *testing.T) {
	ctx := context.Background()
	r, _, _, store, _ := newTwoWorkerRunner(t)

	store.Seed(&agentdb.Session{ID: "s-nosnap", Customer: "acme", Job: "j1"})

	// Step 1: CreateSession.
	if _, err := r.CreateSession(ctx, CreateSessionRequest{SessionID: "s-nosnap", Customer: "acme", Job: "j1"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	boundWorkerID := r.getWorkerID("s-nosnap")

	// Step 2: Deregister the bound worker WITHOUT snapshotting.
	if err := r.deps.Fleet.Deregister(ctx, boundWorkerID, fleet.DrainImmediate); err != nil {
		t.Fatalf("Deregister: %v", err)
	}

	// Step 3: ensureRunning must return a clear error (not silently succeed).
	_, err := r.ensureRunning(ctx, "s-nosnap")
	if err == nil {
		t.Fatal("expected ensureRunning to return an error when worker is gone and no snapshot exists, got nil")
	}
	// The error message must be meaningful (not just "worker gone").
	errStr := err.Error()
	if !strings.Contains(errStr, "re-created") && !strings.Contains(errStr, "unrecoverable") && !strings.Contains(errStr, "no snapshot") {
		t.Errorf("error message does not indicate the session is unrecoverable: %q", errStr)
	}
}

// TestProvision_ForwardsPolicyMounts locks the dev-mount passthrough: a host's
// Policy.Mounts (dev-only hot-reload binds, e.g. skills/plugins source mounted
// through DinD) must reach the engine's ProvisionSpec for session containers.
func TestProvision_ForwardsPolicyMounts(t *testing.T) {
	ctx := context.Background()
	env := execenv.NewMock()
	mounts := []execenv.Mount{
		{Source: "/sandbox-src/plugins", Target: "/app/product-plugins", ReadOnly: true},
		{Source: "/sandbox-src/workspace-lib", Target: "/workspace/lib", ReadOnly: true},
	}
	store := agentkittest.NewMemStore()
	runner, err := NewRunner(Deps{
		Env:       env,
		Registry:  imageregistry.NewMock(),
		Store:     store,
		Artifacts: artifacts.NewMock(),
		Claims:    agentkittest.StaticClaims{Token: "test-token"},
		Events:    events.NewPipeline(events.NewMockSink()),
		Policy:    Policy{BaseImage: "agentkit-sandbox:test", Mounts: mounts},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	r := runner.(*runnerImpl)
	store.Seed(&agentdb.Session{ID: "s1", Customer: "acme", Job: "j1", UserEmail: "u@example.com"})

	if _, err := r.CreateSession(ctx, CreateSessionRequest{SessionID: "s1", Customer: "acme", Job: "j1", UserEmail: "u@example.com"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if len(env.Provisions) != 1 {
		t.Fatalf("Provisions recorded = %d, want 1", len(env.Provisions))
	}
	got := env.Provisions[0].Mounts
	if len(got) != 2 || got[0] != mounts[0] || got[1] != mounts[1] {
		t.Errorf("ProvisionSpec.Mounts = %+v, want %+v", got, mounts)
	}
}

// TestSharedTenancy_ReusesOneInstance tests that a shared-tenancy worker uses
// only ONE provisioned instance for two different sessions. Both sessions are
// routed to the shared instance (Provision called once, not twice).
func TestSharedTenancy_ReusesOneInstance(t *testing.T) {
	ctx := context.Background()
	store := agentkittest.NewMemStore()
	reg := imageregistry.NewMock()
	arts := artifacts.NewMock()
	sink := events.NewMockSink()

	// Shared tenancy at TierVM (trusted, so no gate violation).
	sharedCaps := execenv.Capabilities{
		Backend:          execenv.BackendDockerDinD,
		Tenancy:          execenv.TenancyShared,
		IsolationTier:    execenv.TierVM,
		SupportsSnapshot: false,
	}
	sharedEnv := execenv.NewMock()
	sharedEnv.Caps = &sharedCaps

	f := fleet.NewMemory(store, &fleet.MemFleetOptions{TrustedWorkload: false})
	w := &fleet.Worker{ID: "shared-w", Env: sharedEnv, Caps: sharedCaps}
	if err := f.Register(ctx, w); err != nil {
		t.Fatalf("Register shared worker: %v", err)
	}

	runner, err := NewRunner(Deps{
		Fleet:     f,
		Registry:  reg,
		Store:     store,
		Artifacts: arts,
		Claims:    agentkittest.StaticClaims{Token: "t"},
		Events:    events.NewPipeline(sink),
		Policy:    Policy{BaseImage: "shared-image:test"},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	r := runner.(*runnerImpl)

	store.Seed(&agentdb.Session{ID: "shared-s1", Customer: "acme", Job: "j1"})
	store.Seed(&agentdb.Session{ID: "shared-s2", Customer: "acme", Job: "j1"})

	// CreateSession for two different sessions.
	if _, err := r.CreateSession(ctx, CreateSessionRequest{SessionID: "shared-s1", Customer: "acme", Job: "j1"}); err != nil {
		t.Fatalf("CreateSession s1: %v", err)
	}

	// Provision should have been called once (for the shared instance).
	if sharedEnv.Count("Provision") != 1 {
		t.Errorf("after first CreateSession: Provision = %d, want 1", sharedEnv.Count("Provision"))
	}

	if _, err := r.CreateSession(ctx, CreateSessionRequest{SessionID: "shared-s2", Customer: "acme", Job: "j1"}); err != nil {
		t.Fatalf("CreateSession s2: %v", err)
	}

	// Provision must still be 1 (shared instance reused).
	if sharedEnv.Count("Provision") != 1 {
		t.Errorf("after second CreateSession: Provision = %d, want 1 (shared instance reused)", sharedEnv.Count("Provision"))
	}

	// ensureRunning for both sessions must return the same instance address.
	inst1, err := r.ensureRunning(ctx, "shared-s1")
	if err != nil {
		t.Fatalf("ensureRunning s1: %v", err)
	}
	inst2, err := r.ensureRunning(ctx, "shared-s2")
	if err != nil {
		t.Fatalf("ensureRunning s2: %v", err)
	}
	if inst1.Address != inst2.Address {
		t.Errorf("shared instances have different addresses: s1=%q s2=%q", inst1.Address, inst2.Address)
	}
	// Still only one Provision.
	if sharedEnv.Count("Provision") != 1 {
		t.Errorf("after ensureRunning: Provision = %d, want 1", sharedEnv.Count("Provision"))
	}
}

// TestStatusReportsHasSnapshot verifies that Status.HasSnapshot is true when a
// durable snapshot handle exists in the store, and false when it does not.
func TestStatusReportsHasSnapshot(t *testing.T) {
	ctx := context.Background()
	r, _, _, store, _, _ := newTestRunner(t)
	sid := "sess-hassnap"
	store.Seed(&agentdb.Session{ID: sid, Customer: "acme", Job: "j1"})

	// No snapshot yet: HasSnapshot must be false.
	st, err := r.Status(ctx, SessionRef{SessionID: sid})
	if err != nil {
		t.Fatal(err)
	}
	if st.HasSnapshot {
		t.Fatal("expected HasSnapshot=false before any snapshot is set")
	}

	// Set a snapshot handle directly via the store.
	if err := store.SetSnapshotHandle(ctx, sid, imageregistry.Handle{Kind: "registry", Ref: "r/x:latest"}); err != nil {
		t.Fatal(err)
	}

	// Now HasSnapshot must be true.
	st, err = r.Status(ctx, SessionRef{SessionID: sid})
	if err != nil {
		t.Fatal(err)
	}
	if !st.HasSnapshot {
		t.Fatalf("expected HasSnapshot=true when a snapshot handle exists")
	}
}

// TestSharedTenancy_SnapshotBan verifies that calling Snapshot on a session
// whose worker has TenancyShared / SupportsSnapshot=false returns a clear
// error rather than silently producing an incorrect diff archive.
func TestSharedTenancy_SnapshotBan(t *testing.T) {
	ctx := context.Background()
	store := agentkittest.NewMemStore()
	reg := imageregistry.NewMock()
	arts := artifacts.NewMock()
	sink := events.NewMockSink()

	// Shared tenancy, snapshot not supported.
	sharedCaps := execenv.Capabilities{
		Backend:          execenv.BackendDockerDinD,
		Tenancy:          execenv.TenancyShared,
		IsolationTier:    execenv.TierVM,
		SupportsSnapshot: false,
	}
	sharedEnv := execenv.NewMock()
	sharedEnv.Caps = &sharedCaps

	f := fleet.NewMemory(store, &fleet.MemFleetOptions{TrustedWorkload: false})
	w := &fleet.Worker{ID: "shared-snap-w", Env: sharedEnv, Caps: sharedCaps}
	if err := f.Register(ctx, w); err != nil {
		t.Fatalf("Register shared worker: %v", err)
	}

	runner, err := NewRunner(Deps{
		Fleet:     f,
		Registry:  reg,
		Store:     store,
		Artifacts: arts,
		Claims:    agentkittest.StaticClaims{Token: "t"},
		Events:    events.NewPipeline(sink),
		Policy:    Policy{BaseImage: "shared-image:test"},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	r := runner.(*runnerImpl)

	store.Seed(&agentdb.Session{ID: "shared-snap-s1", Customer: "acme", Job: "j1"})

	if _, err := r.CreateSession(ctx, CreateSessionRequest{SessionID: "shared-snap-s1", Customer: "acme", Job: "j1"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Snapshot MUST return an error on shared tenancy.
	_, snapErr := r.Snapshot(ctx, SessionRef{SessionID: "shared-snap-s1"})
	if snapErr == nil {
		t.Fatal("Snapshot on shared-tenancy worker should return an error, got nil")
	}
	if !strings.Contains(snapErr.Error(), "shared-tenancy") {
		t.Errorf("snapshot error should mention 'shared-tenancy', got: %q", snapErr.Error())
	}

	// Persist must NOT have been called (the ban fires before the snapshot pipeline).
	if reg.Count("Persist") != 0 {
		t.Errorf("Persist was called despite shared-tenancy ban: called %d times", reg.Count("Persist"))
	}
}
