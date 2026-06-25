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
