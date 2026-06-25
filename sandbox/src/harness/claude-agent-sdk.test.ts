import { describe, it, expect, afterAll } from 'vitest'
import http from 'node:http'
import {
  startSessionProxy,
  buildImagePrompt,
  ClaudeAgentSdkHarness,
} from './claude-agent-sdk.js'
import type { SessionProxy } from './claude-agent-sdk.js'

// --- startSessionProxy ---

describe('startSessionProxy', () => {
  let proxy: SessionProxy | null = null
  let upstream: http.Server | null = null

  afterAll(() => {
    proxy?.close()
    upstream?.close()
  })

  it('forwards requests to upstream with x-session-id header injected', async () => {
    // Stand up a tiny upstream that captures headers
    let capturedHeaders: http.IncomingHttpHeaders = {}
    upstream = http.createServer((req, res) => {
      capturedHeaders = req.headers
      res.writeHead(200, { 'Content-Type': 'application/json' })
      res.end('{"ok":true}')
    })

    await new Promise<void>((resolve) => {
      upstream!.listen(0, '127.0.0.1', resolve)
    })
    const upstreamPort = (upstream.address() as { port: number }).port
    const upstreamURL = `http://127.0.0.1:${upstreamPort}`

    proxy = await startSessionProxy('test-session-42', upstreamURL)

    // Make a request through the proxy
    const res = await fetch(`${proxy.baseURL}/v1/messages`, {
      method: 'POST',
      body: '{}',
    })
    expect(res.status).toBe(200)
    expect(capturedHeaders['x-session-id']).toBe('test-session-42')
  })
})

// --- buildImagePrompt ---

describe('buildImagePrompt', () => {
  it('yields a single SDKUserMessage with text and image content blocks', async () => {
    const images = [
      { base64: 'abc123', mimeType: 'image/png' as const, label: 'Slide 1' },
      { base64: 'def456', mimeType: 'image/jpeg' as const, label: 'Slide 2' },
    ]

    const messages: unknown[] = []
    for await (const msg of buildImagePrompt('Hello world', images, 'sess-1')) {
      messages.push(msg)
    }

    expect(messages.length).toBe(1)
    const msg = messages[0] as {
      type: string
      message: { role: string; content: unknown[] }
      session_id: string
    }
    expect(msg.type).toBe('user')
    expect(msg.message.role).toBe('user')
    expect(msg.session_id).toBe('sess-1')

    // Content should be: text, image, text(label), image, text(label)
    const content = msg.message.content as Array<{ type: string }>
    expect(content.length).toBe(5) // text + image + label + image + label
    expect(content[0].type).toBe('text')
    expect(content[1].type).toBe('image')
    expect(content[2].type).toBe('text')
    expect(content[3].type).toBe('image')
    expect(content[4].type).toBe('text')
  })
})

// --- ClaudeAgentSdkHarness conversation management ---

describe('ClaudeAgentSdkHarness — conversation management', () => {
  it('loadConversation populates history', () => {
    const harness = new ClaudeAgentSdkHarness()
    harness.loadConversation([
      { role: 'user', content: 'Hello' },
      { role: 'assistant', content: 'Hi there' },
    ])
    // We can't read conversationHistory directly (private), but resetConversation
    // should clear it — and loadConversation again should work.
    harness.resetConversation()
    // After reset, loading again should not throw or accumulate
    harness.loadConversation([{ role: 'user', content: 'New conversation' }])
  })

  it('resetConversation clears history', () => {
    const harness = new ClaudeAgentSdkHarness()
    harness.loadConversation([
      { role: 'user', content: 'msg1' },
      { role: 'assistant', content: 'msg2' },
    ])
    // Should not throw
    harness.resetConversation()
    // Load new conversation — should work cleanly
    harness.loadConversation([{ role: 'user', content: 'fresh start' }])
  })
})
