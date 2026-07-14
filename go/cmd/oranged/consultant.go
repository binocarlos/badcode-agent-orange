package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

// The consultant is deliberately NOT an engine primitive — there is no
// consultant.go in the orchestrator package. It is one way of wiring what
// already exists: read the evidence (ticket outcomes + the telemetry run log),
// run a Model over it, and apply any advice through the one policy syscall,
// WriteFragment — the same write a human note takes, with the trigger/input
// swapped. This composition is replicated from the reference in
// go/orchestrator/learning_e2e_test.go (consultantScope), where the loop is
// proven offline and live.

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

// consultantScope wires the primitives into the consultant role.
type consultantScope struct {
	Board   agentdb.BoardStore
	Model   orchestrator.Model
	Tickets orchestrator.TicketStore
	Tel     orchestrator.Telemetry
	Charter string // standing instruction: what the consultant looks for
}

// review runs one consultant pass over the evidence. An "OK" or unparseable
// reply is conservative (no edit); either way the run is recorded, pinned to
// the board revision it read, so non-edits stay auditable.
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
			if f.ID == orchestrator.RoutingFragmentID {
				guidance = f.Body
			}
		}
	}
	charter := ""
	if c.Charter != "" {
		charter = "\nYOUR CHARTER (what to look for):\n" + c.Charter + "\n"
	}
	prompt := fmt.Sprintf(consultantTemplate, charter, guidance, ev.String(), orchestrator.MaxFragmentLen)
	out, _, err := c.Model.Run(ctx, prompt)
	if err != nil {
		return "", false, err
	}
	if _, err := c.Tel.Record(ctx, orchestrator.Run{
		Scope: "consultant", BoardRevision: boardRev, Prompt: prompt, Output: out,
	}); err != nil {
		return "", false, err
	}
	body, ok := parseAdvice(out)
	if !ok {
		return "", false, nil
	}
	rev, err := orchestrator.WriteFragment(ctx, c.Board,
		orchestrator.RoutingFragmentID, body, "consultant", "consultant review")
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
