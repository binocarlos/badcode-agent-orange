package agentkit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/agentkittest"
	"github.com/binocarlos/badcode-agent-orange/artifacts"
	"github.com/binocarlos/badcode-agent-orange/events"
	"github.com/binocarlos/badcode-agent-orange/execenv"
	"github.com/binocarlos/badcode-agent-orange/execenv/docker"
	"github.com/binocarlos/badcode-agent-orange/fleet"
	"github.com/binocarlos/badcode-agent-orange/imageregistry"
)

// ─── test doubles ────────────────────────────────────────────────────────────
//
// The defect under test is a race between an in-flight create and a delete, so
// the doubles below exist to stop the create at a chosen instant (image pull,
// or container provision) and hold it there while the delete lands.
//
// portedEnv additionally leases from the REAL PortAllocator, because the host
// port is the scarce resource this bug consumes: "no container left behind" is
// only interesting if the port comes back with it.

// gate is a one-shot rendezvous: the code under test signals it has arrived and
// then blocks until the test releases it.
type gate struct {
	arrived chan struct{}
	release chan struct{}
	once    sync.Once
}

func newGate() *gate {
	return &gate{arrived: make(chan struct{}), release: make(chan struct{})}
}

// wait is called from inside the code under test.
func (g *gate) wait() {
	g.once.Do(func() { close(g.arrived) })
	<-g.release
}

// awaitArrival blocks until the gated call has been entered.
func (g *gate) awaitArrival(t *testing.T) {
	t.Helper()
	select {
	case <-g.arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the gated call to be entered")
	}
}

func (g *gate) open() { close(g.release) }

// gatedRegistry blocks EnsurePresent (the image pull) on a gate, modelling the
// seconds-to-minutes pull window the async create exists for.
type gatedRegistry struct {
	*imageregistry.MockImageRegistry
	pull *gate
}

func (g *gatedRegistry) EnsurePresent(ctx context.Context, ref execenv.ImageRef) error {
	if g.pull != nil {
		g.pull.wait()
	}
	return g.MockImageRegistry.EnsurePresent(ctx, ref)
}

// portedEnv is a mock execution environment that leases a host port per
// provisioned container from the real docker.PortAllocator and releases it on
// destroy — exactly like the DinD environment, minus Docker.
type portedEnv struct {
	*execenv.MockExecutionEnvironment
	ports *docker.PortAllocator

	provision *gate // optional: block inside Provision

	mu       sync.Mutex
	sessions map[execenv.InstanceID]string // instance → session, for port release
	live     map[execenv.InstanceID]bool
}

func newPortedEnv(t *testing.T, rangeStart, rangeEnd int) *portedEnv {
	t.Helper()
	pa, err := docker.NewPortAllocator(rangeStart, rangeEnd)
	if err != nil {
		t.Fatalf("NewPortAllocator: %v", err)
	}
	return &portedEnv{
		MockExecutionEnvironment: execenv.NewMock(),
		ports:                    pa,
		sessions:                 map[execenv.InstanceID]string{},
		live:                     map[execenv.InstanceID]bool{},
	}
}

func (p *portedEnv) Provision(ctx context.Context, spec execenv.ProvisionSpec) (*execenv.Instance, error) {
	if p.provision != nil {
		p.provision.wait()
	}
	if _, err := p.ports.Allocate(spec.SessionID); err != nil {
		return nil, err
	}
	inst, err := p.MockExecutionEnvironment.Provision(ctx, spec)
	if err != nil {
		p.ports.Release(spec.SessionID)
		return nil, err
	}
	p.mu.Lock()
	p.sessions[inst.ID] = spec.SessionID
	p.live[inst.ID] = true
	p.mu.Unlock()
	return inst, nil
}

func (p *portedEnv) Destroy(ctx context.Context, id execenv.InstanceID, opts execenv.DestroyOptions) error {
	if err := p.MockExecutionEnvironment.Destroy(ctx, id, opts); err != nil {
		return err
	}
	p.mu.Lock()
	sid, ok := p.sessions[id]
	delete(p.sessions, id)
	delete(p.live, id)
	p.mu.Unlock()
	if ok {
		p.ports.Release(sid)
	}
	return nil
}

// liveContainers is the count of containers that were provisioned and never
// destroyed — the orphans, when nothing owns them.
func (p *portedEnv) liveContainers() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.live)
}

func (p *portedEnv) portsInUse() int {
	_, inUse, _ := p.ports.Stats()
	return inUse
}

// newOrphanTestRunner builds a runner over portedEnv + gatedRegistry.
func newOrphanTestRunner(t *testing.T, env *portedEnv, reg *gatedRegistry) (*runnerImpl, *agentkittest.MemStore) {
	t.Helper()
	store := agentkittest.NewMemStore()
	runner, err := NewRunner(Deps{
		Env:       env,
		Registry:  reg,
		Store:     store,
		Artifacts: artifacts.NewMock(),
		Claims:    agentkittest.StaticClaims{Token: "test-token"},
		Events:    events.NewPipeline(events.NewMockSink()),
		Policy:    Policy{BaseImage: "agentkit-sandbox:test"},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return runner.(*runnerImpl), store
}

// hostDelete replays exactly what httpapi.DeleteSession does: tear the runtime
// instance down through the Runner, then drop the session row.
func hostDelete(t *testing.T, r *runnerImpl, store *agentkittest.MemStore, sid string) {
	t.Helper()
	if err := r.Destroy(context.Background(), SessionRef{SessionID: sid}); err != nil {
		t.Fatalf("Destroy(%s): %v", sid, err)
	}
	_ = store.DeleteSession(context.Background(), sid)
}

func seedCreating(store *agentkittest.MemStore, sid string) {
	store.Seed(&agentdb.Session{ID: sid, Customer: "acme", Job: "j1", Status: "creating"})
}

func createReq(sid string) CreateSessionRequest {
	return CreateSessionRequest{SessionID: sid, Customer: "acme", Job: "j1"}
}

// ─── the defect ──────────────────────────────────────────────────────────────

// TestCreateSession_DeletedDuringProvision_LeavesNoOrphanContainer is the bug.
//
// POST /agent/session answers 200 "creating" and provisions in a background
// goroutine. Delete the session before that finishes and the container arrives
// afterwards belonging to nothing: no session row, no tracked instance, and
// nothing that iterates sessions can ever see it — so it holds one of the
// host's 100 ports until a human finds it.
func TestCreateSession_DeletedDuringProvision_LeavesNoOrphanContainer(t *testing.T) {
	env := newPortedEnv(t, 30000, 30001)
	env.provision = newGate()
	reg := &gatedRegistry{MockImageRegistry: imageregistry.NewMock()}
	r, store := newOrphanTestRunner(t, env, reg)

	seedCreating(store, "s1")

	// Host path: MarkCreating synchronously, CreateSession backgrounded.
	r.MarkCreating("s1")
	errCh := make(chan error, 1)
	go func() {
		_, err := r.CreateSession(context.Background(), createReq("s1"))
		errCh <- err
	}()

	// Wait until the create is inside Provision, then delete the session.
	env.provision.awaitArrival(t)
	hostDelete(t, r, store, "s1")
	env.provision.open()

	select {
	case <-errCh:
	case <-time.After(5 * time.Second):
		t.Fatal("CreateSession did not return")
	}

	if n := env.liveContainers(); n != 0 {
		t.Errorf("delete-during-create left %d orphaned container(s) running; want 0", n)
	}
	if n := env.portsInUse(); n != 0 {
		t.Errorf("delete-during-create leaked %d host port(s); want 0 — the port is the scarce resource", n)
	}
	if inst := r.get("s1"); inst != nil {
		t.Errorf("runner still tracks an instance for the deleted session: %+v", inst)
	}
}

// TestCreateSession_DeletedDuringImagePull_ProvisionsNothing is the cheaper half
// of the same race: the delete lands during the (long) image pull, before any
// container exists. The create must then not provision one at all.
func TestCreateSession_DeletedDuringImagePull_ProvisionsNothing(t *testing.T) {
	env := newPortedEnv(t, 30000, 30001)
	reg := &gatedRegistry{MockImageRegistry: imageregistry.NewMock(), pull: newGate()}
	r, store := newOrphanTestRunner(t, env, reg)

	seedCreating(store, "s1")

	r.MarkCreating("s1")
	errCh := make(chan error, 1)
	go func() {
		_, err := r.CreateSession(context.Background(), createReq("s1"))
		errCh <- err
	}()

	reg.pull.awaitArrival(t)
	hostDelete(t, r, store, "s1")
	reg.pull.open()

	select {
	case <-errCh:
	case <-time.After(5 * time.Second):
		t.Fatal("CreateSession did not return")
	}

	if n := env.liveContainers(); n != 0 {
		t.Errorf("create continued past a delete and left %d container(s); want 0", n)
	}
	if n := env.portsInUse(); n != 0 {
		t.Errorf("leaked %d host port(s); want 0", n)
	}
}

// TestCreateSession_DeletedBeforeCreateGoroutineRuns covers the narrower window
// the host actually has: MarkCreating runs synchronously in the POST handler,
// but the goroutine that calls CreateSession may not be scheduled before the
// DELETE arrives. The create must still not leave a container behind.
func TestCreateSession_DeletedBeforeCreateGoroutineRuns(t *testing.T) {
	env := newPortedEnv(t, 30000, 30001)
	reg := &gatedRegistry{MockImageRegistry: imageregistry.NewMock()}
	r, store := newOrphanTestRunner(t, env, reg)

	seedCreating(store, "s1")

	// The POST handler's synchronous half…
	r.MarkCreating("s1")
	// …the DELETE wins the race with the goroutine entirely…
	hostDelete(t, r, store, "s1")
	// …and only now does the backgrounded create run.
	if _, err := r.CreateSession(context.Background(), createReq("s1")); err == nil {
		t.Error("CreateSession for a session deleted before it started should fail, got nil error")
	}

	if n := env.liveContainers(); n != 0 {
		t.Errorf("create after delete left %d container(s); want 0", n)
	}
	if n := env.portsInUse(); n != 0 {
		t.Errorf("leaked %d host port(s); want 0", n)
	}
}

// ─── the behaviour that must not regress ─────────────────────────────────────

// TestCreateSession_NormalCreateThenDelete is the ordinary path: a create that
// completes, then a delete. The container must live until the delete and the
// port must come back after it.
func TestCreateSession_NormalCreateThenDelete(t *testing.T) {
	env := newPortedEnv(t, 30000, 30001)
	reg := &gatedRegistry{MockImageRegistry: imageregistry.NewMock()}
	r, store := newOrphanTestRunner(t, env, reg)

	seedCreating(store, "s1")
	r.MarkCreating("s1")
	if _, err := r.CreateSession(context.Background(), createReq("s1")); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if n := env.liveContainers(); n != 1 {
		t.Fatalf("after a clean create: %d live containers, want 1", n)
	}
	if n := env.portsInUse(); n != 1 {
		t.Fatalf("after a clean create: %d ports in use, want 1", n)
	}

	hostDelete(t, r, store, "s1")
	if n := env.liveContainers(); n != 0 {
		t.Errorf("after delete: %d live containers, want 0", n)
	}
	if n := env.portsInUse(); n != 0 {
		t.Errorf("after delete: %d ports in use, want 0", n)
	}
}

// TestCreateSession_DeleteOfAnotherSessionDoesNotAbandon is the first reverse
// race: aborting a create must key on the session being deleted, not on "a
// delete happened".
func TestCreateSession_DeleteOfAnotherSessionDoesNotAbandon(t *testing.T) {
	env := newPortedEnv(t, 30000, 30005)
	env.provision = newGate()
	reg := &gatedRegistry{MockImageRegistry: imageregistry.NewMock()}
	r, store := newOrphanTestRunner(t, env, reg)

	seedCreating(store, "s1")
	seedCreating(store, "other")

	r.MarkCreating("s1")
	errCh := make(chan error, 1)
	go func() {
		_, err := r.CreateSession(context.Background(), createReq("s1"))
		errCh <- err
	}()

	env.provision.awaitArrival(t)
	hostDelete(t, r, store, "other") // a DIFFERENT session
	env.provision.open()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("create of s1 must survive the delete of another session: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CreateSession did not return")
	}

	if n := env.liveContainers(); n != 1 {
		t.Errorf("s1's container should be live: %d containers", n)
	}
	if inst := r.get("s1"); inst == nil {
		t.Error("s1 should still be tracked")
	}
}

// TestCreateSession_RecreatedAfterDelete is the second reverse race: the same
// session id, deleted and then legitimately created again. The abort must not
// carry over to the new create — the new container must survive.
func TestCreateSession_RecreatedAfterDelete(t *testing.T) {
	env := newPortedEnv(t, 30000, 30005)
	reg := &gatedRegistry{MockImageRegistry: imageregistry.NewMock()}
	r, store := newOrphanTestRunner(t, env, reg)

	seedCreating(store, "s1")
	r.MarkCreating("s1")
	hostDelete(t, r, store, "s1")
	_, _ = r.CreateSession(context.Background(), createReq("s1")) // aborted

	// Now create the very same id again, cleanly.
	seedCreating(store, "s1")
	r.MarkCreating("s1")
	if _, err := r.CreateSession(context.Background(), createReq("s1")); err != nil {
		t.Fatalf("re-create of a previously deleted session id must succeed: %v", err)
	}
	if n := env.liveContainers(); n != 1 {
		t.Errorf("re-created session should have exactly 1 live container, got %d", n)
	}
	if n := env.portsInUse(); n != 1 {
		t.Errorf("re-created session should hold exactly 1 port, got %d", n)
	}
	if inst := r.get("s1"); inst == nil {
		t.Error("re-created session should be tracked")
	}
}

// TestCreateSession_DeleteAfterCreateCompletes is the third reverse race: the
// delete lands just after the create returns. Exactly one teardown must happen
// and the port must be released once.
func TestCreateSession_DeleteAfterCreateCompletes(t *testing.T) {
	env := newPortedEnv(t, 30000, 30005)
	reg := &gatedRegistry{MockImageRegistry: imageregistry.NewMock()}
	r, store := newOrphanTestRunner(t, env, reg)

	seedCreating(store, "s1")
	r.MarkCreating("s1")
	if _, err := r.CreateSession(context.Background(), createReq("s1")); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		hostDelete(t, r, store, "s1")
	}()
	wg.Wait()

	if n := env.liveContainers(); n != 0 {
		t.Errorf("%d containers left after delete, want 0", n)
	}
	if n := env.portsInUse(); n != 0 {
		t.Errorf("%d ports left leased after delete, want 0", n)
	}
}

// TestCreateSession_AbandonNeverDestroysASharedContainer is the fourth reverse
// race, and the one with teeth: on a shared-tenancy worker the "session's"
// container hosts other sessions too. Aborting one create must never take it
// down — deleting live work is strictly worse than leaking, and a shared
// instance holds no per-session port to reclaim anyway.
func TestCreateSession_AbandonNeverDestroysASharedContainer(t *testing.T) {
	ctx := context.Background()
	store := agentkittest.NewMemStore()

	sharedCaps := execenv.Capabilities{
		Backend:          execenv.BackendDockerDinD,
		Tenancy:          execenv.TenancyShared,
		IsolationTier:    execenv.TierVM,
		SupportsSnapshot: false,
	}
	sharedEnv := execenv.NewMock()
	sharedEnv.Caps = &sharedCaps

	f := fleet.NewMemory(store, &fleet.MemFleetOptions{TrustedWorkload: false})
	if err := f.Register(ctx, &fleet.Worker{ID: "shared-w", Env: sharedEnv, Caps: sharedCaps}); err != nil {
		t.Fatalf("Register shared worker: %v", err)
	}
	runner, err := NewRunner(Deps{
		Fleet:     f,
		Registry:  imageregistry.NewMock(),
		Store:     store,
		Artifacts: artifacts.NewMock(),
		Claims:    agentkittest.StaticClaims{Token: "t"},
		Events:    events.NewPipeline(events.NewMockSink()),
		Policy:    Policy{BaseImage: "shared-image:test"},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	r := runner.(*runnerImpl)

	// A first session brings the shared container up and keeps using it.
	seedCreating(store, "keeper")
	if _, err := r.CreateSession(ctx, createReq("keeper")); err != nil {
		t.Fatalf("CreateSession keeper: %v", err)
	}
	destroysBefore := sharedEnv.Count("Destroy")

	// A second session is created and deleted mid-create.
	seedCreating(store, "doomed")
	r.MarkCreating("doomed")
	r.markCreateAbandoned("doomed") // as Destroy would
	if _, err := r.CreateSession(ctx, createReq("doomed")); !errors.Is(err, ErrSessionDeleted) {
		t.Fatalf("want ErrSessionDeleted, got %v", err)
	}

	if got := sharedEnv.Count("Destroy") - destroysBefore; got != 0 {
		t.Errorf("aborting a create on a shared-tenancy worker destroyed the shared container "+
			"(%d Destroy calls) — that container hosts other sessions", got)
	}
	if inst := r.get("__shared__shared-w"); inst == nil {
		t.Error("the shared instance must still be tracked and usable by other sessions")
	}
}

// TestCreateSession_AbandonLeavesANewerCreatesContainerAlone is the fifth
// reverse race. Two creates overlap on the same session id: the first is
// deleted, the second is legitimate and (because the environment de-duplicates
// containers per session) may be holding the very same container. The abort
// must not pull it out from under the live create — leaking beats deleting
// somebody else's work.
func TestCreateSession_AbandonLeavesANewerCreatesContainerAlone(t *testing.T) {
	env := newPortedEnv(t, 30000, 30005)
	reg := &gatedRegistry{MockImageRegistry: imageregistry.NewMock()}
	r, store := newOrphanTestRunner(t, env, reg)

	seedCreating(store, "s1")

	// Attempt #1 gets as far as a provisioned, tracked container.
	g1 := r.adoptCreateGuard("s1")
	worker, err := r.deps.Fleet.PlaceForSession(context.Background(), "s1", fleet.PlacementHint{})
	if err != nil {
		t.Fatalf("PlaceForSession: %v", err)
	}
	inst, err := env.Provision(context.Background(), execenv.ProvisionSpec{SessionID: "s1", Image: "img"})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	r.track("s1", worker.ID, inst)

	// The session is deleted…
	r.markCreateAbandoned("s1")
	// …and a NEW create attempt for the same id starts and adopts the session.
	r.MarkCreating("s1")

	// Only now does attempt #1 reach its checkpoint.
	if err := r.abandonCreate(context.Background(), "s1", g1, worker, inst); !errors.Is(err, ErrSessionDeleted) {
		t.Fatalf("want ErrSessionDeleted, got %v", err)
	}

	if n := env.liveContainers(); n != 1 {
		t.Errorf("the newer create's container was destroyed under it: %d live, want 1", n)
	}
	if inst := r.get("s1"); inst == nil {
		t.Error("the newer create's instance must remain tracked")
	}
}

// TestErrSessionDeleted_IsIdentifiable pins the typed error hosts branch on:
// "the session was deleted while it was being created" is not a create failure
// worth recording against a row that no longer exists.
func TestErrSessionDeleted_IsIdentifiable(t *testing.T) {
	env := newPortedEnv(t, 30000, 30001)
	reg := &gatedRegistry{MockImageRegistry: imageregistry.NewMock()}
	r, store := newOrphanTestRunner(t, env, reg)

	seedCreating(store, "s1")
	r.MarkCreating("s1")
	hostDelete(t, r, store, "s1")

	_, err := r.CreateSession(context.Background(), createReq("s1"))
	if !errors.Is(err, ErrSessionDeleted) {
		t.Fatalf("want ErrSessionDeleted, got %v", err)
	}
}
