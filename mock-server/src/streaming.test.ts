import { describe, it, expect, vi } from 'vitest'
import { chunkText, countAssistantMessages, sseEvent, delay } from './streaming.js'
import * as http from 'node:http'

// --- chunkText ---

describe('chunkText', () => {
  it('splits text into chunks of the given size', () => {
    expect(chunkText('abcdef', 2)).toEqual(['ab', 'cd', 'ef'])
  })

  it('returns [""] for empty string', () => {
    expect(chunkText('', 10)).toEqual([''])
  })

  it('returns single-element array when text is shorter than size', () => {
    expect(chunkText('hi', 100)).toEqual(['hi'])
  })

  it('handles exact multiples without trailing empty chunk', () => {
    const result = chunkText('abcd', 2)
    expect(result).toEqual(['ab', 'cd'])
    expect(result.length).toBe(2)
  })
})

// --- countAssistantMessages ---

describe('countAssistantMessages', () => {
  it('counts only assistant messages in a mixed array', () => {
    const body = {
      messages: [
        { role: 'user', content: 'hi' },
        { role: 'assistant', content: 'hello' },
        { role: 'user', content: 'more' },
        { role: 'assistant', content: 'response' },
      ],
    }
    expect(countAssistantMessages(body)).toBe(2)
  })

  it('returns 0 for null input', () => {
    expect(countAssistantMessages(null)).toBe(0)
  })

  it('returns 0 for undefined input', () => {
    expect(countAssistantMessages(undefined)).toBe(0)
  })

  it('returns 0 when messages array is missing', () => {
    expect(countAssistantMessages({ other: 'data' })).toBe(0)
  })

  it('returns 0 for empty messages array', () => {
    expect(countAssistantMessages({ messages: [] })).toBe(0)
  })

  it('returns 0 when no assistant messages exist', () => {
    const body = { messages: [{ role: 'user', content: 'hi' }] }
    expect(countAssistantMessages(body)).toBe(0)
  })
})

// --- sseEvent ---

describe('sseEvent', () => {
  it('writes event in SSE format: event: <type>\\ndata: <json>\\n\\n', () => {
    const chunks: string[] = []
    const mockRes = {
      write: (data: string) => { chunks.push(data) },
    } as unknown as http.ServerResponse

    sseEvent(mockRes, 'content_delta', { text: 'hello' })

    expect(chunks.length).toBe(1)
    expect(chunks[0]).toBe('event: content_delta\ndata: {"text":"hello"}\n\n')
  })
})

// --- delay ---

describe('delay', () => {
  it('resolves immediately for zero ms', async () => {
    const start = Date.now()
    await delay(0)
    const elapsed = Date.now() - start
    expect(elapsed).toBeLessThan(50)
  })

  it('resolves immediately for negative ms', async () => {
    const start = Date.now()
    await delay(-10)
    const elapsed = Date.now() - start
    expect(elapsed).toBeLessThan(50)
  })
})
