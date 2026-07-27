//go:build integration

// Package systemtest drives the Runner as a LIBRARY against real containers.
//
// What it is for, and what nothing else covers:
//
//   - It is the only test that runs the real sandbox image (built from
//     ../../sandbox in TestMain) under the real agentkit.Runner, with a mock
//     Anthropic endpoint in place of the model. Everything above the container
//     is production code — Runner, fleet, image registry, event sink, artifact
//     store; only the model and the ExecutionEnvironment are substituted.
//   - The compose-stack e2e (e2e/features/) covers the same ground through
//     agentd's HTTP API and the DinD adapter. It is the better test of the
//     product, and where behaviour overlaps this suite defers to it. What it
//     cannot reach is the embed-as-a-library seam described in
//     docs/14-host-adapters.md: a host that constructs a Runner with its own
//     ExecutionEnvironment. That seam is this package's subject.
//
// The model is stubbed by go/mockmodel — the shared scriptable Anthropic mock,
// which was itself promoted OUT of this package. The copy left behind here was
// deleted in the same change as this comment; a script written for a systemtest
// is now the same format agentd's proxy takes in AGENTKIT_MOCK_MODEL_SCRIPT.
//
// Everything here is behind `//go:build integration` because it needs Docker
// and builds an image. CI gates that the package COMPILES under the tag
// (.github/workflows/ci.yml); CI does not run it. Run it by hand:
//
//	cd go && go test -tags integration ./systemtest/ -v -timeout 900s
//
// TestSystemSnapshotRestoreTrust additionally needs the local registry from
// docker-compose.test.yml and skips without it — see its own comment.
package systemtest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	agentkit "github.com/binocarlos/badcode-agent-orange"
	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/artifacts"
	"github.com/binocarlos/badcode-agent-orange/execenv"
	"github.com/binocarlos/badcode-agent-orange/mockmodel"
	"github.com/binocarlos/badcode-agent-orange/modelproxy"
)

func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	fmt.Println("=== Building sandbox image ===")
	buildCmd := exec.CommandContext(ctx, "docker", "build",
		"-t", sandboxImage,
		"../../sandbox")
	buildCmd.Dir = "."
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: sandbox image build failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("=== Sandbox image built ===")

	code := m.Run()

	env, err := NewTestEnv("")
	if err == nil {
		env.DestroyAll(context.Background())
	}

	os.Exit(code)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func TestSystemCreateAndSendMessage(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping system test in short mode")
	}

	proxy := mockmodel.New(t)
	defer proxy.Close()

	rig, err := newSystemRig(proxy.URL)
	if err != nil {
		t.Fatalf("newSystemRig: %v", err)
	}
	defer rig.cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	sessionID := "sys-create-send-1"
	rig.store.Seed(&agentdb.Session{
		ID:       sessionID,
		Customer: "test",
		Job:      "test-job",
	})

	h, err := rig.runner.CreateSession(ctx, agentkit.CreateSessionRequest{
		SessionID: sessionID,
		Customer:  "test",
		Job:       "test-job",
		Harness:   agentkit.HarnessClaudeAgentSDK,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if h.SessionID != sessionID {
		t.Errorf("SessionID = %q, want %q", h.SessionID, sessionID)
	}
	if h.Address == "" {
		t.Error("Address is empty — container not reachable")
	}
	t.Logf("Session created: address=%s", h.Address)

	var buf bytes.Buffer
	err = rig.runner.SendMessage(ctx, agentkit.SessionRef{SessionID: sessionID},
		agentkit.SendMessageRequest{
			Content:  "What is 2+2?",
			Customer: "test",
			Job:      "test-job",
		}, &buf)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	output := buf.String()
	t.Logf("SSE output length: %d bytes", len(output))
	t.Logf("SSE output (first 500 chars): %s", truncate(output, 500))

	if !strings.Contains(output, "content_delta") && !strings.Contains(output, "query_complete") {
		t.Errorf("SSE output missing expected event types. Got: %s", truncate(output, 1000))
	}

	if err := rig.runner.Destroy(ctx, agentkit.SessionRef{SessionID: sessionID}); err != nil {
		t.Errorf("Destroy: %v", err)
	}
}

func TestSystemTwoSessionIsolation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping system test in short mode")
	}

	proxy := mockmodel.New(t)
	defer proxy.Close()

	rig, err := newSystemRig(proxy.URL)
	if err != nil {
		t.Fatalf("newSystemRig: %v", err)
	}
	defer rig.cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	for _, sid := range []string{"iso-s1", "iso-s2"} {
		rig.store.Seed(&agentdb.Session{ID: sid, Customer: "test", Job: "test-job"})
		_, err := rig.runner.CreateSession(ctx, agentkit.CreateSessionRequest{
			SessionID: sid,
			Customer:  "test",
			Job:       "test-job",
			Harness:   agentkit.HarnessClaudeAgentSDK,
		})
		if err != nil {
			t.Fatalf("CreateSession(%s): %v", sid, err)
		}
	}

	type result struct {
		sid    string
		output string
		err    error
	}
	ch := make(chan result, 2)

	for _, sid := range []string{"iso-s1", "iso-s2"} {
		sid := sid
		go func() {
			var buf bytes.Buffer
			sendErr := rig.runner.SendMessage(ctx,
				agentkit.SessionRef{SessionID: sid},
				agentkit.SendMessageRequest{
					Content:  fmt.Sprintf("Hello from %s", sid),
					Customer: "test",
					Job:      "test-job",
				}, &buf)
			ch <- result{sid: sid, output: buf.String(), err: sendErr}
		}()
	}

	results := make(map[string]result, 2)
	for range 2 {
		r := <-ch
		results[r.sid] = r
	}

	for _, sid := range []string{"iso-s1", "iso-s2"} {
		r := results[sid]
		if r.err != nil {
			t.Errorf("SendMessage(%s): %v", sid, r.err)
			continue
		}
		if len(r.output) == 0 {
			t.Errorf("SendMessage(%s): empty SSE output", sid)
		}
		t.Logf("%s output: %d bytes", sid, len(r.output))
	}

	for _, sid := range []string{"iso-s1", "iso-s2"} {
		_ = rig.runner.Destroy(ctx, agentkit.SessionRef{SessionID: sid})
	}
}

func TestSystemDestroyCleanup(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping system test in short mode")
	}

	proxy := mockmodel.New(t)
	defer proxy.Close()

	rig, err := newSystemRig(proxy.URL)
	if err != nil {
		t.Fatalf("newSystemRig: %v", err)
	}
	defer rig.cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	sessionID := "sys-destroy-1"
	rig.store.Seed(&agentdb.Session{ID: sessionID, Customer: "test", Job: "test-job"})

	h, err := rig.runner.CreateSession(ctx, agentkit.CreateSessionRequest{
		SessionID: sessionID,
		Customer:  "test",
		Job:       "test-job",
		Harness:   agentkit.HarnessClaudeAgentSDK,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	resp, err := http.Get(h.Address + "/health")
	if err != nil {
		t.Fatalf("health check before destroy: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("health check status = %d, want 200", resp.StatusCode)
	}

	if err := rig.runner.Destroy(ctx, agentkit.SessionRef{SessionID: sessionID}); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	time.Sleep(1 * time.Second)
	_, err = http.Get(h.Address + "/health")
	if err == nil {
		t.Error("container still reachable after Destroy — expected connection refused")
	}
}

// TestSystemArchiveRestore is the port of the deleted TestSystemSuspendResume.
// The old test asserted "a session can be put away and woken, and still take a
// turn" through Suspend (docker stop) → Status==suspended → Resume (docker
// start). Suspend and StateSuspended no longer exist: the lifecycle is
// Running-or-Archived, and Resume is the only wake path — it finds a live
// container or restores from the snapshot handle.
//
// The intent survives the vocabulary change, so it is expressed in the current
// one: snapshot → destroy → Resume → turn. It also asserts the thing the old
// shape could not, and which is now the whole substance of a wake: the restored
// container carries the archived FILESYSTEM, so it is the session coming back
// rather than a fresh one wearing its ID.
//
// Unlike TestSystemSnapshotRestoreTrust this runs on the mock registry, so it
// never skips — it is the archive/restore assertion that is always exercised.
func TestSystemArchiveRestore(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping system test in short mode")
	}

	proxy := mockmodel.New(t)
	defer proxy.Close()

	rig, err := newSystemRig(proxy.URL)
	if err != nil {
		t.Fatalf("newSystemRig: %v", err)
	}
	defer rig.cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	sessionID := "sys-archive-restore-1"
	rig.store.Seed(&agentdb.Session{ID: sessionID, Customer: "test", Job: "test-job"})

	_, err = rig.runner.CreateSession(ctx, agentkit.CreateSessionRequest{
		SessionID: sessionID,
		Customer:  "test",
		Job:       "test-job",
		Harness:   agentkit.HarnessClaudeAgentSDK,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	const marker = "archive-marker-4c1e"
	inst := mustInstance(t, rig, sessionID)
	if _, err := rig.env.Exec(ctx, inst,
		[]string{"sh", "-c", "printf %s '" + marker + "' > /workspace/archive-marker.txt"},
		execenv.ExecOptions{Timeout: 20 * time.Second}); err != nil {
		t.Fatalf("write workspace marker: %v", err)
	}

	// Archive: snapshot, then drop the container. This is what the idle archive
	// loop does; Destroy alone would leave nothing to restore from.
	if _, err := rig.runner.Snapshot(ctx, agentkit.SessionRef{SessionID: sessionID}); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if err := rig.runner.Destroy(ctx, agentkit.SessionRef{SessionID: sessionID}); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	st, err := rig.runner.Status(ctx, agentkit.SessionRef{SessionID: sessionID})
	if err != nil {
		t.Fatalf("Status after archive: %v", err)
	}
	if st.RuntimeState != string(execenv.StateDestroyed) {
		t.Errorf("state after archive = %q, want %q", st.RuntimeState, execenv.StateDestroyed)
	}
	if !st.HasSnapshot {
		t.Error("HasSnapshot = false after archive — nothing to restore from")
	}

	// Wake: Resume restores from the snapshot handle.
	h, err := rig.runner.Resume(ctx, agentkit.SessionRef{SessionID: sessionID})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if h.State != string(execenv.StateRunning) {
		t.Errorf("state after resume = %q, want %q", h.State, execenv.StateRunning)
	}

	inst2 := mustInstance(t, rig, sessionID)
	if inst2 == inst {
		t.Error("restore reused the archived instance ID — the container was never dropped")
	}
	res, err := rig.env.Exec(ctx, inst2, []string{"cat", "/workspace/archive-marker.txt"},
		execenv.ExecOptions{Timeout: 20 * time.Second})
	if err != nil {
		t.Fatalf("read workspace marker after restore: %v", err)
	}
	if got := strings.TrimSpace(string(res.Stdout)); !strings.Contains(got, marker) {
		t.Errorf("restored container is not the archived one: marker = %q, want substring %q", got, marker)
	}

	var buf bytes.Buffer
	err = rig.runner.SendMessage(ctx, agentkit.SessionRef{SessionID: sessionID},
		agentkit.SendMessageRequest{Content: "Post-restore test", Customer: "test", Job: "test-job"},
		&buf)
	if err != nil {
		t.Fatalf("SendMessage after restore: %v", err)
	}
	if buf.Len() == 0 {
		t.Error("empty SSE output after restore")
	}

	_ = rig.runner.Destroy(ctx, agentkit.SessionRef{SessionID: sessionID})
}

func TestSystemSnapshotRestore(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping system test in short mode")
	}

	proxy := mockmodel.New(t)
	defer proxy.Close()

	rig, err := newSystemRig(proxy.URL)
	if err != nil {
		t.Fatalf("newSystemRig: %v", err)
	}
	defer rig.cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	sessionID := "sys-snapshot-1"
	rig.store.Seed(&agentdb.Session{ID: sessionID, Customer: "test", Job: "test-job"})

	_, err = rig.runner.CreateSession(ctx, agentkit.CreateSessionRequest{
		SessionID: sessionID,
		Customer:  "test",
		Job:       "test-job",
		Harness:   agentkit.HarnessClaudeAgentSDK,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	handle, err := rig.runner.Snapshot(ctx, agentkit.SessionRef{SessionID: sessionID})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if handle.Kind == "" && handle.Ref == "" {
		t.Error("snapshot returned empty handle")
	}
	t.Logf("Snapshot handle: kind=%q ref=%q", handle.Kind, handle.Ref)

	storedHandle, ok, err := rig.store.GetSnapshotHandle(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetSnapshotHandle: %v", err)
	}
	if !ok {
		t.Fatal("no snapshot handle in store after Snapshot()")
	}
	if storedHandle.Ref != handle.Ref {
		t.Errorf("stored handle ref = %q, want %q", storedHandle.Ref, handle.Ref)
	}

	if err := rig.runner.Destroy(ctx, agentkit.SessionRef{SessionID: sessionID}); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	_, ok, err = rig.store.GetSnapshotHandle(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetSnapshotHandle after destroy: %v", err)
	}
	if !ok {
		t.Error("snapshot handle lost after destroy")
	}
}

// TestSystemSnapshotRestoreTrust proves the four trust invariants end-to-end against
// real containers + a REAL ociregistry/registry:2: after force-archiving a session
// (snapshot → push → destroy) and restoring it, (1) a workspace file that was never
// extracted as an artifact survives, (2) conversation history is preserved, (3) an
// extracted artifact is not lost, and (4) the session is transparently usable again.
//
// Needs a registry on localhost:5001; it SKIPS without one. Session containers
// and the push/pull both run on the DEFAULT Docker daemon (TestEnv and the
// ociregistry adapter each use DOCKER_HOST/FromEnv), so a bare registry is all
// that is required — docker-compose.test.yml also provides one, along with a
// DinD this test does not use. Verified 2026-07-26 with:
//
//	docker run -d --name agentorange-test-registry -p 5001:5000 registry:2
//	cd go && OCIREGISTRY_URL=localhost:5001/agentkit go test -tags integration ./systemtest/ \
//	  -run TestSystemSnapshotRestoreTrust -v -timeout 600s
//
// No insecure-registry daemon config is needed: Docker treats localhost:* as
// insecure by default.
func TestSystemSnapshotRestoreTrust(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping system test in short mode")
	}
	if !registryReachable(ociRegistryURL()) {
		t.Skipf("registry %q not reachable — start agent-library/docker-compose.test.yml", ociRegistryURL())
	}

	proxy := mockmodel.New(t)
	defer proxy.Close()

	rig, err := newSystemRigOCI(proxy.URL)
	if err != nil {
		t.Fatalf("newSystemRigOCI: %v", err)
	}
	defer rig.cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	sessionID := "sys-trust-1"
	rig.store.Seed(&agentdb.Session{ID: sessionID, Customer: "test", Job: "test-job"})

	if _, err := rig.runner.CreateSession(ctx, agentkit.CreateSessionRequest{
		SessionID: sessionID, Customer: "test", Job: "test-job", Harness: agentkit.HarnessClaudeAgentSDK,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// (1 setup) Write a workspace file that is NOT extracted as an artifact.
	const marker = "trust-marker-7f3a"
	inst := mustInstance(t, rig, sessionID)
	if _, err := rig.env.Exec(ctx, inst,
		[]string{"sh", "-c", "mkdir -p /workspace/data && printf %s '" + marker + "' > /workspace/data/trust.txt"},
		execenv.ExecOptions{Timeout: 20 * time.Second}); err != nil {
		t.Fatalf("write workspace file: %v", err)
	}

	// (3 setup) Extract an artifact before archive (content → StatusExtracted).
	if _, err := rig.arts.Save(ctx,
		&artifacts.Artifact{SessionID: sessionID, FilePath: "report.txt", Status: artifacts.StatusExtracted},
		strings.NewReader("artifact-bytes")); err != nil {
		t.Fatalf("seed artifact: %v", err)
	}

	// (2 setup) Create conversation history.
	var sb bytes.Buffer
	if err := rig.runner.SendMessage(ctx, agentkit.SessionRef{SessionID: sessionID},
		agentkit.SendMessageRequest{Content: "remember the codeword", Customer: "test", Job: "test-job"}, &sb); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	eventsBefore := countQueryEvents(t, rig, sessionID)
	if eventsBefore == 0 {
		t.Fatal("no query events recorded before archive")
	}

	// --- force archive: snapshot → persist (push diff layer) → destroy ---
	handle, err := rig.runner.Snapshot(ctx, agentkit.SessionRef{SessionID: sessionID})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if handle.Kind != "registry" {
		t.Fatalf("snapshot handle kind = %q, want \"registry\" (real ociregistry)", handle.Kind)
	}
	if err := rig.runner.Destroy(ctx, agentkit.SessionRef{SessionID: sessionID}); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	// --- restore: Resume → Materialize (pull) → re-provision from the snapshot image ---
	if _, err := rig.runner.Resume(ctx, agentkit.SessionRef{SessionID: sessionID}); err != nil {
		t.Fatalf("Resume (restore): %v", err)
	}
	inst2 := mustInstance(t, rig, sessionID)

	// (1) Filesystem survived — the raw, un-extracted file is byte-identical.
	res, err := rig.env.Exec(ctx, inst2, []string{"cat", "/workspace/data/trust.txt"},
		execenv.ExecOptions{Timeout: 20 * time.Second})
	if err != nil {
		t.Fatalf("read workspace file after restore: %v", err)
	}
	if got := strings.TrimSpace(string(res.Stdout)); !strings.Contains(got, marker) {
		t.Fatalf("workspace file did not survive restore: got %q want substring %q", got, marker)
	}

	// (2) Conversation continuity — query events preserved across the cycle.
	if got := countQueryEvents(t, rig, sessionID); got < eventsBefore {
		t.Fatalf("query events regressed after restore: before=%d after=%d", eventsBefore, got)
	}

	// (3) Artifacts intact — the extracted artifact is not lost.
	arts, err := rig.arts.List(ctx, sessionID)
	if err != nil {
		t.Fatalf("List artifacts: %v", err)
	}
	if len(arts) == 0 {
		t.Fatal("artifact missing after restore")
	}
	for _, a := range arts {
		if a.Status == artifacts.StatusLost {
			t.Fatalf("artifact %q marked lost after restore", a.FilePath)
		}
	}

	// (4) Transparent restore — the session takes another turn with no re-create.
	var sb2 bytes.Buffer
	if err := rig.runner.SendMessage(ctx, agentkit.SessionRef{SessionID: sessionID},
		agentkit.SendMessageRequest{Content: "post-restore", Customer: "test", Job: "test-job"}, &sb2); err != nil {
		t.Fatalf("SendMessage after restore: %v", err)
	}
}

func TestSystemSandboxCrash(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping system test in short mode")
	}

	proxy := mockmodel.New(t)
	defer proxy.Close()

	rig, err := newSystemRig(proxy.URL)
	if err != nil {
		t.Fatalf("newSystemRig: %v", err)
	}
	defer rig.cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	sessionID := "sys-crash-1"
	rig.store.Seed(&agentdb.Session{ID: sessionID, Customer: "test", Job: "test-job"})

	_, err = rig.runner.CreateSession(ctx, agentkit.CreateSessionRequest{
		SessionID: sessionID,
		Customer:  "test",
		Job:       "test-job",
		Harness:   agentkit.HarnessClaudeAgentSDK,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Find the container ID for this session.
	rig.env.mu.Lock()
	var containerID string
	for _, s := range rig.env.containers {
		if s.inst.SessionID == sessionID {
			containerID = s.containerID
			break
		}
	}
	rig.env.mu.Unlock()

	if containerID == "" {
		t.Fatal("could not find container ID for session")
	}

	t.Log("Killing container to simulate crash...")
	killCmd := exec.CommandContext(ctx, "docker", "kill", containerID)
	if err := killCmd.Run(); err != nil {
		t.Fatalf("docker kill: %v", err)
	}

	time.Sleep(2 * time.Second)

	sendCtx, sendCancel := context.WithTimeout(ctx, 30*time.Second)
	defer sendCancel()

	var buf bytes.Buffer
	err = rig.runner.SendMessage(sendCtx, agentkit.SessionRef{SessionID: sessionID},
		agentkit.SendMessageRequest{Content: "after crash", Customer: "test", Job: "test-job"},
		&buf)
	if err == nil {
		t.Error("SendMessage after crash succeeded — expected an error")
	} else {
		t.Logf("SendMessage after crash correctly failed: %v", err)
	}
}

func TestSystemHarnessSelection(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping system test in short mode")
	}

	proxy := mockmodel.New(t)
	defer proxy.Close()

	rig, err := newSystemRig(proxy.URL)
	if err != nil {
		t.Fatalf("newSystemRig: %v", err)
	}
	defer rig.cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	sessionID := "sys-harness-bad"
	rig.store.Seed(&agentdb.Session{ID: sessionID, Customer: "test", Job: "test-job"})

	_, err = rig.runner.CreateSession(ctx, agentkit.CreateSessionRequest{
		SessionID: sessionID,
		Customer:  "test",
		Job:       "test-job",
		Harness:   agentkit.Harness("nonexistent-harness"),
	})
	if err == nil {
		t.Fatal("CreateSession with unknown harness should fail")
	}

	var harnessErr *agentkit.ErrHarnessUnavailable
	if !errors.As(err, &harnessErr) {
		t.Fatalf("expected ErrHarnessUnavailable, got: %T: %v", err, err)
	}
	if harnessErr.StatusCode != 400 {
		t.Errorf("status code = %d, want 400", harnessErr.StatusCode)
	}
	t.Logf("Correctly rejected unknown harness: %s", harnessErr.Body)

	sessionID2 := "sys-harness-good"
	rig.store.Seed(&agentdb.Session{ID: sessionID2, Customer: "test", Job: "test-job"})

	h, err := rig.runner.CreateSession(ctx, agentkit.CreateSessionRequest{
		SessionID: sessionID2,
		Customer:  "test",
		Job:       "test-job",
		Harness:   agentkit.HarnessClaudeAgentSDK,
	})
	if err != nil {
		t.Fatalf("CreateSession with valid harness: %v", err)
	}
	t.Logf("Session with valid harness created at %s", h.Address)

	_ = rig.runner.Destroy(ctx, agentkit.SessionRef{SessionID: sessionID2})
}

func TestSystemSessionHeaderIsolation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping system test in short mode")
	}

	var headerMu sync.Mutex
	receivedHeaders := make(map[string][]string)

	// This is the one test that needs its own handler — it asserts on what the
	// sandbox SENT, which mockmodel does not expose. The SSE body still comes
	// from the shared renderer (modelproxy.TurnSSE) rather than a local copy:
	// this package used to carry a third copy of that builder, which is exactly
	// how it drifted out of date unnoticed.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/messages" {
			sid := r.Header.Get("x-session-id")
			headerMu.Lock()
			receivedHeaders[sid] = append(receivedHeaders[sid], sid)
			headerMu.Unlock()

			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, modelproxy.TurnSSE(modelproxy.Turn{
				Blocks: []modelproxy.Block{{Type: "text", Text: "Hello from the mock proxy"}},
			}, 0))
			return
		}
		if r.URL.Path == "/health" {
			w.WriteHeader(200)
			return
		}
		http.NotFound(w, r)
	})
	proxy := httptest.NewServer(handler)
	defer proxy.Close()

	rig, err := newSystemRig(proxy.URL)
	if err != nil {
		t.Fatalf("newSystemRig: %v", err)
	}
	defer rig.cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	sids := []string{"hdr-s1", "hdr-s2"}
	for _, sid := range sids {
		rig.store.Seed(&agentdb.Session{ID: sid, Customer: "test", Job: "test-job"})
		_, err := rig.runner.CreateSession(ctx, agentkit.CreateSessionRequest{
			SessionID: sid, Customer: "test", Job: "test-job",
			Harness: agentkit.HarnessClaudeAgentSDK,
		})
		if err != nil {
			t.Fatalf("CreateSession(%s): %v", sid, err)
		}
	}

	for _, sid := range sids {
		var buf bytes.Buffer
		err := rig.runner.SendMessage(ctx, agentkit.SessionRef{SessionID: sid},
			agentkit.SendMessageRequest{Content: "header test", Customer: "test", Job: "test-job"},
			&buf)
		if err != nil {
			t.Errorf("SendMessage(%s): %v", sid, err)
		}
	}

	headerMu.Lock()
	defer headerMu.Unlock()

	for _, sid := range sids {
		headers, ok := receivedHeaders[sid]
		if !ok || len(headers) == 0 {
			t.Errorf("no x-session-id=%q received at mock proxy", sid)
			continue
		}
		t.Logf("x-session-id=%q received %d time(s)", sid, len(headers))
	}

	for k := range receivedHeaders {
		found := false
		for _, sid := range sids {
			if k == sid {
				found = true
				break
			}
		}
		if !found && k != "" {
			t.Logf("note: unexpected x-session-id=%q (may be from sandbox init)", k)
		}
	}

	for _, sid := range sids {
		_ = rig.runner.Destroy(ctx, agentkit.SessionRef{SessionID: sid})
	}
}

func TestSystemMultiTurnToolCall(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping system test in short mode")
	}

	script := &modelproxy.Script{
		Turns: []modelproxy.Turn{
			{Blocks: []modelproxy.Block{
				{Type: "text", Text: "Let me check that for you."},
				{Type: "tool_use", Name: "Bash", Input: map[string]any{"command": "echo hello"}},
			}},
			{Blocks: []modelproxy.Block{
				{Type: "text", Text: "The command returned hello."},
			}},
		},
	}

	proxy := mockmodel.NewScripted(t, script)
	defer proxy.Close()

	rig, err := newSystemRig(proxy.URL)
	if err != nil {
		t.Fatalf("newSystemRig: %v", err)
	}
	defer rig.cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	sessionID := "sys-multiturn-1"
	rig.store.Seed(&agentdb.Session{ID: sessionID, Customer: "test", Job: "test-job"})

	_, err = rig.runner.CreateSession(ctx, agentkit.CreateSessionRequest{
		SessionID: sessionID,
		Customer:  "test",
		Job:       "test-job",
		Harness:   agentkit.HarnessClaudeAgentSDK,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	var buf bytes.Buffer
	err = rig.runner.SendMessage(ctx, agentkit.SessionRef{SessionID: sessionID},
		agentkit.SendMessageRequest{Content: "Run echo hello", Customer: "test", Job: "test-job"},
		&buf)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	output := buf.String()
	t.Logf("SSE output length: %d bytes", len(output))

	if !strings.Contains(output, "tool_use") {
		t.Error("SSE output missing tool_use event from first turn")
	}
	if !strings.Contains(output, "Let me check") {
		t.Error("SSE output missing text from first turn")
	}

	_ = rig.runner.Destroy(ctx, agentkit.SessionRef{SessionID: sessionID})
}
