package orchestrator

import (
	"context"
	"testing"
)

func TestMemTicketsCRUDAndList(t *testing.T) {
	ctx := context.Background()
	ts := NewMemTickets()

	id1, err := ts.Create(ctx, Ticket{Title: "draft post", Objective: "write it", Status: StatusTodo})
	if err != nil || id1 != "t1" {
		t.Fatalf("create t1: id=%q err=%v", id1, err)
	}
	id2, _ := ts.Create(ctx, Ticket{Title: "review post", Status: StatusBacklog})
	if id2 != "t2" {
		t.Fatalf("create t2: id=%q", id2)
	}

	got, err := ts.Get(ctx, "t1")
	if err != nil || got.Title != "draft post" {
		t.Fatalf("get t1: %+v err=%v", got, err)
	}

	got.Status = StatusInReview
	if err := ts.Update(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	if again, _ := ts.Get(ctx, "t1"); again.Status != StatusInReview {
		t.Fatalf("update not persisted: %s", again.Status)
	}

	all, _ := ts.List(ctx, "")
	if len(all) != 2 || all[0].ID != "t1" || all[1].ID != "t2" {
		t.Fatalf("list all wrong: %+v", all)
	}
	backlog, _ := ts.List(ctx, StatusBacklog)
	if len(backlog) != 1 || backlog[0].ID != "t2" {
		t.Fatalf("list backlog wrong: %+v", backlog)
	}

	if _, err := ts.Get(ctx, "nope"); err == nil {
		t.Fatalf("expected error on unknown id")
	}
	if err := ts.Update(ctx, Ticket{ID: "nope"}); err == nil {
		t.Fatalf("expected error updating unknown id")
	}
}
