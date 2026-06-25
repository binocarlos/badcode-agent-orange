# 08 — Tools: internal vs external, and the plugin seam

Tools are where a generic agent runtime becomes a *specific* product. The library ships a small set of
generic tools and a **plugin seam** so a host product registers its own — the way Platinum supplies
`render_table`, `create_dashboard`, and the `pt` CLI.

## Two kinds of tools

The current system already embodies a clean distinction, which the library names explicitly:

- **Internal tools** run *inside the sandbox* and execute for real: `Bash`, `Glob`, `Grep`, `Read`,
  the `pt` CLI (invoked via `Bash`), `Skill`. They're the Agent SDK's own tools plus binaries baked
  into the image. The agent's effects on the workspace come from these.
- **External / app-handled tools** do **not** execute in the sandbox. They return a **marker payload**
  that the PostToolUse hook intercepts and turns into an SSE event for the host/UI to act on:
  `render_table`, `render_chart`, `ask_user`, `create_dashboard`, `register_artifact`. They're how the
  agent "speaks to the application."

The generic core ships the app-handled tools that *every* product needs (`ask_user`, `write_file`,
`view_image`, `screenshot_url`) and treats the rest as plugins.

## The marker pattern (preserved exactly)

An app-handled tool returns text containing a JSON marker (`{"__render_table": true, ...}`); the
PostToolUse hook detects it, emits the corresponding SSE event, and **replaces the tool's visible
output** with a compact form so the model sees clean text instead of a huge JSON blob. This is a good
design (it keeps token cost down and decouples "the agent asked to render X" from "the app renders X")
and the library keeps it verbatim. The marker→event mapping becomes data, not hard-coded branches:

```ts
// sandbox: a tool plugin declares its marker and the event it produces.
interface ToolPlugin {
  name: string;                       // "render_table"
  sdkTool: SdkMcpToolDef;             // the MCP tool definition (args schema + handler)
  marker?: {
    key: string;                      // "__render_table"
    event: string;                    // "table_rendered"  (an extension event type)
    // Map the marker payload → the SSE event data, and → the text the model should see.
    toEvent(payload: any): Record<string, unknown>;
    toModelText(payload: any): string;
  };
}
```

The generic PostToolUse hook iterates registered plugins' markers instead of the current hard-coded
`if (__render_table) … else if (__render_chart) …` ladder in `agent-service.ts`. Adding a tool is
registering a plugin; no core edits.

## The `ToolRegistry` (in-image)

```ts
// sandbox/src/tools/registry.ts
export interface ToolRegistry {
  // System/built-in app-handled tools shipped by the library.
  builtins(): ToolPlugin[];           // ask_user, write_file, view_image, screenshot_url
  // Host/product-registered plugins.
  register(p: ToolPlugin): void;
  // Resolve the SDK query() options for a turn given the request's allowlist.
  resolve(allowed?: string[]): {
    allowedTools: string[];           // [...internalNames, ...pluginToolNames, 'Skill']
    disallowedTools: string[];        // ['Task','Write']  (Write replaced by write_file)
    mcpServers: Record<string, McpServer>;  // { ui: <server built from plugins> }
    markers: MarkerSpec[];            // for the PostToolUse hook
  };
}
```

`resolve()` is the single place that builds the SDK options block — successor to the inline option
assembly in `agent-service.ts` (lines ~125–578). Internal-tool allow/deny policy (`disallowedTools:
['Task','Write']`, `permissionMode: 'bypassPermissions'`) is library default, overridable per product.

## Internal tools and the image

Internal tools are mostly the SDK's own; the only product-specific internal tool today is the **`pt`
CLI** — a Go binary baked into the Platinum image and invoked via `Bash`. In the library:

- `pt` is **not** part of the core. It's installed by the *Platinum image build* (a `BuildSpec.Overlay`
  or Dockerfile step), and the agent discovers it because it's on `PATH`.
- The frontend's `pt`-command parsing/labelling (`tool-formatters.ts`) is likewise a **host render
  plugin** ([09](09-frontend-components.md)), not core.

So "which internal tools exist" is an **image** decision, and "how their calls are labelled in the UI"
is a **render-plugin** decision — neither is baked into the runtime.

## External vs internal: who handles the result

```
              ┌──────────────────────── in-image agent ─────────────────────────┐
   model ───▶ │ tool call                                                        │
              │   ├─ internal (Bash/pt/Read…) → executes in sandbox → real result │──▶ model
              │   └─ app-handled (render_table…) → returns marker payload          │
              │            └─ PostToolUse hook: emit SSE event + compact text ─────┼──▶ model
              └───────────────────────────────────────────────────────────────────┘
                                         │ SSE event (table_rendered, ask_user, …)
                                         ▼
                               host (Go) ─ optional hook ─▶ browser render plugin
```

`ask_user` is the canonical app-handled tool: the agent calls it, the hook emits `ask_user`, the UI
shows option buttons, the user's answer comes back as the next turn. Fully generic, ships in core.

## What a product supplies

To give an agent product its character, a host supplies:

1. **An image** with the internal tools/binaries it needs and its `CLAUDE.md`/skills (build-time —
   [03](03-image-registry.md)).
2. **Tool plugins** (in-image) for its app-handled tools + markers.
3. **Render plugins** (browser) for those markers' event types ([09](09-frontend-components.md)).
4. Optionally, **host hooks** for marker side-effects (e.g. `register_artifact` → `ArtifactStore`).

Platinum's `render_table`/`create_dashboard`/`generate_pptx` are precisely such a plugin bundle and
ship *with Platinum*, not with the library.

## Mapping: today → library

| Today | Library |
|---|---|
| `agent/src/mcp/ui-tools.ts` generic tools | `sandbox/src/tools/builtin/*` (ship in core) |
| `agent/src/mcp/ui-tools.ts` render_table/chart/dashboard/pptx | Platinum tool-plugin bundle (host-owned) |
| `agent-service.ts` PostToolUse marker `if/else` ladder | generic plugin-driven marker dispatch |
| `agent-service.ts` SDK option assembly | `ToolRegistry.resolve()` |
| `pt` binary baked into Platinum image | Platinum image build step (not core) |
| `frontend/.../tool-formatters.ts` `pt` parsing | Platinum render plugin (not core) |
