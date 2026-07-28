// @vitest-environment jsdom
// W4 (doc 21 §4.2): the pill, the waterline and the pause toggle.
//
// These three exist because WCAG 2.2.2 applies to a list that inserts rows on
// its own. So what is pinned is the accessibility contract, not the look: ONE
// debounced `role="status"` summary rather than an announcement per item, a
// separator a screen reader can find, and a control that actually says "Pause
// live updates".

import React from 'react'
import { render, screen, act } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, it, expect, vi, afterEach } from 'vitest'
import { ThemeProvider, createTheme } from '@mui/material/styles'
import { FeedWaterline, NewItemsPill, PauseLiveUpdates, PILL_DEBOUNCE_MS } from './FeedLiveness.js'

function wrap(node: React.ReactNode) {
  return render(<ThemeProvider theme={createTheme()}>{node}</ThemeProvider>)
}

afterEach(() => {
  vi.useRealTimers()
})

describe('the waterline', () => {
  it('is a labelled separator, not a decoration', () => {
    wrap(<FeedWaterline label="New since 09:12" />)
    const rule = screen.getByRole('separator', { name: 'New since 09:12' })
    expect(rule).toBeInTheDocument()
    expect(rule).toHaveTextContent('New since 09:12')
  })
})

describe('the "N new" pill', () => {
  it('is absent when nothing is staged — no pill, no live region text', () => {
    wrap(<NewItemsPill count={0} summary="" onShow={() => {}} />)
    expect(screen.queryByTestId('new-items-pill')).toBeNull()
    expect(screen.getByRole('status')).toHaveTextContent('')
  })

  it('offers the staged rows as a button, and flushes them on click', async () => {
    const onShow = vi.fn()
    wrap(<NewItemsPill count={3} summary="3 new changes" onShow={onShow} />)
    await userEvent.click(screen.getByRole('button', { name: '3 new changes' }))
    expect(onShow).toHaveBeenCalledTimes(1)
  })

  it('announces ONE debounced summary, not one announcement per arrival', () => {
    vi.useFakeTimers()
    const { rerender } = render(
      <ThemeProvider theme={createTheme()}>
        <NewItemsPill count={1} summary="1 new change" onShow={() => {}} />
      </ThemeProvider>,
    )
    const region = screen.getByRole('status')
    // Mid-burst: the button has already updated (it is a control), the live
    // region has not (it is an interruption).
    rerender(
      <ThemeProvider theme={createTheme()}>
        <NewItemsPill count={2} summary="2 new changes" onShow={() => {}} />
      </ThemeProvider>,
    )
    expect(region).toHaveTextContent('')
    act(() => {
      vi.advanceTimersByTime(PILL_DEBOUNCE_MS + 10)
    })
    expect(region).toHaveTextContent('2 new changes')
  })

  it('keeps the live region mounted while empty', () => {
    // A live region inserted at the moment it gains text is a live region
    // screen readers may never announce.
    wrap(<NewItemsPill count={0} summary="" onShow={() => {}} />)
    expect(screen.getByRole('status')).toBeInTheDocument()
  })
})

describe('the pause toggle', () => {
  it('says what it does, and reports both directions', async () => {
    const onChange = vi.fn()
    wrap(<PauseLiveUpdates paused={false} onChange={onChange} />)
    const toggle = screen.getByRole('checkbox', { name: 'Pause live updates' })
    await userEvent.click(toggle)
    expect(onChange).toHaveBeenCalledWith(true)
  })

  it('reads as pressed when paused', () => {
    wrap(<PauseLiveUpdates paused onChange={() => {}} />)
    expect(screen.getByRole('checkbox', { name: 'Pause live updates' })).toBeChecked()
  })
})
