package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/binocarlos/badcode-agent-orange"
	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/events"
	"github.com/binocarlos/badcode-agent-orange/imageregistry"
)

// recordingStore is a RunnerStore that actually keeps rows, so a test can watch
// what survives a failed background create. The stubStore in fakes_test.go
// throws writes away, which is exactly the property under test here.
type recordingStore struct {
	mu   sync.Mutex
	rows map[string]*agentdb.Session
}

func newRecordingStore() *recordingStore {
	return &recordingStore{rows: map[string]*agentdb.Session{}}
}

func (s *recordingStore) GetSession(_ context.Context, id string) (*agentdb.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.rows[id]
	if !ok {
		return nil, errors.New("not found")
	}
	cp := *row
	return &cp, nil
}

func (s *recordingStore) UpdateSession(_ context.Context, sess *agentdb.Session) (*agentdb.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *sess
	s.rows[sess.ID] = &cp
	return &cp, nil
}

func (s *recordingStore) row(id string) agentdb.Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.rows[id]; ok {
		return *r
	}
	return agentdb.Session{}
}

func (s *recordingStore) SetSnapshotHandle(context.Context, string, imageregistry.Handle) error {
	return nil
}
func (s *recordingStore) GetSnapshotHandle(context.Context, string) (imageregistry.Handle, bool, error) {
	return imageregistry.Handle{}, false, nil
}
func (s *recordingStore) PersistQueryEventsFlat(context.Context, string, string, []events.Envelope, string) error {
	return nil
}
func (s *recordingStore) ListQueryEventsFlat(context.Context, string) ([]events.Envelope, error) {
	return nil, nil
}
func (s *recordingStore) GetWorkerBinding(context.Context, string) (string, bool, error) {
	return "", false, nil
}
func (s *recordingStore) SetWorkerBinding(context.Context, string, string) error { return nil }
func (s *recordingStore) ClearWorkerBinding(context.Context, string) error       { return nil }

// waitFor polls cond until it holds, or fails the test. The create handler's
// store writes happen in a goroutine after CreateSession returns.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for the background create goroutine to settle")
}

// TestBackgroundCreateFailureIsLoggedAndKeepsTheReason covers the two halves of
// the silence: agentd logged NOTHING when a background create failed, and the
// status flip that followed must not clobber the reason the Runner recorded.
func TestBackgroundCreateFailureIsLoggedAndKeepsTheReason(t *testing.T) {
	const reason = `project_settings.base_image = "definitely-not-an-image:v9" (project "acme") ` +
		`names no image in the §13 catalogue, so it was used as a literal registry reference and that reference failed: pull access denied`

	store := newRecordingStore()
	done := make(chan struct{})
	// Stand in for the Runner: it records the reason on the row (as
	// runnerImpl.recordCreateOutcome does) and then returns the error.
	runner := stubRunner{createFn: func(ctx context.Context, req agentkit.CreateSessionRequest) (*agentkit.SessionHandle, error) {
		defer close(done)
		if sess, err := store.GetSession(ctx, req.SessionID); err == nil {
			sess.CreateError = reason
			_, _ = store.UpdateSession(ctx, sess)
		}
		return nil, errors.New("ensure image present: " + reason)
	}}

	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)

	h := newHandlers(t, Config{Runner: runner, Store: store, Identity: okIdentity})
	req := httptest.NewRequest("POST", "/agent/session", strings.NewReader(`{"sessionId":"s-bad"}`))
	rec := httptest.NewRecorder()
	h.CreateSession(rec, req)
	awaitCreate(t, done)

	// The goroutine's store writes happen after createFn returns; give them the
	// same wait the fakes helper gives the create itself.
	waitFor(t, func() bool { return store.row("s-bad").Status == "error" })

	row := store.row("s-bad")
	if row.CreateError != reason {
		t.Errorf("the status flip clobbered the recorded reason: %q", row.CreateError)
	}
	if !strings.Contains(logs.String(), "s-bad") || !strings.Contains(logs.String(), "base_image") {
		t.Errorf("a failed background create must be logged with its cause, got %q", logs.String())
	}
}

// TestGetSessionExposesTheCreateError: a UI rendering `status: "error"` with no
// explanation is the same defect one layer up from the SSE one.
func TestGetSessionExposesTheCreateError(t *testing.T) {
	const reason = `project_settings.base_image = "definitely-not-an-image:v9" (project "acme") failed`
	h := newHandlers(t, Config{
		Runner: stubRunner{},
		Store: stubStore{getSessionFn: func(_ context.Context, id string) (*agentdb.Session, error) {
			return &agentdb.Session{ID: id, Status: "error", CreateError: reason}, nil
		}},
		Identity: okIdentity,
	})
	req := httptest.NewRequest("GET", "/agent/session/s-bad", nil)
	req.SetPathValue("id", "s-bad")
	rec := httptest.NewRecorder()
	h.GetSession(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	var out struct {
		Status      string `json:"status"`
		CreateError string `json:"create_error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != "error" || out.CreateError != reason {
		t.Fatalf("GET must say WHY, got %+v", out)
	}
}
