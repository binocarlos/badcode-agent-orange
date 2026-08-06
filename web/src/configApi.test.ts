// The pure half of configApi: how a DELETE, which has no body, says why.

import { describe, expect, it } from 'vitest'
import { ApiError, errorStatus, withRationale } from './configApi.js'

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

// RD15/I1: a caller that must tell "this is gone" from "this failed" needs the
// HTTP status, and `request` used to throw the body text and drop it. Reading a
// status out of prose is B6's defect; this is the material that lets a caller
// avoid it.
describe('errorStatus', () => {
  it('reads the status off an ApiError', () => {
    expect(errorStatus(new ApiError('not found', 404))).toBe(404)
    expect(errorStatus(new ApiError('boom', 500))).toBe(500)
  })

  it('is null for anything that did not come from an HTTP response', () => {
    // A network failure, a bug in a reducer, a rejected value carrying a
    // string: none of these is a 404, and none may be reported as deletion.
    expect(errorStatus(new Error('Failed to fetch'))).toBeNull()
    expect(errorStatus('404')).toBeNull()
    expect(errorStatus(null)).toBeNull()
  })

  it('keeps the server body as the message, which is what every caller renders', () => {
    const err = new ApiError('worker not found', 404)
    expect(err.message).toBe('worker not found')
    expect(err instanceof Error).toBe(true)
  })
})
