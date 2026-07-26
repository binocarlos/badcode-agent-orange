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

// newFailingImageRunner builds a Runner whose registry cannot pull anything, so
// a create fails for a reason that is entirely about the session's own
// configuration — the mis-typed `base_image` case, not a host capacity case.
func newFailingImageRunner(t *testing.T, pullErr error, env execenv.ExecutionEnvironment) (*runnerImpl, *agentkittest.MemStore) {
	t.Helper()
	reg := imageregistry.NewMock()
	reg.Err = pullErr
	store := agentkittest.NewMemStore()
	runner, err := NewRunner(Deps{
		Env: env, Registry: reg, Store: store, Artifacts: artifacts.NewMock(),
		Claims:         agentkittest.StaticClaims{Token: "test-token"},
		Events:         events.NewPipeline(events.NewMockSink()),
		SessionContext: staticSessionContext{sc: projectBase("definitely-not-an-image:v9")},
		Images:         newCatalogueStub(),
		Policy:         Policy{BaseImage: "agentkit-sandbox:test"},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return runner.(*runnerImpl), store
}

// TestCreateFailureReasonReachesTheCaller is the defect: a create that fails for
// a NON-capacity reason (a base_image naming an image that does not exist)
// throws the reason away, and the caller's next message is told the session is
// lost and should be re-created — which is both false and useless advice.
//
// The rich diagnostic the §13 pointer fix wrote exists; it just reached nobody.
// This asserts the same property e2e/features/image-curation.stack.spec.ts
// asserts over HTTP: the caller is told the setting, its value, the project and
// which of the two interpretations the string was given.
func TestCreateFailureReasonReachesTheCaller(t *testing.T) {
	ctx := context.Background()
	const pullFailure = "Error response from daemon: pull access denied"
	r, store := newFailingImageRunner(t, errors.New(pullFailure), execenv.NewMock())
	store.Seed(&agentdb.Session{ID: "s-badimage", Customer: "acme", Job: "j1"})

	_, createErr := r.CreateSession(ctx, CreateSessionRequest{
		SessionID: "s-badimage", Customer: "acme", Job: "j1",
	})
	if createErr == nil {
		t.Fatalf("create must fail when the configured image cannot be pulled")
	}
	t.Logf("create error (which today only the goroutine that discarded it ever saw): %v", createErr)

	var buf bytes.Buffer
	err := r.SendMessage(ctx, SessionRef{SessionID: "s-badimage"},
		SendMessageRequest{Content: "hello", Customer: "acme", Job: "j1"}, &buf)
	if err == nil {
		t.Fatalf("expected an error from a session that never provisioned")
	}
	t.Logf("operator sees: %v", err)

	for _, want := range []string{
		"project_settings.base_image",
		`"definitely-not-an-image:v9"`,
		`project "acme"`,
		"used as a literal registry reference",
		pullFailure,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the real cause did not survive to the caller: %q missing from %q", want, err.Error())
		}
	}
	// It is not a lost session, so it must not invite the one action that
	// cannot help — re-creating it unchanged.
	if strings.Contains(err.Error(), "has no running instance and no snapshot") {
		t.Errorf("a configuration failure is reported as a lost session: %q", err.Error())
	}
}

// TestStoredReasonIsSurfacedByStatusAndStore pins that the reason is DURABLE:
// it lands on the session row, so a process restart — or any other reader, such
// as the API's GET /agent/session/{id} — can still say what went wrong.
func TestStoredReasonIsSurfacedByStatusAndStore(t *testing.T) {
	ctx := context.Background()
	r, store := newFailingImageRunner(t, errors.New("pull access denied"), execenv.NewMock())
	store.Seed(&agentdb.Session{ID: "s-row", Customer: "acme"})

	if _, err := r.CreateSession(ctx, CreateSessionRequest{SessionID: "s-row", Customer: "acme"}); err == nil {
		t.Fatalf("create must fail")
	}
	sess, err := store.GetSession(ctx, "s-row")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if !strings.Contains(sess.CreateError, "project_settings.base_image") {
		t.Errorf("session row did not record why the create failed: %q", sess.CreateError)
	}
}

// TestSuccessfulCreateClearsTheStoredReason: a reason that outlived its cause
// is worse than no reason. A create that succeeds wipes the previous failure.
func TestSuccessfulCreateClearsTheStoredReason(t *testing.T) {
	ctx := context.Background()
	store := agentkittest.NewMemStore()
	runner, err := NewRunner(Deps{
		Env: execenv.NewMock(), Registry: imageregistry.NewMock(), Store: store,
		Artifacts: artifacts.NewMock(),
		Claims:    agentkittest.StaticClaims{Token: "test-token"},
		Events:    events.NewPipeline(events.NewMockSink()),
		Policy:    Policy{BaseImage: "agentkit-sandbox:test"},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	store.Seed(&agentdb.Session{
		ID: "s-retry", Customer: "acme",
		CreateError: "project_settings.base_image = \"old\" ... which failed: nope",
	})
	if _, err := runner.CreateSession(ctx, CreateSessionRequest{SessionID: "s-retry", Customer: "acme"}); err != nil {
		t.Fatalf("create must succeed: %v", err)
	}
	sess, err := store.GetSession(ctx, "s-retry")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.CreateError != "" {
		t.Errorf("a successful create must clear the stale reason, got %q", sess.CreateError)
	}
}

// saturatedFailingImageEnv is a host with no capacity left AND a session whose
// configuration is independently broken.
type saturatedFailingImageEnv struct {
	*execenv.MockExecutionEnvironment
	err error
}

func (e *saturatedFailingImageEnv) Provision(context.Context, execenv.ProvisionSpec) (*execenv.Instance, error) {
	return nil, e.err
}
func (e *saturatedFailingImageEnv) Capacity() error { return e.err }

// TestLiveCapacityWinsOverAStoredReason: the port-pool fix asks the environment
// LIVE precisely because a stored error goes stale. So when both speak, the live
// answer wins — and it wins as a TYPE, which a stored string could never carry.
func TestLiveCapacityWinsOverAStoredReason(t *testing.T) {
	ctx := context.Background()
	poolErr := fmt.Errorf("dind provision: %w: the host port pool is exhausted", execenv.ErrNoCapacity)
	env := &saturatedFailingImageEnv{MockExecutionEnvironment: execenv.NewMock(), err: poolErr}
	r, store := newFailingImageRunner(t, errors.New("pull access denied"), env)
	store.Seed(&agentdb.Session{
		ID:          "s-both",
		Customer:    "acme",
		CreateError: `project_settings.base_image = "definitely-not-an-image:v9" (project "acme") failed`,
	})

	var buf bytes.Buffer
	err := r.SendMessage(ctx, SessionRef{SessionID: "s-both"},
		SendMessageRequest{Content: "hello", Customer: "acme"}, &buf)
	if err == nil {
		t.Fatalf("expected an error")
	}
	t.Logf("operator sees: %v", err)
	if !errors.Is(err, execenv.ErrNoCapacity) {
		t.Errorf("the live capacity answer must win, and win as a type: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "port pool") {
		t.Errorf("the live cause did not survive: %q", err.Error())
	}
}

// TestCapacityFailureDoesNotOverwriteAConfigReason: a capacity failure is a fact
// about the HOST at one moment, not about this session, so recording it would
// plant a reason guaranteed to go stale. The permanent configuration fact
// already on the row must survive it.
func TestCapacityFailureDoesNotOverwriteAConfigReason(t *testing.T) {
	ctx := context.Background()
	poolErr := fmt.Errorf("dind provision: %w: the host port pool is exhausted", execenv.ErrNoCapacity)
	env := &saturatedFailingImageEnv{MockExecutionEnvironment: execenv.NewMock(), err: poolErr}
	store := agentkittest.NewMemStore()
	runner, err := NewRunner(Deps{
		Env: env, Registry: imageregistry.NewMock(), Store: store, Artifacts: artifacts.NewMock(),
		Claims: agentkittest.StaticClaims{Token: "test-token"},
		Events: events.NewPipeline(events.NewMockSink()),
		Policy: Policy{BaseImage: "agentkit-sandbox:test"},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	const configReason = `project_settings.base_image = "definitely-not-an-image:v9" (project "acme") failed`
	store.Seed(&agentdb.Session{ID: "s-keep", Customer: "acme", CreateError: configReason})

	if _, err := runner.CreateSession(ctx, CreateSessionRequest{SessionID: "s-keep", Customer: "acme"}); err == nil {
		t.Fatalf("create must fail on a saturated host")
	}
	sess, err := store.GetSession(ctx, "s-keep")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.CreateError != configReason {
		t.Errorf("a transient capacity failure overwrote the durable configuration reason: %q", sess.CreateError)
	}
}
