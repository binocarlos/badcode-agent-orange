// F1/J4: the changelog's pure logic — entity keying, read-time diffs between
// consecutive events for the same key, and the §15.9-shaped filter.

import { describe, it, expect } from 'vitest'
import {
  actionMatches,
  buildChangelog,
  changelogQueryParams,
  changelogTitle,
  coerceConfigEvent,
  configEntity,
  configPromptText,
  CONFIG_ACTIONS,
  describeConfigAction,
  diffLines,
  extractConfigEvents,
  filterChangelog,
  formatConfigTimestamp,
  type ConfigEvent,
} from './configLog.js'

const ev = (over: Partial<ConfigEvent> = {}): ConfigEvent =>
  coerceConfigEvent({
    id: 'c1',
    project: 'acme',
    actor_worker: '',
    actor_session: '',
    action: 'worker_update',
    payload: { name: 'email-answerer' },
    rationale: '',
    created_at: 1_789_000_000_000,
    ...over,
  })

describe('the §15.3 vocabulary', () => {
  it('carries all eighteen actions, including the freeze toggle F1 gained', () => {
    expect(CONFIG_ACTIONS).toHaveLength(18)
    expect(CONFIG_ACTIONS).toContain('worker_delete')
    expect(CONFIG_ACTIONS).toContain('worker_prompt_write')
    expect(CONFIG_ACTIONS).toContain('worker_freeze')
    expect(CONFIG_ACTIONS).toContain('worker_unfreeze')
    expect(CONFIG_ACTIONS).toContain('image_create')
  })

  it('renders freeze and unfreeze as their own verbs, keyed to the worker', () => {
    expect(describeConfigAction('worker_freeze')).toBe('Froze worker')
    expect(describeConfigAction('worker_unfreeze')).toBe('Unfroze worker')
    expect(changelogTitle(ev({ action: 'worker_freeze', payload: { name: 'quality-scorer' } }))).toBe(
      'Froze worker “quality-scorer”',
    )
    expect(configEntity(ev({ action: 'worker_unfreeze' })).key).toBe('worker:email-answerer')
  })

  it('a freeze entry carries the full row but no prompt diff — the prompt did not change', () => {
    const entries = buildChangelog([
      ev({
        id: 'c1',
        action: 'worker_create',
        payload: { name: 'w', system_prompt: 'P' },
        created_at: 1,
      }),
      ev({
        id: 'c2',
        action: 'worker_freeze',
        payload: { name: 'w', system_prompt: 'P', frozen: true },
        created_at: 2,
      }),
    ])
    expect(entries[0]!.action).toBe('worker_freeze')
    expect(entries[0]!.title).toBe('Froze worker “w”')
    expect(entries[0]!.diff).toBeNull()
  })

  it('gives every action a human verb, and passes an unknown one through', () => {
    for (const action of CONFIG_ACTIONS) {
      expect(describeConfigAction(action)).not.toBe(action)
    }
    expect(describeConfigAction('what_is_this')).toBe('what_is_this')
  })
})

describe('configEntity — what the diff groups by', () => {
  it('keys workers by name, whichever worker verb it was', () => {
    for (const action of ['worker_create', 'worker_disable', 'worker_prompt_write']) {
      expect(configEntity(ev({ action })).key).toBe('worker:email-answerer')
    }
  })

  it('keys the two project singletons without a name', () => {
    expect(configEntity(ev({ action: 'project_settings_put', payload: {} })).key).toBe(
      'project-settings',
    )
    expect(configEntity(ev({ action: 'project_prompt_write', payload: {} })).key).toBe(
      'project-prompt',
    )
  })

  it('keys subscriptions and schedules by id, images by name:version', () => {
    expect(configEntity(ev({ action: 'subscription_delete', payload: { id: 'sub-7' } })).key).toBe(
      'subscription:sub-7',
    )
    expect(configEntity(ev({ action: 'schedule_update', payload: { id: 'sch-1' } })).key).toBe(
      'schedule:sch-1',
    )
    expect(
      configEntity(ev({ action: 'image_create', payload: { name: 'marketing', version: 3 } })).key,
    ).toBe('image:marketing:3')
  })

  it('titles an entry as a human would say it', () => {
    expect(changelogTitle(ev({ action: 'worker_create' }))).toBe('Hired worker “email-answerer”')
    expect(changelogTitle(ev({ action: 'project_settings_put', payload: {} }))).toBe(
      'Changed project settings',
    )
  })
})

describe('configPromptText', () => {
  it('finds the prompt on a worker row', () => {
    expect(configPromptText(ev({ payload: { name: 'w', system_prompt: 'be brief' } }))).toBe(
      'be brief',
    )
  })

  it('is null when the payload carries no prompt', () => {
    expect(configPromptText(ev({ payload: { name: 'w' } }))).toBeNull()
  })
})

describe('diffLines', () => {
  it('marks added, removed and unchanged lines', () => {
    const d = diffLines('a\nb\nc', 'a\nB\nc')
    expect(d).toEqual([
      { type: 'ctx', text: 'a' },
      { type: 'del', text: 'b' },
      { type: 'add', text: 'B' },
      { type: 'ctx', text: 'c' },
    ])
  })

  it('handles pure insertion and pure deletion', () => {
    expect(diffLines('', 'x')).toEqual([{ type: 'add', text: 'x' }])
    expect(diffLines('x', '')).toEqual([{ type: 'del', text: 'x' }])
  })

  it('is empty for identical text', () => {
    expect(diffLines('same', 'same').every((l) => l.type === 'ctx')).toBe(true)
  })
})

describe('buildChangelog', () => {
  it('returns entries newest first whatever order it was handed', () => {
    const entries = buildChangelog([
      ev({ id: 'old', created_at: 100 }),
      ev({ id: 'new', created_at: 300 }),
      ev({ id: 'mid', created_at: 200 }),
    ])
    expect(entries.map((e) => e.id)).toEqual(['new', 'mid', 'old'])
  })

  it('diffs a prompt rewrite against the previous state of the SAME key', () => {
    const entries = buildChangelog([
      ev({
        id: 'a',
        created_at: 100,
        action: 'worker_prompt_write',
        payload: { name: 'email-answerer', system_prompt: 'be thorough' },
      }),
      // A different worker in between must not become the diff's baseline.
      ev({
        id: 'b',
        created_at: 200,
        action: 'worker_prompt_write',
        payload: { name: 'copy-editor', system_prompt: 'be pedantic' },
      }),
      ev({
        id: 'c',
        created_at: 300,
        action: 'worker_prompt_write',
        payload: { name: 'email-answerer', system_prompt: 'be brief' },
        rationale: 'customers complained the replies were long',
      }),
    ])
    const latest = entries[0]!
    expect(latest.id).toBe('c')
    expect(latest.diff).not.toBeNull()
    expect(latest.diff!.previousEventId).toBe('a')
    expect(latest.diff!.lines).toEqual([
      { type: 'del', text: 'be thorough' },
      { type: 'add', text: 'be brief' },
    ])
    expect(latest.diff!.added).toBe(1)
    expect(latest.diff!.removed).toBe(1)
    expect(latest.rationale).toMatch(/customers complained/)
  })

  it('gives the first record for a key no diff — there is nothing to diff against', () => {
    const entries = buildChangelog([
      ev({ id: 'a', action: 'worker_create', payload: { name: 'w', system_prompt: 'hello' } }),
    ])
    expect(entries[0]!.diff).toBeNull()
  })

  it('gives no diff when the prompt did not actually change', () => {
    const entries = buildChangelog([
      ev({ id: 'a', created_at: 1, payload: { name: 'w', system_prompt: 'same' } }),
      ev({ id: 'b', created_at: 2, payload: { name: 'w', system_prompt: 'same' } }),
    ])
    expect(entries[0]!.diff).toBeNull()
  })

  it('deep-links to the acting session, and leaves human edits unlinked', () => {
    const [byWorker, byHuman] = buildChangelog(
      [
        ev({ id: 'w', created_at: 2, actor_worker: 'tuner', actor_session: 'sess-9' }),
        ev({ id: 'h', created_at: 1 }),
      ],
      { projectId: 'acme' },
    )
    expect(byWorker!.sessionPath).toBe('/p/acme/s/sess-9')
    expect(byWorker!.actorWorker).toBe('tuner')
    expect(byHuman!.sessionPath).toBeNull()
  })

  it('prefers a server-supplied session_url over the locally built path', () => {
    const [entry] = buildChangelog(
      [ev({ actor_session: 'sess-9', session_url: 'https://ui.example/p/acme/s/sess-9' })],
      { projectId: 'acme' },
    )
    expect(entry!.sessionPath).toBe('https://ui.example/p/acme/s/sess-9')
  })
})

describe('filtering (§15.9, client side)', () => {
  const entries = buildChangelog([
    ev({ id: 'a', created_at: 100, action: 'worker_create', actor_worker: 'manager' }),
    ev({ id: 'b', created_at: 200, action: 'worker_prompt_write', actor_worker: 'tuner' }),
    ev({ id: 'c', created_at: 300, action: 'subscription_create', payload: { id: 'x' } }),
  ])

  it('matches an exact action and a trailing-* prefix', () => {
    expect(actionMatches('worker_*', 'worker_create')).toBe(true)
    expect(actionMatches('worker_*', 'subscription_create')).toBe(false)
    expect(actionMatches('worker_create', 'worker_create')).toBe(true)
    expect(actionMatches('', 'anything')).toBe(true)
  })

  it('filters by action prefix, actor and time range', () => {
    expect(filterChangelog(entries, { action: 'worker_*' }).map((e) => e.id)).toEqual(['b', 'a'])
    expect(filterChangelog(entries, { actorWorker: 'tuner' }).map((e) => e.id)).toEqual(['b'])
    expect(filterChangelog(entries, { since: 200, until: 250 }).map((e) => e.id)).toEqual(['b'])
    expect(filterChangelog(entries, { entity: 'subscription:x' }).map((e) => e.id)).toEqual(['c'])
    expect(filterChangelog(entries, { limit: 1 }).map((e) => e.id)).toEqual(['c'])
  })
})

describe('the read-route contract', () => {
  it('builds only the parameters the route is asked to support', () => {
    const params = changelogQueryParams({
      action: 'worker_*',
      actorWorker: 'tuner',
      since: 1,
      until: 2,
      limit: 10,
    })
    expect(params.toString()).toBe('action=worker_*&actor_worker=tuner&since=1&until=2&limit=10')
    expect(changelogQueryParams({}).toString()).toBe('')
  })

  it('reads the {config_events: [...]} envelope, and tolerates the alternatives', () => {
    const row = { id: 'c1', action: 'worker_create', payload: { name: 'w' }, created_at: 5 }
    expect(extractConfigEvents({ config_events: [row] })[0]!.id).toBe('c1')
    expect(extractConfigEvents({ events: [row] })[0]!.id).toBe('c1')
    expect(extractConfigEvents([row])[0]!.id).toBe('c1')
    expect(extractConfigEvents(null)).toEqual([])
    expect(extractConfigEvents({ nope: 1 })).toEqual([])
  })

  it('treats created_at as milliseconds, not seconds', () => {
    // 1789000000123 ms is 2026-09; the same number read as seconds is year 58,000.
    expect(formatConfigTimestamp(1_789_000_000_123)).toContain('2026')
    expect(formatConfigTimestamp(0)).toBe('')
  })
})
