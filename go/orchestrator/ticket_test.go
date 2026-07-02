package orchestrator

import (
	"context"
	"encoding/json"
	"testing"
)

func TestTicketStatusVocabulary(t *testing.T) {
	cases := map[TicketStatus]string{
		StatusBacklog:    "backlog",
		StatusTodo:       "todo",
		StatusInProgress: "in_progress",
		StatusInReview:   "in_review",
		StatusDone:       "done",
		StatusBlocked:    "blocked",
		StatusNeedsHuman: "needs_human",
	}
	for got, want := range cases {
		if string(got) != want {
			t.Fatalf("status %q != %q", got, want)
		}
	}
}

func TestTicketJSONRoundTrip(t *testing.T) {
	in := Ticket{
		ID: "t1", ProjectID: "badcode", Title: "Draft launch post",
		Objective: "write a post about X", Acceptance: "on-brand, <=280 chars",
		Status: StatusTodo, Scope: json.RawMessage(`{"name":"post-writer"}`),
		PublishedRef: "https://x.example/1",
		DependsOn:    []string{"t0"}, Parent: "", Attempts: 1, BoardRev: "r3",
		CreatedAt: 100, UpdatedAt: 200,
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Ticket
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.ID != "t1" || out.Status != StatusTodo || len(out.DependsOn) != 1 || out.BoardRev != "r3" {
		t.Fatalf("round-trip wrong: %+v", out)
	}
	if out.PublishedRef != "https://x.example/1" {
		t.Fatalf("published_ref lost in round-trip: %+v", out)
	}
}

// nopTicketStore proves TicketStore is implementable (compile-time only).
type nopTicketStore struct{}

func (nopTicketStore) Create(context.Context, Ticket) (string, error)       { return "", nil }
func (nopTicketStore) Update(context.Context, Ticket) error                 { return nil }
func (nopTicketStore) Get(context.Context, string) (Ticket, error)          { return Ticket{}, nil }
func (nopTicketStore) List(context.Context, TicketStatus) ([]Ticket, error) { return nil, nil }

var _ TicketStore = nopTicketStore{}
