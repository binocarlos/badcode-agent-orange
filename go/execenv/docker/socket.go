package docker

// socket.go — shared-daemon socket ExecutionEnvironment adapter.
//
// Provisions per-session containers against the host Docker socket
// (/var/run/docker.sock or DOCKER_HOST). Containers are placed on a shared
// network and addressed by container DNS name (http://<name>:<agentPort>).
// No port allocator is needed; isolation is at the container (not process) level.
//
// Porting source: orchestrator/src/sandbox-manager.ts (socket/non-DinD branches)
// See docs/02-execution-environment.md and AG-5.md.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	dockertypes "github.com/docker/docker/api/types"
	dockercontainer "github.com/docker/docker/api/types/container"

	"github.com/binocarlos/badcode-agent-orange/execenv"
)

// SocketConfig configures the socket adapter.
type SocketConfig struct {
	// DockerHost overrides the Docker socket path or URL. If empty, the default
	// socket (/var/run/docker.sock) is used.
	DockerHost string

	// Network is the Docker network the containers are attached to.
	// The host process must be on the same network to reach the container by DNS.
	// Defaults to "bridge".
	Network string
}

// socketState holds the runtime information for a provisioned socket container.
type socketState struct {
	inst        execenv.Instance
	containerID string
}

// Socket implements execenv.ExecutionEnvironment using the host Docker socket.
// Each session gets its own container (TenancyPerSession); the host reaches the
// agent at http://<containerName>:<agentPort> on the shared Docker network.
//
// Compile-time assertion that Socket implements ExecutionEnvironment:
var _ execenv.ExecutionEnvironment = (*Socket)(nil)

// Socket implements execenv.ExecutionEnvironment for shared Docker socket mode.
type Socket struct {
	cfg    SocketConfig
	docker dockerAPI

	mu         sync.Mutex
	containers map[execenv.InstanceID]*socketState
	onDestroy  []func(execenv.InstanceID)

	// poller is injected for testing (same pattern as DinD).
	poller func(ctx context.Context, address string) bool
	// healthRetryInterval is the sleep between health poll attempts.
	// Defaults to 1s; set to 0 in tests for instant retries.
	healthRetryInterval time.Duration
}

// NewSocket constructs a Socket adapter using the real Docker client.
func NewSocket(cfg SocketConfig) (*Socket, error) {
	if cfg.Network == "" {
		cfg.Network = "bridge"
	}
	d, err := newRealClient(cfg.DockerHost)
	if err != nil {
		return nil, err
	}
	return newSocketWith(cfg, d), nil
}

// newSocketWith constructs a Socket from an already-built dockerAPI.
// Used by tests to inject the fake client.
func newSocketWith(cfg SocketConfig, d dockerAPI) *Socket {
	return &Socket{
		cfg:                 cfg,
		docker:              d,
		containers:          make(map[execenv.InstanceID]*socketState),
		poller:              defaultPoller,
		healthRetryInterval: 1 * time.Second,
	}
}

// ─── ExecutionEnvironment ────────────────────────────────────────────────────

// Provision creates and starts a new per-session container.
// The agent is reachable at http://<containerName>:<agentPort>.
// Ported from sandbox-manager.ts createSandbox@193 (socket branch).
func (e *Socket) Provision(ctx context.Context, spec execenv.ProvisionSpec) (*execenv.Instance, error) {
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

	name := containerName(spec.SessionID)
	agentPort := spec.AgentPort
	if agentPort == 0 {
		agentPort = 3010
	}

	env := mergeEnv(spec.Env, map[string]string{
		"SESSION_ID": spec.SessionID,
		"PORT":       fmt.Sprintf("%d", agentPort),
	})

	labels := mergeStringMap(spec.Labels, map[string]string{
		labelManaged:   "true",
		labelSessionID: spec.SessionID,
	})

	hostCfg := &dockercontainer.HostConfig{
		NetworkMode: dockercontainer.NetworkMode(e.cfg.Network),
	}
	if spec.Resources.MemoryMB > 0 {
		hostCfg.Memory = int64(spec.Resources.MemoryMB) * 1024 * 1024
	}
	if spec.Resources.CPUMillis > 0 {
		hostCfg.NanoCPUs = int64(spec.Resources.CPUMillis) * 1_000_000
	}
	if len(spec.Mounts) > 0 {
		binds := make([]string, 0, len(spec.Mounts))
		for _, m := range spec.Mounts {
			entry := m.Source + ":" + m.Target
			if m.ReadOnly {
				entry += ":ro"
			}
			binds = append(binds, entry)
		}
		hostCfg.Binds = binds
	}

	containerCfg := &dockercontainer.Config{
		Image:  string(spec.Image),
		Env:    buildEnvList(env),
		Labels: labels,
	}

	resp, createErr := e.docker.ContainerCreate(ctx, containerCfg, hostCfg, nil, nil, name)
	if createErr != nil {
		return nil, fmt.Errorf("socket provision: create container: %w", createErr)
	}

	if startErr := e.docker.ContainerStart(ctx, resp.ID, dockertypes.ContainerStartOptions{}); startErr != nil {
		_ = e.docker.ContainerRemove(ctx, resp.ID, dockertypes.ContainerRemoveOptions{Force: true})
		return nil, fmt.Errorf("socket provision: start container: %w", startErr)
	}

	id := execenv.InstanceID(resp.ID)
	// Address the agent by container DNS name on the shared network.
	address := fmt.Sprintf("http://%s:%d", name, agentPort)
	inst := execenv.Instance{
		ID:        id,
		SessionID: spec.SessionID,
		Address:   address,
		State:     execenv.StateRunning,
		Image:     spec.Image,
		CreatedAt: time.Now().UTC(),
	}

	e.mu.Lock()
	e.containers[id] = &socketState{inst: inst, containerID: resp.ID}
	e.mu.Unlock()

	cp := inst
	return &cp, nil
}

// Exec runs a command inside the container.
func (e *Socket) Exec(ctx context.Context, id execenv.InstanceID, cmd []string, opts execenv.ExecOptions) (*execenv.ExecResult, error) {
	e.mu.Lock()
	s, ok := e.containers[id]
	e.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("socket exec: instance %q not found", id)
	}

	execCtx := ctx
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	stdout, stderr, exitCode, err := execAndCollect(execCtx, e.docker, s.containerID, cmd, opts.WorkingDir, opts.Env)
	if err != nil {
		return nil, fmt.Errorf("socket exec: %w", err)
	}
	return &execenv.ExecResult{ExitCode: exitCode, Stdout: stdout, Stderr: stderr}, nil
}

// Snapshot commits the container's filesystem as a new image.
// Ported from sandbox-manager.ts archiveSandbox@886 (docker commit step).
func (e *Socket) Snapshot(ctx context.Context, id execenv.InstanceID, opts execenv.SnapshotOptions) (execenv.ImageRef, error) {
	e.mu.Lock()
	s, ok := e.containers[id]
	e.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("socket snapshot: instance %q not found", id)
	}

	commitOpts := dockertypes.ContainerCommitOptions{}
	if opts.Tag != "" {
		parts := strings.SplitN(opts.Tag, ":", 2)
		if len(parts) == 2 {
			commitOpts.Reference = parts[0] + ":" + parts[1]
		} else {
			commitOpts.Reference = opts.Tag
		}
	}

	result, err := e.docker.ContainerCommit(ctx, s.containerID, commitOpts)
	if err != nil {
		return "", fmt.Errorf("socket snapshot: commit: %w", err)
	}
	return execenv.ImageRef(result.ID), nil
}

// Destroy stops and removes the container.
// Ported from sandbox-manager.ts destroySandbox@377 (no-archive path) +
// removeSandbox@1211.
func (e *Socket) Destroy(ctx context.Context, id execenv.InstanceID, opts execenv.DestroyOptions) error {
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

	if removeErr := e.docker.ContainerRemove(ctx, s.containerID, dockertypes.ContainerRemoveOptions{Force: true}); removeErr != nil {
		if !isNotFound(removeErr) {
			return fmt.Errorf("socket destroy: remove container: %w", removeErr)
		}
	}

	e.mu.Lock()
	delete(e.containers, id)
	cbs := append([]func(execenv.InstanceID){}, e.onDestroy...)
	e.mu.Unlock()

	for _, cb := range cbs {
		cb(id)
	}
	return nil
}

// Status reports the runtime state by inspecting the container.
func (e *Socket) Status(ctx context.Context, id execenv.InstanceID) (*execenv.InstanceStatus, error) {
	e.mu.Lock()
	s, ok := e.containers[id]
	e.mu.Unlock()
	if !ok {
		return &execenv.InstanceStatus{ID: id, State: execenv.StateDestroyed}, nil
	}

	info, err := e.docker.ContainerInspect(ctx, s.containerID)
	if err != nil {
		if isNotFound(err) {
			return &execenv.InstanceStatus{ID: id, State: execenv.StateDestroyed}, nil
		}
		return nil, fmt.Errorf("socket status: inspect: %w", err)
	}

	state := mapContainerState(info)
	return &execenv.InstanceStatus{
		ID:      id,
		State:   state,
		Address: s.inst.Address,
	}, nil
}

// Recover lists containers labelled agentkit.managed=true and re-adopts them.
// Ported from sandbox-manager.ts recoverContainers@1516 (socket branch).
func (e *Socket) Recover(ctx context.Context) ([]*execenv.Instance, error) {
	listed, err := e.docker.ContainerList(ctx, dockertypes.ContainerListOptions{
		All:     true,
		Filters: buildLabelFilter(),
	})
	if err != nil {
		return nil, fmt.Errorf("socket recover: list containers: %w", err)
	}

	var recovered []*execenv.Instance
	for _, c := range listed {
		sessionID := c.Labels[labelSessionID]
		if sessionID == "" {
			continue
		}

		// Lifecycle is Running-or-Archived: a managed container found stopped is
		// reclaimable resource, not a warm session. Destroy it and skip — the
		// session restores from its snapshot on next use.
		if c.State != "running" {
			if removeErr := e.docker.ContainerRemove(ctx, c.ID, dockertypes.ContainerRemoveOptions{Force: true}); removeErr != nil && !isNotFound(removeErr) {
				return nil, fmt.Errorf("socket recover: reclaim stopped container %s: %w", c.ID, removeErr)
			}
			continue
		}

		name := containerName(sessionID)
		// Derive agentPort from ports if advertised.
		agentPort := 3010
		for _, p := range c.Ports {
			if p.PrivatePort != 0 {
				agentPort = int(p.PrivatePort)
				break
			}
		}

		address := fmt.Sprintf("http://%s:%d", name, agentPort)

		id := execenv.InstanceID(c.ID)
		image := execenv.ImageRef(c.Image)
		createdAt := time.Unix(c.Created, 0).UTC()

		inst := &execenv.Instance{
			ID:        id,
			SessionID: sessionID,
			Address:   address,
			State:     execenv.StateRunning,
			Image:     image,
			CreatedAt: createdAt,
		}

		e.mu.Lock()
		e.containers[id] = &socketState{inst: *inst, containerID: c.ID}
		e.mu.Unlock()

		recovered = append(recovered, inst)
	}
	return recovered, nil
}

// OnDestroy registers a callback fired when any instance is destroyed.
func (e *Socket) OnDestroy(cb func(id execenv.InstanceID)) {
	e.mu.Lock()
	e.onDestroy = append(e.onDestroy, cb)
	e.mu.Unlock()
}

// Capabilities reports socket adapter capabilities.
// Socket is per-session (one container per session), TierContainer isolation.
func (e *Socket) Capabilities() execenv.Capabilities {
	return execenv.Capabilities{
		SupportsSnapshot:   true,
		SupportsExec:       true,
		IsolatedPerSession: true, // deprecated but kept for back-compat
		Backend:            execenv.BackendDockerSocket,
		Tenancy:            execenv.TenancyPerSession,
		IsolationTier:      execenv.TierContainer,
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// waitForHealthy polls <address>/health with the injected poller.
// Identical logic to DinD.waitForHealthy; separated to keep the adapters
// independent (no shared state).
func (e *Socket) waitForHealthy(ctx context.Context, address string, maxRetries int, interval time.Duration) bool {
	for i := 0; i < maxRetries; i++ {
		if ctx.Err() != nil {
			return false
		}
		if e.poller(ctx, address) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(interval):
		}
	}
	return false
}
