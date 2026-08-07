package agentdb

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The §5 snapshot metadata tuple {source session, created_at, expiry,
// last_resumed_at} and the store half of the B4 reaper.

// ── The expiry rule (§5) ────────────────────────────────────────────────────

func TestSnapshotTTL_ExpiryRule(t *testing.T) {
	const created = 1_000_000
	tests := []struct {
		name    string
		ttlDays int
		want    int64
	}{
		{"the default horizon", DefaultSnapshotTTLDays, created + 30*SecondsPerDay},
		{"one day", 1, created + SecondsPerDay},
		{"zero means never — the snapshot is kept forever", 0, 0},
		{"a negative TTL cannot mean 'expired already'", -5, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := SnapshotExpiry(created, tc.ttlDays); got != tc.want {
				t.Fatalf("SnapshotExpiry(%d, %d) = %d, want %d", created, tc.ttlDays, got, tc.want)
			}
		})
	}
	if got := SnapshotExpiry(0, 30); got != 0 {
		t.Fatalf("an unstamped creation time cannot produce an expiry, got %d", got)
	}
}

// ── The metadata is stamped at burn time, from the project's TTL ────────────

func TestSnapshotTTL_BurnStampsTheWholeTuple(t *testing.T) {
	s := newImageCatalogueTestStore(t)
	ctx := context.Background()

	tests := []struct {
		name       string
		ttlDays    int
		setSetting bool
		wantExpiry func(createdAt int64) int64
	}{
		{
			name:       "no settings row: the §5 default of 30 days applies",
			wantExpiry: func(c int64) int64 { return c + DefaultSnapshotTTLDays*SecondsPerDay },
		},
		{
			name: "a project TTL of 7 days", ttlDays: 7, setSetting: true,
			wantExpiry: func(c int64) int64 { return c + 7*SecondsPerDay },
		},
		{
			name:    "a project TTL of 0 means never — no expiry is stamped at all",
			ttlDays: 0, setSetting: true,
			wantExpiry: func(int64) int64 { return 0 },
		},
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			project := "acme-" + strings.ReplaceAll(tc.name, " ", "-")
			if tc.setSetting {
				if _, err := s.PutProjectSettings(ctx, &ProjectSettings{
					Project: project, SnapshotTTLDays: tc.ttlDays,
				}, ConfigWrite{}); err != nil {
					t.Fatalf("put settings: %v", err)
				}
			}
			ci := burn(t, s, project, "toolbox", LabelSet{"purpose": "test"})

			if want := tc.wantExpiry(ci.CreatedAt); ci.ExpiresAt != want {
				t.Fatalf("expires_at = %d, want %d (created_at %d)", ci.ExpiresAt, want, ci.CreatedAt)
			}
			// The rest of the tuple.
			if ci.CreatedAt == 0 {
				t.Fatalf("created_at must be stamped")
			}
			if ci.CreatedBySession != "s-toolbox" {
				t.Fatalf("source session must be the burning session, got %q", ci.CreatedBySession)
			}
			if ci.LastResumedAt != 0 {
				t.Fatalf("a fresh burn has never been resumed, got %d", ci.LastResumedAt)
			}
			if ci.ReapedAt != 0 {
				t.Fatalf("a fresh burn is not tombstoned")
			}
			_ = i
		})
	}
}

// The expiry is a PROMISE made at burn time: changing the setting afterwards
// does not retroactively move it. Without this the operator could shorten a TTL
// and silently delete images someone had been told would live for a month.
func TestSnapshotTTL_ExpiryIsStampedNotRecomputed(t *testing.T) {
	s := newImageCatalogueTestStore(t)
	ctx := context.Background()
	const project = "acme"

	if _, err := s.PutProjectSettings(ctx, &ProjectSettings{Project: project, SnapshotTTLDays: 30}, ConfigWrite{}); err != nil {
		t.Fatalf("put settings: %v", err)
	}
	first := burn(t, s, project, "toolbox", nil)

	if _, err := s.PutProjectSettings(ctx, &ProjectSettings{Project: project, SnapshotTTLDays: 1}, ConfigWrite{}); err != nil {
		t.Fatalf("shorten TTL: %v", err)
	}
	second := burn(t, s, project, "toolbox", nil)

	reread, err := s.GetCustomImageVersion(ctx, project, "toolbox", first.Version)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if reread.ExpiresAt != first.ExpiresAt {
		t.Fatalf("the earlier version's expiry moved: %d -> %d", first.ExpiresAt, reread.ExpiresAt)
	}
	if second.ExpiresAt != second.CreatedAt+SecondsPerDay {
		t.Fatalf("the new burn should use the NEW ttl, got %d", second.ExpiresAt)
	}
	if second.ExpiresAt >= first.ExpiresAt {
		t.Fatalf("the shorter TTL should expire sooner: %d vs %d", second.ExpiresAt, first.ExpiresAt)
	}
}

// A caller cannot choose its own expiry — a tool that could would be opting out
// of the operator's storage policy.
func TestSnapshotTTL_CallerSuppliedExpiryIsOverwritten(t *testing.T) {
	s := newImageCatalogueTestStore(t)
	ctx := context.Background()

	ci, err := s.CreateCustomImage(ctx, &CustomImage{
		Name: "toolbox", Customer: "acme", RegistryHandle: `{"kind":"blob-archive","ref":"b1"}`,
		ExpiresAt: 1, LastResumedAt: 99,
	}, ConfigWrite{Worker: "curator", Session: "s-1"})
	if err != nil {
		t.Fatalf("burn: %v", err)
	}
	if ci.ExpiresAt != ci.CreatedAt+DefaultSnapshotTTLDays*SecondsPerDay {
		t.Fatalf("caller-supplied expiry survived: %d", ci.ExpiresAt)
	}
	if ci.LastResumedAt != 0 {
		t.Fatalf("caller-supplied last_resumed_at survived: %d", ci.LastResumedAt)
	}
}

// ── last_resumed_at ─────────────────────────────────────────────────────────

func TestSnapshotTTL_MarkResumedStampsWithoutExtendingTheExpiry(t *testing.T) {
	s := newImageCatalogueTestStore(t)
	ctx := context.Background()
	const project = "acme"

	ci := burn(t, s, project, "toolbox", nil)
	at := time.Now().Unix() + 5

	if err := s.MarkCustomImageResumed(ctx, project, "toolbox", ci.Version, at); err != nil {
		t.Fatalf("mark resumed: %v", err)
	}
	got, err := s.GetCustomImageVersion(ctx, project, "toolbox", ci.Version)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if got.LastResumedAt != at {
		t.Fatalf("last_resumed_at = %d, want %d", got.LastResumedAt, at)
	}
	// The stamp does not REWRITE expires_at: §5 sets it at snapshot time and the
	// row keeps its promise. The reaper nonetheless honours recent use by
	// DEFERRING the reap (RD9, agentkit.snapshotInUse) — that decision lives in
	// the reaper, not in this column, which is why this assertion still holds.
	if got.ExpiresAt != ci.ExpiresAt {
		t.Fatalf("resuming must NOT rewrite the expiry (§5 sets it at snapshot time): %d -> %d",
			ci.ExpiresAt, got.ExpiresAt)
	}
	// It is a stamp, not an append: a second resume overwrites the first.
	later := at + 100
	if err := s.MarkCustomImageResumed(ctx, project, "toolbox", ci.Version, later); err != nil {
		t.Fatalf("second resume: %v", err)
	}
	got, _ = s.GetCustomImageVersion(ctx, project, "toolbox", ci.Version)
	if got.LastResumedAt != later {
		t.Fatalf("last_resumed_at = %d, want %d", got.LastResumedAt, later)
	}

	// Unknown versions and cross-project stamps fail rather than no-op.
	if err := s.MarkCustomImageResumed(ctx, project, "toolbox", 99, at); err == nil {
		t.Fatalf("stamping a version that does not exist must fail")
	}
	if err := s.MarkCustomImageResumed(ctx, "globex", "toolbox", ci.Version, at); err == nil {
		t.Fatalf("stamping across projects must fail (P5)")
	}
	if err := s.MarkCustomImageResumed(ctx, "", "toolbox", ci.Version, at); err == nil {
		t.Fatalf("a stamp without a project must fail (P5)")
	}
}

// ── The reaper's driver queries ─────────────────────────────────────────────

func TestSnapshotReaper_DriverQueryFindsStaleLiveVersionsOnly(t *testing.T) {
	s := newImageCatalogueTestStore(t)
	ctx := context.Background()
	const project = "acme"

	now := time.Now().Unix()
	old := burn(t, s, project, "old", nil)
	fresh := burn(t, s, project, "fresh", nil)

	// Age the first one by hand: burns are stamped with the wall clock.
	if err := s.gdb.Model(&CustomImage{}).Where("id = ?", old.ID).
		Updates(map[string]any{
			"created_at": now - 40*SecondsPerDay,
			"expires_at": now - 10*SecondsPerDay,
		}).Error; err != nil {
		t.Fatalf("age the old image: %v", err)
	}

	cutoff := now - 30*SecondsPerDay
	stale, err := s.ListCustomImageVersions(ctx, ImageCatalogQuery{Project: project, CreatedBefore: cutoff})
	if err != nil {
		t.Fatalf("list stale: %v", err)
	}
	if len(stale) != 1 || stale[0].Name != "old" {
		t.Fatalf("the driver query must return only versions older than the cutoff, got %d", len(stale))
	}
	if stale[0].ExpiresAt != now-10*SecondsPerDay {
		t.Fatalf("the reaper needs the stamped expiry on the row it scans")
	}

	// Once tombstoned it is not offered again — no re-reaping.
	if err := s.MarkCustomImageReaped(ctx, project, "old", stale[0].Version, now); err != nil {
		t.Fatalf("tombstone: %v", err)
	}
	stale, err = s.ListCustomImageVersions(ctx, ImageCatalogQuery{Project: project, CreatedBefore: cutoff})
	if err != nil {
		t.Fatalf("list after tombstone: %v", err)
	}
	if len(stale) != 0 {
		t.Fatalf("a tombstoned version must not be offered to the reaper again, got %d", len(stale))
	}
	// …and the fresh one was never in scope.
	if _, err := s.ResolveCustomImage(ctx, project, "fresh"); err != nil {
		t.Fatalf("the fresh image must still resolve: %v", err)
	}
	_ = fresh
}

func TestSnapshotReaper_ListCatalogueProjects(t *testing.T) {
	s := newImageCatalogueTestStore(t)
	ctx := context.Background()

	burn(t, s, "acme", "toolbox", nil)
	burn(t, s, "acme", "vanilla", nil)
	globex := burn(t, s, "globex", "toolbox", nil)

	got, err := s.ListCatalogueProjects(ctx)
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"acme", "globex"}) {
		t.Fatalf("want [acme globex], got %v", got)
	}

	// A project with nothing left to reap drops off the list.
	if err := s.MarkCustomImageReaped(ctx, "globex", "toolbox", globex.Version, 0); err != nil {
		t.Fatalf("tombstone: %v", err)
	}
	got, err = s.ListCatalogueProjects(ctx)
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"acme"}) {
		t.Fatalf("want [acme], got %v", got)
	}
}

// A tombstoned version stops resolving — the whole point of tombstoning rather
// than exempting (§13.7). Resolution says WHAT happened rather than sliding
// back to an older live version.
func TestSnapshotReaper_ReapedVersionStopsResolvingAndKeepsItsNumber(t *testing.T) {
	s := newImageCatalogueTestStore(t)
	ctx := context.Background()
	const project = "acme"

	v1 := burn(t, s, project, "toolbox", nil)
	v2 := burn(t, s, project, "toolbox", nil)
	if err := s.MarkCustomImageReaped(ctx, project, "toolbox", v2.Version, 0); err != nil {
		t.Fatalf("tombstone: %v", err)
	}

	_, err := s.ResolveCustomImage(ctx, project, "toolbox")
	if err == nil {
		t.Fatalf("a floating ref whose newest version was reaped must error, not slide back to v%d", v1.Version)
	}
	if !strings.Contains(err.Error(), "reaped") {
		t.Fatalf("the error must say what happened: %v", err)
	}
	// The number is never reissued: the next burn is v3.
	if next := burn(t, s, project, "toolbox", nil); next.Version != v2.Version+1 {
		t.Fatalf("reaping must not make a version number reusable: got %d", next.Version)
	}
}

// The constraint I1 recorded and B4 must honour: MarkCustomImageReaped writes a
// GUARDED projection table outside the config-event seam, on purpose (§15.3 has
// no verb for storage GC). So the reaper cannot run against a store with the
// write guard armed — and that is a property, not a comment.
func TestSnapshotReaper_TombstoneRefusedUnderTheConfigEventGuard(t *testing.T) {
	s := newImageCatalogueTestStore(t)
	ctx := context.Background()
	const project = "acme"

	ci := burn(t, s, project, "toolbox", nil)

	// The claim is only meaningful if the table really is guarded.
	var guarded bool
	for _, tbl := range ConfigGuardedTables() {
		if tbl == "agent_custom_images" {
			guarded = true
		}
	}
	if !guarded {
		t.Fatalf("agent_custom_images is no longer guarded — this test proves nothing; " +
			"check what changed in ConfigMutations")
	}

	if err := InstallConfigEventGuard(s.gdb); err != nil {
		t.Fatalf("install guard: %v", err)
	}
	err := s.MarkCustomImageReaped(ctx, project, "toolbox", ci.Version, 0)
	if err == nil {
		t.Fatalf("with the guard armed the tombstone must fail — the reaper must run on an unguarded store")
	}
	if !strings.Contains(err.Error(), "outside a config-event transaction") {
		t.Fatalf("unexpected failure: %v", err)
	}
	// And it really did not write: the version still resolves.
	if _, err := s.ResolveCustomImage(ctx, project, "toolbox"); err != nil {
		t.Fatalf("a refused tombstone must leave the row live: %v", err)
	}
	// Same for the resume stamp, the other runtime write on this table.
	if err := s.MarkCustomImageResumed(ctx, project, "toolbox", ci.Version, 0); err == nil {
		t.Fatalf("the resume stamp is a guarded-table write too and must fail the same way")
	}
}
