package agentdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newLiveScratchDB creates a throwaway database on the live Postgres server and
// returns a URL pointing at it, dropping it on cleanup.
//
// A fresh database — not the shared one — because the interesting state is
// "migrations are pending", and the shared test database has had every
// migration applied since the first run. A per-test database also keeps the
// concurrent-boot test from writing anything another package's test binary can
// see, which is exactly the interference this whole file is about.
func newLiveScratchDB(t *testing.T) string {
	t.Helper()
	adminURL := os.Getenv("AGENTKIT_TEST_POSTGRES_URL")
	if adminURL == "" {
		t.Skip("AGENTKIT_TEST_POSTGRES_URL not set — skipping live Postgres test")
	}

	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("random db name: %v", err)
	}
	name := "agentdb_mig_" + hex.EncodeToString(suffix[:])

	admin, err := gorm.Open(postgres.Open(adminURL), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	if err := admin.Exec("CREATE DATABASE " + name).Error; err != nil {
		t.Fatalf("create scratch database %s: %v", name, err)
	}
	t.Cleanup(func() {
		// Terminate stragglers first: a pooled connection left open by a
		// failed Open would block DROP DATABASE and leak the scratch DB.
		admin.Exec("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = ?", name)
		if err := admin.Exec("DROP DATABASE IF EXISTS " + name).Error; err != nil {
			t.Logf("drop scratch database %s: %v", name, err)
		}
		if sqlDB, err := admin.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	u, err := url.Parse(adminURL)
	if err != nil {
		t.Fatalf("parse %q: %v", adminURL, err)
	}
	u.Path = "/" + name
	return u.String()
}

// TestLivePG_ConcurrentMigrationsAreSerialised is the regression test for the
// boot race: two processes calling Open against the same database at the same
// moment both read an empty `agentdb_migrations`, both decide every migration
// is pending, and both run them — the loser dies on
// `duplicate key value violates unique constraint "agentdb_migrations_pkey"`
// (or on whatever DDL the winner is halfway through).
//
// This is not hypothetical: `go test ./agentdb/... ./cmd/agentd/...` starts one
// binary per package in parallel, and both of them Open the same URL. It bit us
// twice in one day, on migrations 032 and 033 — both times the first time a new
// migration met a shared database. In production it is worse: it is two agentd
// replicas booting together, one of which crashes.
//
// Every concurrent Open must succeed, and each migration must be recorded
// exactly once.
func TestLivePG_ConcurrentMigrationsAreSerialised(t *testing.T) {
	dbURL := newLiveScratchDB(t)

	const boots = 8
	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	errs := make([]error, boots)

	for i := 0; i < boots; i++ {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			start.Wait() // release all boots at the same instant
			s, err := Open(dbURL)
			errs[i] = err
			if err == nil {
				if sqlDB, dbErr := s.DB().DB(); dbErr == nil {
					_ = sqlDB.Close()
				}
			}
		}(i)
	}
	start.Done()
	done.Wait()

	var failed int
	for i, err := range errs {
		if err != nil {
			failed++
			t.Errorf("concurrent boot %d failed: %v", i, err)
		}
	}
	if failed > 0 {
		t.Fatalf("%d of %d concurrent boots failed; every replica must survive a simultaneous start", failed, boots)
	}

	// Exactly-once: no migration name recorded twice, none missing. The
	// primary key makes a duplicate row impossible, so the real assertion
	// here is completeness — a loser that skipped the lock and bailed early
	// would leave the set short.
	s, err := Open(dbURL)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	var names []string
	if err := s.DB().Raw("SELECT name FROM agentdb_migrations ORDER BY name").Scan(&names).Error; err != nil {
		t.Fatalf("read migration table: %v", err)
	}
	if len(names) != len(agentMigrations) {
		t.Fatalf("applied %d migrations, want %d: %v", len(names), len(agentMigrations), names)
	}
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			t.Fatalf("migration %s recorded twice", n)
		}
		seen[n] = true
	}
	for _, m := range agentMigrations {
		if !seen[m.Name] {
			t.Fatalf("migration %s not applied", m.Name)
		}
	}
}

// TestLivePG_ConcurrentMigrationsWithOnePending is the exact shape that bit us
// twice: the tracking table already exists and every migration but the newest
// is recorded, which is what a shared test database looks like the first time a
// branch adding a migration runs against it. Before the fix seven of eight boots
// died on `agentdb_migrations_pkey`.
func TestLivePG_ConcurrentMigrationsWithOnePending(t *testing.T) {
	dbURL := newLiveScratchDB(t)

	s, err := Open(dbURL)
	if err != nil {
		t.Fatalf("bootstrap open: %v", err)
	}
	// Un-apply the newest migration. Every migration body in this repo is
	// written idempotently (IF NOT EXISTS / ADD COLUMN IF NOT EXISTS), so
	// re-running it is safe; what was never safe is two processes racing to
	// record it.
	newest := agentMigrations[len(agentMigrations)-1].Name
	if err := s.DB().Exec("DELETE FROM agentdb_migrations WHERE name = ?", newest).Error; err != nil {
		t.Fatalf("un-apply %s: %v", newest, err)
	}
	if sqlDB, err := s.DB().DB(); err == nil {
		_ = sqlDB.Close()
	}

	const boots = 8
	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	errs := make([]error, boots)
	for i := 0; i < boots; i++ {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			start.Wait()
			s, err := Open(dbURL)
			errs[i] = err
			if err == nil {
				if sqlDB, dbErr := s.DB().DB(); dbErr == nil {
					_ = sqlDB.Close()
				}
			}
		}(i)
	}
	start.Done()
	done.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("boot %d failed with one migration pending: %v", i, err)
		}
	}
}

// TestLivePG_MigrationLoserAppliesNothing pins the answer to "what should the
// losing process do?": wait, then re-read the applied set, find its work
// already done, and boot without touching the schema. That is what a starting
// replica wants — and it is only true because the SELECT of the applied set
// moved *inside* the lock. A fix that serialised just the INSERT would leave
// the loser re-executing a migration body it had already decided was pending.
//
// The probe migration is deliberately NOT idempotent: if the loser re-ran it,
// `CREATE TABLE` would fail with "already exists" and this test would catch it.
func TestLivePG_MigrationLoserAppliesNothing(t *testing.T) {
	dbURL := newLiveScratchDB(t)

	loser, err := gorm.Open(postgres.Open(dbURL), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("connect loser: %v", err)
	}
	if err := applyMigrations(loser, agentMigrations); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	probe := migration{Name: "900_loser_probe", SQL: `CREATE TABLE probe_900 (id INT)`}
	list := append(append([]migration{}, agentMigrations...), probe)

	// The "winner": a separate session holding the lock.
	winner, err := gorm.Open(postgres.Open(dbURL), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("connect winner: %v", err)
	}
	winnerDB, err := winner.DB()
	if err != nil {
		t.Fatalf("winner sql.DB: %v", err)
	}
	winnerDB.SetMaxOpenConns(1) // one backend session, so the lock stays put
	if _, err := winnerDB.ExecContext(context.Background(), "SELECT pg_advisory_lock($1)", migrationLockKey); err != nil {
		t.Fatalf("winner take lock: %v", err)
	}

	loserDone := make(chan error, 1)
	go func() { loserDone <- applyMigrations(loser, list) }()

	// It must actually be blocked, not racing ahead.
	select {
	case err := <-loserDone:
		t.Fatalf("loser returned (%v) while the lock was held — it never waited", err)
	case <-time.After(750 * time.Millisecond):
	}

	// The winner does the work the loser thinks is pending, then releases.
	if err := winner.Exec(probe.SQL).Error; err != nil {
		t.Fatalf("winner apply probe: %v", err)
	}
	if err := winner.Exec("INSERT INTO agentdb_migrations (name) VALUES (?)", probe.Name).Error; err != nil {
		t.Fatalf("winner record probe: %v", err)
	}
	if _, err := winnerDB.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", migrationLockKey); err != nil {
		t.Fatalf("winner release lock: %v", err)
	}

	select {
	case err := <-loserDone:
		if err != nil {
			t.Fatalf("the loser must boot cleanly, applying nothing; got: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("loser never returned after the lock was released")
	}
}

// TestLivePG_MigrationLockIsReleasedAfterAFailedMigration pins the rule that a
// migration erroring mid-way must not leave the advisory lock held. A leaked
// session-level lock would wedge every later boot against this database —
// trading an intermittent race for a permanent outage, which is strictly worse
// than the bug being fixed.
func TestLivePG_MigrationLockIsReleasedAfterAFailedMigration(t *testing.T) {
	dbURL := newLiveScratchDB(t)

	gdb, err := gorm.Open(postgres.Open(dbURL), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatalf("sql.DB: %v", err)
	}
	// A single connection, so a leaked lock would be leaked on the very
	// backend session the next call gets handed back — the honest shape of the
	// production hazard rather than a lucky miss.
	sqlDB.SetMaxOpenConns(1)

	broken := append(append([]migration{}, agentMigrations...),
		migration{Name: "901_deliberately_broken", SQL: `THIS IS NOT SQL`})
	err = applyMigrations(gdb, broken)
	if err == nil {
		t.Fatal("a broken migration must return an error")
	}
	if !strings.Contains(err.Error(), "901_deliberately_broken") {
		t.Fatalf("the error should name the failing migration, got: %v", err)
	}

	// Nothing on the server still holds our key. classid/objid are the high
	// and low halves of the 64-bit advisory key.
	var held int64
	if err := gdb.Raw(
		"SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND classid = ?::oid AND objid = ?::oid",
		uint32(migrationLockKey>>32), uint32(migrationLockKey),
	).Scan(&held).Error; err != nil {
		t.Fatalf("inspect pg_locks: %v", err)
	}
	if held != 0 {
		t.Fatalf("advisory lock leaked after a failed migration: %d holder(s)", held)
	}

	// And the proof that matters: the next boot still works, on this pool and
	// on a fresh one.
	if err := applyMigrations(gdb, agentMigrations); err != nil {
		t.Fatalf("boot after a failed migration must still work: %v", err)
	}
	if _, err := Open(dbURL); err != nil {
		t.Fatalf("fresh Open after a failed migration: %v", err)
	}
}

// TestMigrationLockKeyIsStable pins the advisory-lock key. It is derived, not
// chosen, so nothing should ever change it — but if something did, two agentd
// versions booting together would take different locks, exclude nobody, and the
// race would be silently back with no test failing. Hence a literal.
//
// Runs everywhere; needs no database.
func TestMigrationLockKeyIsStable(t *testing.T) {
	const want = int64(7398895747279634323) // fnv64a("agentdb:migrations") & 2^63-1
	if migrationLockKey != want {
		t.Fatalf("migration advisory-lock key changed: got %d, want %d — a changed key means "+
			"old and new processes no longer exclude each other", migrationLockKey, want)
	}
	if migrationLockKey <= 0 {
		t.Fatalf("key must be positive so the pg_locks halves are unambiguous, got %d", migrationLockKey)
	}
}

// TestAdvisoryLockGateIsPostgresOnly pins the dialect gate. sqlite has no
// pg_advisory_lock, and a "fix" that made every sqlite-backed store in this repo
// fail on an unknown function would be worse than the race it fixed.
//
// Needs no database.
func TestAdvisoryLockGateIsPostgresOnly(t *testing.T) {
	for _, tc := range []struct {
		name string
		gdb  *gorm.DB
		want bool
	}{
		{"sqlite", newTestStore(t).DB(), false}, // newTestStore: sqlite, artifacts_test.go
		{"nil db", nil, false},
		{"zero-value db", &gorm.DB{}, false},
		{"db with no dialector", &gorm.DB{Config: &gorm.Config{}}, false},
	} {
		if got := usesAdvisoryLocks(tc.gdb); got != tc.want {
			t.Errorf("%s: usesAdvisoryLocks = %v, want %v", tc.name, got, tc.want)
		}
	}

	// The sqlite store really is sqlite — otherwise the case above proves
	// nothing about the dialect we care about.
	if got := newTestStore(t).DB().Dialector.Name(); got != "sqlite" {
		t.Fatalf("test store dialect: got %q, want sqlite", got)
	}
	// And a Postgres dialector (no server needed to construct one) does gate on.
	if !usesAdvisoryLocks(&gorm.DB{Config: &gorm.Config{Dialector: postgres.Open("")}}) {
		t.Fatal("a postgres dialector must take the advisory lock")
	}
}

// TestApplyMigrationsOnSqliteNeverReachesForTheLock is the evidence behind the
// gate: run applyMigrations against sqlite and it fails — but on the tracking
// table's own Postgres-only DDL (`TIMESTAMPTZ DEFAULT NOW()`), never on a
// missing pg_advisory_lock. That is the honest claim. sqlite could not run this
// migration list before this change and still cannot; what matters is that the
// lock is not what stops it.
//
// Needs no database.
func TestApplyMigrationsOnSqliteNeverReachesForTheLock(t *testing.T) {
	s := newTestStore(t)

	err := applyMigrations(s.DB(), nil)
	if err == nil {
		t.Skip("sqlite now parses the tracking-table DDL; the lock assertion below is moot")
	}
	if strings.Contains(err.Error(), "advisory") {
		t.Fatalf("sqlite must never reach the advisory lock, got: %v", err)
	}
	if !strings.Contains(err.Error(), "create migration table") {
		t.Fatalf("expected the failure to be the Postgres-only tracking-table DDL, got: %v", err)
	}
}
