package docker

// client.go — shared Docker client helper + narrow internal interface.
//
// The narrow dockerAPI interface covers only the moby client methods used by
// the dind.go and socket.go adapters. The real adapter wraps *client.Client
// (moby); a fake implementing dockerAPI in _test.go lets lifecycle logic be
// unit-tested with NO daemon.
//
// Porting source: orchestrator/src/sandbox-manager.ts constructor@114
// See docs/02-execution-environment.md.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	dockertypes "github.com/docker/docker/api/types"
	dockercontainer "github.com/docker/docker/api/types/container"
	dockerfilters "github.com/docker/docker/api/types/filters"
	dockernetwork "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// dockerAPI is the narrow interface over the moby Docker client that the
// adapters actually use. Keeping it narrow means the fake in _test.go stays
// small and fast to implement, and changes to the real client don't silently
// break tests.
type dockerAPI interface {
	ContainerCreate(
		ctx context.Context,
		config *dockercontainer.Config,
		hostConfig *dockercontainer.HostConfig,
		networkingConfig *dockernetwork.NetworkingConfig,
		platform *ocispec.Platform,
		containerName string,
	) (dockercontainer.CreateResponse, error)

	ContainerStart(ctx context.Context, containerID string, options dockertypes.ContainerStartOptions) error

	ContainerStop(ctx context.Context, containerID string, options dockercontainer.StopOptions) error

	ContainerRemove(ctx context.Context, containerID string, options dockertypes.ContainerRemoveOptions) error

	ContainerInspect(ctx context.Context, containerID string) (dockertypes.ContainerJSON, error)

	ContainerCommit(ctx context.Context, containerID string, options dockertypes.ContainerCommitOptions) (dockertypes.IDResponse, error)

	ContainerList(ctx context.Context, options dockertypes.ContainerListOptions) ([]dockertypes.Container, error)

	ContainerExecCreate(ctx context.Context, container string, config dockertypes.ExecConfig) (dockertypes.IDResponse, error)

	ContainerExecStart(ctx context.Context, execID string, config dockertypes.ExecStartCheck) error

	ContainerExecAttach(ctx context.Context, execID string, config dockertypes.ExecStartCheck) (dockertypes.HijackedResponse, error)

	ContainerExecInspect(ctx context.Context, execID string) (dockertypes.ContainerExecInspect, error)

	ImageList(ctx context.Context, options dockertypes.ImageListOptions) ([]dockertypes.ImageSummary, error)

	NetworkList(ctx context.Context, options dockertypes.NetworkListOptions) ([]dockertypes.NetworkResource, error)

	NetworkCreate(ctx context.Context, name string, options dockertypes.NetworkCreate) (dockertypes.NetworkCreateResponse, error)
}

// realDockerClient wraps *client.Client and satisfies dockerAPI so the real
// adapter can be swapped with the fake in tests.
type realDockerClient struct {
	c *client.Client
}

func (r *realDockerClient) ContainerCreate(ctx context.Context, config *dockercontainer.Config, hostConfig *dockercontainer.HostConfig, networkingConfig *dockernetwork.NetworkingConfig, platform *ocispec.Platform, containerName string) (dockercontainer.CreateResponse, error) {
	return r.c.ContainerCreate(ctx, config, hostConfig, networkingConfig, platform, containerName)
}
func (r *realDockerClient) ContainerStart(ctx context.Context, containerID string, options dockertypes.ContainerStartOptions) error {
	return r.c.ContainerStart(ctx, containerID, options)
}
func (r *realDockerClient) ContainerStop(ctx context.Context, containerID string, options dockercontainer.StopOptions) error {
	return r.c.ContainerStop(ctx, containerID, options)
}
func (r *realDockerClient) ContainerRemove(ctx context.Context, containerID string, options dockertypes.ContainerRemoveOptions) error {
	return r.c.ContainerRemove(ctx, containerID, options)
}
func (r *realDockerClient) ContainerInspect(ctx context.Context, containerID string) (dockertypes.ContainerJSON, error) {
	return r.c.ContainerInspect(ctx, containerID)
}
func (r *realDockerClient) ContainerCommit(ctx context.Context, containerID string, options dockertypes.ContainerCommitOptions) (dockertypes.IDResponse, error) {
	return r.c.ContainerCommit(ctx, containerID, options)
}
func (r *realDockerClient) ContainerList(ctx context.Context, options dockertypes.ContainerListOptions) ([]dockertypes.Container, error) {
	return r.c.ContainerList(ctx, options)
}
func (r *realDockerClient) ContainerExecCreate(ctx context.Context, container string, config dockertypes.ExecConfig) (dockertypes.IDResponse, error) {
	return r.c.ContainerExecCreate(ctx, container, config)
}
func (r *realDockerClient) ContainerExecStart(ctx context.Context, execID string, config dockertypes.ExecStartCheck) error {
	return r.c.ContainerExecStart(ctx, execID, config)
}
func (r *realDockerClient) ContainerExecAttach(ctx context.Context, execID string, config dockertypes.ExecStartCheck) (dockertypes.HijackedResponse, error) {
	return r.c.ContainerExecAttach(ctx, execID, config)
}
func (r *realDockerClient) ContainerExecInspect(ctx context.Context, execID string) (dockertypes.ContainerExecInspect, error) {
	return r.c.ContainerExecInspect(ctx, execID)
}
func (r *realDockerClient) ImageList(ctx context.Context, options dockertypes.ImageListOptions) ([]dockertypes.ImageSummary, error) {
	return r.c.ImageList(ctx, options)
}

func (r *realDockerClient) NetworkList(ctx context.Context, options dockertypes.NetworkListOptions) ([]dockertypes.NetworkResource, error) {
	return r.c.NetworkList(ctx, options)
}

func (r *realDockerClient) NetworkCreate(ctx context.Context, name string, options dockertypes.NetworkCreate) (dockertypes.NetworkCreateResponse, error) {
	return r.c.NetworkCreate(ctx, name, options)
}

// newRealClient constructs a *client.Client from the given host string (e.g.
// "tcp://localhost:2375" for DinD, "" for the default socket path).
func newRealClient(dockerHost string) (*realDockerClient, error) {
	opts := []client.Opt{client.WithAPIVersionNegotiation()}
	if dockerHost != "" {
		opts = append(opts, client.WithHost(dockerHost))
	} else {
		opts = append(opts, client.FromEnv)
	}
	c, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, fmt.Errorf("docker: create client: %w", err)
	}
	return &realDockerClient{c: c}, nil
}

// containerName converts a sessionID into a valid Docker container name.
// Docker names must match [a-zA-Z0-9][a-zA-Z0-9_.-]*.
// We prefix with "sandbox-" and truncate to 63 chars total.
// Ported from sandbox-manager.ts containerName@185.
func containerName(sessionID string) string {
	// Replace disallowed characters with "-"
	sb := strings.Builder{}
	for _, r := range sessionID {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '.' || r == '-' {
			sb.WriteRune(r)
		} else {
			sb.WriteRune('-')
		}
	}
	sanitized := sb.String()
	if len(sanitized) > 54 {
		sanitized = sanitized[:54]
	}
	return "sandbox-" + sanitized
}

// buildEnvList converts a string map into Docker-compatible "KEY=VALUE" env
// entries. Order is deterministic (sorted by key) so tests can assert exact
// env lists.
func buildEnvList(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	// Collect keys then sort.
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sortStrings(keys)
	out := make([]string, 0, len(env))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

// sortStrings sorts a slice of strings in-place (simple insertion sort — the
// lists are tiny so we don't need the stdlib sort import in this file).
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// buildLabelFilter builds a filters.Args that selects containers labelled
// agentkit.managed=true (the library's ownership marker).
func buildLabelFilter() dockerfilters.Args {
	f := dockerfilters.NewArgs()
	f.Add("label", labelManaged+"=true")
	return f
}

// labelManaged is the Docker label the adapters apply to every container they
// own, so Recover can re-adopt them after a host restart.
// Mirrors "platinum.orchestrator=true" from sandbox-manager.ts@308.
const labelManaged = "agentkit.managed"

// labelSessionID is the Docker label that stores the session ID so Recover can
// re-hydrate the Instance without parsing the container name.
const labelSessionID = "agentkit.session-id"

// labelBaseImageID is the Docker label that stores the sha256 image ID the
// container was launched from (survives image tag changes).
const labelBaseImageID = "agentkit.base-image-id"

// execAndCollect creates a Docker exec, attaches, reads stdout+stderr, and
// waits for the process to exit. It returns the combined stdout/stderr bytes
// plus the exit code.
//
// This is shared between DinD and socket adapters.
func execAndCollect(ctx context.Context, d dockerAPI, containerID string, cmd []string, workDir string, envMap map[string]string) (stdout, stderr []byte, exitCode int, err error) {
	env := buildEnvList(envMap)

	// 1. Create the exec.
	execID, err := d.ContainerExecCreate(ctx, containerID, dockertypes.ExecConfig{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          cmd,
		WorkingDir:   workDir,
		Env:          env,
	})
	if err != nil {
		return nil, nil, -1, fmt.Errorf("exec create: %w", err)
	}

	// 2. Attach to capture output.
	resp, err := d.ContainerExecAttach(ctx, execID.ID, dockertypes.ExecStartCheck{})
	if err != nil {
		return nil, nil, -1, fmt.Errorf("exec attach: %w", err)
	}
	defer resp.Close()

	// 3. Read all output. Docker multiplexes stdout+stderr in a framed stream.
	// We demultiplex using StdCopy.
	var stdoutBuf, stderrBuf bytes.Buffer
	_, err = stdcopy.StdCopy(&stdoutBuf, &stderrBuf, resp.Reader)
	if err != nil && err != io.EOF {
		return nil, nil, -1, fmt.Errorf("exec read: %w", err)
	}

	// 4. Inspect for exit code.
	inspect, err := d.ContainerExecInspect(ctx, execID.ID)
	if err != nil {
		return nil, nil, -1, fmt.Errorf("exec inspect: %w", err)
	}

	return stdoutBuf.Bytes(), stderrBuf.Bytes(), inspect.ExitCode, nil
}
