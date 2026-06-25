package agentkit

import (
	"context"
	"testing"

	"github.com/bayes-price/agentkit/agentdb"
)

// CreateSession must record image-pull progress under a "create" op so the
// frontend can render a download bar while the launch image is being pulled.
func TestCreateSession_RecordsProgress(t *testing.T) {
	r, _, _, store, _, _ := newTestRunner(t)
	ctx := context.Background()
	sid := "sess-create"
	store.Seed(&agentdb.Session{ID: sid, Customer: "acme", Job: "j1"})
	if _, err := r.CreateSession(ctx, CreateSessionRequest{SessionID: sid, Customer: "acme", Job: "j1"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	got, ok := r.progress.get(sid)
	if !ok {
		t.Fatal("expected progress recorded for create")
	}
	if got.Op != "create" || got.Phase != "done" {
		t.Fatalf("unexpected op/phase: %+v", got)
	}
	if got.BytesDone != 100 || got.BytesTotal != 100 {
		t.Fatalf("expected mock-emitted pull bytes 100/100, got %d/%d", got.BytesDone, got.BytesTotal)
	}
}

// MarkCreating pre-registers the create progress synchronously so a status poll
// landing before CreateSession's goroutine schedules still observes an active op
// (preventing the frontend from treating the not-yet-provisioned session as
// settled/destroyed and stopping its poll).
func TestMarkCreating_RegistersDownloadingPhase(t *testing.T) {
	r, _, _, _, _, _ := newTestRunner(t)
	sid := "sess-mark"
	r.MarkCreating(sid)
	got, ok := r.progress.get(sid)
	if !ok {
		t.Fatal("expected progress recorded after MarkCreating")
	}
	if got.Op != "create" || got.Phase != "downloading" {
		t.Fatalf("unexpected op/phase: %+v", got)
	}
}
