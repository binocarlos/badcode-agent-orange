package docker

import (
	"context"
	"os"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/execenv"
)

// Gated: requires a real DinD daemon. Run with AGENTKIT_DIND_IT=1.
// Verifies an instance on the isolated build network can reach the public
// internet but NOT the sandbox-proxy gateway (internal services).
func TestDinD_BuildNetworkIsolation(t *testing.T) {
	if os.Getenv("AGENTKIT_DIND_IT") == "" {
		t.Skip("set AGENTKIT_DIND_IT=1 to run the DinD build-network isolation integration test")
	}
	ctx := context.Background()
	// Construct a DinD directly, mirroring the existing integration-test pattern in
	// docker_integration_test.go (no helper exists for a *testing.T-scoped DinD).
	e, err := NewDinD(DinDConfig{
		DockerHost:     "tcp://localhost:2375",
		PortRangeStart: 31500,
		PortRangeEnd:   31599,
		GatewayIP:      "172.17.0.1",
	})
	if err != nil {
		t.Fatalf("NewDinD: %v", err)
	}
	inst, err := e.Provision(ctx, execenv.ProvisionSpec{
		SessionID: "buildnet-it",
		Image:     execenv.ImageRef(os.Getenv("AGENTKIT_DIND_IT_IMAGE")),
		Env:       map[string]string{},
		Network:   "agentkit-build-isolated",
		AgentPort: 3010,
	})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	defer e.Destroy(ctx, inst.ID, execenv.DestroyOptions{SkipSnapshot: true}) //nolint:errcheck

	// Public egress should succeed.
	pub, _ := e.Exec(ctx, inst.ID, []string{"sh", "-c", "curl -sS -o /dev/null -w '%{http_code}' https://pypi.org/simple/ || echo FAIL"}, execenv.ExecOptions{})
	if string(pub.Stdout) == "FAIL" {
		t.Fatalf("expected public egress to pypi to succeed")
	}
	// Internal gateway (sandbox-proxy) should be unreachable.
	internal, _ := e.Exec(ctx, inst.ID, []string{"sh", "-c", "curl -sS -m 3 -o /dev/null -w '%{http_code}' http://172.17.0.1:3080/ || echo BLOCKED"}, execenv.ExecOptions{})
	if string(internal.Stdout) != "BLOCKED" {
		t.Fatalf("expected internal proxy to be unreachable from the build network, got %q", internal.Stdout)
	}
}
