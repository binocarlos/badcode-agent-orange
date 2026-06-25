// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { renderHook, act } from '@testing-library/react'
import useFileAttachments from './useFileAttachments.js'

function makeFile(name: string, size: number, type = 'text/plain'): File {
  const buffer = new ArrayBuffer(size)
  return new File([buffer], name, { type })
}

// Mock URL.createObjectURL / revokeObjectURL
const mockObjectURLs: string[] = []
beforeEach(() => {
  mockObjectURLs.length = 0
  vi.stubGlobal('URL', {
    ...URL,
    createObjectURL: vi.fn((blob: Blob) => {
      const url = `blob:mock-${mockObjectURLs.length}`
      mockObjectURLs.push(url)
      return url
    }),
    revokeObjectURL: vi.fn(),
  })
})

describe('useFileAttachments', () => {
  it('addFiles adds files with pending status', () => {
    const { result } = renderHook(() => useFileAttachments())

    act(() => {
      result.current.addFiles([makeFile('test.txt', 1024)])
    })

    expect(result.current.attachments.length).toBe(1)
    expect(result.current.attachments[0].uploadState).toBe('pending')
    expect(result.current.attachments[0].file.name).toBe('test.txt')
  })

  it('addFiles generates preview URLs for image files', () => {
    const { result } = renderHook(() => useFileAttachments())

    act(() => {
      result.current.addFiles([makeFile('photo.png', 1024, 'image/png')])
    })

    expect(result.current.attachments[0].previewUrl).toBeDefined()
    expect(result.current.attachments[0].previewUrl).toContain('blob:mock')
  })

  it('addFiles skips files larger than 100MB', () => {
    const { result } = renderHook(() => useFileAttachments())

    const bigFile = makeFile('huge.bin', 101 * 1024 * 1024)
    act(() => {
      result.current.addFiles([bigFile])
    })

    expect(result.current.attachments.length).toBe(0)
  })

  it('removeFile removes by id and revokes preview URL', () => {
    const { result } = renderHook(() => useFileAttachments())

    act(() => {
      result.current.addFiles([makeFile('photo.png', 1024, 'image/png')])
    })

    const id = result.current.attachments[0].id

    act(() => {
      result.current.removeFile(id)
    })

    expect(result.current.attachments.length).toBe(0)
    expect(URL.revokeObjectURL).toHaveBeenCalled()
  })

  it('clear removes all attachments and revokes URLs', () => {
    const { result } = renderHook(() => useFileAttachments())

    act(() => {
      result.current.addFiles([
        makeFile('a.png', 100, 'image/png'),
        makeFile('b.png', 100, 'image/png'),
      ])
    })

    expect(result.current.attachments.length).toBe(2)

    act(() => {
      result.current.clear()
    })

    expect(result.current.attachments.length).toBe(0)
  })

  it('uploadAll returns existing IDs when no pending files', async () => {
    const { result } = renderHook(() => useFileAttachments())

    let ids: string[] = []
    await act(async () => {
      ids = await result.current.uploadAll('sess-1')
    })

    expect(ids).toEqual([])
  })

  it('uploadAll transitions pending → uploaded on success', async () => {
    const mockFetch = vi.fn().mockResolvedValue({
      ok: true,
      json: () => Promise.resolve({ id: 'artifact-123' }),
    })
    vi.stubGlobal('fetch', mockFetch)

    const { result } = renderHook(() => useFileAttachments())

    act(() => {
      result.current.addFiles([makeFile('doc.pdf', 1024)])
    })

    let ids: string[] = []
    await act(async () => {
      ids = await result.current.uploadAll('sess-1')
    })

    expect(ids).toContain('artifact-123')
    expect(mockFetch).toHaveBeenCalledTimes(1)
  })

  it('uploadAll transitions to error state on failure', async () => {
    const mockFetch = vi.fn().mockResolvedValue({
      ok: false,
      status: 500,
    })
    vi.stubGlobal('fetch', mockFetch)

    const { result } = renderHook(() => useFileAttachments())

    act(() => {
      result.current.addFiles([makeFile('fail.txt', 1024)])
    })

    await act(async () => {
      await result.current.uploadAll('sess-1')
    })

    const attachment = result.current.attachments[0]
    expect(attachment.uploadState).toBe('error')
  })
})
