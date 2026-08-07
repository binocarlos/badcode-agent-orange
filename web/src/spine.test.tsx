// @vitest-environment jsdom
// DK1: the spine primitives — the closed glyph set, its meanings, and the
// colour resolution that works with or without a host's named palette.

import React from 'react'
import { render, screen } from '@testing-library/react'
import { createTheme, ThemeProvider } from '@mui/material'
import { describe, it, expect } from 'vitest'
import {
  consoleColor,
  SPINE_GLYPH_MEANINGS,
  SPINE_GLYPHS,
  SpineGap,
  SpineGlyph,
  SpineRail,
  SpineRow,
  spineGlyphColor,
  spineRailColor,
} from './spine.js'

describe('the closed glyph set', () => {
  it('is exactly the five of §3.6, each with a sentence', () => {
    expect(SPINE_GLYPHS).toEqual(['agent', 'human', 'attention', 'failure', 'freeze'])
    for (const glyph of SPINE_GLYPHS) {
      expect(SPINE_GLYPH_MEANINGS[glyph]).not.toBe('')
    }
  })

  it('labels itself with its meaning, and marks the shape it drew', () => {
    render(<SpineGlyph glyph="attention" />)
    const glyph = screen.getByLabelText('waiting for you')
    expect(glyph.getAttribute('data-glyph')).toBe('attention')
  })

  it('can be silenced where the row already says it', () => {
    const { container } = render(<SpineGlyph glyph="agent" label="" />)
    expect(container.querySelector('[role="presentation"]')).not.toBeNull()
    expect(screen.queryByLabelText('a worker did this')).toBeNull()
  })
})

describe('colour', () => {
  const light = createTheme({ palette: { mode: 'light' } })
  const dark = createTheme({ palette: { mode: 'dark' } })

  it('falls back to the design token for the mode when the host has no named palette', () => {
    expect(spineGlyphColor(light, 'agent')).toBe('#B3541E')
    expect(spineGlyphColor(dark, 'agent')).toBe('#E0873F')
    expect(spineGlyphColor(light, 'attention')).toBe('#A6376A')
    expect(spineGlyphColor(light, 'failure')).toBe('#8F2B2B')
    expect(spineGlyphColor(dark, 'freeze')).toBe('#6FA6B8')
  })

  it('prefers the host theme when it does declare the named colours', () => {
    const themed = createTheme({
      palette: { mode: 'light', ember: { main: '#123456' } } as Record<string, unknown>,
    })
    expect(spineGlyphColor(themed, 'agent')).toBe('#123456')
    expect(consoleColor(themed, 'nosuch', '#fallback')).toBe('#fallback')
  })

  it('takes the human mark from text, not from a fifth colour', () => {
    expect(spineGlyphColor(light, 'human')).toBe(light.palette.text.primary)
  })

  it('draws the rail from ink, one step darker than divider (doc 21, X12)', () => {
    // The signature element read as floating dots at MUI's ~12% divider.
    expect(spineRailColor(light)).toBe('rgba(0, 0, 0, 0.28)')
    expect(spineRailColor(dark)).toBe('rgba(255, 255, 255, 0.32)')
    expect(spineRailColor(light)).not.toBe(light.palette.divider)
  })

  it('leaves a colour it cannot parse alone rather than breaking the line', () => {
    const named = createTheme({ palette: { mode: 'light', text: { primary: 'rebeccapurple' } } })
    expect(spineRailColor(named)).toBe('rebeccapurple')
  })
})

describe('the rail', () => {
  it('renders its rows and can hide the hairline', () => {
    render(
      <ThemeProvider theme={createTheme()}>
        <SpineRail component="ol">
          <SpineRow glyph="agent" component="li">
            <span>a worker rewrote a prompt</span>
          </SpineRow>
          <SpineRow glyph="human" component="li">
            <span>you retuned a schedule</span>
          </SpineRow>
          <SpineGap label="4h 20m of nothing" />
        </SpineRail>
      </ThemeProvider>,
    )
    expect(screen.getByText('a worker rewrote a prompt')).toBeTruthy()
    expect(screen.getByText('you retuned a schedule')).toBeTruthy()
    expect(screen.getByText('4h 20m of nothing')).toBeTruthy()
    expect(screen.getAllByRole('img')).toHaveLength(2)
  })
})
