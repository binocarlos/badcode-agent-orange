// @vitest-environment jsdom
// BR1 — the bench viewer: file in, ranked table out, with the §7.3 editorial
// rules asserted as behaviour (banner not dismissable, churn muted and last,
// spread always shown, dedupe noted).

import React from 'react'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, it, expect } from 'vitest'
import fixture from '../__fixtures__/actor-critic-vs-sham-vs-solo.report.json'
import BenchReportView, { DEDUPE_NOTE, TIER_A_BANNER } from './BenchReportView.js'
import { parseBenchReport } from '../benchreport.js'

const reportFile = (body: unknown = fixture, name = 'report.json') =>
  new File([JSON.stringify(body)], name, { type: 'application/json' })

describe('BenchReportView', () => {
  it('shows the Tier A banner before anything is loaded, with no way to dismiss it', () => {
    render(<BenchReportView />)
    const banner = screen.getByTestId('bench-tier-a-banner')
    expect(banner).toHaveTextContent(TIER_A_BANNER)
    expect(within(banner).queryByRole('button')).toBeNull()
    expect(screen.getByText(/No report loaded/i)).toBeInTheDocument()
  })

  it('renders the ranked table from a dropped file', async () => {
    const user = userEvent.setup()
    render(<BenchReportView />)
    await user.upload(screen.getByTestId('bench-file-input'), reportFile())

    await waitFor(() => expect(screen.getByTestId('bench-table')).toBeInTheDocument())
    const first = screen.getByTestId('bench-row-actor-critic')
    expect(within(first).getByText('actor-critic@v1')).toBeInTheDocument()
    // Spread is rendered, never averaged away.
    expect(first).toHaveTextContent('0.5 ±0')
    // The banner survives loading.
    expect(screen.getByTestId('bench-tier-a-banner')).toHaveTextContent(TIER_A_BANNER)
  })

  it('puts churn last, labelled churn, and never first', () => {
    render(<BenchReportView report={parseBenchReport(fixture)} />)
    const headers = screen.getAllByRole('columnheader')
    expect(headers[headers.length - 1]).toHaveTextContent(/churn/)
    expect(headers[0]).not.toHaveTextContent(/churn/)
    // The outcome column leads the metrics and is named as such.
    expect(headers[3]).toHaveTextContent('headline-rule')
    expect(headers[3]).toHaveTextContent('outcome')
  })

  it('notes the identical-rewrite dedupe beside the churn number', () => {
    render(<BenchReportView report={parseBenchReport(fixture)} />)
    expect(screen.getByTestId('bench-dedupe-note')).toHaveTextContent(DEDUPE_NOTE)
    expect(screen.getByTestId('bench-row-actor-critic')).toHaveTextContent(
      '4 rewrites · 1 distinct',
    )
    expect(screen.getByTestId('bench-row-solo')).toHaveTextContent('no rewrites')
  })

  it('does not raise the spread alarm on a clean mock report', () => {
    render(<BenchReportView report={parseBenchReport(fixture)} />)
    expect(screen.queryByTestId('bench-spread-alarm')).toBeNull()
  })

  it('raises the spread alarm when a mock arm moved', () => {
    const noisy = JSON.parse(JSON.stringify(fixture)) as typeof fixture
    ;(noisy.summaries[0].metrics as Record<string, { spread: number }>)[
      'prop:headline-rule'
    ].spread = 0.5
    render(<BenchReportView report={parseBenchReport(noisy)} />)
    expect(screen.getByTestId('bench-spread-alarm')).toHaveTextContent(/alarm, not a data point/)
  })

  it('names the problem when the file is not a report', async () => {
    const user = userEvent.setup()
    render(<BenchReportView />)
    await user.upload(screen.getByTestId('bench-file-input'), reportFile({ schema: 'other@1' }))
    await waitFor(() =>
      expect(screen.getByTestId('bench-error')).toHaveTextContent(/not a comparison report/),
    )
    expect(screen.queryByTestId('bench-table')).toBeNull()
  })
})
