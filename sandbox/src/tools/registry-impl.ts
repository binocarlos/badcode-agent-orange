// Concrete ToolRegistry implementation.
// Factored out of agent/src/services/agent-service.ts's option-assembly block
// per docs/90-provenance-map.md. See docs/08-tool-registry.md for the design.

import { createSdkMcpServer } from '@anthropic-ai/claude-agent-sdk';
import {
  ToolRegistry,
  ToolPlugin,
  ResolvedTools,
  DEFAULT_DISALLOWED_TOOLS,
} from './registry.js';
import { askUserTool } from './builtin/ask_user.js';
import { writeFileTool } from './builtin/write_file.js';
import { viewImageTool } from './builtin/view_image.js';
import { screenshotUrlTool } from './builtin/screenshot_url.js';

/**
 * DefaultToolRegistry — the concrete ToolRegistry used by AgentService.
 *
 * Ships the 4 generic builtins (ask_user, write_file, view_image, screenshot_url)
 * and lets product-level code register additional tool plugins (e.g. Platinum's
 * render_table, create_dashboard, etc.).
 *
 * resolve() builds the SDK query() options block — successor to the inline option
 * assembly in agent/src/services/agent-service.ts (lines ~96–149).
 */
export class DefaultToolRegistry implements ToolRegistry {
  private readonly _builtins: ToolPlugin[] = [
    askUserTool,
    writeFileTool,
    viewImageTool,
    screenshotUrlTool,
  ];

  private readonly _plugins: ToolPlugin[] = [];

  builtins(): ToolPlugin[] {
    return this._builtins;
  }

  register(p: ToolPlugin): void {
    this._plugins.push(p);
  }

  resolve(allowed?: string[]): ResolvedTools {
    const allPlugins = [...this._builtins, ...this._plugins];

    // Build the MCP server from all plugin tools
    const mcpServer = createSdkMcpServer({
      name: 'ui',
      version: '1.0.0',
      tools: allPlugins.map(p => p.sdkTool),
    });

    // SDK-prefixed MCP tool names (the SDK prefixes with mcp__<serverName>__)
    const serverName = 'ui';
    const mcpToolNames = allPlugins.map(p => `mcp__${serverName}__${p.name}`);

    // Build the short-name → prefixed-name map for allowlist resolution
    const toolNameMap: Record<string, string> = {};
    for (const p of allPlugins) {
      toolNameMap[p.name] = `mcp__${serverName}__${p.name}`;
    }

    // Resolve the allowedTools list
    let resolvedAllowedTools: string[];
    if (allowed && allowed.length > 0) {
      const result: string[] = [];
      for (const t of allowed) {
        if (toolNameMap[t]) {
          // Short MCP tool name → prefixed name
          result.push(toolNameMap[t]);
        } else if (t.startsWith('mcp__')) {
          // Already prefixed
          result.push(t);
        } else {
          // SDK built-in tool (WebSearch, WebFetch, Bash, etc.)
          result.push(t);
        }
      }
      resolvedAllowedTools = [...result, 'Skill'];
    } else {
      // No allowlist: permit SDK defaults + all MCP tools
      const sdkDefaults = ['Bash', 'WebSearch', 'WebFetch'];
      resolvedAllowedTools = [...sdkDefaults, ...mcpToolNames, 'Skill'];
    }

    // Collect marker specs from all plugins that have one
    const markers = allPlugins
      .filter(p => p.marker != null)
      .map(p => p.marker!);

    return {
      allowedTools: resolvedAllowedTools,
      disallowedTools: DEFAULT_DISALLOWED_TOOLS,
      mcpServers: { [serverName]: mcpServer },
      markers,
    };
  }
}

// Singleton registry used by the agent service.
// Products that need to register custom tool plugins should call
// `toolRegistry.register(myPlugin)` before the first query.
export const toolRegistry = new DefaultToolRegistry();
