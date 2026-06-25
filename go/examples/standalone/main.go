// Command standalone — THE reference host for agentkit.
//
// This is the minimal Go main that a new adopter copies when they want to run
// the agentkit library against a real DinD daemon.  It exercises the COMPLETE
// stack: two concurrent sessions, real container provisioning, the
// ClaudeAgentSDK harness, and SSE event streaming — with NO Platinum-specific
// code.
//
// # Quick start
//
//  1. Build the sandbox image:
//
//	   docker build -t agentkit-sandbox:dev agent-library/sandbox
//
//  2. Load it into the DinD daemon (if running inside DinD rather than host Docker):
//
//	   docker save agentkit-sandbox:dev | docker -H tcp://localhost:2375 load
//
//  3. Start the mock model proxy (no real API key required):
//
//	   go run ./examples/mockproxy
//
//  4. Run this binary:
//
//	   DOCKER_HOST=tcp://localhost:2375 \
//	   BASE_IMAGE=agentkit-sandbox:dev \
//	   ANTHROPIC_BASE_URL=http://172.17.0.1:4000 \
//	   go run ./examples/standalone
//
// Expected output: two turns complete in parallel, SSE events printed to stdout,
// and a final confirmation line.
//
// See agent-library/examples/README.md for the full step-by-step walkthrough.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	agentkit "github.com/bayes-price/agentkit"
	"github.com/bayes-price/agentkit/agentdb"
	"github.com/bayes-price/agentkit/agentkittest"
	"github.com/bayes-price/agentkit/artifacts"
	dockerdind "github.com/bayes-price/agentkit/execenv/docker"
	"github.com/bayes-price/agentkit/fleet"
	"github.com/bayes-price/agentkit/imageregistry"
)

// envOrDefault returns the value of the environment variable key, or def if unset.
func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	// ── Flags / env ─────────────────────────────────────────────────────────────
	dockerHost := flag.String("docker-host", envOrDefault("DOCKER_HOST", "tcp://localhost:2375"),
		"Docker daemon address (env: DOCKER_HOST)")
	baseImage := flag.String("base-image", envOrDefault("BASE_IMAGE", "agentkit-sandbox:dev"),
		"Sandbox image tag (env: BASE_IMAGE)")
	agentPort := flag.Int("agent-port", 3010, "In-image agent HTTP port (default 3010)")
	flag.Parse()

	log.Printf("[standalone] docker-host=%s  base-image=%s  agent-port=%d",
		*dockerHost, *baseImage, *agentPort)
	log.Printf("[standalone] ANTHROPIC_BASE_URL=%s",
		envOrDefault("ANTHROPIC_BASE_URL", "(not set — SDK will use Anthropic direct)"))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// ── Build the ExecutionEnvironment (real DinD) ──────────────────────────────
	//
	// DinDConfig.DockerHost points at the DinD daemon.
	// PortRangeStart/End define the host-port pool the Runner leases so it can
	// reach each per-session container at http://localhost:<port>.
	// GatewayIP is 172.17.0.1 — the default Docker bridge gateway — so that
	// processes INSIDE the container can reach services on the Docker host
	// (e.g. the mock proxy listening on the host).
	dindEnv, err := dockerdind.NewDinD(dockerdind.DinDConfig{
		DockerHost:     *dockerHost,
		PortRangeStart: 30001,
		PortRangeEnd:   30100,
		GatewayIP:      "172.17.0.1",
	})
	if err != nil {
		log.Fatalf("[standalone] NewDinD: %v", err)
	}

	// ── In-memory host adapters ─────────────────────────────────────────────────
	//
	// The standalone demo uses in-memory mocks for all host services.
	// A production host replaces each with:
	//   store    → adapter over store.Store (Postgres)
	//   registry → imageregistry/blobarchive or imageregistry/ociregistry
	//   arts     → artifacts.NewBlobStore(azureClient, ...)
	//   claims   → a real HS256 JWT issuer
	store := agentkittest.NewMemStore()
	arts := artifacts.NewMock()
	claims := agentkittest.StaticClaims{Token: "dev-token"}

	// Use the in-memory mock registry: EnsurePresent is a no-op (the image is
	// already present in the daemon), and Persist/Materialize round-trip handles
	// in memory.  Swap for imageregistry.NewBlobArchive(...) to persist snapshots.
	reg := imageregistry.NewMock()

	// ── Build the Fleet ─────────────────────────────────────────────────────────
	//
	// A Fleet wraps one or more workers (compute units). Here we have a single
	// DinD daemon as one worker.  The Fleet layer provides sticky placement and
	// is the knob for horizontal scaling: add more workers and the Runner
	// distributes sessions across them automatically.
	f := fleet.NewMemory(store, &fleet.MemFleetOptions{
		// TrustedWorkload=true permits TierContainer (Docker) workers in dev.
		// Production sets this only for internal/trusted workloads; untrusted
		// code requires TierVM or higher.
		TrustedWorkload: true,
	})
	w := &fleet.Worker{
		ID:   "dind-1",
		Env:  dindEnv,
		Caps: dindEnv.Capabilities(),
	}
	if err := f.Register(ctx, w); err != nil {
		log.Fatalf("[standalone] fleet.Register: %v", err)
	}

	// ── Construct the Runner ────────────────────────────────────────────────────
	//
	// The Runner is the single object your HTTP handlers call.  It owns the full
	// session lifecycle: Provision, SendMessage, Stream, Stop, Resume,
	// Destroy, Snapshot, BuildUserImage.
	runner, err := agentkit.NewRunner(agentkit.Deps{
		Fleet:     f,
		Registry:  reg,
		Store:     store,
		Artifacts: arts,
		Claims:    claims,
		Policy: agentkit.Policy{
			BaseImage: *baseImage,
			AgentPort: *agentPort,
			// ArchiveTimeout left at zero → idle archive loop disabled for demo.
		},
	})
	if err != nil {
		log.Fatalf("[standalone] NewRunner: %v", err)
	}

	// Start background control loops and recover any surviving containers from a
	// prior run.
	if err := runner.Start(ctx); err != nil {
		log.Fatalf("[standalone] runner.Start: %v", err)
	}
	defer runner.Close() //nolint:errcheck

	// ── Seed the session store ──────────────────────────────────────────────────
	//
	// In production the host handler persists the session row BEFORE calling
	// CreateSession and deletes it if CreateSession returns an error (orphan-cleanup
	// ownership — see docs/04-session-orchestration.md).
	store.Seed(&agentdb.Session{ID: "s1", Customer: "demo", Job: "demo-job", UserEmail: "dev@example.com", WorkflowID: "agent"})
	store.Seed(&agentdb.Session{ID: "s2", Customer: "demo", Job: "demo-job", UserEmail: "dev@example.com", WorkflowID: "agent"})

	// ── CreateSession for both sessions ─────────────────────────────────────────
	for _, sid := range []string{"s1", "s2"} {
		h, err := runner.CreateSession(ctx, agentkit.CreateSessionRequest{
			SessionID: sid,
			Customer:  "demo",
			Job:       "demo-job",
			UserEmail: "dev@example.com",
			// Harness selects the agentic framework inside the sandbox.
			// HarnessClaudeAgentSDK is the default; the sandbox also supports
			// HarnessClaudeCLI and HarnessGeminiCLI (future).
			Harness: agentkit.HarnessClaudeAgentSDK,
		})
		if err != nil {
			log.Fatalf("[standalone] CreateSession %s: %v", sid, err)
		}
		log.Printf("[standalone] session %s created  addr=%s  state=%s",
			h.SessionID, h.Address, h.State)
	}

	// ── SendMessage to both sessions CONCURRENTLY ────────────────────────────────
	//
	// The Runner's internal SSE relay handles each session in its own goroutine
	// with an independent event buffer — session s1 and s2 streams never merge.
	type result struct {
		sessionID string
		output    string
		err       error
	}
	ch := make(chan result, 2)

	for _, sid := range []string{"s1", "s2"} {
		sid := sid // capture loop variable
		go func() {
			var buf bytes.Buffer
			sendErr := runner.SendMessage(ctx,
				agentkit.SessionRef{SessionID: sid},
				agentkit.SendMessageRequest{
					Content:  fmt.Sprintf("Hello from session %s — please reply with just your session ID.", sid),
					Customer: "demo",
					Job:      "demo-job",
				},
				&buf,
			)
			ch <- result{sessionID: sid, output: buf.String(), err: sendErr}
		}()
	}

	// Collect results from both goroutines.
	results := make(map[string]result, 2)
	for range []string{"s1", "s2"} {
		r := <-ch
		results[r.sessionID] = r
	}

	// ── Report and verify ────────────────────────────────────────────────────────
	allOK := true
	for _, sid := range []string{"s1", "s2"} {
		r := results[sid]
		if r.err != nil {
			log.Printf("[standalone] session %s ERROR: %v", sid, r.err)
			allOK = false
			continue
		}
		log.Printf("[standalone] session %s COMPLETE  SSE bytes=%d", sid, len(r.output))
		// Print the first 300 bytes so the operator can see streamed content.
		preview := r.output
		if len(preview) > 300 {
			preview = preview[:300] + "..."
		}
		fmt.Printf("\n--- SSE from %s ---\n%s\n", sid, preview)
	}

	// Cross-session isolation check: if both turns received SSE, verify they are
	// NOT byte-for-byte identical (a routing bug would cause this).
	s1 := results["s1"]
	s2 := results["s2"]
	if s1.err == nil && s2.err == nil && len(s1.output) > 0 && len(s2.output) > 0 {
		if s1.output == s2.output {
			log.Printf("[standalone] WARNING: s1 and s2 produced identical SSE — possible routing bug; check mockproxy x-session-id logs")
		} else {
			log.Printf("[standalone] session isolation OK: s1 and s2 streams differ as expected")
		}
	}

	fmt.Println()
	if allOK {
		fmt.Println("BOTH sessions completed — check mockproxy logs for x-session-id headers to confirm no cross-session contamination")
	} else {
		fmt.Println("One or more sessions failed — see errors above")
		os.Exit(1)
	}
}
