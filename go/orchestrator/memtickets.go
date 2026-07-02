package orchestrator

import (
	"context"
	"fmt"
	"sync"
)

// MemTickets is an in-memory TicketStore (contracts §5) with deterministic ids
// (t1, t2, …). It is the Slice-A stand-in the manager tests run against; the
// Postgres impl satisfies the same seam. Create ignores any inbound ID and assigns
// the next counter; List("") returns all in creation order.
type MemTickets struct {
	mu    sync.Mutex
	seq   int
	byID  map[string]Ticket
	order []string
}

// NewMemTickets returns an empty ticket store.
func NewMemTickets() *MemTickets {
	return &MemTickets{byID: map[string]Ticket{}}
}

func (m *MemTickets) Create(_ context.Context, t Ticket) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	t.ID = fmt.Sprintf("t%d", m.seq)
	m.byID[t.ID] = t
	m.order = append(m.order, t.ID)
	return t.ID, nil
}

func (m *MemTickets) Update(_ context.Context, t Ticket) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.byID[t.ID]; !ok {
		return fmt.Errorf("tickets: update unknown id %q", t.ID)
	}
	m.byID[t.ID] = t
	return nil
}

func (m *MemTickets) Get(_ context.Context, id string) (Ticket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.byID[id]
	if !ok {
		return Ticket{}, fmt.Errorf("tickets: unknown id %q", id)
	}
	return t, nil
}

func (m *MemTickets) List(_ context.Context, status TicketStatus) ([]Ticket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Ticket
	for _, id := range m.order {
		t := m.byID[id]
		if status == "" || t.Status == status {
			out = append(out, t)
		}
	}
	return out, nil
}

var _ TicketStore = (*MemTickets)(nil)
