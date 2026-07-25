package agentdb

import (
	"context"
	"testing"
)

// BeforeSeq is the changelog's page cursor. It keys on seq — the only total
// order the log has (J2) — so paging can neither skip nor repeat a record when
// several writes share a millisecond, which a created_at cursor would.
func TestListConfigEvents_BeforeSeqPaginates(t *testing.T) {
	s := newConfigLogTestStore(t)
	ctx := context.Background()

	for _, name := range []string{"w1", "w2", "w3", "w4"} {
		if _, err := s.UpsertWorker(ctx, &Worker{Project: "acme", Name: name}, ConfigWrite{}); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	all, err := s.ListConfigEvents(ctx, ConfigEventQuery{Project: "acme"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("want 4 records, got %d", len(all))
	}
	// Newest first, by seq.
	for i := 1; i < len(all); i++ {
		if all[i-1].Seq <= all[i].Seq {
			t.Fatalf("records are not ordered by seq DESC: %+v", all)
		}
	}

	tests := []struct {
		name      string
		q         ConfigEventQuery
		wantSeqs  []int64
		wantCount int
	}{
		{name: "no cursor is the newest page",
			q: ConfigEventQuery{Project: "acme", Limit: 2}, wantSeqs: []int64{all[0].Seq, all[1].Seq}},
		{name: "cursor continues exactly where the page ended",
			q: ConfigEventQuery{Project: "acme", Limit: 2, BeforeSeq: all[1].Seq}, wantSeqs: []int64{all[2].Seq, all[3].Seq}},
		{name: "cursor is exclusive",
			q: ConfigEventQuery{Project: "acme", BeforeSeq: all[3].Seq}, wantSeqs: nil, wantCount: 0},
		{name: "zero cursor is unbounded",
			q: ConfigEventQuery{Project: "acme", BeforeSeq: 0}, wantCount: 4},
		{name: "cursor composes with a filter",
			q: ConfigEventQuery{Project: "acme", Action: "worker_*", BeforeSeq: all[2].Seq}, wantSeqs: []int64{all[3].Seq}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.ListConfigEvents(ctx, tc.q)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if tc.wantSeqs == nil {
				if len(got) != tc.wantCount {
					t.Fatalf("want %d records, got %d", tc.wantCount, len(got))
				}
				return
			}
			if len(got) != len(tc.wantSeqs) {
				t.Fatalf("want %d records, got %d", len(tc.wantSeqs), len(got))
			}
			for i, want := range tc.wantSeqs {
				if got[i].Seq != want {
					t.Fatalf("record %d: seq %d, want %d", i, got[i].Seq, want)
				}
			}
		})
	}

	// The cursor never crosses a project boundary (P5).
	if _, err := s.UpsertWorker(ctx, &Worker{Project: "other", Name: "w1"}, ConfigWrite{}); err != nil {
		t.Fatalf("seed other project: %v", err)
	}
	got, err := s.ListConfigEvents(ctx, ConfigEventQuery{Project: "other", BeforeSeq: 1000})
	if err != nil {
		t.Fatalf("list other: %v", err)
	}
	if len(got) != 1 || got[0].Project != "other" {
		t.Fatalf("a cursor must not reach another project's log: %+v", got)
	}
}
