// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import React from 'react'
import ArtifactPreviewDialog from './ArtifactPreviewDialog.js'
import type { ArtifactInfo } from '../types.js'

// A 1x1 transparent PNG data URL — stands in for an inline screenshot result.
const PNG_DATA_URL =
  'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=='

describe('ArtifactPreviewDialog', () => {
  beforeEach(() => {
    // If the dialog tries to fetch, simulate the 404 the user saw for inline
    // screenshots (no backing file/artifact).
    globalThis.fetch = vi.fn(() =>
      Promise.resolve({ ok: false, status: 404, text: () => Promise.resolve(''), blob: () => Promise.resolve(new Blob()) } as unknown as Response)
    ) as unknown as typeof fetch
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('renders an inline data: URL image without fetching (no 404)', async () => {
    const artifact: ArtifactInfo = {
      filePath: 'screenshot.png',
      fileName: 'screenshot.png',
      label: 'screenshot.png',
      artifactType: 'image',
      source: 'auto',
      status: 'live',
      downloadUrl: PNG_DATA_URL,
    }

    render(<ArtifactPreviewDialog artifact={artifact} sessionId="s1" onClose={() => {}} />)

    // The image should render straight from the data URL; no error card.
    await waitFor(() => {
      const img = screen.getByRole('img') as HTMLImageElement
      expect(img.src).toBe(PNG_DATA_URL)
    })
    expect(screen.queryByText(/Failed to load/)).toBeNull()
    expect(globalThis.fetch).not.toHaveBeenCalled()
  })
})
