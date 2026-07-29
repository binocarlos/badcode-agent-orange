// @vitest-environment jsdom
// W2 / doc 21 X11: `awaiting_human` is rose, everywhere a delivery chip renders.

import React from 'react'
import { render, screen } from '@testing-library/react'
import { createTheme, ThemeProvider } from '@mui/material'
import { describe, it, expect } from 'vitest'
import DeliveryStatusChip, { attentionColor } from './DeliveryStatusChip.js'
import { deliveryStatusSeverity } from '../events.js'

const rgb = (el: Element): string => getComputedStyle(el).backgroundColor

const renderChip = (status: string, theme = createTheme()) =>
  render(
    <ThemeProvider theme={theme}>
      <DeliveryStatusChip status={status} />
    </ThemeProvider>,
  )

describe('DeliveryStatusChip', () => {
  it('paints a parked job rose, not amber', () => {
    const { container } = renderChip('awaiting_human')
    const chip = container.querySelector('[data-status="awaiting_human"]')!
    expect(chip).toBeTruthy()
    // #A6376A — the design's rose token for light mode.
    expect(rgb(chip)).toBe('rgb(166, 55, 106)')
    expect(chip.className).not.toMatch(/colorWarning/)
  })

  it('takes rose from the host theme when the host declares it', () => {
    const themed = createTheme({
      palette: { mode: 'light', rose: { main: '#123456' } } as Record<string, unknown>,
    })
    expect(attentionColor(themed)).toBe('#123456')
    const { container } = renderChip('awaiting_human', themed)
    expect(rgb(container.querySelector('[data-status="awaiting_human"]')!)).toBe('rgb(18, 52, 86)')
  })

  // Superseded deliberately: W2 painted only `awaiting_human` and left the rest
  // on MUI's buckets, which rendered the job table as traffic lights — most rows
  // a loud success green. The design's palette has no green and spends no colour
  // on "normal" (doc 21 §2 follow-up), so every status is now painted from the
  // console tokens and MUI's buckets are reserved for statuses we do not know.
  it('paints a failure with the console fault, not MUI error red', () => {
    const { container } = renderChip('failed')
    const chip = container.querySelector('[data-status="failed"]')!
    expect(chip.className).not.toMatch(/colorError/)
    expect(rgb(chip)).toBe('rgb(143, 43, 43)') // #8F2B2B, the fault token
  })

  it('keeps the expected case quiet — a finished job is a label, not a signal', () => {
    const { container } = renderChip('ok')
    const chip = container.querySelector('[data-status="ok"]')!
    expect(chip.className).not.toMatch(/colorSuccess/)
    // Outlined: no fill at all, so a table of finished jobs reads as calm.
    expect(rgb(chip)).toBe('rgba(0, 0, 0, 0)')
  })

  it('marks a running job with ember, matching the chart', () => {
    const { container } = renderChip('running')
    expect(rgb(container.querySelector('[data-status="running"]')!)).toBe('rgb(179, 84, 30)')
  })

  it('distinguishes dropped from broken — rate_limited is outlined fault', () => {
    const { container } = renderChip('rate_limited')
    const chip = container.querySelector('[data-status="rate_limited"]')!
    expect(rgb(chip)).toBe('rgba(0, 0, 0, 0)') // outlined, not filled
    expect(getComputedStyle(chip).color).toBe('rgb(143, 43, 43)')
  })

  it('falls back to a MUI bucket for a status outside the vocabulary', () => {
    const { container } = renderChip('something-new')
    expect(container.querySelector('[data-status="something-new"]')).toBeTruthy()
  })

  it('never hands a host the warning bucket for a pause', () => {
    expect(deliveryStatusSeverity('awaiting_human')).toBe('default')
    expect(deliveryStatusSeverity('rate_limited')).toBe('warning')
  })

  it('still explains itself', async () => {
    renderChip('awaiting_human')
    expect(await screen.findByText('awaiting_human')).toBeInTheDocument()
  })
})
