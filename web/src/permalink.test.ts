// Locks the canonical session permalink format (F3). agentd mints the same
// string server-side (go/cmd/agentd/permalink.go) — if this table changes, that
// one changes with it.

import { describe, it, expect } from 'vitest'
import {
  SESSION_PERMALINK_FORMAT,
  buildSessionPath,
  buildSessionPermalink,
  parseSessionPermalink,
} from './permalink.js'

describe('buildSessionPath', () => {
  const cases: Array<{ name: string; project: string; session: string; want: string }> = [
    { name: 'plain ids', project: 'acme', session: 'sess-123', want: '/p/acme/s/sess-123' },
    { name: 'uuid session', project: 'p1', session: '9f1c2d3e-0000-4a5b-8c7d-000000000001',
      want: '/p/p1/s/9f1c2d3e-0000-4a5b-8c7d-000000000001' },
    { name: 'encodes slashes', project: 'a/b', session: 'c d', want: '/p/a%2Fb/s/c%20d' },
    { name: 'encodes query chars', project: 'p?x', session: 's#y', want: '/p/p%3Fx/s/s%23y' },
  ]
  for (const c of cases) {
    it(c.name, () => {
      expect(buildSessionPath(c.project, c.session)).toBe(c.want)
    })
  }
})

describe('buildSessionPermalink', () => {
  const cases: Array<{ name: string; base: string; want: string }> = [
    { name: 'empty base → relative', base: '', want: '/p/acme/s/s1' },
    { name: 'origin', base: 'https://orange.example.com', want: 'https://orange.example.com/p/acme/s/s1' },
    { name: 'trailing slash trimmed', base: 'https://orange.example.com/', want: 'https://orange.example.com/p/acme/s/s1' },
    { name: 'many trailing slashes trimmed', base: 'https://o.example.com///', want: 'https://o.example.com/p/acme/s/s1' },
    { name: 'sub-path base preserved', base: 'https://o.example.com/agents', want: 'https://o.example.com/agents/p/acme/s/s1' },
    { name: 'localhost with port', base: 'http://localhost:8080', want: 'http://localhost:8080/p/acme/s/s1' },
  ]
  for (const c of cases) {
    it(c.name, () => {
      expect(buildSessionPermalink(c.base, 'acme', 's1')).toBe(c.want)
    })
  }
})

describe('parseSessionPermalink', () => {
  const ok: Array<{ name: string; input: string; project: string; session: string }> = [
    { name: 'relative path', input: '/p/acme/s/s1', project: 'acme', session: 's1' },
    { name: 'absolute url', input: 'https://o.example.com/p/acme/s/s1', project: 'acme', session: 's1' },
    { name: 'http url with port', input: 'http://localhost:8080/p/acme/s/s1', project: 'acme', session: 's1' },
    { name: 'sub-path mount', input: 'https://o.example.com/agents/p/acme/s/s1', project: 'acme', session: 's1' },
    { name: 'trailing slash', input: '/p/acme/s/s1/', project: 'acme', session: 's1' },
    { name: 'query string ignored', input: '/p/acme/s/s1?tab=artifacts', project: 'acme', session: 's1' },
    { name: 'hash ignored', input: '/p/acme/s/s1#msg-4', project: 'acme', session: 's1' },
    { name: 'hash before query', input: '/p/acme/s/s1#a?b', project: 'acme', session: 's1' },
    { name: 'percent-decoded', input: '/p/a%2Fb/s/c%20d', project: 'a/b', session: 'c d' },
  ]
  for (const c of ok) {
    it(`parses ${c.name}`, () => {
      expect(parseSessionPermalink(c.input)).toEqual({ projectId: c.project, sessionId: c.session })
    })
  }

  const bad: Array<{ name: string; input: string }> = [
    { name: 'empty', input: '' },
    { name: 'root', input: '/' },
    { name: 'project only', input: '/p/acme' },
    { name: 'wrong session segment', input: '/p/acme/x/s1' },
    { name: 'wrong project segment', input: '/q/acme/s/s1' },
    { name: 'missing session id', input: '/p/acme/s/' },
    { name: 'unrelated path', input: '/settings/workers' },
    { name: 'origin only', input: 'https://o.example.com' },
  ]
  for (const c of bad) {
    it(`rejects ${c.name}`, () => {
      expect(parseSessionPermalink(c.input)).toBeNull()
    })
  }

  it('round-trips every build output', () => {
    const ids: Array<[string, string]> = [
      ['acme', 's1'],
      ['a/b', 'c d'],
      ['proj-with-dashes', '9f1c2d3e-0000-4a5b-8c7d-000000000001'],
      ['ünï', 'sé§sion'],
    ]
    for (const [project, session] of ids) {
      const url = buildSessionPermalink('https://o.example.com', project, session)
      expect(parseSessionPermalink(url)).toEqual({ projectId: project, sessionId: session })
    }
  })

  it('malformed percent-escapes do not throw', () => {
    expect(parseSessionPermalink('/p/%E0%A4%A/s/s1')).toEqual({
      projectId: '%E0%A4%A',
      sessionId: 's1',
    })
  })
})

it('documents the canonical format', () => {
  expect(SESSION_PERMALINK_FORMAT).toBe('/p/:projectId/s/:sessionId')
})
