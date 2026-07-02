package pgstore

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

func TestPgTicketStoreCRUD(t *testing.T) {
	ctx := context.Background()
	ts := NewPgTicketStore(newTestDB(t))

	id, err := ts.Create(ctx, orchestrator.Ticket{
		ProjectID: "badcode", Title: "Draft post", Objective: "write X",
		Acceptance: "on-brand", Status: orchestrator.StatusTodo,
		Scope: json.RawMessage(`{"name":"post-writer"}`), DependsOn: []string{"t0"}, BoardRev: "r3",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if id == "" {
		t.Fatalf("expected generated id")
	}

	got, err := ts.Get(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != "Draft post" || got.Status != orchestrator.StatusTodo ||
		len(got.DependsOn) != 1 || got.DependsOn[0] != "t0" || string(got.Scope) != `{"name":"post-writer"}` {
		t.Fatalf("get round-trip wrong: %+v", got)
	}
	if got.CreatedAt == 0 || got.UpdatedAt == 0 {
		t.Fatalf("timestamps not stamped: %+v", got)
	}

	got.Status = orchestrator.StatusInReview
	got.Result = json.RawMessage(`{"status":"done"}`)
	got.PublishedRef = "https://x.example/42"
	if err := ts.Update(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	again, _ := ts.Get(ctx, id)
	if again.Status != orchestrator.StatusInReview || string(again.Result) != `{"status":"done"}` {
		t.Fatalf("update not persisted: %+v", again)
	}
	if again.PublishedRef != "https://x.example/42" {
		t.Fatalf("published_ref not persisted: %+v", again)
	}
}

// §10c I-1 — the gate-bypass bug. A ticket stored with nil Scope/Result/
// PendingPost lands as '{}' in the JSONB columns; fromRow must map that back to
// nil so every len(...)==0 emptiness guard behaves identically on Mem and Pg.
func TestPgTicketStoreEmptyJSONRoundTripsToNil(t *testing.T) {
	ctx := context.Background()
	ts := NewPgTicketStore(newTestDB(t))

	id, err := ts.Create(ctx, orchestrator.Ticket{
		Title: "escalated", Status: orchestrator.StatusNeedsHuman, // no PendingPost: a worker escalation
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := ts.Get(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.PendingPost) != 0 {
		t.Fatalf("PendingPost = %q, want nil/empty (len %d)", got.PendingPost, len(got.PendingPost))
	}
	if len(got.Scope) != 0 || len(got.Result) != 0 {
		t.Fatalf("Scope/Result not nil: scope=%q result=%q", got.Scope, got.Result)
	}
	// A REAL pending post still round-trips verbatim.
	got.PendingPost = json.RawMessage(`{"channel":"bsky","text":"hi"}`)
	if err := ts.Update(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	again, _ := ts.Get(ctx, id)
	if string(again.PendingPost) != `{"channel":"bsky","text":"hi"}` {
		t.Fatalf("real pending post mangled: %q", again.PendingPost)
	}
}

// publishRecorder counts Connector.Publish calls — the gate must never reach it
// for a ticket with no pending post.
type publishRecorder struct{ calls int }

func (c *publishRecorder) Publish(context.Context, orchestrator.Post) (string, error) {
	c.calls++
	return "at://ref/1", nil
}

// §10c I-1 parity: an escalated (needs_human, NO pending post) ticket must make
// ApprovalService.Approve error "no pending post" on BOTH stores — before the
// fix the Pg store round-tripped '{}' and bypassed the publish gate.
func TestApprovalNoPendingPostParityMemVsPg(t *testing.T) {
	ctx := context.Background()
	stores := map[string]orchestrator.TicketStore{
		"mem": orchestrator.NewMemTickets(),
		"pg":  NewPgTicketStore(newTestDB(t)),
	}
	for name, store := range stores {
		conn := &publishRecorder{}
		svc := orchestrator.NewApprovalService(store, conn, orchestrator.NewTelemetry())
		id, err := store.Create(ctx, orchestrator.Ticket{
			Title: "escalated", Status: orchestrator.StatusNeedsHuman,
		})
		if err != nil {
			t.Fatalf("[%s] create: %v", name, err)
		}
		_, err = svc.Approve(ctx, id)
		if err == nil || !strings.Contains(err.Error(), "no pending post") {
			t.Fatalf("[%s] approve = %v, want 'no pending post' error", name, err)
		}
		if conn.calls != 0 {
			t.Fatalf("[%s] publish gate bypassed: connector called %d times", name, conn.calls)
		}
	}
}

// §10c I-2: Update is guarded — an unknown id errors (RowsAffected==0) and no
// phantom row is upserted. gorm Save is banned for updates.
func TestPgTicketStoreUpdateUnknownIDFailsLoud(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	ts := NewPgTicketStore(db)

	err := ts.Update(ctx, orchestrator.Ticket{ID: "ghost", Title: "phantom"})
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("update unknown id = %v, want fail-loud error", err)
	}
	var count int64
	if err := db.Model(&agentdb.Ticket{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("phantom row upserted: %d tickets", count)
	}
}

// §10c I-6: Disposition and AttemptNotes round-trip through the Pg store.
func TestPgTicketStoreDispositionAndAttemptNotesRoundTrip(t *testing.T) {
	ctx := context.Background()
	ts := NewPgTicketStore(newTestDB(t))

	id, err := ts.Create(ctx, orchestrator.Ticket{
		Title: "publishable", Status: orchestrator.StatusTodo,
		Disposition:  orchestrator.DispositionPublish,
		AttemptNotes: []string{"too dull", "still too dull"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := ts.Get(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Disposition != orchestrator.DispositionPublish {
		t.Fatalf("disposition = %q, want publish", got.Disposition)
	}
	if len(got.AttemptNotes) != 2 || got.AttemptNotes[1] != "still too dull" {
		t.Fatalf("attempt notes round-trip wrong: %+v", got.AttemptNotes)
	}

	// Empty notes come back empty (len 0), like every other emptiness guard.
	id2, _ := ts.Create(ctx, orchestrator.Ticket{Title: "fresh", Status: orchestrator.StatusTodo})
	fresh, _ := ts.Get(ctx, id2)
	if len(fresh.AttemptNotes) != 0 || fresh.Disposition != "" {
		t.Fatalf("fresh ticket not empty: notes=%+v disp=%q", fresh.AttemptNotes, fresh.Disposition)
	}

	// Update persists a grown notes list.
	got.AttemptNotes = append(got.AttemptNotes, "answered: mention the demo")
	if err := ts.Update(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	again, _ := ts.Get(ctx, id)
	if len(again.AttemptNotes) != 3 {
		t.Fatalf("updated notes lost: %+v", again.AttemptNotes)
	}
}

func TestPgTicketStoreList(t *testing.T) {
	ctx := context.Background()
	ts := NewPgTicketStore(newTestDB(t))
	_, _ = ts.Create(ctx, orchestrator.Ticket{ID: "a", Title: "A", Status: orchestrator.StatusTodo})
	_, _ = ts.Create(ctx, orchestrator.Ticket{ID: "b", Title: "B", Status: orchestrator.StatusNeedsHuman})
	_, _ = ts.Create(ctx, orchestrator.Ticket{ID: "c", Title: "C", Status: orchestrator.StatusTodo})

	all, err := ts.List(ctx, "")
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("list all = %d, want 3", len(all))
	}
	todo, err := ts.List(ctx, orchestrator.StatusTodo)
	if err != nil {
		t.Fatalf("list todo: %v", err)
	}
	if len(todo) != 2 {
		t.Fatalf("list todo = %d, want 2", len(todo))
	}
	nh, _ := ts.List(ctx, orchestrator.StatusNeedsHuman)
	if len(nh) != 1 || nh[0].ID != "b" {
		t.Fatalf("list needs_human wrong: %+v", nh)
	}
}
