package docker

// dind.go — DinD ExecutionEnvironment adapter.
//
// Provisions per-session containers against a Docker daemon exposed over TCP
// (e.g. DinD at tcp://localhost:2375). Each container gets a leased host port
// so the host can reach the in-image agent at http://localhost:<port>.
//
// Porting source: orchestrator/src/sandbox-manager.ts (DinD branches)
// See docs/02-execution-environment.md and AG-5.md.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	dockertypes "github.com/docker/docker/api/types"
	dockercontainer "github.com/docker/docker/api/types/container"
	dockerfilters "github.com/docker/docker/api/types/filters"
	"github.com/docker/go-connections/nat"

	"github.com/bayes-price/agentkit/execenv"
)

// DinDConfig configures the DinD adapter.
type DinDConfig struct {
	// DockerHost is the TCP address of the DinD daemon, e.g. "tcp://localhost:2375".
	// If empty the DOCKER_HOST environment variable is used.
	DockerHost string

	// PortRangeStart and PortRangeEnd define the inclusive host-port pool.
	PortRangeStart int
	PortRangeEnd   int

	// GatewayIP is injected as the callback address (ANTHROPIC_BASE_URL host etc.)
	// so in-container processes can reach services on the Docker host.
	// If empty it defaults to 172.17.0.1.
	GatewayIP string

	// HealthHost is the hostname/IP used by the caller (goapi) to health-check
	// sandbox containers on their published port. Defaults to the hostname in
	// DockerHost (e.g. "dind" from "tcp://dind:2375"). Set to "127.0.0.1" for
	// --network=host or when the caller runs on the same host as the Docker daemon.
	HealthHost string

	// Network is the Docker network mode for the containers. Defaults to "bridge".
	Network string
}

// dindState holds the runtime information for a provisioned container.
type dindState struct {
	inst        execenv.Instance
	containerID string
	hostPort    int
}

// DinD is a per-session ExecutionEnvironment that provisions containers against
// a Docker daemon over TCP. The host reaches the agent at http://localhost:<port>
// via a leased host-port mapping.
//
// Compile-time assertion that DinD implements ExecutionEnvironment:
var _ execenv.ExecutionEnvironment = (*DinD)(nil)

// DinD implements execenv.ExecutionEnvironment for Docker-in-Docker mode.
type DinD struct {
	cfg    DinDConfig
	docker dockerAPI
	ports  *PortAllocator

	mu         sync.Mutex
	containers map[execenv.InstanceID]*dindState
	onDestroy  []func(execenv.InstanceID)

	// poller is the function used by waitForHealthy — injected for testing so
	// tests don't need real sleeps.
	poller func(ctx context.Context, address string) bool
	// healthRetryInterval is the sleep between health poll attempts.
	// Defaults to 1s; set to 0 in tests for instant retries.
	healthRetryInterval time.Duration
}

// NewDinD constructs a DinD adapter using the real Docker client.
func NewDinD(cfg DinDConfig) (*DinD, error) {
	if cfg.PortRangeStart == 0 {
		cfg.PortRangeStart = 30000
	}
	if cfg.PortRangeEnd == 0 {
		cfg.PortRangeEnd = 31000
	}
	if cfg.GatewayIP == "" {
		cfg.GatewayIP = "172.17.0.1"
	}
	if cfg.HealthHost == "" {
		cfg.HealthHost = healthHostFromDockerHost(cfg.DockerHost)
	}
	if cfg.Network == "" {
		cfg.Network = "bridge"
	}
	ports, err := NewPortAllocator(cfg.PortRangeStart, cfg.PortRangeEnd)
	if err != nil {
		return nil, fmt.Errorf("dind: port allocator: %w", err)
	}
	d, err := newRealClient(cfg.DockerHost)
	if err != nil {
		return nil, err
	}
	return newDinDWith(cfg, d, ports), nil
}

// newDinDWith constructs a DinD from an already-built dockerAPI. Used by tests
// to inject the fake client.
func newDinDWith(cfg DinDConfig, d dockerAPI, ports *PortAllocator) *DinD {
	if cfg.HealthHost == "" {
		cfg.HealthHost = healthHostFromDockerHost(cfg.DockerHost)
	}
	return &DinD{
		cfg:                 cfg,
		docker:              d,
		ports:               ports,
		containers:          make(map[execenv.InstanceID]*dindState),
		poller:              defaultPoller,
		healthRetryInterval: 1 * time.Second,
	}
}

// defaultPoller performs an HTTP GET to <address>/health.
// It is the real health checker; tests inject a faster fake.
func defaultPoller(_ context.Context, address string) bool {
	resp, err := http.Get(address + "/health") //nolint:noctx // best-effort health check
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// ─── ExecutionEnvironment ────────────────────────────────────────────────────

// Provision creates and starts a new per-session container.
// Ported from sandbox-manager.ts createSandbox@193 (DinD branch).
func (e *DinD) Provision(ctx context.Context, spec execenv.ProvisionSpec) (*execenv.Instance, error) {
	// Leak guard: if an instance already exists for this session return it.
	e.mu.Lock()
	for _, s := range e.containers {
		if s.inst.SessionID == spec.SessionID {
			cp := s.inst
			e.mu.Unlock()
			return &cp, nil
		}
	}
	e.mu.Unlock()

	// Allocate a host port.
	port, err := e.ports.Allocate(spec.SessionID)
	if err != nil {
		return nil, fmt.Errorf("dind provision: %w", err)
	}

	name := containerName(spec.SessionID)
	agentPort := spec.AgentPort
	if agentPort == 0 {
		agentPort = 3010
	}

	// Build env — start from caller-supplied env, then inject DinD-specific
	// overrides so the container agent can reach the gateway.
	env := mergeEnv(spec.Env, map[string]string{
		"SESSION_ID": spec.SessionID,
		"PORT":       fmt.Sprintf("%d", agentPort),
	})

	// Labels for ownership + Recover.
	labels := mergeStringMap(spec.Labels, map[string]string{
		labelManaged:   "true",
		labelSessionID: spec.SessionID,
	})

	// Build host config: port binding + resource limits.
	hostCfg := &dockercontainer.HostConfig{
		PortBindings: portBindings(agentPort, port),
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
	if spec.Network != "" {
		if err := e.ensureNetwork(ctx, spec.Network); err != nil {
			e.ports.Release(spec.SessionID)
			return nil, err
		}
		hostCfg.NetworkMode = dockercontainer.NetworkMode(spec.Network)
	}

	containerCfg := &dockercontainer.Config{
		Image:        string(spec.Image),
		Env:          buildEnvList(env),
		ExposedPorts: exposedPorts(agentPort),
		Labels:       labels,
	}

	resp, createErr := e.docker.ContainerCreate(ctx, containerCfg, hostCfg, nil, nil, name)
	if createErr != nil {
		e.ports.Release(spec.SessionID)
		return nil, fmt.Errorf("dind provision: create container: %w", createErr)
	}

	if startErr := e.docker.ContainerStart(ctx, resp.ID, dockertypes.ContainerStartOptions{}); startErr != nil {
		e.ports.Release(spec.SessionID)
		// Best-effort cleanup.
		_ = e.docker.ContainerRemove(ctx, resp.ID, dockertypes.ContainerRemoveOptions{Force: true})
		return nil, fmt.Errorf("dind provision: start container: %w", startErr)
	}

	id := execenv.InstanceID(resp.ID)
	address := fmt.Sprintf("http://%s:%d", e.cfg.HealthHost, port)
	inst := execenv.Instance{
		ID:        id,
		SessionID: spec.SessionID,
		Address:   address,
		State:     execenv.StateRunning,
		Image:     spec.Image,
		CreatedAt: time.Now().UTC(),
	}

	e.mu.Lock()
	e.containers[id] = &dindState{inst: inst, containerID: resp.ID, hostPort: port}
	e.mu.Unlock()

	// Wait for the in-image agent's HTTP server to report healthy before returning.
	// The Docker userland proxy binds the published port and accepts TCP the moment
	// the container starts — well before the agent process is listening — so a caller
	// that POSTs /sessions immediately would race the not-yet-ready backend and get a
	// connection reset (the proxy fails to dial the backend). Resume already gates on
	// health; Provision must too, or the very first turn after CreateSession flakes.
	if !e.waitForHealthy(ctx, address, 30, e.healthRetryInterval) {
		e.mu.Lock()
		delete(e.containers, id)
		e.mu.Unlock()
		e.ports.Release(spec.SessionID)
		_ = e.docker.ContainerRemove(ctx, resp.ID, dockertypes.ContainerRemoveOptions{Force: true})
		return nil, fmt.Errorf("dind provision: agent at %s did not become healthy", address)
	}

	cp := inst
	return &cp, nil
}

// Exec runs a command inside the container.
// Ported from sandbox-manager.ts (docker exec usage).
func (e *DinD) Exec(ctx context.Context, id execenv.InstanceID, cmd []string, opts execenv.ExecOptions) (*execenv.ExecResult, error) {
	e.mu.Lock()
	s, ok := e.containers[id]
	e.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("dind exec: instance %q not found", id)
	}

	execCtx := ctx
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	stdout, stderr, exitCode, err := execAndCollect(execCtx, e.docker, s.containerID, cmd, opts.WorkingDir, opts.Env)
	if err != nil {
		return nil, fmt.Errorf("dind exec: %w", err)
	}
	return &execenv.ExecResult{ExitCode: exitCode, Stdout: stdout, Stderr: stderr}, nil
}

// Snapshot commits the container's filesystem as a new image.
// Returns an ImageRef containing the committed image ID.
// Ported from sandbox-manager.ts archiveSandbox@886 (docker commit step).
func (e *DinD) Snapshot(ctx context.Context, id execenv.InstanceID, opts execenv.SnapshotOptions) (execenv.ImageRef, error) {
	e.mu.Lock()
	s, ok := e.containers[id]
	e.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("dind snapshot: instance %q not found", id)
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
		return "", fmt.Errorf("dind snapshot: commit: %w", err)
	}
	return execenv.ImageRef(result.ID), nil
}

// Destroy stops and removes the container, releasing its leased port.
// Ported from sandbox-manager.ts destroySandbox@377 (no-archive path) +
// removeSandbox@1211.
func (e *DinD) Destroy(ctx context.Context, id execenv.InstanceID, opts execenv.DestroyOptions) error {
	e.mu.Lock()
	s, ok := e.containers[id]
	if !ok {
		e.mu.Unlock()
		return nil // already gone — idempotent
	}
	// Optimistically mark as destroyed before releasing the lock so concurrent
	// Destroy calls are no-ops.
	s.inst.State = execenv.StateDestroyed
	e.mu.Unlock()

	timeout := 5
	stopErr := e.docker.ContainerStop(ctx, s.containerID, dockercontainer.StopOptions{Timeout: &timeout})
	if stopErr != nil && !isNotRunning(stopErr) {
		// Non-fatal: still try to remove.
		_ = stopErr
	}

	if removeErr := e.docker.ContainerRemove(ctx, s.containerID, dockertypes.ContainerRemoveOptions{Force: true}); removeErr != nil {
		if !isNotFound(removeErr) {
			return fmt.Errorf("dind destroy: remove container: %w", removeErr)
		}
	}

	e.mu.Lock()
	delete(e.containers, id)
	e.ports.Release(s.inst.SessionID)
	cbs := append([]func(execenv.InstanceID){}, e.onDestroy...)
	e.mu.Unlock()

	for _, cb := range cbs {
		cb(id)
	}
	return nil
}

// Status reports the runtime state by inspecting the container.
// Ported from sandbox-manager.ts getSandbox@1311.
func (e *DinD) Status(ctx context.Context, id execenv.InstanceID) (*execenv.InstanceStatus, error) {
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
		return nil, fmt.Errorf("dind status: inspect: %w", err)
	}

	state := mapContainerState(info)
	return &execenv.InstanceStatus{
		ID:      id,
		State:   state,
		Address: s.inst.Address,
	}, nil
}

// Recover lists containers labelled agentkit.managed=true and re-adopts the
// running ones. A managed container found stopped is reclaimed (destroyed) and
// its port released, since the lifecycle is Running-or-Archived with no warm
// "suspended" state — the session restores from its snapshot on next use.
// Ported from sandbox-manager.ts recoverContainers@1516.
func (e *DinD) Recover(ctx context.Context) ([]*execenv.Instance, error) {
	listed, err := e.docker.ContainerList(ctx, dockertypes.ContainerListOptions{
		All:     true,
		Filters: buildLabelFilter(),
	})
	if err != nil {
		return nil, fmt.Errorf("dind recover: list containers: %w", err)
	}

	var recovered []*execenv.Instance
	for _, c := range listed {
		sessionID := c.Labels[labelSessionID]
		if sessionID == "" {
			continue // orphan with no session — skip
		}

		// Reclaim stopped containers rather than re-adopting them. The port (if
		// any) is freed with the container and stays in the available pool.
		if c.State != "running" {
			if removeErr := e.docker.ContainerRemove(ctx, c.ID, dockertypes.ContainerRemoveOptions{Force: true}); removeErr != nil && !isNotFound(removeErr) {
				return nil, fmt.Errorf("dind recover: reclaim stopped container %s: %w", c.ID, removeErr)
			}
			continue
		}

		// Determine host port from port bindings.
		var hostPort int
		for _, p := range c.Ports {
			if p.PrivatePort == uint16(3010) && p.PublicPort != 0 {
				hostPort = int(p.PublicPort)
				break
			}
		}
		if hostPort == 0 {
			// Try inspecting for port bindings.
			inspected, inspErr := e.docker.ContainerInspect(ctx, c.ID)
			if inspErr == nil {
				bindings := inspected.HostConfig.PortBindings
				for portProto, hostBindings := range bindings {
					if strings.HasPrefix(string(portProto), "3010/") && len(hostBindings) > 0 {
						if _, err := fmt.Sscanf(hostBindings[0].HostPort, "%d", &hostPort); err == nil {
							break
						}
					}
				}
			}
		}
		if hostPort == 0 {
			continue // DinD container without a host port — skip
		}

		// Adopt port (remove from available, mark as allocated).
		e.ports.Adopt(sessionID, hostPort)

		address := fmt.Sprintf("http://%s:%d", e.cfg.HealthHost, hostPort)

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
		e.containers[id] = &dindState{
			inst:        *inst,
			containerID: c.ID,
			hostPort:    hostPort,
		}
		e.mu.Unlock()

		recovered = append(recovered, inst)
	}
	return recovered, nil
}

// OnDestroy registers a callback fired when any instance is destroyed.
func (e *DinD) OnDestroy(cb func(id execenv.InstanceID)) {
	e.mu.Lock()
	e.onDestroy = append(e.onDestroy, cb)
	e.mu.Unlock()
}

// Capabilities reports DinD capabilities.
func (e *DinD) Capabilities() execenv.Capabilities {
	return execenv.Capabilities{
		SupportsSnapshot:   true,
		SupportsExec:       true,
		IsolatedPerSession: true, // deprecated but kept for back-compat
		Backend:            execenv.BackendDockerDinD,
		Tenancy:            execenv.TenancyPerSession,
		IsolationTier:      execenv.TierContainer,
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// waitForHealthy polls <address>/health until the agent responds OK or the
// retry budget is exhausted. The poller function is injected so unit tests
// can control timing without real sleeps.
//
// Ported from sandbox-manager.ts waitForHealthy@1367.
func (e *DinD) waitForHealthy(ctx context.Context, address string, maxRetries int, interval time.Duration) bool {
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

// healthHostFromDockerHost extracts the hostname from a Docker host URL.
// "tcp://dind:2375" → "dind", "unix:///var/run/docker.sock" → "127.0.0.1".
func healthHostFromDockerHost(dockerHost string) string {
	if dockerHost == "" || strings.HasPrefix(dockerHost, "unix://") {
		return "127.0.0.1"
	}
	u, err := url.Parse(dockerHost)
	if err != nil || u.Hostname() == "" {
		return "127.0.0.1"
	}
	return u.Hostname()
}

// isNotRunning returns true for Docker errors meaning the container is already stopped.
func isNotRunning(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "is not running") || strings.Contains(msg, "304")
}

// isNotFound returns true for Docker errors meaning the container/image does not exist.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such container") ||
		strings.Contains(msg, "no such image") ||
		strings.Contains(msg, "not found") ||
		strings.Contains(msg, "404")
}

// mapContainerState converts Docker's container state string to InstanceState.
func mapContainerState(info dockertypes.ContainerJSON) execenv.InstanceState {
	if info.State == nil {
		return execenv.StateError
	}
	switch info.State.Status {
	case "running":
		return execenv.StateRunning
	case "created", "restarting":
		return execenv.StateStarting
	// A stopped container is not a live session in the Running-or-Archived model;
	// report it as destroyed so the Runner restores from snapshot on next use.
	case "exited", "paused", "dead", "removing":
		return execenv.StateDestroyed
	default:
		return execenv.StateError
	}
}

// portBindings creates the HostConfig.PortBindings map for a single port.
func portBindings(containerPort, hostPort int) nat.PortMap {
	key := nat.Port(fmt.Sprintf("%d/tcp", containerPort))
	return nat.PortMap{
		key: []nat.PortBinding{{HostPort: fmt.Sprintf("%d", hostPort)}},
	}
}

// exposedPorts creates the Config.ExposedPorts map for a single port.
func exposedPorts(containerPort int) nat.PortSet {
	key := nat.Port(fmt.Sprintf("%d/tcp", containerPort))
	return nat.PortSet{key: struct{}{}}
}

// mergeEnv merges two env maps; values in override take precedence.
func mergeEnv(base, override map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(override))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range override {
		out[k] = v
	}
	return out
}

// mergeStringMap merges two string maps; values in override take precedence.
func mergeStringMap(base, override map[string]string) map[string]string {
	return mergeEnv(base, override)
}

// ensureNetwork creates a dedicated user-defined bridge network if it does not
// already exist, used as the placement target for untrusted composition-image
// builds. It retains NAT internet egress (apt/pip/npm).
//
// KNOWN LIMITATION (verified 2026-06-17 against the live DinD daemon): a
// dedicated bridge does NOT, on its own, prevent the build container from
// reaching internal services. Docker's inter-bridge isolation only blocks
// container-to-container traffic between bridge subnets; the sandbox-proxy
// listens on the DinD host's own interfaces (network_mode: service:dind), so it
// stays reachable at 172.17.0.1:3080 (-> goapi's anonymous API surface) from any
// bridge. The model proxy (/v1) is token-protected (401) and the build box
// carries no token/creds, so the practical blast radius is bounded, but the
// "no internal reachability" guarantee is NOT enforced here. Real enforcement
// needs an iptables INPUT DROP inside the privileged DinD container (the host IP
// is not inter-bridge traffic) or an egress-allowlist proxy — tracked as a
// follow-up hardening pass. The dedicated network is kept as the hook for it.
func (e *DinD) ensureNetwork(ctx context.Context, name string) error {
	nets, err := e.docker.NetworkList(ctx, dockertypes.NetworkListOptions{
		Filters: dockerfilters.NewArgs(dockerfilters.Arg("name", name)),
	})
	if err != nil {
		return fmt.Errorf("dind ensureNetwork list: %w", err)
	}
	for _, n := range nets {
		if n.Name == name {
			return nil // already exists
		}
	}
	_, err = e.docker.NetworkCreate(ctx, name, dockertypes.NetworkCreate{
		Driver:     "bridge",
		Internal:   false, // need public egress for apt/pip/npm
		Attachable: true,
		Labels:     map[string]string{labelManaged: "true"},
	})
	if err != nil {
		return fmt.Errorf("dind ensureNetwork create %q: %w", name, err)
	}
	return nil
}
