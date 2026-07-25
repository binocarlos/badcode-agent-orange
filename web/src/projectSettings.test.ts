// B3: the pure half of the project-settings surface — JSON gating and the
// "0 means…" semantics, which are the two things this screen can get wrong in
// a way that costs money.

import { describe, it, expect } from 'vitest'
import {
  coerceProjectSettings,
  defaultProjectSettings,
  describeNumericSetting,
  formatJsonObject,
  parseJsonObject,
  projectSettingsBody,
  PROJECT_SETTING_NUMERICS,
  validateProjectSettings,
  DEFAULT_BRIEFING_MAX_BYTES,
  DEFAULT_MAX_CONCURRENT_JOBS,
  DEFAULT_SNAPSHOT_TTL_DAYS,
} from './projectSettings.js'

describe('parseJsonObject', () => {
  const cases: { name: string; input: string; ok: boolean }[] = [
    { name: 'empty text is the empty object', input: '', ok: true },
    { name: 'whitespace is the empty object', input: '   \n ', ok: true },
    { name: 'empty object literal', input: '{}', ok: true },
    { name: 'populated object', input: '{"gmail":{"command":"npx"}}', ok: true },
    { name: 'trailing comma is a syntax error', input: '{"a":1,}', ok: false },
    { name: 'unclosed brace', input: '{"a":1', ok: false },
    { name: 'array is not an object', input: '[1,2]', ok: false },
    { name: 'string is not an object', input: '"hello"', ok: false },
    { name: 'number is not an object', input: '42', ok: false },
    { name: 'null is not an object', input: 'null', ok: false },
  ]

  for (const c of cases) {
    it(c.name, () => {
      const result = parseJsonObject(c.input)
      expect(result.ok).toBe(c.ok)
      if (result.ok) expect(typeof result.value).toBe('object')
      else expect(result.error.length).toBeGreaterThan(0)
    })
  }

  it('returns the parsed object so the caller PUTs what was typed', () => {
    const result = parseJsonObject('{"gmail":{"url":"https://x"}}')
    expect(result.ok).toBe(true)
    if (result.ok) expect(result.value).toEqual({ gmail: { url: 'https://x' } })
  })
})

describe('formatJsonObject', () => {
  it('renders an empty object on one line', () => {
    expect(formatJsonObject({})).toBe('{}')
  })
  it('pretty-prints a populated object', () => {
    expect(formatJsonObject({ a: 1 })).toBe('{\n  "a": 1\n}')
  })
  it('falls back to {} for non-objects', () => {
    expect(formatJsonObject(null)).toBe('{}')
    expect(formatJsonObject('x')).toBe('{}')
  })
})

describe('zero semantics', () => {
  // The table is the contract: which fields keep a written 0 and which read it
  // as "unset". It mirrors ProjectSettings.normalize() in the engine.
  const expected: Record<string, 'meaningful' | 'unset'> = {
    daily_tokens_soft: 'meaningful',
    daily_tokens_hard: 'meaningful',
    snapshot_ttl_days: 'meaningful',
    briefing_max_bytes: 'unset',
    max_concurrent_jobs: 'unset',
  }

  it('classifies every numeric setting exactly as the engine does', () => {
    expect(PROJECT_SETTING_NUMERICS.length).toBe(Object.keys(expected).length)
    for (const spec of PROJECT_SETTING_NUMERICS) {
      expect(spec.zeroSemantics).toBe(expected[spec.key])
    }
  })

  it('names the server default on the fields that substitute one', () => {
    const byKey = Object.fromEntries(PROJECT_SETTING_NUMERICS.map((s) => [s.key, s]))
    expect(byKey.briefing_max_bytes!.serverDefault).toBe(DEFAULT_BRIEFING_MAX_BYTES)
    expect(byKey.max_concurrent_jobs!.serverDefault).toBe(DEFAULT_MAX_CONCURRENT_JOBS)
  })

  it('swaps the helper sentence at zero, and only at zero', () => {
    for (const spec of PROJECT_SETTING_NUMERICS) {
      expect(describeNumericSetting(spec, 0)).toBe(spec.zeroHelp)
      expect(describeNumericSetting(spec, 5)).toBe(spec.help)
    }
  })

  it('spells out "off" / "never" / the substituted default in words', () => {
    const byKey = Object.fromEntries(PROJECT_SETTING_NUMERICS.map((s) => [s.key, s]))
    expect(byKey.daily_tokens_soft!.zeroHelp).toMatch(/off/i)
    expect(byKey.daily_tokens_hard!.zeroHelp).toMatch(/off/i)
    expect(byKey.snapshot_ttl_days!.zeroHelp).toMatch(/never/i)
    expect(byKey.briefing_max_bytes!.zeroHelp).toContain(String(DEFAULT_BRIEFING_MAX_BYTES))
    expect(byKey.max_concurrent_jobs!.zeroHelp).toContain(String(DEFAULT_MAX_CONCURRENT_JOBS))
  })
})

describe('validateProjectSettings', () => {
  it('accepts the defaults', () => {
    expect(validateProjectSettings(defaultProjectSettings('acme'))).toEqual({})
  })

  it('accepts zero everywhere — zero is a legal value on every field', () => {
    const s = defaultProjectSettings('acme')
    for (const spec of PROJECT_SETTING_NUMERICS) s[spec.key] = 0
    expect(validateProjectSettings(s)).toEqual({})
  })

  it('rejects negatives, matching the engine', () => {
    const s = defaultProjectSettings('acme')
    s.daily_tokens_hard = -1
    expect(validateProjectSettings(s).daily_tokens_hard).toMatch(/negative/)
  })

  it('rejects a non-integer (a half-typed number is not a budget)', () => {
    const s = defaultProjectSettings('acme')
    s.snapshot_ttl_days = 1.5
    expect(validateProjectSettings(s).snapshot_ttl_days).toMatch(/whole number/)
  })
})

describe('coerceProjectSettings', () => {
  it('fills every field the server omitted', () => {
    const s = coerceProjectSettings({ base_image: 'core:1' }, 'acme')
    expect(s.base_image).toBe('core:1')
    expect(s.project).toBe('acme')
    expect(s.mcp_config).toEqual({})
    expect(s.max_concurrent_jobs).toBe(DEFAULT_MAX_CONCURRENT_JOBS)
    expect(s.snapshot_ttl_days).toBe(DEFAULT_SNAPSHOT_TTL_DAYS)
  })

  it('keeps a server-sent zero rather than re-defaulting it', () => {
    const s = coerceProjectSettings({ snapshot_ttl_days: 0, briefing_max_bytes: 0 }, 'acme')
    expect(s.snapshot_ttl_days).toBe(0)
    expect(s.briefing_max_bytes).toBe(0)
  })

  it('drops an mcp_config that is not an object', () => {
    expect(coerceProjectSettings({ mcp_config: [1, 2] }).mcp_config).toEqual({})
  })

  it('survives a null/garbage response', () => {
    expect(coerceProjectSettings(null, 'acme').project).toBe('acme')
    expect(coerceProjectSettings('nope', 'acme').project).toBe('acme')
  })
})

describe('projectSettingsBody', () => {
  it('drops project and updated_at — the server owns both', () => {
    const body = projectSettingsBody({ ...defaultProjectSettings('acme'), updated_at: 999 })
    expect('project' in body).toBe(false)
    expect('updated_at' in body).toBe(false)
    expect(body.base_image).toBe('')
  })
})
