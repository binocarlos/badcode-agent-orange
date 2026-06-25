//go:build integration

package systemtest

// testenv_test.go — real-Docker ExecutionEnvironment for system tests.
//
// TestEnv implements execenv.ExecutionEnvironment using the host Docker socket
// with --network host. Each container gets a unique PORT env variable and is
// health-checked before Provision returns. Containers are labelled for cleanup.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	dockertypes "github.com/docker/docker/api/types"
	dockercontainer "github.com/docker/docker/api/types/container"
	dockerfilters "github.com/docker/docker/api/types/filters"
	dockernetwork "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/bayes-price/agentkit/execenv"
)

const (
	testLabel      = "agentkit.systemtest"
	testLabelValue = "true"
)

// testState tracks a provisioned container.
type testState struct {
	inst        execenv.Instance
	containerID string
	port        int
}

// TestEnv implements execenv.ExecutionEnvironment using real Docker with --network host.
type TestEnv struct {
	docker       *client.Client
	mockProxyURL string

	mu         sync.Mutex
	containers map[execenv.InstanceID]*testState
	onDestroy  []func(execenv.InstanceID)
}

// Compile-time assertion.
var _ execenv.ExecutionEnvironment = (*TestEnv)(nil)

// NewTestEnv creates a TestEnv connected to the local Docker socket.
func NewTestEnv(mockProxyURL string) (*TestEnv, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("testenv: docker client: %w", err)
	}
	return &TestEnv{
		docker:       cli,
		mockProxyURL: mockProxyURL,
		containers:   make(map[execenv.InstanceID]*testState),
	}, nil
}

func (e *TestEnv) Provision(ctx context.Context, spec execenv.ProvisionSpec) (*execenv.Instance, error) {
	// Idempotency: if already provisioned for this session, return existing.
	e.mu.Lock()
	for _, s := range e.containers {
		if s.inst.SessionID == spec.SessionID {
			cp := s.inst
			e.mu.Unlock()
			return &cp, nil
		}
	}
	e.mu.Unlock()

	port := getFreePort()

	env := make(map[string]string)
	for k, v := range spec.Env {
		env[k] = v
	}
	env["SESSION_ID"] = spec.SessionID
	env["PORT"] = fmt.Sprintf("%d", port)
	if e.mockProxyURL != "" {
		env["ANTHROPIC_BASE_URL"] = e.mockProxyURL
		// Provide a dummy API key so the claude subprocess does not exit
		// immediately with "Not logged in" — the mock proxy doesn't check
		// credentials, but the CLI requires a key to be present.
		if _, hasKey := env["ANTHROPIC_API_KEY"]; !hasKey {
			env["ANTHROPIC_API_KEY"] = "sk-ant-systemtest-mock-key"
		}
	}

	labels := make(map[string]string)
	for k, v := range spec.Labels {
		labels[k] = v
	}
	labels[testLabel] = testLabelValue
	labels["agentkit.session-id"] = spec.SessionID

	envList := make([]string, 0, len(env))
	for k, v := range env {
		envList = append(envList, k+"="+v)
	}

	containerCfg := &dockercontainer.Config{
		Image:  string(spec.Image),
		Env:    envList,
		Labels: labels,
	}

	hostCfg := &dockercontainer.HostConfig{
		NetworkMode: "host",
	}

	resp, err := e.docker.ContainerCreate(ctx, containerCfg, hostCfg, (*dockernetwork.NetworkingConfig)(nil), (*ocispec.Platform)(nil), "")
	if err != nil {
		return nil, fmt.Errorf("testenv provision: create: %w", err)
	}

	if err := e.docker.ContainerStart(ctx, resp.ID, dockertypes.ContainerStartOptions{}); err != nil {
		_ = e.docker.ContainerRemove(ctx, resp.ID, dockertypes.ContainerRemoveOptions{Force: true})
		return nil, fmt.Errorf("testenv provision: start: %w", err)
	}

	address := fmt.Sprintf("http://127.0.0.1:%d", port)
	if !waitForHealthy(ctx, address, 30*time.Second) {
		// Cleanup on failure.
		timeout := 5
		_ = e.docker.ContainerStop(ctx, resp.ID, dockercontainer.StopOptions{Timeout: &timeout})
		_ = e.docker.ContainerRemove(ctx, resp.ID, dockertypes.ContainerRemoveOptions{Force: true})
		return nil, fmt.Errorf("testenv provision: agent at %s did not become healthy within 30s", address)
	}

	id := execenv.InstanceID(resp.ID)
	inst := execenv.Instance{
		ID:        id,
		SessionID: spec.SessionID,
		Address:   address,
		State:     execenv.StateRunning,
		Image:     spec.Image,
		CreatedAt: time.Now().UTC(),
	}

	e.mu.Lock()
	e.containers[id] = &testState{inst: inst, containerID: resp.ID, port: port}
	e.mu.Unlock()

	cp := inst
	return &cp, nil
}

func (e *TestEnv) Suspend(ctx context.Context, id execenv.InstanceID) error {
	e.mu.Lock()
	s, ok := e.containers[id]
	e.mu.Unlock()
	if !ok {
		return fmt.Errorf("testenv suspend: instance %q not found", id)
	}
	if s.inst.State == execenv.StateSuspended {
		return nil
	}

	timeout := 5
	if err := e.docker.ContainerStop(ctx, s.containerID, dockercontainer.StopOptions{Timeout: &timeout}); err != nil {
		if !strings.Contains(err.Error(), "is not running") {
			return fmt.Errorf("testenv suspend: stop: %w", err)
		}
	}

	e.mu.Lock()
	s.inst.State = execenv.StateSuspended
	e.mu.Unlock()
	return nil
}

func (e *TestEnv) Resume(ctx context.Context, id execenv.InstanceID) (*execenv.Instance, error) {
	e.mu.Lock()
	s, ok := e.containers[id]
	e.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("testenv resume: instance %q not found", id)
	}
	if s.inst.State == execenv.StateRunning {
		cp := s.inst
		return &cp, nil
	}

	if err := e.docker.ContainerStart(ctx, s.containerID, dockertypes.ContainerStartOptions{}); err != nil {
		return nil, fmt.Errorf("testenv resume: start: %w", err)
	}

	if !waitForHealthy(ctx, s.inst.Address, 30*time.Second) {
		return nil, fmt.Errorf("testenv resume: agent at %s did not become healthy", s.inst.Address)
	}

	e.mu.Lock()
	s.inst.State = execenv.StateRunning
	cp := s.inst
	e.mu.Unlock()
	return &cp, nil
}

func (e *TestEnv) Exec(ctx context.Context, id execenv.InstanceID, cmd []string, opts execenv.ExecOptions) (*execenv.ExecResult, error) {
	e.mu.Lock()
	s, ok := e.containers[id]
	e.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("testenv exec: instance %q not found", id)
	}

	execCtx := ctx
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	envList := make([]string, 0, len(opts.Env))
	for k, v := range opts.Env {
		envList = append(envList, k+"="+v)
	}

	execCfg := dockertypes.ExecConfig{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          cmd,
		WorkingDir:   opts.WorkingDir,
		Env:          envList,
	}

	createResp, err := e.docker.ContainerExecCreate(execCtx, s.containerID, execCfg)
	if err != nil {
		return nil, fmt.Errorf("testenv exec: create: %w", err)
	}

	attachResp, err := e.docker.ContainerExecAttach(execCtx, createResp.ID, dockertypes.ExecStartCheck{})
	if err != nil {
		return nil, fmt.Errorf("testenv exec: attach: %w", err)
	}
	defer attachResp.Close()

	var stdoutBuf, stderrBuf bytes.Buffer
	_, err = stdcopy.StdCopy(&stdoutBuf, &stderrBuf, attachResp.Reader)
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("testenv exec: read: %w", err)
	}

	inspect, err := e.docker.ContainerExecInspect(execCtx, createResp.ID)
	if err != nil {
		return nil, fmt.Errorf("testenv exec: inspect: %w", err)
	}

	return &execenv.ExecResult{
		ExitCode: inspect.ExitCode,
		Stdout:   stdoutBuf.Bytes(),
		Stderr:   stderrBuf.Bytes(),
	}, nil
}

func (e *TestEnv) Snapshot(ctx context.Context, id execenv.InstanceID, opts execenv.SnapshotOptions) (execenv.ImageRef, error) {
	e.mu.Lock()
	s, ok := e.containers[id]
	e.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("testenv snapshot: instance %q not found", id)
	}

	commitOpts := dockertypes.ContainerCommitOptions{}
	if opts.Tag != "" {
		commitOpts.Reference = opts.Tag
	}

	result, err := e.docker.ContainerCommit(ctx, s.containerID, commitOpts)
	if err != nil {
		return "", fmt.Errorf("testenv snapshot: commit: %w", err)
	}
	return execenv.ImageRef(result.ID), nil
}

func (e *TestEnv) Destroy(ctx context.Context, id execenv.InstanceID, opts execenv.DestroyOptions) error {
	e.mu.Lock()
	s, ok := e.containers[id]
	if !ok {
		e.mu.Unlock()
		return nil
	}
	s.inst.State = execenv.StateDestroyed
	e.mu.Unlock()

	timeout := 5
	_ = e.docker.ContainerStop(ctx, s.containerID, dockercontainer.StopOptions{Timeout: &timeout})
	_ = e.docker.ContainerRemove(ctx, s.containerID, dockertypes.ContainerRemoveOptions{Force: true})

	e.mu.Lock()
	delete(e.containers, id)
	cbs := append([]func(execenv.InstanceID){}, e.onDestroy...)
	e.mu.Unlock()

	for _, cb := range cbs {
		cb(id)
	}
	return nil
}

func (e *TestEnv) Status(ctx context.Context, id execenv.InstanceID) (*execenv.InstanceStatus, error) {
	e.mu.Lock()
	s, ok := e.containers[id]
	e.mu.Unlock()
	if !ok {
		return &execenv.InstanceStatus{ID: id, State: execenv.StateDestroyed}, nil
	}

	info, err := e.docker.ContainerInspect(ctx, s.containerID)
	if err != nil {
		if strings.Contains(err.Error(), "No such container") {
			return &execenv.InstanceStatus{ID: id, State: execenv.StateDestroyed}, nil
		}
		return nil, fmt.Errorf("testenv status: inspect: %w", err)
	}

	state := execenv.StateSuspended
	if info.State != nil && info.State.Running {
		state = execenv.StateRunning
	}

	return &execenv.InstanceStatus{
		ID:      id,
		State:   state,
		Address: s.inst.Address,
	}, nil
}

func (e *TestEnv) Recover(ctx context.Context) ([]*execenv.Instance, error) {
	filters := dockerfilters.NewArgs()
	filters.Add("label", testLabel+"="+testLabelValue)

	listed, err := e.docker.ContainerList(ctx, dockertypes.ContainerListOptions{
		All:     true,
		Filters: filters,
	})
	if err != nil {
		return nil, fmt.Errorf("testenv recover: list: %w", err)
	}

	var recovered []*execenv.Instance
	for _, c := range listed {
		sessionID := c.Labels["agentkit.session-id"]
		if sessionID == "" {
			continue
		}

		state := execenv.StateSuspended
		if c.State == "running" {
			state = execenv.StateRunning
		}

		id := execenv.InstanceID(c.ID)
		inst := &execenv.Instance{
			ID:        id,
			SessionID: sessionID,
			State:     state,
			Image:     execenv.ImageRef(c.Image),
			CreatedAt: time.Unix(c.Created, 0).UTC(),
		}

		e.mu.Lock()
		e.containers[id] = &testState{inst: *inst, containerID: c.ID}
		e.mu.Unlock()

		recovered = append(recovered, inst)
	}
	return recovered, nil
}

func (e *TestEnv) OnDestroy(cb func(id execenv.InstanceID)) {
	e.mu.Lock()
	e.onDestroy = append(e.onDestroy, cb)
	e.mu.Unlock()
}

func (e *TestEnv) Capabilities() execenv.Capabilities {
	return execenv.Capabilities{
		SupportsSuspend:    true,
		SupportsSnapshot:   true,
		SupportsExec:       true,
		IsolatedPerSession: true,
		Backend:            execenv.BackendDockerSocket,
		Tenancy:            execenv.TenancyPerSession,
		IsolationTier:      execenv.TierContainer,
	}
}

// InstanceForSession returns the live InstanceID for a session, for Exec in tests.
func (e *TestEnv) InstanceForSession(sessionID string) (execenv.InstanceID, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for id, s := range e.containers {
		if s.inst.SessionID == sessionID && s.inst.State != execenv.StateDestroyed {
			return id, true
		}
	}
	return "", false
}

// DestroyAll removes all containers created by this test environment.
func (e *TestEnv) DestroyAll(ctx context.Context) {
	filters := dockerfilters.NewArgs()
	filters.Add("label", testLabel+"="+testLabelValue)

	listed, err := e.docker.ContainerList(ctx, dockertypes.ContainerListOptions{
		All:     true,
		Filters: filters,
	})
	if err != nil {
		return
	}

	for _, c := range listed {
		timeout := 3
		_ = e.docker.ContainerStop(ctx, c.ID, dockercontainer.StopOptions{Timeout: &timeout})
		_ = e.docker.ContainerRemove(ctx, c.ID, dockertypes.ContainerRemoveOptions{Force: true})
	}
}
