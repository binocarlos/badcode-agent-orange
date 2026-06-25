import type { MockScript, MockTurn, MockContentBlock } from './mock-client.js'

export class MockScriptBuilder {
  private id: string
  private description: string
  private model?: string
  private turns: MockTurn[] = []
  private currentBlocks: MockContentBlock[] = []
  private currentStreamDelay?: number

  constructor(id: string, description?: string) {
    this.id = id
    this.description = description || id
  }

  setModel(model: string): this { this.model = model; return this }
  setStreamDelay(ms: number): this { this.currentStreamDelay = ms; return this }

  addText(text: string): this {
    this.currentBlocks.push({ type: 'text', text })
    return this
  }

  addThinking(thinking: string): this {
    this.currentBlocks.push({ type: 'thinking', thinking })
    return this
  }

  addBashTool(command: string, description?: string): this {
    this.currentBlocks.push({
      type: 'tool_use',
      name: 'Bash',
      input: { command, description: description || 'Running command' },
    })
    return this
  }

  addMcpTool(server: string, tool: string, input: Record<string, unknown>): this {
    this.currentBlocks.push({ type: 'tool_use', name: `mcp__${server}__${tool}`, input })
    return this
  }

  addToolUse(name: string, input: Record<string, unknown>, id?: string): this {
    this.currentBlocks.push({ type: 'tool_use', name, input, id })
    return this
  }

  nextTurn(): this {
    if (this.currentBlocks.length > 0) {
      const turn: MockTurn = { blocks: [...this.currentBlocks] }
      if (this.currentStreamDelay !== undefined) turn.streamDelayMs = this.currentStreamDelay
      this.turns.push(turn)
      this.currentBlocks = []
      this.currentStreamDelay = undefined
    }
    return this
  }

  build(): MockScript {
    if (this.currentBlocks.length > 0) this.nextTurn()
    return { id: this.id, description: this.description, turns: this.turns, model: this.model }
  }
}

export function createTextOnlyScript(text: string): MockScript {
  return { id: 'text-only', description: 'Single text response', turns: [{ blocks: [{ type: 'text', text }] }] }
}

export function createBashScript(command: string, responseText?: string): MockScript {
  return new MockScriptBuilder('bash-tool', `Bash: ${command}`)
    .addText('Let me run that command.')
    .addBashTool(command)
    .nextTurn()
    .addText(responseText || 'The command executed successfully.')
    .build()
}

export function createThinkingScript(thinking: string, text: string): MockScript {
  return { id: 'thinking', description: 'Thinking then text', turns: [{ blocks: [{ type: 'thinking', thinking }, { type: 'text', text }] }] }
}

export function createErrorRecoveryScript(command: string, errorMessage: string, successText: string): MockScript {
  return new MockScriptBuilder('error-recovery', `Error recovery: ${command}`)
    .addText('Let me try running that command.')
    .addBashTool(command, 'First attempt')
    .nextTurn()
    .addText(`I encountered an error: "${errorMessage}". Let me try a different approach.`)
    .addBashTool(command, 'Retry attempt')
    .nextTurn()
    .addText(successText)
    .build()
}
