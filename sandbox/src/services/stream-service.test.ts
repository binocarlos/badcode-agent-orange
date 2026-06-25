import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { StreamService } from './stream-service.js'
import type { FastifyReply } from 'fastify'

// Mock session-context to avoid AsyncLocalStorage side effects
vi.mock('../session-context.js', async () => {
  const { AsyncLocalStorage } = await import('node:async_hooks')
  return { sessionContext: new AsyncLocalStorage<{ sessionId: string }>() }
})

function makeReply(): FastifyReply & { written: string[] } {
  const written: string[] = []
  const raw = {
    write: vi.fn((data: string) => { written.push(data) }),
    end: vi.fn(),
    on: vi.fn(),
    writeHead: vi.fn(),
    headersSent: false,
  }
  const reply = { raw, written } as unknown as FastifyReply & { written: string[] }
  return reply
}

describe('StreamService — core mechanics', () => {
  let svc: StreamService

  beforeEach(() => {
    vi.useFakeTimers()
    svc = new StreamService()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('addStream + sendEvent writes SSE to the reply', () => {
    const reply = makeReply()
    svc.addStream('q1', reply, 'sess1')
    svc.sendEvent('q1', 'content_delta', { text: 'hello' }, 'sess1')

    expect(reply.written.length).toBe(1)
    expect(reply.written[0]).toContain('event: content_delta')
    expect(reply.written[0]).toContain('"text":"hello"')
  })

  it('buffers events when no stream is connected', () => {
    // Send event before any stream connects
    svc.sendEvent('q2', 'content_delta', { text: 'buffered' }, 'sess2')

    // Now connect — should replay
    const reply = makeReply()
    svc.addStream('q2', reply, 'sess2')

    expect(reply.written.length).toBe(1)
    expect(reply.written[0]).toContain('buffered')
  })

  it('does not grow buffer past MAX_BUFFER_SIZE', () => {
    // Send 2100 events (MAX_BUFFER_SIZE is 2000)
    for (let i = 0; i < 2100; i++) {
      svc.sendEvent('q3', 'content_delta', { i }, 'sess3')
    }

    // Connect and check replay count
    const reply = makeReply()
    svc.addStream('q3', reply, 'sess3')

    // Should have replayed exactly 2000 (capped)
    expect(reply.written.length).toBe(2000)
  })

  it('removeStream causes subsequent events to buffer', () => {
    const reply = makeReply()
    svc.addStream('q4', reply, 'sess4')

    // Send one event — should be written
    svc.sendEvent('q4', 'content_delta', { n: 1 }, 'sess4')
    expect(reply.written.length).toBe(1)

    // Remove stream
    svc.removeStream('q4', reply, 'sess4')

    // Send another event — should buffer, not write
    svc.sendEvent('q4', 'content_delta', { n: 2 }, 'sess4')
    expect(reply.written.length).toBe(1) // still 1

    // Reconnect — buffered event replays
    const reply2 = makeReply()
    svc.addStream('q4', reply2, 'sess4')
    expect(reply2.written.length).toBe(1)
    expect(reply2.written[0]).toContain('"n":2')
  })

  it('hasActiveStreams returns true when connected, false when not', () => {
    expect(svc.hasActiveStreams('q5', 'sess5')).toBe(false)

    const reply = makeReply()
    svc.addStream('q5', reply, 'sess5')
    expect(svc.hasActiveStreams('q5', 'sess5')).toBe(true)

    svc.removeStream('q5', reply, 'sess5')
    expect(svc.hasActiveStreams('q5', 'sess5')).toBe(false)
  })

  it('closeQuery ends the reply and clears buffers', () => {
    const reply = makeReply()
    svc.addStream('q6', reply, 'sess6')

    svc.closeQuery('q6', 'sess6')

    expect(reply.raw.end).toHaveBeenCalled()
    expect(svc.hasActiveStreams('q6', 'sess6')).toBe(false)

    // Further events should buffer (no stream) but the old buffer is cleared
    svc.sendEvent('q6', 'content_delta', { text: 'after close' }, 'sess6')
    const reply2 = makeReply()
    svc.addStream('q6', reply2, 'sess6')
    expect(reply2.written.length).toBe(1) // only the new event
  })

  it('closeSession cascades to all keys with session prefix', () => {
    const r1 = makeReply()
    const r2 = makeReply()
    svc.addStream('q-a', r1, 'sess7')
    svc.addStream('q-b', r2, 'sess7')

    svc.closeSession('sess7')

    expect(r1.raw.end).toHaveBeenCalled()
    expect(r2.raw.end).toHaveBeenCalled()
    expect(svc.hasActiveStreams('q-a', 'sess7')).toBe(false)
    expect(svc.hasActiveStreams('q-b', 'sess7')).toBe(false)
  })

  it('coalesces tool_input_delta events and flushes on boundary', () => {
    const reply = makeReply()
    svc.addStream('q8', reply, 'sess8')

    // Send three input deltas rapidly — should be coalesced
    svc.sendEvent('q8', 'tool_input_delta', { partialJson: '{"a"' }, 'sess8')
    svc.sendEvent('q8', 'tool_input_delta', { partialJson: ':"b"' }, 'sess8')
    svc.sendEvent('q8', 'tool_input_delta', { partialJson: '}' }, 'sess8')

    // No writes yet — debouncing
    expect(reply.written.length).toBe(0)

    // Boundary event flushes
    svc.sendEvent('q8', 'tool_use_end', { toolCallId: 't1' }, 'sess8')

    // Should have 2 writes: the coalesced delta + the tool_use_end
    expect(reply.written.length).toBe(2)
    // First write is the coalesced delta
    expect(reply.written[0]).toContain('tool_input_delta')
    // The partialJson value is JSON-serialized inside SSE data, so verify via parsing
    const sseData = reply.written[0].split('\ndata: ')[1].trim()
    const parsed = JSON.parse(sseData)
    expect(parsed.data.partialJson).toBe('{"a":"b"}')
    // Second is the boundary event
    expect(reply.written[1]).toContain('tool_use_end')
  })

  it('two sessions with same queryId are independent', () => {
    const replyA = makeReply()
    const replyB = makeReply()
    svc.addStream('shared-q', replyA, 'sessA')
    svc.addStream('shared-q', replyB, 'sessB')

    svc.sendEvent('shared-q', 'content_delta', { text: 'for A' }, 'sessA')

    expect(replyA.written.length).toBe(1)
    expect(replyB.written.length).toBe(0)
  })
})
