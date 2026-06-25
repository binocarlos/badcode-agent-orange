package agentkit

import (
	"testing"

	"github.com/bayes-price/agentkit/agentkittest"
	"github.com/bayes-price/agentkit/artifacts"
	"github.com/bayes-price/agentkit/events"
	"github.com/bayes-price/agentkit/execenv"
	"github.com/bayes-price/agentkit/imageregistry"
)

// minimalDeps returns a Deps with the given environment and policy, using
// in-memory mocks for all other required dependencies — matching the pattern
// in runner_test.go (newTestRunner).
func minimalDeps(env execenv.ExecutionEnvironment, policy Policy) Deps {
	store := agentkittest.NewMemStore()
	return Deps{
		Env:       env,
		Registry:  imageregistry.NewMock(),
		Store:     store,
		Artifacts: artifacts.NewMock(),
		Claims:    agentkittest.StaticClaims{Token: "test-token"},
		Events:    events.NewPipeline(events.NewMockSink()),
		Policy:    policy,
	}
}

// mockWithCaps returns a MockExecutionEnvironment whose Capabilities() returns
// the given caps value.
func mockWithCaps(caps execenv.Capabilities) *execenv.MockExecutionEnvironment {
	m := execenv.NewMock()
	m.Caps = &caps
	return m
}

// TestTrustGate_SharedUntrustedContainer asserts that shared tenancy at
// TierContainer without TrustedWorkload is rejected at NewRunner construction.
func TestTrustGate_SharedUntrustedContainer(t *testing.T) {
	env := mockWithCaps(execenv.Capabilities{
		Tenancy:       execenv.TenancyShared,
		IsolationTier: execenv.TierContainer,
		Backend:       execenv.BackendDockerDinD,
	})
	_, err := NewRunner(minimalDeps(env, Policy{BaseImage: "test"}))
	if err == nil {
		t.Fatal("expected NewRunner to return an error for shared+container+untrusted, got nil")
	}
}

// TestTrustGate_SharedTrustedContainer asserts that shared tenancy at TierContainer
// is allowed when TrustedWorkload is true.
func TestTrustGate_SharedTrustedContainer(t *testing.T) {
	env := mockWithCaps(execenv.Capabilities{
		Tenancy:       execenv.TenancyShared,
		IsolationTier: execenv.TierContainer,
		Backend:       execenv.BackendDockerDinD,
	})
	_, err := NewRunner(minimalDeps(env, Policy{BaseImage: "test", TrustedWorkload: true}))
	if err != nil {
		t.Fatalf("expected NewRunner to succeed for shared+container+trusted, got: %v", err)
	}
}

// TestTrustGate_SharedUntrustedVM asserts that shared tenancy at TierVM is
// allowed even without TrustedWorkload, because the isolation boundary is sufficient.
func TestTrustGate_SharedUntrustedVM(t *testing.T) {
	env := mockWithCaps(execenv.Capabilities{
		Tenancy:       execenv.TenancyShared,
		IsolationTier: execenv.TierVM,
		Backend:       execenv.BackendDockerDinD,
	})
	_, err := NewRunner(minimalDeps(env, Policy{BaseImage: "test", TrustedWorkload: false}))
	if err != nil {
		t.Fatalf("expected NewRunner to succeed for shared+VM+untrusted, got: %v", err)
	}
}

// TestTrustGate_PerSessionUntrusted asserts that per-session tenancy is always
// allowed regardless of TrustedWorkload (the existing default case).
func TestTrustGate_PerSessionUntrusted(t *testing.T) {
	env := mockWithCaps(execenv.Capabilities{
		Tenancy:            execenv.TenancyPerSession,
		IsolationTier:      execenv.TierContainer,
		Backend:            execenv.BackendDockerDinD,
		IsolatedPerSession: true,
	})
	_, err := NewRunner(minimalDeps(env, Policy{BaseImage: "test", TrustedWorkload: false}))
	if err != nil {
		t.Fatalf("expected NewRunner to succeed for per-session+untrusted, got: %v", err)
	}
}
