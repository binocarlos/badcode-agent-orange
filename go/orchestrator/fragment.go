package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// RoutingFragmentID is the fragment a ticket:/run: feedback target resolves to
// (contracts §10b S-3): the manager routing-guidance note the learning loop edits
// when the critique is about a work item rather than a named fragment.
const RoutingFragmentID = "routing-guidance"

// WriteFragment is the §6 policy syscall: append an OpUpdate prompt_fragment as a
// new board revision. This is Slice 0's ApplyFeedback board-write, generalised to
// a raw (id, body) write with the coherence guards intact — the one write the
// learning loop, the Consultant, and any policy edit share. It preserves the
// fragment's existing Kind (defaults to FragmentRouting when the fragment is new).
func WriteFragment(ctx context.Context, board agentdb.BoardStore, id, body, author, message string) (string, error) {
	if body == "" {
		return "", fmt.Errorf("write_fragment: empty body (refusing to wipe %q)", id)
	}
	if len(body) > MaxFragmentLen {
		return "", fmt.Errorf("write_fragment: body %d > MaxFragmentLen %d", len(body), MaxFragmentLen)
	}
	kind := string(FragmentRouting)
	if cur, err := board.Current(ctx); err == nil {
		for _, f := range cur.Fragments {
			if f.ID == id && f.Kind != "" {
				kind = f.Kind
				break
			}
		}
	}
	raw, err := json.Marshal(agentdb.BoardPromptFragment{ID: id, Kind: kind, Body: body})
	if err != nil {
		return "", err
	}
	return board.Append(ctx, agentdb.Changeset{
		Author:  author,
		Message: message,
		Ops:     []agentdb.Op{{Op: agentdb.OpUpdate, EntityType: "prompt_fragment", EntityID: id, Body: raw}},
	})
}

// ApplyHumanFeedback routes a (target_ref, note) to a fragment edit — the learning
// loop entry point. §10b S-3 resolution rule: "fragment:<id>" edits that fragment
// directly; "ticket:<id>" / "run:<id>" resolve to the RoutingFragmentID (the
// manager routing-guidance note). It never errors on a ticket/run target.
func ApplyHumanFeedback(ctx context.Context, board agentdb.BoardStore, reviser Model, fb HumanFeedback) (string, error) {
	kind, ref, ok := strings.Cut(fb.TargetRef, ":")
	if !ok {
		return "", fmt.Errorf("feedback: malformed target_ref %q", fb.TargetRef)
	}
	switch kind {
	case "fragment":
		return ApplyFeedback(ctx, board, reviser, ref, fb.Note)
	case "ticket", "run":
		return ApplyFeedback(ctx, board, reviser, RoutingFragmentID, fb.Note)
	default:
		return "", fmt.Errorf("feedback: unknown target kind %q", kind)
	}
}

// HumanFeedbackApplier satisfies the frozen FeedbackApplier seam (§10b S-3) by
// resolving a TargetRef via the S-3 rule and applying the note through the reviser.
type HumanFeedbackApplier struct {
	Board   agentdb.BoardStore
	Reviser Model
}

func (a HumanFeedbackApplier) Apply(ctx context.Context, fb HumanFeedback) (string, error) {
	return ApplyHumanFeedback(ctx, a.Board, a.Reviser, fb)
}

var _ FeedbackApplier = HumanFeedbackApplier{}
