//go:build docker

package docker

// docker_integration_test.go — integration tests that require a real Docker daemon.
//
// These tests are gated behind //go:build docker and are NOT run by the default
// "go test ./..." invocation. They require a live Docker daemon (DinD or socket)
// and a real container image.
//
// Run with:
//   go test -tags docker ./execenv/docker/... -v
//
// A full DinD round-trip (Provision → Exec → Snapshot → Destroy) and a Recover
// re-adoption test are included. Adjust the image and config to match the local
// environment before running.

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/bayes-price/agentkit/execenv"
)

// testImage is the container image used for integration tests. It MUST have a
// long-running default command so the container stays up between Provision and
// Exec/Snapshot (in production the agent image self-starts its HTTP server;
// Provision deliberately does not override the image command). A bare
// `alpine:latest` exits immediately and is therefore unsuitable. Override with
// the INTEGRATION_TEST_IMAGE environment variable.
var testImage = func() string {
	if v := os.Getenv("INTEGRATION_TEST_IMAGE"); v != "" {
		return v
	}
	return "nginx:alpine" // runs nginx in the foreground → stays alive
}()

// TestIntegration_DinD_ProvisionExecSnapshotDestroy runs a full lifecycle
// against a real DinD daemon.
//
// Prerequisites:
//   - DinD daemon running at tcp://localhost:2375
//   - testImage pulled: docker pull alpine:latest
func TestIntegration_DinD_ProvisionExecSnapshotDestroy(t *testing.T) {
	cfg := DinDConfig{
		DockerHost:     "tcp://localhost:2375",
		PortRangeStart: 31000,
		PortRangeEnd:   31100,
		GatewayIP:      "172.17.0.1",
	}
	env, err := NewDinD(cfg)
	if err != nil {
		t.Fatalf("NewDinD: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	spec := execenv.ProvisionSpec{
		SessionID: "integration-test-dind-" + t.Name(),
		Image:     execenv.ImageRef(testImage),
		AgentPort: 3010,
		Env: map[string]string{
			"TEST_VAR": "hello",
		},
	}

	// 1. Provision.
	inst, err := env.Provision(ctx, spec)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	t.Logf("Provisioned: id=%s address=%s", inst.ID, inst.Address)
	defer env.Destroy(ctx, inst.ID, execenv.DestroyOptions{SkipSnapshot: true}) //nolint:errcheck

	// 2. Exec — list files in root.
	result, err := env.Exec(ctx, inst.ID, []string{"ls", "/"}, execenv.ExecOptions{})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	t.Logf("Exec stdout: %s", string(result.Stdout))
	if result.ExitCode != 0 {
		t.Errorf("Exec exit code: %d", result.ExitCode)
	}

	// 3. Snapshot.
	ref, err := env.Snapshot(ctx, inst.ID, execenv.SnapshotOptions{Tag: "integration-test-snap:latest"})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	t.Logf("Snapshot ref: %s", ref)

	// 4. Destroy.
	if err := env.Destroy(ctx, inst.ID, execenv.DestroyOptions{SkipSnapshot: true}); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	t.Log("Destroy OK")
}

// TestIntegration_DinD_Recover provisions a container, then creates a new DinD
// adapter and verifies that Recover re-adopts the running container.
func TestIntegration_DinD_Recover(t *testing.T) {
	cfg := DinDConfig{
		DockerHost:     "tcp://localhost:2375",
		PortRangeStart: 31200,
		PortRangeEnd:   31300,
		GatewayIP:      "172.17.0.1",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// 1. Provision a container with the first adapter instance.
	env1, err := NewDinD(cfg)
	if err != nil {
		t.Fatalf("NewDinD: %v", err)
	}
	inst, err := env1.Provision(ctx, execenv.ProvisionSpec{
		SessionID: "recover-test-" + t.Name(),
		Image:     execenv.ImageRef(testImage),
		AgentPort: 3010,
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	t.Logf("Provisioned for recover test: id=%s address=%s", inst.ID, inst.Address)

	// 2. Create a fresh adapter (simulating a restart) and run Recover.
	env2, err := NewDinD(cfg)
	if err != nil {
		t.Fatalf("NewDinD (recover): %v", err)
	}
	recovered, err := env2.Recover(ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}

	found := false
	for _, r := range recovered {
		if r.SessionID == inst.SessionID {
			found = true
			t.Logf("Re-adopted: id=%s address=%s state=%s", r.ID, r.Address, r.State)
		}
	}
	if !found {
		t.Errorf("Recover did not re-adopt session %q", inst.SessionID)
	}

	// Cleanup via the second adapter.
	if err := env2.Destroy(ctx, inst.ID, execenv.DestroyOptions{SkipSnapshot: true}); err != nil {
		t.Logf("Destroy cleanup: %v", err)
	}
}

// TestIntegration_Socket_ProvisionDestroy runs a basic lifecycle against the
// host Docker socket.
func TestIntegration_Socket_ProvisionDestroy(t *testing.T) {
	cfg := SocketConfig{
		Network: "bridge",
	}
	env, err := NewSocket(cfg)
	if err != nil {
		t.Fatalf("NewSocket: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	spec := execenv.ProvisionSpec{
		SessionID: "integration-test-socket-" + t.Name(),
		Image:     execenv.ImageRef(testImage),
		AgentPort: 3010,
	}

	inst, err := env.Provision(ctx, spec)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	t.Logf("Provisioned: id=%s address=%s", inst.ID, inst.Address)
	defer env.Destroy(ctx, inst.ID, execenv.DestroyOptions{SkipSnapshot: true}) //nolint:errcheck

	// Exec a command.
	result, err := env.Exec(ctx, inst.ID, []string{"echo", "socket-test"}, execenv.ExecOptions{})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	t.Logf("Exec output: %s", string(result.Stdout))

	if err := env.Destroy(ctx, inst.ID, execenv.DestroyOptions{SkipSnapshot: true}); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
}
