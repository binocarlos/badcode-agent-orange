package agentdb

// memories_query_live_test.go — the two query narrowings added 2026-08-10:
// time bounds (Since/Until) and current-value-per-label (LatestPer).
//
// Both are HARD FILTERS, not ranking hints, so every case here asserts against
// BOTH paths through SearchMemories: the recency path (no query text) and the
// hybrid path (query text, RRF over two legs). That duplication is the point —
// the whole design claim is that one `where` string governs every leg, and a
// test that only exercised the recency path would not notice if the CTE lost it.
//
// Run with AGENTKIT_TEST_POSTGRES_URL set; without it these skip.

import (
	"context"
	"strings"
	"testing"
	"time"
)

// atMillis writes a memory with an explicit created_at, because every case here
// is about time and CreateMemory stamps `now` when the field is zero.
func atMillis(t *testing.T, s *Store, project, content string, labels LabelSet, ms int64, emb []float32) *Memory {
	t.Helper()
	return mustCreateMemory(t, s, &Memory{
		Project:   project,
		Labels:    labels,
		Content:   content,
		CreatedAt: ms,
	}, emb)
}

func hasID(res []*MemorySearchResult, id string) bool {
	for _, r := range res {
		if r.ID == id {
			return true
		}
	}
	return false
}

func TestMemorySearchTimeBounds(t *testing.T) {
	s := openLivePG(t)
	project := newLiveProject(t, s)
	ctx := context.Background()

	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC).UnixMilli()
	day := int64(24 * 60 * 60 * 1000)

	old := atMillis(t, s, project, "wombat burrow census", LabelSet{"kind": "fact"}, base-3*day, unitVector(1))
	mid := atMillis(t, s, project, "wombat burrow survey", LabelSet{"kind": "fact"}, base-1*day, unitVector(1))
	recent := atMillis(t, s, project, "wombat burrow report", LabelSet{"kind": "fact"}, base, unitVector(1))

	// Both paths, same expectations: the bounds are a hard filter, so adding
	// query text must not readmit a row the window excluded.
	paths := []struct {
		name  string
		query string
	}{
		{"recency path", ""},
		{"hybrid path", "wombat burrow"},
	}

	for _, path := range paths {
		t.Run(path.name, func(t *testing.T) {
			t.Run("since excludes older rows", func(t *testing.T) {
				res, err := s.SearchMemories(ctx, &MemorySearchQuery{
					Project: project, Query: path.query, Since: base - 2*day,
				})
				if err != nil {
					t.Fatalf("search: %v", err)
				}
				if hasID(res, old.ID) {
					t.Error("a memory older than `since` was returned")
				}
				if !hasID(res, mid.ID) || !hasID(res, recent.ID) {
					t.Errorf("rows inside the window are missing: %v", resultIDs(res))
				}
			})

			t.Run("until excludes newer rows", func(t *testing.T) {
				res, err := s.SearchMemories(ctx, &MemorySearchQuery{
					Project: project, Query: path.query, Until: base - 2*day,
				})
				if err != nil {
					t.Fatalf("search: %v", err)
				}
				if !hasID(res, old.ID) {
					t.Error("the row inside the window is missing")
				}
				if hasID(res, mid.ID) || hasID(res, recent.ID) {
					t.Errorf("rows newer than `until` were returned: %v", resultIDs(res))
				}
			})

			t.Run("both bounds select the middle", func(t *testing.T) {
				res, err := s.SearchMemories(ctx, &MemorySearchQuery{
					Project: project, Query: path.query,
					Since: base - 2*day, Until: base - day/2,
				})
				if err != nil {
					t.Fatalf("search: %v", err)
				}
				if len(res) != 1 || res[0].ID != mid.ID {
					t.Fatalf("want exactly the middle row, got %v", resultIDs(res))
				}
			})

			// The contract says INCLUSIVE at both ends; an off-by-one here would
			// silently drop the boundary row, which is the row a "since my last
			// pass" query is most likely to care about.
			t.Run("bounds are inclusive", func(t *testing.T) {
				res, err := s.SearchMemories(ctx, &MemorySearchQuery{
					Project: project, Query: path.query,
					Since: base, Until: base,
				})
				if err != nil {
					t.Fatalf("search: %v", err)
				}
				if len(res) != 1 || res[0].ID != recent.ID {
					t.Fatalf("an instant window must match the row stamped at that instant, got %v", resultIDs(res))
				}
			})

			t.Run("unbounded is unchanged", func(t *testing.T) {
				res, err := s.SearchMemories(ctx, &MemorySearchQuery{Project: project, Query: path.query})
				if err != nil {
					t.Fatalf("search: %v", err)
				}
				if len(res) != 3 {
					t.Fatalf("want all three rows when no bound is set, got %v", resultIDs(res))
				}
			})
		})
	}
}

func TestMemorySearchTimeBoundsRefuseAnInvertedRange(t *testing.T) {
	s := openLivePG(t)
	project := newLiveProject(t, s)

	_, err := s.SearchMemories(context.Background(), &MemorySearchQuery{
		Project: project, Since: 200, Until: 100,
	})
	if err == nil {
		t.Fatal("an inverted range must be refused rather than silently returning nothing")
	}
	for _, want := range []string{"200", "100", "matches nothing"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q: %v", want, err)
		}
	}
}

func TestMemorySearchLatestPer(t *testing.T) {
	s := openLivePG(t)
	project := newLiveProject(t, s)
	ctx := context.Background()

	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC).UnixMilli()
	min := int64(60 * 1000)

	// Two names, three versions, plus a row with no `name` at all.
	atMillis(t, s, project, "campaign alpha status: draft", LabelSet{"kind": "status", "name": "alpha"}, base, unitVector(2))
	alphaNew := atMillis(t, s, project, "campaign alpha status: live", LabelSet{"kind": "status", "name": "alpha"}, base+min, unitVector(2))
	beta := atMillis(t, s, project, "campaign beta status: paused", LabelSet{"kind": "status", "name": "beta"}, base, unitVector(2))
	keyless := atMillis(t, s, project, "campaign notes with no name label", LabelSet{"kind": "status"}, base+2*min, unitVector(2))

	paths := []struct {
		name  string
		query string
	}{
		{"recency path", ""},
		{"hybrid path", "campaign status"},
	}

	for _, path := range paths {
		t.Run(path.name, func(t *testing.T) {
			res, err := s.SearchMemories(ctx, &MemorySearchQuery{
				Project: project, Query: path.query,
				LabelSelector: "kind=status", LatestPer: "name",
			})
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			if len(res) != 2 {
				t.Fatalf("want one row per distinct name, got %d: %v", len(res), resultIDs(res))
			}
			if !hasID(res, alphaNew.ID) {
				t.Error("the NEWEST alpha row must win")
			}
			if !hasID(res, beta.ID) {
				t.Error("the only beta row is missing")
			}
			// The keyless row is the trap: labels->>'name' is NULL for it, and
			// DISTINCT ON groups NULLs together, so without an explicit
			// existence clause exactly one keyless row survives.
			if hasID(res, keyless.ID) {
				t.Error("a memory without the label key must be excluded, not grouped under NULL")
			}
		})
	}

	t.Run("recency path orders newest-first after reduction", func(t *testing.T) {
		res, err := s.SearchMemories(ctx, &MemorySearchQuery{
			Project: project, LabelSelector: "kind=status", LatestPer: "name",
		})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		// alphaNew (base+1m) is newer than beta (base): output order must be
		// by created_at, NOT by the label value the reduction ordered on.
		if len(res) != 2 || res[0].ID != alphaNew.ID {
			t.Fatalf("want newest-first after reduction, got %v", resultIDs(res))
		}
	})

	t.Run("composes with time bounds", func(t *testing.T) {
		res, err := s.SearchMemories(ctx, &MemorySearchQuery{
			Project: project, LabelSelector: "kind=status", LatestPer: "name",
			Until: base, // hides alphaNew, so the older alpha becomes current
		})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if len(res) != 2 {
			t.Fatalf("want two rows, got %v", resultIDs(res))
		}
		if hasID(res, alphaNew.ID) {
			t.Error("a row outside the window must not be the current value")
		}
	})

	t.Run("an invalid label key is refused", func(t *testing.T) {
		_, err := s.SearchMemories(ctx, &MemorySearchQuery{
			Project: project, LatestPer: "not a valid key!",
		})
		if err == nil {
			t.Fatal("an invalid label key must be refused rather than interpolated")
		}
	})
}
