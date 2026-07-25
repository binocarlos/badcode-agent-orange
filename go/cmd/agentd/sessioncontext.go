package main

// sessioncontext.go — agentd's real SessionContextProvider (docs/product/01-session-config.md §5).
//
// The Runner asks this seam what a session's *defaults* are. agentd answers from
// the product-layer tables: `project_settings` (B1) for the project-wide
// defaults and `workers` (C1) for the worker the session runs as. Precedence,
// per §5:
//
//	system prompt : project prompt PREPENDED to the worker prompt (concatenative)
//	base image    : worker.Image > project_settings.base_image > global Policy.BaseImage
//	MCP servers   : project MCP ∪ worker MCP (union; worker wins on name collision)
//
// The MCP rule is a union on purpose: project MCP config is "granted to **all**
// workers, no exceptions, no filtering" (§5) — a worker can add tools, never
// subtract the project's. Nothing resolved here is a secret: MCP values only
// ever *name* environment variables (`${VAR}`, §4.4); the values themselves
// reach the container through the AGENTKIT_MCP_ENV allowlist (mcpenv.go).
//
// Scope mapping (extension.ContextScope is the engine's generic identity):
//
//	scope.Customer → project (the tenancy namespace, the JWT `customer` claim)
//	scope.Persona  → worker name ((project, name) is a worker's identity)
//
// A persona naming no configured worker is not an error: personas predate the
// workers table, so an unknown one simply contributes no worker layer.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/extension"
)

// projectConfigStore is the narrow read seam the provider needs. `*agentdb.Store`
// satisfies it; tests supply a fake, so §5's precedence rules are unit-testable
// with no database (the seam pattern B1 introduced for httpapi handlers).
type projectConfigStore interface {
	GetProjectSettings(ctx context.Context, project string) (*agentdb.ProjectSettings, error)
	GetWorker(ctx context.Context, project, name string) (*agentdb.Worker, error)
}

// sessionContextProvider implements extension.SessionContextProvider against the
// product-layer tables. globalBaseImage is the stack-wide fallback
// (Policy.BaseImage / AGENTKIT_IMAGE) — the last link of the §5 image chain.
type sessionContextProvider struct {
	store           projectConfigStore
	globalBaseImage string
}

// newSessionContextProvider builds the provider. store must be non-nil.
func newSessionContextProvider(store projectConfigStore, globalBaseImage string) *sessionContextProvider {
	return &sessionContextProvider{store: store, globalBaseImage: globalBaseImage}
}

// resolvedContext is the full §5 resolution. extension.SessionContext carries
// only prompt + image today, so the MCP defaults are reached through
// ResolveMCPServers until the wire path (A2) carries them.
type resolvedContext struct {
	SystemPrompt string
	BaseImage    string
	MCPServers   agentdb.MCPServers
}

// Resolve implements extension.SessionContextProvider: the system prompt and the
// launch image this session defaults to.
func (p *sessionContextProvider) Resolve(ctx context.Context, scope extension.ContextScope) (*extension.SessionContext, error) {
	rc, err := p.resolve(ctx, scope)
	if err != nil {
		return nil, err
	}
	return &extension.SessionContext{SystemPrompt: rc.SystemPrompt, BaseImage: rc.BaseImage}, nil
}

// ResolveMCPServers returns the project ∪ worker MCP defaults for a scope. The
// session-create path lays request-supplied servers *over* these (§5:
// request-supplied values are additive for MCP) — see MergeMCPServers.
func (p *sessionContextProvider) ResolveMCPServers(ctx context.Context, scope extension.ContextScope) (agentdb.MCPServers, error) {
	rc, err := p.resolve(ctx, scope)
	if err != nil {
		return nil, err
	}
	return rc.MCPServers, nil
}

// resolve reads both layers and applies §5's precedence.
func (p *sessionContextProvider) resolve(ctx context.Context, scope extension.ContextScope) (*resolvedContext, error) {
	rc := &resolvedContext{BaseImage: p.globalBaseImage, MCPServers: agentdb.MCPServers{}}
	project := scope.Customer
	if project == "" {
		// No tenancy in scope: nothing project-specific to apply. The global
		// default still stands, so such a session starts rather than failing.
		return rc, nil
	}

	// ── Project layer ────────────────────────────────────────────────────────
	// GetProjectSettings returns the spec defaults (never "not found") for a
	// project whose settings nobody has written yet.
	ps, err := p.store.GetProjectSettings(ctx, project)
	if err != nil {
		return nil, fmt.Errorf("session context: project settings for %q: %w", project, err)
	}
	projectMCP, err := decodeMCPConfig(ps.MCPConfig)
	if err != nil {
		return nil, fmt.Errorf("session context: project %q mcp_config: %w", project, err)
	}
	prompts := []string{ps.SystemPrompt}
	if ps.BaseImage != "" {
		rc.BaseImage = ps.BaseImage
	}
	rc.MCPServers = MergeMCPServers(rc.MCPServers, projectMCP)

	// ── Worker layer (wins on every axis) ────────────────────────────────────
	if workerName := scope.Persona; workerName != "" {
		w, err := p.store.GetWorker(ctx, project, workerName)
		switch {
		case errors.Is(err, agentdb.ErrWorkerNotFound):
			// A persona with no worker row: project defaults only.
		case err != nil:
			return nil, fmt.Errorf("session context: worker %q/%q: %w", project, workerName, err)
		default:
			workerMCP, decErr := decodeMCPConfig(w.MCPConfig)
			if decErr != nil {
				return nil, fmt.Errorf("session context: worker %q/%q mcp_config: %w", project, workerName, decErr)
			}
			prompts = append(prompts, w.SystemPrompt)
			if w.Image != "" {
				// Passed through verbatim: §13's bare-name → latest /
				// name:version → pinned resolution belongs to the image
				// catalogue, not to this seam.
				rc.BaseImage = w.Image
			}
			// Union, worker wins on collision — never a filter of project tools.
			rc.MCPServers = MergeMCPServers(rc.MCPServers, workerMCP)
		}
	}

	rc.SystemPrompt = joinPrompts(prompts...)
	// Fail loudly on config that cannot work rather than handing the sandbox a
	// server it would mis-spawn with a literal "${VAR}" credential (§4.1).
	if err := rc.MCPServers.Validate(); err != nil {
		return nil, fmt.Errorf("session context: project %q mcp config: %w", project, err)
	}
	return rc, nil
}

// joinPrompts concatenates the non-empty layers in precedence order, project
// first ("the project-level system prompt, prepended to every worker's prompt").
func joinPrompts(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, s := range parts {
		if s = strings.TrimSpace(s); s != "" {
			kept = append(kept, s)
		}
	}
	return strings.Join(kept, "\n\n")
}

// MergeMCPServers returns base ∪ over, with `over` winning on name collision.
// Neither input is mutated. Used for both layers of the §5 union, and by the
// session-create path to lay request-supplied servers over the defaults.
func MergeMCPServers(base, over agentdb.MCPServers) agentdb.MCPServers {
	merged := make(agentdb.MCPServers, len(base)+len(over))
	for name, cfg := range base {
		merged[name] = cfg
	}
	for name, cfg := range over {
		merged[name] = cfg
	}
	return merged
}

// decodeMCPConfig turns the opaque `mcp_config` jsonb (agentdb stores it as a
// JSONMap so the store stays free of A1's types) into typed MCP servers.
// A decode failure is an error, not a silent drop: a project whose tool config
// is unreadable must be fixed, not quietly run without its tools.
func decodeMCPConfig(raw agentdb.JSONMap) (agentdb.MCPServers, error) {
	if len(raw) == 0 {
		return agentdb.MCPServers{}, nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("re-encode: %w", err)
	}
	var servers agentdb.MCPServers
	if err := json.Unmarshal(b, &servers); err != nil {
		return nil, fmt.Errorf("decode into MCPServers: %w", err)
	}
	if servers == nil {
		servers = agentdb.MCPServers{}
	}
	return servers, nil
}

// logSessionContextWiring reports which config layers agentd applies, so an
// operator can see at a glance whether project settings are live.
func logSessionContextWiring(enabled bool) {
	if enabled {
		log.Printf("[agentd] session context=project_settings+workers (base image, system prompt, MCP defaults)")
		return
	}
	log.Printf("[agentd] session context=global only (no Postgres store → project settings/workers unavailable)")
}
