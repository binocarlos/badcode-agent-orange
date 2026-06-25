//go:build integration

package systemtest

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	agentkit "github.com/bayes-price/agentkit"
	"github.com/bayes-price/agentkit/agentkittest"
	"github.com/bayes-price/agentkit/artifacts"
	"github.com/bayes-price/agentkit/execenv"
	"github.com/bayes-price/agentkit/fleet"
	"github.com/bayes-price/agentkit/imageregistry"
	"github.com/bayes-price/agentkit/imageregistry/ociregistry"
)

const sandboxImage = "agentkit-sandbox:systemtest"

type systemRig struct {
	runner agentkit.Runner
	env    *TestEnv
	store  *agentkittest.MemStore
	arts   *artifacts.MockArtifactStore
}

func newSystemRig(mockProxyURL string) (*systemRig, error) {
	return newSystemRigWithRegistry(mockProxyURL, imageregistry.NewMock())
}

// ociRegistryURL is the registry prefix for the real-registry trust test.
// Defaults to the local registry:2 from agent-library/docker-compose.test.yml.
func ociRegistryURL() string {
	if v := os.Getenv("OCIREGISTRY_URL"); v != "" {
		return v
	}
	return "localhost:5001/agentkit"
}

// newSystemRigOCI builds a rig backed by the REAL ociregistry adapter pushing to a
// local registry:2 (default localhost:5001), exercising the genuine commit→push→pull
// round-trip rather than the in-memory mock registry.
func newSystemRigOCI(mockProxyURL string) (*systemRig, error) {
	reg, err := ociregistry.New(ociregistry.Config{
		DockerHost: os.Getenv("DOCKER_HOST"), // "" → FromEnv: the same daemon TestEnv uses
		Registry:   ociRegistryURL(),
	})
	if err != nil {
		return nil, fmt.Errorf("ociregistry: %w", err)
	}
	return newSystemRigWithRegistry(mockProxyURL, reg)
}

func newSystemRigWithRegistry(mockProxyURL string, reg imageregistry.ImageRegistry) (*systemRig, error) {
	env, err := NewTestEnv(mockProxyURL)
	if err != nil {
		return nil, fmt.Errorf("create test env: %w", err)
	}

	store := agentkittest.NewMemStore()
	arts := artifacts.NewMock()
	claims := agentkittest.StaticClaims{Token: "dev-token"}

	f := fleet.NewMemory(store, &fleet.MemFleetOptions{TrustedWorkload: true})
	w := &fleet.Worker{
		ID:   "testenv-1",
		Env:  env,
		Caps: env.Capabilities(),
	}
	if err := f.Register(context.Background(), w); err != nil {
		return nil, fmt.Errorf("fleet register: %w", err)
	}

	// Events left nil → the Runner builds a Store-backed sink, so query events
	// persist into the MemStore exactly as in production (and restore can replay
	// them via /load-conversation). This is what makes the conversation-continuity
	// invariant meaningful.
	runner, err := agentkit.NewRunner(agentkit.Deps{
		Fleet:      f,
		Registry:   reg,
		Store:      store,
		Artifacts:  arts,
		Claims:     claims,
		HTTPClient: &http.Client{Timeout: 120 * time.Second},
		Policy: agentkit.Policy{
			BaseImage: sandboxImage,
			AgentPort: 0,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("new runner: %w", err)
	}

	return &systemRig{
		runner: runner,
		env:    env,
		store:  store,
		arts:   arts,
	}, nil
}

func (r *systemRig) cleanup() {
	r.env.DestroyAll(context.Background())
}

func getFreePort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic("getFreePort: " + err.Error())
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

func waitForHealthy(ctx context.Context, address string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return false
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, address+"/health", nil)
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				return true
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

func readAll(r io.Reader) ([]byte, error) {
	return io.ReadAll(r)
}

// registryReachable reports whether the registry behind registryURL answers its
// /v2/ API root. registryURL is a prefix like "localhost:5001/agentkit"; the host
// is the part before the first slash.
func registryReachable(registryURL string) bool {
	host := registryURL
	if i := strings.IndexByte(host, '/'); i >= 0 {
		host = host[:i]
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + host + "/v2/")
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return true
}

// countQueryEvents returns the number of persisted query events for a session.
func countQueryEvents(t *testing.T, rig *systemRig, sessionID string) int {
	t.Helper()
	evs, err := rig.store.ListQueryEventsFlat(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("ListQueryEventsFlat: %v", err)
	}
	return len(evs)
}

// mustInstance returns the live InstanceID for a session or fails the test.
func mustInstance(t *testing.T, rig *systemRig, sessionID string) execenv.InstanceID {
	t.Helper()
	id, ok := rig.env.InstanceForSession(sessionID)
	if !ok {
		t.Fatalf("no live instance for session %q", sessionID)
	}
	return id
}
