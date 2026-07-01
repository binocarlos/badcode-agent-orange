package pgstore

import (
	"context"
	"encoding/json"
	"testing"

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
