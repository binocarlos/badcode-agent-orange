package agentkit

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/bayes-price/agentkit/agentdb"
	"github.com/bayes-price/agentkit/agentkittest"
	"github.com/bayes-price/agentkit/artifacts"
	"github.com/bayes-price/agentkit/events"
	"github.com/bayes-price/agentkit/execenv"
	"github.com/bayes-price/agentkit/imageregistry"
)

// TestCreateSession_RegistryEnsurePresentFails verifies that when the image
// registry fails to ensure the image is present, CreateSession surfaces the
// error rather than silently proceeding.
func TestCreateSession_RegistryEnsurePresentFails(t *testing.T) {
	ctx := context.Background()
	r, _, reg, store, _, _ := newTestRunner(t)
	store.Seed(&agentdb.Session{ID: "s-reg-fail", Customer: "acme", Job: "j1"})

	// Inject an error into the registry — EnsurePresent will fail.
	reg.Err = fmt.Errorf("registry: image pull failed")

	_, err := r.CreateSession(ctx, CreateSessionRequest{
		SessionID: "s-reg-fail",
		Customer:  "acme",
		Job:       "j1",
	})
	if err == nil {
		t.Fatal("expected error when registry.EnsurePresent fails, got nil")
	}
	if !strings.Contains(err.Error(), "ensure image present") && !strings.Contains(err.Error(), "image pull") {
		t.Errorf("error should mention image/registry, got: %q", err.Error())
	}
}

// TestCreateSession_ProvisionFails verifies that when the execution environment
// Provision call fails, CreateSession returns an error.
func TestCreateSession_ProvisionFails(t *testing.T) {
	ctx := context.Background()
	r, env, _, store, _, _ := newTestRunner(t)
	store.Seed(&agentdb.Session{ID: "s-prov-fail", Customer: "acme", Job: "j1"})

	// Inject an error into the env — Provision will fail.
	env.Err = fmt.Errorf("docker: out of resources")

	_, err := r.CreateSession(ctx, CreateSessionRequest{
		SessionID: "s-prov-fail",
		Customer:  "acme",
		Job:       "j1",
	})
	if err == nil {
		t.Fatal("expected error when Provision fails, got nil")
	}
	if !strings.Contains(err.Error(), "provision") && !strings.Contains(err.Error(), "resources") {
		t.Errorf("error should mention provision/resources, got: %q", err.Error())
	}
}

// TestSendMessage_NotProvisioned verifies that SendMessage on a session that
// has never been provisioned (no running instance, no snapshot) returns a clear
// error rather than panicking or silently failing.
func TestSendMessage_NotProvisioned(t *testing.T) {
	ctx := context.Background()
	r, _, _, store, _, _ := newTestRunner(t)
	store.Seed(&agentdb.Session{ID: "s-not-provisioned", Customer: "acme", Job: "j1"})

	// Do NOT call CreateSession — the session exists in the store but has no
	// running instance and no snapshot handle.
	var buf bytes.Buffer
	err := r.SendMessage(ctx, SessionRef{SessionID: "s-not-provisioned"},
		SendMessageRequest{Content: "hello", Customer: "acme", Job: "j1"},
		&buf,
	)
	if err == nil {
		t.Fatal("expected error for SendMessage on unprovisioned session, got nil")
	}
	// The error must describe the unrecoverable state, not be a generic nil-ptr.
	errStr := err.Error()
	if !strings.Contains(errStr, "re-created") && !strings.Contains(errStr, "no snapshot") &&
		!strings.Contains(errStr, "unrecoverable") && !strings.Contains(errStr, "place session") &&
		!strings.Contains(errStr, "no running") {
		t.Errorf("error does not describe the problem clearly: %q", errStr)
	}
}

// TestStop_NonExistentSession verifies that Stop on a session that is not
// tracked returns nil (idempotent — stopping something already stopped is fine).
func TestStop_NonExistentSession(t *testing.T) {
	ctx := context.Background()
	r, _, _, _, _, _ := newTestRunner(t)

	// Stop a session that was never created — must not error.
	err := r.Stop(ctx, SessionRef{SessionID: "s-nonexistent"})
	if err != nil {
		t.Fatalf("Stop on non-existent session should return nil, got: %v", err)
	}
}

// TestDestroy_EnvDestroyFails verifies that when the execution environment's
// Destroy call fails, the runner surfaces that error.
func TestDestroy_EnvDestroyFails(t *testing.T) {
	ctx := context.Background()
	r, env, _, store, _, _ := newTestRunner(t)
	store.Seed(&agentdb.Session{ID: "s-destroy-fail", Customer: "acme", Job: "j1"})

	// Provision the session so it is tracked.
	if _, err := r.CreateSession(ctx, CreateSessionRequest{
		SessionID: "s-destroy-fail",
		Customer:  "acme",
		Job:       "j1",
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Inject an error into the environment — Destroy will fail.
	env.Err = fmt.Errorf("docker: container removal failed")

	err := r.Destroy(ctx, SessionRef{SessionID: "s-destroy-fail"})
	if err == nil {
		t.Fatal("expected error when env.Destroy fails, got nil")
	}
	if !strings.Contains(err.Error(), "removal") && !strings.Contains(err.Error(), "container") {
		t.Errorf("error should mention container/removal, got: %q", err.Error())
	}
}

// TestCreateSession_ErrorAfterProvision_NoOrphan verifies that if the session
// row is not seeded in the store but CreateSession is called, it still fails
// gracefully at the provision stage (not at a GetSession call that doesn't happen
// in the happy path).
func TestCreateSession_NoStoreSeedRequired(t *testing.T) {
	ctx := context.Background()
	r, _, _, _, _, _ := newTestRunner(t)

	// CreateSession does NOT call Store.GetSession — that is only called in the
	// restore flow. So CreateSession should succeed even without seeding the store.
	handle, err := r.CreateSession(ctx, CreateSessionRequest{
		SessionID: "s-no-seed",
		Customer:  "acme",
		Job:       "j1",
	})
	if err != nil {
		t.Fatalf("CreateSession without store seed should succeed (store not read during create): %v", err)
	}
	if handle == nil || handle.SessionID != "s-no-seed" {
		t.Fatalf("expected valid handle with SessionID=s-no-seed, got %+v", handle)
	}
}

// TestSendMessage_StoreGetSessionFailsOnRestore verifies that when
// Store.GetSession fails during the restore path (ensureRunning after instance
// is destroyed), the error is propagated to the caller.
func TestSendMessage_StoreGetSessionFailsOnRestore(t *testing.T) {
	ctx := context.Background()
	// Use an error-injecting store.
	store := &errStore{inner: agentkittest.NewMemStore()}
	env := execenv.NewMock()
	reg := imageregistry.NewMock()
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

	// Seed so CreateSession succeeds.
	store.inner.Seed(&agentdb.Session{ID: "s-store-fail", Customer: "acme", Job: "j1"})
	if _, err := r.CreateSession(ctx, CreateSessionRequest{
		SessionID: "s-store-fail", Customer: "acme", Job: "j1",
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Destroy the instance so the next operation must restore from a snapshot.
	// But there is no snapshot — so ensureRunning will call restoreToWorker →
	// Store.GetSnapshotHandle → ok=false → error without ever calling GetSession.
	// Let's use Snapshot first to persist a handle, then destroy, then inject
	// the store error so GetSession fails during restore.
	if _, err := r.Snapshot(ctx, SessionRef{SessionID: "s-store-fail"}); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if err := r.Destroy(ctx, SessionRef{SessionID: "s-store-fail"}); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	// Now inject the error — GetSession will fail during provisionForSession.
	store.getSessionErr = fmt.Errorf("db: connection lost")

	var buf bytes.Buffer
	sendErr := r.SendMessage(ctx, SessionRef{SessionID: "s-store-fail"},
		SendMessageRequest{Content: "hi", Customer: "acme", Job: "j1"},
		&buf,
	)
	if sendErr == nil {
		t.Fatal("expected error when Store.GetSession fails during restore, got nil")
	}
	if !strings.Contains(sendErr.Error(), "db") && !strings.Contains(sendErr.Error(), "connection") {
		t.Errorf("error should propagate db error, got: %q", sendErr.Error())
	}
}

// errStore wraps MemStore and injects errors into GetSession.
type errStore struct {
	inner         *agentkittest.MemStore
	getSessionErr error
}

func (s *errStore) GetSession(ctx context.Context, id string) (*agentdb.Session, error) {
	if s.getSessionErr != nil {
		return nil, s.getSessionErr
	}
	return s.inner.GetSession(ctx, id)
}

func (s *errStore) UpdateSession(ctx context.Context, sess *agentdb.Session) (*agentdb.Session, error) {
	return s.inner.UpdateSession(ctx, sess)
}

func (s *errStore) SetSnapshotHandle(ctx context.Context, sessionID string, h imageregistry.Handle) error {
	return s.inner.SetSnapshotHandle(ctx, sessionID, h)
}

func (s *errStore) GetSnapshotHandle(ctx context.Context, sessionID string) (imageregistry.Handle, bool, error) {
	return s.inner.GetSnapshotHandle(ctx, sessionID)
}

func (s *errStore) PersistQueryEventsFlat(ctx context.Context, sessionID, queryID string, evs []events.Envelope, searchText string) error {
	return s.inner.PersistQueryEventsFlat(ctx, sessionID, queryID, evs, searchText)
}

func (s *errStore) ListQueryEventsFlat(ctx context.Context, sessionID string) ([]events.Envelope, error) {
	return s.inner.ListQueryEventsFlat(ctx, sessionID)
}

func (s *errStore) GetWorkerBinding(ctx context.Context, sessionID string) (string, bool, error) {
	return s.inner.GetWorkerBinding(ctx, sessionID)
}

func (s *errStore) SetWorkerBinding(ctx context.Context, sessionID, workerID string) error {
	return s.inner.SetWorkerBinding(ctx, sessionID, workerID)
}

func (s *errStore) ClearWorkerBinding(ctx context.Context, sessionID string) error {
	return s.inner.ClearWorkerBinding(ctx, sessionID)
}
