// The tool-plugin seam (in-image). A product registers app-handled tools and
// their marker→event mappings here instead of editing the agent core. The
// generic core ships the builtins; Platinum's render_table/create_dashboard/etc.
// become a registered ToolPlugin bundle. See ../../docs/08-tool-registry.md.

import type { SdkMcpToolDefinition, McpSdkServerConfigWithInstance } from '@anthropic-ai/claude-agent-sdk';

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

/** Resolved SDK query() options for one turn. */
export interface ResolvedTools {
  allowedTools: string[];
  disallowedTools: string[];
  mcpServers: Record<string, McpSdkServerConfigWithInstance>;
  markers: MarkerSpec[];
}

/** ToolRegistry: builtins + product plugins, resolved per turn. */
export interface ToolRegistry {
  builtins(): ToolPlugin[]; // ask_user, write_file, view_image, screenshot_url
  register(p: ToolPlugin): void;
  resolve(allowed?: string[]): ResolvedTools;
}

// Library defaults applied by resolve(): the SDK's own internal tools run in the
// sandbox; Task (sub-agents) and Write (replaced by write_file) are disallowed.
export const DEFAULT_DISALLOWED_TOOLS = ['Task', 'Write'];
export const DEFAULT_PERMISSION_MODE = 'bypassPermissions';
