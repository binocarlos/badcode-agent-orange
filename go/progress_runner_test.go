package agentkit

import (
	"context"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

func TestSnapshot_RecordsProgress(t *testing.T) {
	r, _, _, store, _, _ := newTestRunner(t)
	ctx := context.Background()
	sid := "sess-snap"
	store.Seed(&agentdb.Session{ID: sid, Customer: "acme", Job: "j1"})
	if _, err := r.CreateSession(ctx, CreateSessionRequest{SessionID: sid, Customer: "acme", Job: "j1"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := r.Snapshot(ctx, SessionRef{SessionID: sid}); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	got, ok := r.progress.get(sid)
	if !ok {
		t.Fatal("expected progress recorded for snapshot")
	}
	if got.Op != "snapshot" || got.Phase != "done" {
		t.Fatalf("unexpected op/phase: %+v", got)
	}
	if got.BytesDone != 100 || got.BytesTotal != 100 {
		t.Fatalf("expected mock-emitted bytes 100/100, got %d/%d", got.BytesDone, got.BytesTotal)
	}
}

func TestStatus_CarriesProgress(t *testing.T) {
	r, _, _, store, _, _ := newTestRunner(t)
	ctx := context.Background()
	sid := "sess-status"
	store.Seed(&agentdb.Session{ID: sid, Customer: "acme", Job: "j1"})
	if _, err := r.CreateSession(ctx, CreateSessionRequest{SessionID: sid, Customer: "acme", Job: "j1"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := r.Snapshot(ctx, SessionRef{SessionID: sid}); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	st, err := r.Status(ctx, SessionRef{SessionID: sid})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Progress == nil {
		t.Fatal("expected Status to carry progress")
	}
	if st.Progress.Op != "snapshot" || st.Progress.Phase != "done" {
		t.Fatalf("unexpected progress on status: %+v", st.Progress)
	}
	// Destroyed branch must still carry progress (covers idle-archive toast).
	if err := r.Destroy(ctx, SessionRef{SessionID: sid}); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	st2, err := r.Status(ctx, SessionRef{SessionID: sid})
	if err != nil {
		t.Fatalf("Status after destroy: %v", err)
	}
	if st2.Progress == nil {
		t.Fatal("expected progress on destroyed-branch status")
	}
}

func TestRestore_RecordsProgress(t *testing.T) {
	r, _, _, store, _, _ := newTestRunner(t)
	ctx := context.Background()
	sid := "sess-restore"
	store.Seed(&agentdb.Session{ID: sid, Customer: "acme", Job: "j1"})
	if _, err := r.CreateSession(ctx, CreateSessionRequest{SessionID: sid, Customer: "acme", Job: "j1"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := r.Snapshot(ctx, SessionRef{SessionID: sid}); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if err := r.Destroy(ctx, SessionRef{SessionID: sid}); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, err := r.Resume(ctx, SessionRef{SessionID: sid}); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	got, ok := r.progress.get(sid)
	if !ok {
		t.Fatal("expected progress recorded for restore")
	}
	if got.Op != "restore" || got.Phase != "done" {
		t.Fatalf("unexpected op/phase: %+v", got)
	}
	if got.BytesDone != 100 || got.BytesTotal != 100 {
		t.Fatalf("expected mock-emitted pull bytes 100/100, got %d/%d", got.BytesDone, got.BytesTotal)
	}
}
