package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

const (
	// goalFragmentID is the board fragment holding the top-level goal — durable
	// across restarts and audited in the revision log for free. No prompt
	// template references {{fragment:goal}}, so it never composes into prompts;
	// the goal reaches the plan as {{input}}.
	goalFragmentID = "goal"

	// reportKeep bounds the in-memory tick-report ring served by /api/status.
	reportKeep = 50
)

// tickRecord is one manager tick as the status endpoint reports it.
type tickRecord struct {
	At      time.Time               `json:"at"`
	Skipped bool                    `json:"skipped"` // no goal set → zero model calls
	Report  orchestrator.TickReport `json:"report"`
	Err     string                  `json:"err,omitempty"`
}

type consultantStatus struct {
	LastReviewedSeq int       `json:"last_reviewed_seq"`
	LastAdviceRev   string    `json:"last_advice_revision,omitempty"`
	LastReviewAt    time.Time `json:"last_review_at"`
}

type statusView struct {
	Goal       string           `json:"goal"`
	SpentUSD   float64          `json:"spent_usd"`
	Ticks      []tickRecord     `json:"ticks"` // newest first, ≤ reportKeep
	Consultant consultantStatus `json:"consultant"`
}

// Daemon owns the runtime loop around ONE ManagerExchange: the manager ticker,
// the slower consultant cadence (ARCHITECTURE §6A — the two loops run at
// deliberately different cadences and are never collapsed), manual triggers,
// and goal changes. ManagerExchange.Goal is a plain field read during Tick and
// its mutex is unexported, so d.mu serializes every entry point — cron tick,
// POST /api/trigger, goal set, consultant review.
type Daemon struct {
	mu    sync.Mutex
	ex    *orchestrator.ManagerExchange
	board agentdb.BoardStore
	tel   orchestrator.Telemetry
	meter orchestrator.SpendMeter
	cons  consultantScope

	tickInterval    time.Duration
	consultInterval time.Duration

	goal            string
	reports         []tickRecord
	lastReviewedSeq int
	lastAdviceRev   string
	lastReviewAt    time.Time
	now             func() time.Time
}

// NewDaemon validates the exchange wiring fail-loud: a missing Channel or a
// non-publish default disposition would silently bypass the approval queue.
func NewDaemon(ex *orchestrator.ManagerExchange, board agentdb.BoardStore,
	tel orchestrator.Telemetry, meter orchestrator.SpendMeter, cons consultantScope,
	tick, consult time.Duration) (*Daemon, error) {
	if ex.Channel == "" {
		return nil, fmt.Errorf("oranged: ManagerExchange.Channel must be set (posts need a channel)")
	}
	if ex.DefaultDisposition != orchestrator.DispositionPublish {
		return nil, fmt.Errorf("oranged: DefaultDisposition must be publish (nothing would reach the approval queue)")
	}
	return &Daemon{
		ex: ex, board: board, tel: tel, meter: meter, cons: cons,
		tickInterval: tick, consultInterval: consult, now: time.Now,
	}, nil
}

// Tick runs one manager exchange under the daemon lock. It satisfies
// orchestrator.Triggerer, so watchapi's POST /api/trigger gets the same
// empty-goal guard and its TickReport is captured rather than discarded
// (ExchangeTrigger would drop it).
func (d *Daemon) Tick(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	rec := tickRecord{At: d.now()}
	if d.goal == "" {
		rec.Skipped = true
		d.record(rec)
		log.Printf("tick skipped: no goal set (POST /api/goal, or the goal box in the web UI)")
		return nil
	}
	d.ex.Goal = d.goal
	rep, err := d.ex.Tick(ctx)
	rec.Report = rep
	if err != nil {
		rec.Err = err.Error()
	}
	d.record(rec)
	log.Printf("tick: planned=%d verified=%d done=%d replanned=%d spawned=%d refused=%d err=%v",
		rep.Planned, rep.Verified, rep.Done, rep.RePlanned, rep.Spawned, rep.Refused, err)
	return err
}

var _ orchestrator.Triggerer = (*Daemon)(nil)

func (d *Daemon) record(rec tickRecord) {
	d.reports = append(d.reports, rec)
	if len(d.reports) > reportKeep {
		d.reports = d.reports[len(d.reports)-reportKeep:]
	}
}

// SetGoal persists the goal as the "goal" board fragment (WriteFragment
// enforces non-empty ≤ MaxFragmentLen) and caches it for ticks. Existing
// non-terminal tickets are left to run to a terminal lane — title dedup and
// the plan status appendix keep the next plan incremental; stragglers surface
// at needs_human where the existing UI handles them.
func (d *Daemon) SetGoal(ctx context.Context, goal string) (string, error) {
	goal = strings.TrimSpace(goal)
	d.mu.Lock()
	defer d.mu.Unlock()
	rev, err := orchestrator.WriteFragment(ctx, d.board, goalFragmentID, goal, "human", "set goal")
	if err != nil {
		return "", err
	}
	d.goal = goal
	log.Printf("goal set (rev %s): %s", rev, goal)
	return rev, nil
}

// Goal returns the current goal ("" when none is set).
func (d *Daemon) Goal() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.goal
}

// loadGoal restores the durable goal from the board, falling back to initial
// (the GOAL env) — persisting it — when the board has none.
func (d *Daemon) loadGoal(ctx context.Context, initial string) error {
	if cur, err := d.board.Current(ctx); err == nil {
		for _, f := range cur.Fragments {
			if f.ID == goalFragmentID {
				d.mu.Lock()
				d.goal = f.Body
				d.mu.Unlock()
				log.Printf("goal restored: %s", f.Body)
				return nil
			}
		}
	}
	if strings.TrimSpace(initial) == "" {
		return nil
	}
	_, err := d.SetGoal(ctx, initial)
	return err
}

// Review runs one consultant pass when there is fresh evidence: a run newer
// than the last review, excluding the consultant's own runs (no
// self-triggering). No fresh evidence → skipped, zero model calls.
func (d *Daemon) Review(ctx context.Context) (rev string, advised, skipped bool, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	runs, err := d.tel.Runs(ctx)
	if err != nil {
		return "", false, false, err
	}
	maxSeq, fresh := d.lastReviewedSeq, false
	for _, r := range runs {
		if r.Seq > maxSeq {
			maxSeq = r.Seq
		}
		if r.Seq > d.lastReviewedSeq && r.Scope != "consultant" {
			fresh = true
		}
	}
	if !fresh {
		return "", false, true, nil
	}
	rev, advised, err = d.cons.review(ctx)
	if err != nil {
		return "", false, false, err
	}
	d.lastReviewedSeq = maxSeq
	d.lastReviewAt = d.now()
	if advised {
		d.lastAdviceRev = rev
		log.Printf("consultant advised: revision %s", rev)
	} else {
		log.Printf("consultant reviewed: no change")
	}
	return rev, advised, false, nil
}

// Status snapshots the cockpit view for GET /api/status (ticks newest first).
func (d *Daemon) Status(ctx context.Context) statusView {
	d.mu.Lock()
	defer d.mu.Unlock()
	spent, _ := d.meter.Spent(ctx)
	ticks := make([]tickRecord, 0, len(d.reports))
	for i := len(d.reports) - 1; i >= 0; i-- {
		ticks = append(ticks, d.reports[i])
	}
	return statusView{
		Goal: d.goal, SpentUSD: spent, Ticks: ticks,
		Consultant: consultantStatus{
			LastReviewedSeq: d.lastReviewedSeq,
			LastAdviceRev:   d.lastAdviceRev,
			LastReviewAt:    d.lastReviewAt,
		},
	}
}

// recoverStranded reverts tickets stranded in_progress by a crash mid-tick to
// todo via the engine's own transient-revert transition. The in-proc runtime
// is synchronous inside Tick, so after a restart nothing is genuinely running.
func (d *Daemon) recoverStranded(ctx context.Context) error {
	tickets, err := d.ex.Tickets.List(ctx, orchestrator.StatusInProgress)
	if err != nil {
		return err
	}
	for _, t := range tickets {
		orchestrator.Transition(&t, orchestrator.EvSpawnFailed, "", 0)
		if err := d.ex.Tickets.Update(ctx, t); err != nil {
			return err
		}
		log.Printf("recovered stranded ticket %q → todo", t.Title)
	}
	return nil
}

// Run drives the two cadences until ctx ends. Tick logs its own summary; the
// consultant's skip/advice outcomes are logged here.
func (d *Daemon) Run(ctx context.Context) {
	tick := time.NewTicker(d.tickInterval)
	defer tick.Stop()
	review := time.NewTicker(d.consultInterval)
	defer review.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			_ = d.Tick(ctx) // outcome logged + recorded in the ring
		case <-review.C:
			if _, _, skipped, err := d.Review(ctx); err != nil {
				log.Printf("consultant error: %v", err)
			} else if skipped {
				log.Printf("consultant skipped: no new evidence")
			}
		}
	}
}
