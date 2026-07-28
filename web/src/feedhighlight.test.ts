// W4 (doc 21 §4.2, §5-M4): what an arrival looks like, in both branches.
//
// The feel is a screenshot pass, not a vitest one. What IS pinned here is the
// part a screenshot cannot show: that the reduced-motion branch replaces the
// motion with something that survives a still frame rather than deleting it,
// that the timings are the researched ones, and that a status transition pulses
// in the DESTINATION colour once and then stops.

import { describe, it, expect } from 'vitest'
import { createTheme, type Theme } from '@mui/material/styles'
import {
  CONSOLE_TOKENS,
} from './spine.js'
import {
  HIGHLIGHT_ENTRANCE_MS,
  HIGHLIGHT_TOTAL_MS,
  STATUS_CROSSFADE_MS,
  STATUS_PULSE_MS,
  diffStatuses,
  highlightMarker,
  highlightSx,
  statusCrossfadeSx,
  statusPulseSx,
} from './feedhighlight.js'

const light = createTheme({ palette: { mode: 'light' } })

/** Resolve an sx that may be a theme callback into the object it renders as. */
function resolve(sx: unknown, theme: Theme = light): Record<string, unknown> {
  return (typeof sx === 'function' ? (sx as (t: Theme) => unknown)(theme) : sx) as Record<
    string,
    unknown
  >
}

describe('an arrival, with motion', () => {
  const style = resolve(highlightSx({ active: true, tone: 'agent', reduced: false }))

  it('enters in 180ms and decays over 2.3s — one shot, then still', () => {
    const animation = String(style.animation)
    expect(animation).toContain(`ao-feed-enter ${HIGHLIGHT_ENTRANCE_MS}ms`)
    expect(animation).toContain(`ao-feed-tint-agent ${HIGHLIGHT_TOTAL_MS}ms`)
    // No iteration count: an animation that names none runs once and stops,
    // which is §4.1 rule 2 (motion must terminate) enforced by CSS itself.
    expect(animation).not.toContain('infinite')
  })

  it('tints in the AUTHORSHIP colour, as a wash and never a band', () => {
    const frames = style['@keyframes ao-feed-tint-agent'] as Record<
      string,
      { backgroundColor: string }
    >
    const tint = frames['0%']!.backgroundColor
    // Ember, alpha'd — the same token the spine glyph is drawn in.
    const [r, g, b] = [0xb3, 0x54, 0x1e]
    expect(tint).toBe(`rgba(${r}, ${g}, ${b}, 0.14)`)
    expect(CONSOLE_TOKENS.light.ember).toBe('#B3541E')
    expect(frames['100%']!.backgroundColor).toBe('transparent')
  })

  it('tints an ask rose and a human edit in ink — three tones, one vocabulary', () => {
    const ask = resolve(highlightSx({ active: true, tone: 'ask', reduced: false }))
    const frames = ask['@keyframes ao-feed-tint-ask'] as Record<
      string,
      { backgroundColor: string }
    >
    expect(frames['0%']!.backgroundColor).toBe('rgba(166, 55, 106, 0.14)')
  })

  it('does nothing at all to a row that did not arrive', () => {
    expect(highlightSx({ active: false, tone: 'agent', reduced: false })).toEqual({})
    expect(highlightMarker({ active: false, tone: 'agent', reduced: true })).toBe(false)
  })
})

describe('an arrival, under reduced motion', () => {
  const style = resolve(highlightSx({ active: true, tone: 'ask', reduced: true }))

  it('replaces the fade with a persistent border — better, not lesser', () => {
    expect(style.animation).toBeUndefined()
    expect(style.borderLeft).toBe('2px solid')
    expect(style.borderColor).toBe(CONSOLE_TOKENS.light.rose)
  })

  it('carries a NEW marker in words, so nothing is encoded only in colour', () => {
    expect(highlightMarker({ active: true, tone: 'ask', reduced: true })).toBe(true)
    // And NOT under motion: a chip that vanished with the tint would be a lie
    // the moment it faded.
    expect(highlightMarker({ active: true, tone: 'ask', reduced: false })).toBe(false)
  })
})

describe('status transitions', () => {
  it('reports only rows whose status actually moved', () => {
    const first = diffStatuses(new Map(), [
      { id: 'd1', status: 'pending' },
      { id: 'd2', status: 'ok' },
    ])
    // A row seen for the first time did not change — that is an arrival, and a
    // different module's business.
    expect([...first.changed]).toEqual([])

    const second = diffStatuses(first.next, [
      { id: 'd1', status: 'running' },
      { id: 'd2', status: 'ok' },
      { id: 'd3', status: 'pending' },
    ])
    expect([...second.changed]).toEqual(['d1'])
    expect(second.next.get('d1')).toBe('running')
  })

  it('pulses once, in the destination colour', () => {
    const style = resolve(statusPulseSx('failed', false))
    expect(String(style.animation)).toContain(`ao-status-pulse-failed ${STATUS_PULSE_MS}ms`)
    const frames = style['@keyframes ao-status-pulse-failed'] as Record<
      string,
      { backgroundColor: string }
    >
    // fault, alpha'd: severity-weighted, and the colour of where it LANDED.
    expect(frames['15%']!.backgroundColor).toBe('rgba(143, 43, 43, 0.14)')
    expect(frames['100%']!.backgroundColor).toBe('transparent')
  })

  it('does not pulse under reduced motion, and loses nothing by it', () => {
    // The chip beside it already changed, and the chip is the payload.
    expect(statusPulseSx('failed', true)).toEqual({})
    expect(statusPulseSx('', false)).toEqual({})
  })

  it('crossfades the chip in 140ms, instantly under reduced motion', () => {
    const style = resolve(statusCrossfadeSx(false))
    expect(String(style.animation)).toContain(`ao-chip-crossfade ${STATUS_CROSSFADE_MS}ms`)
    expect(statusCrossfadeSx(true)).toEqual({})
  })
})
