import { describe, it, expect } from 'vitest'
import {
  briefingSlots,
  buildMemorySelector,
  capBriefingContent,
  coerceMemory,
  DEFAULT_BRIEFING_HEADING,
  foldNamedMemories,
  formatRequirement,
  labelKeyError,
  labelValueError,
  parseMemorySelector,
  rollingSummarySelector,
  semanticLegLooksOff,
  type MemoryRow,
} from './memories.js'

const row = (over: Partial<MemoryRow> = {}): MemoryRow => ({
  id: 'm1',
  labels: {},
  snippet: '',
  score: 0,
  created_by_worker: '',
  created_by_session: '',
  created_at: 0,
  ...over,
})

describe('coerceMemory', () => {
  it('takes the wire shape and drops non-string labels', () => {
    expect(
      coerceMemory({
        id: 'm7',
        labels: { kind: 'lesson', n: 4 },
        snippet: 'hi',
        score: 0.5,
        created_by_worker: 'archivist',
        created_by_session: 's1',
        created_at: 1_753_000_000_000,
      }),
    ).toEqual({
      id: 'm7',
      labels: { kind: 'lesson' },
      snippet: 'hi',
      score: 0.5,
      created_by_worker: 'archivist',
      created_by_session: 's1',
      created_at: 1_753_000_000_000,
    })
  })

  it('never throws on rubbish', () => {
    expect(coerceMemory(null).id).toBe('')
    expect(coerceMemory(42).labels).toEqual({})
    expect(coerceMemory({ score: 'high' }).score).toBe(0)
  })
})

describe('label validation mirrors the engine', () => {
  const cases: [string, string | null][] = [
    ['kind', null],
    ['a.b_c-d', null],
    ['', 'label key must not be empty'],
    ['-bad', `label key "-bad" is invalid: must be alphanumeric, optionally containing '-', '_' or '.', and start and end alphanumeric`],
    ['a/b', `label key "a/b" is invalid: must be alphanumeric, optionally containing '-', '_' or '.', and start and end alphanumeric`],
    ['x'.repeat(64), `label key "${'x'.repeat(64)}" is 64 chars, max 63`],
  ]
  for (const [input, want] of cases) {
    it(`key ${JSON.stringify(input)}`, () => expect(labelKeyError(input)).toBe(want))
  }

  it('the empty VALUE is legal, unlike the empty key', () => {
    expect(labelValueError('')).toBeNull()
    expect(labelValueError('ok-1')).toBeNull()
    expect(labelValueError('no spaces')).toMatch(/label value "no spaces" is invalid/)
  })
})

describe('parseMemorySelector', () => {
  it('parses the whole grammar', () => {
    expect(parseMemorySelector('worker=email-answerer,kind!=raw-transcript')).toEqual({
      requirements: [
        { key: 'worker', op: '=', values: ['email-answerer'] },
        { key: 'kind', op: '!=', values: ['raw-transcript'] },
      ],
      error: null,
    })
    expect(parseMemorySelector('kind in (summary, lesson)').requirements).toEqual([
      { key: 'kind', op: 'in', values: ['summary', 'lesson'] },
    ])
    expect(parseMemorySelector('thread notin (spam)').requirements).toEqual([
      { key: 'thread', op: 'notin', values: ['spam'] },
    ])
    expect(parseMemorySelector('exists thread').requirements).toEqual([
      { key: 'thread', op: 'exists', values: [] },
    ])
    expect(parseMemorySelector('thread').requirements).toEqual([
      { key: 'thread', op: 'exists', values: [] },
    ])
    expect(parseMemorySelector('!archived').requirements).toEqual([
      { key: 'archived', op: '!', values: [] },
    ])
    expect(parseMemorySelector('kind==lesson').requirements).toEqual([
      { key: 'kind', op: '=', values: ['lesson'] },
    ])
  })

  it('the empty selector matches everything', () => {
    expect(parseMemorySelector('')).toEqual({ requirements: [], error: null })
    expect(parseMemorySelector('   ,  ')).toEqual({ requirements: [], error: null })
  })

  it('a comma inside a set belongs to the set', () => {
    const parsed = parseMemorySelector('kind in (a, b),worker=w')
    expect(parsed.error).toBeNull()
    expect(parsed.requirements).toHaveLength(2)
  })

  it('reports errors the way the parser does, keeping the good chips', () => {
    expect(parseMemorySelector('kind in (a').error).toBe(`selector "kind in (a": unbalanced '('`)
    expect(parseMemorySelector('a),b').error).toBe(`selector "a),b": unbalanced ')'`)
    expect(parseMemorySelector('kind in (a,,b)').error).toBe(
      `selector term "kind in (a,,b)": empty value in set`,
    )
    const partial = parseMemorySelector('worker=w,!!bad')
    expect(partial.requirements).toEqual([{ key: 'worker', op: '=', values: ['w'] }])
    expect(partial.error).toMatch(/^selector term "!!bad": label key "!bad" is invalid/)
    expect(parseMemorySelector('no spaces here').error).toMatch(
      /^selector term "no spaces here" is not a valid requirement/,
    )
  })
})

describe('the builder round-trips the parser', () => {
  const inputs = [
    'worker=email-answerer,kind!=raw-transcript',
    'kind in (summary, lesson)',
    'thread notin (spam)',
    'thread',
    '!archived',
  ]
  for (const input of inputs) {
    it(input, () => {
      const first = parseMemorySelector(input)
      const rebuilt = buildMemorySelector(first.requirements)
      expect(parseMemorySelector(rebuilt).requirements).toEqual(first.requirements)
    })
  }

  it('formats one requirement as its canonical text', () => {
    expect(formatRequirement({ key: 'kind', op: '=', values: ['lesson'] })).toBe('kind=lesson')
    expect(formatRequirement({ key: 'kind', op: 'in', values: ['a', 'b'] })).toBe('kind in (a, b)')
    expect(formatRequirement({ key: 'k', op: 'exists', values: [] })).toBe('k')
    expect(formatRequirement({ key: 'k', op: '!', values: [] })).toBe('!k')
  })
})

describe('semanticLegLooksOff', () => {
  it('says nothing without a query or without rows', () => {
    expect(semanticLegLooksOff([], 'anything')).toBe(false)
    expect(semanticLegLooksOff([row({ snippet: 'x' })], '')).toBe(false)
    expect(semanticLegLooksOff([row({ snippet: 'x' })], 'a of')).toBe(false)
  })

  it('is quiet when a keyword hit exists — the legs cannot be told apart then', () => {
    expect(semanticLegLooksOff([row({ snippet: 'the invoice was paid' })], 'invoice')).toBe(false)
  })

  it('flags a result set with no keyword hit at all', () => {
    expect(semanticLegLooksOff([row({ snippet: 'the cat sat' })], 'invoice')).toBe(true)
  })
})

describe('foldNamedMemories', () => {
  it('puts the current value first and folds the superseded ones under it', () => {
    const older = row({ id: 'a', labels: { name: 'tone' }, snippet: 'old', created_at: 1000 })
    const newer = row({ id: 'b', labels: { name: 'tone' }, snippet: 'new', created_at: 2000 })
    const other = row({ id: 'c', labels: { kind: 'lesson' }, snippet: 'l' })
    const folded = foldNamedMemories([older, newer, other])
    expect(folded.named).toHaveLength(1)
    expect(folded.named[0].name).toBe('tone')
    expect(folded.named[0].current.id).toBe('b')
    expect(folded.named[0].superseded.map((r) => r.id)).toEqual(['a'])
    expect(folded.rest.map((r) => r.id)).toEqual(['c'])
  })

  it('breaks timestamp ties by id, so the fold is deterministic', () => {
    const x = row({ id: 'x', labels: { name: 'n' }, created_at: 5 })
    const y = row({ id: 'y', labels: { name: 'n' }, created_at: 5 })
    expect(foldNamedMemories([y, x]).named[0].current.id).toBe('x')
    expect(foldNamedMemories([x, y]).named[0].current.id).toBe('x')
  })

  it('keeps first-seen group order and server order for the rest', () => {
    const rows = [
      row({ id: '1', labels: { name: 'b' } }),
      row({ id: '2', labels: {} }),
      row({ id: '3', labels: { name: 'a' } }),
      row({ id: '4', labels: {} }),
    ]
    const folded = foldNamedMemories(rows)
    expect(folded.named.map((g) => g.name)).toEqual(['b', 'a'])
    expect(folded.rest.map((r) => r.id)).toEqual(['2', '4'])
  })
})

describe('briefingSlots mirrors BuildBriefingSections', () => {
  it('puts the built-in selector first, then the worker\'s own', () => {
    expect(briefingSlots({ name: 'answerer', briefing: ['kind=lesson'] })).toEqual([
      {
        selector: 'kind=rolling-summary,worker=answerer',
        heading: DEFAULT_BRIEFING_HEADING,
        builtin: true,
      },
      {
        selector: 'kind=lesson',
        heading: `${DEFAULT_BRIEFING_HEADING}: kind=lesson`,
        builtin: false,
      },
    ])
  })

  it('deduplicates: listing the rolling summary explicitly is one section, not two', () => {
    const slots = briefingSlots({
      name: 'answerer',
      briefing: [' kind=rolling-summary,worker=answerer ', ''],
    })
    expect(slots).toHaveLength(1)
    expect(slots[0].builtin).toBe(true)
  })

  it('a nameless worker and a null briefing are both legal', () => {
    expect(briefingSlots({ name: '', briefing: null })).toEqual([])
    expect(briefingSlots({ name: 'w', briefing: null })).toHaveLength(1)
  })

  it('the built-in selector is byte-for-byte the engine\'s', () => {
    expect(rollingSummarySelector('email-answerer')).toBe(
      'kind=rolling-summary,worker=email-answerer',
    )
  })
})

describe('capBriefingContent', () => {
  it('leaves a short section alone', () => {
    expect(capBriefingContent('short', 2048)).toEqual({
      text: 'short',
      truncated: false,
      bytes: 5,
    })
  })

  it('exactly at the cap is not truncated', () => {
    const c = capBriefingContent('x'.repeat(32), 32)
    expect(c.truncated).toBe(false)
  })

  it('cuts at the cap and appends the marker after it', () => {
    const c = capBriefingContent('x'.repeat(40), 32)
    expect(c.truncated).toBe(true)
    expect(c.text).toBe('x'.repeat(32) + '\n\n[… briefing section truncated at 32 bytes]')
  })

  it('counts BYTES, not characters, and cuts on a rune boundary', () => {
    // Six 3-byte runes = 18 bytes; a 10-byte cap lands mid-rune.
    const c = capBriefingContent('日本語日本語', 10)
    expect(c.bytes).toBe(18)
    expect(c.truncated).toBe(true)
    expect(c.text.startsWith('日本語')).toBe(true)
    expect(c.text).not.toContain('�')
  })

  it('a zero cap means unset, not "no briefing"', () => {
    expect(capBriefingContent('anything', 0).truncated).toBe(false)
  })
})
