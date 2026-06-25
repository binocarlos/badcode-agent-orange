package docker

// docker_test.go — hermetic unit tests for execenv/docker.
//
// These tests run with NO Docker daemon by injecting a fake dockerAPI
// implementation. They exercise: PortAllocator, container lifecycle verbs
// (Provision/Exec/Snapshot/Destroy/Recover/Status), the
// waitForHealthy retry logic with an injected poller, and pure helpers
// (containerName, buildEnvList, sortStrings, …).
//
// Compile-time assertions that both adapters satisfy ExecutionEnvironment are
// also in this file.

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	dockertypes "github.com/docker/docker/api/types"
	dockercontainer "github.com/docker/docker/api/types/container"
	dockerfilters "github.com/docker/docker/api/types/filters"
	dockernetwork "github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/binocarlos/badcode-agent-orange/execenv"
)

// ─── compile-time interface assertions ───────────────────────────────────────

var _ execenv.ExecutionEnvironment = (*DinD)(nil)
var _ execenv.ExecutionEnvironment = (*Socket)(nil)

// ─── fakeDockerAPI ───────────────────────────────────────────────────────────

// noopConn is a minimal net.Conn that does nothing. Used so HijackedResponse
// can call Close() without a nil pointer dereference in tests.
type noopConn struct{ net.Conn }

func (noopConn) Close() error                { return nil }
func (noopConn) Read(b []byte) (int, error)  { return 0, nil }
func (noopConn) Write(b []byte) (int, error) { return len(b), nil }

// fakeDockerAPI is an in-memory implementation of dockerAPI used by hermetic
// tests. It records calls and simulates success/failure of each Docker API.
type fakeDockerAPI struct {
	mu sync.Mutex

	// containers maps containerID → fakeContainer
	containers map[string]*fakeContainer

	// nextID is incremented for each ContainerCreate.
	nextID int

	// CallLog records every method name invoked.
	CallLog []string

	// CreateErr, StartErr, … are per-method error hooks for failure injection.
	CreateErr  error
	StartErr   error
	StopErr    error
	RemoveErr  error
	InspectErr error
	CommitErr  error
	ListErr    error
	ExecErr    error
	AttachErr  error
	InspectExecErr error
}

type fakeContainer struct {
	id      string
	name    string
	image   string
	env     []string
	labels  map[string]string
	running bool
	ports   nat.PortMap
}

func newFakeDocker() *fakeDockerAPI {
	return &fakeDockerAPI{
		containers: make(map[string]*fakeContainer),
	}
}

func (f *fakeDockerAPI) log(method string) {
	f.mu.Lock()
	f.CallLog = append(f.CallLog, method)
	f.mu.Unlock()
}

func (f *fakeDockerAPI) ContainerCreate(ctx context.Context, config *dockercontainer.Config, hostConfig *dockercontainer.HostConfig, networkingConfig *dockernetwork.NetworkingConfig, platform *ocispec.Platform, containerName string) (dockercontainer.CreateResponse, error) {
	f.log("ContainerCreate")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.CreateErr != nil {
		err := f.CreateErr
		f.CreateErr = nil
		return dockercontainer.CreateResponse{}, err
	}
	f.nextID++
	id := fmt.Sprintf("fake-container-%d", f.nextID)
	labels := make(map[string]string)
	for k, v := range config.Labels {
		labels[k] = v
	}
	f.containers[id] = &fakeContainer{
		id:      id,
		name:    containerName,
		image:   config.Image,
		env:     config.Env,
		labels:  labels,
		running: false,
	}
	return dockercontainer.CreateResponse{ID: id}, nil
}

func (f *fakeDockerAPI) ContainerStart(ctx context.Context, containerID string, options dockertypes.ContainerStartOptions) error {
	f.log("ContainerStart")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.StartErr != nil {
		err := f.StartErr
		f.StartErr = nil
		return err
	}
	c, ok := f.containers[containerID]
	if !ok {
		return fmt.Errorf("no such container: %s", containerID)
	}
	c.running = true
	return nil
}

func (f *fakeDockerAPI) ContainerStop(ctx context.Context, containerID string, options dockercontainer.StopOptions) error {
	f.log("ContainerStop")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.StopErr != nil {
		err := f.StopErr
		f.StopErr = nil
		return err
	}
	c, ok := f.containers[containerID]
	if !ok {
		return fmt.Errorf("no such container: %s", containerID)
	}
	c.running = false
	return nil
}

func (f *fakeDockerAPI) ContainerRemove(ctx context.Context, containerID string, options dockertypes.ContainerRemoveOptions) error {
	f.log("ContainerRemove")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.RemoveErr != nil {
		err := f.RemoveErr
		f.RemoveErr = nil
		return err
	}
	delete(f.containers, containerID)
	return nil
}

func (f *fakeDockerAPI) ContainerInspect(ctx context.Context, containerID string) (dockertypes.ContainerJSON, error) {
	f.log("ContainerInspect")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.InspectErr != nil {
		err := f.InspectErr
		f.InspectErr = nil
		return dockertypes.ContainerJSON{}, err
	}
	c, ok := f.containers[containerID]
	if !ok {
		return dockertypes.ContainerJSON{}, fmt.Errorf("no such container: %s", containerID)
	}
	status := "exited"
	if c.running {
		status = "running"
	}
	return dockertypes.ContainerJSON{
		ContainerJSONBase: &dockertypes.ContainerJSONBase{
			ID:   c.id,
			Name: "/" + c.name,
			State: &dockertypes.ContainerState{
				Status:  status,
				Running: c.running,
			},
			HostConfig: &dockercontainer.HostConfig{
				PortBindings: c.ports,
			},
		},
		Config: &dockercontainer.Config{
			Image:  c.image,
			Labels: c.labels,
		},
	}, nil
}

func (f *fakeDockerAPI) ContainerCommit(ctx context.Context, containerID string, options dockertypes.ContainerCommitOptions) (dockertypes.IDResponse, error) {
	f.log("ContainerCommit")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.CommitErr != nil {
		err := f.CommitErr
		f.CommitErr = nil
		return dockertypes.IDResponse{}, err
	}
	if _, ok := f.containers[containerID]; !ok {
		return dockertypes.IDResponse{}, fmt.Errorf("no such container: %s", containerID)
	}
	return dockertypes.IDResponse{ID: "sha256:fake-commit-" + containerID}, nil
}

func (f *fakeDockerAPI) ContainerList(ctx context.Context, options dockertypes.ContainerListOptions) ([]dockertypes.Container, error) {
	f.log("ContainerList")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ListErr != nil {
		err := f.ListErr
		f.ListErr = nil
		return nil, err
	}
	var result []dockertypes.Container
	for _, c := range f.containers {
		state := "exited"
		if c.running {
			state = "running"
		}
		var ports []dockertypes.Port
		for portProto, bindings := range c.ports {
			if len(bindings) > 0 {
				var hostPort uint16
				fmt.Sscanf(bindings[0].HostPort, "%d", &hostPort) //nolint:errcheck
				ports = append(ports, dockertypes.Port{
					PrivatePort: uint16(portProto.Int()),
					PublicPort:  hostPort,
					Type:        "tcp",
				})
			}
		}
		result = append(result, dockertypes.Container{
			ID:     c.id,
			Names:  []string{"/" + c.name},
			Image:  c.image,
			State:  state,
			Labels: c.labels,
			Ports:  ports,
		})
	}
	return result, nil
}

func (f *fakeDockerAPI) ContainerExecCreate(ctx context.Context, container string, config dockertypes.ExecConfig) (dockertypes.IDResponse, error) {
	f.log("ContainerExecCreate")
	if f.ExecErr != nil {
		err := f.ExecErr
		f.ExecErr = nil
		return dockertypes.IDResponse{}, err
	}
	return dockertypes.IDResponse{ID: "fake-exec-id"}, nil
}

func (f *fakeDockerAPI) ContainerExecStart(ctx context.Context, execID string, config dockertypes.ExecStartCheck) error {
	f.log("ContainerExecStart")
	return nil
}

func (f *fakeDockerAPI) ContainerExecAttach(ctx context.Context, execID string, config dockertypes.ExecStartCheck) (dockertypes.HijackedResponse, error) {
	f.log("ContainerExecAttach")
	if f.AttachErr != nil {
		err := f.AttachErr
		f.AttachErr = nil
		return dockertypes.HijackedResponse{}, err
	}
	// Return a minimal hijacked response with docker-framed stdout.
	// Docker framing: 8-byte header (stream type + 4-byte big-endian length) + payload.
	// stream type 1 = stdout.
	payload := []byte("hello stdout")
	frame := make([]byte, 8+len(payload))
	frame[0] = 1 // stdout
	frame[4] = byte(len(payload) >> 24)
	frame[5] = byte(len(payload) >> 16)
	frame[6] = byte(len(payload) >> 8)
	frame[7] = byte(len(payload))
	copy(frame[8:], payload)
	return dockertypes.HijackedResponse{
		Conn:   noopConn{},
		Reader: bufio.NewReader(bytes.NewReader(frame)),
	}, nil
}

func (f *fakeDockerAPI) ContainerExecInspect(ctx context.Context, execID string) (dockertypes.ContainerExecInspect, error) {
	f.log("ContainerExecInspect")
	if f.InspectExecErr != nil {
		err := f.InspectExecErr
		f.InspectExecErr = nil
		return dockertypes.ContainerExecInspect{}, err
	}
	return dockertypes.ContainerExecInspect{ExitCode: 0}, nil
}

func (f *fakeDockerAPI) ImageList(ctx context.Context, options dockertypes.ImageListOptions) ([]dockertypes.ImageSummary, error) {
	f.log("ImageList")
	return nil, nil
}

func (f *fakeDockerAPI) NetworkList(ctx context.Context, options dockertypes.NetworkListOptions) ([]dockertypes.NetworkResource, error) {
	f.log("NetworkList")
	return nil, nil
}

func (f *fakeDockerAPI) NetworkCreate(ctx context.Context, name string, options dockertypes.NetworkCreate) (dockertypes.NetworkCreateResponse, error) {
	f.log("NetworkCreate")
	return dockertypes.NetworkCreateResponse{ID: "fake-net-" + name}, nil
}

// addContainerForRecover inserts a container directly into the fake (bypassing
// ContainerCreate) so Recover can find pre-existing containers.
func (f *fakeDockerAPI) addContainerForRecover(id, name, sessionID, image string, running bool, hostPort int, agentPort int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	labels := map[string]string{
		labelManaged:   "true",
		labelSessionID: sessionID,
	}
	portKey := nat.Port(fmt.Sprintf("%d/tcp", agentPort))
	portMap := nat.PortMap{
		portKey: []nat.PortBinding{{HostPort: fmt.Sprintf("%d", hostPort)}},
	}
	f.containers[id] = &fakeContainer{
		id:      id,
		name:    name,
		image:   image,
		labels:  labels,
		running: running,
		ports:   portMap,
	}
}

// countCalls returns how many times a method was called.
func (f *fakeDockerAPI) countCalls(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, c := range f.CallLog {
		if c == method {
			count++
		}
	}
	return count
}

// ─── PortAllocator tests ──────────────────────────────────────────────────────

func TestPortAllocator_AllocateRelease(t *testing.T) {
	pa, err := NewPortAllocator(30000, 30002)
	if err != nil {
		t.Fatalf("NewPortAllocator: %v", err)
	}

	// Allocate three ports — should be deterministic lowest-first.
	p1, err := pa.Allocate("s1")
	if err != nil || p1 != 30000 {
		t.Fatalf("expected 30000, got %d err %v", p1, err)
	}
	p2, err := pa.Allocate("s2")
	if err != nil || p2 != 30001 {
		t.Fatalf("expected 30001, got %d err %v", p2, err)
	}
	p3, err := pa.Allocate("s3")
	if err != nil || p3 != 30002 {
		t.Fatalf("expected 30002, got %d err %v", p3, err)
	}

	// Pool is exhausted.
	_, err = pa.Allocate("s4")
	if err == nil {
		t.Fatal("expected error on exhausted pool")
	}

	// Release s2 → pool gets 30001 back.
	pa.Release("s2")
	p, err := pa.Allocate("s5")
	if err != nil || p != 30001 {
		t.Fatalf("after release, expected 30001, got %d err %v", p, err)
	}

	// Idempotent: allocate same session twice.
	p6a, _ := pa.Allocate("s6")
	p6b, _ := pa.Allocate("s6")
	if p6a != p6b {
		t.Fatalf("idempotent alloc failed: %d != %d", p6a, p6b)
	}
	_ = p3
}

func TestPortAllocator_Adopt(t *testing.T) {
	pa, _ := NewPortAllocator(30000, 30005)

	// Pre-allocate to consume 30000 and 30001.
	pa.Allocate("s1") //nolint:errcheck
	pa.Allocate("s2") //nolint:errcheck

	// Adopt port 30003 (currently available).
	pa.Adopt("recovered-session", 30003)

	// 30003 should no longer be available.
	// Allocate remaining: 30002, 30004, 30005.
	alloc := map[int]bool{}
	for i := 0; i < 3; i++ {
		p, err := pa.Allocate(fmt.Sprintf("x%d", i))
		if err != nil {
			t.Fatalf("unexpected alloc error: %v", err)
		}
		alloc[p] = true
	}
	if alloc[30003] {
		t.Fatal("adopted port 30003 should not appear in allocation pool")
	}

	// Adopted session already has port; Get should return it.
	p, ok := pa.Get("recovered-session")
	if !ok || p != 30003 {
		t.Fatalf("Get adopted: want 30003 ok=true, got %d ok=%v", p, ok)
	}
}

func TestPortAllocator_Stats(t *testing.T) {
	pa, _ := NewPortAllocator(40000, 40009) // 10 ports
	total, inUse, free := pa.Stats()
	if total != 10 || inUse != 0 || free != 10 {
		t.Fatalf("initial stats wrong: total=%d inUse=%d free=%d", total, inUse, free)
	}
	pa.Allocate("a") //nolint:errcheck
	pa.Allocate("b") //nolint:errcheck
	_, inUse, free = pa.Stats()
	if inUse != 2 || free != 8 {
		t.Fatalf("after 2 allocs: inUse=%d free=%d", inUse, free)
	}
}

func TestPortAllocator_EmptyRange(t *testing.T) {
	_, err := NewPortAllocator(30005, 30000)
	if err == nil {
		t.Fatal("expected error for inverted range")
	}
}

// ─── helper function tests ────────────────────────────────────────────────────

func TestContainerName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"abc123", "sandbox-abc123"},
		{"a-b.c_d", "sandbox-a-b.c_d"},
		{"a b/c", "sandbox-a-b-c"},
		// Truncation: 54 chars sanitized → 63 total with "sandbox-" prefix.
		{strings.Repeat("x", 60), "sandbox-" + strings.Repeat("x", 54)},
	}
	for _, tc := range cases {
		got := containerName(tc.in)
		if got != tc.want {
			t.Errorf("containerName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBuildEnvList(t *testing.T) {
	env := map[string]string{
		"Z": "z",
		"A": "a",
		"M": "m",
	}
	got := buildEnvList(env)
	want := []string{"A=a", "M=m", "Z=z"}
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] got %q want %q", i, got[i], want[i])
		}
	}
}

func TestBuildEnvList_Empty(t *testing.T) {
	if got := buildEnvList(nil); got != nil {
		t.Errorf("nil map should return nil, got %v", got)
	}
}

func TestSortStrings(t *testing.T) {
	s := []string{"z", "a", "m", "b"}
	sortStrings(s)
	for i := 1; i < len(s); i++ {
		if s[i] < s[i-1] {
			t.Fatalf("not sorted at %d: %v", i, s)
		}
	}
}

func TestInsertSorted(t *testing.T) {
	s := []int{1, 3, 5}
	s = insertSorted(s, 4)
	want := []int{1, 3, 4, 5}
	for i, v := range want {
		if s[i] != v {
			t.Fatalf("insertSorted: got %v want %v", s, want)
		}
	}
	s = insertSorted(s, 0)
	if s[0] != 0 {
		t.Fatalf("insertSorted at front: %v", s)
	}
	s = insertSorted(s, 99)
	if s[len(s)-1] != 99 {
		t.Fatalf("insertSorted at end: %v", s)
	}
}

// ─── DinD adapter unit tests ──────────────────────────────────────────────────

// newTestDinD creates a DinD backed by a fake Docker client and a tiny port pool.
func newTestDinD(d *fakeDockerAPI) *DinD {
	ports, _ := NewPortAllocator(30000, 30010)
	cfg := DinDConfig{
		GatewayIP: "172.17.0.1",
		Network:   "bridge",
	}
	e := newDinDWith(cfg, d, ports)
	// Inject a poller that always returns true immediately (no sleeps).
	e.poller = func(_ context.Context, _ string) bool { return true }
	// Zero retry interval so tests don't sleep.
	e.healthRetryInterval = 0
	return e
}

func TestDinD_Provision(t *testing.T) {
	d := newFakeDocker()
	e := newTestDinD(d)

	spec := execenv.ProvisionSpec{
		SessionID: "sess-abc",
		Image:     "my-image:latest",
		AgentPort: 3010,
		Env:       map[string]string{"MY_VAR": "1"},
	}

	inst, err := e.Provision(context.Background(), spec)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if inst.SessionID != spec.SessionID {
		t.Errorf("SessionID: got %q want %q", inst.SessionID, spec.SessionID)
	}
	if inst.State != execenv.StateRunning {
		t.Errorf("State: got %q want running", inst.State)
	}
	if !strings.HasPrefix(inst.Address, "http://127.0.0.1:") {
		t.Errorf("Address should be 127.0.0.1:port (IPv4, avoids the IPv6 docker-proxy reset), got %q", inst.Address)
	}
	if d.countCalls("ContainerCreate") != 1 {
		t.Errorf("expected 1 ContainerCreate")
	}
	if d.countCalls("ContainerStart") != 1 {
		t.Errorf("expected 1 ContainerStart")
	}
}

func TestDinD_Provision_Idempotent(t *testing.T) {
	d := newFakeDocker()
	e := newTestDinD(d)

	spec := execenv.ProvisionSpec{SessionID: "sess-idem", Image: "img", AgentPort: 3010}
	inst1, _ := e.Provision(context.Background(), spec)
	inst2, err := e.Provision(context.Background(), spec)
	if err != nil {
		t.Fatalf("second Provision: %v", err)
	}
	if inst1.ID != inst2.ID {
		t.Errorf("idempotent: IDs differ %q vs %q", inst1.ID, inst2.ID)
	}
	// Only one ContainerCreate should have been issued.
	if d.countCalls("ContainerCreate") != 1 {
		t.Errorf("expected 1 ContainerCreate, got %d", d.countCalls("ContainerCreate"))
	}
}

func TestDinD_Provision_PortExhausted(t *testing.T) {
	d := newFakeDocker()
	ports, _ := NewPortAllocator(30000, 30000) // only one port
	cfg := DinDConfig{GatewayIP: "172.17.0.1"}
	e := newDinDWith(cfg, d, ports)
	e.poller = func(_ context.Context, _ string) bool { return true }

	_, err := e.Provision(context.Background(), execenv.ProvisionSpec{SessionID: "s1", Image: "img", AgentPort: 3010})
	if err != nil {
		t.Fatalf("first provision should succeed: %v", err)
	}
	_, err = e.Provision(context.Background(), execenv.ProvisionSpec{SessionID: "s2", Image: "img", AgentPort: 3010})
	if err == nil {
		t.Fatal("second provision should fail (pool exhausted)")
	}
}

func TestDinD_Provision_CreateFailReleasesPort(t *testing.T) {
	d := newFakeDocker()
	e := newTestDinD(d)
	d.CreateErr = fmt.Errorf("create failed")

	_, err := e.Provision(context.Background(), execenv.ProvisionSpec{SessionID: "fail-sess", Image: "img", AgentPort: 3010})
	if err == nil {
		t.Fatal("expected error")
	}
	// Port should have been released — next provision should succeed.
	d.CreateErr = nil
	_, err = e.Provision(context.Background(), execenv.ProvisionSpec{SessionID: "fail-sess", Image: "img", AgentPort: 3010})
	if err != nil {
		t.Fatalf("provision after release: %v", err)
	}
}

func TestDinD_Exec(t *testing.T) {
	d := newFakeDocker()
	e := newTestDinD(d)

	inst, _ := e.Provision(context.Background(), execenv.ProvisionSpec{SessionID: "s1", Image: "img", AgentPort: 3010})

	result, err := e.Exec(context.Background(), inst.ID, []string{"ls", "-la"}, execenv.ExecOptions{WorkingDir: "/app"})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode: %d", result.ExitCode)
	}
	if d.countCalls("ContainerExecCreate") != 1 {
		t.Errorf("expected 1 ContainerExecCreate")
	}
}

func TestDinD_Snapshot(t *testing.T) {
	d := newFakeDocker()
	e := newTestDinD(d)

	inst, _ := e.Provision(context.Background(), execenv.ProvisionSpec{SessionID: "s1", Image: "img", AgentPort: 3010})

	ref, err := e.Snapshot(context.Background(), inst.ID, execenv.SnapshotOptions{Tag: "my-snap:v1"})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !strings.HasPrefix(string(ref), "sha256:") {
		t.Errorf("ImageRef should start with sha256:, got %q", ref)
	}
	if d.countCalls("ContainerCommit") != 1 {
		t.Errorf("expected 1 ContainerCommit")
	}
}

func TestDinD_Destroy(t *testing.T) {
	d := newFakeDocker()
	e := newTestDinD(d)

	var destroyed execenv.InstanceID
	e.OnDestroy(func(id execenv.InstanceID) { destroyed = id })

	inst, _ := e.Provision(context.Background(), execenv.ProvisionSpec{SessionID: "s1", Image: "img", AgentPort: 3010})

	if err := e.Destroy(context.Background(), inst.ID, execenv.DestroyOptions{}); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if destroyed != inst.ID {
		t.Errorf("OnDestroy callback: got %q want %q", destroyed, inst.ID)
	}
	// Port should be released.
	_, ok := e.ports.Get("s1")
	if ok {
		t.Error("port should be released after Destroy")
	}

	// Destroy again — idempotent.
	if err := e.Destroy(context.Background(), inst.ID, execenv.DestroyOptions{}); err != nil {
		t.Fatalf("second Destroy: %v", err)
	}
}

func TestDinD_Status_Unknown(t *testing.T) {
	d := newFakeDocker()
	e := newTestDinD(d)

	status, err := e.Status(context.Background(), "unknown-id")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.State != execenv.StateDestroyed {
		t.Errorf("unknown instance: want destroyed, got %q", status.State)
	}
}

func TestDinD_Recover(t *testing.T) {
	d := newFakeDocker()
	// Insert fake containers. Only the running one should be re-adopted; the
	// stopped one is reclaimed (destroyed), since the lifecycle is now
	// Running-or-Archived with no warm "suspended" state.
	d.addContainerForRecover("cid-1", "sandbox-sess-1", "sess-1", "img", true, 30000, 3010)
	d.addContainerForRecover("cid-2", "sandbox-sess-2", "sess-2", "img", false, 30001, 3010)
	// Orphan without session ID (should be skipped).
	d.mu.Lock()
	d.containers["cid-orphan"] = &fakeContainer{id: "cid-orphan", name: "orphan", labels: map[string]string{labelManaged: "true"}}
	d.mu.Unlock()

	e := newTestDinD(d)
	recovered, err := e.Recover(context.Background())
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if len(recovered) != 1 {
		t.Fatalf("expected 1 recovered (running only), got %d", len(recovered))
	}
	if recovered[0].SessionID != "sess-1" || recovered[0].State != execenv.StateRunning {
		t.Errorf("expected sess-1 running, got %q %q", recovered[0].SessionID, recovered[0].State)
	}

	// The stopped container should have been removed (reclaimed), not adopted.
	d.mu.Lock()
	_, stillThere := d.containers["cid-2"]
	d.mu.Unlock()
	if stillThere {
		t.Errorf("stopped container cid-2 should have been destroyed on recover")
	}

	// Only the running session's port is adopted; the destroyed one's is not.
	p1, ok1 := e.ports.Get("sess-1")
	if !ok1 || p1 != 30000 {
		t.Errorf("sess-1 port: want 30000, got %d ok=%v", p1, ok1)
	}
	if _, ok2 := e.ports.Get("sess-2"); ok2 {
		t.Errorf("sess-2 port should not be adopted for a destroyed container")
	}
}

func TestDinD_Capabilities(t *testing.T) {
	d := newFakeDocker()
	e := newTestDinD(d)
	caps := e.Capabilities()
	if caps.Backend != execenv.BackendDockerDinD {
		t.Errorf("Backend: %q", caps.Backend)
	}
	if caps.Tenancy != execenv.TenancyPerSession {
		t.Errorf("Tenancy: %q", caps.Tenancy)
	}
	if !caps.SupportsSnapshot || !caps.SupportsExec {
		t.Errorf("missing capability: %+v", caps)
	}
	if caps.IsolationTier != execenv.TierContainer {
		t.Errorf("IsolationTier: %d", caps.IsolationTier)
	}
}

// ─── Socket adapter unit tests ────────────────────────────────────────────────

func newTestSocket(d *fakeDockerAPI) *Socket {
	cfg := SocketConfig{Network: "bridge"}
	e := newSocketWith(cfg, d)
	e.poller = func(_ context.Context, _ string) bool { return true }
	// Zero retry interval so tests don't sleep.
	e.healthRetryInterval = 0
	return e
}

func TestSocket_Provision(t *testing.T) {
	d := newFakeDocker()
	e := newTestSocket(d)

	spec := execenv.ProvisionSpec{
		SessionID: "sess-socket",
		Image:     "my-image:latest",
		AgentPort: 3010,
	}

	inst, err := e.Provision(context.Background(), spec)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	expectedName := containerName(spec.SessionID)
	expectedAddr := fmt.Sprintf("http://%s:3010", expectedName)
	if inst.Address != expectedAddr {
		t.Errorf("Address: got %q want %q", inst.Address, expectedAddr)
	}
	if inst.State != execenv.StateRunning {
		t.Errorf("State: got %q want running", inst.State)
	}
}

func TestSocket_Provision_Idempotent(t *testing.T) {
	d := newFakeDocker()
	e := newTestSocket(d)

	spec := execenv.ProvisionSpec{SessionID: "s-idem", Image: "img", AgentPort: 3010}
	inst1, _ := e.Provision(context.Background(), spec)
	inst2, err := e.Provision(context.Background(), spec)
	if err != nil {
		t.Fatalf("second Provision: %v", err)
	}
	if inst1.ID != inst2.ID {
		t.Errorf("idempotent: IDs differ")
	}
	if d.countCalls("ContainerCreate") != 1 {
		t.Errorf("expected 1 ContainerCreate, got %d", d.countCalls("ContainerCreate"))
	}
}

func TestSocket_Exec(t *testing.T) {
	d := newFakeDocker()
	e := newTestSocket(d)

	inst, _ := e.Provision(context.Background(), execenv.ProvisionSpec{SessionID: "s1", Image: "img", AgentPort: 3010})

	result, err := e.Exec(context.Background(), inst.ID, []string{"echo", "hi"}, execenv.ExecOptions{})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode: %d", result.ExitCode)
	}
}

func TestSocket_Snapshot(t *testing.T) {
	d := newFakeDocker()
	e := newTestSocket(d)

	inst, _ := e.Provision(context.Background(), execenv.ProvisionSpec{SessionID: "s1", Image: "img", AgentPort: 3010})

	ref, err := e.Snapshot(context.Background(), inst.ID, execenv.SnapshotOptions{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !strings.HasPrefix(string(ref), "sha256:") {
		t.Errorf("ImageRef: %q", ref)
	}
}

func TestSocket_Destroy(t *testing.T) {
	d := newFakeDocker()
	e := newTestSocket(d)

	var destroyCalled bool
	e.OnDestroy(func(id execenv.InstanceID) { destroyCalled = true })

	inst, _ := e.Provision(context.Background(), execenv.ProvisionSpec{SessionID: "s1", Image: "img", AgentPort: 3010})

	if err := e.Destroy(context.Background(), inst.ID, execenv.DestroyOptions{}); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if !destroyCalled {
		t.Error("OnDestroy callback not called")
	}
}

func TestSocket_Recover(t *testing.T) {
	d := newFakeDocker()
	d.mu.Lock()
	// Add a container that has the managed label and session ID but no port binding
	// (socket mode doesn't use ports — we use container name for address).
	d.containers["cid-s1"] = &fakeContainer{
		id:     "cid-s1",
		name:   "sandbox-my-sess",
		image:  "img",
		labels: map[string]string{labelManaged: "true", labelSessionID: "my-sess"},
		running: true,
		ports:  nat.PortMap{nat.Port("3010/tcp"): []nat.PortBinding{{HostIP: ""}}},
	}
	// A stopped managed container — should be reclaimed (destroyed), not adopted.
	d.containers["cid-s2"] = &fakeContainer{
		id:      "cid-s2",
		name:    "sandbox-stopped-sess",
		image:   "img",
		labels:  map[string]string{labelManaged: "true", labelSessionID: "stopped-sess"},
		running: false,
	}
	d.mu.Unlock()

	e := newTestSocket(d)
	recovered, err := e.Recover(context.Background())
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if len(recovered) != 1 {
		t.Fatalf("expected 1 recovered (running only), got %d", len(recovered))
	}
	if recovered[0].SessionID != "my-sess" {
		t.Errorf("SessionID: %q", recovered[0].SessionID)
	}

	// The stopped container should have been removed (reclaimed).
	d.mu.Lock()
	_, stillThere := d.containers["cid-s2"]
	d.mu.Unlock()
	if stillThere {
		t.Errorf("stopped container cid-s2 should have been destroyed on recover")
	}
}

func TestSocket_Capabilities(t *testing.T) {
	d := newFakeDocker()
	e := newTestSocket(d)
	caps := e.Capabilities()
	if caps.Backend != execenv.BackendDockerSocket {
		t.Errorf("Backend: %q", caps.Backend)
	}
	if caps.Tenancy != execenv.TenancyPerSession {
		t.Errorf("Tenancy: %q", caps.Tenancy)
	}
	if !caps.SupportsSnapshot || !caps.SupportsExec {
		t.Errorf("missing capability: %+v", caps)
	}
}

// ─── waitForHealthy tests ─────────────────────────────────────────────────────

func TestWaitForHealthy_SuccessFirstTry(t *testing.T) {
	d := newFakeDocker()
	e := newTestDinD(d)
	e.poller = func(_ context.Context, _ string) bool { return true }

	ok := e.waitForHealthy(context.Background(), "http://localhost:30000", 3, 0)
	if !ok {
		t.Error("should succeed immediately")
	}
}

func TestWaitForHealthy_SuccessAfterRetries(t *testing.T) {
	d := newFakeDocker()
	e := newTestDinD(d)
	calls := 0
	e.poller = func(_ context.Context, _ string) bool {
		calls++
		return calls >= 3 // succeed on 3rd try
	}

	ok := e.waitForHealthy(context.Background(), "http://localhost:30000", 5, 0)
	if !ok {
		t.Error("should succeed after retries")
	}
	if calls != 3 {
		t.Errorf("expected 3 poller calls, got %d", calls)
	}
}

func TestWaitForHealthy_ExhaustedRetries(t *testing.T) {
	d := newFakeDocker()
	e := newTestDinD(d)
	e.poller = func(_ context.Context, _ string) bool { return false }

	ok := e.waitForHealthy(context.Background(), "http://localhost:30000", 3, 0)
	if ok {
		t.Error("should fail after max retries")
	}
}

func TestWaitForHealthy_ContextCancelled(t *testing.T) {
	d := newFakeDocker()
	e := newTestDinD(d)
	e.poller = func(_ context.Context, _ string) bool { return false }

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	ok := e.waitForHealthy(ctx, "http://localhost:30000", 1000, 10*time.Millisecond)
	if ok {
		t.Error("should fail on context cancellation")
	}
}

// ─── filterArgs test ──────────────────────────────────────────────────────────

func TestBuildLabelFilter(t *testing.T) {
	f := buildLabelFilter()
	// Verify the filter contains the agentkit.managed label.
	got := f.Get("label")
	if len(got) == 0 {
		t.Fatal("filter has no label entries")
	}
	found := false
	for _, v := range got {
		if v == labelManaged+"=true" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("filter missing %q=true label, got %v", labelManaged, got)
	}
	_ = dockerfilters.Args{} // ensure import is used
}

// ─── isNotRunning / isNotFound ────────────────────────────────────────────────

func TestIsNotRunning(t *testing.T) {
	if !isNotRunning(fmt.Errorf("container is not running")) {
		t.Error("should detect 'is not running'")
	}
	if !isNotRunning(fmt.Errorf("status 304")) {
		t.Error("should detect 304")
	}
	if isNotRunning(fmt.Errorf("some other error")) {
		t.Error("should not detect unrelated error")
	}
	if isNotRunning(nil) {
		t.Error("nil should return false")
	}
}

func TestIsNotFound(t *testing.T) {
	if !isNotFound(fmt.Errorf("no such container: abc")) {
		t.Error("should detect 'no such container'")
	}
	if !isNotFound(fmt.Errorf("not found")) {
		t.Error("should detect 'not found'")
	}
	if isNotFound(fmt.Errorf("some other error")) {
		t.Error("should not detect unrelated error")
	}
}
