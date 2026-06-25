import * as http from 'node:http'
import type { MockScript, SessionState } from './types.js'
import { countAssistantMessages, streamSSEResponse } from './streaming.js'

const AUTO_EXPIRY_MS = 5 * 60 * 1000
const GLOBAL_KEY = '__global__'
const PORT = parseInt(process.env.MOCK_SERVER_PORT || '4010', 10)

const sessions = new Map<string, SessionState>()
const expiryTimers = new Map<string, ReturnType<typeof setTimeout>>()

function clearSession(key: string): void {
  const timer = expiryTimers.get(key)
  if (timer) {
    clearTimeout(timer)
    expiryTimers.delete(key)
  }
  sessions.delete(key)
}

function startExpiryTimer(key: string): void {
  const existing = expiryTimers.get(key)
  if (existing) clearTimeout(existing)
  const timer = setTimeout(() => {
    console.log(`[mock] Session ${key} auto-expired`)
    clearSession(key)
  }, AUTO_EXPIRY_MS)
  expiryTimers.set(key, timer)
}

function resolveKey(sessionId?: string): string | null {
  if (sessionId && sessions.has(sessionId)) return sessionId
  if (sessions.has(GLOBAL_KEY)) return GLOBAL_KEY
  return null
}

function readBody(req: http.IncomingMessage): Promise<unknown> {
  return new Promise((resolve, reject) => {
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => chunks.push(chunk))
    req.on('end', () => {
      const raw = Buffer.concat(chunks).toString('utf-8')
      if (!raw) { resolve({}); return }
      try { resolve(JSON.parse(raw)) }
      catch { reject(new Error(`Invalid JSON: ${raw}`)) }
    })
    req.on('error', reject)
  })
}

function setCORSHeaders(res: http.ServerResponse): void {
  res.setHeader('Access-Control-Allow-Origin', '*')
  res.setHeader('Access-Control-Allow-Methods', 'GET, POST, OPTIONS')
  res.setHeader('Access-Control-Allow-Headers', 'Content-Type, x-api-key, x-session-id, anthropic-version, Authorization')
}

function jsonResponse(res: http.ServerResponse, statusCode: number, body: unknown): void {
  res.writeHead(statusCode, { 'Content-Type': 'application/json' })
  res.end(JSON.stringify(body))
}

const server = http.createServer(async (req, res) => {
  setCORSHeaders(res)
  if (req.method === 'OPTIONS') { res.writeHead(204); res.end(); return }

  const url = new URL(req.url || '/', `http://localhost:${PORT}`)

  try {
    if (req.method === 'GET' && url.pathname === '/health') {
      jsonResponse(res, 200, { status: 'ok' })
      return
    }

    if (req.method === 'GET' && url.pathname === '/mock/status') {
      const sessionId = url.searchParams.get('sessionId') || undefined
      const key = sessionId ? resolveKey(sessionId) : GLOBAL_KEY
      const state = key ? sessions.get(key) : undefined
      jsonResponse(res, 200, {
        active: state !== undefined,
        scriptId: state?.script.id,
        requestCount: state?.requestCount ?? 0,
        sessions: [...sessions.keys()].filter((k) => k !== GLOBAL_KEY),
      })
      return
    }

    if (req.method === 'POST' && url.pathname === '/mock/reset') {
      const body = (await readBody(req)) as { sessionId?: string }
      if (body.sessionId) {
        clearSession(body.sessionId)
      } else {
        for (const key of [...sessions.keys()]) clearSession(key)
      }
      jsonResponse(res, 200, { ok: true })
      return
    }

    if (req.method === 'POST' && url.pathname === '/mock/load-script') {
      const script = (await readBody(req)) as MockScript
      const key = script.sessionId || GLOBAL_KEY
      clearSession(key)
      sessions.set(key, { script, requestCount: 0, toolIdCounter: 0, lastRequestTime: Date.now() })
      startExpiryTimer(key)
      console.log(`[mock] Loaded script "${script.id}" for "${key}" (${script.turns.length} turns)`)
      jsonResponse(res, 200, { ok: true })
      return
    }

    if (req.method === 'POST' && url.pathname === '/v1/messages') {
      const sessionId = req.headers['x-session-id'] as string | undefined
      const key = resolveKey(sessionId)

      if (!key) {
        jsonResponse(res, 500, {
          type: 'error',
          error: { type: 'invalid_request_error', message: 'No mock script loaded' },
        })
        return
      }

      const body = await readBody(req)
      const state = sessions.get(key)!
      state.lastRequestTime = Date.now()
      state.requestCount++
      startExpiryTimer(key)

      const turnIndex = countAssistantMessages(body)
      const turn = state.script.turns[turnIndex]

      const blocks = turn?.blocks ?? [{ type: 'text' as const, text: 'Mock script exhausted' }]
      const stopReason = blocks.some((b) => b.type === 'tool_use') ? 'tool_use' : 'end_turn'

      const model = state.script.model || 'claude-opus-4-5'
      const delayMs = turn?.streamDelayMs ?? 10

      res.writeHead(200, {
        'Content-Type': 'text/event-stream',
        'Cache-Control': 'no-cache',
        'X-Accel-Buffering': 'no',
        Connection: 'keep-alive',
      })

      await streamSSEResponse(res, blocks, stopReason, model, delayMs, state)
      res.end()
      return
    }

    jsonResponse(res, 404, { error: `Not found: ${req.method} ${url.pathname}` })
  } catch (err) {
    console.error('[mock] Error:', err)
    if (!res.headersSent) jsonResponse(res, 500, { error: String(err) })
    else res.end()
  }
})

server.listen(PORT, '0.0.0.0', () => {
  console.log(`[mock] Agent mock server listening on 0.0.0.0:${PORT}`)
})

process.on('SIGTERM', () => { server.close(() => process.exit(0)) })
process.on('SIGINT', () => { server.close(() => process.exit(0)) })
