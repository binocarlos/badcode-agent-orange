package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/binocarlos/badcode-agent-orange"
	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/imageregistry"
)

func TestArchiveSnapshotsThenDestroys(t *testing.T) {
	var snapped, destroyed bool
	h := newHandlers(t, Config{
		Runner: stubRunner{
			snapshotFn: func(_ context.Context, _ agentkit.SessionRef) (imageregistry.Handle, error) {
				snapped = true
				return imageregistry.Handle{Kind: "registry", Ref: "r/x:latest"}, nil
			},
			destroyFn: func(_ context.Context, _ agentkit.SessionRef) error { destroyed = true; return nil },
		},
		Store: stubStore{getSessionFn: func(_ context.Context, _ string) (*agentdb.Session, error) {
			return &agentdb.Session{ID: "s1", Customer: "c1"}, nil
		}},
		Identity: func(*http.Request) (Identity, error) { return Identity{Customer: "c1"}, nil },
	})
	req := httptest.NewRequest("POST", "/agent/session/s1/archive", nil)
	req.SetPathValue("id", "s1")
	w := httptest.NewRecorder()
	h.Archive(w, req)
	if w.Code != http.StatusOK || !snapped || !destroyed {
		t.Fatalf("archive: code=%d snapped=%v destroyed=%v", w.Code, snapped, destroyed)
	}
}

// TestRestoreCrossCustomerIs404 asserts that a caller scoped to a different
// customer cannot restore another tenant's session. The ownsSession check must
// reject with 404 (not 403) so existence isn't leaked.
func TestRestoreCrossCustomerIs404(t *testing.T) {
	h := newHandlers(t, Config{
		Runner: stubRunner{},
		Store: stubStore{getSessionFn: func(_ context.Context, id string) (*agentdb.Session, error) {
			return &agentdb.Session{ID: id, Customer: "owner-co"}, nil
		}},
		Identity: func(*http.Request) (Identity, error) { return Identity{Customer: "other-co"}, nil },
	})
	req := httptest.NewRequest("POST", "/agent/session/s1/restore", nil)
	req.SetPathValue("id", "s1")
	w := httptest.NewRecorder()
	h.Restore(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-customer restore: want 404, got %d", w.Code)
	}
}
