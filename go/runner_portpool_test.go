package agentkit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/agentkittest"
	"github.com/binocarlos/badcode-agent-orange/artifacts"
	"github.com/binocarlos/badcode-agent-orange/events"
	"github.com/binocarlos/badcode-agent-orange/execenv"
	"github.com/binocarlos/badcode-agent-orange/imageregistry"
)

// saturatedEnv is a MockExecutionEnvironment whose provisioning resource (in
// production: the DinD host-port pool) is fully leased.
type saturatedEnv struct {
	*execenv.MockExecutionEnvironment
	err error
}

func (e *saturatedEnv) Provision(context.Context, execenv.ProvisionSpec) (*execenv.Instance, error) {
	return nil, e.err
}

// Capacity is the optional execenv.CapacityReporter seam: it answers "could this
// environment provision anything at all right now?".
func (e *saturatedEnv) Capacity() error { return e.err }

func newSaturatedRunner(t *testing.T, env execenv.ExecutionEnvironment) (*runnerImpl, *agentkittest.MemStore) {
	t.Helper()
	store := agentkittest.NewMemStore()
	runner, err := NewRunner(Deps{
		Env:       env,
		Registry:  imageregistry.NewMock(),
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

// TestSaturatedHostIsNotReportedAsALostSession is the bug: when the host's port
// pool is full, the create fails for a reason that is entirely about the HOST,
// but the next thing the operator touches says the SESSION is lost and invites
// them to re-create it — which fails identically, for ever.
func TestSaturatedHostIsNotReportedAsALostSession(t *testing.T) {
	ctx := context.Background()
	// Shaped exactly like the DinD adapter's: sentinel first, pool detail after.
	poolErr := fmt.Errorf("dind provision: %w: the host port pool is exhausted — all 100 ports "+
		"in 30001-30100 are leased to live sessions", execenv.ErrNoCapacity)
	env := &saturatedEnv{MockExecutionEnvironment: execenv.NewMock(), err: poolErr}
	r, store := newSaturatedRunner(t, env)
	store.Seed(&agentdb.Session{ID: "s-101", Customer: "acme", Job: "j1"})

	_, err := r.CreateSession(ctx, CreateSessionRequest{SessionID: "s-101", Customer: "acme", Job: "j1"})
	if err == nil {
		t.Fatalf("create must fail on a saturated host")
	}
	t.Logf("create error: %v", err)

	var buf bytes.Buffer
	err = r.SendMessage(ctx, SessionRef{SessionID: "s-101"},
		SendMessageRequest{Content: "hello", Customer: "acme", Job: "j1"}, &buf)
	if err == nil {
		t.Fatalf("expected an error from a session that never provisioned")
	}
	t.Logf("operator sees: %v", err)
	if strings.Contains(err.Error(), "must be re-created") {
		t.Errorf("a saturated host is reported as a lost session: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "port pool") {
		t.Errorf("the real cause did not survive to the operator: %q", err.Error())
	}
	// The cause must survive as a TYPE too, so a host can branch on it (503 vs
	// 404) without string-matching.
	if !errors.Is(err, execenv.ErrNoCapacity) {
		t.Errorf("errors.Is(err, execenv.ErrNoCapacity) = false: %q", err.Error())
	}
}

// TestLostSessionIsStillReportedAsLost is the other half: when the host has room
// and the session genuinely has neither instance nor snapshot, the message must
// NOT change — "re-create it" is correct advice there.
func TestLostSessionIsStillReportedAsLost(t *testing.T) {
	ctx := context.Background()
	r, store := newSaturatedRunner(t, execenv.NewMock())
	store.Seed(&agentdb.Session{ID: "s-lost", Customer: "acme", Job: "j1"})

	var buf bytes.Buffer
	err := r.SendMessage(ctx, SessionRef{SessionID: "s-lost"},
		SendMessageRequest{Content: "hello", Customer: "acme", Job: "j1"}, &buf)
	if err == nil || !strings.Contains(err.Error(), "must be re-created") {
		t.Fatalf("a genuinely lost session must still say so, got %v", err)
	}
	if errors.Is(err, execenv.ErrNoCapacity) {
		t.Errorf("a lost session must not be reported as a capacity problem: %q", err.Error())
	}
}
