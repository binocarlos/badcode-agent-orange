package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bayes-price/agentkit"
	"github.com/bayes-price/agentkit/agentdb"
	"github.com/bayes-price/agentkit/artifacts"
	"github.com/bayes-price/agentkit/events"
)

// okIdentity is a convenience identity function that always succeeds.
var okIdentity = func(*http.Request) (Identity, error) {
	return Identity{UserEmail: "a@b.c", Customer: "acme"}, nil
}

func newHandlers(t *testing.T, cfg Config) *Handlers {
	t.Helper()
	h, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

// --- Status ---

func TestStatusHappyPath(t *testing.T) {
	h := newHandlers(t, Config{
		Runner: stubRunner{statusFn: func(_ context.Context, ref agentkit.SessionRef) (*agentkit.SessionStatus, error) {
			return &agentkit.SessionStatus{SessionID: ref.SessionID, RuntimeState: "running", ActiveQueryID: "q1"}, nil
		}},
		Store:    stubStore{},
		Identity: okIdentity,
	})
	req := httptest.NewRequest("GET", "/agent/session/s1/status", nil)
	req.SetPathValue("id", "s1")
	rec := httptest.NewRecorder()
	h.Status(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	// Response shape: {sessionId, sandboxState, activeQuery:{queryId}}
	var out struct {
		SessionID    string          `json:"sessionId"`
		SandboxState string          `json:"sandboxState"`
		ActiveQuery  *struct{ QueryID string `json:"queryId"` } `json:"activeQuery"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.SessionID != "s1" || out.SandboxState != "running" {
		t.Fatalf("unexpected status: %+v", out)
	}
	if out.ActiveQuery == nil || out.ActiveQuery.QueryID != "q1" {
		t.Fatalf("unexpected activeQuery: %+v", out.ActiveQuery)
	}
}

// The Status response must surface has_snapshot so the UI can decide whether to
// offer Restore. Regression guard: the handler's statusResp once dropped it.
func TestStatusExposesHasSnapshot(t *testing.T) {
	h := newHandlers(t, Config{
		Runner: stubRunner{statusFn: func(_ context.Context, ref agentkit.SessionRef) (*agentkit.SessionStatus, error) {
			return &agentkit.SessionStatus{SessionID: ref.SessionID, RuntimeState: "destroyed", HasSnapshot: true}, nil
		}},
		Store:    stubStore{},
		Identity: okIdentity,
	})
	req := httptest.NewRequest("GET", "/agent/session/s1/status", nil)
	req.SetPathValue("id", "s1")
	rec := httptest.NewRecorder()
	h.Status(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	var out struct {
		HasSnapshot bool `json:"has_snapshot"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.HasSnapshot {
		t.Fatalf("expected has_snapshot=true in status response, got body %s", rec.Body)
	}
}

func TestStatusRunnerErrorIs500(t *testing.T) {
	h := newHandlers(t, Config{
		Runner: stubRunner{statusFn: func(_ context.Context, _ agentkit.SessionRef) (*agentkit.SessionStatus, error) {
			return nil, errors.New("runner down")
		}},
		Store:    stubStore{},
		Identity: okIdentity,
	})
	req := httptest.NewRequest("GET", "/agent/session/s1/status", nil)
	req.SetPathValue("id", "s1")
	rec := httptest.NewRecorder()
	h.Status(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d body=%s", rec.Code, rec.Body)
	}
}

// --- Cancel ---

func TestCancelHappyPath(t *testing.T) {
	stopped := ""
	h := newHandlers(t, Config{
		Runner: stubRunner{stopFn: func(_ context.Context, ref agentkit.SessionRef) error {
			stopped = ref.SessionID
			return nil
		}},
		Store:    stubStore{},
		Identity: okIdentity,
	})
	req := httptest.NewRequest("POST", "/agent/session/s2/cancel", nil)
	req.SetPathValue("id", "s2")
	rec := httptest.NewRecorder()
	h.Cancel(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	if stopped != "s2" {
		t.Fatalf("Stop not called with s2: %q", stopped)
	}
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["status"] != "cancelled" {
		t.Fatalf("unexpected body: %v", out)
	}
}

// --- GetSession ---

func TestGetSessionHappyPath(t *testing.T) {
	h := newHandlers(t, Config{
		Runner: stubRunner{},
		Store: stubStore{getSessionFn: func(_ context.Context, id string) (*agentdb.Session, error) {
			return &agentdb.Session{ID: id, Status: "active"}, nil
		}},
		Identity: okIdentity,
	})
	req := httptest.NewRequest("GET", "/agent/session/s3", nil)
	req.SetPathValue("id", "s3")
	rec := httptest.NewRecorder()
	h.GetSession(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	// Response shape: {id, customer, job, persona, status} (camelCase for frontend)
	var out struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.ID != "s3" || out.Status != "active" {
		t.Fatalf("unexpected session: %+v", out)
	}
}

func TestGetSessionNotFound(t *testing.T) {
	h := newHandlers(t, Config{
		Runner: stubRunner{},
		Store: stubStore{getSessionFn: func(_ context.Context, _ string) (*agentdb.Session, error) {
			return nil, nil
		}},
		Identity: okIdentity,
	})
	req := httptest.NewRequest("GET", "/agent/session/missing", nil)
	req.SetPathValue("id", "missing")
	rec := httptest.NewRecorder()
	h.GetSession(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestGetSessionWrongTenantIs404(t *testing.T) {
	// okIdentity acts as Customer "acme"; the row belongs to "other". The
	// defense-in-depth check must reject with 404 (not 403) to avoid leaking
	// existence.
	h := newHandlers(t, Config{
		Runner: stubRunner{},
		Store: stubStore{getSessionFn: func(_ context.Context, id string) (*agentdb.Session, error) {
			return &agentdb.Session{ID: id, Customer: "other"}, nil
		}},
		Identity: okIdentity,
	})
	req := httptest.NewRequest("GET", "/agent/session/s3", nil)
	req.SetPathValue("id", "s3")
	rec := httptest.NewRecorder()
	h.GetSession(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-tenant read, got %d body=%s", rec.Code, rec.Body)
	}
}

// --- DeleteSession ---

func TestDeleteSessionHappyPath(t *testing.T) {
	destroyed := ""
	h := newHandlers(t, Config{
		Runner: stubRunner{destroyFn: func(_ context.Context, ref agentkit.SessionRef) error {
			destroyed = ref.SessionID
			return nil
		}},
		Store:    stubStore{},
		Identity: okIdentity,
	})
	req := httptest.NewRequest("DELETE", "/agent/session/s4", nil)
	req.SetPathValue("id", "s4")
	rec := httptest.NewRecorder()
	h.DeleteSession(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	if destroyed != "s4" {
		t.Fatalf("Destroy not called with s4: %q", destroyed)
	}
}

// --- Restore ---

func TestRestoreHappyPath(t *testing.T) {
	h := newHandlers(t, Config{
		Runner: stubRunner{resumeFn: func(_ context.Context, ref agentkit.SessionRef) (*agentkit.SessionHandle, error) {
			return &agentkit.SessionHandle{SessionID: ref.SessionID, State: "running", Address: "http://sandbox:3010"}, nil
		}},
		Store:    stubStore{},
		Identity: okIdentity,
	})
	req := httptest.NewRequest("POST", "/agent/session/s5/restore", nil)
	req.SetPathValue("id", "s5")
	rec := httptest.NewRecorder()
	h.Restore(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	var out agentkit.SessionHandle
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.SessionID != "s5" || out.State != "running" {
		t.Fatalf("unexpected handle: %+v", out)
	}
}

// --- Messages ---

func TestMessagesHappyPath(t *testing.T) {
	env := []events.Envelope{
		{Type: events.MessageStart, Data: map[string]any{"id": "e1"}},
		{Type: events.MessageEnd, Data: map[string]any{"id": "e2"}},
	}
	h := newHandlers(t, Config{
		Runner:   stubRunner{},
		Store:    stubStore{evts: env},
		Identity: okIdentity,
	})
	req := httptest.NewRequest("GET", "/agent/session/s6/messages", nil)
	req.SetPathValue("id", "s6")
	rec := httptest.NewRecorder()
	h.Messages(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	var out []events.Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 events, got %d: %s", len(out), rec.Body)
	}
}

func TestMessagesEmptyIsArray(t *testing.T) {
	h := newHandlers(t, Config{
		Runner:   stubRunner{},
		Store:    stubStore{evts: nil},
		Identity: okIdentity,
	})
	req := httptest.NewRequest("GET", "/agent/session/s7/messages", nil)
	req.SetPathValue("id", "s7")
	rec := httptest.NewRecorder()
	h.Messages(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	body := strings.TrimSpace(rec.Body.String())
	if body != "[]" {
		t.Fatalf("expected [], got %q", body)
	}
}

// --- QueryEvents ---

func TestQueryEventsHappyPath(t *testing.T) {
	env := []events.Envelope{
		{Type: events.MessageStart, Data: map[string]any{"id": "e1"}},
		{Type: events.MessageEnd, Data: map[string]any{"id": "e2"}},
	}
	h := newHandlers(t, Config{
		Runner:   stubRunner{},
		Store:    stubStore{evts: env},
		Identity: okIdentity,
	})
	req := httptest.NewRequest("GET", "/agent/session/s6/query-events", nil)
	req.SetPathValue("id", "s6")
	rec := httptest.NewRecorder()
	h.QueryEvents(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	var out []events.Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 events, got %d: %s", len(out), rec.Body)
	}
}

// --- ListSessions ---

func TestListSessionsReturnsEmptyArray(t *testing.T) {
	h := newHandlers(t, Config{
		Runner:   stubRunner{},
		Store:    stubStore{},
		Identity: okIdentity,
	})
	req := httptest.NewRequest("GET", "/agent/sessions", nil)
	rec := httptest.NewRecorder()
	h.ListSessions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	body := strings.TrimSpace(rec.Body.String())
	if body != "[]" {
		t.Fatalf("expected [], got %q", body)
	}
}

// --- SearchMessages ---

func TestSearchMessagesReturnsEmptyArray(t *testing.T) {
	h := newHandlers(t, Config{
		Runner:   stubRunner{},
		Store:    stubStore{},
		Identity: okIdentity,
	})
	req := httptest.NewRequest("GET", "/agent/messages/search", nil)
	rec := httptest.NewRecorder()
	h.SearchMessages(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	body := strings.TrimSpace(rec.Body.String())
	if body != "[]" {
		t.Fatalf("expected [], got %q", body)
	}
}

// --- Artifacts (nil cfg) ---

func TestArtifactsNilStoreReturns501(t *testing.T) {
	h := newHandlers(t, Config{
		Runner:    stubRunner{},
		Store:     stubStore{},
		Identity:  okIdentity,
		Artifacts: nil, // not configured
	})
	req := httptest.NewRequest("GET", "/agent/session/s8/artifacts", nil)
	req.SetPathValue("id", "s8")
	rec := httptest.NewRecorder()
	h.Artifacts(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d body=%s", rec.Code, rec.Body)
	}
}

// --- Artifacts (real store) ---

func TestArtifactsListHappyPath(t *testing.T) {
	arts := []*artifacts.Artifact{
		{ID: "a1", SessionID: "s9", Label: "report.pdf"},
	}
	h := newHandlers(t, Config{
		Runner: stubRunner{},
		Store:  stubStore{},
		Artifacts: &stubArtifacts{listFn: func(_ context.Context, sessionID string) ([]*artifacts.Artifact, error) {
			return arts, nil
		}},
		Identity: okIdentity,
	})
	req := httptest.NewRequest("GET", "/agent/session/s9/artifacts", nil)
	req.SetPathValue("id", "s9")
	rec := httptest.NewRecorder()
	h.Artifacts(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	var out []*artifacts.Artifact
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].ID != "a1" {
		t.Fatalf("unexpected artifacts: %+v", out)
	}
}

// --- CreateArtifact (nil cfg) ---

func TestCreateArtifactNilStoreReturns501(t *testing.T) {
	h := newHandlers(t, Config{
		Runner:    stubRunner{},
		Store:     stubStore{},
		Identity:  okIdentity,
		Artifacts: nil,
	})
	body := `{"label":"chart.png","path":"/workspace/chart.png","type":"image"}`
	req := httptest.NewRequest("POST", "/agent/session/s10/artifacts", strings.NewReader(body))
	req.SetPathValue("id", "s10")
	rec := httptest.NewRecorder()
	h.CreateArtifact(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d", rec.Code)
	}
}

// --- CreateArtifact (real store) ---

func TestCreateArtifactHappyPath(t *testing.T) {
	h := newHandlers(t, Config{
		Runner:    stubRunner{},
		Store:     stubStore{},
		Artifacts: &stubArtifacts{},
		Identity:  okIdentity,
	})
	body := `{"label":"chart.png","path":"/workspace/chart.png","type":"image"}`
	req := httptest.NewRequest("POST", "/agent/session/s10/artifacts", strings.NewReader(body))
	req.SetPathValue("id", "s10")
	rec := httptest.NewRecorder()
	h.CreateArtifact(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	var out artifacts.Artifact
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Label != "chart.png" || out.SessionID != "s10" {
		t.Fatalf("unexpected artifact: %+v", out)
	}
}

func TestCreateArtifactBadJSONIs400(t *testing.T) {
	// Non-nil store so the request passes the 501 guard and reaches JSON decode.
	h := newHandlers(t, Config{
		Runner:    stubRunner{},
		Store:     stubStore{},
		Artifacts: &stubArtifacts{},
		Identity:  okIdentity,
	})
	req := httptest.NewRequest("POST", "/agent/session/s10/artifacts", strings.NewReader("{not json"))
	req.SetPathValue("id", "s10")
	rec := httptest.NewRecorder()
	h.CreateArtifact(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body)
	}
}

// --- Upload (nil cfg) ---

func TestUploadNilStoreReturns501(t *testing.T) {
	h := newHandlers(t, Config{
		Runner:    stubRunner{},
		Store:     stubStore{},
		Identity:  okIdentity,
		Artifacts: nil,
	})
	req := httptest.NewRequest("POST", "/agent/session/s11/upload?path=/workspace/file.txt&label=file.txt&type=file",
		strings.NewReader("file bytes"))
	req.SetPathValue("id", "s11")
	rec := httptest.NewRecorder()
	h.Upload(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d", rec.Code)
	}
}

// --- Upload (real store) ---

func TestUploadHappyPath(t *testing.T) {
	var savedContent []byte
	h := newHandlers(t, Config{
		Runner: stubRunner{},
		Store:  stubStore{},
		Artifacts: &stubArtifacts{saveFn: func(_ context.Context, art *artifacts.Artifact, content io.Reader) (*artifacts.Artifact, error) {
			if content != nil {
				buf := make([]byte, 512)
				n, _ := content.Read(buf)
				savedContent = buf[:n]
			}
			return art, nil
		}},
		Identity: okIdentity,
	})
	req := httptest.NewRequest("POST", "/agent/session/s12/upload?path=/workspace/data.csv&label=data.csv&type=file",
		strings.NewReader("col1,col2\n1,2"))
	req.SetPathValue("id", "s12")
	rec := httptest.NewRecorder()
	h.Upload(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	if !strings.Contains(string(savedContent), "col1") {
		t.Fatalf("content not saved: %q", savedContent)
	}
	var out artifacts.Artifact
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.SessionID != "s12" {
		t.Fatalf("unexpected artifact: %+v", out)
	}
}

// --- Stream ---

func TestStreamSetsSSEHeaders(t *testing.T) {
	h := newHandlers(t, Config{
		Runner: stubRunner{streamFn: func(_ context.Context, ref agentkit.SessionRef, opts agentkit.StreamOptions, w agentkit.Writer) error {
			_, _ = w.Write([]byte("event: connected\ndata: {}\n\n"))
			return nil
		}},
		Store:    stubStore{},
		Identity: okIdentity,
	})
	req := httptest.NewRequest("GET", "/agent/session/s13/stream", nil)
	req.SetPathValue("id", "s13")
	rec := httptest.NewRecorder()
	h.Stream(rec, req)

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type=%q", ct)
	}
	if !strings.Contains(rec.Body.String(), "event: connected") {
		t.Fatalf("frame missing: %q", rec.Body)
	}
}

func TestReconnectSetsIsReconnectTrue(t *testing.T) {
	var gotOpts agentkit.StreamOptions
	h := newHandlers(t, Config{
		Runner: stubRunner{streamFn: func(_ context.Context, _ agentkit.SessionRef, opts agentkit.StreamOptions, _ agentkit.Writer) error {
			gotOpts = opts
			return nil
		}},
		Store:    stubStore{},
		Identity: okIdentity,
	})
	req := httptest.NewRequest("GET", "/agent/session/s14/reconnect?queryId=q99", nil)
	req.SetPathValue("id", "s14")
	rec := httptest.NewRecorder()
	h.Reconnect(rec, req)

	if !gotOpts.IsReconnect {
		t.Fatal("expected IsReconnect=true")
	}
	if gotOpts.QueryID != "q99" {
		t.Fatalf("expected QueryID=q99, got %q", gotOpts.QueryID)
	}
}
