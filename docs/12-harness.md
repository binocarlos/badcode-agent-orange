# 12 — The harness seam: pluggable agentic frameworks, selected per session

> **Definition.** A **harness** (a.k.a. agentic framework) is the thing that actually drives a model
> through an agent turn: the Claude Agent SDK, the Claude CLI, the Gemini CLI, Codex, etc. agentkit
> treats the harness as a **first-class, per-session choice**, not a hard-wired implementation.

This is the largest change from the v0 design, where the in-image agent was hard-wired to the Claude
Agent SDK. The motivation and the shape are below.

## Why a harness seam

The old assumption — "the sandbox *is* the Claude Agent SDK" — does not scale to the goal of running
different agentic frameworks (and, later, different vendors) on the same infrastructure. The naïve
alternative — a different sandbox image per framework, times each execution environment — is a
combinatorial explosion. agentkit avoids it with one rule:

> **All supported harness binaries are baked into the base image.** Any container started from that
> image can run any harness it baked in. The harness is chosen *per session, at runtime*, by name.

So there is no execution-environment × harness matrix: the execution environment ([02](02-execution-environment.md))
only places and isolates compute; the **image** carries the harness software; the **request** names which
harness to boot. Adding a harness = (a) bake its binary into the base image + (b) ship an adapter +
(c) register it. Zero changes to the execution environment, the fleet, or the Go core.

## The two parts of the sandbox

The in-image agent ([07](07-in-image-agent.md)) splits into two cleanly separated parts:

1. **The control server (harness-agnostic, multi-session).** A long-running HTTP server that owns:
   session lifecycle + routing by session ID, the SSE event stream + replay buffer, the tool-plugin
   seam ([08](08-tool-registry.md)), and the credential/proxy plumbing. It knows *nothing* about any
   specific framework — only the `Harness` interface and a `HarnessRegistry`.
2. **Harness adapters.** One `Harness` instance per session. `ClaudeAgentSdkHarness` is today's
   `agent-service.ts` `runQuery` body lifted behind the interface. Future adapters
   (`ClaudeCliHarness`, `GeminiCliHarness`, `CodexHarness`) drive their respective binaries and
   translate the binary's native event stream into our SSE vocabulary ([05](05-event-streaming.md)).

## The `Harness` adapter interface (TypeScript, in-image)

New files under `sandbox/src/harness/`:

```ts
// harness.ts

// What credentials/config a harness needs — checked at session-start, before booting.
export interface HarnessCredentialSpec {
  requiredEnv: string[];      // env vars that must be non-empty for this harness to run
  describe(): string;         // human-readable note surfaced in the start-session error if missing
}

// Per-turn context handed to the harness by the control server.
export interface TurnContext {
  sessionId: string;
  queryId: string;
  signal: AbortSignal;        // the control server owns cancellation; the harness honours this
  emit: HarnessEmitter;       // typed SSE surface, pre-bound to (sessionId, queryId)
  resolved: ResolvedTools;    // from tools/registry resolve(request.tools)
  config: SandboxConfig;      // proxy URL, host API URL, defaults
}

// The typed event surface — the harness NEVER touches StreamService directly.
// (messageStart/contentDelta/thinkingDelta/messageEnd, toolUseStart/End/Progress,
//  toolInputDelta, hookEvent, subagentEvent, activityUpdate, systemStatus, sessionInfo,
//  event(type,data) for extension/plugin events, endQuery(...), error(code,message).)
export interface HarnessEmitter { /* … see events vocabulary … */ }

// One instance per session (stateful: owns its own conversation + per-turn abort wiring).
export interface Harness {
  readonly name: string;                       // "claude-agent-sdk" | "claude-cli" | …
  runTurn(req: QueryRequest, ctx: TurnContext): Promise<void>;  // one turn; honours ctx.signal
  loadConversation(messages: Array<{ role: "user" | "assistant"; content: string }>): void;
  resetConversation(): void;
  dispose?(): Promise<void>;                   // graceful teardown on session destroy
}

// registry.ts
export interface HarnessDescriptor {
  name: string;
  credentials: HarnessCredentialSpec;
  create(sessionId: string): Harness;          // fresh per-session instance
}

export class HarnessRegistry {
  register(d: HarnessDescriptor): void;
  has(name: string): boolean;
  get(name: string): HarnessDescriptor | undefined;
  names(): string[];
}

export const DEFAULT_HARNESS = "claude-agent-sdk";
```

`ClaudeAgentSdkHarness` (`sandbox/src/harness/claude-agent-sdk.ts`) is today's `runQuery` body with
three mechanical substitutions:

1. `streamService.sendX(queryId, …)` → `ctx.emit.X(…)` (sessionId/queryId already bound).
2. `this.abortController` / supersede logic → `ctx.signal` (the control server owns abort).
3. `this.conversationHistory` stays instance-local to the harness (already instance-shaped).

Its credential spec: `{ requiredEnv: ["ANTHROPIC_BASE_URL"], describe: () => "Claude Agent SDK needs
ANTHROPIC_BASE_URL (the host model proxy)" }`. (The API key itself is a placeholder satisfied by the
proxy — see [07](07-in-image-agent.md) — so the real required signal is the proxy URL.)

## Session-start: harness selection + the credential pre-check

When a session is created (`POST /sessions`, see [07](07-in-image-agent.md)) the control server:

1. Resolves `harness = req.harness || DEFAULT_HARNESS`.
2. If `!registry.has(harness)` → `400 { code: "UNKNOWN_HARNESS", supported: registry.names() }`.
3. `desc = registry.get(harness)`; if any `desc.credentials.requiredEnv[k]` is empty →
   `424 { code: "HARNESS_CREDENTIALS_MISSING", message: desc.credentials.describe(), missing: [...] }`.
4. Otherwise `desc.create(sessionId)` and store on the session.

This is the locked behaviour: a sandbox that physically cannot run the requested harness (no creds)
refuses **the session**, not the turn. The static per-harness boot code lives in the image; the
control server just dispatches by name and surfaces a clean error.

## What each future harness needs

| Harness | Binary (baked in base image) | Adapter drives it via | Credentials (`requiredEnv`) |
|---------|------------------------------|------------------------|------------------------------|
| `claude-agent-sdk` (now) | `@anthropic-ai/claude-agent-sdk` (npm) | in-process `query()` | `ANTHROPIC_BASE_URL` (proxy) |
| `claude-cli` (future) | `claude` CLI | `child_process` + `--output-format stream-json` | proxy/key env |
| `gemini-cli` (future) | `gemini` CLI | `child_process` stream | `GEMINI_API_KEY` (or proxy) |
| `codex` (future) | `codex` CLI | `child_process` stream | OpenAI key (or proxy) |

For CLI harnesses, the session ID is passed via spawn env to the child process (so the
per-session-proxy-header AsyncLocalStorage trick — [07](07-in-image-agent.md) — is only needed for
*in-process* harnesses like the SDK).

## The Go contract (host side)

`agentkit.go` gains a `Harness` type and a per-session field (additive, back-compatible):

```go
type Harness string
const (
    HarnessClaudeAgentSDK Harness = "claude-agent-sdk" // default when empty
    HarnessClaudeCLI      Harness = "claude-cli"
    HarnessGeminiCLI      Harness = "gemini-cli"
    HarnessCodex          Harness = "codex"
)

// CreateSessionRequest gains:
Harness Harness // empty => HarnessClaudeAgentSDK (sandbox default)
```

Harness is **per session** (fixed at creation), so it is passed at session-create time, not per
message:

- `CreateSession` (`runner.go`) calls `POST {addr}/sessions` after Provision + health, with
  `{ sessionId, harness, model, maxTurns }`. The credential/unknown errors map to a typed Go
  `ErrHarnessUnavailable`, so the host can clean up the orphan session row.
- `SendMessage`/`Stream`/`Stop` use the session-scoped paths (`/sessions/:id/...`); harness is not in
  the message body (it is session state).
- Roll-out order: ship the sandbox `/sessions` route + back-compat shims first, then flip the Runner.
- Later: surface `Harness` up to goapi's `CreateAgentSessionRequest`. Empty ⇒ default everywhere.

## Risks / open decisions

- **Error code for missing creds:** `424 Failed Dependency` (recommended) vs `400`. Documented as 424.
- **Per-session credential isolation:** in multi-tenant (shared) mode, a single sandbox process holds
  env for the harnesses it supports; per-session secrets must ride per-request (not process env) — see
  the scoped-token decision in [04](04-session-orchestration.md).
- The single-active-turn-per-session rule (mechanism keyed by queryId) lives in the control server, not
  the harness — see [07](07-in-image-agent.md).

## Mapping: today → library

| Today (`agent-library/sandbox/src/`) | Library (revised) | Disposition |
|---|---|---|
| `services/agent-service.ts` (`runQuery`, SDK `query()` loop, hooks) | `harness/claude-agent-sdk.ts` | lift behind `Harness`; emit via `ctx.emit` |
| `services/agent-service.ts` (singleton, abort, conversation) | `services/session-manager.ts` + per-harness state | dissolve; abort moves to control server |
| (none) | `harness/{harness,registry}.ts` | new seam |
| `contract.ts` `QueryRequest` | `+ harness?` on `POST /sessions` | additive |
