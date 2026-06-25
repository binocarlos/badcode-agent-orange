# @agentkit/chat-ui — the rendering package

React components that turn an agent event stream into a polished chat. The crown jewel is the
**single `agentEventReducer`** — one pure `(state, event) => state` function that reconstructs the UI
identically for live streaming and replayed sessions (Platinum CLAUDE.md rule 12). The library treats
any second reconstruction path as a bug.

Full design: [../docs/09-frontend-components.md](../docs/09-frontend-components.md).

## Status

**Scaffold.** This directory defines the package shape and the **render-plugin seam**
(`src/plugins.ts`) — the boundary that keeps the reducer/components generic while Platinum's
Carbon table/chart/dashboard widgets register as plugins. The components themselves are copied from
Platinum's `frontend/src/` per the provenance map
([../docs/90-provenance-map.md](../docs/90-provenance-map.md)):

| Copy from `frontend/src/` | Into | Disposition |
|---|---|---|
| `hooks/agentEventReducer.ts` (622) | `src/agentEventReducer.ts` | copy; self-parse → plugin dispatch |
| `utils/replayEvents.ts` | `src/replayEvents.ts` | copy verbatim (pure) |
| `hooks/useAgentSession.ts` (~600) | `src/useAgentSession.ts` | copy; endpoints/callbacks → props |
| `components/agent/AgentChat.tsx` (839) + generic children | `src/components/` | copy; model list + plugin slots; drop Carbon |
| `components/agent/{ToolCallGroup,AskUserCard,ChatInputToolbar,ThinkingBlock,AgentMarkdown}.tsx` | `src/components/` | copy |
| `components/agent/{InlineArtifactPreview,ArtifactPanel}.tsx` | `src/components/` | copy + factor (endpoints/props) |
| `components/agent/tool-formatters.ts` | `src/tool-formatters.ts` | split: generic kept, `pt` parsing → plugin |
| `types/agent.ts` | `src/types.ts` | copy; `platinumData: unknown` |
| `Inline{PlatinumTable,Dashboard}`, `ArtifactViewer`, `PublishWebappDialog` | — | **stays in Platinum** as the render-plugin bundle |

## The invariant

Live SSE events and durable-replay events go through the **same** reducer. New event types are added
only via the plugin dispatch in `src/plugins.ts`, never via a separate reconstruction path.

## Wiring (the host supplies)

- API base + endpoint paths (props, with defaults).
- `models: {id,label}[]` (instead of a hard-coded list).
- Side-effect callbacks (`onToolResult`, `onSessionTitle`, `onArtifactsUpdated`) — no-ops by default.
- `plugins: RenderPlugin[]` — e.g. Platinum's Carbon widgets.
- MUI theme — the host's; the library uses semantic tokens.
