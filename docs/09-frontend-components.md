# 09 — Frontend: the rendering package

The library ships the React code that turns an agent event stream into a polished chat — the
`web/` package. It is copied from `frontend/src/`'s agent components, which the exploration found to be
~85% reusable, with Carbon table/chart widgets the main thing to factor out behind a **render-plugin**
seam. The non-negotiable invariant is preserved: **one reducer, one rendering codepath, live and
replayed alike** (CLAUDE.md rule 12).

## The crown jewel: a single pure reducer

`web/src/agentEventReducer.ts` is copied verbatim in shape: a pure `(state, event) => state` function
that reconstructs the entire conversation UI from events. It serves three callers identically:

1. live SSE (`useAgentSession.readSSEStream` → `handleSSEEvent`),
2. durable replay (`replayEvents` re-runs persisted compacted events),
3. tests.

Because it's pure and side-effect-free, replay is deterministic — the property that lets a restored
session look exactly like the live one. The library treats any second reconstruction path as a bug.

### State shape (preserved)

The reducer's `AgentEventState` keeps its proven structure: `messages[]`, `isStreaming`, `error`,
`artifacts[]`, `currentMessage`, and the keyed maps `toolCalls`, `askedQuestions`, plus
continuation-splitting state (`hasActiveToolCalls`, `originalMessageId`, `continuationCount`) that
correctly interleaves text→tool→text within one turn, `activityStatus`, `toolInputBuffer`, `todos`,
`sessionInfo`, `subagentEvents`. (From `agentEventReducer.ts` lines ~23–60.)

The Platinum-coupled maps — `renderedTables`, `renderedCharts`, `createdDashboards` and the
`tool_use_end` self-parse for `__render_table` etc. — become **render-plugin state**, not core state.

## The render-plugin seam

The reducer and components dispatch **extension event types** (`table_rendered`, `chart_rendered`,
`dashboard_created`, `webapp_ready`, `page_tool_request`, `settings_updated`) to registered plugins
instead of handling them inline. A render plugin declares:

```ts
interface RenderPlugin<TState = unknown> {
  // Extension event types this plugin owns.
  eventTypes: string[];
  // Fold a plugin event into plugin-scoped state (kept in a side-channel map keyed by
  // toolCallId/messageId), so the core reducer state stays generic.
  reduce(state: TState, event: AgentSSEEvent): TState;
  // Render the plugin's artifact inline, given the tool call it attaches to.
  render(props: { event: TState; toolCallId: string; sessionId: string }): React.ReactNode;
}
```

- **Generic core** handles the ~20 generic event types and renders messages, thinking blocks, content,
  tool-call cards, `ask_user` cards, artifact previews, the input toolbar, stuck-detection banners.
- **Platinum render plugins** handle `table_rendered`/`chart_rendered` (→ `InlinePlatinumTable`,
  Carbon-backed) and `dashboard_created` (→ `InlineDashboard`). They ship with Platinum, register into
  the library's `<AgentChatProvider plugins={[...]}>`.

This is exactly the boundary the exploration recommended: generalise `RenderedTableInfo.platinumData`
to `unknown` and let the plugin own the widget.

## Components: copy vs factor vs leave

| Component | LOC | Disposition |
|---|---|---|
| `agentEventReducer.ts` | 622 | **Copy** (factor self-parse → plugin dispatch) |
| `replayEvents.ts` | ~80 | **Copy** verbatim (pure) |
| `useAgentSession.ts` | ~600 | **Copy + parameterise** endpoints (`/agent/session/...`) and side-effect callbacks |
| `AgentChat.tsx` | 839 | **Copy + parameterise** model selector + plugin slots; remove Carbon imports |
| `ToolCallGroup.tsx` | 524 | **Copy** (generic; image-output parsing stays) |
| `AskUserCard.tsx` | 127 | **Copy** verbatim (fully generic) |
| `ChatInputToolbar.tsx` | 230 | **Copy** verbatim |
| `useVoiceDictation.ts` | 186 | **Copy** (transcription endpoint is a prop) |
| `useFileAttachments.ts` | 104 | **Copy** (upload endpoint is a prop) |
| `ThinkingBlock`, `AgentMarkdown` | — | **Copy** (check markdown extensions) |
| `InlineArtifactPreview.tsx` | 434 | **Copy + factor** (remove webapp dialog → plugin; endpoints as props) |
| `ArtifactPanel.tsx` | 347 | **Copy + factor** (remove dashboard-pin callback → prop) |
| `tool-formatters.ts` | 350 | **Split**: keep `stripMcpPrefix`/`formatMcpToolName`/image parsing; `pt`-CLI parsing → Platinum render plugin |
| `InlinePlatinumTable.tsx`, `InlineDashboard.tsx`, `ArtifactViewer.tsx`, `PublishWebappDialog.tsx` | — | **Leave in Platinum** as the reference render-plugin bundle |
| `types/agent.ts` | 283 | **Copy** (mirror of `events/events.go`; `platinumData: unknown`) |

~5,200 LOC extractable, ~85% clean. The Carbon widgets stay in Platinum and register as plugins.

## Parameterisation (the host wires these)

`useAgentSession` and `AgentChat` take their Platinum-isms as props/config, not imports:

- **API base + endpoints** — `createSession`/`sendMessage`/`stream`/`reconnect`/`artifacts`/`upload`
  paths (props with sensible defaults).
- **Model list** — `models: {id,label}[]` instead of the hard-coded `AGENT_MODELS`.
- **Side-effect callbacks** — `onToolResult`, `onSessionTitle`, `onArtifactsUpdated` (Platinum uses
  these for observability/title; other products pass no-ops).
- **Render plugins** — the array above.
- **Theme** — MUI theme is the host's; the library uses semantic tokens, not Platinum's literal palette.

## Reconnect & stuck detection (kept)

`readSSEStream`'s silent-reconnect (up to 3 attempts, then durable replay) and the heartbeat/progress
**stuck detection** (60s → possibly-stuck, 120s → likely-stuck + nudge) are generic resilience
features and copy across with their thresholds as config.

## Packaging

`web/` is an npm package (`@agentkit/chat-ui`, working name) with peer deps on `react`, `react-dom`,
`@mui/material`, `@emotion/*`. It exports the provider, `AgentChat`, the hooks, the reducer, the types,
and the `RenderPlugin`/`ToolFormatter` seams. It has **no** dependency on Platinum app state, routing,
or contexts — those entanglements (`AccountContext`, app router) are replaced by props/provider.

## Mapping: today → library

| Today (`frontend/src/`) | Library (`web/src/`) |
|---|---|
| `hooks/agentEventReducer.ts` | `agentEventReducer.ts` (+ plugin dispatch) |
| `utils/replayEvents.ts` | `replayEvents.ts` |
| `hooks/useAgentSession.ts` | `useAgentSession.ts` (parameterised) |
| `components/agent/AgentChat.tsx` + generic children | `components/*` |
| `components/agent/Inline{PlatinumTable,Dashboard}.tsx`, `ArtifactViewer`, `PublishWebappDialog` | **Platinum render-plugin bundle** (host-owned) |
| `types/agent.ts` | `types.ts` (mirror of `events/events.go`) |
