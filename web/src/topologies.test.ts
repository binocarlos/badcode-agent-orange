// T3: the pure half of the topology onboarding flow — the wire mirror of
// go/httpapi/topologies.go, answer seeding/validation, and the answers body.

import { describe, it, expect } from 'vitest'
import {
  TOPOLOGY_ENDPOINTS,
  coerceTopology,
  coerceTopologyApplyResult,
  coerceTopologyPreview,
  initialTopologyAnswers,
  topologyAnswersBody,
  topologyRef,
  validateTopologyAnswers,
  type TopologyQuestion,
} from './topologies.js'

const question = (over: Partial<TopologyQuestion> = {}): TopologyQuestion => ({
  id: 'q',
  prompt: 'A question?',
  type: 'string',
  choices: [],
  default: null,
  required: false,
  ...over,
})

describe('endpoints', () => {
  it('mirrors the three T2 routes', () => {
    expect(TOPOLOGY_ENDPOINTS.list).toBe('/agent/topologies')
    expect(TOPOLOGY_ENDPOINTS.preview).toBe('/agent/topologies/preview')
    expect(TOPOLOGY_ENDPOINTS.apply).toBe('/agent/topologies/apply')
  })
})

describe('coerceTopology', () => {
  it('keeps the wire fields and defaults the rest', () => {
    const t = coerceTopology({
      name: 'actor-critic',
      version: 'v1',
      description: 'An actor and a critic.',
      questions: [
        { id: 'domain', prompt: 'What domain?', type: 'string', required: true },
        { id: 'freeze', prompt: 'Freeze the critic?', type: 'bool', default: true },
        { id: 'cadence', prompt: 'How often?', type: 'choice', choices: ['daily', 'weekly'], default: 'daily' },
      ],
    })
    expect(topologyRef(t)).toBe('actor-critic@v1')
    expect(t.questions).toHaveLength(3)
    expect(t.questions[0]).toEqual({
      id: 'domain',
      prompt: 'What domain?',
      type: 'string',
      choices: [],
      default: null,
      required: true,
    })
    expect(t.questions[1]!.default).toBe(true)
    expect(t.questions[2]!.choices).toEqual(['daily', 'weekly'])
  })

  it('survives garbage without throwing', () => {
    const t = coerceTopology(null)
    expect(t.name).toBe('')
    expect(t.questions).toEqual([])
  })
})

describe('coerceTopologyPreview', () => {
  it('fills every list so components never branch on undefined', () => {
    const p = coerceTopologyPreview({})
    expect(p.diff.new_workers).toEqual([])
    expect(p.diff.colliding_workers).toEqual([])
    expect(p.diff.new_subscriptions).toEqual([])
    expect(p.diff.new_schedules).toEqual([])
    expect(p.diff.settings_fields).toEqual([])
    expect(p.diff.memory_seeds).toBe(0)
    expect(p.missing_images).toEqual([])
    expect(p.missing_skills).toEqual([])
    expect(p.bundle.workers).toEqual([])
  })

  it('reads an absent or garbled verdict as NOT applicable', () => {
    expect(coerceTopologyPreview({}).applicable).toBe(false)
    expect(coerceTopologyPreview({ applicable: 'yes' }).applicable).toBe(false)
    expect(coerceTopologyPreview({ applicable: true }).applicable).toBe(true)
  })

  it('keeps the diff summaries verbatim', () => {
    const p = coerceTopologyPreview({
      diff: {
        new_workers: ['actor'],
        colliding_workers: ['critic'],
        new_subscriptions: [{ event_type: 'worker.finished', worker: 'critic' }],
        new_schedules: [{ cron: '0 9 * * *', worker: 'actor', input: 'go' }],
        settings_fields: ['base_image'],
        memory_seeds: 2,
      },
    })
    expect(p.diff.new_subscriptions[0]).toEqual({ event_type: 'worker.finished', worker: 'critic' })
    expect(p.diff.new_schedules[0]).toEqual({ cron: '0 9 * * *', worker: 'actor', input: 'go' })
    expect(p.diff.settings_fields).toEqual(['base_image'])
    expect(p.diff.memory_seeds).toBe(2)
  })
})

describe('coerceTopologyApplyResult', () => {
  it('reads back the created rows and the receipt event', () => {
    const r = coerceTopologyApplyResult({
      workers: [{ name: 'actor' }, { name: 'critic', frozen: true }],
      subscriptions: [{ id: 's1' }],
      schedules: [],
      event: { id: 'ev1', action: 'topology_apply', created_at: 5 },
    })
    expect(r.workers.map((w) => w.name)).toEqual(['actor', 'critic'])
    expect(r.workers[1]!.frozen).toBe(true)
    expect(r.subscriptions).toHaveLength(1)
    expect(r.event.id).toBe('ev1')
    expect(r.event.action).toBe('topology_apply')
  })
})

describe('initialTopologyAnswers', () => {
  it('seeds defaults, false for undefaulted bools, blank otherwise', () => {
    const qs = [
      question({ id: 'name', type: 'string' }),
      question({ id: 'greeting', type: 'string', default: 'hello' }),
      question({ id: 'freeze', type: 'bool' }),
      question({ id: 'loud', type: 'bool', default: true }),
      question({ id: 'cadence', type: 'choice', choices: ['daily', 'weekly'], default: 'daily' }),
    ]
    expect(initialTopologyAnswers(qs)).toEqual({
      name: '',
      greeting: 'hello',
      freeze: false,
      loud: true,
      cadence: 'daily',
    })
  })
})

describe('validateTopologyAnswers', () => {
  it('flags a required string left blank', () => {
    const qs = [question({ id: 'domain', required: true })]
    expect(validateTopologyAnswers(qs, { domain: '' })).toEqual({
      domain: 'an answer is required',
    })
    expect(validateTopologyAnswers(qs, { domain: '  ' })).toEqual({
      domain: 'an answer is required',
    })
    expect(validateTopologyAnswers(qs, { domain: 'marketing' })).toEqual({})
  })

  it('lets an optional question stay blank', () => {
    const qs = [question({ id: 'note' })]
    expect(validateTopologyAnswers(qs, { note: '' })).toEqual({})
  })

  it('flags a choice outside the choices, names the legal ones', () => {
    const qs = [question({ id: 'cadence', type: 'choice', choices: ['daily', 'weekly'] })]
    expect(validateTopologyAnswers(qs, { cadence: 'hourly' })).toEqual({
      cadence: 'must be one of: daily, weekly',
    })
    expect(validateTopologyAnswers(qs, { cadence: 'weekly' })).toEqual({})
  })

  it('never flags a bool — false is an answer, not an absence', () => {
    const qs = [question({ id: 'freeze', type: 'bool', required: true })]
    expect(validateTopologyAnswers(qs, { freeze: false })).toEqual({})
  })
})

describe('topologyAnswersBody', () => {
  it('trims strings, omits blanks, keeps bools (including false)', () => {
    const qs = [
      question({ id: 'domain' }),
      question({ id: 'note' }),
      question({ id: 'freeze', type: 'bool' }),
      question({ id: 'cadence', type: 'choice', choices: ['daily'] }),
    ]
    expect(
      topologyAnswersBody(qs, { domain: '  marketing ', note: '', freeze: false, cadence: 'daily' }),
    ).toEqual({ domain: 'marketing', freeze: false, cadence: 'daily' })
  })

  it('ignores answers no question asked for', () => {
    expect(topologyAnswersBody([question({ id: 'domain' })], { stray: 'x', domain: 'y' })).toEqual({
      domain: 'y',
    })
  })
})
