// LN1: worker lineage (design §7.1) — the changelog, filtered to one worker's
// prompt and numbered. Pure; the rendering lives in components/WorkerLineage.

import { describe, it, expect } from 'vitest'
import { buildChangelog, coerceConfigEvent, workerLineage, type ConfigEvent } from './configLog.js'

const ev = (over: Partial<ConfigEvent> = {}): ConfigEvent =>
  coerceConfigEvent({
    project: 'acme',
    actor_worker: '',
    actor_session: '',
    action: 'worker_update',
    payload: { name: 'email-answerer' },
    rationale: '',
    created_at: 1_789_000_000_000,
    ...over,
  })

const log = (): ConfigEvent[] => [
  ev({
    id: 'c1',
    action: 'worker_create',
    payload: { name: 'email-answerer', system_prompt: 'one' },
    rationale: 'hired',
    created_at: 1000,
  }),
  ev({
    id: 'c2',
    action: 'worker_prompt_write',
    actor_worker: 'email-reviewer',
    actor_session: 'sess-2',
    payload: { name: 'email-answerer', system_prompt: 'two' },
    rationale: 'quote the ticket reference',
    created_at: 2000,
  }),
  ev({
    id: 'c3',
    action: 'worker_enable',
    payload: { name: 'email-answerer' },
    created_at: 3000,
  }),
  ev({
    id: 'c4',
    action: 'worker_prompt_write',
    actor_worker: 'email-reviewer',
    payload: { name: 'email-answerer', system_prompt: 'two' },
    rationale: 'rewrote it identically',
    created_at: 4000,
  }),
  ev({
    id: 'd1',
    action: 'worker_prompt_write',
    payload: { name: 'copy-editor', system_prompt: 'other' },
    created_at: 5000,
  }),
]

describe('workerLineage', () => {
  it('filters to one worker and numbers prompt writes oldest-first', () => {
    const lineage = workerLineage(buildChangelog(log()), 'email-answerer')
    expect(lineage.entityKey).toBe('worker:email-answerer')
    // Newest first, matching the changelog it is built from.
    expect(lineage.entries.map((r) => r.entry.id)).toEqual(['c4', 'c3', 'c2', 'c1'])
    // The enable carries no prompt, so it carries no version.
    expect(lineage.entries.map((r) => r.version)).toEqual([3, null, 2, 1])
    expect(lineage.versions).toBe(3)
  })

  it('counts rewrites past v1 and dedupes identical prompt texts', () => {
    const lineage = workerLineage(buildChangelog(log()), 'email-answerer')
    expect(lineage.rewrites).toBe(2)
    expect(lineage.distinct).toBe(2)
    expect(lineage.summary).toBe('2 rewrites · 2 distinct')
    expect(lineage.entries.find((r) => r.entry.id === 'c4')!.duplicate).toBe(true)
    expect(lineage.entries.find((r) => r.entry.id === 'c2')!.duplicate).toBe(false)
  })

  it('marks worker authorship and keeps the changelog diff', () => {
    const lineage = workerLineage(buildChangelog(log()), 'email-answerer')
    const rewrite = lineage.entries.find((r) => r.entry.id === 'c2')!
    expect(rewrite.byWorker).toBe(true)
    expect(rewrite.prompt).toBe('two')
    expect(rewrite.entry.diff?.previousEventId).toBe('c1')
    expect(lineage.entries.find((r) => r.entry.id === 'c3')!.byWorker).toBe(false)
  })

  it('says so plainly when a worker has never been rewritten', () => {
    const one = workerLineage(buildChangelog([log()[0]!]), 'email-answerer')
    expect(one.versions).toBe(1)
    expect(one.rewrites).toBe(0)
    expect(one.summary).toBe('no rewrites yet')
  })

  it('is empty for a worker with no records', () => {
    const none = workerLineage(buildChangelog(log()), 'nobody')
    expect(none.entries).toEqual([])
    expect(none.versions).toBe(0)
    expect(none.summary).toBe('no rewrites yet')
  })
})
