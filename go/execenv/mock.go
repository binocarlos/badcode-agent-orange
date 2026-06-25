package execenv

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/bayes-price/agentkit/internal/recorder"
)

// MockExecutionEnvironment is an in-memory ExecutionEnvironment for testing the
// orchestration core and Runner with no Docker, no registry, no network. It
// keeps instances in a map, hands out mock:// addresses, records every call, and
// makes Snapshot return a deterministic ref the ImageRegistry mock can round-trip.
type MockExecutionEnvironment struct {
	recorder.Recorder

	mu        sync.Mutex
	instances map[InstanceID]*Instance
	onDestroy []func(InstanceID)
	clock     time.Time

	// Err, if set, is returned by the next mutating call (for failure injection).
	Err error
	// Caps overrides the reported capabilities (zero value = all-capable, isolated).
	Caps *Capabilities
	// AddrOverride, if set, is used as the Instance.Address instead of a mock:// URL
	// — point it at an httptest server to exercise the sandbox HTTP contract.
	AddrOverride string

	// ExecStdinCapture, if true, causes Exec to capture the stdin bytes read from
	// ExecOptions.Stdin for each call.
	ExecStdinCapture bool
	// ExecStdinLog records stdin bytes per Exec call in insertion order. Each entry
	// maps the concatenated cmd (joined by " ") to the bytes read from Stdin.
	ExecStdinLog []ExecStdinEntry

	// ExecStdoutByCmd, if non-nil, is consulted when Exec is called: if the
	// concatenated cmd string (joined by " ") is a key, the associated bytes are
	// returned as ExecResult.Stdout.
	ExecStdoutByCmd map[string][]byte

	// ExecExitByCmd, if non-nil, overrides ExecResult.ExitCode when the
	// concatenated cmd string (joined by " ") is a key. Used to simulate a
	// failing install.sh in composition-build tests.
	ExecExitByCmd map[string]int

	// Provisions records every full ProvisionSpec passed to Provision, in order.
	// Use it to assert on fields the string Recorder can't capture (Mounts, Env...).
	Provisions []ProvisionSpec
}

// ExecStdinEntry records one Exec call's stdin bytes alongside the command.
type ExecStdinEntry struct {
	Cmd   []string
	Stdin []byte
}

// NewMock returns an empty in-memory environment.
func NewMock() *MockExecutionEnvironment {
	return &MockExecutionEnvironment{
		instances: map[InstanceID]*Instance{},
		clock:     time.Unix(0, 0).UTC(),
	}
}

func instIDFor(sessionID string) InstanceID { return InstanceID("inst-" + sessionID) }

func (m *MockExecutionEnvironment) Provision(ctx context.Context, spec ProvisionSpec) (*Instance, error) {
	m.Record("Provision", spec.SessionID, string(spec.Image))
	if m.Err != nil {
		return nil, m.takeErr()
	}
	id := instIDFor(spec.SessionID)
	addr := "mock://instance/" + string(id)
	if m.AddrOverride != "" {
		addr = m.AddrOverride
	}
	inst := &Instance{
		ID:        id,
		SessionID: spec.SessionID,
		Address:   addr,
		State:     StateRunning,
		Image:     spec.Image,
		CreatedAt: m.clock,
	}
	m.mu.Lock()
	m.instances[id] = inst
	m.Provisions = append(m.Provisions, spec)
	m.mu.Unlock()
	cp := *inst
	return &cp, nil
}

func (m *MockExecutionEnvironment) Exec(ctx context.Context, id InstanceID, cmd []string, opts ExecOptions) (*ExecResult, error) {
	m.Record("Exec", string(id), cmd)
	if m.Err != nil {
		return nil, m.takeErr()
	}
	res := &ExecResult{ExitCode: 0}

	// Capture stdin bytes for write-assertion tests.
	if m.ExecStdinCapture && opts.Stdin != nil {
		data, _ := io.ReadAll(opts.Stdin)
		m.mu.Lock()
		m.ExecStdinLog = append(m.ExecStdinLog, ExecStdinEntry{Cmd: append([]string(nil), cmd...), Stdin: data})
		m.mu.Unlock()
	}

	// Return seeded stdout when the command key matches.
	if m.ExecStdoutByCmd != nil {
		key := strings.Join(cmd, " ")
		if out, ok := m.ExecStdoutByCmd[key]; ok {
			res.Stdout = out
		}
	}

	if m.ExecExitByCmd != nil {
		key := strings.Join(cmd, " ")
		if code, ok := m.ExecExitByCmd[key]; ok {
			res.ExitCode = code
		}
	}

	return res, nil
}

func (m *MockExecutionEnvironment) Snapshot(ctx context.Context, id InstanceID, opts SnapshotOptions) (ImageRef, error) {
	m.Record("Snapshot", string(id), opts.ForceFull)
	if m.Err != nil {
		return "", m.takeErr()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	inst, ok := m.instances[id]
	if !ok {
		return "", fmt.Errorf("mock execenv: instance %q not found", id)
	}
	return ImageRef("mock-image:" + inst.SessionID), nil
}

func (m *MockExecutionEnvironment) Destroy(ctx context.Context, id InstanceID, opts DestroyOptions) error {
	m.Record("Destroy", string(id), opts.SkipSnapshot)
	if m.Err != nil {
		return m.takeErr()
	}
	m.setState(id, StateDestroyed)
	m.mu.Lock()
	cbs := append([]func(InstanceID){}, m.onDestroy...)
	m.mu.Unlock()
	for _, cb := range cbs {
		cb(id)
	}
	return nil
}

func (m *MockExecutionEnvironment) Status(ctx context.Context, id InstanceID) (*InstanceStatus, error) {
	m.Record("Status", string(id))
	m.mu.Lock()
	defer m.mu.Unlock()
	inst, ok := m.instances[id]
	if !ok {
		return &InstanceStatus{ID: id, State: StateDestroyed}, nil
	}
	return &InstanceStatus{ID: id, State: inst.State, Address: inst.Address}, nil
}

func (m *MockExecutionEnvironment) Recover(ctx context.Context) ([]*Instance, error) {
	m.Record("Recover")
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Instance
	for _, inst := range m.instances {
		if inst.State == StateRunning {
			cp := *inst
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (m *MockExecutionEnvironment) OnDestroy(cb func(id InstanceID)) {
	m.mu.Lock()
	m.onDestroy = append(m.onDestroy, cb)
	m.mu.Unlock()
}

func (m *MockExecutionEnvironment) Capabilities() Capabilities {
	if m.Caps != nil {
		return *m.Caps
	}
	return Capabilities{
		SupportsSnapshot:   true,
		SupportsExec:       true,
		IsolatedPerSession: true, // deprecated; derived from Tenancy == TenancyPerSession
		Backend:            BackendDockerDinD,
		Tenancy:            TenancyPerSession,
		IsolationTier:      TierContainer,
	}
}

// CapturedStdinLog returns a copy of the ExecStdinLog (safe to iterate — shallow
// copy; byte slices share backing array with original entries).
func (m *MockExecutionEnvironment) CapturedStdinLog() []ExecStdinEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ExecStdinEntry, len(m.ExecStdinLog))
	copy(out, m.ExecStdinLog)
	return out
}

func (m *MockExecutionEnvironment) setState(id InstanceID, s InstanceState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if inst, ok := m.instances[id]; ok {
		inst.State = s
	}
}

func (m *MockExecutionEnvironment) takeErr() error {
	err := m.Err
	m.Err = nil
	return err
}

// Compile-time assertion.
var _ ExecutionEnvironment = (*MockExecutionEnvironment)(nil)
