package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange"
)

func TestCreateSession_ForwardsCustomImageID(t *testing.T) {
	var captured agentkit.CreateSessionRequest
	done := make(chan struct{}, 1)
	h := newHandlers(t, Config{
		Runner: stubRunner{createFn: func(ctx context.Context, req agentkit.CreateSessionRequest) (*agentkit.SessionHandle, error) {
			captured = req
			done <- struct{}{}
			return &agentkit.SessionHandle{SessionID: req.SessionID, State: "running"}, nil
		}},
		Store:    stubStore{},
		Identity: func(*http.Request) (Identity, error) { return Identity{UserEmail: "a@b.c", Customer: "acme"}, nil },
	})
	body := `{"sessionId":"s1","job":"j","customImageId":"img-1"}`
	req := httptest.NewRequest("POST", "/agent/session", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.CreateSession(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d body=%s", rec.Code, rec.Body)
	}
	var out struct{ ID, Status string }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	awaitCreate(t, done)
	if captured.CustomImageID != "img-1" {
		t.Fatalf("customImageId not forwarded, got %q", captured.CustomImageID)
	}
}

// TestCreateSession_CustomImageID_TakesPrecedenceOverInstallation verifies that
// when both customImageId AND installation are set in the request body, the
// customImageId wins: req.Image must be empty (so resolveLaunchImage ranks
// CustomImageID first) and req.CustomImageID must carry the caller's value.
func TestCreateSession_CustomImageID_TakesPrecedenceOverInstallation(t *testing.T) {
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
		// ImageResolver is wired — simulates a Platinum deployment that has installations.
		ImageResolver: func(name string) (string, error) {
			return "RESOLVED-" + name, nil
		},
	})

	// Body carries BOTH fields — the frontend always auto-sends installation.
	body := `{"sessionId":"s2","customImageId":"custom-img-42","installation":"core-v1"}`
	req := httptest.NewRequest("POST", "/agent/session", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.CreateSession(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	awaitCreate(t, done)
	// Custom image must win: Image must be empty so resolveLaunchImage picks CustomImageID.
	if captured.Image != "" {
		t.Fatalf("req.Image = %q, want empty (customImageId must win over installation)", captured.Image)
	}
	if captured.CustomImageID != "custom-img-42" {
		t.Fatalf("req.CustomImageID = %q, want custom-img-42", captured.CustomImageID)
	}
}
