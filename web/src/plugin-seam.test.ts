// Proof test for the RenderPlugin seam (AL-6-revise).
//
// Verifies that foldPluginEvents:
//   - calls plugin.reduce() for every matching event
//   - keys state by toolCallId (not plugin type — separate buckets per tool call)
//   - ignores events whose type is not in plugin.eventTypes
//   - handles multiple plugins without cross-contamination

import { describe, it, expect } from 'vitest'
import { foldPluginEvents } from './components/AgentChat.js'
// The fold helper accepts CoreSSEEvent (data: unknown, type: AgentSSEEventType) from
// types.ts — the same type useAgentSession returns. Tests build events as CoreSSEEvent.
import type { AgentSSEEvent as CoreSSEEvent, AgentSSEEventType } from './types.js'
// Plugin contract type used inside plugin.reduce() (data: Record<string,unknown>).
import type { AgentSSEEvent as PluginEvent, RenderPlugin } from './plugins.js'

// Trivial plugin that accumulates title strings from table_rendered events,
// keyed by toolCallId.
// The plugin contract (plugins.ts RenderPlugin) types event.data as
// Record<string,unknown> so plugins can safely access data properties directly.
interface TableState {
  count: number
  titles: string[]
}

const tablePlugin: RenderPlugin<TableState> = {
  eventTypes: ['table_rendered'],
  init(): TableState { return { count: 0, titles: [] } },
  reduce(state: TableState, event: PluginEvent): TableState {
    const title = (event.data?.title as string | undefined) || ''
    return { count: state.count + 1, titles: [...state.titles, title] }
  },
  render(_props) { return null },
}

function makeEvent(
  type: AgentSSEEventType,
  toolCallId: string,
  extra: Record<string, unknown> = {},
): CoreSSEEvent {
  return { type, data: { toolCallId, ...extra } as unknown, timestamp: '2026-01-01T00:00:00Z' }
}

describe('foldPluginEvents', () => {
  it('reduce is called — state for toolCall "A" accumulates two events', () => {
    const events: CoreSSEEvent[] = [
      makeEvent('table_rendered', 'A', { title: 'Alpha' }),
      makeEvent('table_rendered', 'A', { title: 'Beta' }),
      makeEvent('table_rendered', 'B', { title: 'Gamma' }),
    ]

    const result = foldPluginEvents([tablePlugin], events)
    const byTool = result.get(0)!

    expect(byTool).toBeDefined()
    const stateA = byTool.get('A') as TableState
    expect(stateA.count).toBe(2)
    expect(stateA.titles).toEqual(['Alpha', 'Beta'])
  })

  it('buckets are keyed by toolCallId — A and B produce independent states', () => {
    const events: CoreSSEEvent[] = [
      makeEvent('table_rendered', 'A', { title: 'Alpha' }),
      makeEvent('table_rendered', 'B', { title: 'Gamma' }),
      makeEvent('table_rendered', 'B', { title: 'Delta' }),
    ]

    const result = foldPluginEvents([tablePlugin], events)
    const byTool = result.get(0)!

    const stateA = byTool.get('A') as TableState
    const stateB = byTool.get('B') as TableState

    expect(stateA.count).toBe(1)
    expect(stateB.count).toBe(2)
    // They must be distinct objects
    expect(stateA).not.toBe(stateB)
  })

  it('events of unhandled types are ignored', () => {
    const events: CoreSSEEvent[] = [
      makeEvent('table_rendered', 'A', { title: 'Alpha' }),
      makeEvent('chart_rendered', 'A', { title: 'ShouldBeIgnored' }),
      makeEvent('session_title', 'A', {}),
    ]

    const result = foldPluginEvents([tablePlugin], events)
    const byTool = result.get(0)!

    const stateA = byTool.get('A') as TableState
    // Only the table_rendered event should have been reduced
    expect(stateA.count).toBe(1)
    expect(stateA.titles).toEqual(['Alpha'])
  })

  it('two plugins sharing an event type maintain independent state (no collision)', () => {
    // Second plugin also handles table_rendered but tracks something different.
    interface CountState { n: number }
    const counterPlugin: RenderPlugin<CountState> = {
      eventTypes: ['table_rendered'],
      init(): CountState { return { n: 0 } },
      reduce(state: CountState): CountState { return { n: state.n + 10 } },
      render(_props) { return null },
    }

    const events: CoreSSEEvent[] = [
      makeEvent('table_rendered', 'A', { title: 'T1' }),
      makeEvent('table_rendered', 'A', { title: 'T2' }),
    ]

    const result = foldPluginEvents([tablePlugin, counterPlugin], events)

    const byTool0 = result.get(0)!
    const byTool1 = result.get(1)!

    const s0 = byTool0.get('A') as TableState
    const s1 = byTool1.get('A') as CountState

    // Plugin 0 (tablePlugin) accumulated titles
    expect(s0.count).toBe(2)
    expect(s0.titles).toEqual(['T1', 'T2'])

    // Plugin 1 (counterPlugin) accumulated by 10 per event
    expect(s1.n).toBe(20)
  })

  it('empty pluginEvents returns empty byTool maps', () => {
    const result = foldPluginEvents([tablePlugin], [])
    const byTool = result.get(0)!
    expect(byTool.size).toBe(0)
  })

  it('empty plugins returns empty result map', () => {
    const events: CoreSSEEvent[] = [makeEvent('table_rendered', 'A', {})]
    const result = foldPluginEvents([], events)
    expect(result.size).toBe(0)
  })

  it('toolCallId falls back to tool_use_id field when toolCallId is absent', () => {
    const ev: CoreSSEEvent = {
      type: 'table_rendered',
      data: { tool_use_id: 'fallback-id', title: 'FallbackTitle' } as unknown,
      timestamp: '2026-01-01T00:00:00Z',
    }

    const result = foldPluginEvents([tablePlugin], [ev])
    const byTool = result.get(0)!
    const state = byTool.get('fallback-id') as TableState
    expect(state).toBeDefined()
    expect(state.count).toBe(1)
    expect(state.titles).toEqual(['FallbackTitle'])
  })
})
