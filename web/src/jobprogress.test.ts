// W6 / doc 21 §5-M6: the long-wait affordance is step count + last-step label
// + elapsed, derived from a request the job table already makes. These tests
// pin the derivation (which envelopes count as a step, which do not), the
// threshold, and the two honesty rules — never a percentage, never "Done".

import { describe, it, expect } from 'vitest'
import {
  EMPTY_JOB_PROGRESS,
  LONG_JOB_AFTER_SECONDS,
  PROGRESS_REFRESH_MS,
  formatProduced,
  formatStepLine,
  isLongRunningJob,
  progressAriaLabel,
  progressRefreshKey,
  summariseJobProgress,
} from './jobprogress.js'

/** The real `GET /agent/session/{id}/query-events` envelope, same wrapping row
 *  shape as the captured token fixture in events.test.ts. */
const row = (queryId: string, events: unknown[]) => ({
  id: `row-${queryId}`,
  session_id: '1a4f6f27-4a6e-4f27-a27d-1811fe93c078',
  query_id: queryId,
  created_at: 1785022143,
  search_text: '',
  events,
})

const toolStart = (id: string, toolName: string, input: Record<string, unknown> = {}) => ({
  type: 'tool_use_start',
  timestamp: '2026-07-25T23:29:03.604Z',
  data: { toolCallId: id, toolName, input },
})

const toolEnd = (id: string) => ({
  type: 'tool_use_end',
  timestamp: '2026-07-25T23:29:05.604Z',
  data: { toolCallId: id, output: 'ok' },
})

describe('summariseJobProgress', () => {
  it('counts tool calls as steps and names the newest one', () => {
    const payload = {
      events: [
        row('q-1', [
          { type: 'user_message', data: { content: 'Event: schedule.fired' } },
          toolStart('t1', 'read', { file_path: '/workspace/notes.md' }),
          toolEnd('t1'),
          toolStart('t2', 'bash', { command: 'ls' }),
          toolEnd('t2'),
        ]),
        row('q-2', [toolStart('t3', 'text_editor', { command: 'str_replace' })]),
      ],
    }
    const p = summariseJobProgress(payload)
    expect(p.steps).toBe(3)
    expect(p.lastStep).toBe('Edit File')
    // t3 has no end envelope: the session is still inside that step.
    expect(p.active).toBe(true)
  })

  it('reads the legacy flat envelope list too', () => {
    const p = summariseJobProgress([toolStart('t1', 'bash', { command: 'ls' }), toolEnd('t1')])
    expect(p.steps).toBe(1)
    expect(p.active).toBe(false)
  })

  it('does not count a `type` key buried in a tool input as a step', () => {
    // The regression this guards: walking into an envelope's `data` turns a
    // JSON-schema fragment into a phantom step, and the count silently inflates.
    const payload = [
      toolStart('t1', 'bash', { command: 'ls', schema: { type: 'tool_use_start' } }),
    ]
    expect(summariseJobProgress(payload).steps).toBe(1)
  })

  it('is empty for a session that has emitted nothing', () => {
    expect(summariseJobProgress({ events: [] })).toEqual(EMPTY_JOB_PROGRESS)
    expect(summariseJobProgress(null)).toEqual(EMPTY_JOB_PROGRESS)
    expect(summariseJobProgress('nonsense')).toEqual(EMPTY_JOB_PROGRESS)
  })

  it('collects what was produced, deduped and in order', () => {
    const payload = [
      { type: 'artifact_registered', data: { filePath: '/out/report.pdf', label: 'Weekly report' } },
      { type: 'artifact_registered', data: { filePath: '/out/report.pdf', label: 'Weekly report' } },
      { type: 'dashboard_created', data: { title: 'Inbox health' } },
      { type: 'artifact_registered', data: { filePath: '/out/raw.csv', label: '' } },
    ]
    expect(summariseJobProgress(payload).produced).toEqual([
      'Weekly report',
      'Inbox health',
      'raw.csv',
    ])
  })
})

describe('the long-job threshold', () => {
  it('applies only to work that is genuinely still going, and only past 10s', () => {
    expect(isLongRunningJob('running', LONG_JOB_AFTER_SECONDS)).toBe(true)
    expect(isLongRunningJob('running', LONG_JOB_AFTER_SECONDS - 1)).toBe(false)
    // An ask is the longest wait on the console — "12 steps, then it asked you
    // something" is exactly what decides whether to read it now.
    expect(isLongRunningJob('awaiting_human', 4000)).toBe(true)
    expect(isLongRunningJob('ok', 4000)).toBe(false)
    expect(isLongRunningJob('failed', 4000)).toBe(false)
    expect(isLongRunningJob('pending', 4000)).toBe(false)
    expect(isLongRunningJob('running', null)).toBe(false)
  })
})

describe('the lines an operator reads', () => {
  it('says "step N", never "N of M" and never a percentage', () => {
    const p = { ...EMPTY_JOB_PROGRESS, steps: 7, lastStep: 'Edit File' }
    expect(formatStepLine(p)).toBe('step 7 · Edit File')
    expect(formatStepLine(p)).not.toMatch(/%|of \d/)
  })

  it('says "starting up" rather than "step 0"', () => {
    expect(formatStepLine(EMPTY_JOB_PROGRESS)).toBe('starting up')
    expect(progressAriaLabel(EMPTY_JOB_PROGRESS)).toBe('starting up')
  })

  it('gives a screen reader a sentence, not the digits', () => {
    expect(progressAriaLabel({ ...EMPTY_JOB_PROGRESS, steps: 1, lastStep: 'Run Command' })).toBe(
      '1 step so far, most recently Run Command',
    )
    expect(progressAriaLabel({ ...EMPTY_JOB_PROGRESS, steps: 4, lastStep: '' })).toBe(
      '4 steps so far',
    )
  })

  it('names what was produced, and stays silent rather than saying "Done"', () => {
    const made = (produced: string[]) => formatProduced({ ...EMPTY_JOB_PROGRESS, produced })
    expect(made([])).toBe('')
    expect(made(['Weekly report'])).toBe('Weekly report')
    expect(made(['a', 'b'])).toBe('a and b')
    expect(made(['a', 'b', 'c', 'd'])).toBe('a, b and 2 more')
  })
})

describe('progressRefreshKey', () => {
  it('changes once per refresh window while a job is open', () => {
    const base = PROGRESS_REFRESH_MS * 40
    const a = progressRefreshKey('running', base)
    expect(a).not.toBeNull()
    expect(progressRefreshKey('running', base + PROGRESS_REFRESH_MS - 1)).toBe(a)
    expect(progressRefreshKey('running', base + PROGRESS_REFRESH_MS)).toBe(a! + 1)
  })

  it('is null for anything that must never re-fetch', () => {
    // A finished job's steps cannot change; re-reading them is a request that
    // buys nothing, once per row per tick.
    expect(progressRefreshKey('ok', 1_000_000)).toBeNull()
    expect(progressRefreshKey('failed', 1_000_000)).toBeNull()
    // No shared clock: the surface is not ticking, so nothing re-reads either.
    expect(progressRefreshKey('running', undefined)).toBeNull()
  })
})
