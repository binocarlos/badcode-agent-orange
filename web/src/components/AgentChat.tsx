// Agent chat UI component.
// Copied + factored from frontend/src/components/agent/AgentChat.tsx.
// Factoring changes:
//   - Removed InlinePlatinumTable, InlineDashboard, ArtifactViewer (Platinum render plugins).
//   - Removed AGENT_MODELS constant; model list is a prop.
//   - Removed PreparingToolCard; inline equivalent.
//   - ThinkingBlock extracted to its own component (ThinkingBlock.tsx).
//   - Plugin slots for table/chart/dashboard rendering via RenderPlugin seam.
//   - API base URL and auth header as props.
//   - No dependency on AccountContext, router5, or Platinum app state.
// See ../../docs/09-frontend-components.md and ../../docs/90-provenance-map.md.

import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Box, Button, Typography, Alert, Switch, Chip, Divider } from '@mui/material'
import type { ActivityStatus, AgentMessage, ArtifactInfo, AskUserQuestionInfo, CreatedDashboardInfo, RenderedTableInfo, RenderedChartInfo, TodoItem } from '../types.js'
import { getToolCategory, getToolIcon } from '../tool-formatters.js'
import AgentMarkdown from './AgentMarkdown.js'
import ToolCallGroup, { tryParseImageOutput, isImageToolCall } from './ToolCallGroup.js'
import InlineArtifactPreview from './InlineArtifactPreview.js'
import AskUserCard from './AskUserCard.js'
import ArtifactPanel from './ArtifactPanel.js'
import ThinkingBlock from './ThinkingBlock.js'
import ChatInputToolbar from './ChatInputToolbar.js'
import useFileAttachments from '../hooks/useFileAttachments.js'
import useVoiceDictation from '../hooks/useVoiceDictation.js'
import type { RenderPlugin, AgentSSEEvent } from '../plugins.js'
import type { AgentSSEEvent as CoreSSEEvent } from '../types.js'
import { useAgentChatContextOptional } from '../AgentChatProvider.js'

/**
 * Fold plugin events into per-plugin, per-toolCallId state maps.
 *
 * Pure — replay-safe: same input events always produce the same output.
 *
 * Returns Map<pluginIndex, Map<toolCallId, TState>>.
 * Keyed by plugin index so two plugins sharing an event type never collide.
 *
 * Accepts CoreSSEEvent (data: unknown from types.ts) so hosts can pass the
 * pluginEvents array returned directly from useAgentSession without a cast.
 * The data fields are accessed via an explicit cast inside.
 */
export function foldPluginEvents(
  plugins: RenderPlugin[],
  pluginEvents: CoreSSEEvent[],
): Map<number, Map<string, unknown>> {
  const byPlugin = new Map<number, Map<string, unknown>>()
  for (let i = 0; i < plugins.length; i++) {
    const plugin = plugins[i]
    const typeSet = new Set(plugin.eventTypes)
    const byTool = new Map<string, unknown>()
    for (const ev of pluginEvents) {
      if (!typeSet.has(ev.type)) continue
      // Cast data to Record so we can extract the toolCallId key. Plugin.reduce()
      // receives the event typed as plugins.AgentSSEEvent (data: Record<string,unknown>)
      // — the cast is safe because any event processed here went through the SSE parser
      // which always produces an object for data.
      const d = ev.data as Record<string, unknown>
      const key =
        (d?.toolCallId as string | undefined) ||
        (d?.tool_use_id as string | undefined) ||
        (d?.messageId as string | undefined) ||
        ''
      const pluginEv: AgentSSEEvent = { type: ev.type, data: d, timestamp: ev.timestamp }
      const prev = byTool.has(key) ? byTool.get(key) : plugin.init()
      byTool.set(key, plugin.reduce(prev, pluginEv))
    }
    byPlugin.set(i, byTool)
  }
  return byPlugin
}

interface SubagentEventInfo {
  event: 'start' | 'stop'
  agentId: string
  agentType?: string
  result?: string
  timestamp: string
}

interface AgentChatProps {
  // All props are optional so <AgentChat/> can be used inside <AgentChatProvider>
  // with zero props. Each value falls back to the context when omitted.
  messages?: AgentMessage[]
  isStreaming?: boolean
  error?: string | null
  activityStatus?: ActivityStatus | null
  onSendMessage?: (content: string, model?: string, attachmentIds?: string[]) => void
  onCancel?: () => void
  artifacts?: ArtifactInfo[]
  todos?: TodoItem[]
  renderedTables?: Map<string, RenderedTableInfo>
  renderedCharts?: Map<string, RenderedChartInfo>
  askedQuestions?: Map<string, AskUserQuestionInfo>
  createdDashboards?: Map<string, CreatedDashboardInfo>
  onAnswerQuestion?: (toolCallId: string, value: string) => void
  sessionId?: string
  toolInputBuffer?: string
  subagentEvents?: SubagentEventInfo[]
  selectedModel?: string
  onModelChange?: (model: string) => void
  /** Model list for the model selector. */
  models?: { id: string; label: string }[]
  lastHeartbeat?: number
  stuckStatus?: 'ok' | 'possibly_stuck' | 'likely_stuck'
  onNudge?: () => void
  readOnly?: boolean
  /** Render plugins (e.g. Platinum's Carbon table/chart widgets). */
  plugins?: RenderPlugin[]
  /**
   * Raw plugin events accumulated by useAgentSession (live + restored).
   * Folded per-plugin per-toolCallId by AgentChat via foldPluginEvents.
   * Pass useAgentSession's `pluginEvents` return value here directly.
   */
  pluginEvents?: CoreSSEEvent[]
  /** API base URL for artifact downloads. */
  apiBaseUrl?: string
  /** Auth header for artifact downloads and voice transcription. */
  authHeader?: string
  /** Transcription endpoint. Default: apiBaseUrl + /transcribe */
  transcribeEndpoint?: string
  /** Optional upload endpoint factory for file attachments. */
  uploadEndpoint?: (sessionId: string) => string
  /** Optional callback when user clicks "add artifact" (e.g. pin to dashboard). */
  onPinToDashboard?: (sessionId: string) => void
  /** Optional callback to open the full artifact viewer. */
  onOpenArtifactViewer?: (artifact: ArtifactInfo | null) => void
  forkedMessageCount?: number
}

export default function AgentChat(props: AgentChatProps) {
  // Read optional context — null when used outside a Provider (legacy prop-only usage).
  const ctx = useAgentChatContextOptional()

  // Resolve each value: explicit prop takes precedence, then context, then empty default.
  const messages        = props.messages        ?? ctx?.messages        ?? []
  const isStreaming     = props.isStreaming      ?? ctx?.isStreaming     ?? false
  const error           = props.error           ?? ctx?.error           ?? null
  const activityStatus  = props.activityStatus  ?? ctx?.activityStatus  ?? null
  const onSendMessage   = props.onSendMessage   ?? ctx?.sendMessage     ?? (() => {})
  const onCancel        = props.onCancel        ?? ctx?.cancelSession   ?? (() => {})
  const artifacts       = props.artifacts       ?? ctx?.artifacts       ?? []
  const todos           = props.todos           ?? ctx?.todos           ?? []
  const renderedTables  = props.renderedTables  ?? ctx?.renderedTables  ?? new Map()
  const renderedCharts  = props.renderedCharts  ?? ctx?.renderedCharts  ?? new Map()
  const askedQuestions  = props.askedQuestions  ?? ctx?.askedQuestions  ?? new Map()
  const createdDashboards = props.createdDashboards ?? ctx?.createdDashboards ?? new Map()
  const onAnswerQuestion = props.onAnswerQuestion ?? ctx?.markQuestionAnswered ?? (() => {})
  const sessionId       = props.sessionId       ?? ctx?.session?.id    ?? ''
  const toolInputBuffer = props.toolInputBuffer ?? ctx?.toolInputBuffer ?? ''
  const subagentEvents  = props.subagentEvents  ?? (ctx?.subagentEvents as SubagentEventInfo[] | undefined)
  const selectedModel   = props.selectedModel   ?? ctx?.selectedModel  ?? ''
  const onModelChange   = props.onModelChange   ?? ctx?.setSelectedModel ?? (() => {})
  const models          = props.models          ?? ctx?.config.models   ?? []
  const _lastHeartbeat  = props.lastHeartbeat   ?? ctx?.lastHeartbeat
  const stuckStatus     = props.stuckStatus     ?? ctx?.stuckStatus
  const onNudge         = props.onNudge         ?? ctx?.nudgeAgent
  const readOnly        = props.readOnly
  const plugins         = props.plugins         ?? ctx?.config.plugins ?? []
  const pluginEvents    = props.pluginEvents    ?? ctx?.pluginEvents   ?? []
  const apiBaseUrl      = props.apiBaseUrl      ?? ctx?.config.apiBaseUrl ?? ''
  const authHeader      = props.authHeader
  const transcribeEndpoint = props.transcribeEndpoint
  const uploadEndpoint  = props.uploadEndpoint
  const onPinToDashboard = props.onPinToDashboard
  const onOpenArtifactViewer = props.onOpenArtifactViewer
  const forkedMessageCount = props.forkedMessageCount

  const [input, setInput] = useState('')
  const [viewerArtifact, setViewerArtifact] = useState<ArtifactInfo | null>(null)
  const messagesEndRef = useRef<HTMLDivElement>(null)
  const scrollContainerRef = useRef<HTMLDivElement>(null)
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  const prevMsgCountRef = useRef(0)
  const prevMsgLenRef = useRef(messages.length)
  const prevStreamingRef = useRef(isStreaming)

  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && isStreaming && !readOnly) {
        e.preventDefault()
        onCancel()
      }
    }
    window.addEventListener('keydown', handleKeyDown)
    return () => window.removeEventListener('keydown', handleKeyDown)
  }, [isStreaming, readOnly, onCancel])

  const fileAttachments = useFileAttachments({
    uploadEndpoint,
    authHeader,
  })

  const handleTranscription = useCallback((text: string) => {
    setInput(prev => prev ? prev + ' ' + text : text)
  }, [])

  const resolvedTranscribeEndpoint = transcribeEndpoint || `${apiBaseUrl}/transcribe`

  const voiceDictation = useVoiceDictation({
    onTranscription: handleTranscription,
    transcribeEndpoint: resolvedTranscribeEndpoint,
    authHeader,
  })

  const handleToggleRecording = useCallback(() => {
    if (voiceDictation.isRecording) {
      voiceDictation.stopRecording()
    } else {
      voiceDictation.startRecording()
    }
  }, [voiceDictation.isRecording, voiceDictation.stopRecording, voiceDictation.startRecording])

  const handleSubmit = useCallback(async (e: React.FormEvent) => {
    e.preventDefault()
    if (!input.trim() || isStreaming) return
    let attachmentIds: string[] | undefined
    if (fileAttachments.attachments.length > 0) {
      attachmentIds = await fileAttachments.uploadAll(sessionId)
      fileAttachments.clear()
    }
    onSendMessage(input.trim(), selectedModel, attachmentIds)
    setInput('')
  }, [input, isStreaming, onSendMessage, selectedModel, fileAttachments.attachments.length, fileAttachments.uploadAll, fileAttachments.clear, sessionId])

  const handleKeyDown = useCallback((e: React.KeyboardEvent) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      handleSubmit(e as unknown as React.FormEvent)
    }
  }, [handleSubmit])

  // Fold plugin events into per-plugin, per-toolCallId state.
  // Pure — replay-safe. Keyed by plugin index so two plugins sharing an
  // event type do not collide. Depends on plugins identity + pluginEvents array.
  const byPlugin = useMemo(
    () => foldPluginEvents(plugins, pluginEvents),
    [plugins, pluginEvents],
  )

  // Build two maps of artifacts per message
  const { autoArtifacts, toolArtifacts } = useMemo(() => {
    const autoMap = new Map<string, ArtifactInfo[]>()
    const toolMap = new Map<string, ArtifactInfo[]>()
    const claimedPaths = new Set<string>()

    for (let i = messages.length - 1; i >= 0; i--) {
      const message = messages[i]
      const toolPaths: string[] = []
      const autoPaths: string[] = []

      if (message.toolCalls) {
        for (const tc of message.toolCalls) {
          if (tc.name === 'register_artifact' || tc.name === 'mcp__ui__register_artifact' || tc.name === 'mcp__data__register_artifact') {
            const filePath = tc.input?.file_path as string | undefined
            if (filePath && !claimedPaths.has(filePath)) toolPaths.push(filePath)
          }
        }
      }

      if (message.autoArtifactPaths) {
        for (const p of message.autoArtifactPaths) {
          if (!claimedPaths.has(p)) autoPaths.push(p)
        }
      }

      for (const p of [...toolPaths, ...autoPaths]) claimedPaths.add(p)

      if (toolPaths.length > 0) {
        const matched = artifacts.filter(a => toolPaths.some(p => a.filePath === p || a.filePath.endsWith(p)))
        if (matched.length > 0) toolMap.set(message.id, matched)
      }
      if (autoPaths.length > 0) {
        const matched = artifacts.filter(a =>
          a.artifactType !== 'webapp' &&
          autoPaths.some(p => a.filePath === p || a.filePath.endsWith(p))
        )
        if (matched.length > 0) autoMap.set(message.id, matched)
      }
    }
    return { autoArtifacts: autoMap, toolArtifacts: toolMap }
  }, [messages, artifacts])

  // Build maps of tool call IDs to rendered tables/charts
  const toolCallTables = useMemo(() => {
    const map = new Map<string, RenderedTableInfo[]>()
    for (const table of renderedTables.values()) {
      if (table.toolCallId) {
        const existing = map.get(table.toolCallId) || []
        existing.push(table)
        map.set(table.toolCallId, existing)
      }
    }
    return map
  }, [renderedTables])

  const toolCallCharts = useMemo(() => {
    const map = new Map<string, RenderedChartInfo[]>()
    for (const chart of renderedCharts.values()) {
      if (chart.toolCallId) {
        const existing = map.get(chart.toolCallId) || []
        existing.push(chart)
        map.set(chart.toolCallId, existing)
      }
    }
    return map
  }, [renderedCharts])

  const toolCallQuestions = useMemo(() => {
    const map = new Map<string, AskUserQuestionInfo>()
    for (const q of askedQuestions.values()) {
      if (q.toolCallId) map.set(q.toolCallId, q)
    }
    return map
  }, [askedQuestions])

  const toolCallDashboards = useMemo(() => {
    const map = new Map<string, CreatedDashboardInfo>()
    for (const dash of createdDashboards.values()) {
      if (dash.toolCallId) map.set(dash.toolCallId, dash)
    }
    return map
  }, [createdDashboards])

  const displayMessages = messages

  if (messages.length !== prevMsgLenRef.current) {
    prevMsgLenRef.current = messages.length
  }

  if (prevStreamingRef.current && !isStreaming) {
    textareaRef.current?.focus()
  }
  prevStreamingRef.current = isStreaming

  if (displayMessages.length > prevMsgCountRef.current) {
    messagesEndRef.current?.scrollIntoView({ behavior: 'smooth' })
  }
  prevMsgCountRef.current = displayMessages.length

  if (isStreaming && scrollContainerRef.current) {
    const el = scrollContainerRef.current
    const isNearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 150
    if (isNearBottom) {
      el.scrollTop = el.scrollHeight
    }
  }

  // Helper: render plugin slots for a tool call ID.
  // Only renders when the plugin has accumulated state for this specific toolCallId
  // (i.e. at least one plugin event arrived with that toolCallId). This is correct:
  // if no event arrived for a toolCallId, there is nothing to render.
  const renderPluginSlots = (toolCallId: string): React.ReactNode => {
    if (plugins.length === 0) return null
    const slots: React.ReactNode[] = []
    for (let i = 0; i < plugins.length; i++) {
      const plugin = plugins[i]
      const state = byPlugin.get(i)?.get(toolCallId)
      if (state !== undefined) {
        const node = plugin.render({ state, toolCallId, sessionId })
        if (node) slots.push(<React.Fragment key={i}>{node}</React.Fragment>)
      }
    }
    return slots.length > 0 ? <>{slots}</> : null
  }

  // Determine model toggle elements
  const hasModelToggle = models.length >= 2

  const handleOpenPreview = useCallback((a: ArtifactInfo) => {
    setViewerArtifact(a)
    if (onOpenArtifactViewer) onOpenArtifactViewer(a)
  }, [onOpenArtifactViewer])

  return (
    <Box sx={{ display: 'flex', flex: 1, minHeight: 0 }}>
      {/* Chat area */}
      <Box sx={{ display: 'flex', flexDirection: 'column', flex: 1, minWidth: 0, minHeight: 0 }}>
        {/* Messages */}
        <Box ref={scrollContainerRef} sx={{ flex: 1, overflow: 'auto', p: 2, position: 'relative' }}>
          {displayMessages.map((message, index) => {
            const hasContent = message.role === 'user' || message.content.trim()
            const hasThinking = !!message.thinking
            const hasTools = message.toolCalls && message.toolCalls.length > 0
            const hasArtifacts = autoArtifacts.has(message.id) || toolArtifacts.has(message.id)
            if (!hasContent && !hasThinking && !hasTools && !hasArtifacts) return null
            const showForkDivider = forkedMessageCount != null && forkedMessageCount > 0 && index === forkedMessageCount - 1
            return (
              <React.Fragment key={message.id}>
                <Box
                  data-role={message.role}
                  sx={{
                    mb: 2,
                    display: 'flex',
                    flexDirection: 'column',
                    alignItems: message.role === 'user' ? 'flex-end' : 'flex-start',
                  }}
                >
                  {/* Thinking block (extracted component) */}
                  {message.thinking && (
                    <ThinkingBlock
                      content={message.thinking}
                      isActivelyThinking={
                        isStreaming
                        && activityStatus?.category === 'thinking'
                        && index === displayMessages.length - 1
                      }
                    />
                  )}
                  {/* Hide empty assistant bubbles during streaming */}
                  {hasContent && (
                    <Box
                      sx={{
                        maxWidth: '80%',
                        p: '12px 16px',
                        borderRadius: 0,
                        backgroundColor: message.role === 'user' ? 'primary.main' : 'background.default',
                        color: message.role === 'user' ? 'white' : 'text.primary',
                        borderLeft: message.role === 'assistant' ? '3px solid #00B2FF' : 'none',
                        wordBreak: 'break-word',
                        fontSize: '0.8125rem',
                        lineHeight: 1.6,
                        ...(message.role === 'user' ? { whiteSpace: 'pre-wrap' } : {}),
                      }}
                    >
                      {message.role === 'assistant' ? (
                        <AgentMarkdown content={message.content} />
                      ) : (
                        message.content
                      )}
                    </Box>
                  )}
                  {/* Auto-registered artifact previews */}
                  {autoArtifacts.get(message.id)?.map((artifact, i) => (
                    <Box key={`auto-artifact-${message.id}-${i}`} sx={{ maxWidth: artifact.artifactType === 'webapp' ? '100%' : '80%', ...(artifact.artifactType === 'webapp' ? { alignSelf: 'stretch' } : {}), mt: 1 }}>
                      <InlineArtifactPreview
                        artifact={artifact}
                        sessionId={sessionId}
                        onOpenPreview={handleOpenPreview}
                        apiBaseUrl={apiBaseUrl}
                        authHeader={authHeader}
                      />
                    </Box>
                  ))}
                  {/* Tool calls */}
                  {hasTools && (
                    <Box sx={{ maxWidth: '80%', mt: hasContent ? 1 : 0 }}>
                      <ToolCallGroup toolCalls={message.toolCalls!} />
                      {/* Inline tables/charts from renderedTables/Charts — dispatched through plugins when registered */}
                      {message.toolCalls!.map(tc => (
                        <React.Fragment key={tc.id}>
                          {/* Render plugin slots (Platinum table/chart/dashboard widgets register here) */}
                          {renderPluginSlots(tc.id)}
                          {/* Fallback: show raw table/chart state for hosts without plugins.
                              When plugins are registered (e.g. Platinum's Carbon widgets) the host
                              owns rendering via renderPluginSlots above, so suppress these debug
                              cards — otherwise a stray "📈 Chart: X" leaks in next to the widget. */}
                          {plugins.length === 0 && (toolCallTables.get(tc.id) || []).map(table => (
                            <Box key={table.id} sx={{ mt: 1, p: 1.5, border: '1px solid #e5e7eb', borderRadius: 1, backgroundColor: '#f9fafb', fontSize: 12, color: '#6b7280' }}>
                              📊 Table: {table.title || table.id}
                            </Box>
                          ))}
                          {plugins.length === 0 && (toolCallCharts.get(tc.id) || []).map(chart => (
                            <Box key={chart.id} sx={{ mt: 1, p: 1.5, border: '1px solid #e5e7eb', borderRadius: 1, backgroundColor: '#f9fafb', fontSize: 12, color: '#6b7280' }}>
                              📈 Chart: {chart.title || chart.id}
                            </Box>
                          ))}
                          {toolCallQuestions.has(tc.id) && (
                            <AskUserCard
                              question={toolCallQuestions.get(tc.id)!}
                              onAnswer={(value) => {
                                onAnswerQuestion(tc.id, value)
                                onSendMessage(value)
                              }}
                              disabled={isStreaming || toolCallQuestions.get(tc.id)!.answered}
                            />
                          )}
                          {plugins.length === 0 && toolCallDashboards.has(tc.id) && (
                            <Box sx={{ mt: 1, p: 1.5, border: '1px solid #e5e7eb', borderRadius: 1, backgroundColor: '#f9fafb', fontSize: 12, color: '#6b7280' }}>
                              🗂️ Dashboard created
                            </Box>
                          )}
                        </React.Fragment>
                      ))}
                    </Box>
                  )}
                  {/* View image tool results */}
                  {hasTools && message.toolCalls!.filter(tc => isImageToolCall(tc) && tc.output && tryParseImageOutput(tc.output!)).map(tc => {
                    const imgData = tryParseImageOutput(tc.output!)!
                    const filePath = (tc.input?.file_path as string) || 'image.png'
                    const fileName = filePath.split('/').pop() || 'Image'
                    const dataUrl = `data:${imgData.mimeType};base64,${imgData.base64}`
                    const syntheticArtifact: ArtifactInfo = {
                      filePath,
                      fileName,
                      mimeType: imgData.mimeType,
                      label: fileName,
                      artifactType: 'image',
                      source: 'auto',
                      status: 'live',
                      downloadUrl: dataUrl,
                    }
                    return (
                      <Box key={`view-image-${tc.id}`} sx={{ maxWidth: '80%', mt: 1 }}>
                        <InlineArtifactPreview
                          artifact={syntheticArtifact}
                          sessionId={sessionId}
                          dataUrl={dataUrl}
                          onOpenPreview={handleOpenPreview}
                          apiBaseUrl={apiBaseUrl}
                          authHeader={authHeader}
                        />
                      </Box>
                    )
                  })}
                  {/* Inline artifact previews from register_artifact tool calls */}
                  {toolArtifacts.get(message.id)?.map((artifact, i) => (
                    <Box key={`tool-artifact-${message.id}-${i}`} sx={{ maxWidth: artifact.artifactType === 'webapp' ? '100%' : '80%', ...(artifact.artifactType === 'webapp' ? { alignSelf: 'stretch' } : {}), mt: 1 }}>
                      <InlineArtifactPreview
                        artifact={artifact}
                        sessionId={sessionId}
                        onOpenPreview={handleOpenPreview}
                        apiBaseUrl={apiBaseUrl}
                        authHeader={authHeader}
                      />
                    </Box>
                  ))}
                </Box>
                {showForkDivider && (
                  <Divider sx={{ my: 2, borderStyle: 'dashed', borderColor: '#d1d5db' }}>
                    <Chip
                      label="Forked from published app — new messages below"
                      size="small"
                      sx={{ fontSize: 11, color: '#6b7280', backgroundColor: '#f9fafb', fontWeight: 500 }}
                    />
                  </Divider>
                )}
              </React.Fragment>
            )
          })}

          {error && (
            <Alert severity="error" sx={{ mt: 1, fontSize: 13 }}>
              {error}
            </Alert>
          )}

          <div ref={messagesEndRef} />
        </Box>

        {/* Preparing tool card (file write preview) */}
        {!readOnly && isStreaming && activityStatus?.category === 'preparing_tool' && activityStatus.toolName && toolInputBuffer && (
          <Box sx={{ px: 2, py: 1, borderTop: '1px solid #f3f4f6', backgroundColor: '#f9fafb', display: 'flex', alignItems: 'center', gap: 1 }}>
            <Box sx={{ width: 14, height: 14, border: '2px solid #e5e7eb', borderTopColor: '#6b7280', borderRadius: '50%', animation: 'spin 0.8s linear infinite', '@keyframes spin': { '100%': { transform: 'rotate(360deg)' } }, flexShrink: 0 }} />
            <Typography sx={{ fontSize: 12, color: '#6b7280', fontFamily: 'monospace', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
              {activityStatus.toolName}: {toolInputBuffer.slice(-120)}
            </Typography>
          </Box>
        )}

        {/* System status */}
        {!readOnly && !isStreaming && activityStatus?.category === 'system' && (
          <Box sx={{ px: 2, py: '6px', display: 'flex', alignItems: 'center', gap: 1, fontSize: 13, color: '#6b7280', borderTop: '1px solid #f3f4f6' }}>
            <Box sx={{ width: 16, height: 16, border: '2px solid #e5e7eb', borderTopColor: '#6b7280', borderRadius: '50%', animation: 'spin 0.8s linear infinite', '@keyframes spin': { '100%': { transform: 'rotate(360deg)' } }, flexShrink: 0 }} />
            <span style={{ fontWeight: 500 }}>{activityStatus.label}</span>
          </Box>
        )}

        {/* Activity status line */}
        {!readOnly && isStreaming && activityStatus && (
          <Box sx={{ px: 2, py: '6px', display: 'flex', alignItems: 'center', gap: 1, fontSize: 13, color: '#6b7280', borderTop: '1px solid #f3f4f6' }}>
            <Box sx={{ width: 16, height: 16, border: '2px solid #e5e7eb', borderTopColor: '#6b7280', borderRadius: '50%', animation: 'spin 0.8s linear infinite', '@keyframes spin': { '100%': { transform: 'rotate(360deg)' } }, flexShrink: 0 }} />
            {activityStatus.toolName && (
              <span>{getToolIcon(getToolCategory(activityStatus.toolName, activityStatus.toolInput))}</span>
            )}
            <span style={{ fontWeight: 500 }}>{activityStatus.label}</span>
            {activityStatus.detail && (
              <span style={{ color: '#9ca3af' }}>{activityStatus.detail}</span>
            )}
            {activityStatus.elapsedSeconds != null && activityStatus.elapsedSeconds > 0 && (
              <span style={{ color: '#9ca3af' }}>{activityStatus.elapsedSeconds}s</span>
            )}
            {activityStatus.category === 'preparing_tool' && activityStatus.toolInputPreview && !toolInputBuffer && (
              <span style={{ color: '#9ca3af', fontFamily: 'monospace', fontSize: 11, maxWidth: 200, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', display: 'inline-block' }}>{activityStatus.toolInputPreview}</span>
            )}
          </Box>
        )}

        {/* Active sub-agent indicators */}
        {!readOnly && isStreaming && subagentEvents && (() => {
          const stoppedIds = new Set(subagentEvents.filter(e => e.event === 'stop').map(e => e.agentId))
          const active = subagentEvents.filter(e => e.event === 'start' && !stoppedIds.has(e.agentId))
          if (active.length === 0) return null
          return (
            <Box sx={{ px: 2, py: '4px', display: 'flex', gap: 1, flexWrap: 'wrap' }}>
              {active.map(a => (
                <Chip key={a.agentId} size="small" label={`${a.agentType || 'Sub-agent'} running...`} sx={{ fontSize: 11, height: 22, backgroundColor: '#eef2ff', color: '#4338ca', fontWeight: 500 }} />
              ))}
            </Box>
          )
        })()}

        {/* Possibly stuck banner */}
        {!readOnly && isStreaming && stuckStatus === 'possibly_stuck' && (
          <Box sx={{ px: 2, py: 1, display: 'flex', alignItems: 'center', gap: 1.5, backgroundColor: '#fffbeb', borderTop: '1px solid #fde68a' }}>
            <Typography sx={{ fontSize: 13, flex: 1, color: '#92400e' }}>
              The agent has been quiet for a while. It may be working on something complex.
            </Typography>
            <Button variant="outlined" color="info" size="small" onClick={onCancel} sx={{ textTransform: 'none', fontSize: 12, whiteSpace: 'nowrap' }}>
              Stop
            </Button>
          </Box>
        )}

        {/* Stuck detection banner */}
        {!readOnly && isStreaming && stuckStatus === 'likely_stuck' && (
          <Box sx={{ px: 2, py: 1, display: 'flex', alignItems: 'center', gap: 1.5, backgroundColor: '#fef2f2', borderTop: '1px solid #fecaca' }}>
            <Typography sx={{ fontSize: 13, flex: 1, color: '#991b1b' }}>
              The agent appears to be stuck. No activity for over 2 minutes.
            </Typography>
            {onNudge && (
              <Button variant="contained" color="info" size="small" onClick={onNudge} sx={{ textTransform: 'none', fontSize: 12, whiteSpace: 'nowrap' }}>
                Send Nudge
              </Button>
            )}
            <Button variant="outlined" color="info" size="small" onClick={onCancel} sx={{ textTransform: 'none', fontSize: 12, whiteSpace: 'nowrap' }}>
              Stop
            </Button>
          </Box>
        )}

        {/* Input */}
        {!readOnly && (
          <Box
            component="form"
            onSubmit={handleSubmit}
            onDragOver={(e: React.DragEvent) => { e.preventDefault(); e.stopPropagation() }}
            onDrop={(e: React.DragEvent) => {
              e.preventDefault()
              e.stopPropagation()
              if (e.dataTransfer.files.length > 0) {
                fileAttachments.addFiles(e.dataTransfer.files)
              }
            }}
            sx={{ p: '12px 16px', borderTop: '1px solid #e5e7eb', display: 'flex', flexDirection: 'column', gap: 1 }}
          >
            <Box sx={{ display: 'flex', gap: 1, alignItems: 'center' }}>
              {/* Model toggle (only shown when 2 models provided) */}
              {hasModelToggle && (
                <Box sx={{ display: 'flex', alignItems: 'center', flexShrink: 0 }}>
                  <Typography sx={{ fontSize: 13, fontWeight: selectedModel === models[0].id ? 600 : 400, color: selectedModel === models[0].id ? '#1f2937' : '#9ca3af' }}>
                    {models[0].label}
                  </Typography>
                  <Switch
                    size="small"
                    checked={selectedModel === models[1].id}
                    onChange={(_e, checked) => onModelChange(checked ? models[1].id : models[0].id)}
                    disabled={isStreaming}
                    sx={{ mx: 0.5 }}
                  />
                  <Typography sx={{ fontSize: 13, fontWeight: selectedModel === models[1].id ? 600 : 400, color: selectedModel === models[1].id ? '#1f2937' : '#9ca3af' }}>
                    {models[1].label}
                  </Typography>
                </Box>
              )}
              <ChatInputToolbar
                onFilesSelected={files => fileAttachments.addFiles(files)}
                attachments={fileAttachments.attachments}
                onRemoveAttachment={fileAttachments.removeFile}
                isRecording={voiceDictation.isRecording}
                isTranscribing={voiceDictation.isTranscribing}
                onToggleRecording={handleToggleRecording}
                onStopRecording={voiceDictation.stopRecording}
                onCancelRecording={voiceDictation.cancelRecording}
                stream={voiceDictation.stream}
                error={voiceDictation.error}
                disabled={isStreaming}
                devices={voiceDictation.devices}
                selectedDeviceId={voiceDictation.selectedDeviceId}
                onSelectDevice={voiceDictation.selectDevice}
              />
            </Box>
            <Box sx={{ display: 'flex', gap: 1, alignItems: 'flex-end' }}>
              <Box
                ref={textareaRef}
                component="textarea"
                value={input}
                onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setInput(e.target.value)}
                onKeyDown={handleKeyDown}
                onPaste={(e: React.ClipboardEvent) => fileAttachments.handlePaste(e.nativeEvent)}
                placeholder="Type a message..."
                rows={1}
                disabled={isStreaming}
                sx={{
                  flex: 1,
                  p: '8px 12px',
                  border: '1px solid #d1d5db',
                  borderRadius: '8px',
                  resize: 'none',
                  fontSize: 14,
                  fontFamily: 'inherit',
                  outline: 'none',
                  '&:focus': { borderColor: '#3b82f6' },
                }}
              />
              {isStreaming ? (
                <Button type="button" aria-label="Stop" onClick={onCancel} variant="contained" color="info" sx={{ borderRadius: '8px', textTransform: 'none' }}>
                  Stop
                </Button>
              ) : (
                <Button type="submit" aria-label="Send" disabled={!input.trim()} variant="contained" color="info" sx={{ borderRadius: '8px', textTransform: 'none' }}>
                  Send
                </Button>
              )}
            </Box>
          </Box>
        )}
      </Box>

      {/* Artifact Panel */}
      {!readOnly && (
        <ArtifactPanel
          artifacts={artifacts}
          todos={todos}
          sessionId={sessionId}
          onPinToDashboard={onPinToDashboard}
          onArtifactClick={(a) => {
            setViewerArtifact(a)
            if (onOpenArtifactViewer) onOpenArtifactViewer(a)
          }}
          onViewAll={() => {
            if (onOpenArtifactViewer) onOpenArtifactViewer(null)
          }}
          apiBaseUrl={apiBaseUrl}
          authHeader={authHeader}
        />
      )}
    </Box>
  )
}
