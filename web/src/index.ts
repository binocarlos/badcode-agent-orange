// Public surface of @agentkit/chat-ui.
// Re-exports the reducer, hooks, components, and the plugins.ts seam.
// See ../docs/09-frontend-components.md and ../docs/90-provenance-map.md.

// Core types
export type {
  ActivityStatus,
  AgentMessage,
  AgentMessageSearchResult,
  AgentSession,
  AgentSessionListItem,
  AgentSSEEvent,
  AgentSSEEventType,
  ArtifactInfo,
  AskUserOption,
  AskUserQuestionInfo,
  CreateAgentSessionRequest,
  CreateAgentSessionResponse,
  CreatedDashboardInfo,
  HookEventInfo,
  PersistedAgentMessage,
  RenderedChartInfo,
  RenderedTableInfo,
  SendAgentMessageRequest,
  TodoItem,
  ToolCallInfo,
} from './types.js'

// Reducer (single-reducer invariant: live + replay)
export {
  agentEventReducer,
  initialAgentEventState,
} from './agentEventReducer.js'
export type { AgentEventState, AgentSessionInfo, InstalledSkillInfo } from './agentEventReducer.js'

// Replay utilities
export {
  replayEvents,
  replayFromPersistedMessages,
  persistedToEvents,
} from './replayEvents.js'

// Session hook
export { default as useAgentSession } from './useAgentSession.js'
export type { UseAgentSessionOptions, PersonaInfo } from './useAgentSession.js'

// Plugin fold helper — pure, replay-safe. Used by AgentChat internally and
// available to hosts that want to pre-compute plugin state outside the component.
export { foldPluginEvents } from './components/AgentChat.js'

// Canonical session permalink (the /p/:projectId/s/:sessionId route)
export {
  PROJECT_SEGMENT,
  SESSION_SEGMENT,
  SESSION_PERMALINK_FORMAT,
  buildSessionPath,
  buildSessionPermalink,
  parseSessionPermalink,
} from './permalink.js'
export type { SessionRoute } from './permalink.js'
export { default as useSessionPermalink, projectIdFromLocation } from './useSessionPermalink.js'
export type { UseSessionPermalinkOptions, SessionPermalinkApi } from './useSessionPermalink.js'

// Project settings (B3) — the /agent/project-settings surface: pure helpers,
// the load/edit/save hook, and the page.
export {
  PROJECT_SETTINGS_ENDPOINT,
  PROJECT_SETTING_NUMERICS,
  DEFAULT_MAX_CONCURRENT_JOBS,
  DEFAULT_BRIEFING_MAX_BYTES,
  DEFAULT_SNAPSHOT_TTL_DAYS,
  defaultProjectSettings,
  coerceProjectSettings,
  parseJsonObject,
  formatJsonObject,
  describeNumericSetting,
  validateProjectSettings,
  projectSettingsBody,
} from './projectSettings.js'
export type {
  ProjectSettings,
  JsonParseResult,
  NumericSettingSpec,
  ZeroSemantics,
  FieldErrors,
} from './projectSettings.js'
export { default as useProjectSettings } from './useProjectSettings.js'
export type { UseProjectSettingsOptions, ProjectSettingsApi } from './useProjectSettings.js'

// Workers (C3) — the /agent/workers surface: pure helpers, the CRUD and
// job-history hooks, and the components.
export {
  WORKER_ENDPOINTS,
  WORKER_NAME_PATTERN,
  WORKER_QUERY_PARAM,
  DEFAULT_MAX_INSTANCES,
  newWorkerDraft,
  coerceWorker,
  validateWorkerName,
  parseImageRef,
  validateImageRef,
  describeImageRef,
  validateSelector,
  validateWorker,
  workerBody,
  workerFromSearch,
  buildWorkerSearch,
} from './workers.js'
export type { Worker, WorkerDraft, ImageRef, WorkerFieldErrors } from './workers.js'
export { default as useWorkers, useWorkerJobs } from './useWorkers.js'
export type {
  UseWorkersOptions,
  WorkersApi,
  UseWorkerJobsOptions,
  WorkerJobsApi,
} from './useWorkers.js'

// Images (B4) — the read-only /agent/images catalogue: the picker's source of
// suggestions for a worker's image field, which stays free text.
export { IMAGE_ENDPOINTS, coerceImage, imageOptionsFrom } from './images.js'
export type { ProjectImage } from './images.js'
export { default as useImages } from './useImages.js'
export type { UseImagesOptions, ImagesApi } from './useImages.js'

// Topologies (T3) — the /agent/topologies surface: catalogue, question form
// helpers, preview + apply, and the onboarding flow component.
export {
  TOPOLOGY_ENDPOINTS,
  topologyRef,
  coerceTopologyQuestion,
  coerceTopology,
  coerceTopologyDiff,
  coerceTopologyBundle,
  coerceTopologyPreview,
  coerceTopologyApplyResult,
  initialTopologyAnswers,
  validateTopologyAnswers,
  topologyAnswersBody,
} from './topologies.js'
export type {
  Topology,
  TopologyQuestion,
  TopologyQuestionType,
  TopologyAnswers,
  TopologyAnswerErrors,
  TopologyRouteSummary,
  TopologyCronSummary,
  TopologyDiff,
  TopologyBundle,
  TopologyPreview,
  TopologyApplyResult,
} from './topologies.js'
export { default as useTopologies } from './useTopologies.js'
export type { UseTopologiesOptions, TopologiesApi } from './useTopologies.js'

// Events & observability (F1) — the `/agent/events` + `/agent/deliveries` read
// surface: pure helpers, the overview hook, and the dry-run subscription
// matcher. Read-only; F2 owns the subscription/schedule editors.
export {
  EVENT_ENDPOINTS,
  EVENT_SOURCES,
  EVENT_QUERY_PARAM,
  EVENT_DRAFT_TEMPLATE,
  DELIVERY_STATUSES,
  ENVELOPE_FILTER_KEYS,
  FAILURE_REASONS,
  coerceEnvelope,
  coerceProjectEvent,
  coerceDelivery,
  coerceSubscription,
  isDeliveryStatus,
  isTerminalDeliveryStatus,
  describeDeliveryStatus,
  deliveryStatusSeverity,
  deliveryDurationSeconds,
  formatDuration,
  buildJobRows,
  validateEventTypePattern,
  eventTypeMatches,
  envelopeFilterMatches,
  matchSubscriptions,
  blankEnvelope,
  parseEventDraft,
  eventToDraftText,
  eventFromSearch,
  buildEventSearch,
  sumTokens,
  formatTokens,
} from './events.js'
export type {
  EventEnvelope,
  EventSource,
  ProjectEvent,
  EventDelivery,
  DeliveryStatus,
  Subscription,
  JobRow,
  SubscriptionMatch,
  MatchableEvent,
  EventDraftParse,
  TokenTotals,
} from './events.js'
export {
  default as useEventsOverview,
  useSessionTokens,
  DEFAULT_EVENT_PAGE,
} from './useEvents.js'
export type {
  UseEventsOverviewOptions,
  EventsOverviewApi,
  UseSessionTokensOptions,
  SessionTokensApi,
} from './useEvents.js'

// Subscriptions + schedules — the EDITING surface (F2), §8.3 and §8.6. The
// reading half stays in events.js above; this adds validation, the write
// bodies, the cron grammar and its plain-English description, and the
// config-time NL assist. The assist compiles a description into a cron
// expression or an envelope filter ONCE, for a human to confirm — nothing here
// runs when a schedule fires or an event is routed.
export {
  SUBSCRIPTION_ENDPOINTS,
  SUBSCRIPTION_QUERY_PARAM,
  newSubscriptionDraft,
  subscriptionBody,
  validateSubscription,
  validateFilterEntry,
  describeSubscription,
  subscriptionFromSearch,
  buildSubscriptionSearch,
} from './subscriptions.js'
export type { SubscriptionDraft, SubscriptionFieldErrors } from './subscriptions.js'
export {
  SCHEDULE_ENDPOINTS,
  SCHEDULE_QUERY_PARAM,
  CRON_FIELD_NAMES,
  WEEKDAY_LABELS,
  newScheduleDraft,
  coerceSchedule,
  scheduleBody,
  validateCron,
  validateCronField,
  validateSchedule,
  describeCron,
  scheduleFromSearch,
  buildScheduleSearch,
} from './schedules.js'
export type { Schedule, ScheduleDraft, ScheduleFieldErrors, CronFieldName } from './schedules.js'
export { compileCron, compileEnvelopeFilter, weekdayNames } from './nlAssist.js'
export type { CompileResult, CronProposal, FilterProposal, NlCompiler } from './nlAssist.js'
export { default as useSubscriptions } from './useSubscriptions.js'
export type { UseSubscriptionsOptions, SubscriptionsApi } from './useSubscriptions.js'
export { default as useSchedules } from './useSchedules.js'
export type { UseSchedulesOptions, SchedulesApi } from './useSchedules.js'

// The config log / changelog (F1, owning J4) — §15.10. NOTE: the read route
// `GET /agent/config-events` does not exist yet (J2/J3 owns it); until it does,
// pass `fetchConfigEvents`. The exact contract is in configLog.ts's header.
export {
  CONFIG_LOG_ENDPOINT,
  CONFIG_ACTIONS,
  DIFF_LINE_BUDGET,
  coerceConfigEvent,
  configEntity,
  describeConfigAction,
  changelogTitle,
  configPromptText,
  diffLines,
  buildChangelog,
  workerLineage,
  actionMatches,
  filterChangelog,
  changelogQueryParams,
  formatConfigTimestamp,
  extractConfigEvents,
} from './configLog.js'
export type {
  ConfigAction,
  ConfigEvent,
  ConfigEntity,
  ConfigEntityKind,
  ConfigEventFetcher,
  ChangelogEntry,
  ChangelogDiff,
  ChangelogQuery,
  DiffLine,
  LineageEntry,
  WorkerLineage as WorkerLineageData,
  BuildChangelogOptions,
} from './configLog.js'
export { default as useConfigLog } from './useConfigLog.js'
export type { UseConfigLogOptions, ConfigLogApi } from './useConfigLog.js'

// The spine (DK1) — the rail and its closed glyph set (design §3.6).
// Presentational only; what hangs off the rail is decided by desk.ts.
export {
  SPINE_GLYPHS,
  SPINE_GLYPH_MEANINGS,
  SPINE_GUTTER,
  SPINE_GLYPH_SIZE,
  SpineGlyph,
  SpineRail,
  SpineRow,
  SpineGap,
  consoleColor,
  spineGlyphColor,
} from './spine.js'
export type {
  SpineGlyphName,
  SpineGlyphProps,
  SpineRailProps,
  SpineRowProps,
  SpineGapProps,
} from './spine.js'

// The Desk fold (DK1) — asks, changes, trouble, computed read-time (§5.2).
export {
  ATTENTION_ENDPOINTS,
  DESK_GLYPHS,
  DESK_ASKS_CAVEAT,
  DESK_NO_DELIVERY_REASON,
  DESK_FREEZE_REFUSAL_NOTE,
  SCHEDULE_MAX_PROVISION_FAILURES,
  buildDesk,
  coerceAttentionRequest,
  isAttentionRequestOpen,
  deskChangeVerb,
  deskChangeSubject,
  frozenTargetFromText,
} from './desk.js'
export type {
  AttentionRequest,
  BuildDeskInput,
  Desk,
  DeskAsk,
  DeskChange,
  DeskGlyph,
  DeskTrouble,
  DeskTroubleKind,
} from './desk.js'

// The Desk (DK2) — the landing view, its data hook, and the attention read
// route behind the Asks stack.
export { default as useAttentionRequests } from './useAttentionRequests.js'
export type { UseAttentionRequestsOptions, AttentionRequestsApi } from './useAttentionRequests.js'
export {
  default as useDesk,
  deskLastSeenKey,
  readDeskLastSeen,
  writeDeskLastSeen,
} from './useDesk.js'
export type { UseDeskOptions, DeskApi } from './useDesk.js'
export { default as DeskPage } from './components/DeskPage.js'
export type { DeskPageProps } from './components/DeskPage.js'

// Supporting hooks
export { default as useVoiceDictation } from './hooks/useVoiceDictation.js'
export type { UseVoiceDictationOptions } from './hooks/useVoiceDictation.js'
export { default as useFileAttachments } from './hooks/useFileAttachments.js'
export type { PendingAttachment, UseFileAttachmentsOptions } from './hooks/useFileAttachments.js'

// Tool formatters
export {
  getToolCategory,
  getToolDisplayName,
  getToolIcon,
  getToolSummary,
  formatMcpToolName,
  stripMcpPrefix,
  parsePtCommand,
  parseScriptExecution,
  PT_COMMANDS,
  TOOL_DISPLAY_OVERRIDES,
  SDK_TOOL_DISPLAY_NAMES,
} from './tool-formatters.js'
export type { ToolCategory, PtCommandMatch, ScriptExecutionMatch } from './tool-formatters.js'

// Components
export { default as ChatHistoryDrawer, DRAWER_WIDTH, STORAGE_KEY } from './components/ChatHistoryDrawer.js'
export { default as AgentChat } from './components/AgentChat.js'
export { default as AgentSessionList } from './components/AgentSessionList.js'
export { default as AgentMarkdown } from './components/AgentMarkdown.js'
export { default as ArtifactPanel } from './components/ArtifactPanel.js'
export { default as AskUserCard } from './components/AskUserCard.js'
export { default as ChatInputToolbar } from './components/ChatInputToolbar.js'
export { default as CodeCreatedBlock } from './components/CodeCreatedBlock.js'
export { default as InlineArtifactPreview } from './components/InlineArtifactPreview.js'
export { default as RecordingOverlay } from './components/RecordingOverlay.js'
export { default as ScriptExecutionBlock } from './components/ScriptExecutionBlock.js'
export { default as ThinkingBlock } from './components/ThinkingBlock.js'
export {
  default as ToolCallGroup,
  tryParseImageOutput,
  isImageToolCall,
  isImageReadToolCall,
  isScreenshotToolCall,
} from './components/ToolCallGroup.js'

// Configuration components (B3 + C3)
export { default as ProjectSettingsPage } from './components/ProjectSettingsPage.js'
export type { ProjectSettingsPageProps } from './components/ProjectSettingsPage.js'
export { default as JsonObjectEditor } from './components/JsonObjectEditor.js'
export type { JsonObjectEditorProps } from './components/JsonObjectEditor.js'
export { default as WorkersPage } from './components/WorkersPage.js'
export type { WorkersPageProps } from './components/WorkersPage.js'
export { default as WorkerLineage } from './components/WorkerLineage.js'
export type { WorkerLineageProps, LineageVersion } from './components/WorkerLineage.js'

// Before / after (BA1) — design §7.2: one rewrite, the jobs either side of it.
export {
  beforeAfter,
  eventMs,
  BEFORE_AFTER_CAVEAT,
  FINISHED_EVENT_TYPE,
  NO_JOB_YET,
} from './learning.js'
export type { BeforeAfter, BeforeAfterSide } from './learning.js'
export { default as BeforeAfterView } from './components/BeforeAfterView.js'
export type { BeforeAfterViewProps } from './components/BeforeAfterView.js'
export { default as WorkerPromptVersion, restoreRationale } from './components/WorkerPromptVersion.js'
export type { WorkerPromptVersionProps } from './components/WorkerPromptVersion.js'
export { default as WorkerList } from './components/WorkerList.js'
export type { WorkerListProps } from './components/WorkerList.js'
export { default as WorkerEditor } from './components/WorkerEditor.js'
export type { WorkerEditorProps } from './components/WorkerEditor.js'
export { default as WorkerJobHistory } from './components/WorkerJobHistory.js'
export type { WorkerJobHistoryProps } from './components/WorkerJobHistory.js'
export { default as WorkerChatPanel } from './components/WorkerChatPanel.js'
export type { WorkerChatPanelProps } from './components/WorkerChatPanel.js'
export { default as TopologyOnboarding } from './components/TopologyOnboarding.js'
export type { TopologyOnboardingProps } from './components/TopologyOnboarding.js'

// Observability + changelog components (F1, owning J4)
export { default as EventsPage } from './components/EventsPage.js'
export type { EventsPageProps } from './components/EventsPage.js'
export { default as EventList } from './components/EventList.js'
export type { EventListProps } from './components/EventList.js'
export { default as EventDetail } from './components/EventDetail.js'
export type { EventDetailProps } from './components/EventDetail.js'
export { default as EventJobHistory, statusChipColor } from './components/EventJobHistory.js'
export type { EventJobHistoryProps } from './components/EventJobHistory.js'
export { default as EventReplayPanel } from './components/EventReplayPanel.js'
export type { EventReplayPanelProps } from './components/EventReplayPanel.js'
export { default as ChangelogView, ACTION_FILTERS, DiffBlock } from './components/ChangelogView.js'
export type { ChangelogViewProps } from './components/ChangelogView.js'

// Subscription + schedule editors (F2) — the two ways a worker is woken.
export { default as AutomationPage } from './components/AutomationPage.js'
export type { AutomationPageProps, AutomationTab } from './components/AutomationPage.js'
export { default as SubscriptionEditor } from './components/SubscriptionEditor.js'
export type { SubscriptionEditorProps } from './components/SubscriptionEditor.js'
export { default as ScheduleEditor } from './components/ScheduleEditor.js'
export type { ScheduleEditorProps } from './components/ScheduleEditor.js'
export { default as NlAssistField } from './components/NlAssistField.js'
export type { NlAssistFieldProps } from './components/NlAssistField.js'

// Artifact utilities
export { buildArtifactTree } from './artifactTree.js'
export type { ArtifactTreeNode } from './artifactTree.js'
export {
  filterArtifactsByType,
  filterArtifactsByStatus,
  filterArtifactsBySearch,
  getLanguageFromFilename,
  parseCSVPreview,
} from './artifactFilters.js'
export type {
  ArtifactTypeFilter,
  ArtifactStatusFilter,
  CSVPreviewResult,
} from './artifactFilters.js'
export { getPrismLanguage } from './prismLanguage.js'

// Artifact viewer components
export { default as ArtifactViewer } from './components/ArtifactViewer.js'
export type { PlatinumArtifactData } from './components/ArtifactViewer.js'
export { default as ArtifactPreviewDialog } from './components/ArtifactPreviewDialog.js'
export { default as ArtifactCodePreview } from './components/ArtifactCodePreview.js'
export { default as ArtifactCsvPreview } from './components/ArtifactCsvPreview.js'
export { default as ArtifactLightbox } from './components/ArtifactLightbox.js'
export { default as ArtifactGrid } from './components/ArtifactGrid.js'
export { default as ArtifactTreeView } from './components/ArtifactTreeView.js'

// Provider + context hooks
export {
  AgentChatProvider,
  useAgentChat,
  useAgentChatContext,
  useAgentChatContextOptional,
  useAgentSessions,
} from './AgentChatProvider.js'

// Plugin seam (the render-plugin boundary)
export type {
  AgentSSEEvent as PluginAgentSSEEvent,
  RenderPlugin,
  ToolFormatter,
  AgentChatConfig,
  AgentChatEndpoints,
} from './plugins.js'
export { DEFAULT_ENDPOINTS } from './plugins.js'
