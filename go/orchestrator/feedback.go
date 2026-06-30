package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// MaxFragmentLen caps a revised fragment body — the coherence guard against
// runaway rewrites (the ACE "context collapse" risk).
const MaxFragmentLen = 4000

const reviserTemplate = `You maintain a short guidance note. Here is the current note:
---
%s
---
A reviewer left this note: %q
Return the full revised guidance (a small delta of the current note, not a rewrite).`

// ApplyFeedback turns a (fragment, note) pair into a delta-edited fragment as a
// new board revision. It is the human-feedback half of the learning loop; the
// Consultant is the same write with the trigger/input swapped (cron + telemetry).
//
// Guards: the fragment must exist; the revised body must be non-empty and within
// MaxFragmentLen (never wipe or overrun a load-bearing guidance fragment).
func ApplyFeedback(ctx context.Context, board agentdb.BoardStore, reviser Model, fragmentID, note string) (string, error) {
	cur, err := board.Current(ctx)
	if err != nil {
		return "", err
	}
	var current string
	var found bool
	for _, f := range cur.Fragments {
		if f.ID == fragmentID {
			current, found = f.Body, true
			break
		}
	}
	if !found {
		return "", fmt.Errorf("feedback: unknown fragment %q", fragmentID)
	}

	revised, err := reviser.Run(ctx, fmt.Sprintf(reviserTemplate, current, note))
	if err != nil {
		return "", fmt.Errorf("feedback: reviser: %w", err)
	}
	if revised == "" {
		return "", fmt.Errorf("feedback: reviser returned empty body (refusing to wipe %q)", fragmentID)
	}
	if len(revised) > MaxFragmentLen {
		return "", fmt.Errorf("feedback: revised body %d > MaxFragmentLen %d", len(revised), MaxFragmentLen)
	}

	body, err := json.Marshal(agentdb.BoardPromptFragment{ID: fragmentID, Kind: "role", Body: revised})
	if err != nil {
		return "", err
	}
	return board.Append(ctx, agentdb.Changeset{
		Author:  "human-feedback",
		Message: note,
		Ops:     []agentdb.Op{{Op: agentdb.OpUpdate, EntityType: "prompt_fragment", EntityID: fragmentID, Body: body}},
	})
}
