package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bayes-price/agentkit"
)

func TestNewRequiresRunnerAndIdentity(t *testing.T) {
	idFn := func(*http.Request) (Identity, error) { return Identity{UserEmail: "a@b.c"}, nil }

	// Each guard must fire independently: a Config missing exactly one required
	// dependency returns a non-nil error.
	t.Run("missing Runner", func(t *testing.T) {
		if _, err := New(Config{Store: stubStore{}, Identity: idFn}); err == nil {
			t.Fatal("expected error when Runner nil")
		}
	})
	t.Run("missing Store", func(t *testing.T) {
		if _, err := New(Config{Runner: stubRunner{}, Identity: idFn}); err == nil {
			t.Fatal("expected error when Store nil")
		}
	})
	t.Run("missing Identity", func(t *testing.T) {
		if _, err := New(Config{Runner: stubRunner{}, Store: stubStore{}}); err == nil {
			t.Fatal("expected error when Identity nil")
		}
	})

	h, err := New(Config{
		Runner:   stubRunner{},
		Store:    stubStore{},
		Identity: idFn,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.Mux() == nil {
		t.Fatal("Mux() returned nil")
	}
}

func TestCreateSessionReturnsID(t *testing.T) {
	h, _ := New(Config{
		Runner: stubRunner{createFn: func(_ context.Context, r agentkit.CreateSessionRequest) (*agentkit.SessionHandle, error) {
			if r.UserEmail != "a@b.c" {
				t.Fatalf("identity not merged: %q", r.UserEmail)
			}
			return &agentkit.SessionHandle{SessionID: r.SessionID, State: "running"}, nil
		}},
		Store:    stubStore{},
		Identity: func(*http.Request) (Identity, error) { return Identity{UserEmail: "a@b.c", Customer: "acme"}, nil },
	})
	body := `{"job":"j1","model":"claude-opus-4-5","persona":"default"}`
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
	if out.ID == "" || out.Status == "" {
		t.Fatalf("missing id/status: %s", rec.Body)
	}
}
