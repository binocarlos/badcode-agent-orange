package main

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// RD10 against a real Postgres, with real concurrency.
//
// This is the test the finding is actually about, and it is the one most easily
// made vacuous. `DrainPending` runs from TWO goroutines in one agentd process —
// the router loop every three seconds (router.go) and the scheduler loop
// (scheduler.go) — and until this fix the pending→running transition was a
// read-then-Save with no compare-and-set. Both drains could read the same
// `pending` row, both write `running`, and both start a job: the user's worker
// runs twice. For the first production use case (a marketing worker) that means
// the same outbound action happens twice.
//
// So the test uses N REAL goroutines against a REAL database. A fake store
// cannot prove this: the defect lives in the gap between a read and a write
// that only a database can close.
//
//	AGENTKIT_TEST_POSTGRES_URL=postgres://... go test ./cmd/agentd/ -run LivePG_.*Dispatch
// ---------------------------------------------------------------------------

// countingStarter records how many jobs were actually started, safely under
// concurrency. It is the only thing in this file that is a fake, and it is the
// thing being counted rather than the thing being tested.
type countingStarter struct {
	mu      sync.Mutex
	started int
}

func (c *countingStarter) StartJob(ctx context.Context, in startJobInput) (string, error) {
	c.mu.Lock()
	c.started++
	c.mu.Unlock()
	sessionID := uuid.New().String()
	if in.OnSessionCreated != nil {
		if err := in.OnSessionCreated(ctx, sessionID); err != nil {
			return "", err
		}
	}
	return sessionID, nil
}

// livePendingDelivery seeds a project with one enabled worker, one event and one
// `pending` delivery pointing at both — the exact row both loops would find.
// Capacity is set generously on purpose: a per-worker or per-project cap that
// happened to block the second dispatcher would hide the race behind a
// different mechanism, and the claim is what is under test.
func livePendingDelivery(t *testing.T, s *agentdb.Store) (project string, delivery *agentdb.EventDelivery) {
	t.Helper()
	ctx := context.Background()
	project = "proj-" + uuid.New().String()

	if _, err := s.UpsertWorker(ctx, &agentdb.Worker{
		Project:      project,
		Name:         "racer",
		SystemPrompt: "you are a worker",
		MaxInstances: 50,
		Enabled:      true,
	}, agentdb.ConfigWrite{Rationale: "test fixture"}); err != nil {
		t.Fatalf("upsert worker: %v", err)
	}
	ev, err := s.CreateProjectEvent(ctx, &agentdb.ProjectEvent{
		Project:  project,
		Type:     "test.fired",
		Envelope: agentdb.EventEnvelope{Source: agentdb.EventSourceExternal},
	})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	d, _, err := s.EnsureDelivery(ctx, &agentdb.EventDelivery{
		Project:        project,
		EventID:        ev.ID,
		SubscriptionID: uuid.New().String(),
		Worker:         "racer",
		Status:         agentdb.DeliveryPending,
	})
	if err != nil {
		t.Fatalf("ensure delivery: %v", err)
	}

	t.Cleanup(func() {
		db := s.DB()
		_ = db.Exec("DELETE FROM event_deliveries WHERE project = ?", project).Error
		_ = db.Exec("DELETE FROM project_events WHERE project = ?", project).Error
		_ = db.Exec("DELETE FROM workers WHERE project = ?", project).Error
		_ = db.Exec("DELETE FROM config_events WHERE project = ?", project).Error
	})
	return project, d
}

// TestLivePG_ConcurrentDrainsDispatchOnce is RD10's headline: however many
// drains race over one pending delivery, exactly one job may start.
func TestLivePG_ConcurrentDrainsDispatchOnce(t *testing.T) {
	store := openLiveStore(t)
	ctx := context.Background()
	project, delivery := livePendingDelivery(t, store)

	starter := &countingStarter{}
	gate := newDispatcher(dispatcherConfig{
		Store:        store,
		Starter:      starter,
		DefaultImage: "agentkit-example:dev",
		Logf:         func(string, ...any) {},
	})

	// A start barrier, so every goroutine is inside the read-then-write window
	// at the same time rather than politely queueing behind the first.
	const racers = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := gate.DrainPending(ctx, project); err != nil {
				t.Errorf("drain: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if starter.started != 1 {
		t.Fatalf("%d drains over ONE pending delivery started %d jobs — a user's job ran %d times",
			racers, starter.started, starter.started)
	}
	got, err := store.ListDeliveries(ctx, agentdb.DeliveryQuery{Project: project})
	if err != nil {
		t.Fatalf("list deliveries: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly one delivery row, got %d", len(got))
	}
	if got[0].Status != agentdb.DeliveryRunning {
		t.Fatalf("the winning delivery must be running, got %q", got[0].Status)
	}
	if got[0].SessionID == "" {
		t.Fatalf("the winning delivery must carry the session id of the job that ran")
	}
	if got[0].ID != delivery.ID {
		t.Fatalf("delivery id changed under us: %s → %s", delivery.ID, got[0].ID)
	}
}

// TestLivePG_ClaimDeliveryHasExactlyOneWinner isolates the primitive itself, so
// a later refactor of the gate cannot quietly remove the guarantee the gate
// rests on.
func TestLivePG_ClaimDeliveryHasExactlyOneWinner(t *testing.T) {
	store := openLiveStore(t)
	ctx := context.Background()
	project, delivery := livePendingDelivery(t, store)

	const racers = 16
	start := make(chan struct{})
	var mu sync.Mutex
	won, lost, other := 0, 0, []error{}
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := store.ClaimDelivery(ctx, project, delivery.ID)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				won++
			case errors.Is(err, agentdb.ErrDeliveryNotPending):
				lost++
			default:
				other = append(other, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if len(other) > 0 {
		t.Fatalf("unexpected errors from ClaimDelivery: %v", other)
	}
	if won != 1 || lost != racers-1 {
		t.Fatalf("want exactly 1 winner and %d losers, got %d and %d", racers-1, won, lost)
	}

	// The winner's row is running and stamped; the losers wrote nothing.
	got, err := store.ListDeliveries(ctx, agentdb.DeliveryQuery{Project: project})
	if err != nil {
		t.Fatalf("list deliveries: %v", err)
	}
	if got[0].Status != agentdb.DeliveryRunning || got[0].StartedAt == 0 {
		t.Fatalf("claimed row must be running with started_at set, got %q / %d", got[0].Status, got[0].StartedAt)
	}
}

// TestLivePG_ClaimingATerminalDeliveryIsRefused: the claim is a transition FROM
// pending, not an assignment. A delivery already closed must never be dragged
// back to running — that is how a finished job reappears as live work and eats
// a capacity slot again.
func TestLivePG_ClaimingATerminalDeliveryIsRefused(t *testing.T) {
	store := openLiveStore(t)
	ctx := context.Background()
	project, delivery := livePendingDelivery(t, store)

	if _, err := store.UpdateDeliveryStatus(ctx, project, delivery.ID, agentdb.DeliveryStatusUpdate{
		Status: agentdb.DeliveryOK,
	}); err != nil {
		t.Fatalf("close delivery: %v", err)
	}
	if _, err := store.ClaimDelivery(ctx, project, delivery.ID); !errors.Is(err, agentdb.ErrDeliveryNotPending) {
		t.Fatalf("claiming an `ok` delivery must be refused, got %v", err)
	}
	if _, err := store.ClaimDelivery(ctx, project, uuid.New().String()); !errors.Is(err, agentdb.ErrDeliveryNotPending) {
		t.Fatalf("claiming a delivery that does not exist must be refused, got %v", err)
	}
}
