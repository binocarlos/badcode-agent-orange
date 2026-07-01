// Package orchestrator is the Agent Orange manager core (Slice 0: the learning
// loop). It sits on the agentdb board log and proves the winning narrative —
// "over time it learned to stop being dumb and got clever" — with an offline,
// deterministic vertical: a manager scope whose behaviour visibly changes after
// a human note, every change versioned and pinned so the story is auditable.
//
// Slice 0 is stdlib-only, in-memory, no DB/containers/network, behind the same
// seams the later Postgres/runtime impls swap into.
package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// MemBoard is an in-memory agentdb.BoardStore: an append-only changeset log
// folded on read. Revision ids are a deterministic counter (r1, r2, …) so the
// learning narrative is reproducible. Slice 0 folds only prompt_fragment ops.
type MemBoard struct {
	mu   sync.Mutex
	revs []agentdb.BoardRevision
}

// NewMemBoard returns an empty in-memory board.
func NewMemBoard() *MemBoard { return &MemBoard{} }

// Append records a changeset as the next revision and moves head to it.
func (m *MemBoard) Append(_ context.Context, cs agentdb.Changeset) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	seq := int64(len(m.revs) + 1)
	id := fmt.Sprintf("r%d", seq)
	m.revs = append(m.revs, agentdb.BoardRevision{
		ID: id, ParentID: cs.ParentID, Seq: seq, Status: "applied",
		Author: cs.Author, Message: cs.Message, Ops: toJSONArray(cs.Ops),
	})
	return id, nil
}

// Current folds the whole log (through head) into the live board state.
func (m *MemBoard) Current(ctx context.Context) (agentdb.Board, error) {
	head, err := m.Head(ctx)
	if err != nil {
		return agentdb.Board{}, err
	}
	return m.AsOf(ctx, head)
}

// AsOf folds the log in seq order up to and including revisionID.
func (m *MemBoard) AsOf(_ context.Context, revisionID string) (agentdb.Board, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ordered := append([]agentdb.BoardRevision(nil), m.revs...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Seq < ordered[j].Seq })

	frags := map[string]agentdb.BoardPromptFragment{}
	var foundTarget bool
	for _, rev := range ordered {
		var ops []agentdb.Op
		if err := json.Unmarshal(jsonArrayBytes(rev.Ops), &ops); err != nil {
			return agentdb.Board{}, fmt.Errorf("fold %s: ops: %w", rev.ID, err)
		}
		for _, op := range ops {
			if op.EntityType != "prompt_fragment" {
				continue
			}
			switch op.Op {
			case agentdb.OpAdd, agentdb.OpUpdate:
				var f agentdb.BoardPromptFragment
				if err := json.Unmarshal(op.Body, &f); err != nil {
					return agentdb.Board{}, fmt.Errorf("fold %s: fragment: %w", rev.ID, err)
				}
				f.LastChangedIn = rev.ID
				frags[op.EntityID] = f
			case agentdb.OpRemove:
				delete(frags, op.EntityID)
			}
		}
		if rev.ID == revisionID {
			foundTarget = true
			break
		}
	}
	if !foundTarget {
		return agentdb.Board{}, fmt.Errorf("revision %q not found", revisionID)
	}
	out := agentdb.Board{Revision: revisionID}
	for _, f := range frags {
		out.Fragments = append(out.Fragments, f)
	}
	sort.Slice(out.Fragments, func(i, j int) bool { return out.Fragments[i].ID < out.Fragments[j].ID })
	return out, nil
}

// Revisions returns a copy of the append-only log in ascending seq order — the
// story timeline the "show your work" surface renders (contracts §10b E-2).
func (m *MemBoard) Revisions(_ context.Context) ([]agentdb.BoardRevision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := append([]agentdb.BoardRevision(nil), m.revs...)
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

// Head returns the most recently appended revision id.
func (m *MemBoard) Head(_ context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.revs) == 0 {
		return "", fmt.Errorf("board empty")
	}
	return m.revs[len(m.revs)-1].ID, nil
}

func toJSONArray(ops []agentdb.Op) agentdb.JSONArray {
	b, _ := json.Marshal(ops)
	return agentdb.JSONArray(b)
}

func jsonArrayBytes(j agentdb.JSONArray) []byte {
	if len(j) == 0 {
		return []byte("[]")
	}
	return []byte(j)
}

// compile-time assertion that MemBoard satisfies the seam.
var _ agentdb.BoardStore = (*MemBoard)(nil)
