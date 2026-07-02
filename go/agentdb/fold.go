package agentdb

import (
	"encoding/json"
	"fmt"
	"sort"
)

// FoldFragments folds an append-only revision log into the board state as of
// targetRevisionID — the ONE fold shared by MemBoard and PgBoard (§10c I-4), so
// the dev and prod stores can never diverge on what the board "is". Revisions are
// folded in ascending Seq order (the input is defensively re-sorted); folding
// stops after the target revision. Ops with entity types other than
// "prompt_fragment" are skipped by the fold — Append rejects them at write time
// (RequireFragmentOps), this skip only keeps historical logs readable. Fragments
// are returned sorted by ID with LastChangedIn stamped to the revision that last
// touched them. An unknown targetRevisionID is an error.
func FoldFragments(revs []BoardRevision, targetRevisionID string) (Board, error) {
	ordered := append([]BoardRevision(nil), revs...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Seq < ordered[j].Seq })

	frags := map[string]BoardPromptFragment{}
	var found bool
	for _, rev := range ordered {
		var ops []Op
		if err := json.Unmarshal(JSONBytes(rev.Ops), &ops); err != nil {
			return Board{}, fmt.Errorf("fold %s: ops: %w", rev.ID, err)
		}
		for _, op := range ops {
			if op.EntityType != "prompt_fragment" {
				continue
			}
			switch op.Op {
			case OpAdd, OpUpdate:
				var f BoardPromptFragment
				if err := json.Unmarshal(op.Body, &f); err != nil {
					return Board{}, fmt.Errorf("fold %s: fragment: %w", rev.ID, err)
				}
				f.LastChangedIn = rev.ID
				frags[op.EntityID] = f
			case OpRemove:
				delete(frags, op.EntityID)
			}
		}
		if rev.ID == targetRevisionID {
			found = true
			break
		}
	}
	if !found {
		return Board{}, fmt.Errorf("revision %q not found", targetRevisionID)
	}
	out := Board{Revision: targetRevisionID}
	for _, f := range frags {
		out.Fragments = append(out.Fragments, f)
	}
	sort.Slice(out.Fragments, func(i, j int) bool { return out.Fragments[i].ID < out.Fragments[j].ID })
	return out, nil
}

// RequireFragmentOps is the fail-loud Append guard (§10c I-4): a changeset
// containing any op whose EntityType is not "prompt_fragment" is rejected with
// an error naming the entity type, instead of being silently discarded at fold
// time. The deferred entity types return post-v1 with the bus.
func RequireFragmentOps(ops []Op) error {
	for _, op := range ops {
		if op.EntityType != "prompt_fragment" {
			return fmt.Errorf("board append: unsupported entity type %q (v1 folds only \"prompt_fragment\")", op.EntityType)
		}
	}
	return nil
}

// OpsToJSON marshals changeset ops into the JSONB column shape (the shared
// helper MemBoard and PgBoard both write revisions through — §10c I-4 dedup).
func OpsToJSON(ops []Op) JSONArray {
	b, _ := json.Marshal(ops)
	return JSONArray(b)
}

// JSONBytes returns j as raw bytes, treating an unset column as the empty JSON
// array so json.Unmarshal never sees zero bytes.
func JSONBytes(j JSONArray) []byte {
	if len(j) == 0 {
		return []byte("[]")
	}
	return []byte(j)
}
