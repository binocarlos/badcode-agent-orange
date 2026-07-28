// @vitest-environment jsdom
// W3 (doc 21 §2 X6 + §4.2): the diff's palette and its disclosure.
//
// Two things are pinned here because they are the ones that regress silently:
// the +/− gutter glyphs (nothing is encoded only in colour), and the
// reduced-motion branch (the fold snaps, it does not disappear).

import React from 'react'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, it, expect, afterEach, vi } from 'vitest'
import { ThemeProvider, createTheme } from '@mui/material/styles'
import { DiffBlock } from './ChangelogView.js'
import type { DiffLine } from '../configLog.js'

const lines: DiffLine[] = [
  { type: 'ctx', text: 'you answer support email' },
  { type: 'del', text: 'answer email thoroughly' },
  { type: 'add', text: 'answer email briefly' },
]

/** Mock the media query the one gate reads. */
function setReducedMotion(reduced: boolean) {
  window.matchMedia = vi.fn().mockImplementation((query: string) => ({
    matches: reduced && query.includes('reduce'),
    media: query,
    onchange: null,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia
}

afterEach(() => {
  vi.restoreAllMocks()
})

function renderDiff(mode: 'light' | 'dark', props: Record<string, unknown> = {}) {
  return render(
    <ThemeProvider theme={createTheme({ palette: { mode } })}>
      <DiffBlock lines={lines} {...props} />
    </ThemeProvider>,
  )
}

describe('the diff palette (X6)', () => {
  it('tints added and removed lines rather than painting MUI success/error bands', () => {
    renderDiff('light')
    const diff = screen.getByLabelText('Prompt diff')
    const add = within(diff).getByText('+answer email briefly')
    const del = within(diff).getByText('-answer email thoroughly')
    // A wash of the token, not the raw colour: alpha, never opaque.
    expect(getComputedStyle(add).backgroundColor).toMatch(/^rgba\(/)
    expect(getComputedStyle(del).backgroundColor).toMatch(/^rgba\(/)
    expect(getComputedStyle(add).backgroundColor).not.toBe(
      getComputedStyle(del).backgroundColor,
    )
  })

  it('keeps the +/− gutter glyphs, so the diff survives greyscale', () => {
    renderDiff('dark')
    const diff = screen.getByLabelText('Prompt diff')
    expect(within(diff).getByText('+answer email briefly')).toBeInTheDocument()
    expect(within(diff).getByText('-answer email thoroughly')).toBeInTheDocument()
    // Context lines carry a space gutter and no tint.
    const ctx = within(diff).getByText('you answer support email')
    expect(ctx.textContent).toBe(' you answer support email')
    expect(getComputedStyle(ctx).backgroundColor).toBe('rgba(0, 0, 0, 0)')
  })
})

describe('the disclosure (§4.2)', () => {
  it('is open and uncollapsible by default — a diff shown on its own', () => {
    renderDiff('light')
    expect(document.querySelector('details')).toBeNull()
  })

  it('collapses behind the counts and the first hunk, and opens on click', async () => {
    renderDiff('light', {
      collapsible: true,
      added: 1,
      removed: 1,
      summaryNote: 'against the previous version',
    })
    const summary = screen.getByText(
      '▸ +1 −1 against the previous version · -answer email thoroughly',
    )
    const details = document.querySelector('details')!
    expect(details.open).toBe(false)

    await userEvent.click(summary)
    expect(document.querySelector('details')!.open).toBe(true)
    expect(
      screen.getByText('▾ +1 −1 against the previous version · -answer email thoroughly'),
    ).toBeInTheDocument()
  })

  it('derives the counts from the lines when they are not passed', () => {
    renderDiff('light', { collapsible: true })
    expect(screen.getByText(/^▸ \+1 −1 · -answer email thoroughly$/)).toBeInTheDocument()
  })

  it('animates the height with the 0fr→1fr trick, child min-height 0', async () => {
    renderDiff('light', { collapsible: true })
    const grid = document.querySelector('details > div:last-of-type') as HTMLElement
    expect(getComputedStyle(grid).gridTemplateRows).toBe('0fr')
    expect(getComputedStyle(grid).transition).toContain('200ms')
    const child = grid.firstElementChild as HTMLElement
    expect(getComputedStyle(child).minHeight).toBe('0px')
    expect(getComputedStyle(child).overflow).toBe('hidden')

    await userEvent.click(screen.getByText(/^▸ /))
    expect(getComputedStyle(grid).gridTemplateRows).toBe('1fr')
  })

  it('snaps open under reduced motion — the fold is not lost, only the transition', async () => {
    setReducedMotion(true)
    renderDiff('light', { collapsible: true })
    const grid = document.querySelector('details > div:last-of-type') as HTMLElement
    expect(getComputedStyle(grid).transition).toBe('none')
    await userEvent.click(screen.getByText(/^▸ /))
    expect(getComputedStyle(grid).gridTemplateRows).toBe('1fr')
  })
})
