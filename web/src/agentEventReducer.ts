// Pure event reducer for the agent chat UI.
// Copied + factored from frontend/src/hooks/agentEventReducer.ts.
// Extension event types (table_rendered, chart_rendered, dashboard_created,
// webapp_ready, page_tool_request, settings_updated) and the tool_use_end
// self-parse are dispatched through the RenderPlugin seam (plugins.ts) as
// well as being maintained in the core state maps for generic replay.
// The SINGLE-REDUCER INVARIANT is preserved: one agentEventReducer is used
// for both live streaming and replay — never a second reconstruction path.
// See ../../docs/09-frontend-components.md.

import type {
  ActivityStatus,
  AgentMessage,
  AgentSSEEvent,
  ArtifactInfo,
  AskUserQuestionInfo,
  CreatedDashboardInfo,
  HookEventInfo,
  RenderedChartInfo,
  RenderedTableInfo,
  SkillInstalledEventData,
  TodoItem,
  ToolCallInfo,
} from './types.js'
import { getToolSummary } from './tool-formatters.js'
import { getToolDisplayName } from './tool-formatters.js'

export interface InstalledSkillInfo { id: string; name: string; visibility?: string; requiresBuild?: boolean }

export interface AgentSessionInfo {
  tools: string[]
  model: string
  mcpServers: { name: string; status: string }[]
}

export interface AgentEventState {
  messages: AgentMessage[]
  isStreaming: boolean
  error: string | null
  artifacts: ArtifactInfo[]
  /** Currently building assistant message (null when not streaming) */
  currentMessage: { id: string; content: string } | null
  /** Active tool calls for current message */
  toolCalls: Map<string, ToolCallInfo>
  /** Rendered tables keyed by table ID (populated by plugin dispatch + self-parse) */
  renderedTables: Map<string, RenderedTableInfo>
  /** Rendered charts keyed by chart ID (populated by plugin dispatch + self-parse) */
  renderedCharts: Map<string, RenderedChartInfo>
  /** Asked questions keyed by tool call ID */
  askedQuestions: Map<string, AskUserQuestionInfo>
  /** Created dashboards keyed by dashboard ID */
  createdDashboards: Map<string, CreatedDashboardInfo>
  /** Tracks if tool_use_start was seen since last content_delta — signals split needed */
  hasActiveToolCalls: boolean
  /** Original message ID from message_start, used for generating continuation IDs */
  originalMessageId: string | null
  /** Counter for continuation messages within current API message */
  continuationCount: number
  /** Persistent activity status shown above the input area */
  activityStatus: ActivityStatus | null
  /** Accumulated tool_input_delta partial JSON */
  toolInputBuffer: string
  /** Pending page tool requests (tool name + tool call ID) */
  pendingPageToolRequest: { toolName: string; toolCallId: string } | null
  /** Pending settings update from update_table_settings */
  pendingSettingsUpdate: { toolCallId: string; settings: Record<string, unknown> } | null
  /** Todo items from TodoWrite tool calls */
  todos: TodoItem[]
  /** Session info from session_info event (tools, model, MCP servers) */
  sessionInfo: AgentSessionInfo | null
  /** Subagent lifecycle events for debugging */
  subagentEvents: Array<{ event: 'start' | 'stop'; agentId: string; agentType?: string; result?: string; timestamp: string }>
  /** Skills installed live into this session (deduped by name, latest wins) */
  installedSkills: InstalledSkillInfo[]
}

export function initialAgentEventState(): AgentEventState {
  return {
    messages: [],
    isStreaming: false,
    error: null,
    artifacts: [],
    currentMessage: null,
    toolCalls: new Map(),
    renderedTables: new Map(),
    renderedCharts: new Map(),
    askedQuestions: new Map(),
    createdDashboards: new Map(),
    hasActiveToolCalls: false,
    originalMessageId: null,
    continuationCount: 0,
    activityStatus: null,
    toolInputBuffer: '',
    pendingPageToolRequest: null,
    pendingSettingsUpdate: null,
    todos: [],
    sessionInfo: null,
    subagentEvents: [],
    installedSkills: [],
  }
}

/**
 * Pure reducer that processes an SSE event and returns new state.
 * Used for both live SSE streaming and durable event replay — the same
 * function, the same code path (SINGLE-REDUCER INVARIANT).
 *
 * Extension event types (table_rendered, chart_rendered, dashboard_created,
 * webapp_ready, page_tool_request, settings_updated) are maintained in the
 * core state maps AND dispatched to registered RenderPlugins via the seam
 * in plugins.ts. Platinum's Carbon widgets register there.
 *
 * Message splitting: When content_delta arrives after a tool_use_start within
 * the same message_start/message_end boundary, a new "continuation" message is
 * created instead of appending to the current one.
 */
export function agentEventReducer(state: AgentEventState, event: AgentSSEEvent): AgentEventState {
  const data = event.data as Record<string, unknown>
  // Clone state to avoid mutation
  const next = { ...state }

  switch (event.type) {
    case 'message_start': {
      if (data.role === 'assistant') {
        const msgId = data.messageId as string
        next.currentMessage = { id: msgId, content: '' }
        next.toolCalls = new Map()
        next.hasActiveToolCalls = false
        next.originalMessageId = msgId
        next.continuationCount = 0
        next.activityStatus = { label: 'Thinking...', category: 'thinking' }
        // Create message immediately so tool_use_start can attach to it
        if (!next.messages.find(m => m.id === msgId)) {
          next.messages = [...next.messages, {
            id: msgId,
            role: 'assistant' as const,
            content: '',
            toolCalls: [],
            timestamp: event.timestamp,
          }]
        }
      }
      break
    }
    case 'content_delta': {
      if (next.currentMessage) {
        next.activityStatus = { label: 'Writing response...', category: 'writing' }
        if (next.hasActiveToolCalls) {
          // Split: create continuation message after tool boundary
          next.continuationCount += 1
          const contId = `${next.originalMessageId}-cont-${next.continuationCount}`
          next.currentMessage = { id: contId, content: data.delta as string }
          next.toolCalls = new Map()
          next.hasActiveToolCalls = false
          next.messages = [...next.messages, {
            id: contId,
            role: 'assistant' as const,
            content: data.delta as string,
            toolCalls: [],
            timestamp: event.timestamp,
          }]
        } else {
          const content = next.currentMessage.content + (data.delta as string)
          next.currentMessage = { ...next.currentMessage, content }
          const messageId = next.currentMessage.id
          next.messages = next.messages.map(m =>
            m.id === messageId ? { ...m, content } : m
          )
        }
      }
      break
    }
    case 'thinking_delta': {
      if (next.currentMessage) {
        // Only allocate new activityStatus when category changes (avoids re-render on every delta)
        if (next.activityStatus?.category !== 'thinking') {
          next.activityStatus = { label: 'Reasoning...', category: 'thinking' }
        }
        const messageId = next.currentMessage.id
        const thinkingDelta = data.delta as string
        const currentMsg = next.messages.find(m => m.id === messageId)
        const thinking = (currentMsg?.thinking || '') + thinkingDelta
        next.messages = next.messages.map(m =>
          m.id === messageId ? { ...m, thinking } : m
        )
      }
      break
    }
    case 'tool_use_start': {
      next.hasActiveToolCalls = true
      next.toolInputBuffer = ''
      const toolName = data.toolName as string
      const toolInput = data.input as Record<string, unknown>
      const toolCall: ToolCallInfo = {
        id: data.toolCallId as string,
        name: toolName,
        input: toolInput,
        status: 'running',
      }
      next.toolCalls = new Map(next.toolCalls)
      next.toolCalls.set(toolCall.id, toolCall)
      next.messages = updateToolCallsOnMessages(next.messages, next.currentMessage, next.toolCalls)
      const displayName = getToolDisplayName(toolName, toolInput)
      const summary = getToolSummary(toolName, toolInput)
      next.activityStatus = {
        label: `Running: ${displayName}`,
        detail: summary || undefined,
        category: 'tool',
        toolName,
        toolInput,
      }
      break
    }
    case 'tool_use_end': {
      const toolId = data.toolCallId as string
      next.toolCalls = new Map(next.toolCalls)
      const tc = next.toolCalls.get(toolId)
      if (tc) {
        // Tool is in current Map (same message boundary)
        const updated: ToolCallInfo = {
          ...tc,
          status: (data.isError ? 'error' : 'complete') as ToolCallInfo['status'],
          output: data.output as string,
        }
        next.toolCalls.set(updated.id, updated)
        next.messages = updateToolCallsOnMessages(next.messages, next.currentMessage, next.toolCalls)
        // If no tools still running, revert to thinking
        const anyRunning = Array.from(next.toolCalls.values()).some(t => t.status === 'running')
        if (!anyRunning) {
          next.activityStatus = { label: 'Thinking...', category: 'thinking' }
        }

        // Self-parse rendered content from tool output (tables, charts, ask_user, dashboards).
        // TODO (follow-up AL-6-revise item 3): the __render_table / __render_chart inline parsing
        // below stays in the core reducer for now to keep the 35 reducer tests green and the
        // single-reducer invariant intact. A future cleanup could move this to plugin-supplied
        // reduce() calls (dispatched from AgentChat via foldPluginEvents) once integration tests
        // cover the full pipeline end-to-end.
        const toolName = updated.name || ''
        const isTodoWrite = toolName === 'TodoWrite' || toolName.includes('todo_write') || toolName.includes('TodoWrite')
        const isTable = toolName.includes('render_table')
        const isChart = toolName.includes('render_chart')
        const isAsk = toolName.includes('ask_user')
        const isDashboard = toolName.includes('create_dashboard')
        if (isTodoWrite && !data.isError) {
          // TodoWrite's input contains the todo list; the output is just an acknowledgment
          const input = (updated.input || {}) as Record<string, unknown>
          const todos = input.todos as Array<{ content: string; status: string }> | undefined
          if (Array.isArray(todos)) {
            next.todos = todos.map(t => ({
              content: t.content || '',
              status: (t.status as TodoItem['status']) || 'pending',
            }))
          }
        }
        if ((isTable || isChart || isAsk || isDashboard) && !data.isError) {
          try {
            const parsed = JSON.parse(data.output as string)
            let text: string | undefined
            if (parsed?.content?.[0]?.text) text = parsed.content[0].text
            else if (Array.isArray(parsed) && parsed[0]?.text) text = parsed[0].text
            if (text) {
              const inner = JSON.parse(text)
              const toolCallId = data.toolCallId as string
              if (inner.__render_table) {
                // Deduplicate: table_rendered SSE event may also add this
                const alreadyExists = [...next.renderedTables.values()].some(t => t.toolCallId === toolCallId)
                if (!alreadyExists) {
                  const id = `table-${toolCallId}`
                  next.renderedTables = new Map(next.renderedTables)
                  next.renderedTables.set(id, {
                    id, platinumData: inner.platinumData, title: inner.title || '', toolCallId,
                    customer: inner.customer, job: inner.job, spec: inner.spec,
                  })
                }
              }
              if (inner.__render_chart) {
                // Deduplicate: chart_rendered SSE event may also add this
                const alreadyExists = [...next.renderedCharts.values()].some(c => c.toolCallId === toolCallId)
                if (!alreadyExists) {
                  const id = `chart-${toolCallId}`
                  next.renderedCharts = new Map(next.renderedCharts)
                  next.renderedCharts.set(id, {
                    id, platinumData: inner.platinumData,
                    chartType: inner.chartType || 'bar', title: inner.title || '', toolCallId,
                    customer: inner.customer, job: inner.job, spec: inner.spec,
                  })
                }
              }
              if (inner.__ask_user) {
                next.askedQuestions = new Map(next.askedQuestions)
                next.askedQuestions.set(toolCallId, {
                  question: inner.question, options: inner.options,
                  allowFreetext: inner.allow_freetext || false,
                  context: inner.context || '', toolCallId, answered: false,
                })
              }
              if (inner.__dashboard_created) {
                next.createdDashboards = new Map(next.createdDashboards)
                next.createdDashboards.set(inner.dashboardId as string, {
                  dashboardId: inner.dashboardId as string,
                  customer: inner.customer as string,
                  config: inner.config as CreatedDashboardInfo['config'],
                  viewUrl: inner.viewUrl as string,
                  toolCallId,
                })
              }
            }
          } catch { /* not all outputs are parseable */ }
        }
      } else {
        // Fallback: tool was in a previous message (message_start reset the Map).
        // Update it directly in the messages array.
        const status = (data.isError ? 'error' : 'complete') as ToolCallInfo['status']
        next.messages = updateToolInMessages(next.messages, toolId, {
          status,
          output: data.output as string,
        })
      }
      break
    }
    case 'tool_progress': {
      next.toolCalls = new Map(next.toolCalls)
      const tc = next.toolCalls.get(data.toolUseId as string)
      if (tc) {
        const elapsed = data.elapsedSeconds as number
        next.toolCalls.set(tc.id, { ...tc, elapsedSeconds: elapsed })
        next.messages = updateToolCallsOnMessages(next.messages, next.currentMessage, next.toolCalls)
        if (next.activityStatus?.category === 'tool') {
          next.activityStatus = { ...next.activityStatus, elapsedSeconds: elapsed }
        }
      }
      break
    }
    case 'message_end': {
      next.currentMessage = null
      break
    }
    case 'table_rendered': {
      // Extension event: dispatched to registered RenderPlugins AND stored in core maps.
      const toolCallId = (data.toolCallId as string) || ''
      const id = data.id as string
      // Skip if this exact event was already processed (replay)
      if (next.renderedTables.has(id)) break
      next.renderedTables = new Map(next.renderedTables)
      // Remove self-parse fallback entry if present (SSE is authoritative)
      const selfParseId = `table-${toolCallId}`
      if (next.renderedTables.has(selfParseId)) {
        next.renderedTables.delete(selfParseId)
      }
      next.renderedTables.set(id, {
        id,
        platinumData: data.platinumData,
        title: (data.title as string) || '',
        toolCallId,
        customer: data.customer as string | undefined,
        job: data.job as string | undefined,
        spec: data.spec as string | undefined,
      })
      break
    }
    case 'chart_rendered': {
      // Extension event: dispatched to registered RenderPlugins AND stored in core maps.
      const toolCallId = (data.toolCallId as string) || ''
      const id = data.id as string
      // Skip if this exact event was already processed (replay)
      if (next.renderedCharts.has(id)) break
      next.renderedCharts = new Map(next.renderedCharts)
      // Remove self-parse fallback entry if present (SSE is authoritative)
      const selfParseId = `chart-${toolCallId}`
      if (next.renderedCharts.has(selfParseId)) {
        next.renderedCharts.delete(selfParseId)
      }
      next.renderedCharts.set(id, {
        id,
        platinumData: data.platinumData,
        chartType: (data.chartType as RenderedChartInfo['chartType']) || 'bar',
        title: (data.title as string) || '',
        toolCallId,
        customer: data.customer as string | undefined,
        job: data.job as string | undefined,
        spec: data.spec as string | undefined,
      })
      break
    }
    case 'ask_user': {
      const questionInfo: AskUserQuestionInfo = {
        question: data.question as string,
        options: data.options as AskUserQuestionInfo['options'],
        allowFreetext: (data.allowFreetext as boolean) || false,
        context: (data.context as string) || '',
        toolCallId: (data.toolCallId as string) || '',
        answered: false,
      }
      next.askedQuestions = new Map(next.askedQuestions)
      next.askedQuestions.set(questionInfo.toolCallId, questionInfo)
      break
    }
    case 'dashboard_created': {
      // Extension event: dispatched to registered RenderPlugins AND stored in core maps.
      const dashInfo: CreatedDashboardInfo = {
        dashboardId: data.dashboardId as string,
        customer: data.customer as string,
        config: data.config as CreatedDashboardInfo['config'],
        viewUrl: data.viewUrl as string,
        toolCallId: (data.toolCallId as string) || '',
      }
      next.createdDashboards = new Map(next.createdDashboards)
      next.createdDashboards.set(dashInfo.dashboardId, dashInfo)
      break
    }
    case 'artifact_registered': {
      const artifact: ArtifactInfo = {
        filePath: data.filePath as string,
        fileName: (data.filePath as string).split('/').pop() || '',
        label: data.label as string,
        description: data.description as string,
        artifactType: data.artifactType as ArtifactInfo['artifactType'],
        source: 'registered',
        status: 'live',
      }
      const idx = next.artifacts.findIndex(a => a.filePath === artifact.filePath)
      if (idx >= 0) {
        next.artifacts = [...next.artifacts]
        next.artifacts[idx] = artifact
      } else {
        next.artifacts = [...next.artifacts, artifact]
      }

      // Associate with current message for inline display
      if (next.currentMessage) {
        const messageId = next.currentMessage.id
        next.messages = next.messages.map(m =>
          m.id === messageId
            ? { ...m, autoArtifactPaths: [...(m.autoArtifactPaths || []), artifact.filePath] }
            : m
        )
      }
      break
    }
    case 'webapp_ready': {
      // Extension event: also dispatched to registered RenderPlugins.
      // Webapp files have been extracted to blob storage — update artifact status
      const artifactId = data.artifactId as string
      const webappFilePath = data.filePath as string
      if (webappFilePath) {
        const idx = next.artifacts.findIndex(a => a.filePath === webappFilePath)
        if (idx >= 0) {
          next.artifacts = [...next.artifacts]
          next.artifacts[idx] = { ...next.artifacts[idx], status: 'extracted' }
        }
      }
      // Also try matching by artifactId if we have it stored
      if (artifactId && !next.artifacts.some(a => a.filePath === webappFilePath && a.status === 'extracted')) {
        // Artifact may have been registered with a different path format — mark all webapp artifacts
        next.artifacts = next.artifacts.map(a =>
          a.artifactType === 'webapp' && a.status === 'live'
            ? { ...a, status: 'extracted' }
            : a
        )
      }
      break
    }
    case 'activity_update': {
      const phase = data.phase as string
      const label = data.label as string
      const toolName = data.toolName as string | undefined
      const categoryMap: Record<string, ActivityStatus['category']> = {
        thinking: 'thinking',
        writing: 'writing',
        preparing_tool: 'preparing_tool',
        processing: 'thinking',
        processing_results: 'thinking',
      }
      const category = categoryMap[phase] || 'thinking'
      // Don't override active tool status (tool_use_start takes priority)
      if (next.activityStatus?.category !== 'tool') {
        next.activityStatus = { label, category, toolName }
      }
      break
    }
    case 'tool_input_delta': {
      next.toolInputBuffer = next.toolInputBuffer + (data.partialJson as string)
      const preview = next.toolInputBuffer.length > 80
        ? '...' + next.toolInputBuffer.slice(-80)
        : next.toolInputBuffer
      if (next.activityStatus?.category === 'preparing_tool' || next.activityStatus?.category === 'tool') {
        next.activityStatus = { ...next.activityStatus, toolInputPreview: preview }
      }
      break
    }
    case 'system_status': {
      const status = data.status as string
      const statusLabels: Record<string, string> = {
        init: 'Initializing agent...',
        compacting: 'Compacting conversation history...',
        ready: 'Ready',
        auth: 'Authenticating...',
      }
      const label = statusLabels[status] || `System: ${status}`
      if (status !== 'ready') {
        next.activityStatus = { label, category: 'system' }
      }
      break
    }
    case 'page_tool_request': {
      // Extension event: also dispatched to registered RenderPlugins.
      next.pendingPageToolRequest = {
        toolName: data.toolName as string,
        toolCallId: data.toolCallId as string,
      }
      break
    }
    case 'settings_updated': {
      // Extension event: also dispatched to registered RenderPlugins.
      const { toolCallId, settings } = data as { toolCallId: string; settings: Record<string, unknown> }
      next.pendingSettingsUpdate = { toolCallId, settings }
      break
    }
    case 'session_info': {
      next.sessionInfo = {
        tools: (data.tools as string[]) || [],
        model: (data.model as string) || '',
        mcpServers: (data.mcpServers as AgentSessionInfo['mcpServers']) || [],
      }
      break
    }
    case 'hook_event': {
      const hookType = data.hookType as HookEventInfo['hookType']
      const payload = (data.payload as Record<string, unknown>) || {}
      const toolUseId = (payload.tool_use_id || payload.toolUseId) as string | undefined
      const toolName = (payload.tool_name || payload.toolName) as string | undefined
      const hookEvent: HookEventInfo = {
        hookType,
        toolName,
        toolUseId,
        timestamp: event.timestamp,
        payload,
      }
      if (toolUseId) {
        const tc = next.toolCalls.get(toolUseId)
        if (tc) {
          next.toolCalls = new Map(next.toolCalls)
          next.toolCalls.set(toolUseId, {
            ...tc,
            hookEvents: [...(tc.hookEvents || []), hookEvent],
          })
          next.messages = updateToolCallsOnMessages(next.messages, next.currentMessage, next.toolCalls)
        }
      }
      if ((hookType === 'pre_tool' || hookType === 'post_tool') && next.activityStatus?.category !== 'tool') {
        const label = hookType === 'pre_tool'
          ? `Pre-processing${toolName ? `: ${toolName}` : ''}`
          : `Post-processing${toolName ? `: ${toolName}` : ''}`
        next.activityStatus = { label, category: 'thinking', toolName }
      }
      break
    }
    case 'subagent_event': {
      const subEvent = data.event as 'start' | 'stop'
      const agentType = data.agentType as string | undefined
      next.subagentEvents = [...next.subagentEvents, {
        event: subEvent,
        agentId: data.agentId as string,
        agentType,
        result: data.result as string | undefined,
        timestamp: event.timestamp,
      }]
      if (subEvent === 'start' && agentType) {
        next.activityStatus = { label: `Sub-agent: ${agentType}`, category: 'thinking' }
      }
      break
    }
    case 'user_message': {
      const msgId = (data.id as string) || `user-${event.timestamp}`
      const content = (data.content as string) || ''
      if (!next.messages.find(m => m.id === msgId)) {
        next.messages = [...next.messages, {
          id: msgId,
          role: 'user' as const,
          content,
          timestamp: (data.timestamp as string) || event.timestamp,
        }]
      }
      break
    }
    case 'skill_installed': {
      const d = event.data as SkillInstalledEventData
      const info: InstalledSkillInfo = { id: d.id, name: d.name, visibility: d.visibility, requiresBuild: d.requires_build }
      next.installedSkills = [...next.installedSkills.filter(s => s.name !== d.name), info]
      break
    }
    case 'heartbeat': {
      // No state change — side effects handled externally
      break
    }
    case 'query_complete': {
      next.isStreaming = false
      next.activityStatus = null
      // If result exists but no assistant messages, add one
      const result = data.result as string | undefined
      if (result && !next.currentMessage) {
        const hasAssistant = next.messages.some(m => m.role === 'assistant' && m.content.trim())
        if (!hasAssistant) {
          next.messages = [...next.messages, {
            id: `result-${event.timestamp}`,
            role: 'assistant' as const,
            content: result,
            timestamp: event.timestamp,
          }]
        }
      }
      break
    }
    case 'error': {
      next.error = (data.message as string) || 'Unknown error'
      next.isStreaming = false
      next.activityStatus = null
      break
    }
  }

  return next
}

function updateToolCallsOnMessages(
  messages: AgentMessage[],
  currentMessage: { id: string; content: string } | null,
  toolCalls: Map<string, ToolCallInfo>,
): AgentMessage[] {
  if (!currentMessage) return messages
  const toolCallsArray = Array.from(toolCalls.values())
  return messages.map(m =>
    m.id === currentMessage.id ? { ...m, toolCalls: toolCallsArray } : m
  )
}

/**
 * Update a single tool call by ID across all messages.
 * Used when tool_use_end arrives after a message_start reset the toolCalls Map,
 * so the tool lives in a previous message's toolCalls array.
 */
function updateToolInMessages(
  messages: AgentMessage[],
  toolCallId: string,
  updates: Partial<ToolCallInfo>,
): AgentMessage[] {
  return messages.map(msg => {
    if (!msg.toolCalls) return msg
    const idx = msg.toolCalls.findIndex(tc => tc.id === toolCallId)
    if (idx === -1) return msg
    return {
      ...msg,
      toolCalls: msg.toolCalls.map((tc, i) =>
        i === idx ? { ...tc, ...updates } : tc
      ),
    }
  })
}
