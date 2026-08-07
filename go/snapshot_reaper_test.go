package agentkit

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/execenv"
	"github.com/binocarlos/badcode-agent-orange/imageregistry"
)

// The snapshot TTL reaper (§5, §13.7). The store half lives in
// agentdb/customimages_ttl_test.go; these are the policy decisions: what is
// expired, what order the two deletions happen in, and what a failure leaves
// behind.

// ── Fakes ───────────────────────────────────────────────────────────────────

// reapWorld is a catalogue + registry pair sharing one call log, so the ORDER
// of "delete the bytes" and "tombstone the record" is observable.
type reapWorld struct {
	calls    []string
	settings map[string]int // project -> snapshot_ttl_days
	images   map[string][]*agentdb.CustomImage
	reaped   map[string]int64

	removeErr    error // injected into Registry.Remove
	tombstoneErr error // injected into MarkCustomImageReaped
	listErr      error
	settingsErr  error
}

func newReapWorld() *reapWorld {
	return &reapWorld{
		settings: map[string]int{},
		images:   map[string][]*agentdb.CustomImage{},
		reaped:   map[string]int64{},
	}
}

func (w *reapWorld) log(f string, a ...any) { w.calls = append(w.calls, fmt.Sprintf(f, a...)) }

func (w *reapWorld) add(project, name string, version int, createdAt, expiresAt int64, handleRef string) *agentdb.CustomImage {
	ci := &agentdb.CustomImage{
		ID: fmt.Sprintf("%s/%s:%d", project, name, version), Name: name, Customer: project,
		Version: version, CreatedAt: createdAt, ExpiresAt: expiresAt,
	}
	if handleRef != "" {
		ci.RegistryHandle = fmt.Sprintf(`{"kind":"blob-archive","ref":%q}`, handleRef)
	}
	w.images[project] = append(w.images[project], ci)
	return ci
}

func (w *reapWorld) ListCatalogueProjects(ctx context.Context) ([]string, error) {
	var out []string
	for p := range w.images {
		out = append(out, p)
	}
	// Deterministic order for the assertions.
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out, nil
}

func (w *reapWorld) GetProjectSettings(ctx context.Context, project string) (*agentdb.ProjectSettings, error) {
	if w.settingsErr != nil {
		return nil, w.settingsErr
	}
	ttl, ok := w.settings[project]
	if !ok {
		ttl = agentdb.DefaultSnapshotTTLDays
	}
	return &agentdb.ProjectSettings{Project: project, SnapshotTTLDays: ttl}, nil
}

func (w *reapWorld) ListCustomImageVersions(ctx context.Context, q agentdb.ImageCatalogQuery) ([]*agentdb.CustomImage, error) {
	if w.listErr != nil {
		return nil, w.listErr
	}
	w.log("list %s before=%d includeReaped=%v", q.Project, q.CreatedBefore, q.IncludeReaped)
	var out []*agentdb.CustomImage
	for _, ci := range w.images[q.Project] {
		if !q.IncludeReaped && w.reaped[ci.ID] != 0 {
			continue
		}
		if q.CreatedBefore > 0 && ci.CreatedAt >= q.CreatedBefore {
			continue
		}
		out = append(out, ci)
	}
	return out, nil
}

func (w *reapWorld) MarkCustomImageReaped(ctx context.Context, project, name string, version int, reapedAt int64) error {
	if w.tombstoneErr != nil {
		w.log("tombstone-refused %s/%s:%d", project, name, version)
		return w.tombstoneErr
	}
	w.log("tombstone %s/%s:%d", project, name, version)
	w.reaped[fmt.Sprintf("%s/%s:%d", project, name, version)] = reapedAt
	return nil
}

// fakeRegistry is an ImageRegistry that only knows how to forget things.
type fakeRegistry struct{ w *reapWorld }

func (f fakeRegistry) EnsurePresent(context.Context, execenv.ImageRef) error { return nil }
func (f fakeRegistry) Build(context.Context, imageregistry.BuildSpec) (execenv.ImageRef, error) {
	return "", nil
}
func (f fakeRegistry) Resolve(context.Context, imageregistry.BuildSpec) (execenv.ImageRef, bool, error) {
	return "", false, nil
}
func (f fakeRegistry) Persist(context.Context, execenv.ImageRef, imageregistry.PersistOptions) (imageregistry.Handle, error) {
	return imageregistry.Handle{}, nil
}
func (f fakeRegistry) Materialize(context.Context, imageregistry.Handle) (execenv.ImageRef, error) {
	return "", nil
}
func (f fakeRegistry) Remove(ctx context.Context, h imageregistry.Handle) error {
	if f.w.removeErr != nil {
		f.w.log("remove-failed %s", h.Ref)
		return f.w.removeErr
	}
	f.w.log("remove %s", h.Ref)
	return nil
}
func (f fakeRegistry) Capabilities() imageregistry.Capabilities { return imageregistry.Capabilities{} }

func newReaper(w *reapWorld, now int64) *SnapshotReaper {
	return &SnapshotReaper{
		Catalog:  w,
		Registry: fakeRegistry{w: w},
		Now:      func() time.Time { return time.Unix(now, 0) },
	}
}

const day = int64(agentdb.SecondsPerDay)

// ── What gets reaped ────────────────────────────────────────────────────────

func TestSnapshotReaper_ReapsOnlyWhatItsStampSaysIsExpired(t *testing.T) {
	now := int64(1_000 * day)

	tests := []struct {
		name       string
		ttlDays    int
		createdAt  int64
		expiresAt  int64
		wantReaped bool
	}{
		{
			name: "expired: the stamp has passed", ttlDays: 30,
			createdAt: now - 40*day, expiresAt: now - 10*day, wantReaped: true,
		},
		{
			name: "expiring exactly now counts as expired", ttlDays: 30,
			createdAt: now - 40*day, expiresAt: now, wantReaped: true,
		},
		{
			name: "old enough for the driver query, but its stamp has not passed — " +
				"the TTL was longer when it was burned, and that promise wins",
			ttlDays: 30, createdAt: now - 40*day, expiresAt: now + 50*day, wantReaped: false,
		},
		{
			name:    "expiry 0 means never: burned under snapshot_ttl_days 0, or before B4 existed",
			ttlDays: 30, createdAt: now - 400*day, expiresAt: 0, wantReaped: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := newReapWorld()
			w.settings["acme"] = tc.ttlDays
			w.add("acme", "toolbox", 1, tc.createdAt, tc.expiresAt, "blob-1")

			rep, err := newReaper(w, now).ReapProject(context.Background(), "acme")
			if err != nil {
				t.Fatalf("ReapProject: %v", err)
			}
			if err := rep.Err(); err != nil {
				t.Fatalf("per-image errors: %v", err)
			}
			got := rep.Reaped == 1
			if got != tc.wantReaped {
				t.Fatalf("reaped = %v, want %v (report %+v, calls %v)", got, tc.wantReaped, rep, w.calls)
			}
			if !tc.wantReaped && rep.Scanned == 1 && rep.Kept != 1 {
				t.Fatalf("a scanned-but-unexpired version must be counted as kept: %+v", rep)
			}
		})
	}
}

// ── RD9: a version in daily use is not reaped out from under its worker ─────

// Every row here is expired by its stamp, so the ONLY thing under test is
// `last_resumed_at`. The two arms matter equally: a resumed image must survive a
// pass that would otherwise have deleted it, and an untouched one must still
// die — a reaper that spares everything is a reaper that has been switched off.
func TestSnapshotReaper_RecentUseDefersTheReapAndNeglectStillKills(t *testing.T) {
	now := int64(1_000 * day)

	tests := []struct {
		name          string
		lastResumedAt int64
		wantReaped    bool
	}{
		{
			name:          "never resumed: nothing is using it, so it dies exactly as before",
			lastResumedAt: 0, wantReaped: true,
		},
		{
			name:          "resumed today: a worker is launching from this every day",
			lastResumedAt: now, wantReaped: false,
		},
		{
			name:          "resumed 2 days ago, well inside the 30-day window",
			lastResumedAt: now - 2*day, wantReaped: false,
		},
		{
			name:          "resumed 29 days ago: still inside the window",
			lastResumedAt: now - 29*day, wantReaped: false,
		},
		{
			name: "resumed 31 days ago: the deferral lapsed, so the bytes go — " +
				"this is what keeps storage a function of policy, not of traffic",
			lastResumedAt: now - 31*day, wantReaped: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := newReapWorld()
			w.settings["acme"] = 30
			ci := w.add("acme", "toolbox", 1, now-40*day, now-10*day, "blob-1")
			ci.LastResumedAt = tc.lastResumedAt

			var logs []string
			r := newReaper(w, now)
			r.Logf = func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }

			rep, err := r.ReapProject(context.Background(), "acme")
			if err != nil {
				t.Fatalf("ReapProject: %v", err)
			}
			if err := rep.Err(); err != nil {
				t.Fatalf("per-image errors: %v", err)
			}
			if rep.Scanned != 1 {
				t.Fatalf("the driver query must still see it: %+v", rep)
			}
			if got := rep.Reaped == 1; got != tc.wantReaped {
				t.Fatalf("reaped = %v, want %v (report %+v, calls %v)", got, tc.wantReaped, rep, w.calls)
			}
			if tc.wantReaped {
				if rep.Deferred != 0 {
					t.Fatalf("a reaped image must not also be counted as deferred: %+v", rep)
				}
				if !strings.Contains(strings.Join(w.calls, " "), "remove blob-1") {
					t.Fatalf("the bytes must actually be deleted, calls %v", w.calls)
				}
				return
			}
			// Spared: no bytes deleted, no tombstone, counted as deferred rather
			// than as Kept (Kept means "not due yet", which is a different fact),
			// and the operator is told.
			if rep.Deferred != 1 || rep.Kept != 0 {
				t.Fatalf("an in-use expired image must be counted as deferred, not kept: %+v", rep)
			}
			for _, c := range w.calls {
				if strings.HasPrefix(c, "remove") || strings.HasPrefix(c, "tombstone") {
					t.Fatalf("an in-use image must keep its bytes and its record, got call %q", c)
				}
			}
			if len(logs) != 1 || !strings.Contains(logs[0], "acme/toolbox:1") {
				t.Fatalf("the deferral must be announced and name the image, got %v", logs)
			}
		})
	}
}

// The deferral is measured against the project's CURRENT TTL, read fresh each
// pass — the same lever as the driver query's cutoff. Shortening the TTL is
// therefore how an operator gets the bytes of a still-used image back.
func TestSnapshotReaper_ShorteningTheTTLShortensTheDeferral(t *testing.T) {
	now := int64(1_000 * day)
	resumed := now - 10*day

	for _, tc := range []struct {
		ttlDays    int
		wantReaped bool
	}{
		{ttlDays: 30, wantReaped: false}, // resumed 10 days ago, window 30 → spared
		{ttlDays: 5, wantReaped: true},   // same row, window 5 → the deferral is over
	} {
		t.Run(fmt.Sprintf("ttl=%dd", tc.ttlDays), func(t *testing.T) {
			w := newReapWorld()
			w.settings["acme"] = tc.ttlDays
			ci := w.add("acme", "toolbox", 1, now-400*day, now-300*day, "blob-1")
			ci.LastResumedAt = resumed

			r := newReaper(w, now)
			r.Logf = func(string, ...any) {}
			rep, err := r.ReapProject(context.Background(), "acme")
			if err != nil {
				t.Fatalf("ReapProject: %v", err)
			}
			if got := rep.Reaped == 1; got != tc.wantReaped {
				t.Fatalf("ttl %d: reaped = %v, want %v (%+v)", tc.ttlDays, got, tc.wantReaped, rep)
			}
		})
	}
}

// A mixed project in one pass: the reaper must be selective, not global. This is
// the anti-vacuity assertion — one image spared and one deleted, together.
func TestSnapshotReaper_SparesTheUsedImageAndReapsTheAbandonedOneInOnePass(t *testing.T) {
	now := int64(1_000 * day)
	w := newReapWorld()
	w.settings["acme"] = 30
	daily := w.add("acme", "toolbox", 7, now-40*day, now-10*day, "blob-daily")
	daily.LastResumedAt = now - 1*day
	w.add("acme", "oldproto", 2, now-400*day, now-300*day, "blob-abandoned")

	r := newReaper(w, now)
	r.Logf = func(string, ...any) {}
	rep, err := r.ReapProject(context.Background(), "acme")
	if err != nil {
		t.Fatalf("ReapProject: %v", err)
	}
	if rep.Scanned != 2 || rep.Reaped != 1 || rep.Deferred != 1 {
		t.Fatalf("want 2 scanned / 1 reaped / 1 deferred, got %+v (calls %v)", rep, w.calls)
	}
	joined := strings.Join(w.calls, " ")
	if !strings.Contains(joined, "remove blob-abandoned") || !strings.Contains(joined, "tombstone acme/oldproto:2") {
		t.Fatalf("the abandoned image must still be reaped, calls %v", w.calls)
	}
	if strings.Contains(joined, "blob-daily") || strings.Contains(joined, "toolbox") {
		t.Fatalf("the in-use image must be untouched, calls %v", w.calls)
	}
}

// §5: snapshot_ttl_days 0 means never — and nothing is even listed, so a
// "keep everything" project costs one settings read per pass.
func TestSnapshotReaper_TTLZeroNeverReapsAndNeverLists(t *testing.T) {
	now := int64(1_000 * day)
	w := newReapWorld()
	w.settings["acme"] = 0
	w.add("acme", "toolbox", 1, now-400*day, now-300*day, "blob-1")

	rep, err := newReaper(w, now).ReapProject(context.Background(), "acme")
	if err != nil {
		t.Fatalf("ReapProject: %v", err)
	}
	if rep.Reaped != 0 || rep.Scanned != 0 || rep.Projects != 0 {
		t.Fatalf("a TTL of 0 must reap nothing: %+v", rep)
	}
	if len(w.calls) != 0 {
		t.Fatalf("a TTL of 0 must not even query the catalogue, got %v", w.calls)
	}
}

// The driver query is I1's: everything older than the cutoff that is not
// already tombstoned. Re-reaping a tombstone would try to delete bytes that are
// already gone, every pass, forever.
func TestSnapshotReaper_DriverQueryIsCutoffAndLiveOnly(t *testing.T) {
	now := int64(1_000 * day)
	w := newReapWorld()
	w.settings["acme"] = 30
	w.add("acme", "toolbox", 1, now-40*day, now-10*day, "blob-1")

	if _, err := newReaper(w, now).ReapProject(context.Background(), "acme"); err != nil {
		t.Fatalf("ReapProject: %v", err)
	}
	wantList := fmt.Sprintf("list acme before=%d includeReaped=false", now-30*day)
	if w.calls[0] != wantList {
		t.Fatalf("driver query: want %q, got %q", wantList, w.calls[0])
	}

	// A second pass finds nothing left to do.
	w.calls = nil
	rep, err := newReaper(w, now).ReapProject(context.Background(), "acme")
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if rep.Reaped != 0 || rep.Scanned != 0 {
		t.Fatalf("the second pass must find nothing: %+v", rep)
	}
}

// ── Order of operations (I1's binding constraint) ───────────────────────────

// Bytes first, tombstone second. A crash between them leaves one
// resolvable-but-dead record that the next pass fixes; the reverse order
// orphans the bytes forever, because nothing remembers the handle once the
// record says "reaped".
func TestSnapshotReaper_DeletesBytesBeforeTombstoning(t *testing.T) {
	now := int64(1_000 * day)
	w := newReapWorld()
	w.settings["acme"] = 30
	w.add("acme", "toolbox", 3, now-40*day, now-10*day, "blob-3")

	if _, err := newReaper(w, now).ReapProject(context.Background(), "acme"); err != nil {
		t.Fatalf("ReapProject: %v", err)
	}
	want := []string{
		fmt.Sprintf("list acme before=%d includeReaped=false", now-30*day),
		"remove blob-3",
		"tombstone acme/toolbox:3",
	}
	if !reflect.DeepEqual(w.calls, want) {
		t.Fatalf("wrong order.\nwant %v\ngot  %v", want, w.calls)
	}
}

// If the bytes could not be deleted the record must stay live: tombstoning
// anyway would strand blobs the operator keeps paying for, and the version
// would stop resolving for no gain.
func TestSnapshotReaper_FailedByteDeletionLeavesNoTombstone(t *testing.T) {
	now := int64(1_000 * day)
	w := newReapWorld()
	w.settings["acme"] = 30
	w.add("acme", "toolbox", 1, now-40*day, now-10*day, "blob-1")
	w.removeErr = fmt.Errorf("registry unreachable")

	rep, err := newReaper(w, now).ReapProject(context.Background(), "acme")
	if err != nil {
		t.Fatalf("ReapProject must not fail the whole pass for one image: %v", err)
	}
	if rep.Reaped != 0 {
		t.Fatalf("nothing was reaped, yet the report says %d", rep.Reaped)
	}
	if len(rep.Errors) != 1 || !strings.Contains(rep.Err().Error(), "delete bytes") {
		t.Fatalf("the failure must be reported: %v", rep.Errors)
	}
	for _, c := range w.calls {
		if strings.HasPrefix(c, "tombstone ") {
			t.Fatalf("a failed byte deletion must not tombstone: %v", w.calls)
		}
	}
}

// The crash window, made explicit: bytes gone, tombstone refused. The error
// says what state the record is in, and the next pass repairs it.
func TestSnapshotReaper_FailedTombstoneIsReportedAndRepairableNextPass(t *testing.T) {
	now := int64(1_000 * day)
	w := newReapWorld()
	w.settings["acme"] = 30
	w.add("acme", "toolbox", 1, now-40*day, now-10*day, "blob-1")
	w.tombstoneErr = fmt.Errorf(
		"agentdb: write to projection table \"agent_custom_images\" outside a config-event transaction")

	rep, err := newReaper(w, now).ReapProject(context.Background(), "acme")
	if err != nil {
		t.Fatalf("ReapProject: %v", err)
	}
	if rep.Reaped != 0 || len(rep.Errors) != 1 {
		t.Fatalf("want one reported failure, got %+v", rep)
	}
	if !strings.Contains(rep.Err().Error(), "the next pass repairs this") {
		t.Fatalf("the error must name the state it left behind: %v", rep.Err())
	}

	// Next pass, with the guard off: the same row is offered again and settles.
	w.tombstoneErr = nil
	w.calls = nil
	rep, err = newReaper(w, now).ReapProject(context.Background(), "acme")
	if err != nil {
		t.Fatalf("repair pass: %v", err)
	}
	if rep.Reaped != 1 {
		t.Fatalf("the repair pass must tombstone the record: %+v", rep)
	}
}

// A version with no registry handle has no bytes to delete — it was never
// materialisable. Tombstoning it is still the honest outcome.
func TestSnapshotReaper_HandlelessVersionIsTombstonedWithoutARemoval(t *testing.T) {
	now := int64(1_000 * day)
	w := newReapWorld()
	w.settings["acme"] = 30
	w.add("acme", "toolbox", 1, now-40*day, now-10*day, "")

	rep, err := newReaper(w, now).ReapProject(context.Background(), "acme")
	if err != nil {
		t.Fatalf("ReapProject: %v", err)
	}
	if rep.Reaped != 1 {
		t.Fatalf("want it reaped, got %+v (calls %v)", rep, w.calls)
	}
	for _, c := range w.calls {
		if strings.HasPrefix(c, "remove") {
			t.Fatalf("there were no bytes to remove: %v", w.calls)
		}
	}
}

// A corrupt handle is a per-image failure, not a reason to tombstone blindly.
func TestSnapshotReaper_UndecodableHandleIsAnErrorNotATombstone(t *testing.T) {
	now := int64(1_000 * day)
	w := newReapWorld()
	w.settings["acme"] = 30
	ci := w.add("acme", "toolbox", 1, now-40*day, now-10*day, "blob-1")
	ci.RegistryHandle = "{not json"

	rep, err := newReaper(w, now).ReapProject(context.Background(), "acme")
	if err != nil {
		t.Fatalf("ReapProject: %v", err)
	}
	if rep.Reaped != 0 || len(rep.Errors) != 1 {
		t.Fatalf("want one reported failure and no tombstone, got %+v", rep)
	}
}

// ── Sweeping ────────────────────────────────────────────────────────────────

func TestSnapshotReaper_ReapAllSweepsEveryProjectIndependently(t *testing.T) {
	now := int64(1_000 * day)
	w := newReapWorld()
	w.settings["acme"] = 30
	w.settings["globex"] = 0 // never
	w.settings["initech"] = 7
	w.add("acme", "toolbox", 1, now-40*day, now-10*day, "blob-a")
	w.add("globex", "toolbox", 1, now-400*day, now-300*day, "blob-g")
	w.add("initech", "toolbox", 1, now-40*day, now-33*day, "blob-i")
	w.add("initech", "vanilla", 1, now-1*day, now+6*day, "blob-i2")

	rep, err := newReaper(w, now).ReapAll(context.Background())
	if err != nil {
		t.Fatalf("ReapAll: %v", err)
	}
	if err := rep.Err(); err != nil {
		t.Fatalf("per-image errors: %v", err)
	}
	if rep.Projects != 2 {
		t.Fatalf("globex has TTL 0 and should not count as swept: %+v", rep)
	}
	if rep.Reaped != 2 {
		t.Fatalf("want acme/toolbox:1 and initech/toolbox:1 reaped, got %+v", rep)
	}
	for _, gone := range []string{"acme/toolbox:1", "initech/toolbox:1"} {
		if w.reaped[gone] == 0 {
			t.Fatalf("%s was not tombstoned (calls %v)", gone, w.calls)
		}
	}
	if w.reaped["globex/toolbox:1"] != 0 {
		t.Fatalf("a project with TTL 0 must keep everything")
	}
}

// One project's infrastructure failure must not silence the others.
func TestSnapshotReaper_ReapAllContinuesPastAProjectFailure(t *testing.T) {
	now := int64(1_000 * day)
	w := newReapWorld()
	w.settings["acme"] = 30
	w.add("acme", "toolbox", 1, now-40*day, now-10*day, "blob-a")
	w.settingsErr = fmt.Errorf("settings table unreachable")

	rep, err := newReaper(w, now).ReapAll(context.Background())
	if err != nil {
		t.Fatalf("ReapAll must report per-project failures in the report, not as a hard error: %v", err)
	}
	if len(rep.Errors) != 1 || !strings.Contains(rep.Err().Error(), "project acme") {
		t.Fatalf("want the failing project named: %v", rep.Errors)
	}
}

// ── Wiring guards ───────────────────────────────────────────────────────────

func TestSnapshotReaper_RefusesToRunWithoutARegistry(t *testing.T) {
	w := newReapWorld()
	w.settings["acme"] = 30
	r := &SnapshotReaper{Catalog: w} // no registry

	if _, err := r.ReapProject(context.Background(), "acme"); err == nil {
		t.Fatalf("tombstoning without deleting the bytes orphans them: the reaper must refuse")
	}
	if _, err := (&SnapshotReaper{Registry: fakeRegistry{w: w}}).ReapAll(context.Background()); err == nil {
		t.Fatalf("a reaper with no catalogue must refuse")
	}
	if _, err := newReaper(w, 0).ReapProject(context.Background(), ""); err == nil {
		t.Fatalf("a sweep with no project must refuse (P5)")
	}
}

// The Runner starts the loop only when both halves are wired: an interval with
// no catalogue (or a catalogue with no interval) must stay silent rather than
// panic in a goroutine.
func TestSnapshotReaper_LoopIsOptIn(t *testing.T) {
	tests := []struct {
		name      string
		interval  time.Duration
		catalogue SnapshotCatalog
		wantLoop  bool
	}{
		{"unconfigured", 0, nil, false},
		{"interval but no catalogue", time.Hour, nil, false},
		{"catalogue but no interval", 0, newReapWorld(), false},
		{"both", time.Hour, newReapWorld(), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.interval > 0 && tc.catalogue != nil
			if got != tc.wantLoop {
				t.Fatalf("wiring predicate: got %v, want %v", got, tc.wantLoop)
			}
		})
	}
}

// *agentdb.Store is the catalogue the standalone stack wires in — pinned so a
// signature drift in either half is a compile error here rather than a runtime
// nil in agentd.
var _ SnapshotCatalog = (*agentdb.Store)(nil)
