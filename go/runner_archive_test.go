package agentkit

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/agentkittest"
	"github.com/binocarlos/badcode-agent-orange/artifacts"
	"github.com/binocarlos/badcode-agent-orange/events"
	"github.com/binocarlos/badcode-agent-orange/execenv"
	dockerdind "github.com/binocarlos/badcode-agent-orange/execenv/docker"
	"github.com/binocarlos/badcode-agent-orange/imageregistry"
)

// Session garbage collection: the archive loop (idle container → snapshot →
// release) and the snapshot TTL reaper. Both mechanisms shipped long ago and
// neither had ever run in the standalone stack, because agentd set neither
// Policy field. These tests cover the reclamation policy itself; the two
// variables that switch it on live in cmd/agentd/gc.go with their own table.

// ── The reproduction ─────────────────────────────────────────────────────────
//
// Start gates BOTH loops on Policy, and a host that configures nothing sets
// neither field:
//
//	Policy.ArchiveTimeout       > 0                     → archiveLoop
//	Policy.SnapshotReapInterval > 0 && Snapshots != nil → snapshotReapLoop
//
// The consequence is the one this stack fought all night: a session holds its
// container — and one of the host's 100 host ports — from creation until
// somebody explicitly deletes it.
//
// This test pins the "before" state. It asserts the three gate values AND
// observes the outcome: a session idle for a day still holds a running
// container after Start. (The observation alone would be weak — the sweep runs
// on a timer — so the gate assertions carry the proof: with these values Start
// launches no loop at all, so no amount of waiting would change the outcome.)
func TestArchiveIsOffWithTodaysConfig(t *testing.T) {
	ctx := context.Background()
	r, env, _, store, _, _ := newTestRunner(t)
	store.Seed(&agentdb.Session{ID: "s-idle", Customer: "acme", Job: "j1"})

	if got := r.deps.Policy.ArchiveTimeout; got != 0 {
		t.Fatalf("Policy.ArchiveTimeout = %v, want 0 (the default this test describes)", got)
	}
	if got := r.deps.Policy.SnapshotReapInterval; got != 0 {
		t.Fatalf("Policy.SnapshotReapInterval = %v, want 0", got)
	}
	if r.deps.Snapshots != nil {
		t.Fatalf("Deps.Snapshots = %v, want nil", r.deps.Snapshots)
	}

	if _, err := r.CreateSession(ctx, CreateSessionRequest{SessionID: "s-idle", Customer: "acme", Job: "j1"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer r.Close() //nolint:errcheck

	// A day of idleness.
	r.setIdle("s-idle", 24*time.Hour)
	time.Sleep(250 * time.Millisecond)

	if n := env.Count("Snapshot"); n != 0 {
		t.Fatalf("Snapshot calls = %d, want 0 — nothing archives a session with this config", n)
	}
	inst := r.get("s-idle")
	if inst == nil || inst.State != execenv.StateRunning {
		t.Fatalf("session container = %+v, want a still-running instance", inst)
	}
	t.Logf("a session idle for 24h still holds its container (and its host port): state=%s", inst.State)

	// Even if the sweep were somehow driven, the zero timeout must not be read
	// as "everything is idle" — idleSessions(0) matches every tracked session.
	r.archiveIdleOnce(ctx)
	if n := env.Count("Snapshot"); n != 0 {
		t.Fatalf("a zero ArchiveTimeout archived %d session(s) — 0 means off, not now", n)
	}
}

// ── Reclamation ──────────────────────────────────────────────────────────────

// portLeasingEnv is a MockExecutionEnvironment in front of the REAL DinD port
// allocator, so "the port was released" is asserted against the same code that
// leases host ports in production rather than against a stand-in.
type portLeasingEnv struct {
	*execenv.MockExecutionEnvironment
	ports    *dockerdind.PortAllocator
	sessions map[execenv.InstanceID]string
}

func newPortLeasingEnv(t *testing.T, start, end int) *portLeasingEnv {
	t.Helper()
	pa, err := dockerdind.NewPortAllocator(start, end)
	if err != nil {
		t.Fatalf("NewPortAllocator: %v", err)
	}
	return &portLeasingEnv{
		MockExecutionEnvironment: execenv.NewMock(),
		ports:                    pa,
		sessions:                 map[execenv.InstanceID]string{},
	}
}

func (e *portLeasingEnv) Provision(ctx context.Context, spec execenv.ProvisionSpec) (*execenv.Instance, error) {
	if _, err := e.ports.Allocate(spec.SessionID); err != nil {
		// Shaped like the DinD adapter's: the allocator's message, wrapped.
		return nil, err
	}
	inst, err := e.MockExecutionEnvironment.Provision(ctx, spec)
	if err != nil {
		e.ports.Release(spec.SessionID)
		return nil, err
	}
	e.sessions[inst.ID] = spec.SessionID
	return inst, nil
}

func (e *portLeasingEnv) Destroy(ctx context.Context, id execenv.InstanceID, opts execenv.DestroyOptions) error {
	if err := e.MockExecutionEnvironment.Destroy(ctx, id, opts); err != nil {
		return err
	}
	e.ports.Release(e.sessions[id])
	return nil
}

// Capacity is the execenv.CapacityReporter seam the Runner asks before it
// blames a session for a full host.
func (e *portLeasingEnv) Capacity() error { return e.ports.Capacity() }

func newGCRunner(t *testing.T, env execenv.ExecutionEnvironment, policy Policy) (*runnerImpl, *agentkittest.MemStore, *imageregistry.MockImageRegistry) {
	t.Helper()
	store := agentkittest.NewMemStore()
	reg := imageregistry.NewMock()
	if policy.BaseImage == "" {
		policy.BaseImage = "agentkit-sandbox:test"
	}
	runner, err := NewRunner(Deps{
		Env:       env,
		Registry:  reg,
		Store:     store,
		Artifacts: artifacts.NewMock(),
		Claims:    agentkittest.StaticClaims{Token: "test-token"},
		Events:    events.NewPipeline(events.NewMockSink()),
		Policy:    policy,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return runner.(*runnerImpl), store, reg
}

// setIdle backdates a session's last activity so a sweep sees it as idle.
func (r *runnerImpl) setIdle(sessionID string, by time.Duration) {
	r.mu.Lock()
	r.lastActivity[sessionID] = time.Now().Add(-by)
	r.mu.Unlock()
}

// waitFor polls until cond holds, or fails the test.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestIdleSessionIsArchivedAndItsPortReleased is the fix, end to end through the
// loop's own front door (Start, a real ticker, no hand-driven sweep): a host
// whose single port is leased to an idle session cannot start anything, and
// after the archive sweep it can.
func TestIdleSessionIsArchivedAndItsPortReleased(t *testing.T) {
	ctx := context.Background()
	env := newPortLeasingEnv(t, 40000, 40000) // a pool of exactly one
	r, store, reg := newGCRunner(t, env, Policy{ArchiveTimeout: 40 * time.Millisecond})
	store.Seed(&agentdb.Session{ID: "s-first", Customer: "acme", Job: "j1"})
	store.Seed(&agentdb.Session{ID: "s-second", Customer: "acme", Job: "j1"})

	if _, err := r.CreateSession(ctx, CreateSessionRequest{SessionID: "s-first", Customer: "acme", Job: "j1"}); err != nil {
		t.Fatalf("CreateSession(s-first): %v", err)
	}
	if _, _, free := env.ports.Stats(); free != 0 {
		t.Fatalf("free ports after one session = %d, want 0", free)
	}
	// The host is full: this is the failure the stack actually hit.
	_, err := r.CreateSession(ctx, CreateSessionRequest{SessionID: "s-second", Customer: "acme", Job: "j1"})
	if err == nil || !errors.Is(err, execenv.ErrNoCapacity) {
		t.Fatalf("second session on a full host: err = %v, want execenv.ErrNoCapacity", err)
	}
	t.Logf("host full, as expected: %v", err)

	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer r.Close() //nolint:errcheck
	r.setIdle("s-first", time.Hour)

	waitFor(t, "the idle session to be archived", func() bool {
		_, _, free := env.ports.Stats()
		return free == 1
	})

	// Archiving is snapshot-then-release, and the durable handle is what makes
	// the session resumable at all.
	if n := env.Count("Snapshot"); n != 1 {
		t.Errorf("Snapshot calls = %d, want 1", n)
	}
	if n := reg.Count("Persist"); n != 1 {
		t.Errorf("Persist calls = %d, want 1", n)
	}
	if h, ok, _ := store.GetSnapshotHandle(ctx, "s-first"); !ok || h.Ref == "" {
		t.Errorf("no snapshot handle stored for the archived session")
	}
	// The session ROW is untouched: archiving is not deleting.
	if sess, err := store.GetSession(ctx, "s-first"); err != nil || sess == nil {
		t.Fatalf("archived session row must survive: %v", err)
	}

	// The reclaimed port is usable: the session that could not start now can.
	if _, err := r.CreateSession(ctx, CreateSessionRequest{SessionID: "s-second", Customer: "acme", Job: "j1"}); err != nil {
		t.Fatalf("CreateSession(s-second) after reclamation: %v", err)
	}
	t.Logf("the port freed by archiving s-first started s-second")
}

// TestArchivedSessionResumesOnTheNextMessage is the promise that makes the
// archive loop safe to switch on: a user who comes back to an archived
// conversation must not be able to tell. The session is restored from its
// snapshot and the turn streams normally.
func TestArchivedSessionResumesOnTheNextMessage(t *testing.T) {
	ctx := context.Background()
	env := execenv.NewMock()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/sessions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"data":{"sessionId":"s1"}}`))
		case req.Method == http.MethodPost && req.URL.Path == "/sessions/s1/query-stream":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: content_delta\ndata: {\"delta\":\"still here\"}\n\n"))
			_, _ = w.Write([]byte("event: query_complete\ndata: {\"status\":\"complete\"}\n\n"))
		default:
			http.NotFound(w, req)
		}
	}))
	defer ts.Close()
	env.AddrOverride = ts.URL

	r, store, reg := newGCRunner(t, env, Policy{ArchiveTimeout: 30 * time.Millisecond})
	store.Seed(&agentdb.Session{ID: "s1", Customer: "acme", Job: "j1"})
	if _, err := r.CreateSession(ctx, CreateSessionRequest{SessionID: "s1", Customer: "acme", Job: "j1"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	var first bytes.Buffer
	if err := r.SendMessage(ctx, SessionRef{SessionID: "s1"}, SendMessageRequest{Content: "hello"}, &first); err != nil {
		t.Fatalf("SendMessage before archiving: %v", err)
	}

	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer r.Close() //nolint:errcheck
	r.setIdle("s1", time.Hour)

	waitFor(t, "the session to be archived", func() bool { return r.get("s1") == nil })
	if n := env.Count("Destroy"); n != 1 {
		t.Fatalf("Destroy calls = %d, want 1 (the archived container)", n)
	}

	// The whole point: the next message just works.
	var second bytes.Buffer
	if err := r.SendMessage(ctx, SessionRef{SessionID: "s1"}, SendMessageRequest{Content: "are you still there?"}, &second); err != nil {
		t.Fatalf("SendMessage after archiving must restore the session, got: %v", err)
	}
	if !strings.Contains(second.String(), "still here") {
		t.Errorf("the restored turn did not stream: %q", second.String())
	}
	if n := reg.Count("Materialize"); n != 1 {
		t.Errorf("Materialize calls = %d, want 1 (restore from the snapshot handle)", n)
	}
	if n := env.Count("Provision"); n != 2 {
		t.Errorf("Provision calls = %d, want 2 (create + restore)", n)
	}
	inst := r.get("s1")
	if inst == nil || inst.State != execenv.StateRunning {
		t.Fatalf("session is not running again after resume: %+v", inst)
	}
	t.Logf("archived session resumed and answered: %q", strings.TrimSpace(second.String()))
}

// TestArchiveSkipsASessionMidTurn: lastActivity is stamped when a turn begins
// and again when it ends, so a model run longer than the timeout looks idle
// from its own middle onwards. Tearing the container down there would kill a
// live stream — which is worse than never reclaiming anything.
func TestArchiveSkipsASessionMidTurn(t *testing.T) {
	ctx := context.Background()
	env := execenv.NewMock()
	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/sessions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"data":{"sessionId":"s1"}}`))
		case req.Method == http.MethodPost && req.URL.Path == "/sessions/s1/query-stream":
			w.Header().Set("Content-Type", "text/event-stream")
			if fl, ok := w.(http.Flusher); ok {
				_, _ = w.Write([]byte("event: content_delta\ndata: {\"delta\":\"thinking\"}\n\n"))
				fl.Flush()
			}
			<-release // a very long turn
			_, _ = w.Write([]byte("event: query_complete\ndata: {\"status\":\"complete\"}\n\n"))
		default:
			http.NotFound(w, req)
		}
	}))
	defer ts.Close()
	env.AddrOverride = ts.URL

	r, store, _ := newGCRunner(t, env, Policy{ArchiveTimeout: 20 * time.Millisecond})
	store.Seed(&agentdb.Session{ID: "s1", Customer: "acme", Job: "j1"})
	if _, err := r.CreateSession(ctx, CreateSessionRequest{SessionID: "s1", Customer: "acme", Job: "j1"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	done := make(chan error, 1)
	var out bytes.Buffer
	go func() {
		done <- r.SendMessage(ctx, SessionRef{SessionID: "s1"}, SendMessageRequest{Content: "take your time"}, &out)
	}()
	waitFor(t, "the turn to be in flight", func() bool { return r.heldCount("s1") > 0 })

	// The turn started an hour ago as far as lastActivity is concerned.
	r.setIdle("s1", time.Hour)
	r.archiveIdleOnce(ctx)
	r.archiveIdleOnce(ctx)
	if n := env.Count("Snapshot"); n != 0 {
		t.Fatalf("a session mid-turn was archived (%d snapshots) — the stream would have died", n)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	// Once the turn has settled the session is archivable again.
	r.setIdle("s1", time.Hour)
	r.archiveIdleOnce(ctx)
	if n := env.Count("Snapshot"); n != 1 {
		t.Errorf("Snapshot calls after the turn ended = %d, want 1", n)
	}
}

// TestArchiveDoesNotCancelAnInFlightCreate guards the delete-mid-create fix.
// Destroy marks an in-flight create abandoned so the container it is about to
// produce is not orphaned; archiving must NOT, because the session is not gone
// — teardownInstance, never Destroy. Undoing that distinction would make every
// archive sweep cancel any create it happened to overlap.
func TestArchiveDoesNotCancelAnInFlightCreate(t *testing.T) {
	ctx := context.Background()
	env := execenv.NewMock()
	r, store, _ := newGCRunner(t, env, Policy{ArchiveTimeout: time.Millisecond})
	store.Seed(&agentdb.Session{ID: "s1", Customer: "acme", Job: "j1"})
	if _, err := r.CreateSession(ctx, CreateSessionRequest{SessionID: "s1", Customer: "acme", Job: "j1"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// A second create attempt for the same session is in flight (the host
	// pre-registers it before backgrounding the work).
	r.MarkCreating("s1")
	guard := r.adoptCreateGuard("s1")

	r.setIdle("s1", time.Hour)
	r.archiveIdleOnce(ctx)

	if env.Count("Snapshot") != 1 {
		t.Fatalf("the sweep did not archive the idle session")
	}
	if r.abandoned(guard) {
		t.Fatal("archiving marked an in-flight create abandoned — archiving is not deleting")
	}
	// …whereas deleting the session still does.
	if err := r.Destroy(ctx, SessionRef{SessionID: "s1"}); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if !r.abandoned(guard) {
		t.Fatal("Destroy no longer marks an in-flight create abandoned — the orphan guard is broken")
	}
}

// ── The snapshot TTL reaper ──────────────────────────────────────────────────

// lockedReapWorld makes the reaper fakes (snapshot_reaper_test.go) safe to hand
// to a BACKGROUND loop: every other reaper test drives ReapAll synchronously, so
// the fake itself is deliberately unsynchronised.
type lockedReapWorld struct {
	mu sync.Mutex
	w  *reapWorld
}

func (l *lockedReapWorld) ListCatalogueProjects(ctx context.Context) ([]string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.ListCatalogueProjects(ctx)
}

func (l *lockedReapWorld) GetProjectSettings(ctx context.Context, project string) (*agentdb.ProjectSettings, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.GetProjectSettings(ctx, project)
}

func (l *lockedReapWorld) ListCustomImageVersions(ctx context.Context, q agentdb.ImageCatalogQuery) ([]*agentdb.CustomImage, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.ListCustomImageVersions(ctx, q)
}

func (l *lockedReapWorld) MarkCustomImageReaped(ctx context.Context, project, name string, version int, reapedAt int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.MarkCustomImageReaped(ctx, project, name, version, reapedAt)
}

func (l *lockedReapWorld) reapedAt(id string) int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.reaped[id]
}

func (l *lockedReapWorld) callLog() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.w.calls...)
}

// lockedRegistry is fakeRegistry under the same lock (Remove writes the call
// log the catalogue methods also write).
type lockedRegistry struct {
	fakeRegistry
	l *lockedReapWorld
}

func (r lockedRegistry) Remove(ctx context.Context, h imageregistry.Handle) error {
	r.l.mu.Lock()
	defer r.l.mu.Unlock()
	return r.fakeRegistry.Remove(ctx, h)
}

// TestSnapshotReapLoopSweeps proves the OTHER loop is wired: with an interval
// and a catalogue, Start runs the §13.7 reaper and expired versions are
// tombstoned without anybody calling ReapAll by hand.
func TestSnapshotReapLoopSweeps(t *testing.T) {
	ctx := context.Background()
	now := time.Now().Unix()
	w := newReapWorld()
	w.settings["acme"] = 30
	w.add("acme", "toolbox", 1, now-40*day, now-10*day, "blob-1") // expired
	w.add("acme", "toolbox", 2, now-40*day, now+10*day, "blob-2") // not yet
	locked := &lockedReapWorld{w: w}

	runner, err := NewRunner(Deps{
		Env:       execenv.NewMock(),
		Registry:  lockedRegistry{fakeRegistry: fakeRegistry{w: w}, l: locked},
		Store:     agentkittest.NewMemStore(),
		Artifacts: artifacts.NewMock(),
		Claims:    agentkittest.StaticClaims{Token: "test-token"},
		Events:    events.NewPipeline(events.NewMockSink()),
		Snapshots: locked,
		Policy:    Policy{BaseImage: "agentkit-sandbox:test", SnapshotReapInterval: 20 * time.Millisecond},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	r := runner.(*runnerImpl)
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer r.Close() //nolint:errcheck

	waitFor(t, "the reaper to sweep", func() bool { return locked.reapedAt("acme/toolbox:1") != 0 })
	if locked.reapedAt("acme/toolbox:2") != 0 {
		t.Errorf("the reaper tombstoned a version whose stamp has not passed")
	}
	// Bytes first, then the tombstone — a crash between them must not orphan
	// the blob (snapshot_reaper.go).
	var order []string
	for _, c := range locked.callLog() {
		if strings.HasPrefix(c, "remove ") || strings.HasPrefix(c, "tombstone ") {
			order = append(order, c)
		}
	}
	if len(order) < 2 || !strings.HasPrefix(order[0], "remove ") || !strings.HasPrefix(order[1], "tombstone ") {
		t.Errorf("call order = %v, want remove then tombstone", order)
	}
	t.Logf("the loop swept unattended: %v", order)
}

// TestSnapshotReapLoopNeedsBothGates: an interval with no catalogue (agentd on
// the sqlite fallback) must not start the loop, and a catalogue with no
// interval must not either.
func TestSnapshotReapLoopNeedsBothGates(t *testing.T) {
	for _, tc := range []struct {
		name      string
		interval  time.Duration
		withStore bool
	}{
		{"interval but no catalogue", 10 * time.Millisecond, false},
		{"catalogue but no interval", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now().Unix()
			w := newReapWorld()
			w.settings["acme"] = 30
			w.add("acme", "toolbox", 1, now-40*day, now-10*day, "blob-1")
			locked := &lockedReapWorld{w: w}

			deps := Deps{
				Env:       execenv.NewMock(),
				Registry:  lockedRegistry{fakeRegistry: fakeRegistry{w: w}, l: locked},
				Store:     agentkittest.NewMemStore(),
				Artifacts: artifacts.NewMock(),
				Claims:    agentkittest.StaticClaims{Token: "test-token"},
				Events:    events.NewPipeline(events.NewMockSink()),
				Policy:    Policy{BaseImage: "agentkit-sandbox:test", SnapshotReapInterval: tc.interval},
			}
			if tc.withStore {
				deps.Snapshots = locked
			}
			runner, err := NewRunner(deps)
			if err != nil {
				t.Fatalf("NewRunner: %v", err)
			}
			r := runner.(*runnerImpl)
			if err := r.Start(context.Background()); err != nil {
				t.Fatalf("Start: %v", err)
			}
			defer r.Close() //nolint:errcheck
			time.Sleep(150 * time.Millisecond)
			if locked.reapedAt("acme/toolbox:1") != 0 {
				t.Fatalf("the reaper ran with %s", tc.name)
			}
		})
	}
}

// TestArchiveTick: the sweep cadence. Production ticks once a minute; a timeout
// shorter than that lowers the cadence, because a sweep cannot notice idleness
// sooner than it runs.
func TestArchiveTick(t *testing.T) {
	cases := []struct {
		timeout time.Duration
		want    time.Duration
	}{
		{0, defaultArchiveTick},
		{-time.Second, defaultArchiveTick},
		{30 * time.Minute, defaultArchiveTick},
		{time.Minute, defaultArchiveTick},
		{30 * time.Second, 30 * time.Second},
		{time.Millisecond, 10 * time.Millisecond}, // never a spin
	}
	for _, tc := range cases {
		if got := archiveTick(tc.timeout); got != tc.want {
			t.Errorf("archiveTick(%v) = %v, want %v", tc.timeout, got, tc.want)
		}
	}
}
