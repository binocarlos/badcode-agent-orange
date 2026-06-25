const MOCK_URL = process.env.MOCK_URL || 'http://localhost:4010'

export interface MockScript {
  id: string
  description: string
  turns: MockTurn[]
  model?: string
  sessionId?: string
}

export interface MockTurn {
  blocks: MockContentBlock[]
  streamDelayMs?: number
}

export type MockContentBlock =
  | { type: 'text'; text: string }
  | { type: 'thinking'; thinking: string }
  | { type: 'tool_use'; name: string; input: Record<string, unknown>; id?: string }

export async function loadMockScript(script: MockScript, sessionId?: string): Promise<void> {
  const body = sessionId ? { ...script, sessionId } : script
  const res = await fetch(`${MOCK_URL}/mock/load-script`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) throw new Error(`loadMockScript failed (${res.status}): ${await res.text()}`)
}

export async function resetMock(sessionId?: string): Promise<void> {
  const res = await fetch(`${MOCK_URL}/mock/reset`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(sessionId ? { sessionId } : {}),
  })
  if (!res.ok) throw new Error(`resetMock failed (${res.status}): ${await res.text()}`)
}

export async function getMockStatus(): Promise<{
  active: boolean
  scriptId?: string
  requestCount: number
  sessions?: string[]
}> {
  const res = await fetch(`${MOCK_URL}/mock/status`)
  if (!res.ok) throw new Error(`getMockStatus failed (${res.status})`)
  return res.json()
}
