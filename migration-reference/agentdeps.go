// Package agentdeps constructs the agentkit Runner stack for the Platinum agent
// v2 composition. It is extracted from goapi/cmd/goapi/serve_v2.go so both the
// production serve command and the deterministic integration harness can build
// the same real Runner without duplicating the wiring.
package agentdeps

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/rs/zerolog/log"

	"github.com/Bayes-Price/Platinum/goapi/pkg/agenthost"
	"github.com/Bayes-Price/Platinum/goapi/pkg/config"
	"github.com/Bayes-Price/Platinum/goapi/pkg/controller"
	"github.com/Bayes-Price/Platinum/goapi/pkg/mergedconfig"
	"github.com/Bayes-Price/Platinum/goapi/pkg/prompts"
	"github.com/Bayes-Price/Platinum/goapi/pkg/storage"
	"github.com/Bayes-Price/Platinum/goapi/pkg/store"
	"github.com/Bayes-Price/Platinum/goapi/pkg/types"
	"github.com/bayes-price/agentkit"
	"github.com/bayes-price/agentkit/agentdb"
	"github.com/bayes-price/agentkit/execenv"
	dockerdind "github.com/bayes-price/agentkit/execenv/docker"
	"github.com/bayes-price/agentkit/extension"
	"github.com/bayes-price/agentkit/fleet"
	"github.com/Bayes-Price/Platinum/goapi/pkg/installations"
	"github.com/bayes-price/agentkit/imageregistry"
	"github.com/bayes-price/agentkit/imageregistry/ociregistry"
)

// IssueFunc signs a scoped JWT for a session (mirrors buildIssueFunc in serve_v2.go).
// Exported so callers that need to mint tokens (harness HTTP drive helpers) can
// use the same signing logic.
type IssueFunc = agenthost.IssueFunc

// Build constructs the agentkit Runner stack for COMPOSITION_AGENT=v2.
// overrideEnv, overrideRegistry, and overrideBlobs allow tests to inject mocks
// instead of real Docker / Azure. Pass nil for all three in production.
func Build(
	ctx context.Context,
	cfg *config.ServerConfig,
	adb *agentdb.Store,
	st store.Store,
	ctrl *controller.Controller,
	overrideEnv execenv.ExecutionEnvironment,
	overrideRegistry imageregistry.ImageRegistry,
	overrideBlobs ...storage.BlobStore,
) (agentkit.Runner, *agenthost.Adapters, error) {
	// Blob store for agenthost BlobStore adapter + blob-archive registry mode.
	//
	// Agent-owned data (skill bundles, blob-archive snapshots, session artifacts)
	// lives on a dedicated agent-data account (V2BlobAccount) and MUST persist
	// across restarts. The hybrid driver is copy-on-write (writes local-only,
	// never Azure) — correct for overlaying live customer table data, but it would
	// silently drop hoisted skills the moment the container is recreated. So when
	// an Azure client + agent-data account are configured, bind agent storage
	// directly to Azure regardless of STORAGE_DRIVER; fall back to the configured
	// driver (hybrid/local) only when Azure isn't available.
	var blobStore storage.BlobStore
	if len(overrideBlobs) > 0 && overrideBlobs[0] != nil {
		blobStore = overrideBlobs[0]
	} else if ctrl != nil {
		if azClient := ctrl.AzureClient(); azClient != nil && cfg.Agent.V2BlobAccount != "" {
			log.Info().Str("account", cfg.Agent.V2BlobAccount).Msg("agentdeps.Build: agent data bound directly to Azure (durable across restarts)")
			blobStore = storage.NewAzureStore(azClient)
			// Per-session artifact/skill-source blobs live in a fixed container
			// (session scoping is in the key path). Ensure it exists so writes don't
			// fail with ContainerNotFound on a fresh account.
			if err := azClient.EnsureContainer(ctx, cfg.Agent.V2BlobAccount, agenthost.SessionDataContainer); err != nil {
				log.Warn().Err(err).Str("container", agenthost.SessionDataContainer).Msg("agentdeps.Build: ensure session-data container failed")
			}
		} else {
			var err error
			blobStore, err = storage.New(azClient)
			if err != nil {
				log.Warn().Err(err).Msg("agentdeps.Build: blob store unavailable")
			}
		}
	}

	// Host-extension adapters.
	adapters := agenthost.NewAdapters(agenthost.Config{
		DB:          adb,
		Blobs:       blobStore,
		BlobAccount: cfg.Agent.V2BlobAccount,
		Issue:       BuildIssueFunc(cfg.WebServer.JWTSecret),
		LoadCtx:     BuildSessionContextFunc(st),
	})

	// Execution environment.
	var env execenv.ExecutionEnvironment
	if overrideEnv != nil {
		env = overrideEnv
	} else {
		var err error
		env, err = dockerdind.NewDinD(dockerdind.DinDConfig{
			DockerHost:     cfg.Agent.V2DockerHost,
			PortRangeStart: cfg.Agent.V2PortRangeStart,
			PortRangeEnd:   cfg.Agent.V2PortRangeEnd,
			GatewayIP:      cfg.Agent.V2GatewayIP,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("agentdeps.Build: DinD: %w", err)
		}
	}

	if err := validateRegistryConfig(cfg.Agent); err != nil {
		return nil, nil, err
	}

	// Image registry.
	var reg imageregistry.ImageRegistry
	if overrideRegistry != nil {
		reg = overrideRegistry
	} else {
		var err error
		switch cfg.Agent.V2ImageRegistry {
		case "registry":
			reg, err = ociregistry.New(ociregistry.Config{
				DockerHost: cfg.Agent.V2DockerHost,
				Registry:   cfg.Agent.V2RegistryURL,
				Username:   cfg.Agent.V2RegistryUser,
				Password:   cfg.Agent.V2RegistryPass,
				AlwaysPull: installations.IsLocalRegistry(cfg.Agent.V2RegistryURL),
			})
		default:
			return nil, nil, fmt.Errorf("agentdeps.Build: unknown AGENT_IMAGE_REGISTRY %q (use 'registry')", cfg.Agent.V2ImageRegistry)
		}
		if err != nil {
			return nil, nil, fmt.Errorf("agentdeps.Build: registry(%s): %w", cfg.Agent.V2ImageRegistry, err)
		}
	}

	// Fleet: single-worker backed by the execution environment.
	f := fleet.NewMemory(adb, &fleet.MemFleetOptions{TrustedWorkload: true})
	if err := f.Register(ctx, &fleet.Worker{
		ID:   "w1",
		Env:  env,
		Caps: env.Capabilities(),
	}); err != nil {
		return nil, nil, fmt.Errorf("agentdeps.Build: fleet.Register: %w", err)
	}

	// Sandbox-facing env. The sandbox routes model calls via goapi's agent-proxy
	// (/api/v1/agent-proxy) so the raw key never enters the container.
	// HOST_API_URL routes tool callbacks to goapi.
	sandboxHost := cfg.Agent.V2SandboxHostURL
	sessionEnv := map[string]string{
		"ANTHROPIC_BASE_URL": sandboxHost + "/api/v1/agent-proxy",
		"HOST_API_URL":       sandboxHost + "/api/v1",
		// Claude Code validates ANTHROPIC_API_KEY on startup before making HTTP calls.
		// goapi /agent-proxy injects the real provider key upstream.
		"ANTHROPIC_API_KEY": "sk-ant-api03-proxy-passthrough-key-00000000000000000000000000000000000000000000000000000000AA",
	}
	// Test/mock escape hatch: when a local model mock URL is set, point at it.
	if mock := os.Getenv("AGENT_V2_MODEL_MOCK_URL"); mock != "" {
		sessionEnv["ANTHROPIC_BASE_URL"] = mock
	}

	// Session containers run entirely from their installation image — no host
	// bind mounts. Changes to an installation (skills, workspace-lib, plugins,
	// harness source) take effect by rebuilding the image (`./stack rebuild
	// sandbox <name>`) and starting a new session, not by hot-reload. This keeps
	// the runtime filesystem writable so skills can be installed into a live
	// session and captured when the session is burned into a new image.

	// Build and start Runner.
	skillCatalog := agenthost.NewSkillCatalogStore(adb, adapters.BlobFactory)
	adapters.SkillBundles = skillCatalog
	runner, err := agentkit.NewRunner(agentkit.Deps{
		Fleet:         f,
		Registry:      reg,
		Store:         adb,
		Artifacts:     adapters.Artifacts,
		Claims:        adapters.Claims,
		Blobs:         adapters.BlobFactory,
		SessionContext: adapters.SessionCtx,
		SkillCatalog:  skillCatalog,
		CustomImages:  agenthost.NewCustomImageCatalogStore(adb),
		Policy: agentkit.Policy{
			BaseImage:      cfg.Agent.V2BaseImage,
			AgentPort:      3010,
			SessionEnv:     sessionEnv,
			Mounts:         nil,
			ArchiveTimeout: cfg.Agent.V2ArchiveTimeout,
		},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("agentdeps.Build: NewRunner: %w", err)
	}
	if err = runner.Start(ctx); err != nil {
		return nil, nil, fmt.Errorf("agentdeps.Build: runner.Start: %w", err)
	}
	return runner, adapters, nil
}

// ValidateRegistryConfig fails fast when registry (ociregistry) mode is selected
// but the required configuration is absent. Exported so serve_v2.go can delegate
// instead of duplicating the check.
func ValidateRegistryConfig(a config.Agent) error {
	if a.V2ImageRegistry != "registry" {
		return nil
	}
	if a.V2RegistryURL == "" {
		return fmt.Errorf("AGENT_IMAGE_REGISTRY=registry requires AGENT_V2_REGISTRY_URL")
	}
	return nil
}

// validateRegistryConfig is the unexported shim used internally by Build.
func validateRegistryConfig(a config.Agent) error { return ValidateRegistryConfig(a) }

// BuildIssueFunc returns an agenthost.IssueFunc that signs HS256 JWTs.
// Mirrors the production serve_v2.go issueFunc for consistent token shape.
func BuildIssueFunc(jwtSecret string) IssueFunc {
	type agentClaims struct {
		Email     string `json:"email"`
		Customer  string `json:"customer"`
		Job       string `json:"job,omitempty"`
		SessionID string `json:"session_id"`
		Scope     string `json:"scope"`
		jwt.RegisteredClaims
	}
	return func(email, customer, job, sessionID string) (string, error) {
		c := agentClaims{
			Email:     email,
			Customer:  customer,
			Job:       job,
			SessionID: sessionID,
			Scope:     "agent",
			RegisteredClaims: jwt.RegisteredClaims{
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
				IssuedAt:  jwt.NewNumericDate(time.Now()),
				NotBefore: jwt.NewNumericDate(time.Now()),
				Issuer:    "platinum-api",
				Subject:   email,
			},
		}
		return jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString([]byte(jwtSecret))
	}
}

// BuildSessionContextFunc returns an agenthost.SessionContextFunc backed by the
// Platinum store. Merges cascading config + brand themes + persona into a system prompt.
func BuildSessionContextFunc(st store.Store) agenthost.SessionContextFunc {
	return func(ctx context.Context, scope extension.ContextScope) (*extension.SessionContext, error) {
		customer, job, persona := scope.Customer, scope.Job, scope.Persona
		if customer == "" || st == nil {
			return &extension.SessionContext{}, nil
		}

		// Always lead with the concrete session scope. The container no longer
		// receives SESSION_CUSTOMER/SESSION_JOB env vars — customer/job are
		// derived from the session token by the /agent-gw gateway — so this block
		// is the agent's only way to know which dataset it is operating on.
		// Without it the agent reports "no job is configured" and refuses to run.
		parts := []string{sessionScopeBlock(customer, job, scope.UserEmail)}

		mergedConfig, err := mergedconfig.GetMergedEntityConfig(ctx, st, nil, types.ListConfigsQuery{
			ConfigType: types.ConfigTypeJob,
			Customer:   customer,
			Job:        job,
		})
		if err != nil || mergedConfig == nil || mergedConfig.Data == nil {
			// No per-entity config prompts, but the scope block above still stands.
			return &extension.SessionContext{SystemPrompt: strings.Join(parts, "\n\n")}, nil
		}
		if mergedConfig.Data.TableAnalysisPrompt != "" {
			parts = append(parts, mergedConfig.Data.TableAnalysisPrompt)
		}
		if mergedConfig.Data.MethodPrompt != "" {
			parts = append(parts, mergedConfig.Data.MethodPrompt)
		}
		if st != nil {
			brandThemes, err := st.ListBrandThemes(ctx, customer)
			if err == nil && len(brandThemes) > 0 {
				var tp []string
				tp = append(tp, "## Brand Themes\n")
				for _, bt := range brandThemes {
					pj, _ := json.Marshal(bt.Palette)
					tp = append(tp, fmt.Sprintf("### %s\n```json\n%s\n```", bt.Name, string(pj)))
				}
				parts = append(parts, strings.Join(tp, "\n"))
			}
		}
		if mergedConfig.Data.BrandPalette != nil {
			pj, _ := json.Marshal(mergedConfig.Data.BrandPalette)
			parts = append(parts, fmt.Sprintf("## Brand Palette\n```json\n%s\n```", string(pj)))
		}
		if pp := prompts.ResolvePersonaPrompt(persona, mergedConfig.Data.CustomPersonas); pp != "" {
			parts = append(parts, fmt.Sprintf("## Persona Instructions\n\nYou are operating as: %s\n\n%s", persona, pp))
		}
		return &extension.SessionContext{SystemPrompt: strings.Join(parts, "\n\n")}, nil
	}
}

// sessionScopeBlock renders the authoritative customer/job/user context that the
// in-container agent operates under. customer/job are injected automatically on
// every data request by the /agent-gw gateway (derived from the session token),
// so the agent never needs to ask for them and must not claim it lacks them.
func sessionScopeBlock(customer, job, userEmail string) string {
	var b strings.Builder
	b.WriteString("## Session Context\n\n")
	b.WriteString("You are connected to a specific dataset. This context is already configured — do NOT ask the user which customer or job to use, and do NOT claim that no dataset is loaded.\n\n")
	fmt.Fprintf(&b, "- **Customer:** %s\n", customer)
	if job != "" {
		fmt.Fprintf(&b, "- **Job (dataset):** %s\n", job)
	} else {
		b.WriteString("- **Job (dataset):** (cross-job session — all jobs for this customer are available; use `pt jobs` to list them)\n")
	}
	if userEmail != "" {
		fmt.Fprintf(&b, "- **User:** %s\n", userEmail)
	}
	b.WriteString("\nEvery `pt` command and the `render_table`/`render_chart` tools automatically operate on this customer and job — the scope is applied server-side from your session token, so you do not pass (and cannot override) it. Just run your commands directly.")
	if userEmail != "" {
		fmt.Fprintf(&b, "\n\nYour personal table folder is `Tables/User/%s/`.", userEmail)
	}
	return b.String()
}
