import { describe, it, expect } from 'vitest'
import { replayEvents, persistedToEvents, replayFromPersistedMessages } from './replayEvents.js'
import type { AgentSSEEvent, PersistedAgentMessage } from './types.js'

function makeEvent(type: AgentSSEEvent['type'], data: Record<string, unknown>): AgentSSEEvent {
  return { type, data, timestamp: '2026-02-26T12:00:00Z' }
}

describe('replayEvents', () => {
  it('replays a simple user->assistant exchange', () => {
    const events: AgentSSEEvent[] = [
      makeEvent('user_message', { id: 'user-1', content: 'Hello' }),
      makeEvent('message_start', { role: 'assistant', messageId: 'msg-1' }),
      makeEvent('content_delta', { delta: 'Hi there!' }),
      makeEvent('message_end', {}),
      makeEvent('query_complete', { result: '' }),
    ]

    const state = replayEvents(events)
    expect(state.messages).toHaveLength(2)
    expect(state.messages[0]).toMatchObject({ role: 'user', content: 'Hello' })
    expect(state.messages[1]).toMatchObject({ role: 'assistant', content: 'Hi there!' })
    expect(state.isStreaming).toBe(false)
    expect(state.currentMessage).toBeNull()
  })

  it('replays tool calls with tables', () => {
    const tableOutput = JSON.stringify({
      content: [{ type: 'text', text: JSON.stringify({
        __render_table: true,
        platinumData: { cells: [] },
        title: 'Test Table',
        customer: 'test',
        job: 'test-job',
      })}],
    })

    const events: AgentSSEEvent[] = [
      makeEvent('user_message', { id: 'user-1', content: 'Show table' }),
      makeEvent('message_start', { role: 'assistant', messageId: 'msg-1' }),
      makeEvent('tool_use_start', { toolCallId: 'tc-1', toolName: 'mcp__data__render_table', input: {} }),
      makeEvent('tool_use_end', { toolCallId: 'tc-1', output: tableOutput, isError: false }),
      makeEvent('message_end', {}),
      makeEvent('query_complete', {}),
    ]

    const state = replayEvents(events)
    expect(state.renderedTables.size).toBe(1)
    const table = state.renderedTables.get('table-tc-1')
    expect(table).toBeDefined()
    expect(table?.title).toBe('Test Table')
  })

  it('marks answered ask_user questions', () => {
    const askOutput = JSON.stringify({
      content: [{ type: 'text', text: JSON.stringify({
        __ask_user: true,
        question: 'Which option?',
        options: [{ label: 'A', value: 'a' }, { label: 'B', value: 'b' }],
        allow_freetext: false,
        context: '',
      })}],
    })

    const events: AgentSSEEvent[] = [
      makeEvent('user_message', { id: 'user-1', content: 'Do something' }),
      makeEvent('message_start', { role: 'assistant', messageId: 'msg-1' }),
      makeEvent('tool_use_start', { toolCallId: 'tc-ask', toolName: 'mcp__data__ask_user', input: {} }),
      makeEvent('tool_use_end', { toolCallId: 'tc-ask', output: askOutput, isError: false }),
      makeEvent('message_end', {}),
      makeEvent('query_complete', {}),
      // User answered
      makeEvent('user_message', { id: 'user-2', content: 'a' }),
    ]

    const state = replayEvents(events)
    const question = state.askedQuestions.get('tc-ask')
    expect(question?.answered).toBe(true)
    expect(question?.selectedValue).toBe('a')
  })

  it('clears transient streaming state', () => {
    const events: AgentSSEEvent[] = [
      makeEvent('message_start', { role: 'assistant', messageId: 'msg-1' }),
      makeEvent('content_delta', { delta: 'Hello' }),
      // Intentionally no message_end — simulating incomplete stream
    ]

    const state = replayEvents(events)
    expect(state.isStreaming).toBe(false)
    expect(state.activityStatus).toBeNull()
    expect(state.currentMessage).toBeNull()
    expect(state.toolInputBuffer).toBe('')
  })

  // ==========================================
  // Fragment merging tests
  // ==========================================

  it('merges content split across tool boundary into coherent text blocks', () => {
    // Simulates: assistant writes "I found the template and" → tool call → "ran the table."
    // The reducer creates a continuation message at the tool boundary.
    // On restore, the content before and after should NOT be merged because
    // there are tool calls between them (preserves text→tools→text interleaving).
    const events: AgentSSEEvent[] = [
      makeEvent('user_message', { id: 'user-1', content: 'Do it' }),
      makeEvent('message_start', { role: 'assistant', messageId: 'msg-1' }),
      makeEvent('content_delta', { delta: 'I found the template and ' }),
      makeEvent('tool_use_start', { toolCallId: 'tc-1', toolName: 'bash', input: { command: 'ls' } }),
      makeEvent('tool_use_end', { toolCallId: 'tc-1', output: 'files', isError: false }),
      // content_delta after tool_use_start triggers continuation split in reducer
      makeEvent('content_delta', { delta: 'ran the table. Let me extract the style.' }),
      makeEvent('message_end', {}),
      makeEvent('query_complete', {}),
    ]

    const state = replayEvents(events)
    // Should be: user, assistant(text+tool), assistant(continuation text)
    // The continuation IS preserved because there are tools on the first message
    expect(state.messages).toHaveLength(3)
    expect(state.messages[1].content).toBe('I found the template and ')
    expect(state.messages[1].toolCalls).toHaveLength(1)
    expect(state.messages[2].content).toBe('ran the table. Let me extract the style.')
    expect(state.messages[2].toolCalls).toEqual([])
  })

  it('merges consecutive assistant fragments with no tools between them', () => {
    // Two consecutive assistant messages where the first has no tools = merge them
    const events: AgentSSEEvent[] = [
      makeEvent('user_message', { id: 'user-1', content: 'Hello' }),
      makeEvent('message_start', { role: 'assistant', messageId: 'msg-1' }),
      makeEvent('content_delta', { delta: 'Part one. ' }),
      makeEvent('message_end', {}),
      // New message_start (e.g. from a reconnect or new turn within same query)
      makeEvent('message_start', { role: 'assistant', messageId: 'msg-2' }),
      makeEvent('content_delta', { delta: 'Part two.' }),
      makeEvent('message_end', {}),
      makeEvent('query_complete', {}),
    ]

    const state = replayEvents(events)
    // Two assistant msgs with no tools → merged into one
    expect(state.messages).toHaveLength(2)
    expect(state.messages[0].content).toBe('Hello')
    expect(state.messages[1].content).toBe('Part one. Part two.')
  })

  it('merges fragmented thinking/reasoning into single block', () => {
    // Thinking can get split across chunk boundaries during streaming
    const events: AgentSSEEvent[] = [
      makeEvent('user_message', { id: 'user-1', content: 'Think about this' }),
      makeEvent('message_start', { role: 'assistant', messageId: 'msg-1' }),
      makeEvent('thinking_delta', { delta: 'Let me first summarize ' }),
      makeEvent('thinking_delta', { delta: 'the style guide.' }),
      makeEvent('content_delta', { delta: 'Here is my analysis.' }),
      makeEvent('message_end', {}),
      makeEvent('query_complete', {}),
    ]

    const state = replayEvents(events)
    expect(state.messages).toHaveLength(2)
    expect(state.messages[1].thinking).toBe('Let me first summarize the style guide.')
    expect(state.messages[1].content).toBe('Here is my analysis.')
  })

  it('merges thinking across consecutive assistant fragments without tools', () => {
    // Edge case: thinking split across two message boundaries
    const events: AgentSSEEvent[] = [
      makeEvent('user_message', { id: 'user-1', content: 'Analyze' }),
      makeEvent('message_start', { role: 'assistant', messageId: 'msg-1' }),
      makeEvent('thinking_delta', { delta: 'The user has uploaded ' }),
      makeEvent('message_end', {}),
      makeEvent('message_start', { role: 'assistant', messageId: 'msg-2' }),
      makeEvent('thinking_delta', { delta: 'a template and wants me to' }),
      makeEvent('content_delta', { delta: 'Full analysis here.' }),
      makeEvent('message_end', {}),
      makeEvent('query_complete', {}),
    ]

    const state = replayEvents(events)
    expect(state.messages).toHaveLength(2)
    expect(state.messages[1].thinking).toBe('The user has uploaded a template and wants me to')
    expect(state.messages[1].content).toBe('Full analysis here.')
  })

  it('preserves markdown tables that span content — no mid-table splits', () => {
    // A markdown table that would be split mid-row during live streaming
    // should be merged into a single coherent content block on restore
    const tablePart1 = '**Key findings:**\n\n| Frequency | % |\n|---|---|\n| Every day | 3% |\n| Weekly | 12% |\n| Once every 2'
    const tablePart2 = ' months | 18% |\n| A few times a season | 30% |\n\nThe data shows...'

    const events: AgentSSEEvent[] = [
      makeEvent('user_message', { id: 'user-1', content: 'Show data' }),
      makeEvent('message_start', { role: 'assistant', messageId: 'msg-1' }),
      makeEvent('content_delta', { delta: tablePart1 }),
      makeEvent('message_end', {}),
      makeEvent('message_start', { role: 'assistant', messageId: 'msg-2' }),
      makeEvent('content_delta', { delta: tablePart2 }),
      makeEvent('message_end', {}),
      makeEvent('query_complete', {}),
    ]

    const state = replayEvents(events)
    // The two fragments should be merged into one complete message
    expect(state.messages).toHaveLength(2)
    expect(state.messages[1].content).toBe(tablePart1 + tablePart2)
    // Verify the markdown table is intact (contains both header and all rows)
    expect(state.messages[1].content).toContain('| Once every 2 months | 18% |')
    expect(state.messages[1].content).toContain('The data shows...')
  })

  it('replays multiple queries in sequence', () => {
    const events: AgentSSEEvent[] = [
      // Query 1
      makeEvent('user_message', { id: 'user-1', content: 'First question' }),
      makeEvent('message_start', { role: 'assistant', messageId: 'msg-1' }),
      makeEvent('content_delta', { delta: 'First answer' }),
      makeEvent('message_end', {}),
      makeEvent('query_complete', {}),
      // Query 2
      makeEvent('user_message', { id: 'user-2', content: 'Second question' }),
      makeEvent('message_start', { role: 'assistant', messageId: 'msg-2' }),
      makeEvent('content_delta', { delta: 'Second answer' }),
      makeEvent('message_end', {}),
      makeEvent('query_complete', {}),
    ]

    const state = replayEvents(events)
    expect(state.messages).toHaveLength(4)
    expect(state.messages[0].content).toBe('First question')
    expect(state.messages[1].content).toBe('First answer')
    expect(state.messages[2].content).toBe('Second question')
    expect(state.messages[3].content).toBe('Second answer')
  })
})

describe('persistedToEvents', () => {
  it('converts a simple user+assistant pair to events', () => {
    const messages: PersistedAgentMessage[] = [
      { id: 'msg-1', session_id: 's1', query_id: 'q1', phase_node: '', role: 'user', content: 'Hello', sequence_num: 1, created_at: 1700000000 },
      { id: 'msg-2', session_id: 's1', query_id: 'q1', phase_node: '', role: 'assistant', content: 'Hi there!', sequence_num: 2, created_at: 1700000001 },
    ]

    const events = persistedToEvents(messages)
    expect(events.some(e => e.type === 'user_message')).toBe(true)
    expect(events.some(e => e.type === 'message_start')).toBe(true)
    expect(events.some(e => e.type === 'content_delta')).toBe(true)
    expect(events.some(e => e.type === 'message_end')).toBe(true)

    const userEvent = events.find(e => e.type === 'user_message')
    expect((userEvent?.data as Record<string, unknown>)?.content).toBe('Hello')

    const contentEvent = events.find(e => e.type === 'content_delta')
    expect((contentEvent?.data as Record<string, unknown>)?.delta).toBe('Hi there!')
  })

  it('converts tool_call and tool_result rows to tool events', () => {
    const messages: PersistedAgentMessage[] = [
      { id: 'msg-1', session_id: 's1', query_id: 'q1', phase_node: '', role: 'user', content: 'Run it', sequence_num: 1, created_at: 1700000000 },
      { id: 'msg-2', session_id: 's1', query_id: 'q1', phase_node: '', role: 'assistant', content: 'Running...', sequence_num: 2, created_at: 1700000001 },
      {
        id: 'msg-3',
        session_id: 's1',
        query_id: 'q1',
        phase_node: '',
        role: 'tool_call',
        content: '',
        tool_name: 'bash',
        tool_input: { command: 'ls' },
        sequence_num: 3,
        created_at: 1700000002,
        metadata: { tool_call_id: 'tc-1' },
      },
      {
        id: 'msg-4',
        session_id: 's1',
        query_id: 'q1',
        phase_node: '',
        role: 'tool_result',
        content: 'file1.txt',
        tool_name: 'tc-1',
        sequence_num: 4,
        created_at: 1700000003,
      },
    ]

    const events = persistedToEvents(messages)
    expect(events.some(e => e.type === 'tool_use_start')).toBe(true)
    expect(events.some(e => e.type === 'tool_use_end')).toBe(true)

    const toolStart = events.find(e => e.type === 'tool_use_start')
    expect((toolStart?.data as Record<string, unknown>)?.toolCallId).toBe('tc-1')
    expect((toolStart?.data as Record<string, unknown>)?.toolName).toBe('bash')

    const toolEnd = events.find(e => e.type === 'tool_use_end')
    expect((toolEnd?.data as Record<string, unknown>)?.output).toBe('file1.txt')
  })

  it('sorts messages by sequence_num before converting', () => {
    const messages: PersistedAgentMessage[] = [
      { id: 'msg-2', session_id: 's1', query_id: 'q1', phase_node: '', role: 'assistant', content: 'Hi!', sequence_num: 2, created_at: 1700000001 },
      { id: 'msg-1', session_id: 's1', query_id: 'q1', phase_node: '', role: 'user', content: 'Hello', sequence_num: 1, created_at: 1700000000 },
    ]

    const events = persistedToEvents(messages)
    // user_message should come before message_start
    const userIdx = events.findIndex(e => e.type === 'user_message')
    const assistantIdx = events.findIndex(e => e.type === 'message_start')
    expect(userIdx).toBeLessThan(assistantIdx)
  })

  it('converts thinking metadata into thinking_delta event', () => {
    const messages: PersistedAgentMessage[] = [
      {
        id: 'msg-1',
        session_id: 's1',
        query_id: 'q1',
        phase_node: '',
        role: 'assistant',
        content: 'My answer.',
        sequence_num: 1,
        created_at: 1700000000,
        metadata: { thinking: 'Let me think about this carefully.' },
      },
    ]

    const events = persistedToEvents(messages)
    const thinkingEvent = events.find(e => e.type === 'thinking_delta')
    expect(thinkingEvent).toBeDefined()
    expect((thinkingEvent?.data as Record<string, unknown>)?.delta).toBe('Let me think about this carefully.')
  })

  it('handles __session_info tool_result as session_info event', () => {
    const sessionData = { tools: ['bash'], model: 'claude-3', mcpServers: [] }
    const messages: PersistedAgentMessage[] = [
      { id: 'msg-1', session_id: 's1', query_id: 'q1', phase_node: '', role: 'assistant', content: '', sequence_num: 1, created_at: 1700000000 },
      {
        id: 'msg-2',
        session_id: 's1',
        query_id: 'q1',
        phase_node: '',
        role: 'tool_result',
        content: JSON.stringify(sessionData),
        tool_name: '__session_info',
        sequence_num: 2,
        created_at: 1700000001,
      },
    ]

    const events = persistedToEvents(messages)
    const sessionEvent = events.find(e => e.type === 'session_info')
    expect(sessionEvent).toBeDefined()
    expect((sessionEvent?.data as Record<string, unknown>)?.model).toBe('claude-3')
  })

  it('closes open message_start when next message is a user message', () => {
    const messages: PersistedAgentMessage[] = [
      { id: 'msg-1', session_id: 's1', query_id: 'q1', phase_node: '', role: 'user', content: 'Q1', sequence_num: 1, created_at: 1700000000 },
      { id: 'msg-2', session_id: 's1', query_id: 'q1', phase_node: '', role: 'assistant', content: 'A1', sequence_num: 2, created_at: 1700000001 },
      { id: 'msg-3', session_id: 's1', query_id: 'q2', phase_node: '', role: 'user', content: 'Q2', sequence_num: 3, created_at: 1700000002 },
    ]

    const events = persistedToEvents(messages)
    // Count message_end events — there should be one for the assistant turn
    const messageEnds = events.filter(e => e.type === 'message_end')
    expect(messageEnds.length).toBeGreaterThanOrEqual(1)
  })
})

describe('replayFromPersistedMessages', () => {
  it('returns initial state for empty array', () => {
    const state = replayFromPersistedMessages([])
    expect(state.messages).toHaveLength(0)
    expect(state.isStreaming).toBe(false)
  })

  it('produces correct display state from persisted messages', () => {
    const messages: PersistedAgentMessage[] = [
      { id: 'msg-1', session_id: 's1', query_id: 'q1', phase_node: '', role: 'user', content: 'Hello', sequence_num: 1, created_at: 1700000000 },
      { id: 'msg-2', session_id: 's1', query_id: 'q1', phase_node: '', role: 'assistant', content: 'Hi there!', sequence_num: 2, created_at: 1700000001 },
    ]

    const state = replayFromPersistedMessages(messages)
    expect(state.messages).toHaveLength(2)
    expect(state.messages[0]).toMatchObject({ role: 'user', content: 'Hello' })
    expect(state.messages[1]).toMatchObject({ role: 'assistant', content: 'Hi there!' })
    expect(state.isStreaming).toBe(false)
    expect(state.currentMessage).toBeNull()
  })
})
