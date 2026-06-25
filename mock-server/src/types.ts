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

export interface SessionState {
  script: MockScript
  requestCount: number
  toolIdCounter: number
  lastRequestTime: number
}
