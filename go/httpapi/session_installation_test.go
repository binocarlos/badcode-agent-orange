package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange"
)

func TestCreateSession_ResolvesInstallationToImage(t *testing.T) {
	var captured agentkit.CreateSessionRequest
	done := make(chan struct{}, 1)
	h := newHandlers(t, Config{
		Runner: stubRunner{createFn: func(_ context.Context, req agentkit.CreateSessionRequest) (*agentkit.SessionHandle, error) {
			captured = req
			done <- struct{}{}
			return &agentkit.SessionHandle{SessionID: req.SessionID, State: "running"}, nil
		}},
		Store:    stubStore{},
		Identity: func(*http.Request) (Identity, error) { return Identity{UserEmail: "a@b.c", Customer: "acme"}, nil },
		ImageResolver: func(name string) (string, error) {
			return "RESOLVED-" + name, nil
		},
	})

	req := httptest.NewRequest("POST", "/agent/session", strings.NewReader(`{"installation":"core-v2"}`))
	w := httptest.NewRecorder()
	h.CreateSession(w, req)

	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	awaitCreate(t, done)
	if captured.Image != "RESOLVED-core-v2" {
		t.Fatalf("req.Image = %q, want RESOLVED-core-v2", captured.Image)
	}
}

func TestCreateSession_NilImageResolver_NoImageSet(t *testing.T) {
	// When ImageResolver is nil, Image must not be set (existing behavior preserved).
	var captured agentkit.CreateSessionRequest
	done := make(chan struct{}, 1)
	h := newHandlers(t, Config{
		Runner: stubRunner{createFn: func(_ context.Context, req agentkit.CreateSessionRequest) (*agentkit.SessionHandle, error) {
			captured = req
			done <- struct{}{}
			return &agentkit.SessionHandle{SessionID: req.SessionID, State: "running"}, nil
		}},
		Store:    stubStore{},
		Identity: func(*http.Request) (Identity, error) { return Identity{UserEmail: "a@b.c", Customer: "acme"}, nil },
		// ImageResolver intentionally nil
	})

	req := httptest.NewRequest("POST", "/agent/session", strings.NewReader(`{"installation":"core-v2"}`))
	w := httptest.NewRecorder()
	h.CreateSession(w, req)

	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	awaitCreate(t, done)
	if captured.Image != "" {
		t.Fatalf("req.Image = %q, want empty (nil resolver)", captured.Image)
	}
}

func TestCreateSession_ImageResolver_ErrorReturnsBadRequest(t *testing.T) {
	h := newHandlers(t, Config{
		Runner:   stubRunner{},
		Store:    stubStore{},
		Identity: func(*http.Request) (Identity, error) { return Identity{UserEmail: "a@b.c", Customer: "acme"}, nil },
		ImageResolver: func(name string) (string, error) {
			return "", &installationError{name}
		},
	})

	req := httptest.NewRequest("POST", "/agent/session", strings.NewReader(`{"installation":"unknown-install"}`))
	w := httptest.NewRecorder()
	h.CreateSession(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", w.Code)
	}
}

type installationError struct{ name string }

func (e *installationError) Error() string { return "installation not found: " + e.name }
