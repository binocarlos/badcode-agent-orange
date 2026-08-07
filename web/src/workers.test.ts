// C3: the pure half of the worker surface — identity, the §13 image grammar,
// the PUT body, and the router-free selection parameter.

import { describe, it, expect } from 'vitest'
import {
  buildWorkerSearch,
  coerceWorker,
  describeImageRef,
  newWorkerDraft,
  parseImageRef,
  validateImageRef,
  validateSelector,
  validateWorker,
  workerBody,
  workerFromSearch,
  DEFAULT_MAX_INSTANCES,
} from './workers.js'

describe('validateWorker: name', () => {
  const cases: { name: string; ok: boolean }[] = [
    { name: 'email-answerer', ok: true },
    { name: 'a', ok: true },
    { name: 'worker9', ok: true },
    { name: 'email-review-consultant', ok: true },
    { name: '', ok: false },
    { name: 'Email-Answerer', ok: false },
    { name: 'email_answerer', ok: false },
    { name: 'email--answerer', ok: false },
    { name: '-email', ok: false },
    { name: 'email-', ok: false },
    { name: 'email answerer', ok: false },
  ]

  for (const c of cases) {
    it(`${c.ok ? 'accepts' : 'rejects'} ${JSON.stringify(c.name)}`, () => {
      const errors = validateWorker({ ...newWorkerDraft('p'), name: c.name })
      expect(errors.name === undefined).toBe(c.ok)
    })
  }
})

describe('parseImageRef / validateImageRef', () => {
  it('empty is legal and means "inherit the project base image"', () => {
    expect(parseImageRef('')).toBeNull()
    expect(validateImageRef('')).toBeNull()
    expect(describeImageRef('', 'core:1')).toContain('core:1')
  })

  it('a bare name is the floating form', () => {
    expect(parseImageRef('marketing-tools')).toEqual({ name: 'marketing-tools', version: null })
    expect(describeImageRef('marketing-tools')).toMatch(/floating/i)
  })

  it('name:version pins', () => {
    expect(parseImageRef('marketing-tools:3')).toEqual({ name: 'marketing-tools', version: 3 })
    expect(describeImageRef('marketing-tools:3')).toMatch(/pinned/i)
  })

  const bad = [
    'Marketing-Tools', // uppercase
    'marketing tools', // whitespace
    'marketing-tools:', // no version after the colon
    'marketing-tools:0', // versions start at 1
    'marketing-tools:v3', // versions are integers
    'marketing-tools:1:2', // one colon only
    'registry.io/ns/img', // a full registry URL is not an image *name*
    '-leading',
  ]
  for (const ref of bad) {
    it(`rejects ${JSON.stringify(ref)}`, () => {
      expect(parseImageRef(ref)).toBeNull()
      expect(validateImageRef(ref)).toMatch(/name/)
    })
  }
})

describe('validateWorker: max_instances and briefing', () => {
  it('defaults to 1', () => {
    expect(newWorkerDraft().max_instances).toBe(DEFAULT_MAX_INSTANCES)
  })

  it('rejects 0 and negatives — the engine floor is 1', () => {
    expect(validateWorker({ ...newWorkerDraft(), name: 'w', max_instances: 0 }).max_instances)
      .toMatch(/at least 1/)
    expect(validateWorker({ ...newWorkerDraft(), name: 'w', max_instances: -2 }).max_instances)
      .toMatch(/at least 1/)
  })

  it('flags a blank selector by index, and only that one', () => {
    const errors = validateWorker({
      ...newWorkerDraft(),
      name: 'w',
      briefing: ['kind=house-style', '   ', 'thread=x'],
    })
    expect(errors['briefing.1']).toMatch(/empty/)
    expect(errors['briefing.0']).toBeUndefined()
    expect(errors['briefing.2']).toBeUndefined()
  })

  it('does not second-guess selector grammar — the server owns the parser', () => {
    // Deliberately exotic but legal K8s-ish selectors: this module must pass
    // them through untouched rather than reimplement the grammar.
    for (const sel of ['kind in (a, b)', 'thread notin (spam)', 'worker!=x', '!archived']) {
      expect(validateSelector(sel)).toBeNull()
    }
  })
})

describe('workerBody', () => {
  it('sends only what the body owns — never project, name or timestamps', () => {
    const body = workerBody({
      ...newWorkerDraft('acme'),
      name: 'email-answerer',
      created_at: 1,
      updated_at: 2,
    })
    expect('project' in body).toBe(false)
    expect('name' in body).toBe(false)
    expect('created_at' in body).toBe(false)
    expect(body.enabled).toBe(true)
    expect(body.max_instances).toBe(1)
    expect(body.frozen).toBe(false)
  })

  it('always sends frozen explicitly — PUT replaces, and an omitted frozen unfreezes', () => {
    expect(workerBody({ ...newWorkerDraft(), name: 'w', frozen: true }).frozen).toBe(true)
    expect('frozen' in workerBody({ ...newWorkerDraft(), name: 'w' })).toBe(true)
  })

  it('preserves null vs [] on briefing — the engine keeps them distinct', () => {
    expect(workerBody({ ...newWorkerDraft(), name: 'w' }).briefing).toBeNull()
    expect(workerBody({ ...newWorkerDraft(), name: 'w', briefing: [] }).briefing).toEqual([])
  })

  it('trims the image — a stray space is a silent resolution failure', () => {
    expect(workerBody({ ...newWorkerDraft(), name: 'w', image: ' tools:2 ' }).image).toBe('tools:2')
  })

  it('carries the operator\u2019s reason, and omits an empty one', () => {
    const draft = { ...newWorkerDraft(), name: 'w' }
    expect(workerBody(draft, '  the replies were too long  ')).toMatchObject({
      rationale: 'the replies were too long',
    })
    expect(workerBody(draft)).not.toHaveProperty('rationale')
    expect(workerBody(draft, '   ')).not.toHaveProperty('rationale')
  })
})

describe('coerceWorker', () => {
  it('applies the spec defaults to a sparse row', () => {
    const w = coerceWorker({ name: 'w' }, 'acme')
    expect(w.max_instances).toBe(1)
    expect(w.enabled).toBe(true)
    expect(w.briefing).toBeNull()
    expect(w.mcp_config).toEqual({})
  })

  it('keeps enabled:false rather than re-defaulting it to true', () => {
    expect(coerceWorker({ name: 'w', enabled: false }).enabled).toBe(false)
  })

  it('defaults frozen to false and keeps an explicit true', () => {
    expect(coerceWorker({ name: 'w' }).frozen).toBe(false)
    expect(coerceWorker({ name: 'w', frozen: true }).frozen).toBe(true)
  })

  it('survives a garbage row', () => {
    expect(coerceWorker(undefined, 'acme').project).toBe('acme')
  })
})

describe('worker selection in the URL', () => {
  it('reads the parameter with or without the leading ?', () => {
    expect(workerFromSearch('?worker=email-answerer')).toBe('email-answerer')
    expect(workerFromSearch('worker=email-answerer')).toBe('email-answerer')
    expect(workerFromSearch('')).toBeNull()
    expect(workerFromSearch('?other=1')).toBeNull()
  })

  it('sets the parameter, preserving everything else in the query', () => {
    expect(buildWorkerSearch('?tab=jobs', 'email-answerer')).toBe('?tab=jobs&worker=email-answerer')
  })

  it('replaces rather than appends on re-selection', () => {
    expect(buildWorkerSearch('?worker=a', 'b')).toBe('?worker=b')
  })

  it('removes the parameter on deselect, leaving a clean query', () => {
    expect(buildWorkerSearch('?worker=a', null)).toBe('')
    expect(buildWorkerSearch('?tab=jobs&worker=a', null)).toBe('?tab=jobs')
  })

  it('round-trips a name that needs escaping', () => {
    // Not a legal worker name, but the URL layer must not corrupt whatever it
    // is handed — a broken round-trip here would silently select nothing.
    const search = buildWorkerSearch('', 'a b&c')
    expect(workerFromSearch(search)).toBe('a b&c')
  })
})
