package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestReportMemoryVectorColumn: the absent-extension case must be audible at
// boot. Before RD3 the first sign of it was a search that came back quietly
// keyword-only weeks later — nothing was logged where anyone looks.
func TestReportMemoryVectorColumn(t *testing.T) {
	tests := []struct {
		name       string
		ok         bool
		err        error
		embedder   bool
		wantSubstr []string
		wantAbsent []string
	}{
		{
			name: "present", ok: true, embedder: true,
			wantSubstr: []string{"content_embedding present"},
			wantAbsent: []string{"WARNING"},
		},
		{
			name: "absent with an embedder configured is a WARNING",
			ok:   false, embedder: true,
			wantSubstr: []string{"WARNING", "ABSENT", "memory_create will REFUSE", "pgvector"},
		},
		{
			name: "absent with no embedder is a stated deployment fact",
			ok:   false, embedder: false,
			wantSubstr: []string{"unavailable", "keyword+recency"},
			wantAbsent: []string{"WARNING"},
		},
		{
			name: "a probe error is reported as an error, not as absence",
			err:  errors.New("connection reset"),
			// It must not claim the column is absent — that was the latching bug
			// one layer down, and the boot log must not restate it as fact.
			wantSubstr: []string{"WARNING", "could not determine", "connection reset"},
			wantAbsent: []string{"ABSENT"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var lines []string
			reportMemoryVectorColumn(context.Background(),
				func(context.Context) (bool, error) { return tc.ok, tc.err },
				tc.embedder,
				func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) })
			if len(lines) != 1 {
				t.Fatalf("want exactly one boot line, got %d: %v", len(lines), lines)
			}
			for _, want := range tc.wantSubstr {
				if !strings.Contains(lines[0], want) {
					t.Fatalf("boot line %q must contain %q", lines[0], want)
				}
			}
			for _, no := range tc.wantAbsent {
				if strings.Contains(lines[0], no) {
					t.Fatalf("boot line %q must NOT contain %q", lines[0], no)
				}
			}
		})
	}

	t.Run("a nil probe (no Postgres) says nothing", func(t *testing.T) {
		called := false
		reportMemoryVectorColumn(context.Background(), nil, true,
			func(string, ...any) { called = true })
		if called {
			t.Fatalf("nothing to report without a store")
		}
	})
}
