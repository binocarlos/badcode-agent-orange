package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/bayes-price/agentkit/agentdb"
	"github.com/bayes-price/agentkit/events"
	"github.com/bayes-price/agentkit/imageregistry"
)

// openTestStore opens a fresh SQLite store in a temp directory and registers
// cleanup. Tests use this helper to avoid repeating boilerplate.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestSessionRoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	in := &agentdb.Session{ID: "s1", Customer: "acme", Job: "j1", UserEmail: "u@x.y", Persona: "default", Status: "running"}
	if _, err := st.UpdateSession(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetSession(ctx, "s1")
	if err != nil || got.Customer != "acme" || got.Status != "running" {
		t.Fatalf("got %+v err %v", got, err)
	}

	if err := st.SetWorkerBinding(ctx, "s1", "w1"); err != nil {
		t.Fatal(err)
	}
	wid, ok, err := st.GetWorkerBinding(ctx, "s1")
	if err != nil || !ok || wid != "w1" {
		t.Fatalf("binding wid=%q ok=%v err=%v", wid, ok, err)
	}

	evs := []events.Envelope{{Type: events.Type("assistant"), Data: map[string]any{"text": "hi"}}}
	if err := st.PersistQueryEventsFlat(ctx, "s1", "q1", evs, "hi"); err != nil {
		t.Fatal(err)
	}
	back, err := st.ListQueryEventsFlat(ctx, "s1")
	if err != nil || len(back) != 1 || back[0].Type != events.Type("assistant") {
		t.Fatalf("events back=%+v err=%v", back, err)
	}
}

func TestSnapshotHandleRoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	// Absent: ok=false, err=nil.
	if _, ok, err := st.GetSnapshotHandle(ctx, "s1"); err != nil || ok {
		t.Fatalf("absent: ok=%v err=%v (want ok=false err=nil)", ok, err)
	}

	want := imageregistry.Handle{
		Kind: "blob-archive",
		Ref:  "snapshots/s1.tar",
		Meta: map[string]string{"sessionID": "s1"},
	}
	if err := st.SetSnapshotHandle(ctx, "s1", want); err != nil {
		t.Fatal(err)
	}

	got, ok, err := st.GetSnapshotHandle(ctx, "s1")
	if err != nil || !ok {
		t.Fatalf("present: ok=%v err=%v (want ok=true err=nil)", ok, err)
	}
	if got.Kind != want.Kind || got.Ref != want.Ref || got.Meta["sessionID"] != want.Meta["sessionID"] {
		t.Fatalf("handle did not round-trip: got %+v want %+v", got, want)
	}
}

// TestGetSessionNotFound verifies that GetSession returns an error (wrapping
// sql.ErrNoRows) for a session ID that has never been inserted.
func TestGetSessionNotFound(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	_, err := st.GetSession(ctx, "nonexistent-session")
	if err == nil {
		t.Fatal("expected error for non-existent session, got nil")
	}
	// The error must wrap sql.ErrNoRows so callers can distinguish not-found.
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("error does not wrap sql.ErrNoRows: %v", err)
	}
}

// TestUpdateSessionPersistsChanges verifies that UpdateSession actually changes
// stored fields when called a second time with new values.
func TestUpdateSessionPersistsChanges(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	// Insert initial session.
	initial := &agentdb.Session{
		ID: "s-update", Customer: "acme", Job: "j1",
		UserEmail: "old@acme.com", Persona: "analyst", Status: "running",
	}
	if _, err := st.UpdateSession(ctx, initial); err != nil {
		t.Fatalf("initial UpdateSession: %v", err)
	}

	// Update with a new status and persona.
	updated := &agentdb.Session{
		ID: "s-update", Status: "suspended", Persona: "researcher",
	}
	if _, err := st.UpdateSession(ctx, updated); err != nil {
		t.Fatalf("second UpdateSession: %v", err)
	}

	got, err := st.GetSession(ctx, "s-update")
	if err != nil {
		t.Fatalf("GetSession after update: %v", err)
	}
	// Status and persona should have changed.
	if got.Status != "suspended" {
		t.Errorf("Status = %q, want suspended", got.Status)
	}
	if got.Persona != "researcher" {
		t.Errorf("Persona = %q, want researcher", got.Persona)
	}
	// Non-updated fields should be preserved (upsert merge semantics).
	if got.Customer != "acme" {
		t.Errorf("Customer = %q, want acme (should be preserved)", got.Customer)
	}
	if got.UserEmail != "old@acme.com" {
		t.Errorf("UserEmail = %q, want old@acme.com (should be preserved)", got.UserEmail)
	}
}

// TestPersistQueryEventsAndList verifies the PersistQueryEventsFlat / ListQueryEventsFlat
// round-trip in isolation.
func TestPersistQueryEventsAndList(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	sessionID := "sess-qevents"

	evs1 := []events.Envelope{
		{Type: events.Type("user"), Data: map[string]any{"text": "hello"}},
	}
	evs2 := []events.Envelope{
		{Type: events.Type("assistant"), Data: map[string]any{"text": "world"}},
		{Type: events.Type("tool_use"), Data: map[string]any{"name": "calc"}},
	}

	if err := st.PersistQueryEventsFlat(ctx, sessionID, "q1", evs1, "hello"); err != nil {
		t.Fatalf("PersistQueryEventsFlat q1: %v", err)
	}
	if err := st.PersistQueryEventsFlat(ctx, sessionID, "q2", evs2, "world"); err != nil {
		t.Fatalf("PersistQueryEventsFlat q2: %v", err)
	}

	all, err := st.ListQueryEventsFlat(ctx, sessionID)
	if err != nil {
		t.Fatalf("ListQueryEventsFlat: %v", err)
	}

	// 1 + 2 = 3 total events, in order.
	if len(all) != 3 {
		t.Fatalf("len(all) = %d, want 3", len(all))
	}
	if all[0].Type != events.Type("user") {
		t.Errorf("all[0].Type = %q, want user", all[0].Type)
	}
	if all[1].Type != events.Type("assistant") {
		t.Errorf("all[1].Type = %q, want assistant", all[1].Type)
	}
	if all[2].Type != events.Type("tool_use") {
		t.Errorf("all[2].Type = %q, want tool_use", all[2].Type)
	}
}

// TestPersistQueryEventsUpdateInPlace verifies that re-persisting the same
// (sessionID, queryID) pair overwrites rather than duplicates the events.
func TestPersistQueryEventsUpdateInPlace(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	sessionID := "sess-upsert"
	queryID := "q1"

	original := []events.Envelope{{Type: events.Type("user"), Data: map[string]any{"v": 1}}}
	if err := st.PersistQueryEventsFlat(ctx, sessionID, queryID, original, ""); err != nil {
		t.Fatalf("first persist: %v", err)
	}

	updated := []events.Envelope{
		{Type: events.Type("user"), Data: map[string]any{"v": 1}},
		{Type: events.Type("assistant"), Data: map[string]any{"v": 2}},
	}
	if err := st.PersistQueryEventsFlat(ctx, sessionID, queryID, updated, ""); err != nil {
		t.Fatalf("second persist: %v", err)
	}

	all, err := st.ListQueryEventsFlat(ctx, sessionID)
	if err != nil {
		t.Fatalf("ListQueryEventsFlat: %v", err)
	}
	// Should be 2 (the updated payload), not 3 (original + updated).
	if len(all) != 2 {
		t.Errorf("len(all) = %d, want 2 (upsert should overwrite)", len(all))
	}
}

// TestListQueryEventsEmptySession verifies that listing events for a session
// with no events returns a nil/empty slice without error.
func TestListQueryEventsEmptySession(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	evs, err := st.ListQueryEventsFlat(ctx, "empty-session")
	if err != nil {
		t.Fatalf("ListQueryEventsFlat for empty session: %v", err)
	}
	if len(evs) != 0 {
		t.Errorf("len = %d, want 0", len(evs))
	}
}

// TestSnapshotHandleOverwrite verifies that SetSnapshotHandle can overwrite
// an existing handle (upsert) and GetSnapshotHandle returns the latest value.
func TestSnapshotHandleOverwrite(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	first := imageregistry.Handle{Kind: "blob-archive", Ref: "snap/v1.tar"}
	if err := st.SetSnapshotHandle(ctx, "s-snap", first); err != nil {
		t.Fatalf("SetSnapshotHandle v1: %v", err)
	}

	second := imageregistry.Handle{Kind: "registry", Ref: "acr.io/snap:v2"}
	if err := st.SetSnapshotHandle(ctx, "s-snap", second); err != nil {
		t.Fatalf("SetSnapshotHandle v2: %v", err)
	}

	got, ok, err := st.GetSnapshotHandle(ctx, "s-snap")
	if err != nil || !ok {
		t.Fatalf("GetSnapshotHandle: ok=%v err=%v", ok, err)
	}
	if got.Ref != second.Ref || got.Kind != second.Kind {
		t.Errorf("got %+v, want %+v (latest overwrite)", got, second)
	}
}

// TestBlobsNotNil verifies that the Blobs() method returns a non-nil BlobStore.
func TestBlobsNotNil(t *testing.T) {
	st := openTestStore(t)
	blobs := st.Blobs()
	if blobs == nil {
		t.Fatal("Blobs() returned nil, want non-nil BlobStore")
	}
}

// TestWorkerBindingClearRoundTrip verifies the full worker binding lifecycle:
// set → get → clear → get-absent.
func TestWorkerBindingClearRoundTrip(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	sessionID := "s-wb"

	// Initially absent.
	wid, ok, err := st.GetWorkerBinding(ctx, sessionID)
	if err != nil || ok || wid != "" {
		t.Fatalf("absent: wid=%q ok=%v err=%v (want empty+false+nil)", wid, ok, err)
	}

	// Set binding.
	if err := st.SetWorkerBinding(ctx, sessionID, "worker-42"); err != nil {
		t.Fatalf("SetWorkerBinding: %v", err)
	}

	// Read binding.
	wid, ok, err = st.GetWorkerBinding(ctx, sessionID)
	if err != nil || !ok || wid != "worker-42" {
		t.Fatalf("present: wid=%q ok=%v err=%v (want worker-42+true+nil)", wid, ok, err)
	}

	// Clear binding.
	if err := st.ClearWorkerBinding(ctx, sessionID); err != nil {
		t.Fatalf("ClearWorkerBinding: %v", err)
	}

	// Should be absent again.
	wid, ok, err = st.GetWorkerBinding(ctx, sessionID)
	if err != nil || ok || wid != "" {
		t.Fatalf("after clear: wid=%q ok=%v err=%v (want empty+false+nil)", wid, ok, err)
	}
}

// TestWorkerBindingOverwrite verifies that SetWorkerBinding updates the worker
// when called a second time for the same session.
func TestWorkerBindingOverwrite(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	if err := st.SetWorkerBinding(ctx, "s-wb2", "worker-1"); err != nil {
		t.Fatalf("SetWorkerBinding first: %v", err)
	}
	if err := st.SetWorkerBinding(ctx, "s-wb2", "worker-2"); err != nil {
		t.Fatalf("SetWorkerBinding second: %v", err)
	}
	wid, ok, err := st.GetWorkerBinding(ctx, "s-wb2")
	if err != nil || !ok {
		t.Fatalf("GetWorkerBinding: ok=%v err=%v", ok, err)
	}
	if wid != "worker-2" {
		t.Errorf("wid = %q, want worker-2 (latest overwrite)", wid)
	}
}

// TestClearWorkerBindingNoOp verifies that ClearWorkerBinding does not error
// when called for a session that has no binding.
func TestClearWorkerBindingNoOp(t *testing.T) {
	st := openTestStore(t)
	if err := st.ClearWorkerBinding(context.Background(), "no-binding-session"); err != nil {
		t.Fatalf("ClearWorkerBinding on absent session: %v", err)
	}
}
