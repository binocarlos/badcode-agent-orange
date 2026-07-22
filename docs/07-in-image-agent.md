# 07 — The in-image agent: control server, harness seam, and tool registry

Something has to run *inside* each session container, hold the conversation, and actually drive the
model. That is the `sandbox/` package (TypeScript) — a long-running HTTP/SSE server the
`ExecutionEnvironment` provisions and the Go `Runner` talks to. It is the **only** code that must
live in the image, and it knows nothing about Docker, suspend/resume, archives, or blob storage —
those are the host's job ([02](02-execution-environment.md), [01](01-architecture.md)).

The sandbox has three cleanly separated concerns, each a section below:

1. **The control server** — harness-agnostic, multi-session. Owns session lifecycle + routing by
   session ID, the SSE event stream + replay buffer, and the credential/proxy plumbing.
2. **The harness seam** — one `Harness` instance per session (e.g. `ClaudeAgentSdkHarness`),
   selected by name in the create-session request. This is *which framework drives the model*.
3. **The tool registry** — the in-image plugin seam that decides which tools a turn may call and
   how app-handled tool calls become SSE events.

Its single responsibility:

> Host **N concurrent sessions** keyed by session ID; for each, boot the **harness** named in its
> create request and run one turn at a time, emitting an SSE event stream with a replay buffer so a
> consumer can attach late or reconnect.

The sandbox is **always multi-session-capable** and never limits session count — the execution
environment decides whether to route more than one session to a given container
([02](02-execution-environment.md)). Everything a session runtime wraps around it (lifecycle,
persistence, networking policy) lives up in Go; *which framework drives the model* lives behind the
harness seam.

---

## 1. What runs in-image, and why TypeScript

The control server is TypeScript, but that is not a vendoring decision:

- The **Claude Agent SDK harness** is TypeScript-first (`query()`, hooks, `createSdkMcpServer` are
  SDK-native). Since at least one first-class harness runs in-process in TS, hosting the control
  server in TS keeps that adapter zero-overhead.
- CLI harnesses (Claude CLI, Gemini, Codex) are separate binaries the adapter shells out to via
  `child_process`, so the server's language is independent of theirs.
- It runs *inside* the image regardless of engine, so its language is independent of the host's (Go).

The server is thin generic plumbing. Product-specific behaviour enters only through two seams: the
**harness** (§4) and the **tool registry** (§5), both populated at build/startup time by the image,
never by editing the core.

---

## 2. The control server: HTTP/SSE contract and configuration

This is the stable boundary between the Go `Runner` and the image. The `ExecutionEnvironment`
reports an `Address`; the Runner appends these **session-scoped** paths. Every response carries an
`X-Session-Id` header. Routes are wired in `sandbox/src/index.ts` (session, health, workspace);
implementations in `sandbox/src/routes/`.

| Method · Path | Purpose | Called by |
|---|---|---|
| `POST /sessions` `{sessionId, harness?, model?, maxTurns?}` | **Create a session**: boot the named harness + credential pre-check (§4). Idempotent if it already exists with the same harness | Runner `CreateSession` |
| `DELETE /sessions/:sessionId` | Tear down a session in-process (abort its turns, dispose harness, free maps) | Runner `Destroy` |
| `GET /health` | Liveness; reports `{status, sessions:[...]}` | Runner health checks, Resume wait |
| `POST /sessions/:sessionId/query-stream` | Submit a turn **and** stream its SSE in one response (no race window) | Runner `SendMessage` |
| `GET /sessions/:sessionId/stream/:queryId` | Attach to a query's stream; **replays the in-image buffer** then live | Runner `Stream` (reconnect) |
| `POST /sessions/:sessionId/cancel` `{queryId?}` | Abort a turn (or all turns of the session) via its `AbortController` | Runner `Stop` |
| `POST /sessions/:sessionId/load-conversation` `{messages}` | Seed persisted history into the harness on resume/restore | Runner after Resume |
| `POST /sessions/:sessionId/reset-conversation` | Clear history (phase transition) | Runner |
| `GET /workspace/files`, `GET /workspace/files/*` | List/download workspace files (+ folder slurp for artifacts/user images — [06](06-artifacts.md)) | host artifact extraction |
| `POST /workspace/scan-secrets` | Scan workspace for secrets | host publish flow |
| `POST /workspace/snapshot`, `POST /workspace/diff` | Filesystem metadata snapshot/diff | host (optional) |

All routing is session-scoped: there are no flat single-session routes. `POST
/sessions/:id/query-stream` lazily creates the session with defaults if it does not yet exist, so a
turn can arrive before an explicit `POST /sessions`.

### The `query-stream` request shape

`QueryRequest` (`sandbox/src/types/index.ts`, validated with Zod):

```jsonc
{
  "prompt": "user message",
  "systemPrompt": "optional; org context appended by the host before sending",
  "tools": ["render_table", "Bash", "WebSearch"],   // allowlist; empty = SDK defaults + all plugins
  "model": "claude-opus-4-...",
  "maxTurns": 100,
  "harness": "claude-agent-sdk",                     // only honoured on first (lazy) create
  "attachments": [{ "mimeType": "...", "base64Content": "...", "fileName": "..." }]
}
```

The response is `Content-Type: text/event-stream`; frames are `event: <type>\ndata: <json>\n\n`. The
first frame is a `connected` event carrying the generated `queryId` so the caller can later reconnect
via `GET /sessions/:id/stream/:queryId`. A `heartbeat` event fires every 15s. The event vocabulary is
canonical and shared with Go/web ([05](05-event-streaming.md)).

### Configuration (env injected by the engine)

The `ExecutionEnvironment` injects these as `ProvisionSpec.Env`. Parsed and defaulted in
`sandbox/src/config.ts`:

| Env | Meaning |
|-----|---------|
| `PORT` (3010) | The agent's HTTP port — the `AgentPort` in `ProvisionSpec` |
| `HOST` (0.0.0.0) | Bind address |
| `ANTHROPIC_BASE_URL` | **Set** → the host's model proxy (no real key in the sandbox; per-session proxy header engages — §3). **Unset** → talk to the Anthropic API directly with whatever credential is in the process env |
| `HOST_API_URL` | Host API base for tool callbacks (table data, dashboards, artifact registration) |
| `SESSION_ID`, `SESSION_TOKEN` | **Optional.** Per-session identity is passed per-request in multi-session mode; these remain only for back-compat with single-session deployments where the container serves exactly one session |
| `SESSION_CUSTOMER`, `SESSION_JOB` | Opaque host context; consumed by product tool plugins |
| `PRODUCT_PLUGINS_DIR` | Directory of product tool-plugin modules loaded at startup (§5). Empty = builtins only |
| `DEFAULT_MODEL`, `DEFAULT_MAX_TURNS`, `DEFAULT_THINKING_BUDGET_TOKENS` | Model defaults |
| `LOG_LEVEL` | Pino log level |

The **model proxy** — which injects the real API key and rewrites model IDs — is a **host concern**,
not part of the in-image agent: it is whatever `ANTHROPIC_BASE_URL` points at (a small host-side
proxy or a gateway the host already runs). When it is set, the image ships with only a placeholder
key; the proxy supplies the real one.

---

## 3. Multi-session correctness: routing, abort, and the per-session proxy header

The sandbox hosts N concurrent sessions in one process, so state that would be process-global in a
single-session design moves into the session/turn layer. `SessionManager`
(`sandbox/src/services/session-manager.ts`) keys everything by session ID:

- **Routing & state:** each session record holds its harness instance and a `Map<queryId,
  {abort}>`. **One active turn per session** — `startTurn()` aborts and supersedes any prior turn
  before registering the new one; cross-session turns run fully in parallel. Cancelling session A
  never touches session B. `destroy()` aborts all turns, disposes the harness, and calls
  `streamService.closeSession(sessionId)` to free that session's replay buffers.
- **The outbound proxy header (the tricky part):** the sandbox patches `globalThis.fetch` to stamp
  `x-session-id` on calls to `ANTHROPIC_BASE_URL`, so the host's model proxy can route each call to
  the right per-session context. With N sessions in one process a single global value cannot know
  *which* session is calling. The fix is **`AsyncLocalStorage`** (`sandbox/src/session-context.ts`):
  the control server runs each turn inside `sessionContext.run({sessionId}, () =>
  harness.runTurn(...))`, and the patched `fetch` reads `sessionContext.getStore()?.sessionId`
  (falling back to `config.SESSION_ID` for single-session mode). `ANTHROPIC_BASE_URL` stays
  process-level; only the header is per-session. CLI harnesses instead get the session ID via spawn
  env, so this trick is only needed for *in-process* harnesses like the SDK.
- The fetch patch is installed only when `ANTHROPIC_BASE_URL` is set (see `index.ts`); in
  direct-API mode there is no proxy to route to.

`StreamService` (`sandbox/src/services/stream-service.ts`) delivers SSE, keeps a bounded replay
buffer, and coalesces `tool_input_delta`. Streams are keyed by `${sessionId}:${queryId}` so
`closeSession` can cascade-close all of a session's streams on destroy.

---

## 4. The harness seam: pluggable agentic frameworks, selected per session

> A **harness** (a.k.a. agentic framework) is the thing that actually drives a model through an
> agent turn: the Claude Agent SDK, the Claude CLI, the Gemini CLI, Codex, etc. Agent Orange treats
> the harness as a **first-class, per-session choice**, not a hard-wired implementation.

The naïve alternative — a different sandbox image per framework, times each execution environment —
is a combinatorial explosion. The seam avoids it with one rule:

> **All supported harness binaries are baked into the base image.** Any container from that image can
> run any harness it baked in. The harness is chosen *per session, at runtime*, by name.

So there is no execution-environment × harness matrix: the execution environment only places and
isolates compute; the **image** carries the harness software; the **request** names which harness to
boot. Adding a harness = (a) bake its binary into the base image, (b) ship an adapter, (c) register
it. Zero changes to the execution environment, the fleet, or the Go core.

### The interfaces (`sandbox/src/harness/`)

- **`Harness`** (`harness.ts`) — one **stateful instance per session**. `runTurn(req, ctx)` runs one
  turn, emitting all events through `ctx.emit` and honouring `ctx.signal` for cancellation;
  `loadConversation()` / `resetConversation()` manage its instance-local history; optional
  `dispose()` for teardown. The harness **never** touches `StreamService` directly.
- **`TurnContext`** — what the control server hands each `runTurn`: `sessionId`, `queryId`, an
  `AbortSignal` (the control server owns cancellation), a `HarnessEmitter` pre-bound to
  (sessionId, queryId), the turn's `ResolvedTools` (from the tool registry — §5), and `config`.
- **`HarnessEmitter`** / `BoundHarnessEmitter` — the typed SSE surface (messageStart/contentDelta/
  thinkingDelta/messageEnd, toolUseStart/End/Progress, toolInputDelta, hookEvent, subagentEvent,
  activityUpdate, systemStatus, sessionInfo, generic `event(type,data)`, `endQuery`, `error`). The
  bound implementation delegates to `StreamService` with the IDs already applied.
- **`HarnessDescriptor`** + **`HarnessRegistry`** (`registry.ts`) — a descriptor is `{name,
  credentials, create(sessionId)}`; the registry maps names to descriptors and answers `has` / `get`
  / `names`. `DEFAULT_HARNESS = "claude-agent-sdk"`.
- **`HarnessCredentialSpec`** — `{requiredEnv[], anyOfEnv?[], describe()}`. `checkCredentials()`
  requires every `requiredEnv` var and, if `anyOfEnv` is present, at least one of that group (an
  unsatisfied group surfaces as one synthetic `"one of: A, B, C"` entry).

The registry is constructed once in `bootstrap.ts` (a separate module so routes can import it without
pulling in `index.ts`'s server side effects). `bootstrap.ts` also exports `resolveHarness(name)`,
which validates the name and its credentials and returns either the descriptor or a typed HTTP error.

### Session-start: harness selection + credential pre-check

When a session is created, `SessionManager.create()` calls `resolveHarness()`:

1. Resolve `harness = req.harness || DEFAULT_HARNESS`.
2. If the registry doesn't have it → `400 { code: "UNKNOWN_HARNESS", supported: [...] }`
   (`UnknownHarnessError`).
3. If a required/anyOf credential is missing → `424 { code: "HARNESS_CREDENTIALS_MISSING", message:
   describe(), missing: [...] }` (`HarnessCredentialsMissingError`).
4. Otherwise `descriptor.create(sessionId)` and store the instance on the session record.

This is the locked behaviour: a sandbox that physically cannot run the requested harness (no creds)
refuses **the session**, not the turn. `424 Failed Dependency` is the chosen code for missing creds.

### The one shipped harness, and future ones

`ClaudeAgentSdkHarness` (`claude-agent-sdk.ts`) is the SDK `query()` loop with its Pre/PostToolUse
hooks, conversation history, and attachment processing behind the interface: it emits via `ctx.emit`,
honours `ctx.signal`, and keeps history instance-local. It builds the SDK options block from
`ctx.resolved` — `model` (defaulting to `config.DEFAULT_MODEL`), `disallowedTools`, `mcpServers`,
`permissionMode: 'bypassPermissions'`. Its credential spec accepts **any one** model credential:

```ts
credentials: {
  requiredEnv: [],
  anyOfEnv: ['ANTHROPIC_BASE_URL', 'CLAUDE_CODE_OAUTH_TOKEN', 'ANTHROPIC_API_KEY'],
  describe: () => 'Claude Agent SDK needs a model credential: ANTHROPIC_BASE_URL (host model ' +
    'proxy), CLAUDE_CODE_OAUTH_TOKEN (subscription), or ANTHROPIC_API_KEY (direct API)',
}
```

| Harness | Binary (baked in base image) | Adapter drives it via | Credentials |
|---------|------------------------------|------------------------|-------------|
| `claude-agent-sdk` (now) | `@anthropic-ai/claude-agent-sdk` (npm) | in-process `query()` | one of base-URL / OAuth / API key |
| `claude-cli` (future) | `claude` CLI | `child_process` + `--output-format stream-json` | proxy/key env |
| `gemini-cli` (future) | `gemini` CLI | `child_process` stream | `GEMINI_API_KEY` (or proxy) |
| `codex` (future) | `codex` CLI | `child_process` stream | OpenAI key (or proxy) |

Future CLI adapters translate their binary's native event stream into our SSE vocabulary
([05](05-event-streaming.md)) and receive the session ID via spawn env.

### The Go contract (host side)

`agentkit.go` carries a `Harness` string type (`HarnessClaudeAgentSDK` default, plus `…ClaudeCLI`,
`…GeminiCLI`, `…Codex`) and a per-session `Harness` field on `CreateSessionRequest` (empty ⇒ default).
Harness is fixed at session creation, so `CreateSession` sends it in the `POST /sessions` body after
Provision + health; `SendMessage`/`Stream`/`Stop` use the session-scoped paths and never re-send it.
The `UNKNOWN_HARNESS` / `HARNESS_CREDENTIALS_MISSING` responses map to a typed Go error so the host
can clean up the orphan session row.

---

## 5. The tool registry: internal vs app-handled tools, and the plugin seam

Tools are where a generic runtime becomes a *specific* product. The library ships a small set of
generic tools and a **plugin seam** so a host product registers its own.

### Two kinds of tools

- **Internal tools** run *inside the sandbox* and execute for real: `Bash`, `Glob`, `Grep`, `Read`,
  `Skill`, plus any binaries baked into the image (a product's CLI, discovered on `PATH`). These are
  the SDK's own tools; the agent's real effects on the workspace come from them. *Which* internal
  tools exist is an **image** decision, not a runtime one.
- **App-handled tools** do **not** execute in the sandbox. They return a **marker payload** that the
  PostToolUse hook intercepts and turns into an SSE event for the host/UI to act on. They are how the
  agent "speaks to the application."

### The marker pattern

An app-handled tool returns text containing a JSON marker (e.g. `{"__ask_user": true, ...}`); the
PostToolUse hook detects it, emits the corresponding SSE event, and **replaces the model-visible
output** with a compact form so the model sees clean text instead of a huge JSON blob. This keeps
token cost down and decouples "the agent asked to render X" from "the app renders X". Crucially the
mapping is **data, not branches**: each plugin declares a `MarkerSpec` and the PostToolUse hook in
`claude-agent-sdk.ts` iterates registered markers (`resolved.markers`) rather than a hard-coded
`if/else` ladder.

```ts
// sandbox/src/tools/registry.ts
export interface MarkerSpec {
  key: string;                              // "__ask_user"
  event: string;                            // the extension SSE event type to emit
  toEvent(payload: any): Record<string, unknown>;   // marker payload → SSE event data
  toModelText(payload: any): string;                // marker payload → compact text the model sees
}
export interface ToolPlugin {
  name: string;                             // "ask_user"
  sdkTool: SdkMcpToolDefinition<any>;       // the MCP tool definition (args schema + handler)
  marker?: MarkerSpec;                      // optional — some tools return content directly
}
```

### The registry and `resolve()`

```ts
export interface ToolRegistry {
  builtins(): ToolPlugin[];                 // ask_user, write_file, view_image, screenshot_url
  register(p: ToolPlugin): void;            // product plugins
  resolve(allowed?: string[]): ResolvedTools;
}
```

`DefaultToolRegistry` (`registry-impl.ts`) is the singleton exported as `toolRegistry`. `resolve()`
is the single place that builds a turn's SDK options: it assembles one MCP server (`createSdkMcpServer`,
name `ui`) from all plugin `sdkTool`s, maps short tool names to the SDK's `mcp__ui__<name>` prefix,
resolves the request's allowlist (empty → SDK defaults `Bash`/`WebSearch`/`WebFetch` + all plugin
tools + `Skill`), and collects every plugin's `MarkerSpec`. Library defaults:
`disallowedTools = ['Task', 'Write']` (Task = sub-agents; Write is replaced by the safer `write_file`
builtin) and `permissionMode: 'bypassPermissions'`.

### The four builtins

The generic core ships the app-handled tools *every* product needs
(`sandbox/src/tools/builtin/`):

- **`ask_user`** — the canonical app-handled tool. Emits an `__ask_user` marker; the UI shows option
  buttons and the answer returns as the next turn.
- **`write_file`** — writes to `/workspace` and emits an inline `__artifact_registered` marker so
  files auto-register as artifacts ([06](06-artifacts.md)).
- **`view_image`**, **`screenshot_url`** — return image content blocks directly (no marker).

### Product plugins (`PRODUCT_PLUGINS_DIR`)

At startup `index.ts` calls `loadProductPlugins(config.PRODUCT_PLUGINS_DIR, toolRegistry)`
(`load-plugins.ts`): it scans the directory, dynamically imports each `*.js/*.ts` module (skipping
`.test.`/`.spec.`), extracts any exported `ToolPlugin`s (via `default`/`plugin`/`plugins`/
`toolPlugins`, deduped by `name`), and registers them. So a product supplies its character by:

1. **An image** with the internal tools/binaries it needs, its `CLAUDE.md`, and skills (build-time —
   [03](03-image-registry.md)).
2. **Tool plugins** in `PRODUCT_PLUGINS_DIR` for its app-handled tools + markers.
3. **Render plugins** in the browser for those markers' event types ([05](05-event-streaming.md#rendering-in-the-web-package)).
4. Optionally, **host hooks** for marker side-effects (e.g. an artifact-registration marker →
   `ArtifactStore`).

> **Example.** The original TypeScript host (now `migration-reference/`) supplied a `render_table` /
> `render_chart` / `create_dashboard` / `generate_pptx` bundle plus a `pt` CLI baked into its image —
> precisely such a plugin bundle. In Agent Orange those live *with the product*, not in the core; the
> library names no product tools.

---

## 6. Forward gaps

**Per-session MCP config is not yet plumbed (spec §4, gap G1).** The sandbox currently resolves tools
**only** from its in-image registry — the four builtins plus whatever `PRODUCT_PLUGINS_DIR` loads.
There is no way for the *host* to hand a session a set of MCP servers at create time:
`CreateSessionRequest` has no MCP surface. The spec's §4 closes this by adding
`MCPServers map[string]MCPServerConfig` to `CreateSessionRequest`, extending the `POST /sessions`
payload with a snake_case `mcp_servers`, storing it on the session record, and merging those servers
(never replacing) with the in-image registry when a turn resolves its tools. `Env`/`Headers` values
support a single whole-value `${VAR}` substitution resolved from the container env at MCP-process
spawn, failing loudly on an unset variable. See `docs/17-product-spec.md` §4 for the full design.
