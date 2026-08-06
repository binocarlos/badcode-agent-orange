package agentdb

import (
	"context"
	"strings"
	"testing"

	"gorm.io/gorm"
)

// The config log's append-only promise, tested where the promise now lives:
// in the DATABASE (migration 039, RD13).
//
// These cases deliberately use RAW SQL through Store.DB() rather than the store
// methods, because that is the hole they close. InstallConfigEventGuard is a
// gorm callback: it is opt-in, agentd never arms it, and it cannot see a
// statement that does not go through gorm's model path at all. Proving an
// append-only guarantee with an in-process guard proves only that the guard is
// installed in the test — which is exactly the state RD13 found.
//
// Postgres-only by construction: sqlite test stores build their schema with
// AutoMigrate and never run migration 039, so there is nothing to assert there.
func TestConfigEventsAreAppendOnly_LivePG(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	project := liveConfigLogProject(t, s)

	ev, err := s.WithConfigEvent(ctx, ConfigChange{
		Project: project,
		Action:  ActionWorkerCreate,
		Payload: JSONMap{"name": "auditor", "project": project},
	}, func(tx *gorm.DB) error { return nil })
	if err != nil {
		t.Fatalf("append config event: %v", err)
	}

	t.Run("raw UPDATE of the record is refused", func(t *testing.T) {
		err := s.DB().Exec(
			"UPDATE config_events SET payload = '{\"name\":\"rewritten\"}'::jsonb WHERE id = ?", ev.ID,
		).Error
		if err == nil {
			t.Fatal("UPDATE of a config_events payload succeeded — the log is not append-only")
		}
		if !strings.Contains(err.Error(), "append-only") {
			t.Fatalf("refused, but not by the append-only trigger: %v", err)
		}
		// And the record is unchanged, not merely un-erroring.
		var payload string
		if err := s.DB().Raw("SELECT payload::text FROM config_events WHERE id = ?", ev.ID).
			Scan(&payload).Error; err != nil {
			t.Fatalf("read back payload: %v", err)
		}
		if !strings.Contains(payload, "auditor") {
			t.Fatalf("payload was rewritten despite the error: %s", payload)
		}
	})

	t.Run("raw UPDATE of the rationale is refused", func(t *testing.T) {
		// The field a bad actor would most want to change: WHY a change was made.
		err := s.DB().Exec(
			"UPDATE config_events SET rationale = 'because I said so' WHERE id = ?", ev.ID,
		).Error
		if err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Fatalf("rationale rewrite was not refused by the trigger: %v", err)
		}
	})

	t.Run("raw DELETE is refused", func(t *testing.T) {
		err := s.DB().Exec("DELETE FROM config_events WHERE id = ?", ev.ID).Error
		if err == nil {
			t.Fatal("DELETE from config_events succeeded — the log is not append-only")
		}
		if !strings.Contains(err.Error(), "append-only") {
			t.Fatalf("refused, but not by the append-only trigger: %v", err)
		}
		var n int64
		if err := s.DB().Raw("SELECT count(*) FROM config_events WHERE id = ?", ev.ID).
			Scan(&n).Error; err != nil {
			t.Fatalf("count after refused delete: %v", err)
		}
		if n != 1 {
			t.Fatalf("record gone after a refused DELETE (count %d)", n)
		}
	})

	t.Run("a project-wide raw DELETE is refused too", func(t *testing.T) {
		// The shape every live-PG test's cleanup used to have. If this ever
		// starts passing, every one of those cleanups is silently erasing the
		// audit log again.
		err := s.DB().Exec("DELETE FROM config_events WHERE project = ?", project).Error
		if err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Fatalf("project-wide DELETE was not refused: %v", err)
		}
	})

	t.Run("emitted_at is the one column that may change", func(t *testing.T) {
		if err := s.MarkConfigEventEmitted(ctx, ev.ID); err != nil {
			t.Fatalf("MarkConfigEventEmitted: %v", err)
		}
		var emitted int64
		if err := s.DB().Raw("SELECT emitted_at FROM config_events WHERE id = ?", ev.ID).
			Scan(&emitted).Error; err != nil {
			t.Fatalf("read emitted_at: %v", err)
		}
		if emitted == 0 {
			t.Fatal("emitted_at was not stamped — the trigger is refusing the one legal update")
		}
		// Raw SQL too: the watermark is legal whoever writes it, because it is
		// the sweep's column and a repair pass must not need a store method.
		if err := s.DB().Exec("UPDATE config_events SET emitted_at = 12345 WHERE id = ?", ev.ID).Error; err != nil {
			t.Fatalf("raw emitted_at update refused: %v", err)
		}
	})

	t.Run("PurgeConfigEvents is the sanctioned way out", func(t *testing.T) {
		if err := s.PurgeConfigEvents(ctx, project); err != nil {
			t.Fatalf("PurgeConfigEvents: %v", err)
		}
		var n int64
		if err := s.DB().Raw("SELECT count(*) FROM config_events WHERE project = ?", project).
			Scan(&n).Error; err != nil {
			t.Fatalf("count after purge: %v", err)
		}
		if n != 0 {
			t.Fatalf("purge left %d rows", n)
		}
		// And the escape does NOT leak: it is SET LOCAL inside the purge's own
		// transaction, so the very next statement on the pool is refused again.
		// (A leak here would be worse than no trigger, because it would look
		// like protection.)
		next, err := s.WithConfigEvent(ctx, ConfigChange{
			Project: project,
			Action:  ActionWorkerCreate,
			Payload: JSONMap{"name": "after-purge", "project": project},
		}, func(tx *gorm.DB) error { return nil })
		if err != nil {
			t.Fatalf("append after purge: %v", err)
		}
		if err := s.DB().Exec("DELETE FROM config_events WHERE id = ?", next.ID).Error; err == nil {
			t.Fatal("the purge escape leaked past its transaction — a later raw DELETE succeeded")
		}
	})
}
