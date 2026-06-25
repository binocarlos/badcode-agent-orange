# 07 — The in-image agent: a multi-session, harness-agnostic control server

Something has to run *inside* the container, hold sessions, and actually drive the model. That is the
`sandbox/` package — a long-running server (TypeScript today) that the `ExecutionEnvironment`
provisions and the Go `Runner` talks to over HTTP/SSE. It is the **only** code that must live in the
image, and it knows nothing about Docker, suspend/resume, archives, or Azure — those are the host's job.

The sandbox has **two parts** (the harness seam is its own doc — [12](12-harness.md)):

1. **The control server (harness-agnostic, multi-session)** — this doc. It owns session lifecycle +
   routing by session ID, the SSE event stream + replay buffer, the tool-plugin seam
   ([08](08-tool-registry.md)), and the credential/proxy plumbing.
2. **Harness adapters** — [12](12-harness.md). One `Harness` per session (e.g. `ClaudeAgentSdkHarness`);
   selected by name in the start-session request.

## Its single responsibility

> Host **N concurrent sessions** keyed by session ID; for each, boot the **harness** named in its
> start request and run one turn at a time, emitting an SSE event stream with a replay buffer so a
> consumer can attach late or reconnect.

The sandbox is **always multi-session-capable** and never limits session count — the
**execution environment** decides whether to route more than one session to it ([02](02-execution-environment.md)).
Everything the orchestrator used to wrap around it (lifecycle, persistence, networking policy) moved up
to Go; *which framework drives the model* moved behind the harness seam.

## The sandbox HTTP contract

This is the stable boundary between the Go `Runner` and the image. The `ExecutionEnvironment` reports
an `Address`; the Runner appends these **session-scoped** paths. (Generalised from `agent/src/routes/`
to a multi-session model.) Every response carries an `X-Session-Id` header.

| Method · Path | Purpose | Called by |
|---|---|---|
| `POST /sessions` `{sessionId, harness?, model?, maxTurns?}` | **Create a session**: boot the named harness + credential pre-check ([12](12-harness.md)). Idempotent if it already exists with the same harness | Runner `CreateSession` |
| `DELETE /sessions/:sessionId` | Tear down a session in-process (abort its turns, dispose harness, free maps) | Runner `Destroy` (shared tenancy) |
| `GET /health` | Liveness; reports `{status, sessions:[...]}` | Runner health checks, Resume wait |
| `POST /sessions/:sessionId/query-stream` | Submit a turn **and** stream its SSE in one response (no race) | Runner `SendMessage` |
| `GET /sessions/:sessionId/stream/:queryId` | Attach to a query's stream; **replays the in-image buffer** then live | Runner `Stream` (reconnect) |
| `POST /sessions/:sessionId/cancel` `{queryId?}` | Abort a turn (or all turns of the session) via its `AbortController` | Runner `Stop` |
| `POST /sessions/:sessionId/load-conversation` | Load persisted history on resume/restore | Runner after Resume |
| `POST /sessions/:sessionId/reset-conversation` | Clear history (phase transition) | Runner |
| `GET /workspace/files`, `GET /workspace/files/*` | List/download workspace files (+ folder slurp for artifacts/user images — [06](06-artifacts.md)) | host artifact extraction |
| `POST /workspace/scan-secrets` | Scan workspace for secrets | host publish flow |
| `POST /workspace/snapshot`, `POST /workspace/diff` | Filesystem metadata snapshot/diff | host (optional) |

**Back-compat shims (migration window).** The flat v0 routes (`POST /query-stream`, `GET
/stream/:queryId`, `POST /cancel`, `POST /load-conversation`, `POST /reset-conversation`) remain,
resolving the session ID from an `X-Session-Id` header or, failing that, `config.SESSION_ID`. This lets
the Go side flip to session-scoped paths independently. Sequence the rollout: ship sandbox shims first,
then flip the Runner. Remove the shims once the Runner posts session-scoped paths.

### `POST /query-stream` request shape

```jsonc
{
  "prompt": "user message",
  "systemPrompt": "optional; org context appended by the host before sending",
  "tools": ["render_table", "Bash", "WebSearch"],   // allowlist; empty = all
  "model": "claude-opus-4-...",
  "maxTurns": 100,
  "planMode": "none | suggest | require",
  "attachments": [{ "mimeType": "...", "base64Content": "...", "fileName": "..." }]
}
```

Response: `Content-Type: text/event-stream`, frames `event: <type>\ndata: <json>\n\n`.

## How it's configured (env in, from the engine)

The `ExecutionEnvironment` injects these as `ProvisionSpec.Env` (today set by `sandbox-manager.ts`).
The library standardises the names under an `AGENTKIT_`-ish convention but keeps the current ones as
aliases during migration:

| Env | Meaning |
|-----|---------|
| `SESSION_ID` | The session this instance serves (and multiplex key in shared-container mode) |
| `SESSION_TOKEN` | Scoped token for calling back to the host API |
| `ANTHROPIC_BASE_URL` | Where the SDK sends model calls — the host's model proxy (key injection) |
| `DEFAULT_MODEL`, `DEFAULT_MAX_TURNS`, `DEFAULT_THINKING_BUDGET_TOKENS` | Model defaults |
| `HOST_API_URL` (was `GOAPI_URL`) | Host API base for tool callbacks (table data, dashboards) |
| `PORT` (3010) | The agent's HTTP port — the `AgentPort` in `ProvisionSpec` |
| host-context env (`SESSION_CUSTOMER`, `SESSION_JOB`, …) | Opaque to the core; consumed by host tool plugins |

The model proxy (`orchestrator/src/proxy.ts`) — which injects the real API key and rewrites model IDs
— is a **host concern**, not part of the in-image agent. In the library it becomes a small host-side
proxy the `ANTHROPIC_BASE_URL` points at (or a registry/gateway the host already runs). The image
ships with a placeholder key, exactly as today.

## What's generic vs Platinum-specific inside the image

From the in-image map, the split is clean:

**Generic core (`sandbox/src/` — copies cleanly):**
- `SessionManager` — the multi-session state holder: `Map<sessionId, {harness, turns, …}>`, session
  create/destroy, per-session abort. (New; absorbs `AgentService`'s session-owning role.)
- The `Harness` seam — the SDK `query()` loop + Pre/PostToolUse hooks + conversation history +
  attachment processing move **behind `ClaudeAgentSdkHarness`** ([12](12-harness.md)); the control
  server is harness-agnostic.
- `StreamService` — SSE delivery, the 2000-event replay buffer, `tool_input_delta` coalescing, the
  typed emitters. Keyed by `${sessionId}:${queryId}` (composite) + a `closeSession` cascade.
- The Fastify server + session-scoped routes (the contract above), config (Zod), health.
- The **generic tools**: `ask_user`, `write_file`, `view_image`, `screenshot_url`.

**Platinum-specific (becomes tool plugins — see [08](08-tool-registry.md)):**
- `render_table`, `render_tables`, `render_chart`, `create_dashboard`, `generate_pptx` — they call the
  host API and emit `__render_table`/`__render_chart`/`__dashboard_created` markers.
- The PostToolUse **marker interception** for those types.
- The `pt` CLI (a Platinum binary baked into the Platinum image).

The refactor inside the image is therefore small: lift the Platinum tools and their marker handlers
out of `ui-tools.ts`/`agent-service.ts` into a **tool-plugin registry** that the generic
`AgentService` consumes. The generic core ships with the four generic tools; a product registers its
own.

## Why the control server is TypeScript (and why that is not a vendoring decision)

The control server's language is, in principle, independent of the harness — CLI harnesses (Claude
CLI, Gemini, Codex) are separate binaries the adapter shells out to, so the server could be any
language. We keep it TypeScript because:

- It already exists and is thin (~1,500 LOC of generic plumbing once the Platinum tools leave).
- The **Claude Agent SDK harness** is TypeScript-first (`query()`, hooks, `createSdkMcpServer` are
  SDK-native); since at least one first-class harness runs in-process in TS, hosting the control server
  in TS keeps that adapter zero-overhead. CLI harnesses just spawn child processes.
- It runs *inside* the image regardless of engine, so its language is independent of the host's (Go).

## Multi-session correctness: routing, abort, and the per-session proxy header

The sandbox hosts N concurrent sessions, so a few things that were process-global in v0 move into the
session/turn layer:

- **Routing & state:** `SessionManager` keys everything by session ID; each session has its own
  conversation, harness instance, and `AbortController`-per-turn. **One active turn per session**
  (a new turn supersedes/queues the prior); cross-session turns run fully in parallel. Cancelling
  session A never touches session B.
- **The outbound proxy header (the tricky part):** v0 patched `globalThis.fetch` to stamp
  `x-session-id` from the single `config.SESSION_ID`. With N sessions in one process, a global patch
  cannot know *which* session is making a model call. The fix is **`AsyncLocalStorage`**: the control
  server runs each turn inside `sessionContext.run({sessionId}, () => harness.runTurn(...))`, and the
  patched `fetch` reads `sessionContext.getStore()?.sessionId`. (CLI harnesses get the session ID via
  spawn env instead.) `ANTHROPIC_BASE_URL` stays process-level (the proxy is shared; per-session
  routing is the header). This must be load-tested — two sessions hitting distinct mock proxies, each
  asserting its own header.
- **Env:** `SESSION_ID`/`SESSION_TOKEN` move from required → optional (they are per-session now, passed
  per-request, not process env). `ANTHROPIC_BASE_URL` stays required.

## Mapping: today → library

| Today (`agent/src/`) | Library (`sandbox/src/`) | Disposition |
|---|---|---|
| `index.ts`, `config.ts` | same | copy; `SESSION_ID/TOKEN` → optional; `fetch` patch → AsyncLocalStorage |
| `routes/{agent,health,workspace}.ts` | session-scoped routes + `routes/sessions.ts` + shims | generalise to multi-session |
| `services/agent-service.ts` (session owner, abort) | `services/session-manager.ts` | new; dissolve the singleton |
| `services/agent-service.ts` (SDK `query()` loop, hooks) | `harness/claude-agent-sdk.ts` | lift behind `Harness` ([12](12-harness.md)) |
| (none) | `harness/{harness,registry}.ts` | new harness seam |
| `services/stream-service.ts` | same | copy; composite key `${sessionId}:${queryId}` + `closeSession` |
| `services/attachment-prompt.ts` | same | copy verbatim |
| `mcp/ui-tools.ts` (generic tools) | `tools/builtin/*` | copy `ask_user`/`write_file`/`view_image`/`screenshot_url` |
| `mcp/ui-tools.ts` (render_table, dashboards, pptx) | **host plugin package** | extract as Platinum tool-plugin (lives with Platinum, not the library) |
| `types/index.ts` SSE types | mirror of `events/events.go` | generated/mirrored |
