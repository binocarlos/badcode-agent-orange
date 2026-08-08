package agentdb

import (
	"context"
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// Retraction (§7.1): how an append-only store takes something back.
//
// A memory labelled `retracts=<id>` withdraws the memory with that id from every
// SELECTION path — briefings and search — without deleting a row. Before this,
// `retracts` was a label that nothing anywhere consulted: a project could write
// the withdrawal, see it stored, and go on being briefed by the wrong fact for
// ever. These tests exist so that cannot silently return.
//
// Live-Postgres only, like every memory test: the filter is one correlated
// NOT EXISTS in SQL, so a green `go test ./...` with no database proves nothing
// about it. Run with:
//
//	AGENTKIT_TEST_POSTGRES_URL=postgres://... go test ./agentdb/ -run Retract
// ---------------------------------------------------------------------------

// TestRetractionHidesAMemoryFromEverySelectionPath is the whole contract in one
// test: the wrong fact stops reaching briefings (NewestMemory), stops reaching
// search by recency and stops reaching search by relevance — while its row, and
// the retraction that withdrew it, both remain readable.
func TestRetractionHidesAMemoryFromEverySelectionPath(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	p := newLiveProject(t, s)

	wrong := mustCreateMemory(t, s, &Memory{
		Project: p, Content: "The gallery opens at 10am on Sundays.",
		Labels: map[string]string{"kind": "fact", "name": "opening-hours"},
	}, unitVector(1))

	// Before: it is the current value of its name, and it is findable.
	if got, err := s.NewestMemory(ctx, p, "kind=fact,name=opening-hours"); err != nil || got.ID != wrong.ID {
		t.Fatalf("precondition: newest = %v, %v; want the memory we just wrote", got, err)
	}

	retraction := mustCreateMemory(t, s, &Memory{
		Project: p, Content: "Wrong: Sunday opening was never confirmed. Source was a draft flyer.",
		Labels:  map[string]string{"kind": "retraction", RetractionLabel: wrong.ID},
	}, unitVector(2))

	// 1. Briefings. This is the one that matters most — it is the only memory
	//    read core itself performs, once per selector, at job composition.
	if _, err := s.NewestMemory(ctx, p, "kind=fact,name=opening-hours"); !errors.Is(err, ErrMemoryNotFound) {
		t.Errorf("a retracted memory is still the current value of its selector: err = %v, want ErrMemoryNotFound", err)
	}

	// 2. Search by recency (no query text).
	res, err := s.SearchMemories(ctx, &MemorySearchQuery{Project: p, LabelSelector: "kind=fact"})
	if err != nil {
		t.Fatalf("SearchMemories (recency): %v", err)
	}
	for _, r := range res {
		if r.ID == wrong.ID {
			t.Errorf("a retracted memory came back from the recency leg")
		}
	}

	// 3. Search by relevance — a retracted memory must not be able to return by
	//    scoring well, which is why the filter lives in the hard WHERE and not
	//    in a post-filter over the results.
	res, err = s.SearchMemories(ctx, &MemorySearchQuery{
		Project: p, Query: "gallery opening hours Sundays", QueryEmbedding: unitVector(1),
	})
	if err != nil {
		t.Fatalf("SearchMemories (hybrid): %v", err)
	}
	for _, r := range res {
		if r.ID == wrong.ID {
			t.Errorf("a retracted memory came back from the hybrid leg with score %v", r.Score)
		}
	}

	// 4. Nothing was deleted. The row is still there, still attributed, and the
	//    retraction explaining it is still selectable — that is the point of
	//    doing this with a label instead of a DELETE.
	if got, err := s.GetMemory(ctx, p, wrong.ID); err != nil || got.Content == "" {
		t.Errorf("the retracted row must remain readable by id: %v, %v", got, err)
	}
	if got, err := s.GetMemory(ctx, p, retraction.ID); err != nil || got.Labels[RetractionLabel] != wrong.ID {
		t.Errorf("the retraction itself must remain readable and carry its target: %v, %v", got, err)
	}
}

// TestRetractionUncoversTheMemoryBeneathIt is the reason retraction beats
// "write a correction beside it": once the wrong value is withdrawn, the
// selector resolves to the previous good value rather than to nothing. A project
// that mis-learned something returns to what it knew before.
func TestRetractionUncoversTheMemoryBeneathIt(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	p := newLiveProject(t, s)

	sel := "kind=playbook,name=posting-cadence"
	good := mustCreateMemory(t, s, &Memory{
		Project: p, Content: "Post three times a week.",
		Labels:  map[string]string{"kind": "playbook", "name": "posting-cadence"},
	}, unitVector(3))
	bad := mustCreateMemory(t, s, &Memory{
		Project: p, Content: "Post forty times a day.",
		Labels:  map[string]string{"kind": "playbook", "name": "posting-cadence"},
	}, unitVector(3))

	if got, err := s.NewestMemory(ctx, p, sel); err != nil || got.ID != bad.ID {
		t.Fatalf("precondition: newest must be the bad advice: %v, %v", got, err)
	}

	mustCreateMemory(t, s, &Memory{
		Project: p, Content: "Retracting the 40/day cadence: it came from a misread benchmark.",
		Labels:  map[string]string{"kind": "retraction", RetractionLabel: bad.ID},
	}, unitVector(4))

	got, err := s.NewestMemory(ctx, p, sel)
	if err != nil {
		t.Fatalf("NewestMemory after retraction: %v", err)
	}
	if got.ID != good.ID {
		t.Errorf("selector resolved to %q, want the good value that preceded the retracted one (%q)", got.ID, good.ID)
	}
}

// TestRetractionIsProjectLocal pins P5 at the SQL level. The NOT EXISTS clause
// correlates on the row's own project rather than binding one, so a retraction
// written in one project must not be able to withdraw a memory in another — the
// failure mode a shared `retracts` id would otherwise create.
func TestRetractionIsProjectLocal(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	victim := newLiveProject(t, s)
	attacker := newLiveProject(t, s)

	m := mustCreateMemory(t, s, &Memory{
		Project: victim, Content: "Budget is £2,000/month.",
		Labels:  map[string]string{"kind": "fact", "name": "budget"},
	}, unitVector(5))

	// A memory in a DIFFERENT project naming the victim's id.
	mustCreateMemory(t, s, &Memory{
		Project: attacker, Content: "Retract the neighbour's budget.",
		Labels:  map[string]string{"kind": "retraction", RetractionLabel: m.ID},
	}, unitVector(6))

	got, err := s.NewestMemory(ctx, victim, "kind=fact,name=budget")
	if err != nil {
		t.Fatalf("a cross-project retraction withdrew a memory it does not own: %v", err)
	}
	if got.ID != m.ID {
		t.Errorf("newest = %q, want %q", got.ID, m.ID)
	}
}

// TestRetractionOfAnUnknownIdIsInert covers the ordinary mistake: a typo'd or
// already-gone id must withdraw nothing and fail nothing. Retraction is a filter
// over rows that exist, not an assertion that one does.
func TestRetractionOfAnUnknownIdIsInert(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	p := newLiveProject(t, s)

	keep := mustCreateMemory(t, s, &Memory{
		Project: p, Content: "Keep me.", Labels: map[string]string{"kind": "fact"},
	}, unitVector(7))
	mustCreateMemory(t, s, &Memory{
		Project: p, Content: "Retracts nothing that exists.",
		Labels:  map[string]string{"kind": "retraction", RetractionLabel: "no-such-memory-id"},
	}, unitVector(8))

	got, err := s.NewestMemory(ctx, p, "kind=fact")
	if err != nil || got.ID != keep.ID {
		t.Errorf("an inert retraction disturbed the store: %v, %v", got, err)
	}
}
