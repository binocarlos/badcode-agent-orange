// Agent session hook — manages session lifecycle, SSE streaming, reconnection,
// stuck detection, and event replay.
// Copied + parameterised from frontend/src/hooks/useAgentSession.ts.
// Endpoints, model selection, and callbacks are props/config instead of
// hard-coded Platinum routes. No dependency on AccountContext, app router,
// or Platinum constants. See ../../docs/90-provenance-map.md.

import { useCallback, useRef, useState } from 'react'
import type {
  ActivityStatus,
  AgentMessage,
  AgentMessageSearchResult,
  AgentSession,
  AgentSessionListItem,
  AgentSSEEvent,
  ArtifactInfo,
  AskUserQuestionInfo,
  CreateAgentSessionRequest,
  CreatedDashboardInfo,
  OpProgress,
  PersistedAgentMessage,
  RenderedChartInfo,
  RenderedTableInfo,
  TodoItem,
} from './types.js'
import { replayEvents, replayFromPersistedMessages } from './replayEvents.js'
import { agentEventReducer, initialAgentEventState, type AgentEventState } from './agentEventReducer.js'
import type { AgentChatEndpoints } from './plugins.js'
import { DEFAULT_ENDPOINTS } from './plugins.js'

const AGENT_QUERY_TIMEOUT_MS = 5 * 60 * 1000 // 5 minutes

// Module-level poll/timer variables — survive component unmount/remount cycles.
let _containerPollTimer: ReturnType<typeof setInterval> | null = null
let _containerPollSessionId: string | null = null
let _containerPollInterval = 2000
let _lastNotifiedOp = ''
let _stuckTimer: ReturnType<typeof setInterval> | null = null

const STABLE_CONTAINER_STATES = new Set(['running', 'snapshotted', 'destroyed'])

export interface PersonaInfo {
  name: string
  prompt: string
  source: 'default' | 'custom'
}

export interface UseAgentSessionOptions {
  /** API base URL. Default: empty string (same origin). */
  apiBaseUrl?: string
  /** Override endpoint paths. */
  endpoints?: Partial<AgentChatEndpoints>
  /** Default model ID to use for new sessions. */
  defaultModel?: string
  /** Auth header value (e.g. "Bearer <token>"). Sent with all requests. */
  authHeader?: string
  /**
   * Async (or sync) token provider. If provided, resolves a fresh token on
   * every request and sets the Authorization header. Takes precedence over
   * `authHeader` when both are set.
   */
  getAuthToken?: () => Promise<string> | string
  /**
   * Event types that belong to registered RenderPlugins — e.g.
   * `plugins.flatMap(p => p.eventTypes)`. Events of these types are
   * accumulated in the returned `pluginEvents` array and forwarded to
   * `<AgentChat pluginEvents={...} />` so plugins can fold them per toolCallId.
   */
  pluginEventTypes?: string[]
  onToolResult?: (toolName: string, toolCallId: string, output: string, input: Record<string, unknown>) => void
  onSessionTitle?: (sessionId: string, title: string) => void
  onArtifactsUpdated?: (sessionId: string) => void
  onSnapshotComplete?: (sessionId: string, progress: OpProgress) => void
}

interface UseAgentSessionReturn {
  session: AgentSession | null
  messages: AgentMessage[]
  isStreaming: boolean
  /** Raw plugin events (live + restored). Forward to <AgentChat pluginEvents={...} />. */
  pluginEvents: AgentSSEEvent[]
  isCreating: boolean
  error: string | null
  activityStatus: ActivityStatus | null
  artifacts: ArtifactInfo[]
  todos: TodoItem[]
  renderedTables: Map<string, RenderedTableInfo>
  renderedCharts: Map<string, RenderedChartInfo>
  askedQuestions: Map<string, AskUserQuestionInfo>
  createdDashboards: Map<string, CreatedDashboardInfo>
  pendingPageToolRequest: { toolName: string; toolCallId: string } | null
  pendingSettingsUpdate: { toolCallId: string; settings: Record<string, unknown> } | null
  allPersonas: PersonaInfo[]
  sessionList: AgentSessionListItem[]
  hasMoreSessions: boolean
  toolInputBuffer: string
  subagentEvents: AgentEventState['subagentEvents']
  installedSkills: AgentEventState['installedSkills']
  selectedModel: string
  setSelectedModel: (model: string) => void
  lastHeartbeat: number
  stuckStatus: 'ok' | 'possibly_stuck' | 'likely_stuck'
  nudgeAgent: () => Promise<void>
  createSession: (req: CreateAgentSessionRequest) => Promise<string | null>
  sendMessage: (content: string, model?: string, attachmentIds?: string[]) => Promise<void>
  cancelSession: () => Promise<void>
  deleteSession: () => Promise<void>
  loadPersonas: (customer: string) => Promise<void>
  resumeSession: (sessionId: string) => Promise<void>
  listSessions: (customer?: string, userEmail?: string) => Promise<void>
  loadMoreSessions: (customer?: string, userEmail?: string) => Promise<void>
  searchSessions: (customer: string, query: string, userEmail?: string) => Promise<AgentMessageSearchResult[]>
  clearSession: () => void
  markQuestionAnswered: (toolCallId: string, value: string) => void
  clearPendingPageToolRequest: () => void
  clearPendingSettingsUpdate: () => void
}

export default function useAgentSession(options: UseAgentSessionOptions = {}): UseAgentSessionReturn {
  const {
    apiBaseUrl = '',
    endpoints: endpointOverrides,
    defaultModel = '',
    authHeader,
    getAuthToken,
    pluginEventTypes,
    onToolResult,
    onSessionTitle,
    onArtifactsUpdated,
    onSnapshotComplete,
  } = options

  // Stable Set of plugin event types so handleSSEEvent closure need not depend
  // on the array identity (avoids unnecessary re-creation of the callback).
  const pluginEventTypesRef = useRef<Set<string>>(new Set(pluginEventTypes))
  // Keep in sync if the caller passes a new array (rare, but possible).
  pluginEventTypesRef.current = new Set(pluginEventTypes)

  const endpoints: AgentChatEndpoints = {
    ...DEFAULT_ENDPOINTS,
    ...endpointOverrides,
  }

  const [session, setSession] = useState<AgentSession | null>(null)
  const [messages, setMessages] = useState<AgentMessage[]>([])
  const [isStreaming, setIsStreaming] = useState(false)
  const [isCreating, setIsCreating] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [artifacts, setArtifacts] = useState<ArtifactInfo[]>([])
  const [todos, setTodos] = useState<TodoItem[]>([])
  const [renderedTables, setRenderedTables] = useState<Map<string, RenderedTableInfo>>(new Map())
  const [renderedCharts, setRenderedCharts] = useState<Map<string, RenderedChartInfo>>(new Map())
  const [askedQuestions, setAskedQuestions] = useState<Map<string, AskUserQuestionInfo>>(new Map())
  const [createdDashboards, setCreatedDashboards] = useState<Map<string, CreatedDashboardInfo>>(new Map())
  const [pendingPageToolRequest, setPendingPageToolRequest] = useState<{ toolName: string; toolCallId: string } | null>(null)
  const [pendingSettingsUpdate, setPendingSettingsUpdate] = useState<{ toolCallId: string; settings: Record<string, unknown> } | null>(null)
  const [allPersonas, setAllPersonas] = useState<PersonaInfo[]>([])
  const [sessionList, setSessionList] = useState<AgentSessionListItem[]>([])
  const [hasMoreSessions, setHasMoreSessions] = useState(false)
  const [selectedModel, setSelectedModel] = useState<string>(defaultModel)
  const [activityStatus, setActivityStatus] = useState<ActivityStatus | null>(null)
  const [toolInputBuffer, setToolInputBuffer] = useState<string>('')
  const [stuckStatus, setStuckStatus] = useState<'ok' | 'possibly_stuck' | 'likely_stuck'>('ok')
  const abortControllerRef = useRef<AbortController | null>(null)
  const sessionIdRef = useRef<string | null>(null)
  const lastHeartbeatRef = useRef<number>(Date.now())
  const lastEventTimeRef = useRef<number>(Date.now())

  // Plugin side-channel: raw events whose types belong to registered plugins.
  // Kept separately from core reducer state so AgentChat can fold them per-toolCallId.
  const [pluginEvents, setPluginEvents] = useState<AgentSSEEvent[]>([])

  // Pure reducer state — single source of truth for SSE event processing.
  const eventStateRef = useRef<AgentEventState>(initialAgentEventState())

  async function buildHeadersAsync(extra?: Record<string, string>): Promise<Record<string, string>> {
    const h: Record<string, string> = { ...extra }
    if (getAuthToken) {
      const token = await getAuthToken()
      if (token) h['Authorization'] = `Bearer ${token}`
    } else if (authHeader) {
      h['Authorization'] = authHeader
    }
    return h
  }

  async function apiFetch(url: string, init?: RequestInit): Promise<Response> {
    const headers = await buildHeadersAsync(init?.headers as Record<string, string> | undefined)
    return fetch(apiBaseUrl + url, {
      ...init,
      headers,
    })
  }

  async function apiGet<T>(url: string): Promise<T> {
    const resp = await apiFetch(url)
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
    return resp.json() as Promise<T>
  }

  const loadPersonas = useCallback(async (customer: string) => {
    try {
      const data = await apiGet<{ personas: PersonaInfo[] }>(`/personas/${customer}`)
      setAllPersonas(data?.personas || [])
    } catch (err) {
      console.error('Failed to load personas:', err)
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [apiBaseUrl, authHeader, getAuthToken])

  const loadArtifacts = useCallback(async (sessionId: string) => {
    try {
      const data = await apiGet<{ artifacts?: Record<string, unknown>[] } | Record<string, unknown>[]>(
        endpoints.artifacts(sessionId)
      )
      // The artifacts endpoint returns a bare JSON array; some deployments wrap
      // it as { artifacts: [...] }. Accept both so the DB-backed artifacts (which
      // carry the `id` used for downloads) are not silently dropped.
      const rows: Record<string, unknown>[] = Array.isArray(data)
        ? data
        : (data.artifacts || [])
      // API returns snake_case JSON — normalize to camelCase ArtifactInfo
      const loaded: ArtifactInfo[] = rows.map((a: Record<string, unknown>) => {
        const filePath = (a.file_path as string) || (a.filePath as string) || ''
        return {
        id: (a.id as string) || undefined,
        filePath,
        fileName: (a.file_name as string) || (a.fileName as string) || filePath.split('/').pop() || '',
        fileSize: (a.file_size as number | undefined) ?? (a.fileSize as number | undefined),
        mimeType: (a.mime_type as string) || (a.mimeType as string) || undefined,
        label: (a.label as string) || '',
        description: (a.description as string) || undefined,
        artifactType: ((a.artifact_type || a.artifactType) as ArtifactInfo['artifactType']) || 'file',
        source: ((a.source) as ArtifactInfo['source']) || 'auto',
        status: ((a.status) as ArtifactInfo['status']) || 'live',
        isDir: (a.is_dir as boolean) ?? (a.isDir as boolean) ?? false,
        }
      })
      // Merge: keep SSE-registered live artifacts that the DB doesn't know about yet
      const dbPaths = new Set(loaded.map(a => a.filePath))
      const sseOnly = eventStateRef.current.artifacts.filter(
        a => a.source === 'registered' && a.status === 'live' && !dbPaths.has(a.filePath)
      )
      const merged = [...loaded, ...sseOnly]
      eventStateRef.current = { ...eventStateRef.current, artifacts: merged }
      setArtifacts(merged)
    } catch (err) {
      console.error('Failed to load artifacts:', err)
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [apiBaseUrl, authHeader, getAuthToken])

  /**
   * Dispatch an SSE event through the pure reducer, then sync changed fields
   * to React state. Side effects (session updates, artifact loading, heartbeat)
   * are handled inline after the reducer runs.
   */
  const handleSSEEvent = useCallback((event: AgentSSEEvent) => {
    lastHeartbeatRef.current = Date.now()

    const progressTypes = ['content_delta', 'thinking_delta', 'tool_use_start', 'tool_use_end',
      'tool_progress', 'activity_update', 'message_start', 'system_status',
      'tool_input_delta', 'subagent_event']
    if (progressTypes.includes(event.type)) {
      lastEventTimeRef.current = Date.now()
    }
    const prevState = eventStateRef.current
    const nextState = agentEventReducer(prevState, event)
    eventStateRef.current = nextState

    // Sync only changed fields to React state (referential equality check)
    if (nextState.messages !== prevState.messages) setMessages(nextState.messages)
    if (nextState.isStreaming !== prevState.isStreaming) setIsStreaming(nextState.isStreaming)
    if (nextState.error !== prevState.error) setError(nextState.error)
    if (nextState.activityStatus !== prevState.activityStatus) setActivityStatus(nextState.activityStatus)
    if (nextState.artifacts !== prevState.artifacts) setArtifacts(nextState.artifacts)
    if (nextState.todos !== prevState.todos) setTodos(nextState.todos)
    if (nextState.renderedTables !== prevState.renderedTables) setRenderedTables(nextState.renderedTables)
    if (nextState.renderedCharts !== prevState.renderedCharts) setRenderedCharts(nextState.renderedCharts)
    if (nextState.askedQuestions !== prevState.askedQuestions) setAskedQuestions(nextState.askedQuestions)
    if (nextState.createdDashboards !== prevState.createdDashboards) setCreatedDashboards(nextState.createdDashboards)
    if (nextState.pendingPageToolRequest !== prevState.pendingPageToolRequest) setPendingPageToolRequest(nextState.pendingPageToolRequest)
    if (nextState.pendingSettingsUpdate !== prevState.pendingSettingsUpdate) setPendingSettingsUpdate(nextState.pendingSettingsUpdate)

    if (nextState.toolInputBuffer !== prevState.toolInputBuffer) {
      setToolInputBuffer(nextState.toolInputBuffer)
    }
    if (prevState.activityStatus?.category === 'preparing_tool' && nextState.activityStatus?.category !== 'preparing_tool') {
      setToolInputBuffer('')
    }

    // Accumulate plugin events in the side-channel so AgentChat can fold them
    // per-toolCallId. Reading via ref keeps this closure stable.
    if (pluginEventTypesRef.current.size > 0 && pluginEventTypesRef.current.has(event.type)) {
      setPluginEvents(prev => [...prev, event])
    }

    // --- Side effects the reducer can't handle ---

    if (event.type === 'tool_use_end' && onToolResult) {
      const eventData = event.data as Record<string, unknown>
      const tc = nextState.toolCalls.get(eventData.toolCallId as string)
      if (tc && tc.output) {
        onToolResult(tc.name, tc.id, tc.output, tc.input)
      }
    }

    if (event.type === 'session_title') {
      const { title, sessionId } = event.data as { title: string; sessionId: string }
      setSession(prev => prev?.id === sessionId ? { ...prev, title } : prev)
      setSessionList(prev => prev.map(s => s.id === sessionId ? { ...s, title } : s))
      if (onSessionTitle) onSessionTitle(sessionId, title)
    }

    if (event.type === 'artifacts_updated') {
      if (sessionIdRef.current) {
        loadArtifacts(sessionIdRef.current)
        if (onArtifactsUpdated) onArtifactsUpdated(sessionIdRef.current)
      }
    }

    if (event.type === 'query_complete') {
      const queryStatus = (event.data as Record<string, unknown>)?.status
      if (queryStatus !== 'cancelled') {
        setSession(prev => prev ? { ...prev, status: 'completed' } : prev)
      }
      if (sessionIdRef.current) loadArtifacts(sessionIdRef.current)

      // Delayed title refresh fallback
      const sid = sessionIdRef.current
      if (sid) {
        setTimeout(async () => {
          try {
            const s = await apiGet<{ title?: string; id: string }>(
              endpoints.getSession(sid)
            )
            if (s.title) {
              setSession(prev => prev?.id === sid ? { ...prev, title: s.title } : prev)
              setSessionList(prev => prev.map(item => item.id === sid ? { ...item, title: s.title! } : item))
            }
          } catch { /* best effort */ }
        }, 5000)
      }
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [loadArtifacts, onToolResult, onSessionTitle, onArtifactsUpdated])

  const markQuestionAnswered = useCallback((toolCallId: string, value: string) => {
    const nextQuestions = new Map(eventStateRef.current.askedQuestions)
    const q = nextQuestions.get(toolCallId)
    if (q) nextQuestions.set(toolCallId, { ...q, answered: true, selectedValue: value })
    eventStateRef.current = { ...eventStateRef.current, askedQuestions: nextQuestions }
    setAskedQuestions(nextQuestions)
  }, [])

  const clearPendingPageToolRequest = useCallback(() => {
    eventStateRef.current = { ...eventStateRef.current, pendingPageToolRequest: null }
    setPendingPageToolRequest(null)
  }, [])

  const clearPendingSettingsUpdate = useCallback(() => {
    eventStateRef.current = { ...eventStateRef.current, pendingSettingsUpdate: null }
    setPendingSettingsUpdate(null)
  }, [])

  const resetEventState = () => {
    eventStateRef.current = initialAgentEventState()
  }

  function startStuckDetection() {
    lastEventTimeRef.current = Date.now()
    lastHeartbeatRef.current = Date.now()
    setStuckStatus('ok')
    if (_stuckTimer) clearInterval(_stuckTimer)
    _stuckTimer = setInterval(() => {
      const sinceHeartbeat = Date.now() - lastHeartbeatRef.current
      const sinceProgress = Date.now() - lastEventTimeRef.current
      if (sinceHeartbeat > 45_000) setStuckStatus('likely_stuck')
      else if (sinceProgress > 120_000) setStuckStatus('likely_stuck')
      else if (sinceProgress > 60_000) setStuckStatus('possibly_stuck')
      else setStuckStatus('ok')
    }, 5_000)
  }

  function stopStuckDetection() {
    if (_stuckTimer) {
      clearInterval(_stuckTimer)
      _stuckTimer = null
    }
    setStuckStatus('ok')
  }

  const createSession = useCallback(async (req: CreateAgentSessionRequest): Promise<string | null> => {
    resetEventState()
    setError(null)
    setMessages([])
    setArtifacts([])
    setRenderedTables(new Map())
    setRenderedCharts(new Map())
    setAskedQuestions(new Map())
    setCreatedDashboards(new Map())
    setPluginEvents([])
    setIsCreating(true)
    try {
      const response = await apiFetch(endpoints.createSession, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(req),
      })
      if (!response.ok) throw new Error(`HTTP ${response.status}`)
      const data = await response.json() as { id: string; status: string; workflowId: string }
      const newSession: AgentSession = {
        id: data.id,
        status: 'active',
        workflowId: req.workflow_id || 'agent',
        persona: req.persona,
        customer: req.customer,
        job: req.job,
      }
      setSession(newSession)
      sessionIdRef.current = newSession.id
      // Insert the new session into the list (if absent) so the container-state
      // poll can attach live image-pull progress to it — the poll only updates
      // rows that already exist, and it drives both the drawer "Starting…" chip
      // and the create-complete toast.
      setSessionList(prev => {
        if (prev.some(s => s.id === newSession.id)) return prev
        const now = Math.floor(Date.now() / 1000)
        const row: AgentSessionListItem = {
          id: newSession.id,
          created_at: now,
          updated_at: now,
          user_email: '',
          customer: req.customer ?? '',
          job: req.job ?? '',
          workflow_id: req.workflow_id || 'agent',
          persona: req.persona ?? '',
          status: 'active',
          current_node: '',
          title: '',
          artifact_count: 0,
          container_state: 'starting',
        }
        return [row, ...prev]
      })
      startContainerStatePoll(newSession.id)
      return newSession.id
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : 'Failed to create session')
      return null
    } finally {
      setIsCreating(false)
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [apiBaseUrl, authHeader, getAuthToken])

  /** Max reconnection attempts before falling back to DB load */
  const MAX_RECONNECT_ATTEMPTS = 3

  const checkSessionStatus = async (sessionId: string): Promise<{ activeQuery: { queryId: string } | null } | null> => {
    try {
      const resp = await apiFetch(
        endpoints.status(sessionId),
        { signal: AbortSignal.timeout(5000) }
      )
      if (resp.ok) return await resp.json()
    } catch {
      // Status check failed
    }
    return null
  }

  const attemptReconnect = async (sessionId: string): Promise<Response | null> => {
    try {
      const resp = await apiFetch(
        endpoints.reconnect(sessionId),
        { signal: AbortSignal.timeout(AGENT_QUERY_TIMEOUT_MS) }
      )
      if (resp.ok && resp.headers.get('content-type')?.includes('text/event-stream')) {
        return resp
      }
    } catch {
      // Reconnect failed
    }
    return null
  }

  const readSSEStream = async (response: Response, reconnectDepth = 0, expectedSessionId?: string) => {
    const reader = response.body!.getReader()
    const decoder = new TextDecoder()
    let sseBuffer = ''
    let receivedQueryComplete = false

    try {
      while (true) {
        if (expectedSessionId && sessionIdRef.current !== expectedSessionId) {
          console.log(`[SSE] Session changed (${expectedSessionId} → ${sessionIdRef.current}) — dropping stream`)
          break
        }

        const { done, value } = await reader.read()
        if (done) break

        sseBuffer += decoder.decode(value, { stream: true })
        const lines = sseBuffer.split('\n')
        sseBuffer = lines.pop() || ''

        for (const line of lines) {
          if (line.startsWith('data: ')) {
            try {
              const event: AgentSSEEvent = JSON.parse(line.slice(6))
              if (event.type === 'query_complete' || event.type === 'dag_complete') {
                receivedQueryComplete = true
              }
              handleSSEEvent(event)
            } catch {
              // Ignore malformed SSE data
            }
          }
        }
      }

      if (sseBuffer.startsWith('data: ')) {
        try {
          const event: AgentSSEEvent = JSON.parse(sseBuffer.slice(6))
          if (event.type === 'query_complete' || event.type === 'dag_complete') {
            receivedQueryComplete = true
          }
          handleSSEEvent(event)
        } catch { /* ignore */ }
      }

      if (!receivedQueryComplete && sessionIdRef.current && (!expectedSessionId || sessionIdRef.current === expectedSessionId)) {
        if (reconnectDepth < MAX_RECONNECT_ATTEMPTS) {
          console.log(`[SSE] Stream ended without query_complete — checking for active query (attempt ${reconnectDepth + 1}/${MAX_RECONNECT_ATTEMPTS})`)
          const status = await checkSessionStatus(sessionIdRef.current)
          if (status?.activeQuery) {
            console.log(`[SSE] Active query found (${status.activeQuery.queryId}) — reconnecting`)
            const reconnectResp = await attemptReconnect(sessionIdRef.current)
            if (reconnectResp) {
              await readSSEStream(reconnectResp, reconnectDepth + 1, expectedSessionId)
              return
            }
            console.warn('[SSE] Reconnect failed — falling back to persisted messages')
          } else {
            console.log('[SSE] No active query — query may have completed, loading persisted state')
          }
        } else {
          console.warn(`[SSE] Max reconnect attempts (${MAX_RECONNECT_ATTEMPTS}) reached — falling back to persisted messages`)
        }

        if (sessionIdRef.current) loadArtifacts(sessionIdRef.current)
        try {
          let restored: AgentEventState | null = null
          let restoredFromRawEvents = false
          try {
            const eventsData = await apiGet<{ events: Array<{ events: unknown[] }> }>(
              endpoints.queryEvents(sessionIdRef.current)
            )
            const queryEvents = eventsData.events || []
            if (queryEvents.length > 0) {
              const allEvents = queryEvents.flatMap(qe => qe.events as AgentSSEEvent[])
              restored = replayEvents(allEvents)
              // Restore plugin events from raw events so plugins can reconstruct state.
              const typeSet = pluginEventTypesRef.current
              setPluginEvents(typeSet.size > 0 ? allEvents.filter(e => typeSet.has(e.type)) : [])
              restoredFromRawEvents = true
            }
          } catch { /* fall through to legacy */ }

          if (!restored) {
            const msgsData = await apiGet<{ messages: PersistedAgentMessage[]; total: number }>(
              `${endpoints.messages(sessionIdRef.current)}?limit=500`
            )
            const persisted = msgsData.messages || []
            if (persisted.length > 0) {
              restored = replayFromPersistedMessages(persisted)
            }
          }

          if (restored) {
            eventStateRef.current = {
              ...eventStateRef.current,
              messages: restored.messages,
              renderedTables: restored.renderedTables,
              renderedCharts: restored.renderedCharts,
              askedQuestions: restored.askedQuestions,
              createdDashboards: restored.createdDashboards,
              todos: restored.todos,
              sessionInfo: restored.sessionInfo,
              subagentEvents: restored.subagentEvents,
              artifacts: restored.artifacts,
            }
            setMessages(restored.messages)
            setRenderedTables(restored.renderedTables)
            setRenderedCharts(restored.renderedCharts)
            setAskedQuestions(restored.askedQuestions)
            setCreatedDashboards(restored.createdDashboards)
            setTodos(restored.todos)
            setArtifacts(restored.artifacts)
            // If we fell back to persisted messages, plugin state cannot be
            // reconstructed — reset to empty (renders nothing; live events will fill it).
            if (!restoredFromRawEvents) setPluginEvents([])
          }
        } catch { /* best effort */ }

        if (reconnectDepth >= MAX_RECONNECT_ATTEMPTS) {
          setError('Connection lost — loaded conversation from history')
        }
      }
    } finally {
      reader.releaseLock()
    }
  }

  const sendMessage = useCallback(async (content: string, model?: string, attachmentIds?: string[]) => {
    if (!session) return

    // Mark any unanswered questions as answered (user typed instead of clicking)
    const prevQuestions = eventStateRef.current.askedQuestions
    let questionsChanged = false
    const nextQuestions = new Map(prevQuestions)
    for (const [id, q] of nextQuestions) {
      if (!q.answered) {
        nextQuestions.set(id, { ...q, answered: true, selectedValue: content })
        questionsChanged = true
      }
    }
    if (questionsChanged) {
      eventStateRef.current = { ...eventStateRef.current, askedQuestions: nextQuestions }
      setAskedQuestions(nextQuestions)
    }

    setIsStreaming(true)
    setError(null)
    eventStateRef.current = { ...eventStateRef.current, isStreaming: true, error: null }
    startStuckDetection()

    const userMessage: AgentMessage = {
      id: crypto.randomUUID(),
      role: 'user',
      content,
      timestamp: new Date().toISOString(),
    }
    const messagesWithUser = [...eventStateRef.current.messages, userMessage]
    eventStateRef.current = { ...eventStateRef.current, messages: messagesWithUser }
    setMessages(messagesWithUser)

    const abortController = new AbortController()
    abortControllerRef.current = abortController

    try {
      const response = await apiFetch(endpoints.sendMessage(session.id), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ content, ...(model && { model }), ...(attachmentIds?.length && { attachmentIds }) }),
        signal: AbortSignal.any([abortController.signal, AbortSignal.timeout(AGENT_QUERY_TIMEOUT_MS)]),
      })

      if (!response.ok) {
        const text = await response.text()
        let message = `Request failed (${response.status})`
        try { message = (JSON.parse(text) as { error?: string }).error || message } catch { /* use default */ }
        setError(message)
        setIsStreaming(false)
        setActivityStatus(null)
        stopStuckDetection()
        eventStateRef.current = { ...eventStateRef.current, isStreaming: false, error: message, activityStatus: null }
        return
      }

      await readSSEStream(response, 0, session.id)

      setIsStreaming(false)
      setActivityStatus(null)
      stopStuckDetection()
      eventStateRef.current = { ...eventStateRef.current, isStreaming: false, activityStatus: null }

      if (sessionIdRef.current) loadArtifacts(sessionIdRef.current)
    } catch (err: unknown) {
      if ((err as Error).name !== 'AbortError') {
        setError(err instanceof Error ? err.message : 'Unknown error')
      }
      setIsStreaming(false)
      setActivityStatus(null)
      stopStuckDetection()
      eventStateRef.current = { ...eventStateRef.current, isStreaming: false, activityStatus: null }

      if (sessionIdRef.current) loadArtifacts(sessionIdRef.current)
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [session, handleSSEEvent, loadArtifacts])

  const cancelSession = useCallback(async () => {
    if (!session) return
    abortControllerRef.current?.abort()
    try {
      await apiFetch(endpoints.cancel(session.id), { method: 'POST' })
    } catch {
      // Cancel POST is fire-and-forget
    }
    setIsStreaming(false)
    setActivityStatus(null)
    stopStuckDetection()
    eventStateRef.current = { ...eventStateRef.current, isStreaming: false, activityStatus: null }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [session])

  const deleteSession = useCallback(async () => {
    if (!session) return
    abortControllerRef.current?.abort()
    try {
      await apiFetch(endpoints.deleteSession(session.id), { method: 'DELETE' })
      resetEventState()
      setSession(null)
      setMessages([])
      setIsStreaming(false)
    } catch {
      // Silently handle delete errors
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [session])

  function startContainerStatePoll(sessionId: string) {
    stopContainerStatePoll()
    _containerPollSessionId = sessionId

    const poll = async () => {
      if (_containerPollSessionId !== sessionId) return
      try {
        const resp = await apiFetch(endpoints.status(sessionId), { signal: AbortSignal.timeout(3000) })
        if (!resp.ok) return
        const data = await resp.json() as { sandboxState?: string; progress?: OpProgress }
        const state = data.sandboxState
        const prog = data.progress ?? null
        setSessionList(prev => {
          const idx = prev.findIndex(s => s.id === sessionId)
          if (idx === -1) return prev
          const row = prev[idx]
          const nextState = state && state !== 'unknown' ? state : row.container_state
          if (row.container_state === nextState && row.snapshot_progress?.phase === prog?.phase &&
              row.snapshot_progress?.bytesDone === prog?.bytesDone) return prev
          const next = [...prev]
          next[idx] = { ...row, container_state: nextState, snapshot_progress: prog ?? undefined }
          return next
        })
        // Fire the completion callback once per terminal op.
        if (prog && (prog.phase === 'done' || prog.phase === 'failed') && _lastNotifiedOp !== `${sessionId}:${prog.startedAt}`) {
          _lastNotifiedOp = `${sessionId}:${prog.startedAt}`
          onSnapshotComplete?.(sessionId, prog)
        }
        // Cadence: poll fast while an op is active, slow once settled.
        const active = !!prog && prog.phase !== 'done' && prog.phase !== 'failed'
        const desired = active ? 750 : 2000
        if (_containerPollInterval !== desired) {
          _containerPollInterval = desired
          if (_containerPollTimer) { clearInterval(_containerPollTimer); _containerPollTimer = setInterval(poll, desired) }
        }
        if (!active && state && STABLE_CONTAINER_STATES.has(state)) {
          stopContainerStatePoll()
        }
      } catch {
        // Silently ignore poll failures
      }
    }

    poll()
    _containerPollInterval = 2000
    _containerPollTimer = setInterval(poll, _containerPollInterval)
  }

  function stopContainerStatePoll() {
    if (_containerPollTimer) {
      clearInterval(_containerPollTimer)
      _containerPollTimer = null
    }
    _containerPollSessionId = null
  }

  const clearSession = useCallback(() => {
    abortControllerRef.current?.abort()
    resetEventState()
    setSession(null)
    setMessages([])
    setArtifacts([])
    setTodos([])
    setAskedQuestions(new Map())
    setIsStreaming(false)
    setError(null)
    setActivityStatus(null)
    setPluginEvents([])
    stopStuckDetection()
    stopContainerStatePoll()
    sessionIdRef.current = null
  }, [])

  const resumeSession = useCallback(async (sessionId: string) => {
    resetEventState()
    setError(null)
    setMessages([])
    setArtifacts([])
    setAskedQuestions(new Map())
    try {
      // Load session metadata, events, and messages in parallel
      const [sessionData, eventsData, msgsData] = await Promise.all([
        apiGet<AgentSession>(endpoints.getSession(sessionId)),
        apiGet<{ events: Array<{ events: unknown[] }> }>(
          endpoints.queryEvents(sessionId)
        ).catch(() => ({ events: [] })),
        apiGet<{ messages: PersistedAgentMessage[]; total: number }>(
          `${endpoints.messages(sessionId)}?limit=500`
        ),
      ])

      const s = sessionData
      const workflowId = s.workflowId || (s as unknown as Record<string, string>).workflow_id || 'agent'
      const metadata = (s as unknown as Record<string, unknown>).metadata as Record<string, unknown> | undefined
      const sessionModel = (metadata?.model as string) || defaultModel
      const resumed: AgentSession = {
        id: s.id,
        status: (s.status as AgentSession['status']) || 'active',
        workflowId,
        persona: s.persona,
        customer: s.customer,
        job: s.job,
        model: sessionModel,
        metadata,
      }
      setSelectedModel(sessionModel)

      // Restore state via event replay or legacy reconstruction
      const queryEvents = eventsData.events || []
      let restored: AgentEventState
      if (queryEvents.length > 0) {
        const allEvents = queryEvents.flatMap(qe => qe.events as AgentSSEEvent[])
        restored = replayEvents(allEvents)
        // Restore plugin events from raw events so plugins can reconstruct their state.
        const typeSet = pluginEventTypesRef.current
        setPluginEvents(typeSet.size > 0 ? allEvents.filter(e => typeSet.has(e.type)) : [])
      } else {
        const persisted = msgsData.messages || []
        restored = replayFromPersistedMessages(persisted)
        // Plugin state cannot be reconstructed from persisted messages — falls back to empty.
        // Plugins will render nothing until new live events arrive (acceptable).
        setPluginEvents([])
      }

      eventStateRef.current = {
        ...eventStateRef.current,
        messages: restored.messages,
        askedQuestions: restored.askedQuestions,
        renderedTables: restored.renderedTables,
        renderedCharts: restored.renderedCharts,
        createdDashboards: restored.createdDashboards,
        todos: restored.todos,
        sessionInfo: restored.sessionInfo,
        subagentEvents: restored.subagentEvents,
        artifacts: restored.artifacts,
      }
      setAskedQuestions(restored.askedQuestions)
      setRenderedTables(restored.renderedTables)
      setRenderedCharts(restored.renderedCharts)
      setCreatedDashboards(restored.createdDashboards)
      setTodos(restored.todos)
      setArtifacts(restored.artifacts)

      setSession(resumed)
      sessionIdRef.current = resumed.id
      setMessages(restored.messages)

      startContainerStatePoll(sessionId)
      loadArtifacts(sessionId)

      // Eager restore if session has a snapshot
      const snapshotState = (s as unknown as Record<string, string>).snapshot_state
      if (snapshotState === 'available' || snapshotState === 'archived') {
        setActivityStatus({ label: 'Restoring session...', category: 'system' })
        try {
          await apiFetch(endpoints.restore(sessionId), { method: 'POST' })
        } catch {
          // Fall through — lazy restore still works as backup
        }
        setActivityStatus(null)
        startContainerStatePoll(sessionId)
      }

      // Check for active streaming query and reconnect if needed
      if (resumed.status === 'active' || resumed.status === 'streaming') {
        const status = await checkSessionStatus(sessionId)
        if (status?.activeQuery) {
          eventStateRef.current = { ...eventStateRef.current, isStreaming: true }
          setIsStreaming(true)
          setActivityStatus({ label: 'Reconnecting...', category: 'system' })

          const abortController = new AbortController()
          abortControllerRef.current = abortController
          startStuckDetection()

          const reconnectResp = await attemptReconnect(sessionId)
          if (reconnectResp) {
            await readSSEStream(reconnectResp, 0, sessionId)
          }

          setIsStreaming(false)
          setActivityStatus(null)
          stopStuckDetection()
          eventStateRef.current = { ...eventStateRef.current, isStreaming: false, activityStatus: null }
          if (sessionIdRef.current) loadArtifacts(sessionIdRef.current)
        }
      }
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : 'Unknown error')
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [loadArtifacts, handleSSEEvent])

  const SESSION_PAGE_SIZE = 20

  const listSessions = useCallback(async (customer?: string, userEmail?: string) => {
    try {
      const params = new URLSearchParams({ limit: String(SESSION_PAGE_SIZE) })
      if (customer) params.set('customer', customer)
      if (userEmail) params.set('user_email', userEmail)
      params.set('exclude_workflow_prefix', 'assistant-')
      const data = await apiGet<{ sessions: AgentSessionListItem[] }>(
        `${endpoints.listSessions}?${params.toString()}`
      )
      const freshSessions = data.sessions || []

      setSessionList(prev => {
        if (prev.length <= SESSION_PAGE_SIZE) {
          setHasMoreSessions(freshSessions.length >= SESSION_PAGE_SIZE)
          return freshSessions
        }
        const freshMap = new Map(freshSessions.map(s => [s.id, s]))
        const updated = prev.map(s => freshMap.get(s.id) ?? s)
        const existingIds = new Set(prev.map(s => s.id))
        const newSessions = freshSessions.filter(s => !existingIds.has(s.id))
        return newSessions.length > 0 ? [...newSessions, ...updated] : updated
      })
    } catch (err) {
      console.error('Failed to list sessions:', err)
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [apiBaseUrl, authHeader, getAuthToken])

  const sessionListRef = useRef(sessionList)
  sessionListRef.current = sessionList

  const loadMoreSessions = useCallback(async (customer?: string, userEmail?: string) => {
    try {
      const params = new URLSearchParams({
        has_messages: 'true',
        limit: String(SESSION_PAGE_SIZE),
        offset: String(sessionListRef.current.length),
      })
      if (customer) params.set('customer', customer)
      if (userEmail) params.set('user_email', userEmail)
      params.set('exclude_workflow_prefix', 'assistant-')
      const data = await apiGet<{ sessions: AgentSessionListItem[] }>(
        `${endpoints.listSessions}?${params.toString()}`
      )
      const newSessions = data.sessions || []
      setSessionList(prev => [...prev, ...newSessions])
      setHasMoreSessions(newSessions.length >= SESSION_PAGE_SIZE)
    } catch (err) {
      console.error('Failed to load more sessions:', err)
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [apiBaseUrl, authHeader, getAuthToken])

  const searchSessions = useCallback(async (customer: string, query: string, userEmail?: string): Promise<AgentMessageSearchResult[]> => {
    try {
      const params = new URLSearchParams({ q: query, customer })
      if (userEmail) params.set('user_email', userEmail)
      const data = await apiGet<{ results: AgentMessageSearchResult[] }>(
        `${endpoints.searchMessages}?${params.toString()}`
      )
      return data.results || []
    } catch (err) {
      console.error('Failed to search sessions:', err)
      return []
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [apiBaseUrl, authHeader, getAuthToken])

  const nudgeAgent = useCallback(async () => {
    if (!session) return
    abortControllerRef.current?.abort()
    try {
      await apiFetch(endpoints.cancel(session.id), { method: 'POST' })
    } catch { /* ignore */ }
    setIsStreaming(false)
    setActivityStatus(null)
    stopStuckDetection()
    eventStateRef.current = { ...eventStateRef.current, isStreaming: false, activityStatus: null }
    await sendMessage('Please continue where you left off.')
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [session, sendMessage])

  return {
    session,
    messages,
    isStreaming,
    pluginEvents,
    isCreating,
    error,
    activityStatus,
    artifacts,
    todos,
    renderedTables,
    renderedCharts,
    askedQuestions,
    createdDashboards,
    pendingPageToolRequest,
    pendingSettingsUpdate,
    allPersonas,
    sessionList,
    hasMoreSessions,
    toolInputBuffer,
    subagentEvents: eventStateRef.current.subagentEvents,
    installedSkills: eventStateRef.current.installedSkills,
    selectedModel,
    setSelectedModel,
    lastHeartbeat: lastHeartbeatRef.current,
    createSession,
    sendMessage,
    cancelSession,
    deleteSession,
    loadPersonas,
    resumeSession,
    listSessions,
    loadMoreSessions,
    searchSessions,
    clearSession,
    markQuestionAnswered,
    clearPendingPageToolRequest,
    clearPendingSettingsUpdate,
    stuckStatus,
    nudgeAgent,
  }
}
