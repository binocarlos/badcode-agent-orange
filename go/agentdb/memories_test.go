package agentdb

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// The memory store is append-only BY CONSTRUCTION: the guarantee is that no
// mutating method exists to call. This test is the guard rail — it fails the
// moment someone adds one, which is the only way the invariant can be broken.
func TestMemoriesStoreIsAppendOnly(t *testing.T) {
	typ := reflect.TypeOf(&Store{})

	mutators := []string{"update", "delete", "set", "patch", "remove", "upsert", "purge", "prune"}
	var found []string
	haveCreate, haveGet, haveSearch := false, false, false
	for i := 0; i < typ.NumMethod(); i++ {
		name := typ.Method(i).Name
		lower := strings.ToLower(name)
		if !strings.Contains(lower, "memor") { // Memory / Memories
			continue
		}
		switch name {
		case "CreateMemory":
			haveCreate = true
		case "GetMemory":
			haveGet = true
		case "SearchMemories":
			haveSearch = true
		}
		for _, m := range mutators {
			if strings.Contains(lower, m) {
				found = append(found, name)
			}
		}
	}
	if len(found) > 0 {
		t.Fatalf("memories are immutable (§7.1): no mutating store method may exist, found %v", found)
	}
	if !haveCreate || !haveGet || !haveSearch {
		t.Fatalf("expected CreateMemory/GetMemory/SearchMemories to exist, got create=%v get=%v search=%v",
			haveCreate, haveGet, haveSearch)
	}
}

// TestMemorySqlite pins the degradation decision (§7, docs/15-standalone-stack.md):
// **memory requires Postgres**, and the sqlite dev store says so out loud.
//
// The alternative — a keyword-only sqlite implementation — was rejected: the
// memory system's whole promise is that what a worker wrote down is there
// later, and a store that quietly drops jsonb selectors, tsvector ranking and
// the semantic leg would keep answering searches with plausible, incomplete
// results. A store that silently forgets is worse than no store, so all three
// entry points fail with ErrMemoryRequiresPostgres on any non-Postgres dialect.
//
// (This is the outer boundary only. *Within* Postgres, pgvector is optional:
// migration 022 adds the vector column when the extension is available and
// search drops the semantic CTE when it is not — that degradation is silent by
// design, because keyword+recency still returns real rows in the same shape.)
func TestMemorySqlite(t *testing.T) {
	ctx := context.Background()
	sqliteStore := newTestStore(t)

	// A sqlite store that HAS a memories table is the interesting case: the
	// refusal must come from the dialect, not from a missing table, or a
	// half-working store would appear the moment someone ran AutoMigrate.
	migrated := newTestStore(t)
	if err := migrated.DB().AutoMigrate(&Memory{}); err != nil {
		t.Fatalf("automigrate Memory on sqlite: %v", err)
	}

	// (*Store)(nil) is the "no store wired" case — still an error, not a panic.
	var nilStore *Store

	tests := []struct {
		name string
		call func(s *Store) error
	}{
		{"create", func(s *Store) error {
			_, err := s.CreateMemory(ctx, &Memory{
				Project: "p", Content: "the refund window is 30 days",
				Labels: LabelSet{"kind": "fact"},
			}, nil)
			return err
		}},
		{"create with embedding", func(s *Store) error {
			_, err := s.CreateMemory(ctx, &Memory{Project: "p", Content: "x"}, make([]float32, MemoryEmbeddingDim))
			return err
		}},
		{"get", func(s *Store) error {
			_, err := s.GetMemory(ctx, "p", "some-id")
			return err
		}},
		{"search, bare selector", func(s *Store) error {
			_, err := s.SearchMemories(ctx, &MemorySearchQuery{Project: "p", LabelSelector: "kind=fact"})
			return err
		}},
		{"search, query text", func(s *Store) error {
			_, err := s.SearchMemories(ctx, &MemorySearchQuery{Project: "p", Query: "refund window"})
			return err
		}},
		{"search, nil query", func(s *Store) error {
			_, err := s.SearchMemories(ctx, nil)
			return err
		}},
	}

	for _, tc := range tests {
		for _, store := range []struct {
			label string
			s     *Store
		}{
			{"sqlite", sqliteStore},
			{"sqlite with a memories table", migrated},
			{"nil store", nilStore},
		} {
			t.Run(tc.name+"/"+store.label, func(t *testing.T) {
				err := tc.call(store.s)
				if !errors.Is(err, ErrMemoryRequiresPostgres) {
					t.Fatalf("want ErrMemoryRequiresPostgres, got %v", err)
				}
				// The message has to tell an operator what to do about it:
				// it names Postgres and why (jsonb/tsvector/pgvector).
				for _, want := range []string{"Postgres", "jsonb", "tsvector", "pgvector"} {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("error message must mention %q, got %q", want, err.Error())
					}
				}
			})
		}
	}

	// Nothing was written along the way: the refusal happens before any SQL, so
	// a caller cannot end up with rows it can never read back.
	var rows int64
	if err := migrated.DB().Raw("SELECT COUNT(*) FROM memories").Scan(&rows).Error; err != nil {
		t.Fatalf("count sqlite memories: %v", err)
	}
	if rows != 0 {
		t.Fatalf("sqlite must accept no memory writes at all, found %d rows", rows)
	}
}

func TestMemoriesFormatVector(t *testing.T) {
	if got := FormatVector([]float32{0, 1, -0.5}); got != "[0,1,-0.5]" {
		t.Fatalf("FormatVector = %q", got)
	}
	if got := FormatVector(nil); got != "[]" {
		t.Fatalf("FormatVector(nil) = %q", got)
	}
}

func TestMemoriesTableName(t *testing.T) {
	if name := (Memory{}).TableName(); name != "memories" {
		t.Fatalf("table name: %q", name)
	}
}
