// Ported from frontend/src/hooks/agentEventReducer.test.ts.
// Locks the single-reducer invariant: one agentEventReducer used for
// live streaming and replay, tested here with generic event types.
// Imports adapted to the library's generic types.

import { describe, it, expect } from 'vitest'
import { agentEventReducer, initialAgentEventState, type AgentEventState } from './agentEventReducer.js'
import type { AgentSSEEvent, ToolCallInfo } from './types.js'

function makeEvent(type: AgentSSEEvent['type'], data: Record<string, unknown>): AgentSSEEvent {
  return { type, data, timestamp: '2026-02-26T12:00:00Z' }
}

describe('agentEventReducer', () => {
  it('content_delta accumulation - two deltas after message_start concatenate content', () => {
    let state = initialAgentEventState()

    state = agentEventReducer(state, makeEvent('message_start', {
      role: 'assistant',
      messageId: 'msg-1',
    }))

    state = agentEventReducer(state, makeEvent('content_delta', { delta: 'Hello ' }))
    state = agentEventReducer(state, makeEvent('content_delta', { delta: 'world' }))

    expect(state.currentMessage).toEqual({ id: 'msg-1', content: 'Hello world' })
    expect(state.messages).toHaveLength(1)
    expect(state.messages[0].content).toBe('Hello world')
  })

  it('content_delta creates new message in messages array', () => {
    let state = initialAgentEventState()

    state = agentEventReducer(state, makeEvent('message_start', {
      role: 'assistant',
      messageId: 'msg-new',
    }))

    state = agentEventReducer(state, makeEvent('content_delta', { delta: 'First chunk' }))

    expect(state.messages).toHaveLength(1)
    expect(state.messages[0]).toMatchObject({
      id: 'msg-new',
      role: 'assistant',
      content: 'First chunk',
    })
    expect(state.messages[0].toolCalls).toEqual([])
    expect(state.messages[0].timestamp).toBe('2026-02-26T12:00:00Z')
  })

  it('content_delta updates existing message rather than adding duplicate', () => {
    let state: AgentEventState = {
      ...initialAgentEventState(),
      currentMessage: { id: 'msg-exist', content: 'Previous ' },
      messages: [{
        id: 'msg-exist',
        role: 'assistant',
        content: 'Previous ',
        toolCalls: [],
        timestamp: '2026-02-26T11:00:00Z',
      }],
    }

    state = agentEventReducer(state, makeEvent('content_delta', { delta: 'appended' }))

    expect(state.messages).toHaveLength(1)
    expect(state.messages[0].content).toBe('Previous appended')
    // Timestamp should be preserved from original message
    expect(state.messages[0].timestamp).toBe('2026-02-26T11:00:00Z')
  })

  it('tool_use_start adds tool call with running status', () => {
    let state: AgentEventState = {
      ...initialAgentEventState(),
      currentMessage: { id: 'msg-1', content: 'thinking...' },
      messages: [{
        id: 'msg-1',
        role: 'assistant',
        content: 'thinking...',
        toolCalls: [],
        timestamp: '2026-02-26T12:00:00Z',
      }],
    }

    state = agentEventReducer(state, makeEvent('tool_use_start', {
      toolCallId: 'tc-1',
      toolName: 'read_file',
      input: { path: '/tmp/test.txt' },
    }))

    expect(state.toolCalls.size).toBe(1)
    const tc = state.toolCalls.get('tc-1')
    expect(tc).toMatchObject({
      id: 'tc-1',
      name: 'read_file',
      input: { path: '/tmp/test.txt' },
      status: 'running',
    })
    // Should also be reflected on the message
    expect(state.messages[0].toolCalls).toHaveLength(1)
    expect(state.messages[0].toolCalls![0].status).toBe('running')
  })

  it('tool_use_end updates tool call status to complete with output', () => {
    const existingToolCall: ToolCallInfo = {
      id: 'tc-1',
      name: 'read_file',
      input: { path: '/tmp/test.txt' },
      status: 'running',
    }
    let state: AgentEventState = {
      ...initialAgentEventState(),
      currentMessage: { id: 'msg-1', content: '' },
      toolCalls: new Map([['tc-1', existingToolCall]]),
      messages: [{
        id: 'msg-1',
        role: 'assistant',
        content: '',
        toolCalls: [existingToolCall],
        timestamp: '2026-02-26T12:00:00Z',
      }],
    }

    state = agentEventReducer(state, makeEvent('tool_use_end', {
      toolCallId: 'tc-1',
      isError: false,
      output: 'file contents here',
    }))

    const tc = state.toolCalls.get('tc-1')
    expect(tc!.status).toBe('complete')
    expect(tc!.output).toBe('file contents here')
    expect(state.messages[0].toolCalls![0].status).toBe('complete')
  })

  it('tool_use_end updates tool call status to error when isError is true', () => {
    const existingToolCall: ToolCallInfo = {
      id: 'tc-1',
      name: 'read_file',
      input: { path: '/tmp/missing.txt' },
      status: 'running',
    }
    let state: AgentEventState = {
      ...initialAgentEventState(),
      currentMessage: { id: 'msg-1', content: '' },
      toolCalls: new Map([['tc-1', existingToolCall]]),
      messages: [{
        id: 'msg-1',
        role: 'assistant',
        content: '',
        toolCalls: [existingToolCall],
        timestamp: '2026-02-26T12:00:00Z',
      }],
    }

    state = agentEventReducer(state, makeEvent('tool_use_end', {
      toolCallId: 'tc-1',
      isError: true,
      output: 'File not found',
    }))

    const tc = state.toolCalls.get('tc-1')
    expect(tc!.status).toBe('error')
    expect(tc!.output).toBe('File not found')
  })

  it('tool_progress updates elapsed seconds on tool call', () => {
    const existingToolCall: ToolCallInfo = {
      id: 'tc-1',
      name: 'run_query',
      input: {},
      status: 'running',
    }
    let state: AgentEventState = {
      ...initialAgentEventState(),
      currentMessage: { id: 'msg-1', content: '' },
      toolCalls: new Map([['tc-1', existingToolCall]]),
      messages: [{
        id: 'msg-1',
        role: 'assistant',
        content: '',
        toolCalls: [existingToolCall],
        timestamp: '2026-02-26T12:00:00Z',
      }],
    }

    state = agentEventReducer(state, makeEvent('tool_progress', {
      toolUseId: 'tc-1',
      elapsedSeconds: 5.2,
    }))

    const tc = state.toolCalls.get('tc-1')
    expect(tc!.elapsedSeconds).toBe(5.2)
  })

  it('query_complete sets isStreaming to false', () => {
    let state: AgentEventState = {
      ...initialAgentEventState(),
      isStreaming: true,
    }

    state = agentEventReducer(state, makeEvent('query_complete', {}))

    expect(state.isStreaming).toBe(false)
  })

  it('query_complete with result and no assistant messages adds result message', () => {
    let state: AgentEventState = {
      ...initialAgentEventState(),
      isStreaming: true,
      currentMessage: null,
    }

    state = agentEventReducer(state, makeEvent('query_complete', {
      result: 'The analysis is complete.',
    }))

    expect(state.isStreaming).toBe(false)
    expect(state.messages).toHaveLength(1)
    expect(state.messages[0].content).toBe('The analysis is complete.')
    expect(state.messages[0].role).toBe('assistant')
  })

  it('query_complete with result does not duplicate if assistant message already exists', () => {
    let state: AgentEventState = {
      ...initialAgentEventState(),
      isStreaming: true,
      currentMessage: null,
      messages: [{
        id: 'msg-1',
        role: 'assistant',
        content: 'Already streamed content',
        timestamp: '2026-02-26T11:59:00Z',
      }],
    }

    state = agentEventReducer(state, makeEvent('query_complete', {
      result: 'The analysis is complete.',
    }))

    expect(state.messages).toHaveLength(1)
    expect(state.messages[0].content).toBe('Already streamed content')
  })

  it('error sets error message and isStreaming to false', () => {
    let state: AgentEventState = {
      ...initialAgentEventState(),
      isStreaming: true,
    }

    state = agentEventReducer(state, makeEvent('error', {
      message: 'LLM rate limit exceeded',
    }))

    expect(state.error).toBe('LLM rate limit exceeded')
    expect(state.isStreaming).toBe(false)
  })

  it('error falls back to Unknown error when no message provided', () => {
    let state: AgentEventState = {
      ...initialAgentEventState(),
      isStreaming: true,
    }

    state = agentEventReducer(state, makeEvent('error', {}))

    expect(state.error).toBe('Unknown error')
    expect(state.isStreaming).toBe(false)
  })

  it('artifact_registered adds artifact to artifacts array', () => {
    let state = initialAgentEventState()

    state = agentEventReducer(state, makeEvent('artifact_registered', {
      filePath: '/workspace/output/report.csv',
      label: 'Analysis Report',
      description: 'CSV export of analysis results',
      artifactType: 'csv',
    }))

    expect(state.artifacts).toHaveLength(1)
    expect(state.artifacts[0]).toMatchObject({
      filePath: '/workspace/output/report.csv',
      fileName: 'report.csv',
      label: 'Analysis Report',
      description: 'CSV export of analysis results',
      artifactType: 'csv',
      source: 'registered',
      status: 'live',
    })
  })

  it('message_end clears currentMessage', () => {
    let state: AgentEventState = {
      ...initialAgentEventState(),
      currentMessage: { id: 'msg-1', content: 'complete response' },
      messages: [{
        id: 'msg-1',
        role: 'assistant',
        content: 'complete response',
        timestamp: '2026-02-26T12:00:00Z',
      }],
    }

    state = agentEventReducer(state, makeEvent('message_end', {}))

    expect(state.currentMessage).toBeNull()
    // Messages should be preserved
    expect(state.messages).toHaveLength(1)
  })

  it('tool_use_start with pt bash shows correct activity label and detail', () => {
    let state: AgentEventState = {
      ...initialAgentEventState(),
      currentMessage: { id: 'msg-1', content: '' },
      messages: [{
        id: 'msg-1',
        role: 'assistant',
        content: '',
        toolCalls: [],
        timestamp: '2026-02-26T12:00:00Z',
      }],
    }

    state = agentEventReducer(state, makeEvent('tool_use_start', {
      toolCallId: 'tc-1',
      toolName: 'bash',
      input: { command: 'pt query --side Age --top Gender' },
    }))

    expect(state.activityStatus).toMatchObject({
      label: 'Running: Query Table',
      detail: 'Side: Age, Top: Gender',
      category: 'tool',
      toolName: 'bash',
      toolInput: { command: 'pt query --side Age --top Gender' },
    })
  })

  it('tool_use_start with non-pt bash shows Run Command', () => {
    let state: AgentEventState = {
      ...initialAgentEventState(),
      currentMessage: { id: 'msg-1', content: '' },
      messages: [{
        id: 'msg-1',
        role: 'assistant',
        content: '',
        toolCalls: [],
        timestamp: '2026-02-26T12:00:00Z',
      }],
    }

    state = agentEventReducer(state, makeEvent('tool_use_start', {
      toolCallId: 'tc-1',
      toolName: 'bash',
      input: { command: 'ls -la' },
    }))

    expect(state.activityStatus).toMatchObject({
      label: 'Running: Run Command',
      detail: 'ls -la',
      category: 'tool',
    })
  })

  it('does not mutate the original state', () => {
    const state = initialAgentEventState()
    const original = { ...state, messages: [...state.messages] }

    agentEventReducer(state, makeEvent('message_start', {
      role: 'assistant',
      messageId: 'msg-1',
    }))

    // Original state should be unchanged
    expect(state.currentMessage).toBeNull()
    expect(state.messages).toEqual(original.messages)
  })

  // --- Message splitting at tool boundaries ---

  it('message_start creates message in state immediately', () => {
    let state = initialAgentEventState()

    state = agentEventReducer(state, makeEvent('message_start', {
      role: 'assistant',
      messageId: 'msg-1',
    }))

    // Message exists with empty content — no content_delta needed
    expect(state.messages).toHaveLength(1)
    expect(state.messages[0]).toMatchObject({
      id: 'msg-1',
      role: 'assistant',
      content: '',
      toolCalls: [],
    })
    expect(state.currentMessage).toEqual({ id: 'msg-1', content: '' })
    expect(state.originalMessageId).toBe('msg-1')
    expect(state.hasActiveToolCalls).toBe(false)
    expect(state.continuationCount).toBe(0)
  })

  it('tool_use_start on empty message still attaches correctly', () => {
    let state = initialAgentEventState()

    state = agentEventReducer(state, makeEvent('message_start', {
      role: 'assistant',
      messageId: 'msg-1',
    }))

    state = agentEventReducer(state, makeEvent('tool_use_start', {
      toolCallId: 'tc-1',
      toolName: 'render_table',
      input: { spec: 'Age by Gender' },
    }))

    // Tool call on the message created at message_start
    expect(state.messages).toHaveLength(1)
    expect(state.messages[0].id).toBe('msg-1')
    expect(state.messages[0].content).toBe('')
    expect(state.messages[0].toolCalls).toHaveLength(1)
    expect(state.messages[0].toolCalls![0].id).toBe('tc-1')
    expect(state.hasActiveToolCalls).toBe(true)
  })

  it('content_delta after tool_use_start creates continuation message', () => {
    let state = initialAgentEventState()

    // message_start → content_delta("before") → tool_use_start → content_delta("after")
    state = agentEventReducer(state, makeEvent('message_start', {
      role: 'assistant',
      messageId: 'msg-1',
    }))
    state = agentEventReducer(state, makeEvent('content_delta', { delta: 'I\'ll analyze the data' }))
    state = agentEventReducer(state, makeEvent('tool_use_start', {
      toolCallId: 'tc-1',
      toolName: 'render_table',
      input: {},
    }))
    state = agentEventReducer(state, makeEvent('content_delta', { delta: 'Here\'s the result' }))

    // Should produce 2 messages
    expect(state.messages).toHaveLength(2)
    expect(state.messages[0]).toMatchObject({
      id: 'msg-1',
      content: 'I\'ll analyze the data',
    })
    expect(state.messages[0].toolCalls).toHaveLength(1)
    expect(state.messages[0].toolCalls![0].id).toBe('tc-1')

    expect(state.messages[1]).toMatchObject({
      id: 'msg-1-cont-1',
      role: 'assistant',
      content: 'Here\'s the result',
    })
    expect(state.messages[1].toolCalls).toEqual([])

    // currentMessage should point at continuation
    expect(state.currentMessage).toEqual({ id: 'msg-1-cont-1', content: 'Here\'s the result' })
    expect(state.continuationCount).toBe(1)
  })

  it('multiple tool-text cycles create multiple continuations', () => {
    let state = initialAgentEventState()

    // text → tool → text → tool → text  →  3 messages
    state = agentEventReducer(state, makeEvent('message_start', {
      role: 'assistant',
      messageId: 'msg-1',
    }))
    state = agentEventReducer(state, makeEvent('content_delta', { delta: 'Part 1' }))

    state = agentEventReducer(state, makeEvent('tool_use_start', {
      toolCallId: 'tc-1', toolName: 'render_table', input: {},
    }))
    state = agentEventReducer(state, makeEvent('tool_use_end', {
      toolCallId: 'tc-1', isError: false, output: 'table data',
    }))
    state = agentEventReducer(state, makeEvent('content_delta', { delta: 'Part 2' }))

    state = agentEventReducer(state, makeEvent('tool_use_start', {
      toolCallId: 'tc-2', toolName: 'render_chart', input: {},
    }))
    state = agentEventReducer(state, makeEvent('tool_use_end', {
      toolCallId: 'tc-2', isError: false, output: 'chart data',
    }))
    state = agentEventReducer(state, makeEvent('content_delta', { delta: 'Part 3' }))

    state = agentEventReducer(state, makeEvent('message_end', {}))

    expect(state.messages).toHaveLength(3)
    expect(state.messages[0]).toMatchObject({ id: 'msg-1', content: 'Part 1' })
    expect(state.messages[0].toolCalls).toHaveLength(1)
    expect(state.messages[0].toolCalls![0].id).toBe('tc-1')

    expect(state.messages[1]).toMatchObject({ id: 'msg-1-cont-1', content: 'Part 2' })
    expect(state.messages[1].toolCalls).toHaveLength(1)
    expect(state.messages[1].toolCalls![0].id).toBe('tc-2')

    expect(state.messages[2]).toMatchObject({ id: 'msg-1-cont-2', content: 'Part 3' })
    expect(state.messages[2].toolCalls).toEqual([])

    expect(state.continuationCount).toBe(2)
    expect(state.currentMessage).toBeNull() // message_end clears it
  })

  // --- Rendered content parsing ---

  it('tool_use_end with render_table populates renderedTables (with dedup for table_rendered)', () => {
    const tableOutput = JSON.stringify({
      content: [{ type: 'text', text: JSON.stringify({
        __render_table: true,
        platinumData: { rows: [['Total', '100']], columns: ['Category', 'Count'] },
        title: 'Test Table',
      })}],
    })

    let state = initialAgentEventState()
    state = agentEventReducer(state, makeEvent('message_start', {
      role: 'assistant', messageId: 'msg-1',
    }))
    state = agentEventReducer(state, makeEvent('tool_use_start', {
      toolCallId: 'tc-1', toolName: 'mcp__platinum__render_table', input: {},
    }))
    state = agentEventReducer(state, makeEvent('tool_use_end', {
      toolCallId: 'tc-1', isError: false, output: tableOutput,
    }))

    // tool_use_end parses __render_table and populates renderedTables.
    // A subsequent table_rendered SSE event with the same toolCallId is deduped.
    expect(state.renderedTables.size).toBe(1)
    const table = state.renderedTables.get('table-tc-1')
    expect(table).toBeDefined()
    expect(table!.title).toBe('Test Table')
    expect(table!.toolCallId).toBe('tc-1')
  })

  it('tool_use_end with render_chart populates renderedCharts (with dedup for chart_rendered)', () => {
    const chartOutput = JSON.stringify({
      content: [{ type: 'text', text: JSON.stringify({
        __render_chart: true,
        platinumData: { rows: [['Q1', '50']], columns: ['Quarter', 'Sales'] },
        chartType: 'line',
        title: 'Sales Trend',
      })}],
    })

    let state = initialAgentEventState()
    state = agentEventReducer(state, makeEvent('message_start', {
      role: 'assistant', messageId: 'msg-1',
    }))
    state = agentEventReducer(state, makeEvent('tool_use_start', {
      toolCallId: 'tc-1', toolName: 'mcp__platinum__render_chart', input: {},
    }))
    state = agentEventReducer(state, makeEvent('tool_use_end', {
      toolCallId: 'tc-1', isError: false, output: chartOutput,
    }))

    // tool_use_end parses __render_chart and populates renderedCharts.
    // A subsequent chart_rendered SSE event with the same toolCallId is deduped.
    expect(state.renderedCharts.size).toBe(1)
    const chart = state.renderedCharts.get('chart-tc-1')
    expect(chart).toBeDefined()
    expect(chart!.title).toBe('Sales Trend')
    expect(chart!.chartType).toBe('line')
  })

  it('tool_use_end with ask_user populates askedQuestions', () => {
    const askOutput = JSON.stringify({
      content: [{ type: 'text', text: JSON.stringify({
        __ask_user: true,
        question: 'Which variable?',
        options: [{ label: 'Age', value: 'age' }, { label: 'Gender', value: 'gender' }],
        allow_freetext: true,
        context: 'Select a variable for analysis',
      })}],
    })

    let state = initialAgentEventState()
    state = agentEventReducer(state, makeEvent('message_start', {
      role: 'assistant', messageId: 'msg-1',
    }))
    state = agentEventReducer(state, makeEvent('tool_use_start', {
      toolCallId: 'tc-1', toolName: 'mcp__platinum__ask_user', input: {},
    }))
    state = agentEventReducer(state, makeEvent('tool_use_end', {
      toolCallId: 'tc-1', isError: false, output: askOutput,
    }))

    expect(state.askedQuestions.size).toBe(1)
    const q = state.askedQuestions.get('tc-1')
    expect(q).toBeDefined()
    expect(q!.question).toBe('Which variable?')
    expect(q!.options).toHaveLength(2)
    expect(q!.allowFreetext).toBe(true)
    expect(q!.answered).toBe(false)
  })

  it('tool_use_end with error skips rendered content parsing', () => {
    const tableOutput = JSON.stringify({
      content: [{ type: 'text', text: JSON.stringify({
        __render_table: true,
        platinumData: { rows: [], columns: [] },
        title: 'Should Not Appear',
      })}],
    })

    let state = initialAgentEventState()
    state = agentEventReducer(state, makeEvent('message_start', {
      role: 'assistant', messageId: 'msg-1',
    }))
    state = agentEventReducer(state, makeEvent('tool_use_start', {
      toolCallId: 'tc-1', toolName: 'mcp__platinum__render_table', input: {},
    }))
    state = agentEventReducer(state, makeEvent('tool_use_end', {
      toolCallId: 'tc-1', isError: true, output: tableOutput,
    }))

    expect(state.renderedTables.size).toBe(0)
  })

  it('table_rendered event populates renderedTables', () => {
    let state = initialAgentEventState()

    state = agentEventReducer(state, makeEvent('table_rendered', {
      id: 'tbl-42',
      platinumData: { rows: [['A', '1']], columns: ['Col1', 'Col2'] },
      title: 'Backend Table',
      toolCallId: 'tc-99',
    }))

    expect(state.renderedTables.size).toBe(1)
    const table = state.renderedTables.get('tbl-42')
    expect(table).toBeDefined()
    expect(table!.title).toBe('Backend Table')
    expect(table!.toolCallId).toBe('tc-99')
  })

  // --- Parallel tool calls ---

  it('parallel tool calls all complete correctly', () => {
    let state = initialAgentEventState()

    // message_start → content_delta → 3x tool_use_start → 3x tool_use_end → message_end
    state = agentEventReducer(state, makeEvent('message_start', {
      role: 'assistant',
      messageId: 'msg-1',
    }))
    state = agentEventReducer(state, makeEvent('content_delta', { delta: "I'll look up those variables" }))

    // 3 parallel tool starts
    state = agentEventReducer(state, makeEvent('tool_use_start', {
      toolCallId: 'tc-1', toolName: 'bash', input: { command: 'pt vars gender' },
    }))
    state = agentEventReducer(state, makeEvent('tool_use_start', {
      toolCallId: 'tc-2', toolName: 'bash', input: { command: 'pt vars agegroups1' },
    }))
    state = agentEventReducer(state, makeEvent('tool_use_start', {
      toolCallId: 'tc-3', toolName: 'bash', input: { command: 'pt vars watchfreql12m_2' },
    }))

    // All 3 tools should be running on msg-1
    expect(state.messages).toHaveLength(1)
    expect(state.messages[0].toolCalls).toHaveLength(3)
    expect(state.messages[0].toolCalls!.every(tc => tc.status === 'running')).toBe(true)
    expect(state.toolCalls.size).toBe(3)

    // 3 parallel tool ends
    state = agentEventReducer(state, makeEvent('tool_use_end', {
      toolCallId: 'tc-1', isError: false, output: 'gender codes...',
    }))
    state = agentEventReducer(state, makeEvent('tool_use_end', {
      toolCallId: 'tc-2', isError: false, output: 'agegroups1 codes...',
    }))
    state = agentEventReducer(state, makeEvent('tool_use_end', {
      toolCallId: 'tc-3', isError: false, output: 'watchfreql12m_2 codes...',
    }))

    // All 3 tools should be complete
    expect(state.messages[0].toolCalls).toHaveLength(3)
    expect(state.messages[0].toolCalls!.every(tc => tc.status === 'complete')).toBe(true)
    expect(state.messages[0].toolCalls![0].output).toBe('gender codes...')
    expect(state.messages[0].toolCalls![1].output).toBe('agegroups1 codes...')
    expect(state.messages[0].toolCalls![2].output).toBe('watchfreql12m_2 codes...')

    // No tools should be running in the toolCalls map
    const anyRunning = Array.from(state.toolCalls.values()).some(t => t.status === 'running')
    expect(anyRunning).toBe(false)

    // Activity should revert to thinking (no tools running)
    expect(state.activityStatus).toMatchObject({ label: 'Thinking...', category: 'thinking' })

    state = agentEventReducer(state, makeEvent('message_end', {}))

    // Second message with continuation text
    state = agentEventReducer(state, makeEvent('message_start', {
      role: 'assistant',
      messageId: 'msg-2',
    }))
    state = agentEventReducer(state, makeEvent('content_delta', { delta: 'Here are the results' }))
    state = agentEventReducer(state, makeEvent('message_end', {}))

    // Final state: 2 messages
    expect(state.messages).toHaveLength(2)
    expect(state.messages[0]).toMatchObject({
      id: 'msg-1',
      content: "I'll look up those variables",
    })
    expect(state.messages[0].toolCalls).toHaveLength(3)
    expect(state.messages[0].toolCalls!.every(tc => tc.status === 'complete')).toBe(true)

    expect(state.messages[1]).toMatchObject({
      id: 'msg-2',
      role: 'assistant',
      content: 'Here are the results',
    })
    expect(state.messages[1].toolCalls).toEqual([])
    expect(state.currentMessage).toBeNull()
  })

  it('parallel tool calls with same-message continuation text', () => {
    let state = initialAgentEventState()

    // All within the same message_start/message_end boundary
    state = agentEventReducer(state, makeEvent('message_start', {
      role: 'assistant',
      messageId: 'msg-1',
    }))
    state = agentEventReducer(state, makeEvent('content_delta', { delta: 'Looking up...' }))

    // 3 parallel tool starts
    state = agentEventReducer(state, makeEvent('tool_use_start', {
      toolCallId: 'tc-1', toolName: 'bash', input: { command: 'pt vars gender' },
    }))
    state = agentEventReducer(state, makeEvent('tool_use_start', {
      toolCallId: 'tc-2', toolName: 'bash', input: { command: 'pt vars age' },
    }))
    state = agentEventReducer(state, makeEvent('tool_use_start', {
      toolCallId: 'tc-3', toolName: 'bash', input: { command: 'pt vars region' },
    }))

    // All 3 complete
    state = agentEventReducer(state, makeEvent('tool_use_end', {
      toolCallId: 'tc-1', isError: false, output: 'gender output',
    }))
    state = agentEventReducer(state, makeEvent('tool_use_end', {
      toolCallId: 'tc-2', isError: false, output: 'age output',
    }))
    state = agentEventReducer(state, makeEvent('tool_use_end', {
      toolCallId: 'tc-3', isError: false, output: 'region output',
    }))

    // Content delta arrives in same message after tools complete → should split
    state = agentEventReducer(state, makeEvent('content_delta', { delta: 'Results summary' }))
    state = agentEventReducer(state, makeEvent('message_end', {}))

    // Should produce 2 messages: original with tools + continuation with text
    expect(state.messages).toHaveLength(2)
    expect(state.messages[0].id).toBe('msg-1')
    expect(state.messages[0].toolCalls).toHaveLength(3)
    expect(state.messages[0].toolCalls!.every(tc => tc.status === 'complete')).toBe(true)

    expect(state.messages[1].id).toBe('msg-1-cont-1')
    expect(state.messages[1].content).toBe('Results summary')
    expect(state.messages[1].toolCalls).toEqual([])
  })

  it('parallel tool_use_end across message_start boundaries', () => {
    let state = initialAgentEventState()

    // msg-1: two parallel tool starts, first result arrives
    state = agentEventReducer(state, makeEvent('message_start', {
      role: 'assistant', messageId: 'msg-1',
    }))
    state = agentEventReducer(state, makeEvent('tool_use_start', {
      toolCallId: 'tc-1', toolName: 'bash', input: { command: 'pt vars gender' },
    }))
    state = agentEventReducer(state, makeEvent('tool_use_start', {
      toolCallId: 'tc-2', toolName: 'bash', input: { command: 'pt vars age' },
    }))
    state = agentEventReducer(state, makeEvent('tool_use_end', {
      toolCallId: 'tc-1', isError: false, output: 'gender codes',
    }))
    state = agentEventReducer(state, makeEvent('message_end', {}))

    // Sanity: tc-1 complete, tc-2 still running on msg-1
    expect(state.messages[0].toolCalls!.find(tc => tc.id === 'tc-1')!.status).toBe('complete')
    expect(state.messages[0].toolCalls!.find(tc => tc.id === 'tc-2')!.status).toBe('running')

    // msg-2: second result arrives in a new message envelope (Map is reset!)
    state = agentEventReducer(state, makeEvent('message_start', {
      role: 'assistant', messageId: 'msg-2',
    }))
    state = agentEventReducer(state, makeEvent('tool_use_end', {
      toolCallId: 'tc-2', isError: false, output: 'age codes',
    }))
    state = agentEventReducer(state, makeEvent('message_end', {}))

    // msg-3: assistant response text
    state = agentEventReducer(state, makeEvent('message_start', {
      role: 'assistant', messageId: 'msg-3',
    }))
    state = agentEventReducer(state, makeEvent('content_delta', { delta: 'Here are the results' }))
    state = agentEventReducer(state, makeEvent('message_end', {}))
    state = agentEventReducer(state, makeEvent('query_complete', {}))

    // Both tools on msg-1 must be 'complete' — tc-2 must NOT be stuck as 'running'
    const msg1Tools = state.messages[0].toolCalls!
    expect(msg1Tools).toHaveLength(2)
    expect(msg1Tools.find(tc => tc.id === 'tc-1')!.status).toBe('complete')
    expect(msg1Tools.find(tc => tc.id === 'tc-1')!.output).toBe('gender codes')
    expect(msg1Tools.find(tc => tc.id === 'tc-2')!.status).toBe('complete')
    expect(msg1Tools.find(tc => tc.id === 'tc-2')!.output).toBe('age codes')

    // No tools stuck running across any message
    for (const msg of state.messages) {
      for (const tc of msg.toolCalls || []) {
        expect(tc.status, `Tool ${tc.id} stuck running on message ${msg.id}`).not.toBe('running')
      }
    }
  })

  it('full streaming scenario produces same structure as reload', () => {
    const tableOutput = JSON.stringify({
      content: [{ type: 'text', text: JSON.stringify({
        __render_table: true,
        platinumData: { rows: [['Male', '55'], ['Female', '45']], columns: ['Gender', 'Count'] },
        title: 'Gender Distribution',
      })}],
    })

    let state = initialAgentEventState()

    // Simulate: assistant says something, calls render_table, then says more
    state = agentEventReducer(state, makeEvent('message_start', {
      role: 'assistant', messageId: 'msg-abc',
    }))
    state = agentEventReducer(state, makeEvent('content_delta', { delta: 'Let me ' }))
    state = agentEventReducer(state, makeEvent('content_delta', { delta: 'run that table.' }))
    state = agentEventReducer(state, makeEvent('tool_use_start', {
      toolCallId: 'tc-render', toolName: 'mcp__platinum__render_table',
      input: { spec: 'Gender' },
    }))
    state = agentEventReducer(state, makeEvent('tool_use_end', {
      toolCallId: 'tc-render', isError: false, output: tableOutput,
    }))
    // table_rendered SSE event arrives (sent by agent post-tool hook)
    state = agentEventReducer(state, makeEvent('table_rendered', {
      id: 'table-1710000000-abc123',
      platinumData: { rows: [['Male', '55'], ['Female', '45']], columns: ['Gender', 'Count'] },
      title: 'Gender Distribution',
      toolCallId: 'tc-render',
    }))
    state = agentEventReducer(state, makeEvent('content_delta', { delta: 'The table shows ' }))
    state = agentEventReducer(state, makeEvent('content_delta', { delta: 'a gender split.' }))
    state = agentEventReducer(state, makeEvent('message_end', {}))

    // Should produce 2 messages matching reload structure
    expect(state.messages).toHaveLength(2)

    // First message: text + tool call
    expect(state.messages[0]).toMatchObject({
      id: 'msg-abc',
      role: 'assistant',
      content: 'Let me run that table.',
    })
    expect(state.messages[0].toolCalls).toHaveLength(1)
    expect(state.messages[0].toolCalls![0]).toMatchObject({
      id: 'tc-render',
      name: 'mcp__platinum__render_table',
      status: 'complete',
    })

    // Second message: continuation text, no tool calls
    expect(state.messages[1]).toMatchObject({
      id: 'msg-abc-cont-1',
      role: 'assistant',
      content: 'The table shows a gender split.',
    })
    expect(state.messages[1].toolCalls).toEqual([])

    // SSE table_rendered replaces self-parse fallback (SSE is authoritative)
    expect(state.renderedTables.size).toBe(1)
    const table = state.renderedTables.get('table-1710000000-abc123')
    expect(table).toBeDefined()
    expect(table!.title).toBe('Gender Distribution')
    expect(table!.toolCallId).toBe('tc-render')

    // currentMessage cleared by message_end
    expect(state.currentMessage).toBeNull()
  })

  it('TodoWrite tool_use_end extracts todos from tool input', () => {
    let state = initialAgentEventState()

    state = agentEventReducer(state, makeEvent('message_start', {
      role: 'assistant', messageId: 'msg-1',
    }))
    state = agentEventReducer(state, makeEvent('tool_use_start', {
      toolCallId: 'tc-todo',
      toolName: 'TodoWrite',
      input: {
        todos: [
          { content: 'Analyze data', status: 'in_progress' },
          { content: 'Write report', status: 'pending' },
          { content: 'Send email', status: 'completed' },
        ],
      },
    }))
    state = agentEventReducer(state, makeEvent('tool_use_end', {
      toolCallId: 'tc-todo',
      isError: false,
      output: '[{"type":"text","text":"Todos have been updated."}]',
    }))

    expect(state.todos).toHaveLength(3)
    expect(state.todos[0]).toEqual({ content: 'Analyze data', status: 'in_progress' })
    expect(state.todos[1]).toEqual({ content: 'Write report', status: 'pending' })
    expect(state.todos[2]).toEqual({ content: 'Send email', status: 'completed' })
  })

  it('TodoWrite tool_use_end with error does not set todos', () => {
    let state = initialAgentEventState()

    state = agentEventReducer(state, makeEvent('message_start', {
      role: 'assistant', messageId: 'msg-1',
    }))
    state = agentEventReducer(state, makeEvent('tool_use_start', {
      toolCallId: 'tc-todo',
      toolName: 'TodoWrite',
      input: { todos: [{ content: 'Task', status: 'pending' }] },
    }))
    state = agentEventReducer(state, makeEvent('tool_use_end', {
      toolCallId: 'tc-todo',
      isError: true,
      output: 'Error writing todos',
    }))

    expect(state.todos).toEqual([])
  })

  it('user_message adds a user message to the timeline', () => {
    let state = initialAgentEventState()
    state = agentEventReducer(state, makeEvent('user_message', {
      id: 'user-1',
      content: 'Hello agent',
    }))
    expect(state.messages).toHaveLength(1)
    expect(state.messages[0]).toMatchObject({
      id: 'user-1',
      role: 'user',
      content: 'Hello agent',
    })
  })

  it('user_message does not add duplicate messages', () => {
    let state = initialAgentEventState()
    state = agentEventReducer(state, makeEvent('user_message', {
      id: 'user-1',
      content: 'Hello agent',
    }))
    state = agentEventReducer(state, makeEvent('user_message', {
      id: 'user-1',
      content: 'Hello agent',
    }))
    expect(state.messages).toHaveLength(1)
  })

  it('folds skill_installed into installedSkills (latest wins by name)', () => {
    let s = initialAgentEventState()
    s = agentEventReducer(s, { type: 'skill_installed', data: { id: 's1', name: 'hello', requires_build: false }, timestamp: '' } as any)
    s = agentEventReducer(s, { type: 'skill_installed', data: { id: 's2', name: 'hello', requires_build: false }, timestamp: '' } as any)
    s = agentEventReducer(s, { type: 'skill_installed', data: { id: 's3', name: 'other', requires_build: true }, timestamp: '' } as any)
    expect(s.installedSkills.map(x => x.name).sort()).toEqual(['hello', 'other'])
    expect(s.installedSkills.find(x => x.name === 'hello')?.id).toBe('s2')
  })

  it('ask_user with advance options preserves advance field', () => {
    const askOutput = JSON.stringify({
      content: [{ type: 'text', text: JSON.stringify({
        __ask_user: true,
        question: 'Ready to proceed?',
        options: [
          { label: 'Continue searching', value: 'continue', advance: false },
          { label: 'Proceed to Plan', value: 'advance', advance: true },
        ],
        allow_freetext: false,
        context: 'Found 5 variables',
      })}],
    })

    let state = initialAgentEventState()
    state = agentEventReducer(state, makeEvent('message_start', {
      role: 'assistant', messageId: 'msg-1',
    }))
    state = agentEventReducer(state, makeEvent('tool_use_start', {
      toolCallId: 'tc-1', toolName: 'mcp__ui__ask_user', input: {},
    }))
    state = agentEventReducer(state, makeEvent('tool_use_end', {
      toolCallId: 'tc-1', isError: false, output: askOutput,
    }))

    expect(state.askedQuestions.size).toBe(1)
    const q = state.askedQuestions.get('tc-1')!
    expect(q.options).toHaveLength(2)
    expect(q.options[0].advance).toBe(false)
    expect(q.options[1].advance).toBe(true)
  })
})
