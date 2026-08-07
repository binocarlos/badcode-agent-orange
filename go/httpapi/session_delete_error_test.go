package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// DELETE /agent/session/{id} used to discard the store's error (`_ =`) and
// answer 204 whatever happened (doc 22 RD5's second defect): a delete that
// deleted nothing reported success, the browser dropped the row from the list,
// and the session reappeared on the next refresh with no explanation anywhere.
//
// stubStore has no DeleteSession, so the handler's optional-seam branch is what
// these two types supply.

type deletingStore struct {
	stubStore
	deleted *[]string
	err     error
}

func (s deletingStore) DeleteSession(_ context.Context, id string) error {
	*s.deleted = append(*s.deleted, id)
	return s.err
}

func TestDeleteSessionReportsAStoreFailure(t *testing.T) {
	var deleted []string
	h := newHandlers(t, Config{
		Runner: stubRunner{},
		Store: deletingStore{
			deleted: &deleted,
			err:     errors.New("failed to delete agent session: connection refused"),
		},
		Identity: okIdentity,
	})
	req := httptest.NewRequest("DELETE", "/agent/session/s9", nil)
	req.SetPathValue("id", "s9")
	rec := httptest.NewRecorder()
	h.DeleteSession(rec, req)

	if rec.Code == http.StatusNoContent {
		t.Fatalf("a failed delete answered 204 — the caller is being told the session is gone when it is not")
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	// The store's own sentence, not "HTTP 500": it is the only place the reason
	// exists, and the dialog shows it verbatim.
	if !strings.Contains(rec.Body.String(), "connection refused") {
		t.Fatalf("the reason must reach the caller, got %q", rec.Body.String())
	}
	if len(deleted) != 1 || deleted[0] != "s9" {
		t.Fatalf("the store must have been asked once for s9, got %v", deleted)
	}
}

func TestDeleteSessionSucceedsWhenTheStoreAgrees(t *testing.T) {
	var deleted []string
	h := newHandlers(t, Config{
		Runner:   stubRunner{},
		Store:    deletingStore{deleted: &deleted},
		Identity: okIdentity,
	})
	req := httptest.NewRequest("DELETE", "/agent/session/s10", nil)
	req.SetPathValue("id", "s10")
	rec := httptest.NewRecorder()
	h.DeleteSession(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	if len(deleted) != 1 || deleted[0] != "s10" {
		t.Fatalf("expected exactly one delete of s10, got %v", deleted)
	}
}
