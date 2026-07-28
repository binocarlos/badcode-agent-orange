// @vitest-environment jsdom
// W6 (doc 21 §4.2's GitHub transplants, §5-M5): the lineage's two
// waterline-dependent features, rendered.
//
// The arithmetic is pinned in lineageWaterline.test.ts; what is pinned here is
// that the component uses the ONE watermark (never a second mark of its own),
// that the per-revision diffs survive underneath the cumulative one, that the
// Viewed mark clears when the prompt changes again, and that the cumulative
// diff's fold obeys the reduced-motion gate in both directions.

import React from 'react'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, it, expect, afterEach, beforeEach, vi } from 'vitest'
import { ThemeProvider, createTheme } from '@mui/material/styles'
import WorkerLineage from './WorkerLineage.js'
import { watermarkKey } from '../watermark.js'
import { viewedKey } from '../lineageWaterline.js'

const WORKER = 'email-answerer'
const PROJECT = 'acme'

const write = (id: string, at: number, prompt: string) => ({
  id,
  project: PROJECT,
  actor_worker: 'email-reviewer',
  actor_session: `sess-${id}`,
  action: 'worker_prompt_write',
  payload: { name: WORKER, system_prompt: prompt },
  rationale: `because ${id}`,
  created_at: at,
})

const V1 = 'answer email\nbe thorough'
const V2 = 'answer email\nbe brief'
const V3 = 'answer email\nbe brief\nquote the ticket reference'

let served: ReturnType<typeof write>[] = []
let originalFetch: typeof globalThis.fetch

/** Mock the media query the one gate reads (W3's pattern). */
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

beforeEach(() => {
  localStorage.clear()
  setReducedMotion(false)
  served = [write('c3', 3000, V3), write('c2', 2000, V2), write('c1', 1000, V1)]
  originalFetch = globalThis.fetch
  globalThis.fetch = vi.fn(async () =>
    new Response(JSON.stringify({ events: served }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }),
  ) as unknown as typeof globalThis.fetch
})

afterEach(() => {
  globalThis.fetch = originalFetch
  vi.restoreAllMocks()
})

function renderLineage(props: Record<string, unknown> = {}) {
  return render(
    <ThemeProvider theme={createTheme({ palette: { mode: 'light' } })}>
      <WorkerLineage workerName={WORKER} projectId={PROJECT} {...props} />
    </ThemeProvider>,
  )
}

describe('the cumulative diff since the watermark', () => {
  it('appears when more than one rewrite landed since the operator last looked', async () => {
    renderLineage({ watermarkMs: 1500 })
    const banner = await screen.findByTestId('lineage-cumulative')
    expect(within(banner).getByText('2 rewrites since you last looked · v1 → v3')).toBeTruthy()
    // Open by default, and it is the v1 → v3 diff, not v2 → v3.
    expect(within(banner).getByText('+quote the ticket reference')).toBeTruthy()
    expect(within(banner).getByText('-be thorough')).toBeTruthy()
  })

  it('leaves the per-revision diffs in place underneath it', async () => {
    renderLineage({ watermarkMs: 1500 })
    await screen.findByTestId('lineage-cumulative')
    // Every rewrite still has its own row, its own rationale and its own diff.
    expect(screen.getByText('because c3')).toBeTruthy()
    expect(screen.getByText('because c2')).toBeTruthy()
    expect(screen.getAllByText(/^[▾▸] \+\d+ −\d+/).length).toBeGreaterThan(1)
  })

  it('does not appear for one rewrite, or for an operator who never looked', async () => {
    const { unmount } = renderLineage({ watermarkMs: 2500 })
    await screen.findByText('because c3')
    expect(screen.queryByTestId('lineage-cumulative')).toBeNull()
    unmount()

    renderLineage({ watermarkMs: 0 })
    await screen.findByText('because c3')
    expect(screen.queryByTestId('lineage-cumulative')).toBeNull()
  })

  it('reads the Desk watermark rather than keeping a second mark of its own', async () => {
    // §4.2's framing fact: one integer per operator, four renderings. If this
    // component ever grows its own key, this test is what fails.
    localStorage.setItem(watermarkKey('desk', PROJECT), '1500')
    renderLineage()
    await screen.findByTestId('lineage-cumulative')
    expect(screen.getByText('2 rewrites since you last looked · v1 → v3')).toBeTruthy()
  })

  it('folds with a transition normally and snaps under reduced motion', async () => {
    const { unmount } = renderLineage({ watermarkMs: 1500 })
    const banner = await screen.findByTestId('lineage-cumulative')
    expect(getComputedStyle(banner.querySelector('details > div:last-of-type')!).transition).toContain(
      'grid-template-rows',
    )
    unmount()

    setReducedMotion(true)
    renderLineage({ watermarkMs: 1500 })
    const reducedBanner = await screen.findByTestId('lineage-cumulative')
    expect(
      getComputedStyle(reducedBanner.querySelector('details > div:last-of-type')!).transition,
    ).toBe('none')
  })
})

describe('Viewed, and its auto-invalidation', () => {
  it('marks a version viewed, and remembers it', async () => {
    const user = userEvent.setup()
    renderLineage({ watermarkMs: 1500 })
    await screen.findByText('because c3')
    await user.click(screen.getByTestId('viewed-toggle-c2'))
    expect(screen.getByTestId('viewed-toggle-c2').textContent).toBe('✓ viewed')
    expect(screen.getByTestId('viewed-toggle-c3').textContent).toBe('mark viewed')
    expect(JSON.parse(localStorage.getItem(viewedKey(PROJECT, WORKER))!)).toEqual({
      headEventId: 'c3',
      viewed: ['c2'],
    })
  })

  it('CLEARS the mark when the prompt changes again', async () => {
    // The lie this prevents: an operator returns, sees "✓ viewed" on a worker
    // whose prompt was rewritten since, and skips reading the change.
    localStorage.setItem(
      viewedKey(PROJECT, WORKER),
      JSON.stringify({ headEventId: 'c2', viewed: ['c2', 'c1'] }),
    )
    renderLineage({ watermarkMs: 1500 })
    await screen.findByText('because c3')
    // c3 is the head now, so every mark made against c2 is stale.
    expect(screen.getByTestId('viewed-toggle-c2').textContent).toBe('mark viewed')
    expect(screen.getByTestId('viewed-toggle-c1').textContent).toBe('mark viewed')
  })

  it('keeps the mark when the head is unchanged', async () => {
    localStorage.setItem(
      viewedKey(PROJECT, WORKER),
      JSON.stringify({ headEventId: 'c3', viewed: ['c2'] }),
    )
    renderLineage({ watermarkMs: 1500 })
    await waitFor(() =>
      expect(screen.getByTestId('viewed-toggle-c2').textContent).toBe('✓ viewed'),
    )
  })
})
