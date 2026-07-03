package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// ── The consultant, composed from primitives ─────────────────────────────────
//
// A consultant is deliberately NOT an engine primitive — there is no
// consultant.go. It is one way of wiring what already exists: read the evidence
// (ticket outcomes + the telemetry run log), run a Model over it, and apply any
// advice through the one policy syscall, WriteFragment — the same write a human
// note takes (feedback.go), with the trigger/input swapped. These tests invoke
// it directly after a goal settles; when thread-completion subscriptions exist,
// the SAME composition hangs off that trigger instead.

const consultantTemplate = `You are a consultant reviewing how an AI manager and its workers handled
recent work. Your ONLY lever is the manager's standing guidance note, which is
composed into the manager's prompt on FUTURE work — you cannot change past work.
%s
CURRENT GUIDANCE NOTE:
---
%s
---
WHAT HAPPENED (the evidence):
%s
If the guidance should change, reply with "ADVISE" as the FIRST line, followed
by the FULL revised guidance note (a small delta of the current note, not a
rewrite; under %d characters). If no change is needed, reply with exactly "OK".`

// consultantScope wires the primitives into the consultant role for these tests.
type consultantScope struct {
	Board   agentdb.BoardStore
	Model   Model
	Tickets TicketStore
	Tel     Telemetry
	Charter string // standing instruction: what the consultant looks for
}

// review runs one consultant pass over the evidence. An "OK" or unparseable
// reply is conservative (no edit, same posture as parseVerdict); either way the
// run is recorded, pinned to the board revision it read, so non-edits stay
// auditable.
func (c consultantScope) review(ctx context.Context) (revisionID string, advised bool, err error) {
	var ev strings.Builder
	if c.Tickets != nil {
		tickets, err := c.Tickets.List(ctx, "")
		if err != nil {
			return "", false, err
		}
		for _, t := range tickets {
			fmt.Fprintf(&ev, "- ticket %q: status=%s attempts=%d\n", t.Title, t.Status, t.Attempts)
			for _, n := range t.AttemptNotes {
				fmt.Fprintf(&ev, "    note: %s\n", n)
			}
		}
	}
	runs, err := c.Tel.Runs(ctx)
	if err != nil {
		return "", false, err
	}
	if len(runs) > 20 {
		runs = runs[len(runs)-20:] // evidence digest, not a transcript
	}
	for _, r := range runs {
		out := r.Output
		if len(out) > 240 {
			out = out[:240] + "…"
		}
		fmt.Fprintf(&ev, "- run %s [%s]: %s\n", r.ID, r.Scope, out)
	}

	guidance, boardRev := "(no guidance note exists yet)", ""
	if cur, err := c.Board.Current(ctx); err == nil {
		boardRev = cur.Revision
		for _, f := range cur.Fragments {
			if f.ID == RoutingFragmentID {
				guidance = f.Body
			}
		}
	}
	charter := ""
	if c.Charter != "" {
		charter = "\nYOUR CHARTER (what to look for):\n" + c.Charter + "\n"
	}
	prompt := fmt.Sprintf(consultantTemplate, charter, guidance, ev.String(), MaxFragmentLen)
	out, _, err := c.Model.Run(ctx, prompt)
	if err != nil {
		return "", false, err
	}
	if _, err := c.Tel.Record(ctx, Run{
		Scope: "consultant", BoardRevision: boardRev, Prompt: prompt, Output: out,
	}); err != nil {
		return "", false, err
	}
	body, ok := parseAdvice(out)
	if !ok {
		return "", false, nil
	}
	rev, err := WriteFragment(ctx, c.Board, RoutingFragmentID, body, "consultant", "consultant review")
	if err != nil {
		return "", false, err
	}
	return rev, true, nil
}

// parseAdvice reads the structured first line: "ADVISE" → the remainder (same
// line after an optional ":", plus all following lines) is the full revised
// body; "OK" or anything else → no change. ADVISE with an empty body is
// unparseable, not a wipe (WriteFragment would refuse it anyway).
func parseAdvice(out string) (body string, advised bool) {
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !strings.HasPrefix(strings.ToUpper(trimmed), "ADVISE") {
			return "", false
		}
		rest := strings.TrimSpace(trimmed[len("ADVISE"):])
		rest = strings.TrimSpace(strings.TrimPrefix(rest, ":"))
		body = strings.TrimSpace(rest + "\n" + strings.Join(lines[i+1:], "\n"))
		return body, body != ""
	}
	return "", false
}

func TestParseAdvice(t *testing.T) {
	cases := []struct {
		name, in, body string
		advised        bool
	}{
		{"first-line then body", "ADVISE\nBe cleverer.", "Be cleverer.", true},
		{"inline colon", "ADVISE: Be cleverer.", "Be cleverer.", true},
		{"lowercase", "advise\nBe cleverer.", "Be cleverer.", true},
		{"leading blank lines", "\n  \nADVISE\nBe cleverer.", "Be cleverer.", true},
		{"multi-line body", "ADVISE\nline one\nline two", "line one\nline two", true},
		{"ok", "OK", "", false},
		{"ok trailing", "OK\n", "", false},
		{"prose", "The guidance seems fine to me.", "", false},
		{"advise with empty body", "ADVISE\n\n", "", false},
		{"empty", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, advised := parseAdvice(tc.in)
			if advised != tc.advised || body != tc.body {
				t.Fatalf("parseAdvice(%q) = (%q, %v), want (%q, %v)", tc.in, body, advised, tc.body, tc.advised)
			}
		})
	}
}

// ── The end-to-end learning narrative ────────────────────────────────────────

// TestConsultantLearningLoopE2E is the across-goal learning narrative, end to
// end and fully offline: an event comes in, the manager dispatches per its
// standing policy, the work fails, the consultant mines the evidence and
// revises the policy through the board, and the NEXT event is dispatched
// differently — with every step pinned to a board revision so the causality is
// auditable. Its live-model sibling (same beats, real Anthropic API) is
// TestLiveConsultantLearningLoop in live_e2e_test.go.
//
// The scripted models are keyed so the improved behaviour is UNREACHABLE
// without the loop: the specialist plan fires only when the composed plan
// prompt carries the consultant's advice text, which only exists on the board
// after the consultant's revision.
func TestConsultantLearningLoopE2E(t *testing.T) {
	ctx := context.Background()

	// ── The shared substrate: one board (policy), one telemetry log (evidence).
	board := NewMemBoard()
	seedRev, err := WriteFragment(ctx, board, RoutingFragmentID,
		"Dispatch policy: assign every incident to the web-generalist.",
		"human", "seed dispatch policy")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	tel := NewTelemetry()

	const advice = "Dispatch policy: assign every incident to the web-generalist, " +
		"EXCEPT route database incidents to the database-specialist."
	planA := `[{"title":"web-generalist: restart the checkout service","objective":"web-generalist: investigate and fix the checkout outage","acceptance":"the checkout outage is resolved"}]`
	planB := `[{"title":"database-specialist: repair the orders database","objective":"database-specialist: repair the orders database behind checkout","acceptance":"the checkout outage is resolved"}]`

	router := ScriptedRouter{
		TierFull: &ScriptedModel{ // plan AND verify share the full tier, keyed by prompt text
			Default: "FAIL: unexpected prompt",
			Rules: []Rule{
				// The learned plan: fires ONLY when the composed prompt carries the
				// consultant's advice — impossible before the board revision lands.
				{Contains: "route database incidents to the database-specialist", Reply: planB},
				{Contains: "Assign the incident", Reply: planA}, // the policy-as-seeded plan
				{Contains: "the outage persists", Reply: "FAIL: the web-generalist could not fix a database fault"},
				{Contains: "database repaired", Reply: "PASS: outage resolved"},
			},
		},
		TierMid: &ScriptedModel{ // the workers, keyed by which specialist the objective names
			Default: "restarted the web service; the outage persists",
			Rules:   []Rule{{Contains: "database-specialist:", Reply: "database repaired; checkout restored"}},
		},
	}

	newExchange := func(session, goal string, tickets *MemTickets) *ManagerExchange {
		ledger := NewSpawnLedger()
		budget := Budget{MaxDepth: 3, MaxSpawns: 5, TreeTokens: 100000}
		return &ManagerExchange{
			Board: board, Tickets: tickets, Router: router,
			Runtime: &InProcRuntime{Board: board, Router: router,
				Sink: &TicketResultSink{Tickets: tickets}, Ledger: ledger, Telemetry: tel},
			Ledger: ledger, Telemetry: tel,
			Goal: goal, ProjectID: "p1", ManagerSession: session,
			PlanTier: TierFull, WorkerTier: TierMid, VerifyTier: TierFull,
			WorkerBudget:   budget,
			PlanTemplate:   "{{fragment:routing-guidance}}\nAssign the incident to ONE specialist and plan tickets as JSON: {{input}}",
			WorkerTemplate: "You are the assigned specialist. Task: {{input}}",
			MaxAttempts:    1, // one shot per event: a failure goes straight to needs_human
		}
	}

	// ── EVENT 1: a database incident arrives; policy says web-generalist.
	tickets1 := NewMemTickets()
	ex1 := newExchange("mgr1", "INCIDENT: checkout is down; database timeouts suspected", tickets1)

	rep, err := ex1.Tick(ctx) // plan → spawn generalist → draft lands In-Review
	if err != nil {
		t.Fatalf("event1 tick1: %v", err)
	}
	if rep.Planned != 1 || rep.Spawned != 1 {
		t.Fatalf("event1 tick1 report: %+v", rep)
	}
	if _, err := ex1.Tick(ctx); err != nil { // verify FAIL → at cap → needs_human
		t.Fatalf("event1 tick2: %v", err)
	}
	e1, _ := tickets1.List(ctx, "")
	if !strings.HasPrefix(e1[0].Title, "web-generalist:") {
		t.Fatalf("event1 must be dispatched per the seeded policy: %q", e1[0].Title)
	}
	if e1[0].Status != StatusNeedsHuman || len(e1[0].AttemptNotes) != 1 {
		t.Fatalf("event1 must end needs_human with the verify Reason: %+v", e1[0])
	}

	// ── THE CONSULTANT: reacts to the settled thread, mines the evidence, and
	// revises the dispatch policy — all through existing primitives.
	consultant := consultantScope{
		Board: board, Tickets: tickets1, Tel: tel,
		// The advising reply is keyed on the failing evidence — the consultant
		// cannot advise without having read what happened.
		Model: &ScriptedModel{Default: "OK",
			Rules: []Rule{{Contains: "could not fix a database fault", Reply: "ADVISE\n" + advice}}},
	}
	adviceRev, advised, err := consultant.review(ctx)
	if err != nil || !advised {
		t.Fatalf("consultant: advised=%v err=%v", advised, err)
	}
	if adviceRev == seedRev {
		t.Fatalf("advice must be a NEW board revision")
	}
	cur, _ := board.Current(ctx)
	if cur.Fragments[0].Body != advice {
		t.Fatalf("policy not revised: %q", cur.Fragments[0].Body)
	}
	if cur.Fragments[0].Kind != string(FragmentRouting) {
		t.Fatalf("fragment kind not preserved: %q", cur.Fragments[0].Kind)
	}

	// ── EVENT 2: a similar incident arrives; the manager now acts differently.
	tickets2 := NewMemTickets()
	ex2 := newExchange("mgr2", "INCIDENT: orders database throwing errors", tickets2)

	if _, err := ex2.Tick(ctx); err != nil { // plan (advised) → spawn specialist
		t.Fatalf("event2 tick1: %v", err)
	}
	if _, err := ex2.Tick(ctx); err != nil { // verify PASS → done
		t.Fatalf("event2 tick2: %v", err)
	}
	e2, _ := tickets2.List(ctx, "")
	if !strings.HasPrefix(e2[0].Title, "database-specialist:") {
		t.Fatalf("event2 must be dispatched per the ADVISED policy: %q", e2[0].Title)
	}
	if e2[0].Status != StatusDone {
		t.Fatalf("event2 must succeed under the new policy: %+v", e2[0])
	}

	// ── AUDITABILITY: each plan run is pinned to the board revision it composed
	// from, so the before/after has a recorded cause: seed → advice.
	runs, _ := tel.Runs(ctx)
	var plans []Run
	for _, r := range runs {
		if r.Scope == "manager-plan" {
			plans = append(plans, r)
		}
	}
	first, last := plans[0], plans[len(plans)-1]
	if first.BoardRevision != seedRev || last.BoardRevision != adviceRev {
		t.Fatalf("plan pins wrong: first=%s last=%s seed=%s advice=%s",
			first.BoardRevision, last.BoardRevision, seedRev, adviceRev)
	}
	if strings.Contains(first.Prompt, advice) || !strings.Contains(last.Prompt, advice) {
		t.Fatalf("advice must reach event2's plan prompt and ONLY event2's")
	}
}

// The conservative posture: an "OK" (or unparseable) consultant reply must
// leave the board untouched — the consultant never edits load-bearing guidance
// it was not clearly told to — while the recorded run keeps the non-edit
// auditable.
func TestConsultantOKLeavesBoardUntouched(t *testing.T) {
	ctx := context.Background()
	board := NewMemBoard()
	seedRev, err := WriteFragment(ctx, board, RoutingFragmentID,
		"Assign every incident to the web-generalist.", "human", "seed dispatch policy")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	tel := NewTelemetry()
	_, _ = tel.Record(ctx, Run{Scope: "worker", Output: "the outage persists"})

	for _, reply := range []string{"OK", "Well, the manager could consider a few things..."} {
		c := consultantScope{Board: board, Tel: tel, Model: &ScriptedModel{Default: reply}}
		rev, advised, err := c.review(ctx)
		if err != nil || advised || rev != "" {
			t.Fatalf("reply %q must not edit: rev=%q advised=%v err=%v", reply, rev, advised, err)
		}
		if head, _ := board.Head(ctx); head != seedRev {
			t.Fatalf("board moved on reply %q: head=%q seed=%q", reply, head, seedRev)
		}
	}
	runs, _ := tel.Runs(ctx)
	if runs[len(runs)-1].Scope != "consultant" {
		t.Fatalf("consultant runs not recorded: %+v", runs)
	}
}
