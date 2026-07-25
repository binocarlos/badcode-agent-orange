package agentdb

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// newWorkerTestStore returns a Store over a temp sqlite DB with only the
// `workers` table auto-migrated (the numbered Postgres migrations cannot run on
// sqlite). White-box: constructs Store{gdb} directly, like newTestStore.
func newWorkerTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "workers_test.sqlite")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&Worker{}); err != nil {
		t.Fatalf("automigrate Worker: %v", err)
	}
	return &Store{gdb: db}
}

func mustUpsertWorker(t *testing.T, s *Store, w *Worker) *Worker {
	t.Helper()
	got, err := s.UpsertWorker(context.Background(), w)
	if err != nil {
		t.Fatalf("upsert worker %s/%s: %v", w.Project, w.Name, err)
	}
	return got
}

func TestWorkersTableName(t *testing.T) {
	if got := (Worker{}).TableName(); got != "workers" {
		t.Fatalf("table name: want %q, got %q", "workers", got)
	}
}

// The spec's default: a worker created without an explicit max_instances runs
// one job at a time. Asserted on the returned row AND on a fresh read, so a
// store-side default that never reached the database would fail here.
func TestWorkersMaxInstancesDefault(t *testing.T) {
	s := newWorkerTestStore(t)
	ctx := context.Background()

	tests := []struct {
		name string
		in   int
		want int
	}{
		{"unset defaults to one", 0, DefaultMaxInstances},
		{"explicit one", 1, 1},
		{"explicit many", 7, 7},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := NewWorker("acme", "email-answerer")
			w.MaxInstances = tc.in
			created := mustUpsertWorker(t, s, w)
			if created.MaxInstances != tc.want {
				t.Fatalf("returned max_instances: want %d, got %d", tc.want, created.MaxInstances)
			}
			read, err := s.GetWorker(ctx, "acme", "email-answerer")
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if read.MaxInstances != tc.want {
				t.Fatalf("persisted max_instances: want %d, got %d", tc.want, read.MaxInstances)
			}
		})
	}

	// NewWorker itself carries the default, so a caller that never touches the
	// field still gets 1 (and an enabled worker).
	fresh := NewWorker("acme", "fresh-worker")
	if fresh.MaxInstances != 1 || !fresh.Enabled {
		t.Fatalf("NewWorker defaults: got max_instances=%d enabled=%v", fresh.MaxInstances, fresh.Enabled)
	}

	// A negative cap is nonsense and must fail loudly rather than be coerced.
	bad := NewWorker("acme", "bad-worker")
	bad.MaxInstances = -1
	if _, err := s.UpsertWorker(ctx, bad); !errors.Is(err, ErrWorkerInvalid) {
		t.Fatalf("negative max_instances: want ErrWorkerInvalid, got %v", err)
	}
}

// briefing is a jsonb list of label selectors defaulting to null: "no briefing
// configured" (nil) and "an explicitly empty list" ([]) are different states and
// must both survive a write/read cycle unchanged.
func TestWorkersBriefingRoundTrip(t *testing.T) {
	s := newWorkerTestStore(t)
	ctx := context.Background()

	tests := []struct {
		name string
		in   SelectorList
		want SelectorList
	}{
		{"unset stays null", nil, nil},
		{"empty list stays empty", SelectorList{}, SelectorList{}},
		{"single selector", SelectorList{"kind=house-style"}, SelectorList{"kind=house-style"}},
		{
			"multiple selectors keep order",
			SelectorList{"kind=house-style", "kind=rolling-summary, worker=archivist", "topic=pricing"},
			SelectorList{"kind=house-style", "kind=rolling-summary, worker=archivist", "topic=pricing"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := NewWorker("acme", "briefed-worker")
			w.Briefing = tc.in
			mustUpsertWorker(t, s, w)

			read, err := s.GetWorker(ctx, "acme", "briefed-worker")
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if !reflect.DeepEqual(read.Briefing, tc.want) {
				t.Fatalf("briefing round-trip: want %#v, got %#v", tc.want, read.Briefing)
			}
			// nil and empty must stay distinguishable after the round trip.
			if (tc.want == nil) != (read.Briefing == nil) {
				t.Fatalf("nil-ness lost: want nil=%v, got nil=%v", tc.want == nil, read.Briefing == nil)
			}
		})
	}

	// A blank selector is a configuration mistake, not an empty list.
	w := NewWorker("acme", "blank-selector")
	w.Briefing = SelectorList{"kind=ok", "   "}
	if _, err := s.UpsertWorker(ctx, w); !errors.Is(err, ErrWorkerInvalid) {
		t.Fatalf("blank selector: want ErrWorkerInvalid, got %v", err)
	}
}

func TestWorkersUpsertRoundTripAndReplace(t *testing.T) {
	s := newWorkerTestStore(t)
	ctx := context.Background()

	w := NewWorker("acme", "email-answerer")
	w.Description = "answers inbound email"
	w.SystemPrompt = "You answer email."
	w.MCPConfig = JSONMap{"gmail": map[string]any{"command": "gmail-mcp"}}
	w.Image = "toolbox:2"
	w.MaxInstances = 3
	w.Briefing = SelectorList{"kind=house-style"}
	created := mustUpsertWorker(t, s, w)
	if created.CreatedAt == 0 || created.UpdatedAt == 0 {
		t.Fatalf("timestamps not stamped: %+v", created)
	}

	read, err := s.GetWorker(ctx, "acme", "email-answerer")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if read.Description != "answers inbound email" || read.SystemPrompt != "You answer email." {
		t.Fatalf("text columns: %+v", read)
	}
	if read.Image != "toolbox:2" || read.MaxInstances != 3 || !read.Enabled {
		t.Fatalf("plumbing columns: %+v", read)
	}
	if got, ok := read.MCPConfig["gmail"]; !ok {
		t.Fatalf("mcp_config lost: %#v", read.MCPConfig)
	} else if _, ok := got.(map[string]any); !ok {
		t.Fatalf("mcp_config shape: %#v", got)
	}

	// Upsert is replace, not patch: the second write wins on every column,
	// including switching a worker off (the GORM zero-value trap).
	w2 := NewWorker("acme", "email-answerer")
	w2.Description = "retired"
	w2.Enabled = false
	mustUpsertWorker(t, s, w2)

	read, err = s.GetWorker(ctx, "acme", "email-answerer")
	if err != nil {
		t.Fatalf("get after replace: %v", err)
	}
	if read.Description != "retired" || read.Enabled {
		t.Fatalf("replace did not take: %+v", read)
	}
	if read.Image != "" || read.SystemPrompt != "" || read.Briefing != nil {
		t.Fatalf("replace left stale values: %+v", read)
	}
	if read.MaxInstances != DefaultMaxInstances {
		t.Fatalf("replace should reset max_instances to the default, got %d", read.MaxInstances)
	}

	// One row, not two: (project, name) is the identity.
	list, err := s.ListWorkers(ctx, "acme")
	if err != nil || len(list) != 1 {
		t.Fatalf("list after replace: %d rows, err=%v", len(list), err)
	}
}

func TestWorkersValidation(t *testing.T) {
	s := newWorkerTestStore(t)
	ctx := context.Background()

	tests := []struct {
		name    string
		worker  *Worker
		wantErr error
	}{
		{"nil worker", nil, ErrWorkerInvalid},
		{"missing project", &Worker{Name: "ok-name"}, ErrWorkerInvalid},
		{"missing name", &Worker{Project: "acme"}, ErrWorkerInvalid},
		{"upper case name", &Worker{Project: "acme", Name: "EmailAnswerer"}, ErrWorkerInvalid},
		{"underscore name", &Worker{Project: "acme", Name: "email_answerer"}, ErrWorkerInvalid},
		{"leading hyphen", &Worker{Project: "acme", Name: "-answerer"}, ErrWorkerInvalid},
		{"double hyphen", &Worker{Project: "acme", Name: "email--answerer"}, ErrWorkerInvalid},
		{"space in name", &Worker{Project: "acme", Name: "email answerer"}, ErrWorkerInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.UpsertWorker(ctx, tc.worker); !errors.Is(err, tc.wantErr) {
				t.Fatalf("want %v, got %v", tc.wantErr, err)
			}
		})
	}

	// Names the spec's examples use must be accepted.
	for _, name := range []string{"email-answerer", "email-review-consultant", "archivist", "worker2"} {
		if _, err := s.UpsertWorker(ctx, NewWorker("acme", name)); err != nil {
			t.Fatalf("valid name %q rejected: %v", name, err)
		}
	}
}

func TestWorkersNotFound(t *testing.T) {
	s := newWorkerTestStore(t)
	ctx := context.Background()

	if _, err := s.GetWorker(ctx, "acme", "nobody"); !errors.Is(err, ErrWorkerNotFound) {
		t.Fatalf("get missing: want ErrWorkerNotFound, got %v", err)
	}
	if err := s.DeleteWorker(ctx, "acme", "nobody"); !errors.Is(err, ErrWorkerNotFound) {
		t.Fatalf("delete missing: want ErrWorkerNotFound, got %v", err)
	}

	mustUpsertWorker(t, s, NewWorker("acme", "archivist"))
	if err := s.DeleteWorker(ctx, "acme", "archivist"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetWorker(ctx, "acme", "archivist"); !errors.Is(err, ErrWorkerNotFound) {
		t.Fatalf("get after delete: want ErrWorkerNotFound, got %v", err)
	}

	// An empty project is never a wildcard — every read/write demands one.
	if _, err := s.ListWorkers(ctx, ""); !errors.Is(err, ErrWorkerInvalid) {
		t.Fatalf("list without project: want ErrWorkerInvalid, got %v", err)
	}
	if _, err := s.GetWorker(ctx, "", "archivist"); !errors.Is(err, ErrWorkerInvalid) {
		t.Fatalf("get without project: want ErrWorkerInvalid, got %v", err)
	}
	if err := s.DeleteWorker(ctx, "", "archivist"); !errors.Is(err, ErrWorkerInvalid) {
		t.Fatalf("delete without project: want ErrWorkerInvalid, got %v", err)
	}
}

// Project is a hard namespace (§6.1) and the isolation proof §12 demands: two
// projects may hold same-named workers, and no read, write, or delete in one
// project may reach the other's row.
func TestWorkersProjectIsolation(t *testing.T) {
	s := newWorkerTestStore(t)
	ctx := context.Background()

	acme := NewWorker("acme", "email-answerer")
	acme.SystemPrompt = "acme prompt"
	mustUpsertWorker(t, s, acme)

	other := NewWorker("other", "email-answerer")
	other.SystemPrompt = "other prompt"
	mustUpsertWorker(t, s, other)

	// Same name in both projects, each with its own body.
	got, err := s.GetWorker(ctx, "acme", "email-answerer")
	if err != nil || got.SystemPrompt != "acme prompt" {
		t.Fatalf("acme read: %+v err=%v", got, err)
	}
	got, err = s.GetWorker(ctx, "other", "email-answerer")
	if err != nil || got.SystemPrompt != "other prompt" {
		t.Fatalf("other read: %+v err=%v", got, err)
	}

	// A worker that exists only in acme is invisible from other.
	mustUpsertWorker(t, s, NewWorker("acme", "acme-only"))
	if _, err := s.GetWorker(ctx, "other", "acme-only"); !errors.Is(err, ErrWorkerNotFound) {
		t.Fatalf("cross-project read leaked: %v", err)
	}

	// Lists never cross the boundary.
	list, err := s.ListWorkers(ctx, "other")
	if err != nil {
		t.Fatalf("list other: %v", err)
	}
	if len(list) != 1 || list[0].Name != "email-answerer" || list[0].Project != "other" {
		t.Fatalf("list other leaked: %+v", list)
	}

	// Writing "other"'s worker must not touch acme's same-named row.
	upd := NewWorker("other", "email-answerer")
	upd.SystemPrompt = "other prompt v2"
	mustUpsertWorker(t, s, upd)
	got, err = s.GetWorker(ctx, "acme", "email-answerer")
	if err != nil || got.SystemPrompt != "acme prompt" {
		t.Fatalf("cross-project write leaked: %+v err=%v", got, err)
	}

	// Deleting from other must not delete acme's row.
	if err := s.DeleteWorker(ctx, "other", "email-answerer"); err != nil {
		t.Fatalf("delete other: %v", err)
	}
	if _, err := s.GetWorker(ctx, "acme", "email-answerer"); err != nil {
		t.Fatalf("cross-project delete leaked: %v", err)
	}
	// And a delete aimed at a foreign project's row is a no-op not-found.
	if err := s.DeleteWorker(ctx, "other", "acme-only"); !errors.Is(err, ErrWorkerNotFound) {
		t.Fatalf("cross-project delete: want ErrWorkerNotFound, got %v", err)
	}
	if _, err := s.GetWorker(ctx, "acme", "acme-only"); err != nil {
		t.Fatalf("acme-only was deleted through the other project: %v", err)
	}
}

func TestWorkersListOrderingAndEmpty(t *testing.T) {
	s := newWorkerTestStore(t)
	ctx := context.Background()

	list, err := s.ListWorkers(ctx, "empty-project")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if list == nil || len(list) != 0 {
		t.Fatalf("empty list must be non-nil and empty, got %#v", list)
	}

	for _, name := range []string{"zeta-worker", "archivist", "email-answerer"} {
		mustUpsertWorker(t, s, NewWorker("acme", name))
	}
	list, err = s.ListWorkers(ctx, "acme")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := []string{"archivist", "email-answerer", "zeta-worker"}
	got := make([]string, len(list))
	for i, w := range list {
		got[i] = w.Name
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("list order: want %v, got %v", want, got)
	}
}

func TestWorkersSelectorListValueScan(t *testing.T) {
	tests := []struct {
		name    string
		in      SelectorList
		wantVal any
	}{
		{"nil is SQL NULL", nil, nil},
		{"empty is an empty array", SelectorList{}, `[]`},
		{"values marshal as json", SelectorList{"kind=a", "b=c"}, `["kind=a","b=c"]`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v, err := tc.in.Value()
			if err != nil {
				t.Fatalf("Value: %v", err)
			}
			if v != tc.wantVal {
				t.Fatalf("Value: want %#v, got %#v", tc.wantVal, v)
			}
		})
	}

	scans := []struct {
		name string
		in   any
		want SelectorList
	}{
		{"nil scans to nil", nil, nil},
		{"json null scans to nil", []byte("null"), nil},
		{"empty bytes scan to nil", []byte(""), nil},
		{"bytes", []byte(`["x=1"]`), SelectorList{"x=1"}},
		{"string", `["x=1","y=2"]`, SelectorList{"x=1", "y=2"}},
		{"empty array", []byte(`[]`), SelectorList{}},
	}
	for _, tc := range scans {
		t.Run(tc.name, func(t *testing.T) {
			var l SelectorList
			if err := l.Scan(tc.in); err != nil {
				t.Fatalf("Scan: %v", err)
			}
			if !reflect.DeepEqual(l, tc.want) {
				t.Fatalf("Scan: want %#v, got %#v", tc.want, l)
			}
		})
	}

	var l SelectorList
	if err := l.Scan(42); err == nil {
		t.Fatalf("Scan of an unsupported type must error")
	}
	if err := l.Scan([]byte(`{"not":"a list"}`)); err == nil {
		t.Fatalf("Scan of a non-list must error")
	}
}

// The session columns migration 021 adds: `worker` (which worker's job this
// session is — distinct from the fleet-placement `worker_id`), `composed_prompt`
// (§6.2 provenance) and `lease_expires_at` (§8.4).
func TestWorkersSessionColumns(t *testing.T) {
	s := newSessionTestStore(t)
	ctx := context.Background()

	sess := baseSession("s-worker-job")
	sess.Worker = "email-answerer"
	sess.WorkerID = "fleet-node-7"
	sess.ComposedPrompt = "PREAMBLE\n\nPROJECT\n\nWORKER"
	sess.LeaseExpiresAt = 1750000000
	mustCreateSession(t, s, sess)

	got, err := s.GetSession(ctx, "s-worker-job")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if got.Worker != "email-answerer" {
		t.Fatalf("worker: got %q", got.Worker)
	}
	if got.WorkerID != "fleet-node-7" {
		t.Fatalf("worker_id (fleet binding) must be independent of worker: got %q", got.WorkerID)
	}
	if got.ComposedPrompt != "PREAMBLE\n\nPROJECT\n\nWORKER" {
		t.Fatalf("composed_prompt: got %q", got.ComposedPrompt)
	}
	if got.LeaseExpiresAt != 1750000000 {
		t.Fatalf("lease_expires_at: got %d", got.LeaseExpiresAt)
	}

	// A plain vanilla session leaves all three at their zero values.
	plain := mustCreateSession(t, s, baseSession("s-plain"))
	if plain.Worker != "" || plain.ComposedPrompt != "" || plain.LeaseExpiresAt != 0 {
		t.Fatalf("vanilla session must not be worker-bound: %+v", plain)
	}
}

// Postgres-only: proves migration 021's DDL really carries the max_instances
// default of 1 and a nullable briefing, for rows written outside GORM.
func TestWorkersLivePG_SchemaDefaults(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	project := "cust-workers-" + uuid.New().String()
	t.Cleanup(func() {
		_ = s.DB().WithContext(ctx).Exec("DELETE FROM workers WHERE project = ?", project).Error
	})

	if err := s.DB().WithContext(ctx).Exec(
		"INSERT INTO workers (project, name) VALUES (?, ?)", project, "raw-insert",
	).Error; err != nil {
		t.Fatalf("raw insert: %v", err)
	}

	var row struct {
		MaxInstances int
		Enabled      bool
		Briefing     *string
	}
	if err := s.DB().WithContext(ctx).Raw(
		"SELECT max_instances, enabled, briefing FROM workers WHERE project = ? AND name = ?",
		project, "raw-insert",
	).Scan(&row).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if row.MaxInstances != 1 {
		t.Fatalf("DDL default max_instances: want 1, got %d", row.MaxInstances)
	}
	if !row.Enabled {
		t.Fatalf("DDL default enabled: want true")
	}
	if row.Briefing != nil {
		t.Fatalf("DDL default briefing: want NULL, got %q", *row.Briefing)
	}

	// And the store round-trips jsonb briefing through real Postgres.
	w := NewWorker(project, "live-worker")
	w.Briefing = SelectorList{"kind=rolling-summary, worker=live-worker", "kind=house-style"}
	mustUpsertWorker(t, s, w)
	read, err := s.GetWorker(ctx, project, "live-worker")
	if err != nil {
		t.Fatalf("live get: %v", err)
	}
	if !reflect.DeepEqual(read.Briefing, w.Briefing) {
		t.Fatalf("live briefing round-trip: want %#v, got %#v", w.Briefing, read.Briefing)
	}
}
