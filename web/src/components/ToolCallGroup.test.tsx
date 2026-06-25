import { describe, it, expect } from 'vitest'
import {
  tryParseImageOutput,
  isScreenshotToolCall,
  isImageReadToolCall,
  isImageToolCall,
} from './ToolCallGroup.js'
import type { ToolCallInfo } from '../types.js'

function tc(name: string, input?: Record<string, unknown>): ToolCallInfo {
  return {
    id: 'tc-1',
    name,
    input: input || {},
    status: 'complete',
    output: '',
  }
}

// --- tryParseImageOutput ---

describe('tryParseImageOutput', () => {
  it('extracts from direct format: {"type":"image","file":{"base64":"...","mimeType":"..."}}', () => {
    const output = JSON.stringify({
      type: 'image',
      file: { base64: 'abc123', mimeType: 'image/png' },
    })
    const result = tryParseImageOutput(output)
    expect(result).toEqual({ base64: 'abc123', mimeType: 'image/png' })
  })

  it('extracts from Anthropic format: {"type":"image","source":{"data":"...","media_type":"..."}}', () => {
    const output = JSON.stringify({
      type: 'image',
      source: { data: 'def456', media_type: 'image/jpeg' },
    })
    const result = tryParseImageOutput(output)
    expect(result).toEqual({ base64: 'def456', mimeType: 'image/jpeg' })
  })

  it('extracts from array format: [{"type":"image","file":{"base64":"..."}}]', () => {
    const output = JSON.stringify([
      { type: 'text', text: 'Some text' },
      { type: 'image', file: { base64: 'img-data', mimeType: 'image/gif' } },
    ])
    const result = tryParseImageOutput(output)
    expect(result).toEqual({ base64: 'img-data', mimeType: 'image/gif' })
  })

  it('extracts from content array format: {"content":[{"type":"image","source":{"data":"..."}}]}', () => {
    const output = JSON.stringify({
      content: [
        { type: 'image', source: { data: 'content-img', media_type: 'image/webp' } },
      ],
    })
    const result = tryParseImageOutput(output)
    expect(result).toEqual({ base64: 'content-img', mimeType: 'image/webp' })
  })

  it('returns null for non-image JSON', () => {
    const output = JSON.stringify({ type: 'text', text: 'hello' })
    expect(tryParseImageOutput(output)).toBeNull()
  })

  it('returns null for invalid JSON', () => {
    expect(tryParseImageOutput('not json {')).toBeNull()
  })

  it('returns null for oversized input (>7MB) without parsing', () => {
    const hugeOutput = 'x'.repeat(8_000_000)
    expect(tryParseImageOutput(hugeOutput)).toBeNull()
  })

  it('defaults mimeType to image/png when not specified', () => {
    const output = JSON.stringify({
      type: 'image',
      file: { base64: 'no-mime' },
    })
    const result = tryParseImageOutput(output)
    expect(result).toEqual({ base64: 'no-mime', mimeType: 'image/png' })
  })
})

// --- isScreenshotToolCall ---

describe('isScreenshotToolCall', () => {
  it('returns true for bare screenshot_url', () => {
    expect(isScreenshotToolCall(tc('screenshot_url'))).toBe(true)
  })

  it('returns true for MCP-prefixed screenshot_url', () => {
    expect(isScreenshotToolCall(tc('mcp__ui__screenshot_url'))).toBe(true)
  })

  it('returns false for other tools', () => {
    expect(isScreenshotToolCall(tc('Bash'))).toBe(false)
    expect(isScreenshotToolCall(tc('Read'))).toBe(false)
  })
})

// --- isImageReadToolCall ---

describe('isImageReadToolCall', () => {
  it('returns true for Read with image file path', () => {
    expect(isImageReadToolCall(tc('Read', { file_path: '/workspace/chart.png' }))).toBe(true)
    expect(isImageReadToolCall(tc('Read', { file_path: '/tmp/photo.jpg' }))).toBe(true)
    expect(isImageReadToolCall(tc('Read', { file_path: 'image.webp' }))).toBe(true)
  })

  it('returns false for Read with non-image file path', () => {
    expect(isImageReadToolCall(tc('Read', { file_path: '/workspace/code.ts' }))).toBe(false)
    expect(isImageReadToolCall(tc('Read', { file_path: '/data.json' }))).toBe(false)
  })

  it('returns false for non-Read tools', () => {
    expect(isImageReadToolCall(tc('Bash', { file_path: 'image.png' }))).toBe(false)
    expect(isImageReadToolCall(tc('Write', { file_path: 'image.png' }))).toBe(false)
  })
})

// --- isImageToolCall ---

describe('isImageToolCall', () => {
  it('returns true for screenshot_url', () => {
    expect(isImageToolCall(tc('screenshot_url'))).toBe(true)
    expect(isImageToolCall(tc('mcp__ui__screenshot_url'))).toBe(true)
  })

  it('returns true for view_image', () => {
    expect(isImageToolCall(tc('view_image'))).toBe(true)
    expect(isImageToolCall(tc('mcp__ui__view_image'))).toBe(true)
  })

  it('returns true for Read on image paths', () => {
    expect(isImageToolCall(tc('Read', { file_path: '/workspace/chart.png' }))).toBe(true)
  })

  it('returns false for non-image tools', () => {
    expect(isImageToolCall(tc('Bash'))).toBe(false)
    expect(isImageToolCall(tc('Write'))).toBe(false)
    expect(isImageToolCall(tc('Read', { file_path: '/code.ts' }))).toBe(false)
  })
})
