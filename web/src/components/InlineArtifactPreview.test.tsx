// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import React from 'react'
import InlineArtifactPreview from './InlineArtifactPreview.js'
import type { ArtifactInfo } from '../types.js'

function codeArtifact(overrides: Partial<ArtifactInfo> = {}): ArtifactInfo {
  return {
    filePath: 'src/main.js',
    fileName: 'main.js',
    label: 'main.js',
    artifactType: 'code',
    source: 'registered',
    status: 'live',
    ...overrides,
  }
}

describe('InlineArtifactPreview', () => {
  beforeEach(() => {
    // workspace-files fallback (no id) 404s — simulating the live window before
    // the artifact has been extracted. The id-addressed download succeeds.
    globalThis.fetch = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.includes('/agent/artifacts/')) {
        return Promise.resolve({ ok: true, status: 200, text: () => Promise.resolve('const x = 1') } as Response)
      }
      return Promise.resolve({ ok: false, status: 404, text: () => Promise.resolve('') } as Response)
    }) as unknown as typeof fetch
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('re-fetches and renders content once the artifact becomes extracted', async () => {
    const live = codeArtifact() // no id, status 'live' → first fetch 404s
    const { rerender } = render(
      <InlineArtifactPreview artifact={live} sessionId="s1" onOpenPreview={() => {}} />
    )

    // The freshly-registered artifact's content isn't available yet — it must not
    // be left showing a permanent error.
    await waitFor(() => {
      expect(screen.queryByText(/const x = 1/)).toBeNull()
    })

    // Extraction completes: the artifact gains a DB id and flips to 'extracted'.
    const extracted = codeArtifact({ id: 'art-1', status: 'extracted' })
    rerender(
      <InlineArtifactPreview artifact={extracted} sessionId="s1" onOpenPreview={() => {}} />
    )

    // It should now re-fetch (via the id-addressed download) and show the code,
    // not stay stuck on the earlier 404 error card.
    await waitFor(() => {
      expect(screen.getByText(/const x = 1/)).toBeTruthy()
    })
    expect(screen.queryByText(/Failed to load/)).toBeNull()
  })
})
