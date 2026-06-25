import * as http from 'node:http'
import type { MockContentBlock, SessionState } from './types.js'

export function chunkText(text: string, size: number): string[] {
  const chunks: string[] = []
  for (let i = 0; i < text.length; i += size) {
    chunks.push(text.slice(i, i + size))
  }
  if (chunks.length === 0) chunks.push('')
  return chunks
}

export function sseEvent(res: http.ServerResponse, event: string, data: unknown): void {
  res.write(`event: ${event}\ndata: ${JSON.stringify(data)}\n\n`)
}

export function delay(ms: number): Promise<void> {
  if (ms <= 0) return Promise.resolve()
  return new Promise((resolve) => setTimeout(resolve, ms))
}

export function countAssistantMessages(body: unknown): number {
  if (!body || typeof body !== 'object') return 0
  const messages = (body as Record<string, unknown>).messages
  if (!Array.isArray(messages)) return 0
  return messages.filter((m: unknown) => {
    if (!m || typeof m !== 'object') return false
    return (m as Record<string, unknown>).role === 'assistant'
  }).length
}

export async function streamSSEResponse(
  res: http.ServerResponse,
  blocks: MockContentBlock[],
  stopReason: string,
  model: string,
  delayMs: number,
  state: SessionState,
): Promise<void> {
  const messageId = `msg_mock_${String(state.requestCount).padStart(3, '0')}`

  sseEvent(res, 'message_start', {
    type: 'message_start',
    message: {
      id: messageId, type: 'message', role: 'assistant', content: [], model,
      stop_reason: null, stop_sequence: null,
      usage: { input_tokens: 100, output_tokens: 0 },
    },
  })
  await delay(delayMs)

  sseEvent(res, 'ping', { type: 'ping' })
  await delay(delayMs)

  let blockIndex = 0
  for (const block of blocks) {
    if (block.type === 'text') {
      sseEvent(res, 'content_block_start', {
        type: 'content_block_start', index: blockIndex,
        content_block: { type: 'text', text: '' },
      })
      await delay(delayMs)
      for (const chunk of chunkText(block.text, 20)) {
        sseEvent(res, 'content_block_delta', {
          type: 'content_block_delta', index: blockIndex,
          delta: { type: 'text_delta', text: chunk },
        })
        await delay(delayMs)
      }
      sseEvent(res, 'content_block_stop', { type: 'content_block_stop', index: blockIndex })
      await delay(delayMs)
    } else if (block.type === 'thinking') {
      sseEvent(res, 'content_block_start', {
        type: 'content_block_start', index: blockIndex,
        content_block: { type: 'thinking', thinking: '' },
      })
      await delay(delayMs)
      for (const chunk of chunkText(block.thinking, 20)) {
        sseEvent(res, 'content_block_delta', {
          type: 'content_block_delta', index: blockIndex,
          delta: { type: 'thinking_delta', thinking: chunk },
        })
        await delay(delayMs)
      }
      sseEvent(res, 'content_block_stop', { type: 'content_block_stop', index: blockIndex })
      await delay(delayMs)
    } else if (block.type === 'tool_use') {
      const toolId = block.id || `toolu_mock_${++state.toolIdCounter}`
      sseEvent(res, 'content_block_start', {
        type: 'content_block_start', index: blockIndex,
        content_block: { type: 'tool_use', id: toolId, name: block.name, input: {} },
      })
      await delay(delayMs)
      const inputJson = JSON.stringify(block.input)
      for (const chunk of chunkText(inputJson, 40)) {
        sseEvent(res, 'content_block_delta', {
          type: 'content_block_delta', index: blockIndex,
          delta: { type: 'input_json_delta', partial_json: chunk },
        })
        await delay(delayMs)
      }
      sseEvent(res, 'content_block_stop', { type: 'content_block_stop', index: blockIndex })
      await delay(delayMs)
    }
    blockIndex++
  }

  sseEvent(res, 'message_delta', {
    type: 'message_delta',
    delta: { stop_reason: stopReason, stop_sequence: null },
    usage: { output_tokens: 50 },
  })
  await delay(delayMs)
  sseEvent(res, 'message_stop', { type: 'message_stop' })
}
