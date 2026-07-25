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

// The dev store is sqlite; memory is not available there. Rather than pretend
// (a store that silently forgets is worse than no store), every entry point
// fails loudly with ErrMemoryRequiresPostgres. See docs/15-standalone-stack.md.
func TestMemoriesRequirePostgres(t *testing.T) {
	s := newTestStore(t) // sqlite
	ctx := context.Background()

	if _, err := s.CreateMemory(ctx, &Memory{Project: "p", Content: "hello"}, nil); !errors.Is(err, ErrMemoryRequiresPostgres) {
		t.Fatalf("CreateMemory on sqlite: want ErrMemoryRequiresPostgres, got %v", err)
	}
	if _, err := s.GetMemory(ctx, "p", "id"); !errors.Is(err, ErrMemoryRequiresPostgres) {
		t.Fatalf("GetMemory on sqlite: want ErrMemoryRequiresPostgres, got %v", err)
	}
	if _, err := s.SearchMemories(ctx, &MemorySearchQuery{Project: "p"}); !errors.Is(err, ErrMemoryRequiresPostgres) {
		t.Fatalf("SearchMemories on sqlite: want ErrMemoryRequiresPostgres, got %v", err)
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
