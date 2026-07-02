package orchestrator

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// slowModel widens the check-then-act window (List existing → model call →
// Create) so an unserialized Tick reliably exposes the double-plan/double-spawn
// race instead of passing by timing luck.
type slowModel struct {
	inner Model
	delay time.Duration
}

func (s slowModel) Run(ctx context.Context, prompt string) (string, Usage, error) {
	time.Sleep(s.delay)
	return s.inner.Run(ctx, prompt)
}

// countingRuntime is a goroutine-safe WorkerRuntime that counts Spawn calls per
// ticket id — the double-spawn detector.
type countingRuntime struct {
	mu     sync.Mutex
	seq    int
	spawns map[string]int
}

func (c *countingRuntime) Spawn(_ context.Context, s Scope) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	if c.spawns == nil {
		c.spawns = map[string]int{}
	}
	c.spawns[s.TicketID]++
	return fmt.Sprintf("cs%d", c.seq), nil
}

func (c *countingRuntime) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, v := range c.spawns {
		n += v
	}
	return n
}

// TestConcurrentTickSpawnsExactlyOnce fires N simultaneous Ticks over one
// ManagerExchange whose plan yields exactly one ticket. However the ticks
// interleave, the outcome must be: ONE ticket planned (no duplicate ticket set)
// and ONE worker spawn for it. Run with -race. Before the Tick-serialization fix
// this double-plans and double-spawns (each concurrent tick reads the empty
// board, plans the same ticket, and dispatches it).
func TestConcurrentTickSpawnsExactlyOnce(t *testing.T) {
	ctx := context.Background()
	board := NewMemBoard()
	if _, err := board.Append(ctx, SeedFragment("role-writer", "You are a witty writer.")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tickets := NewMemTickets()
	planJSON := `[{"title":"draft launch post","objective":"write it","acceptance":"witty"}]`
	router := ScriptedRouter{
		TierFull: slowModel{inner: &ScriptedModel{Default: planJSON}, delay: 2 * time.Millisecond},
	}
	rt := &countingRuntime{}
	ledger := NewSpawnLedger()
	budget := Budget{MaxDepth: 3, MaxSpawns: 5, TreeTokens: 100000}
	m := &ManagerExchange{
		Board: board, Tickets: tickets, Router: router, Runtime: rt,
		Ledger: ledger, Telemetry: NewTelemetry(),
		Goal: "launch", ProjectID: "p1", ManagerSession: "mgr",
		PlanTier: TierFull, WorkerTier: TierMid, VerifyTier: TierFull,
		WorkerBudget: budget,
		PlanTemplate: "Plan this goal into tickets as JSON: {{input}}",
		WorkerTemplate: "Task: {{input}}",
		MaxAttempts:  2,
	}

	const n = 8
	start := make(chan struct{})
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = m.Tick(ctx)
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("tick %d errored: %v", i, err)
		}
	}

	all, err := tickets.List(ctx, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	byTitle := map[string]int{}
	for _, tk := range all {
		byTitle[tk.Title]++
	}
	if len(all) != 1 || byTitle["draft launch post"] != 1 {
		t.Fatalf("double-plan: want exactly 1 ticket, got %d (%v)", len(all), byTitle)
	}
	if got := rt.total(); got != 1 {
		t.Fatalf("double-spawn: want exactly 1 worker spawn, got %d (%v)", got, rt.spawns)
	}
}
