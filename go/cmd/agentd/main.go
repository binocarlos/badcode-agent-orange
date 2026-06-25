// Command agentd — standalone agentkit host: Runner + httpapi + /health + /agent-proxy.
//
// agentd is the pre-built host for the standalone stack (docker-compose). Use it
// when you want a running agent API without writing a host. It uses the reference
// adapters (sqlitestore, devclaims) and a real DinD execution environment; the
// blob and image-registry backends are selected from env (filesystem +
// blob-archive by default, or GCS + Artifact Registry — see backends.go).
//
// # Quick start
//
//  1. Build the sandbox image and load it into DinD:
//
//     docker build -t agentkit-sandbox:dev agent-library/sandbox
//     docker save agentkit-sandbox:dev | docker -H tcp://localhost:2375 load
//
//  2. Run agentd (mock model proxy built-in when ANTHROPIC_API_KEY is unset):
//
//     DOCKER_HOST=tcp://localhost:2375 \
//     AGENTKIT_IMAGE=agentkit-sandbox:dev \
//     go run ./cmd/agentd
//
// The server listens on :8099 by default (ADDR env to override).
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"

	agentkit "github.com/binocarlos/badcode-agent-orange"
	dockerdind "github.com/binocarlos/badcode-agent-orange/execenv/docker"
	"github.com/binocarlos/badcode-agent-orange/extension"
	"github.com/binocarlos/badcode-agent-orange/extension/blobartifacts"
	"github.com/binocarlos/badcode-agent-orange/extension/devclaims"
	"github.com/binocarlos/badcode-agent-orange/extension/sqlitestore"
	"github.com/binocarlos/badcode-agent-orange/fleet"
	"github.com/binocarlos/badcode-agent-orange/httpapi"
)

func main() {
	ctx := context.Background()

	// ── Data directory ───────────────────────────────────────────────────────────
	dataDir := envOr("AGENTKIT_DATA", "./.agentkit-data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Fatalf("agentkit-server: mkdir %s: %v", dataDir, err)
	}

	// ── Session store (SQLite) ───────────────────────────────────────────────────
	dbPath := filepath.Join(dataDir, "sessions.db")
	store, err := sqlitestore.Open(dbPath)
	must(err)

	// ── Blob backend (shared by registry + artifact store) ───────────────────────
	// fs (default) or gcs — see backends.go. One BlobStore serves the artifact
	// bytes and (for the blob-archive registry) snapshot tarballs.
	blobCfg, err := resolveBlobConfig(os.Getenv, dataDir)
	must(err)
	blobs, closeBlobs, err := newBlobs(ctx, blobCfg)
	must(err)
	defer closeBlobs() //nolint:errcheck
	artStore := blobartifacts.New(blobs)

	// ── Claims issuer ────────────────────────────────────────────────────────────
	jwtSecret := []byte(os.Getenv("AGENTKIT_JWT_SECRET")) // empty → dev-open
	claims := devclaims.New([]byte(envOr("AGENTKIT_JWT_SECRET", "dev-secret")))

	// ── Docker host (shared by DinD + blobarchive) ───────────────────────────────
	dockerHost := envOr("DOCKER_HOST", "tcp://localhost:2375")

	// ── Image registry (blob-archive default, or ociregistry → Artifact Registry) ─
	regCfg, err := resolveRegistryConfig(os.Getenv)
	must(err)
	registry, err := newRegistry(ctx, dockerHost, blobs, regCfg)
	must(err)
	log.Printf("[agentd] blobs=%s registry=%s", blobCfg.backend, regCfg.backend)

	// ── DinD execution environment ───────────────────────────────────────────────
	dindEnv, err := dockerdind.NewDinD(dockerdind.DinDConfig{
		DockerHost:     dockerHost,
		PortRangeStart: 30001,
		PortRangeEnd:   30100,
		GatewayIP:      "172.17.0.1",
	})
	must(err)

	// ── Fleet (one-worker in-memory) ─────────────────────────────────────────────
	f := fleet.NewMemory(store, &fleet.MemFleetOptions{TrustedWorkload: true})
	err = f.Register(context.Background(), &fleet.Worker{
		ID:   "w1",
		Env:  dindEnv,
		Caps: dindEnv.Capabilities(),
	})
	must(err)

	// ── Session env (model-provider config the in-image agent requires) ──────────
	// selfURL is how a session container (nested in DinD) reaches agentd. With
	// agentd sharing DinD's network namespace, that is the bridge gateway IP.
	selfURL := envOr("AGENTKIT_SELF_URL", "http://172.17.0.1:8099")
	sessionEnv := sandboxSessionEnv(selfURL)

	// ── Runner ───────────────────────────────────────────────────────────────────
	runner, err := agentkit.NewRunner(agentkit.Deps{
		Fleet:     f,
		Registry:  registry,
		Store:     store,
		Artifacts: artStore,
		Claims:    claims,
		Policy: agentkit.Policy{
			BaseImage:  envOr("AGENTKIT_IMAGE", "agentkit-example:dev"),
			AgentPort:  3010,
			SessionEnv: sessionEnv,
		},
	})
	must(err)
	must(runner.Start(context.Background()))
	defer runner.Close() //nolint:errcheck

	// ── HTTP API ─────────────────────────────────────────────────────────────────
	api, err := httpapi.New(httpapi.Config{
		Runner:    runner,
		Store:     store,
		Artifacts: artStore,
		Identity:  identityFromRequest,
	})
	must(err)

	// API mux (authenticated) + an outer root mux for unauthenticated routes.
	apiMux := api.Mux()

	root := http.NewServeMux()
	root.HandleFunc("/health", healthHandler)
	// /dev/token (DEV ONLY): issues a short-lived JWT for the bundled UI. Gated by
	// the shared secret only in that it signs with AGENTKIT_JWT_SECRET.
	root.HandleFunc("/dev/token", func(w http.ResponseWriter, r *http.Request) {
		scope := extension.ContextScope{
			UserEmail: "demo@example.com",
			Customer:  "demo",
			Job:       "demo-job",
		}
		tok, err := claims.Issue(r.Context(), scope, "")
		if err != nil {
			http.Error(w, "token generation failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"token": tok})
	})
	root.Handle("/agent-proxy/", http.StripPrefix("/agent-proxy", newModelProxyHandler()))
	// Everything else goes through auth.
	root.Handle("/", jwtAuthMiddleware(jwtSecret, apiMux))

	// ── Serve ────────────────────────────────────────────────────────────────────
	addr := envOr("ADDR", ":8099")
	log.Printf("[agentd] listening on %s  image=%s  docker=%s",
		addr, envOr("AGENTKIT_IMAGE", "agentkit-example:dev"), dockerHost)
	must(http.ListenAndServe(addr, root))
}

// healthHandler is the unauthenticated liveness probe used by the compose
// healthcheck and the e2e harness.
func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
