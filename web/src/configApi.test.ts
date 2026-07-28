// The pure half of configApi: how a DELETE, which has no body, says why.

import { describe, expect, it } from 'vitest'
import { withRationale } from './configApi.js'

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
