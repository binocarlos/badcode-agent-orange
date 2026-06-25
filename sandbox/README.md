# @agentkit/sandbox — the in-image agent

The thin TypeScript server that runs **inside** each container image. It receives "run this agent
turn," drives the Claude Agent SDK, and emits an SSE event stream with a replay buffer. It knows
nothing about Docker, suspend/resume, archives, or storage — those are the Go host's job now
([../docs/04-session-orchestration.md](../docs/04-session-orchestration.md)).

Full design: [../docs/07-in-image-agent.md](../docs/07-in-image-agent.md).

## Status

**Scaffold.** This directory defines the package shape, the HTTP/SSE **contract** the Go `Runner`
speaks to (`src/contract.ts`), and the **tool-plugin seam** (`src/tools/registry.ts`). The bulk of
the implementation is a near-verbatim copy of Platinum's `agent/src/`, performed per the provenance
map ([../docs/90-provenance-map.md](../docs/90-provenance-map.md)) in a later pass:

| To copy from `agent/src/` | Into | Disposition |
|---|---|---|
| `index.ts`, `config.ts`, `routes/{agent,health,workspace}.ts` | `src/` | copy (env-name aliases) |
| `services/agent-service.ts` | `src/services/agent-service.ts` | copy; Platinum marker `if/else` → plugin dispatch |
| `services/stream-service.ts` | `src/services/stream-service.ts` | copy verbatim |
| `services/attachment-prompt.ts` | `src/services/attachment-prompt.ts` | copy verbatim |
| `mcp/ui-tools.ts` (ask_user, write_file, view_image, screenshot_url) | `src/tools/builtin/` | copy (generic tools) |
| `mcp/ui-tools.ts` (render_table, dashboards, pptx) | — | **stays in Platinum** as a tool-plugin bundle |

## The contract (what the Go Runner calls)

| Method · Path | Purpose |
|---|---|
| `GET /health` | liveness + reports `sessionId` |
| `POST /query-stream` | submit a turn + stream its SSE response |
| `GET /stream/:queryId` | attach to a query stream (replays buffer, then live) |
| `POST /cancel` | abort the in-flight query |
| `POST /load-conversation` | load history on resume/restore |
| `POST /reset-conversation` | clear history |
| `GET /workspace/files[...]`, `POST /workspace/scan-secrets` | workspace ops |

See `src/contract.ts` for the request/response types and the SSE event vocabulary (mirrors
`../go/events/events.go`, the canonical source).

## What a product supplies

- An **image** with its internal tools/binaries + `CLAUDE.md` + `.claude/skills/` (build-time).
- **Tool plugins** (`ToolPlugin`) for its app-handled tools + markers.
See [../docs/08-tool-registry.md](../docs/08-tool-registry.md).
