// Browser event types mirroring the sandbox types/Go events.
// `platinumData` and any Carbon-typed payload fields are `unknown` so this
// package has no dependency on Platinum's Carbon types.
// Mirrors frontend/src/types/agent.ts — see ../docs/90-provenance-map.md.

// Activity status shown in the persistent status line above the input area
export interface ActivityStatus {
  label: string           // "Thinking...", "Running: Query Table", "Writing response..."
  detail?: string         // "Side: Age, Top: Gender"
  category?: 'thinking' | 'tool' | 'writing' | 'system' | 'preparing_tool'
  toolName?: string       // raw tool name for icon lookup
  toolInput?: Record<string, unknown> // tool input for pt command detection
  elapsedSeconds?: number // from tool_progress events
  toolInputPreview?: string // partial JSON streaming into tool params
}

// Agent session types
export interface AgentSession {
  id: string
  status: 'active' | 'creating' | 'streaming' | 'completed' | 'error' | 'cancelled'
  workflowId: string
  persona?: string
  customer: string
  job?: string
  createdAt?: string
  updatedAt?: string
  error?: string
  model?: string
  title?: string
  metadata?: Record<string, unknown>
}

// Message types
export interface AgentMessage {
  id: string
  role: 'user' | 'assistant'
  content: string
  thinking?: string
  toolCalls?: ToolCallInfo[]
  autoArtifactPaths?: string[]
  timestamp: string
}

export interface HookEventInfo {
  hookType: 'pre_tool' | 'post_tool' | 'tool_failure' | 'notification' | 'hook_response'
  toolName?: string
  toolUseId?: string
  timestamp: string
  payload: Record<string, unknown>
}

export interface ToolCallInfo {
  id: string
  name: string
  input: Record<string, unknown>
  output?: string
  status: 'running' | 'complete' | 'error'
  elapsedSeconds?: number
  hookEvents?: HookEventInfo[]
}

// SSE Event types from the agent
export type AgentSSEEventType =
  | 'message_start'
  | 'content_delta'
  | 'thinking_delta'
  | 'tool_use_start'
  | 'tool_use_end'
  | 'tool_progress'
  | 'system_status'
  | 'session_info'
  | 'hook_event'
  | 'subagent_event'
  | 'message_end'
  | 'query_complete'
  | 'artifact_registered'
  | 'artifacts_updated'
  | 'webapp_ready'
  | 'table_rendered'
  | 'chart_rendered'
  | 'ask_user'
  | 'activity_update'
  | 'tool_input_delta'
  | 'page_tool_request'
  | 'settings_updated'
  | 'dashboard_created'
  | 'session_title'
  | 'user_message'
  | 'heartbeat'
  | 'dag_complete'
  | 'skill_installed'
  | 'error'

export interface AgentSSEEvent {
  type: AgentSSEEventType
  data: unknown
  timestamp: string
}

export interface SkillInstalledEventData { id: string; name: string; visibility?: string; requires_build?: boolean; installLog?: string }

export interface ArtifactInfo {
  id?: string
  filePath: string
  fileName: string
  fileSize?: number
  mimeType?: string
  label: string
  description?: string
  artifactType: 'file' | 'image' | 'code' | 'csv' | 'chart' | 'report' | 'webapp' | 'data'
  source: 'auto' | 'registered'
  status: 'live' | 'extracted' | 'lost' | 'extraction_failed'
  isDir?: boolean
  downloadUrl?: string
}

// Todo items from TodoWrite tool calls
export interface TodoItem {
  content: string
  status: 'pending' | 'in_progress' | 'completed'
}

// Persisted message from database
export interface PersistedAgentMessage {
  id: string
  session_id: string
  query_id: string
  phase_node: string
  role: 'user' | 'assistant' | 'tool_call' | 'tool_result'
  content: string
  tool_name?: string
  tool_input?: Record<string, unknown>
  sequence_num: number
  metadata?: Record<string, unknown>
  created_at: number
}

// Session summary for history list
export interface OpProgress {
  op: string
  phase: string
  bytesDone: number
  bytesTotal: number
  layers?: { id: string; current: number; total: number; status: string }[]
  startedAt: string
  err?: string
}

export interface AgentSessionListItem {
  id: string
  created_at: number
  updated_at: number
  user_email: string
  customer: string
  job: string
  workflow_id: string
  persona: string
  status: string
  current_node: string
  title: string
  artifact_count: number
  container_state?: string
  snapshot_state?: '' | 'pending' | 'archived' | 'failed' | 'persistence_failed' | 'extraction_failed'
  snapshot_progress?: OpProgress
  archive_blob_path?: string
  archive_size_bytes?: number
}

// Search result from message search endpoint
export interface AgentMessageSearchResult {
  session_id: string
  session_title: string
  user_email: string
  role: string
  content: string
  created_at: number
  job: string
  workflow_id: string
}

// API request/response types
export interface CreateAgentSessionRequest {
  customer: string
  job?: string
  workflow_id?: string
  persona?: string
  model?: string
  systemPrompt?: string
  tools?: string[]
  maxTurns?: number
}

export interface CreateAgentSessionResponse {
  id: string
  status: string
}

export interface SendAgentMessageRequest {
  content: string
  model?: string
  attachmentIds?: string[]
}

// Rendered table/chart data attached to tool calls.
// `platinumData` is `unknown` so this package has no dependency on Carbon types.
export interface RenderedTableInfo {
  id: string
  platinumData: unknown // opaque; Carbon types live in Platinum's render plugin
  title: string
  toolCallId: string
  customer?: string
  job?: string
  spec?: string // CarbonSpec JSON string for re-fetching with visibility overrides
}

export interface RenderedChartInfo {
  id: string
  platinumData: unknown // opaque; Carbon types live in Platinum's render plugin
  chartType: 'bar' | 'line' | 'pie' | 'stacked'
  title: string
  toolCallId: string
  customer?: string
  job?: string
  spec?: string
}

// Created dashboard info from create_dashboard tool.
// config is unknown — the Platinum render plugin owns the shape.
export interface CreatedDashboardInfo {
  dashboardId: string
  customer: string
  config: unknown
  viewUrl: string
  toolCallId: string
}

// Ask User types
// Custom composition image catalog entry
export interface CustomImageInfo {
  id: string;
  name: string;
  description: string;
  visibility: 'private' | 'organizational';
  customer: string;
  owner_email: string;
  content_hash: string;
  requires_build: boolean;
  created_at: number;
  updated_at: number;
}

export interface AskUserOption {
  label: string
  value: string
  description?: string
  advance?: boolean
}

export interface AskUserQuestionInfo {
  question: string
  options: AskUserOption[]
  allowFreetext: boolean
  context: string
  toolCallId: string
  answered: boolean
  selectedValue?: string
}
