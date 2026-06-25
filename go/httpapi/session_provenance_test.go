package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bayes-price/agentkit"
	"github.com/bayes-price/agentkit/agentdb"
)

// captureStore records the session row persisted at create time (Status=="creating")
// so we can assert provenance fields land on the row, not just the provisioning request.
type captureStore struct {
	stubStore
	created *agentdb.Session
}

func (c *captureStore) UpdateSession(ctx context.Context, s *agentdb.Session) (*agentdb.Session, error) {
	if s.Status == "creating" {
		c.created = s
	}
	return s, nil
}

func TestCreateSession_PersistsCustomImageIDOnRow(t *testing.T) {
	store := &captureStore{}
	done := make(chan struct{}, 1)
	h := newHandlers(t, Config{
		Runner: stubRunner{createFn: func(_ context.Context, req agentkit.CreateSessionRequest) (*agentkit.SessionHandle, error) {
			done <- struct{}{}
			return &agentkit.SessionHandle{SessionID: req.SessionID, State: "running"}, nil
		}},
		Store:    store,
		Identity: func(*http.Request) (Identity, error) { return Identity{UserEmail: "a@b.c", Customer: "acme"}, nil },
	})

	body := `{"sessionId":"s1","job":"j","customImageId":"img-1"}`
	req := httptest.NewRequest("POST", "/agent/session", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.CreateSession(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d body=%s", rec.Code, rec.Body)
	}
	awaitCreate(t, done)
	if store.created == nil {
		t.Fatal("no session row persisted")
	}
	if store.created.CustomImageID != "img-1" {
		t.Fatalf("row.CustomImageID = %q, want img-1", store.created.CustomImageID)
	}
}
