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
	"strings"
	"time"

	agentkit "github.com/binocarlos/badcode-agent-orange"
	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/artifacts"
	dockerdind "github.com/binocarlos/badcode-agent-orange/execenv/docker"
	"github.com/binocarlos/badcode-agent-orange/extension"
	"github.com/binocarlos/badcode-agent-orange/extension/blobartifacts"
	"github.com/binocarlos/badcode-agent-orange/extension/dbartifacts"
	"github.com/binocarlos/badcode-agent-orange/extension/devclaims"
	"github.com/binocarlos/badcode-agent-orange/extension/embedding"
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

	// ── Session store ────────────────────────────────────────────────────────────
	// DATABASE_URL set → Postgres (agentdb.Store, self-migrating): one store for
	// the Runner AND the rich httpapi read paths (session listing, message
	// search, replayable query events). Unset → the legacy local SQLite store.
	var store agentkit.RunnerStore
	var agentDB *agentdb.Store
	if dbURL := os.Getenv("DATABASE_URL"); dbURL != "" {
		pg, err := agentdb.Open(dbURL)
		must(err)
		agentDB = pg
		store = pg
		log.Printf("[agentd] store=postgres")
	} else {
		dbPath := filepath.Join(dataDir, "sessions.db")
		s, err := sqlitestore.Open(dbPath)
		must(err)
		store = s
		log.Printf("[agentd] store=sqlite %s", dbPath)
	}

	// ── Blob backend (shared by registry + artifact store) ───────────────────────
	// fs (default) or gcs — see backends.go. One BlobStore serves the artifact
	// bytes and (for the blob-archive registry) snapshot tarballs.
	blobCfg, err := resolveBlobConfig(os.Getenv, dataDir)
	must(err)

	// ── Embedding provider (memory semantic leg, §7.5) ───────────────────────────
	// AGENTKIT_EMBEDDING_BACKEND: none (default) | mock. A nil provider is a
	// supported deployment, not a failure — memories store a NULL embedding and
	// search degrades to keyword+recency with the same result shape (§7.6.5). A
	// typo in the variable IS a failure, so it is a boot error rather than a
	// silent fall back to "none".
	embedder, err := embedding.NewFromEnv(os.Getenv)
	must(err)
	if embedder == nil {
		log.Printf("[agentd] embeddings=none — memory search is keyword+recency")
	} else {
		log.Printf("[agentd] embeddings=%s", envOr("AGENTKIT_EMBEDDING_BACKEND", "none"))
	}
	blobs, closeBlobs, err := newBlobs(ctx, blobCfg)
	must(err)
	defer closeBlobs() //nolint:errcheck

	// ── Artifact store ───────────────────────────────────────────────────────────
	// Bytes always go to the BlobStore. The METADATA index is durable only on
	// Postgres (extension/dbartifacts → `agent_artifacts`): restart agentd and
	// the rows are still there.
	//
	// On the sqlite fallback there is no such table, so the index stays the
	// in-process map (extension/blobartifacts) it has always been — artifacts
	// written by this process are listable while it lives and are lost, bytes
	// orphaned in the bucket, when it exits. That is the pre-existing behaviour,
	// kept deliberately rather than failing to boot, but it is a real data-loss
	// mode: it is logged loudly here because nothing later in the run will
	// mention it. Compose always sets DATABASE_URL.
	var artStore artifacts.ArtifactStore
	if agentDB != nil {
		artStore = dbartifacts.New(agentDB, blobs)
		log.Printf("[agentd] artifacts=postgres+blob (metadata survives restart)")
	} else {
		artStore = blobartifacts.New(blobs)
		log.Printf("[agentd] artifacts=in-process index — NOT durable: " +
			"artifact metadata is lost on restart and its bytes orphaned. Set DATABASE_URL.")
	}

	// ── Claims issuer ────────────────────────────────────────────────────────────
	// Two secrets, deliberately: jwtSecret verifies API callers and may be empty
	// (dev-open, so the demo UI works with no configuration), while
	// sessionSecret is what the Runner MINTS per-session tokens with and is
	// never empty. The core MCP server verifies against sessionSecret — a
	// session's memories must not be reachable just because the API is open.
	jwtSecret := []byte(os.Getenv("AGENTKIT_JWT_SECRET")) // empty → dev-open
	sessionSecret := []byte(envOr("AGENTKIT_JWT_SECRET", "dev-secret"))
	claims := devclaims.New(sessionSecret)

	// ── Public base URL (session permalinks) ─────────────────────────────────────
	// Where a human clicking a session link lands — the web UI's externally
	// reachable origin, NOT AGENTKIT_SELF_URL (that is a DinD bridge IP only
	// containers can reach). Everything that stamps provenance mints its
	// session_url from this. See permalink.go.
	permalinks, err := resolvePublicBaseURL(os.Getenv)
	must(err)
	log.Printf("[agentd] permalinks=%s/p/<project>/s/<session>", permalinks.BaseURL())

	// ── Docker host (shared by DinD + blobarchive) ───────────────────────────────
	dockerHost := envOr("DOCKER_HOST", "tcp://localhost:2375")

	// ── Image registry (blob-archive default, or ociregistry → Artifact Registry) ─
	regCfg, err := resolveRegistryConfig(os.Getenv)
	must(err)
	registry, err := newRegistry(ctx, dockerHost, blobs, regCfg)
	must(err)
	log.Printf("[agentd] blobs=%s registry=%s", blobCfg.backend, regCfg.backend)

	// ── Session port pool ────────────────────────────────────────────────────────
	// One live session leases one host port until it is deleted, so the size of
	// this range IS the concurrent-session ceiling for the host. Default
	// 30001-30100 (what agentd hardcoded before the seam existed); a nonsense
	// range is a boot error rather than a pool that fails every session. A test
	// stack sets a pool of three to reach the exhaustion path in seconds. See
	// portrange.go.
	pool, err := resolvePortRange(os.Getenv)
	must(err)
	log.Printf("[agentd] session port pool=%s (%d concurrent sessions max on this host; one live session holds one port until it is deleted or reclaimed for idleness)",
		pool, pool.size())

	// ── DinD execution environment ───────────────────────────────────────────────
	dindEnv, err := dockerdind.NewDinD(dockerdind.DinDConfig{
		DockerHost:     dockerHost,
		PortRangeStart: pool.start,
		PortRangeEnd:   pool.end,
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
	// Model auth by key presence: ANTHROPIC_API_KEY → proxy path (wins when both
	// are set); only CLAUDE_CODE_OAUTH_TOKEN → subscription mode (sessions talk
	// to api.anthropic.com directly); neither → proxy path serving the mock.
	selfURL := envOr("AGENTKIT_SELF_URL", "http://172.17.0.1:8099")
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	oauthToken := os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")
	subscriptionMode := apiKey == "" && oauthToken != ""
	sessionEnv := sandboxSessionEnv(selfURL)
	if subscriptionMode {
		sessionEnv = subscriptionSessionEnv(selfURL, oauthToken)
		log.Printf("[agentd] subscription mode (CLAUDE_CODE_OAUTH_TOKEN) → sessions call api.anthropic.com directly")
	} else if apiKey != "" && oauthToken != "" {
		log.Printf("[agentd] both ANTHROPIC_API_KEY and CLAUDE_CODE_OAUTH_TOKEN set — API key wins (proxy mode)")
	}

	// ── MCP credential env (AGENTKIT_MCP_ENV allowlist) ──────────────────────────
	// Operator-designated variables travel agentd → session container, where the
	// sandbox resolves the ${VAR} references in MCP config (§4.4). Allowlist
	// only: agentd's own secrets (JWT, model keys) are never forwarded — and
	// naming one is a boot error. See mcpenv.go.
	mcpEnv, missingMCPEnv, err := resolveMCPEnv(os.Getenv)
	must(err)
	sessionEnv = applyMCPEnv(sessionEnv, mcpEnv)
	if len(mcpEnv) > 0 {
		log.Printf("[agentd] forwarding MCP credential env into sessions: %s", strings.Join(mcpEnvNames(mcpEnv), ","))
	}
	if len(missingMCPEnv) > 0 {
		log.Printf("[agentd] WARNING: %s names unset variable(s) %s — MCP servers referencing them will fail at spawn",
			mcpEnvVar, strings.Join(missingMCPEnv, ","))
	}

	// ── Session context (project settings + workers) ─────────────────────────────
	// The §5 defaults chain: worker beats project beats global, for base image,
	// system prompt and MCP config. Needs the product-layer tables, so it is
	// wired only on the Postgres store. See sessioncontext.go.
	var sessionCtx extension.SessionContextProvider
	baseImage := envOr("AGENTKIT_IMAGE", "agentkit-example:dev")
	if agentDB != nil {
		sessionCtx = newSessionContextProvider(agentDB, baseImage)
	}
	logSessionContextWiring(sessionCtx != nil)

	// ── Image pointer resolution (§13.3, I4) ─────────────────────────────────────
	// ONE resolver, handed to both the Runner (Deps.Images — the launch chain)
	// and the dispatcher (Images — composition step 1), so a worker job and a
	// chat with the same worker cannot launch from different environments. Like
	// everything else product-layer it needs Postgres; without it a worker's
	// `image` column is unreadable anyway, because the catalogue lives there.
	// Declared as the interface, never as the concrete type: a typed-nil
	// *catalogueImageResolver in an interface field is non-nil, and the whole
	// point of nil here is "this host has no catalogue" (see agentkit.Deps).
	var imageResolver agentkit.ImageResolver
	if agentDB != nil {
		imageResolver = newCatalogueImageResolver(agentDB, registry, log.Printf)
	}

	// ── Runner ───────────────────────────────────────────────────────────────────
	// The §8.2 internal emitters (worker.finished / worker.failed) append to the
	// event spine, which only the Postgres store has. Left nil on the sqlite
	// fallback: no event tables, so no events — assigning the typed-nil *Store
	// would hand the Runner a non-nil interface over a nil pointer.
	var workerEvents agentkit.WorkerEventStore
	if agentDB != nil {
		workerEvents = agentDB
	}

	// ── Session garbage collection ───────────────────────────────────────────────
	// Both of the Runner's background loops, off by default until this was wired:
	// idle containers were never reclaimed (so the host port pool only drained)
	// and expired snapshots were never retired. See gc.go for the numbers and
	// why. A nonsense duration is a boot error, not a loop that quietly never
	// runs.
	gc, err := resolveGCConfig(os.Getenv)
	must(err)
	// The B4 constraint, stated where it is relied on: the reaper tombstones
	// `agent_custom_images` — a config-guarded table — deliberately OUTSIDE the
	// config-event seam, because storage GC is not a configuration decision an
	// agent made. That is legal here precisely because agentd never arms
	// agentdb.InstallConfigEventGuard on this store (only tests do). Wire it only
	// on Postgres: the catalogue it sweeps has no sqlite equivalent. Declared as
	// the interface so the sqlite fallback hands the Runner a genuine nil, not a
	// non-nil interface wrapping a nil *Store.
	var snapshotCatalog agentkit.SnapshotCatalog
	if agentDB != nil {
		snapshotCatalog = agentDB
	}
	log.Printf("[agentd] %s", gc.describeIdleTimeout())
	log.Printf("[agentd] %s", gc.describeReapInterval(snapshotCatalog != nil))

	runner, err := agentkit.NewRunner(agentkit.Deps{
		Fleet:          f,
		Registry:       registry,
		Store:          store,
		Artifacts:      artStore,
		Claims:         claims,
		SessionContext: sessionCtx,
		WorkerEvents:   workerEvents,
		// The §13 pointer at the front of the launch chain (§13.5, §13.6): a
		// session whose resolved context carries a worker image resolves it
		// here, and a failure fails the launch rather than substituting the
		// base image.
		Images: imageResolver,
		// The §13.7 / B4 image catalogue the snapshot TTL reaper sweeps.
		Snapshots: snapshotCatalog,
		Policy: agentkit.Policy{
			BaseImage: baseImage,
			AgentPort: 3010,
			// Reclaim the container (and its host port) of a session nobody has
			// spoken to for this long. NOT a delete: the row survives and the
			// next message restores it from its snapshot.
			ArchiveTimeout:             gc.idleTimeout,
			SnapshotReapInterval:       gc.reapInterval,
			SessionEnv:                 sessionEnv,
			DisableModelAPIKeyOverride: subscriptionMode,
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
		AgentDB:   agentDB, // nil on the SQLite fallback → legacy read paths
		// GET /agent/memories borrows the same embedder the memory tools use,
		// on the same READ-path terms: EmbedOrDegrade swallows a provider
		// outage, so one query loses its semantic leg rather than its answer.
		MemoryEmbedder: func(ctx context.Context, text string) []float32 {
			return embedding.EmbedOrDegrade(ctx, embedder, text)
		},
	})
	must(err)

	// API mux (authenticated) + an outer root mux for unauthenticated routes.
	apiMux := api.Mux()

	// ── Router + scheduler + human attention (product layer) ─────────────────────
	// All three need the product-layer tables, so all three are wired only on the
	// Postgres store; on the SQLite fallback events are never routed, schedules
	// never fire and the attention route is not mounted (404). See router.go /
	// scheduler.go / attention.go, and dispatch.go for the ONE gate the router
	// and the scheduler share — capacity is decided in exactly one place.
	//
	// attention is declared out here because it is shared by the HTTP route and
	// E4's `request_human_attention` MCP tool: the §9 mechanics are implemented
	// once, in attention.go, and the tool is a thin adapter onto this service.
	var attention *attentionService
	if agentDB != nil {
		// The `config.changed` emitter (§15.4, §15.8 — J3). Installed FIRST and
		// before anything can serve a request, because it is a post-commit hook
		// on the config-log seam: a configuration mutation that lands before the
		// hook exists is a change nobody is told about. Its sweep repairs
		// emissions lost to a crash between commit and append. See
		// configchanged.go.
		configChanges := newConfigChangeEmitter(agentDB, log.Printf)
		agentDB.SetConfigEventHook(configChanges.Hook())
		go configChanges.Run(ctx)

		gate := newDispatcher(dispatcherConfig{
			Store: agentDB,
			// The lease is what the router's reaper claims a dead job by (§8.4
			// step 4), so the starter must be able to take and release it.
			Starter: newRunnerSessionStarter(runner, store).withLeases(agentDB),
			// The core tool servers every job is told about (§6.2 step 3). This is
			// what makes memory_search & co. reachable from inside a worker job;
			// E4/I2 add their tools to the same server, so this line does not grow.
			CoreMCP:      coreMCPServers(selfURL),
			DefaultImage: baseImage,
			// The briefing read seam (§6.2 step 2.4, §7.4) — the rolling summary
			// and each of the worker's own selectors.
			Memories: agentDB,
			Budget: newTokenBudget(tokenBudgetConfig{
				Store:  agentDB,
				Notify: softBudgetNotifier(os.Getenv, log.Printf),
			}),
			// Composition step 1 (§6.2, §13.5): `worker.image > project
			// base_image > global`. The SAME resolver the Runner holds, so the
			// composed image and a launch-time resolution cannot disagree.
			Images: imageResolver,
		})

		rt := newRouter(routerConfig{
			Store:      agentDB,
			Dispatcher: gate,
			Reaper:     newLeaseReaper(agentDB),
		})
		go rt.Run(ctx)

		sched := newScheduler(schedulerConfig{Store: agentDB, Dispatcher: gate})
		go sched.Run(ctx)

		attention = newAttentionService(agentDB, permalinks)
		apiMux.HandleFunc("POST /agent/attention", attentionHandler(attention))
		go newAttentionSweeper(agentDB).Run(ctx)
		log.Printf("[agentd] router + scheduler + attention sweep running (zone=%s)", time.Local)
	} else {
		log.Printf("[agentd] no DATABASE_URL — event routing, schedules and request_human_attention are unavailable")
	}

	// ── Login modes ──────────────────────────────────────────────────────────────
	// google (GOOGLE_CLIENT_ID) and/or password (AGENTKIT_TEST_LOGIN) mint real
	// project-scoped JWTs, so both require a verifying secret and a project map.
	// Neither set = dev-open mode with the legacy /dev/token issuer.
	googleClientID := os.Getenv("GOOGLE_CLIENT_ID")
	testLogin := os.Getenv("AGENTKIT_TEST_LOGIN")
	loginEnabled := googleClientID != "" || testLogin != ""

	root := http.NewServeMux()
	root.HandleFunc("/health", healthHandler)
	root.HandleFunc("GET /auth/config", authConfigHandler(googleClientID, testLogin != ""))

	if loginEnabled {
		if len(jwtSecret) == 0 {
			log.Fatal("[agentd] login modes require AGENTKIT_JWT_SECRET (dev-open auth would ignore the minted tokens)")
		}
		pm, err := loadProjectMap(os.Getenv)
		must(err)
		loginIssuer := devclaims.NewWithTTL(jwtSecret, 12*time.Hour)
		if googleClientID != "" {
			root.HandleFunc("POST /auth/google", authGoogleHandler(
				&googleVerifier{clientID: googleClientID}, pm, loginIssuer))
			log.Printf("[agentd] google login enabled (%d mapped account(s))", len(pm))
		}
		if testLogin != "" {
			email, password, err := parseTestLogin(testLogin)
			must(err)
			root.HandleFunc("POST /auth/password", authPasswordHandler(email, password, pm, loginIssuer))
			log.Printf("[agentd] WARNING: password test login enabled for %s — all projects granted; test/dev only", email)
		}
		// Wildcard-login exchange: mints tokens for new project IDs.
		root.HandleFunc("POST /auth/project-token", authProjectTokenHandler(jwtSecret, loginIssuer))
	} else {
		// /dev/token (DEV ONLY): issues a short-lived JWT for the bundled UI. Not
		// registered when a login mode is on — it would mint valid demo tokens
		// signed with the real secret.
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
	}

	root.Handle("/agent-proxy/", http.StripPrefix("/agent-proxy", newModelProxyHandler()))

	// ── Core MCP tools (memory, images, skills, management) ──────────────────────
	// One http MCP server, mounted outside the API auth middleware because it
	// authenticates differently: the caller is a session container bearing its
	// per-session token, and the project scope comes from that token's claims.
	// See mcpserver.go. Needs the product-layer tables, so — like the session
	// context provider — it is wired only on the Postgres store.
	//
	// The image and skill tools additionally take the Runner: image_create
	// snapshots the calling session's container (§13.4) and skill_install
	// installs into it (§14.2), both identified from the token, never from a
	// tool argument.
	if agentDB != nil {
		mcpSrv := newMCPServer(coreMCPServerName, newSessionTokenAuth(sessionSecret, agentDB).authenticate)
		mcpSrv.register(newMemoryTools(agentDB, embedder, permalinks).tools()...)
		mcpSrv.register(newImageTools(agentDB, runner, permalinks).tools()...)
		mcpSrv.register(newSkillTools(agentDB, runner, permalinks).tools()...)
		mcpSrv.register(newManagementTools(agentDB, embedder, attention, permalinks).tools()...)
		mcpSrv.register(newConfigLogTools(agentDB, permalinks).tools()...)
		root.Handle(coreMCPPath, mcpSrv)
		// Some MCP clients normalise the endpoint with a trailing slash; both
		// spellings must reach the same server or the tools simply vanish.
		root.Handle(coreMCPPath+"/", mcpSrv)
		log.Printf("[agentd] core mcp: %s%s tools=%s", selfURL, coreMCPPath,
			strings.Join(sortedStrings(mcpSrv.toolNames()), ","))
	} else {
		log.Printf("[agentd] core mcp DISABLED (no DATABASE_URL): memory requires Postgres")
	}

	// Everything else goes through auth.
	root.Handle("/", jwtAuthMiddleware(jwtSecret, apiMux))

	// ── Serve ────────────────────────────────────────────────────────────────────
	addr := envOr("ADDR", ":8099")
	log.Printf("[agentd] listening on %s  image=%s  docker=%s", addr, baseImage, dockerHost)
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
