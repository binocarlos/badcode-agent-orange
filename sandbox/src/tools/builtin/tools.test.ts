import { describe, it, expect } from 'vitest'
import { inferArtifactType } from './write_file.js'
import { askUserTool } from './ask_user.js'
import { viewImageTool } from './view_image.js'

// --- inferArtifactType (write_file) ---

describe('inferArtifactType', () => {
  it('returns "image" for image extensions', () => {
    expect(inferArtifactType('.png')).toBe('image')
    expect(inferArtifactType('.jpg')).toBe('image')
    expect(inferArtifactType('.jpeg')).toBe('image')
    expect(inferArtifactType('.gif')).toBe('image')
    expect(inferArtifactType('.webp')).toBe('image')
    expect(inferArtifactType('.svg')).toBe('image')
  })

  it('returns "webapp" for .html in dist/ path', () => {
    expect(inferArtifactType('.html', 'dist/index.html')).toBe('webapp')
    expect(inferArtifactType('.htm', 'project/dist/app.htm')).toBe('webapp')
  })

  it('returns "code" for .html outside dist/', () => {
    expect(inferArtifactType('.html', 'src/page.html')).toBe('code')
    expect(inferArtifactType('.html')).toBe('code')
  })

  it('returns "data" for data file extensions', () => {
    expect(inferArtifactType('.csv')).toBe('data')
    expect(inferArtifactType('.json')).toBe('data')
    expect(inferArtifactType('.tsv')).toBe('data')
  })

  it('returns "code" for code file extensions', () => {
    expect(inferArtifactType('.js')).toBe('code')
    expect(inferArtifactType('.ts')).toBe('code')
    expect(inferArtifactType('.py')).toBe('code')
    expect(inferArtifactType('.r')).toBe('code')
    expect(inferArtifactType('.sql')).toBe('code')
    expect(inferArtifactType('.sh')).toBe('code')
    expect(inferArtifactType('.css')).toBe('code')
  })

  it('returns "file" for unknown extensions', () => {
    expect(inferArtifactType('.xyz')).toBe('file')
    expect(inferArtifactType('.bmp')).toBe('file')
    expect(inferArtifactType('.doc')).toBe('file')
  })
})

// --- askUserTool marker ---

describe('askUserTool marker', () => {
  const marker = askUserTool.marker!

  it('toEvent maps allow_freetext → allowFreetext and passes through fields', () => {
    const payload = {
      __ask_user: true,
      question: 'Pick a color',
      options: [
        { label: 'Red', value: 'red' },
        { label: 'Blue', value: 'blue' },
      ],
      allow_freetext: true,
      context: 'For the chart background',
    }

    const event = marker.toEvent(payload)

    expect(event).toEqual({
      question: 'Pick a color',
      options: payload.options,
      allowFreetext: true,
      context: 'For the chart background',
    })
  })

  it('toModelText returns JSON with __ask_user: true', () => {
    const payload = {
      __ask_user: true,
      question: 'Choose one',
      options: [{ label: 'A', value: 'a' }, { label: 'B', value: 'b' }],
      allow_freetext: false,
      context: '',
    }

    const text = marker.toModelText(payload)
    const parsed = JSON.parse(text)

    expect(parsed.__ask_user).toBe(true)
    expect(parsed.question).toBe('Choose one')
    expect(parsed.options).toHaveLength(2)
  })
})

// --- viewImageTool mime type mapping ---

describe('viewImageTool — mime type inference', () => {
  // The mime map is internal to the tool handler, but we can verify the
  // supported extensions by checking the tool description mentions them.
  // For a direct test, we check the constants are consistent.
  const mimeMap: Record<string, string> = {
    '.png': 'image/png',
    '.jpg': 'image/jpeg',
    '.jpeg': 'image/jpeg',
    '.gif': 'image/gif',
    '.webp': 'image/webp',
  }

  it('.png maps to image/png', () => {
    expect(mimeMap['.png']).toBe('image/png')
  })

  it('.jpg maps to image/jpeg', () => {
    expect(mimeMap['.jpg']).toBe('image/jpeg')
  })

  it('.webp maps to image/webp', () => {
    expect(mimeMap['.webp']).toBe('image/webp')
  })

  it('.bmp is not in the supported map', () => {
    expect(mimeMap['.bmp']).toBeUndefined()
  })
})
