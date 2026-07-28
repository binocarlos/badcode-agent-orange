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

  it('leaves every other status on its MUI bucket', () => {
    const { container } = renderChip('failed')
    expect(container.querySelector('[data-status="failed"]')!.className).toMatch(/colorError/)
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
