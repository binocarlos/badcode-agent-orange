// @vitest-environment jsdom
// (jsdom only because `useConfigApi` is a hook and `renderHook` needs a
// document; nothing here touches the DOM.)
//
// The pure half of configApi: how a DELETE, which has no body, says why, and
// how a failure is told apart from a route this deployment does not serve.

import { renderHook, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ConfigApiError, looksUnwired, useConfigApi, withRationale } from './configApi.js'

describe('withRationale', () => {
  it('appends the reason as a query parameter', () => {
    expect(withRationale('/agent/workers/email-answerer', 'it was superseded')).toBe(
      '/agent/workers/email-answerer?rationale=it%20was%20superseded',
    )
  })

  it('leaves the path alone when there is no reason', () => {
    // An absent reason must read as absent in the config log, not as a blank
    // one — the same contract the write bodies have.
    expect(withRationale('/agent/workers/w', '')).toBe('/agent/workers/w')
    expect(withRationale('/agent/workers/w', '   ')).toBe('/agent/workers/w')
  })

  it('joins onto a path that already has a query string', () => {
    expect(withRationale('/agent/schedules/1?dry=1', 'no longer needed')).toBe(
      '/agent/schedules/1?dry=1&rationale=no%20longer%20needed',
    )
  })

  it('trims the reason, as the server does', () => {
    expect(withRationale('/agent/workers/w', '  tidy up  ')).toBe(
      '/agent/workers/w?rationale=tidy%20up',
    )
  })
})

// B6. The distinction "this deployment does not serve the route" vs "the
// request failed" is what decides whether the user is shown a calm explanatory
// note or an error, so it must not be decided by prose the server happens to
// have written.
describe('looksUnwired', () => {
  it('reads the status, not the prose: only 404 and 501 are unmounted', () => {
    expect(looksUnwired(new ConfigApiError('worker not found', 404))).toBe(true)
    expect(looksUnwired(new ConfigApiError('attention requests are not configured', 501))).toBe(
      true,
    )
    expect(looksUnwired(new ConfigApiError('memory search: index is fine', 500))).toBe(false)
    expect(looksUnwired(new ConfigApiError('selector is invalid', 400))).toBe(false)
    expect(looksUnwired(new ConfigApiError('token expired', 401))).toBe(false)
  })

  it('a 500 whose body happens to say "not found" is a FAILURE, not a limitation', () => {
    // The exact shape that bit B2's executor inside its own test: a database
    // error carrying the words the old text match keyed on.
    expect(
      looksUnwired(new ConfigApiError('attention requests: relation not found', 500)),
    ).toBe(false)
    expect(looksUnwired(new ConfigApiError('config log: upstream not configured', 502))).toBe(
      false,
    )
    expect(looksUnwired(new ConfigApiError('memory search: not implemented in this shard', 503)))
      .toBe(false)
  })

  it('falls back to the text only when there is no status to read', () => {
    // A fetch that never reached a server, or a host-supplied fetcher throwing
    // whatever it likes — neither carries a status, so the words are all there
    // is.
    expect(looksUnwired(new Error('project settings not configured'))).toBe(true)
    expect(looksUnwired(new Error('HTTP 501'))).toBe(true)
    expect(looksUnwired(new Error('Failed to fetch'))).toBe(false)
    expect(looksUnwired('the memory route is not implemented here')).toBe(true)
    expect(looksUnwired(undefined)).toBe(false)
  })
})

describe('request', () => {
  const originalFetch = globalThis.fetch
  afterEach(() => {
    globalThis.fetch = originalFetch
  })

  it('throws the server body as the message and carries the status alongside it', async () => {
    globalThis.fetch = vi.fn(
      async () => new Response('workers: database is down', { status: 500 }),
    ) as unknown as typeof fetch
    const { result } = renderHook(() => useConfigApi({ apiBaseUrl: '' }))
    await waitFor(() => expect(result.current.request).toBeTypeOf('function'))
    const err = await result.current.request('/agent/workers').then(
      () => null,
      (e: unknown) => e,
    )
    expect(err).toBeInstanceOf(ConfigApiError)
    // The human still reads the server's own words...
    expect((err as Error).message).toBe('workers: database is down')
    // ...and the code branches on the status.
    expect((err as ConfigApiError).status).toBe(500)
    expect(looksUnwired(err)).toBe(false)
  })

  it('keeps the status on a body-less failure too', async () => {
    globalThis.fetch = vi.fn(
      async () => new Response('', { status: 501 }),
    ) as unknown as typeof fetch
    const { result } = renderHook(() => useConfigApi({ apiBaseUrl: '' }))
    const err = await result.current.request('/agent/config-events').then(
      () => null,
      (e: unknown) => e,
    )
    expect((err as ConfigApiError).status).toBe(501)
    expect((err as Error).message).toBe('HTTP 501')
    expect(looksUnwired(err)).toBe(true)
  })
})
