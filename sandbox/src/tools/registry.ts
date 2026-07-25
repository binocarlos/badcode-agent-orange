// The tool-plugin seam (in-image). A product registers app-handled tools and
// their marker→event mappings here instead of editing the agent core. The
// generic core ships the builtins; Platinum's render_table/create_dashboard/etc.
// become a registered ToolPlugin bundle. See ../../docs/08-tool-registry.md.

import type {
  SdkMcpToolDefinition,
  McpSdkServerConfigWithInstance,
  McpServerConfig,
  McpStdioServerConfig,
  McpHttpServerConfig,
} from '@anthropic-ai/claude-agent-sdk';

/** A marker payload an app-handled tool returns; the PostToolUse hook detects it,
 *  emits an SSE event, and replaces the model-visible output with compact text. */
export interface MarkerSpec {
  /** The JSON key that identifies this marker, e.g. "__render_table". */
  key: string;
  /** The extension SSE event type to emit, e.g. "table_rendered". */
  event: string;
  /** Map the marker payload → the SSE event's data. */
  toEvent(payload: any): Record<string, unknown>;
  /** Map the marker payload → the text the MODEL should see (compact). */
  toModelText(payload: any): string;
}

/** A tool plugin: an MCP tool plus an optional marker mapping. */
export interface ToolPlugin {
  name: string; // e.g. "render_table"
  sdkTool: SdkMcpToolDefinition<any>;
  marker?: MarkerSpec;
}

// ---------------------------------------------------------------------------
// Session-supplied MCP servers (docs/product/01-session-config.md §4)
//
// The host configures MCP servers per session; the Runner POSTs them to this
// control server as `mcp_servers` on session create. They are MERGED OVER (never
// replace) the in-image tool registry: session config wins on name collision.
//
// This TS shape mirrors the canonical Go types in go/agentdb/sessions.go
// (MCPServerConfig / MCPServers) field-for-field. Keep them in sync — the Go
// side is the source of truth for validation semantics.
// ---------------------------------------------------------------------------

/**
 * One session-supplied MCP server. Exactly ONE transport is configured:
 * stdio (`command`/`args`/`env`) or http (`url`/`headers`).
 *
 * Values in `env` and `headers` may be whole-value `${VAR}` references — the
 * *name* of an environment variable of the session container, resolved at MCP
 * process spawn time (§4.4). They are never secret values, which is what makes
 * persisting and displaying this config safe by construction.
 */
export interface SessionMCPServerConfig {
  /** stdio transport: an executable inside the container. */
  command?: string;
  /** stdio command arguments. */
  args?: string[];
  /** Environment for the stdio process. Values may be `${VAR}` references. */
  env?: Record<string, string>;
  /** http/sse transport, reachable FROM INSIDE the container. */
  url?: string;
  /** Headers sent with http requests. Values may be `${VAR}` references. */
  headers?: Record<string, string>;
}

/** Name-keyed set of session MCP server configs (wire field: `mcp_servers`). */
export type SessionMCPServers = Record<string, SessionMCPServerConfig>;

/**
 * Server names must match this: the harness derives tool names
 * (`mcp__<name>__*`) from them, so anything exotic produces untypeable tools.
 * Mirrors `mcpServerNamePattern` in go/agentdb/sessions.go.
 */
export const MCP_SERVER_NAME_PATTERN = /^[A-Za-z0-9][A-Za-z0-9_-]*$/;

/**
 * The one supported substitution form: a whole-value `${VAR}` reference.
 * No partial interpolation, no defaults, no nesting (§4.1).
 * Mirrors `envRefPattern` in go/agentdb/sessions.go.
 */
export const MCP_ENV_REF_PATTERN = /^\$\{([A-Za-z_][A-Za-z0-9_]*)\}$/;

/** Thrown when session MCP config is structurally invalid. */
export class MCPConfigError extends Error {
  readonly code = 'INVALID_MCP_SERVERS' as const;
}

/**
 * Thrown when a `${VAR}` reference cannot be resolved at spawn time. The harness
 * lets this propagate rather than spawning: a misconfigured credential must
 * never silently become the literal string `${GMAIL_TOKEN}` or an empty value.
 */
export class MCPEnvResolutionError extends Error {
  readonly code = 'MCP_ENV_UNRESOLVED' as const;
}

/**
 * Validate one server config. Mirrors `MCPServerConfig.Validate()` in
 * go/agentdb/sessions.go — exactly one transport, and only whole-value `${VAR}`
 * references in env/headers.
 */
export function validateSessionMCPServerConfig(cfg: SessionMCPServerConfig): void {
  const hasCommand = !!cfg.command;
  const hasURL = !!cfg.url;

  if (!hasCommand && !hasURL) {
    throw new MCPConfigError('exactly one transport required: set command (stdio) or url (http)');
  }
  if (hasCommand && hasURL) {
    throw new MCPConfigError(
      'exactly one transport allowed: command (stdio) and url (http) are mutually exclusive',
    );
  }
  if (!hasCommand) {
    if (cfg.args && cfg.args.length > 0) {
      throw new MCPConfigError('args require the stdio transport (command)');
    }
    if (cfg.env && Object.keys(cfg.env).length > 0) {
      throw new MCPConfigError('env requires the stdio transport (command)');
    }
  }
  if (!hasURL && cfg.headers && Object.keys(cfg.headers).length > 0) {
    throw new MCPConfigError('headers require the http transport (url)');
  }

  for (const [field, values] of [
    ['env', cfg.env],
    ['headers', cfg.headers],
  ] as const) {
    for (const [k, v] of Object.entries(values ?? {})) {
      if (k === '') {
        throw new MCPConfigError(`${field}: empty key`);
      }
      // A partially interpolated value ("Bearer ${TOKEN}") is rejected rather
      // than accepted: resolution is whole-value only, so such a value would
      // reach the MCP server as the literal string — the silent-credential
      // failure §4.1 forbids.
      if (v.includes('${') && !MCP_ENV_REF_PATTERN.test(v)) {
        throw new MCPConfigError(
          `${field} "${k}": ${JSON.stringify(v)} is not a whole-value \${VAR} reference (no partial interpolation)`,
        );
      }
    }
  }
}

/** Validate every server name and config. Mirrors `MCPServers.Validate()` in Go. */
export function validateSessionMCPServers(servers: SessionMCPServers): void {
  for (const [name, cfg] of Object.entries(servers ?? {})) {
    if (!MCP_SERVER_NAME_PATTERN.test(name)) {
      throw new MCPConfigError(
        `mcp server "${name}": name must match ${MCP_SERVER_NAME_PATTERN.source}`,
      );
    }
    try {
      validateSessionMCPServerConfig(cfg);
    } catch (err) {
      throw new MCPConfigError(
        `mcp server "${name}": ${err instanceof Error ? err.message : String(err)}`,
      );
    }
  }
}

/**
 * The allowedTools entry that grants every tool of a session-supplied MCP
 * server. Tool names are not known until the server is connected, so we grant
 * at server granularity — the SDK's documented `mcp__<server>__*` spec (§4.3).
 */
export function mcpServerToolSpec(name: string): string {
  return `mcp__${name}__*`;
}

/**
 * Resolve one value: either a literal, or a whole-value `${VAR}` reference
 * looked up in `env`. Throws when the variable is unset or empty.
 */
function resolveValue(env: NodeJS.ProcessEnv, where: string, key: string, value: string): string {
  const match = MCP_ENV_REF_PATTERN.exec(value);
  if (!match) {
    if (value.includes('${')) {
      // Defence in depth: validation should already have rejected this.
      throw new MCPEnvResolutionError(
        `${where}: ${key}: ${JSON.stringify(value)} is not a whole-value \${VAR} reference`,
      );
    }
    return value;
  }
  const varName = match[1];
  const resolved = env[varName];
  if (resolved === undefined || resolved === '') {
    throw new MCPEnvResolutionError(
      `${where}: ${key}: environment variable ${varName} is unset or empty — ` +
        'refusing to spawn the MCP server with an unresolved credential ' +
        '(set it on agentd and add it to AGENTKIT_MCP_ENV)',
    );
  }
  return resolved;
}

function resolveMap(
  env: NodeJS.ProcessEnv,
  where: string,
  values: Record<string, string> | undefined,
): Record<string, string> | undefined {
  if (!values || Object.keys(values).length === 0) return undefined;
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(values)) {
    out[k] = resolveValue(env, where, k, v);
  }
  return out;
}

/**
 * Convert session MCP config into the SDK's `mcpServers` shape, resolving every
 * whole-value `${VAR}` reference in env/headers from `env` (the environment the
 * MCP process will be spawned with).
 *
 * Throws MCPEnvResolutionError on the first unset or empty variable — the caller
 * must NOT fall back to spawning without the credential (§4.1).
 */
export function resolveSessionMCPServers(
  servers: SessionMCPServers,
  env: NodeJS.ProcessEnv,
): Record<string, McpServerConfig> {
  const out: Record<string, McpServerConfig> = {};
  for (const [name, cfg] of Object.entries(servers ?? {})) {
    const where = `mcp server "${name}"`;
    if (cfg.command) {
      const stdio: McpStdioServerConfig = {
        type: 'stdio',
        command: cfg.command,
        ...(cfg.args && cfg.args.length > 0 ? { args: cfg.args } : {}),
      };
      const resolvedEnv = resolveMap(env, `${where} env`, cfg.env);
      if (resolvedEnv) stdio.env = resolvedEnv;
      out[name] = stdio;
    } else if (cfg.url) {
      const http: McpHttpServerConfig = { type: 'http', url: cfg.url };
      const resolvedHeaders = resolveMap(env, `${where} headers`, cfg.headers);
      if (resolvedHeaders) http.headers = resolvedHeaders;
      out[name] = http;
    } else {
      throw new MCPConfigError(`${where}: exactly one transport required`);
    }
  }
  return out;
}

/** Resolved SDK query() options for one turn. */
export interface ResolvedTools {
  allowedTools: string[];
  disallowedTools: string[];
  /** In-image registry servers (the SDK-instance `ui` server). */
  mcpServers: Record<string, McpSdkServerConfigWithInstance>;
  /**
   * Session-supplied MCP servers, still UNRESOLVED (`${VAR}` references intact).
   * The harness resolves these against the container environment at spawn time
   * and merges them over `mcpServers` — session config wins on name collision.
   */
  sessionMCPServers: SessionMCPServers;
  markers: MarkerSpec[];
}

/** ToolRegistry: builtins + product plugins, resolved per turn. */
export interface ToolRegistry {
  builtins(): ToolPlugin[]; // ask_user, write_file, view_image, screenshot_url
  register(p: ToolPlugin): void;
  resolve(allowed?: string[], sessionMCPServers?: SessionMCPServers): ResolvedTools;
}

// Library defaults applied by resolve(): the SDK's own internal tools run in the
// sandbox; Task (sub-agents) and Write (replaced by write_file) are disallowed.
export const DEFAULT_DISALLOWED_TOOLS = ['Task', 'Write'];
export const DEFAULT_PERMISSION_MODE = 'bypassPermissions';
