# 90 — Provenance map (the copy plan)

Every file the library will contain, traced to its source in Platinum, with the generic/specific
split and the disposition (copy verbatim · copy + factor · port to Go · leave in Platinum). This is
the worklist for filling in the staged codebase. LOC figures are from the four-stack exploration.

**Legend:** ✅ copy verbatim · ✂️ copy + factor out specifics · ⇄ port to Go · 🏠 stays in Platinum
(reference plugin) · 🆕 new in the library (no direct source).

> **Redesign update.** The rows below now reflect the architecture redesign — the harness seam
> ([12](12-harness.md)), the multi-session control server ([07](07-in-image-agent.md)), the `Fleet`
> layer + capability axis ([02](02-execution-environment.md), [13](13-fleet-placement.md)), and the
> unified image model ([03](03-image-registry.md)). Rows are tagged with the **AG-1…AG-9** sub-issue
> that delivers them ([`stages/06-agent`](../../docs/interface-refactor/stages/06-agent/README.md)).

## `go/` — orchestration core (Go module, own go.mod)

| Library file | Source(s) | LOC (src) | Disp. | Notes |
|---|---|---|---|---|
| `agentkit.go` | — | — | 🆕 | package doc, version, `Deps` (now `Fleet`), `NewRunner`; `Harness` type + `CreateSessionRequest.Harness`; `Policy.TrustedWorkload` trust gate (AG-1/AG-2) |
| `fleet/fleet.go` | 🆕 (distilled from `sandbox-manager.ts` lifecycle ownership) | — | 🆕 | `Fleet`/`Worker`/`PlacementPolicy` (AG-4); sticky binding via `extension.SessionStore` |
| `fleet/memory.go` · `fleet/policy.go` | 🆕 | — | 🆕 | `NewMemory` + `LeastLoaded`/`RoundRobin` policies |
| `runner.go` | `goapi/pkg/server/agent.go` handlers + `orchestrator/src/routes/sessions.ts` | 2854 + 897 | ⇄ | the facade + orchestration impl; thin-proxy logic becomes direct calls |
| `session.go` | `orchestrator/src/state-machine.ts` | 75 | ⇄ | state machine + flush guard (verbatim transition table) |
| `execenv/execenv.go` | 🆕 (contract distilled from `sandbox-manager.ts`) | — | 🆕 | the `ExecutionEnvironment` interface + types; `Capabilities` = `Backend`/`Tenancy`/`IsolationTier` axis split (AG-1, replaces `IsolatedPerSession`) |
| `execenv/docker/dind.go` | `orchestrator/src/sandbox-manager.ts` (DinD branches) | ~1754 | ⇄ | createSandbox/destroy/suspend/resume/recover; `dockerode` → Go Docker SDK |
| `execenv/docker/local.go` | `sandbox-manager.ts` (socket branches) | (subset) | ⇄ | shared-container/dev adapter |
| `execenv/docker/ports.go` | `sandbox-manager.ts` `PortAllocator` | ~45 | ⇄ | DinD host-port pool |
| `execenv/kubernetes/k8s.go` | 🆕 | — | 🆕 | sketch; pod lifecycle |
| `execenv/mock.go` | `goapi/pkg/agent/mock.go` `MockRunner` (shape) | 219 | ✂️ | in-memory instances + Recorder |
| `imageregistry/registry.go` | 🆕 (distilled from `sandbox-manager.ts` archive/restore) | — | 🆕 | `ImageRegistry` interface + `Handle`/`BuildSpec`; `Resolve` (content-hash cache), `Capabilities.PortableHandles`, `BuildSpec.Layer/SourceKey`, content-hash tagging (AG-6) |
| `imageregistry/localbuild/localbuild.go` | `sandbox-manager.ts` (`docker save`/`commit`/build paths) | (subset) | ⇄ | build + save/load |
| `imageregistry/blobarchive/blobarchive.go` | `sandbox-manager.ts` (`archiveSandbox`, `extractDiff*`, `restoreFromArchive`) + `orchestrator/src/azure-upload.ts` | ~700 + 153 | ⇄ | diff archive ↔ blob; OCI/legacy tar parsing; force-full heuristic |
| `imageregistry/remote/remote.go` | 🆕 | — | 🆕 | sketch; push/pull |
| `imageregistry/mock.go` | 🆕 | — | 🆕 | round-trips Snapshot refs in memory |
| `events/events.go` | `agent/src/types/index.ts` + `frontend/src/types/agent.ts` | 174 + 283 | ⇄ | canonical event vocabulary + `Envelope` |
| `events/compact.go` | `orchestrator/src/compact-events.ts` | 93 | ⇄ | `Compact` + `ExtractSearchText` (verbatim logic) |
| `events/pipeline.go` | `orchestrator/src/message-capture.ts` + `agent.go` `proxySSEStream`/`onLine` | 357 + (subset) | ⇄ | `EventPipeline` + `Sink` + flush hooks + marker side-effects |
| `events/mock.go` | 🆕 | — | 🆕 | fake sink collecting events |
| `artifacts/artifacts.go` | `goapi/pkg/store/store_agent_artifacts.go` + `agent.go` extraction/download | 158 + (subset) | ⇄ | `ArtifactStore` + status state machine + dedup/never-regress |
| `artifacts/mock.go` | `goapi/pkg/agent/mock.go` `MockArtifactStore` | (part of 219) | ✂️ | identical semantics in memory |
| `extension/extension.go` | `agent.go` `loadOrgContext`/`issueAgentScopedJWT`/token logging | (subset) | ✂️ | `SessionStore` (+ `Get/Set/ClearWorkerBinding` for sticky placement, AG-4), `BlobStore`, `OrgContextProvider`, `ScopedClaimsIssuer`, `TokenUsageLogger`, `ArtifactEnricher`, `Metrics` |
| `internal/recorder/recorder.go` | `goapi/pkg/mockutil/recorder.go` | ~60 | ✅ | shared `{Method,Args}` recorder (with #821 string-clone) |
| `httpadapter/` | `agent.go` route registration | (subset) | 🆕/✂️ | optional Fiber/net-http handler helpers |

## `sandbox/` — in-image agent (TS package)

| Library file | Source (`agent/src/`) | LOC | Disp. | Notes |
|---|---|---|---|---|
| `src/index.ts` | `index.ts` | 110 | ✂️ | Fastify boot; env-name aliases |
| `src/config.ts` | `config.ts` | 61 | ✂️ | Zod env (rename `GOAPI_URL`→`HOST_API_URL` w/ alias) |
| `src/routes/agent.ts` | `routes/agent.ts` | 291 | ✅ | the contract endpoints |
| `src/routes/health.ts` | `routes/health.ts` | 18 | ✅ | |
| `src/routes/workspace.ts` | `routes/workspace.ts` | 304 | ✅ | files/snapshot/diff/scan-secrets |
| `src/services/agent-service.ts` | — | — | ✂️→removed | **dissolved**: its SDK loop → `claude-agent-sdk.ts` (AG-2), its session-owner role → `session-manager.ts` (AG-3). Gutted-but-present after AG-2; **removed after AG-3** |
| `src/services/session-manager.ts` | `services/agent-service.ts` (session-owner role) | (subset) | 🆕 | multi-session state map; per-(session,query) abort (AG-3) |
| `src/harness/{harness,registry}.ts` | 🆕 | — | 🆕 | `Harness`/`HarnessEmitter`/`HarnessDescriptor`/`HarnessRegistry` seam (AG-2) |
| `src/harness/claude-agent-sdk.ts` | `services/agent-service.ts` (SDK `query()` loop, hooks) | (most of 831) | ✂️ | lift behind `Harness`; `streamService.x` → `ctx.emit.x`; abort via `ctx.signal` (AG-2) |
| `src/services/stream-service.ts` | `services/stream-service.ts` | 351 | ✂️ | buffer/replay/coalesce; composite key `${sessionId}:${queryId}` + `closeSession` (AG-3) |
| `src/routes/sessions.ts` | 🆕 (from `routes/agent.ts`) | — | 🆕 | session-scoped routes + `POST /sessions` + back-compat shims (AG-3) |
| `src/services/attachment-prompt.ts` | `services/attachment-prompt.ts` | 169 | ✅ | uploads/PPTX |
| `src/tools/registry.ts` | 🆕 (from `agent-service.ts` option assembly) | — | 🆕 | `ToolRegistry` + `ToolPlugin` |
| `src/tools/builtin/*` | `mcp/ui-tools.ts` (`ask_user`,`write_file`,`view_image`,`screenshot_url`) | (subset of 993) | ✂️ | generic tools ship in core |
| `src/types/index.ts` | `types/index.ts` | 174 | ✂️ | mirror of `events/events.go`; generic types only |
| — render_table/chart/dashboard/pptx tools | `mcp/ui-tools.ts` (rest of 993) | — | 🏠 | Platinum tool-plugin bundle |
| — `pt` CLI | Platinum image build | — | 🏠 | image step, not core |

## `web/` — React rendering (npm package)

| Library file | Source (`frontend/src/`) | LOC | Disp. | Notes |
|---|---|---|---|---|
| `src/agentEventReducer.ts` | `hooks/agentEventReducer.ts` | 622 | ✂️ | self-parse → plugin dispatch |
| `src/replayEvents.ts` | `utils/replayEvents.ts` | ~80 | ✅ | pure |
| `src/useAgentSession.ts` | `hooks/useAgentSession.ts` | ~600 | ✂️ | endpoints/callbacks as props |
| `src/components/AgentChat.tsx` | `components/agent/AgentChat.tsx` | 839 | ✂️ | model selector + plugin slots; drop Carbon |
| `src/components/ToolCallGroup.tsx` | `components/agent/ToolCallGroup.tsx` | 524 | ✅ | |
| `src/components/AskUserCard.tsx` | `components/agent/AskUserCard.tsx` | 127 | ✅ | |
| `src/components/ChatInputToolbar.tsx` | `components/agent/ChatInputToolbar.tsx` | 230 | ✅ | |
| `src/components/{ThinkingBlock,AgentMarkdown}.tsx` | same | — | ✅ | check md extensions |
| `src/components/InlineArtifactPreview.tsx` | `components/agent/InlineArtifactPreview.tsx` | 434 | ✂️ | webapp dialog → plugin; endpoints props |
| `src/components/ArtifactPanel.tsx` | `components/agent/ArtifactPanel.tsx` | 347 | ✂️ | drop dashboard-pin callback |
| `src/hooks/{useVoiceDictation,useFileAttachments}.ts` | same | 186 + 104 | ✂️ | endpoints as props |
| `src/tool-formatters.ts` | `components/agent/tool-formatters.ts` | 350 | ✂️ | keep generic; `pt` parsing → plugin |
| `src/types.ts` | `types/agent.ts` | 283 | ✂️ | `platinumData: unknown` |
| `src/plugins.ts` | 🆕 | — | 🆕 | `RenderPlugin` seam + provider |
| — Inline{PlatinumTable,Dashboard}, ArtifactViewer, PublishWebappDialog | `components/agent/*` | — | 🏠 | Platinum render-plugin bundle |

## Type ownership (the liftability rule)

The Go module **redefines** the wire types it needs rather than importing `goapi/pkg/types`:

| Library type | Mirrors | Why redefined |
|---|---|---|
| `artifacts.Artifact` | `types.AgentArtifact` | so `go.mod` imports nothing from Platinum |
| `events.Envelope` / `events.Type` | `agent/src/types` SSE types | canonical, generated to TS |
| `agentkit.Session` | `types.AgentSession` (subset) | only fields the runtime needs; host keeps the full row |
| `imageregistry.Handle` | the snapshot fields on `AgentSession` | opaque durable pointer |

A thin adapter in the *host* converts between `store.Store`'s `types.AgentSession` and the library's
`Session` — that adapter is where Platinum-specific columns (workflow_id, persona, counts) stay.

## Rough size of the lift

| Area | Generic LOC to copy/port | Platinum LOC staying home |
|---|---|---|
| Go orchestration | ~2,500 ported (from ~3,750 TS orchestrator + subset of agent.go) | org context, JWT, token cost, title-bot, files publish |
| In-image agent | ~1,500 copied | ~500 (Platinum tools) |
| Frontend | ~4,400 copied/factored | ~800 (Carbon widgets) |

The exploration's headline: ~85% of the frontend and the large majority of the orchestrator are
generic. The copy is mechanical once the seams in this doc are cut; the *design* (the seams) is the
hard part and is what docs 00–11 specify.
