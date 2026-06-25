package httpapi

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/binocarlos/badcode-agent-orange"
	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/artifacts"
	"github.com/binocarlos/badcode-agent-orange/events"
	"github.com/binocarlos/badcode-agent-orange/imageregistry"
)

// awaitCreate blocks until the backgrounded CreateSession goroutine signals on
// done (or fails the test on timeout). CreateSession now provisions asynchronously,
// so tests that assert on the captured request must wait for the goroutine to run.
func awaitCreate(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for background CreateSession")
	}
}

type stubRunner struct {
	createFn func(context.Context, agentkit.CreateSessionRequest) (*agentkit.SessionHandle, error)
	sendFn   func(context.Context, agentkit.SessionRef, agentkit.SendMessageRequest, agentkit.Writer) error
	streamFn func(context.Context, agentkit.SessionRef, agentkit.StreamOptions, agentkit.Writer) error
	statusFn func(context.Context, agentkit.SessionRef) (*agentkit.SessionStatus, error)
	stopFn   func(context.Context, agentkit.SessionRef) error
	destroyFn func(context.Context, agentkit.SessionRef) error
	resumeFn  func(context.Context, agentkit.SessionRef) (*agentkit.SessionHandle, error)
	snapshotFn func(context.Context, agentkit.SessionRef) (imageregistry.Handle, error)
}

func (s stubRunner) CreateSession(ctx context.Context, r agentkit.CreateSessionRequest) (*agentkit.SessionHandle, error) {
	if s.createFn != nil {
		return s.createFn(ctx, r)
	}
	return &agentkit.SessionHandle{SessionID: r.SessionID, State: "running"}, nil
}
func (s stubRunner) SendMessage(ctx context.Context, ref agentkit.SessionRef, m agentkit.SendMessageRequest, w agentkit.Writer) error {
	if s.sendFn != nil {
		return s.sendFn(ctx, ref, m, w)
	}
	return nil
}
func (s stubRunner) Stream(ctx context.Context, ref agentkit.SessionRef, opts agentkit.StreamOptions, w agentkit.Writer) error {
	if s.streamFn != nil {
		return s.streamFn(ctx, ref, opts, w)
	}
	return nil
}
func (s stubRunner) Stop(ctx context.Context, ref agentkit.SessionRef) error {
	if s.stopFn != nil {
		return s.stopFn(ctx, ref)
	}
	return nil
}
func (s stubRunner) Suspend(context.Context, agentkit.SessionRef) error { return nil }
func (s stubRunner) Resume(ctx context.Context, ref agentkit.SessionRef) (*agentkit.SessionHandle, error) {
	if s.resumeFn != nil {
		return s.resumeFn(ctx, ref)
	}
	return &agentkit.SessionHandle{SessionID: ref.SessionID, State: "running"}, nil
}
func (s stubRunner) Destroy(ctx context.Context, ref agentkit.SessionRef) error {
	if s.destroyFn != nil {
		return s.destroyFn(ctx, ref)
	}
	return nil
}
func (s stubRunner) Snapshot(ctx context.Context, ref agentkit.SessionRef) (imageregistry.Handle, error) {
	if s.snapshotFn != nil {
		return s.snapshotFn(ctx, ref)
	}
	return imageregistry.Handle{}, nil
}
func (s stubRunner) WriteWorkspaceFile(_ context.Context, _ agentkit.SessionRef, _ string, _ []byte) error {
	return nil
}
func (s stubRunner) RunningSessions(_ context.Context) (map[string]bool, error) {
	return nil, nil
}
func (s stubRunner) Status(ctx context.Context, ref agentkit.SessionRef) (*agentkit.SessionStatus, error) {
	if s.statusFn != nil {
		return s.statusFn(ctx, ref)
	}
	return &agentkit.SessionStatus{SessionID: ref.SessionID, RuntimeState: "running"}, nil
}
func (s stubRunner) Start(context.Context) error { return nil }
func (s stubRunner) Close() error                { return nil }

type stubStore struct {
	evts          []events.Envelope
	getSessionFn  func(context.Context, string) (*agentdb.Session, error)
	listEventsFn  func(context.Context, string) ([]events.Envelope, error)
}

func (s stubStore) GetSession(ctx context.Context, id string) (*agentdb.Session, error) {
	if s.getSessionFn != nil {
		return s.getSessionFn(ctx, id)
	}
	return &agentdb.Session{ID: id}, nil
}
func (s stubStore) UpdateSession(_ context.Context, sess *agentdb.Session) (*agentdb.Session, error) {
	return sess, nil
}
func (s stubStore) SetSnapshotHandle(context.Context, string, imageregistry.Handle) error        { return nil }
func (s stubStore) GetSnapshotHandle(context.Context, string) (imageregistry.Handle, bool, error) { return imageregistry.Handle{}, false, nil }
func (s stubStore) PersistQueryEventsFlat(context.Context, string, string, []events.Envelope, string) error { return nil }
func (s stubStore) ListQueryEventsFlat(ctx context.Context, sessionID string) ([]events.Envelope, error) {
	if s.listEventsFn != nil {
		return s.listEventsFn(ctx, sessionID)
	}
	return s.evts, nil
}
func (s stubStore) GetWorkerBinding(context.Context, string) (string, bool, error)               { return "", false, nil }
func (s stubStore) SetWorkerBinding(context.Context, string, string) error                       { return nil }
func (s stubStore) ClearWorkerBinding(context.Context, string) error                             { return nil }

// stubArtifacts implements artifacts.ArtifactStore for tests.
type stubArtifacts struct {
	listFn func(context.Context, string) ([]*artifacts.Artifact, error)
	saveFn func(context.Context, *artifacts.Artifact, io.Reader) (*artifacts.Artifact, error)
}

func (s *stubArtifacts) List(ctx context.Context, sessionID string) ([]*artifacts.Artifact, error) {
	if s.listFn != nil {
		return s.listFn(ctx, sessionID)
	}
	return []*artifacts.Artifact{}, nil
}

func (s *stubArtifacts) Save(ctx context.Context, art *artifacts.Artifact, content io.Reader) (*artifacts.Artifact, error) {
	if s.saveFn != nil {
		return s.saveFn(ctx, art, content)
	}
	return art, nil
}

func (s *stubArtifacts) Load(context.Context, string) (*artifacts.Artifact, io.ReadCloser, error) {
	return nil, nil, nil
}

func (s *stubArtifacts) MarkLost(context.Context, string) error { return nil }

func (s *stubArtifacts) CaptureFolder(context.Context, string, string, io.Reader) (*artifacts.Artifact, error) {
	return nil, nil
}
