package agentdb

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newV1TestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "v1_test.sqlite")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&BoardRevision{}, &BoardHead{}, &BoardPromptFragment{}, &Ticket{}, &TelemetryRun{}); err != nil {
		t.Fatalf("automigrate v1: %v", err)
	}
	return &Store{gdb: db}
}

func TestTicketRow_RoundTrip(t *testing.T) {
	s := newV1TestStore(t)
	ctx := context.Background()
	in := &Ticket{
		ID: "t1", ProjectID: "badcode", Title: "Draft post", Objective: "write X",
		Acceptance: "on-brand", Status: "todo", Scope: JSONArray(`{"name":"post-writer"}`),
		DependsOn: JSONArray(`["t0"]`), Attempts: 2, BoardRev: "r3",
		PublishedRef: "https://x.example/123", CreatedAt: 10, UpdatedAt: 20,
	}
	if err := s.gdb.WithContext(ctx).Create(in).Error; err != nil {
		t.Fatalf("create ticket: %v", err)
	}
	var got Ticket
	if err := s.gdb.WithContext(ctx).First(&got, "id = ?", "t1").Error; err != nil {
		t.Fatalf("read ticket: %v", err)
	}
	if got.Status != "todo" || got.BoardRev != "r3" || got.Attempts != 2 || string(got.DependsOn) != `["t0"]` {
		t.Fatalf("ticket round-trip wrong: %+v", got)
	}
	if got.PublishedRef != "https://x.example/123" {
		t.Fatalf("published_ref not persisted: %+v", got)
	}
}

func TestTelemetryRunRow_RoundTrip(t *testing.T) {
	s := newV1TestStore(t)
	ctx := context.Background()
	in := &TelemetryRun{ID: "run1", Seq: 1, Scope: "manager", BoardRevision: "r1", Prompt: "p", Output: "o"}
	if err := s.gdb.WithContext(ctx).Create(in).Error; err != nil {
		t.Fatalf("create run: %v", err)
	}
	var got TelemetryRun
	if err := s.gdb.WithContext(ctx).First(&got, "id = ?", "run1").Error; err != nil {
		t.Fatalf("read run: %v", err)
	}
	if got.Seq != 1 || got.Scope != "manager" || got.Output != "o" {
		t.Fatalf("run round-trip wrong: %+v", got)
	}
}

func TestV1MigrationsRegistered(t *testing.T) {
	want := []string{"022_board_collapse", "023_tickets", "024_runs"}
	have := map[string]string{}
	for _, m := range agentMigrations {
		have[m.Name] = m.SQL
	}
	for _, name := range want {
		sql, ok := have[name]
		if !ok {
			t.Fatalf("migration %q not registered", name)
		}
		if sql == "" {
			t.Fatalf("migration %q has empty SQL", name)
		}
	}
	// §0 collapse must drop exactly the three deferred board tables.
	collapse := have["022_board_collapse"]
	for _, tbl := range []string{"board_staff", "board_pipelines", "board_event_types"} {
		if !contains(collapse, "DROP TABLE IF EXISTS "+tbl) {
			t.Fatalf("022_board_collapse missing drop of %q: %s", tbl, collapse)
		}
	}
	// New tables must be created idempotently.
	if !contains(have["023_tickets"], "CREATE TABLE IF NOT EXISTS tickets") {
		t.Fatalf("023_tickets missing idempotent create")
	}
	if !contains(have["023_tickets"], "published_ref") {
		t.Fatalf("023_tickets missing published_ref column")
	}
	if !contains(have["024_runs"], "CREATE TABLE IF NOT EXISTS runs") {
		t.Fatalf("024_runs missing idempotent create")
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && strings.Contains(s, sub) }
