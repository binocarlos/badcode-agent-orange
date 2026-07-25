// @vitest-environment jsdom
// B3: the project-settings page against a stubbed /agent/project-settings.
// The two behaviours worth pinning: bad JSON is reported inline and blocks the
// PUT, and the "0 means…" sentence tracks the value that is actually typed.

import React from 'react'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import ProjectSettingsPage from './ProjectSettingsPage.js'

let originalFetch: typeof globalThis.fetch
let requests: { url: string; method: string; body: unknown }[] = []
let stored: Record<string, unknown>

beforeEach(() => {
  requests = []
  stored = {
    project: 'acme',
    base_image: 'core:1',
    system_prompt: 'be helpful',
    mcp_config: { gmail: { url: 'https://mcp.example/gmail' } },
    attention_channel: {},
    max_concurrent_jobs: 4,
    daily_tokens_soft: 0,
    daily_tokens_hard: 0,
    briefing_max_bytes: 2048,
    snapshot_ttl_days: 30,
    updated_at: 1,
  }
  originalFetch = globalThis.fetch
  globalThis.fetch = vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
    const method = init?.method ?? 'GET'
    const body = init?.body ? JSON.parse(String(init.body)) : undefined
    requests.push({ url: String(url), method, body })
    if (method === 'PUT') {
      stored = { ...stored, ...(body as object), project: 'acme', updated_at: 2 }
    }
    return new Response(JSON.stringify(stored), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  }) as typeof globalThis.fetch
})

afterEach(() => {
  globalThis.fetch = originalFetch
  vi.restoreAllMocks()
})

const puts = () => requests.filter((r) => r.method === 'PUT')

describe('loading', () => {
  it('GETs the settings and fills the fields', async () => {
    render(<ProjectSettingsPage />)
    await waitFor(() => expect(screen.getByLabelText(/base image/i)).toBeInTheDocument())
    expect(requests[0]!.url).toContain('/agent/project-settings')
    expect(screen.getByLabelText(/base image/i)).toHaveValue('core:1')
    expect(screen.getByLabelText(/project system prompt/i)).toHaveValue('be helpful')
    expect(screen.getByLabelText(/MCP servers/i)).toHaveValue(
      JSON.stringify({ gmail: { url: 'https://mcp.example/gmail' } }, null, 2),
    )
  })

  it('surfaces a server error instead of a blank form', async () => {
    globalThis.fetch = vi.fn(
      async () => new Response('project settings not configured', { status: 501 }),
    ) as typeof globalThis.fetch
    render(<ProjectSettingsPage />)
    expect(await screen.findByText(/project settings not configured/i)).toBeInTheDocument()
  })
})

describe('MCP JSON validation', () => {
  it('shows the parse error inline and refuses to save', async () => {
    render(<ProjectSettingsPage />)
    const editor = await screen.findByLabelText(/MCP servers/i)

    // fireEvent.change rather than userEvent.type: `{`, `[` and `>` are key
    // descriptors in user-event, and this field is nothing but those characters.
    fireEvent.change(editor, { target: { value: '{"broken"' } })

    expect(await screen.findByText(/invalid json/i)).toBeInTheDocument()
    // Disabled, so the PUT is unreachable — asserting on the click would only
    // prove that user-event refuses to click disabled buttons.
    expect(screen.getByRole('button', { name: /save settings/i })).toBeDisabled()
    expect(puts()).toHaveLength(0)
  })

  it('rejects a JSON array — the field is a name-keyed map', async () => {
    render(<ProjectSettingsPage />)
    const editor = await screen.findByLabelText(/MCP servers/i)
    fireEvent.change(editor, { target: { value: '[1]' } })
    expect(await screen.findByText(/must be a JSON object/i)).toBeInTheDocument()
  })

  it('clears the error and saves once the JSON is valid again', async () => {
    render(<ProjectSettingsPage />)
    const editor = await screen.findByLabelText(/MCP servers/i)

    fireEvent.change(editor, { target: { value: '{"broken"' } })
    expect(await screen.findByText(/invalid json/i)).toBeInTheDocument()

    fireEvent.change(editor, { target: { value: '{}' } })
    await waitFor(() => expect(screen.queryByText(/invalid json/i)).not.toBeInTheDocument())

    await userEvent.click(screen.getByRole('button', { name: /save settings/i }))
    await waitFor(() => expect(puts()).toHaveLength(1))
    expect(puts()[0]!.body).toMatchObject({ mcp_config: {} })
  })
})

describe('budget/cap fields', () => {
  it('explains what 0 means, per field, as soon as it is typed', async () => {
    render(<ProjectSettingsPage />)
    await screen.findByLabelText(/base image/i)

    // Loaded state: the two token budgets are already 0 = off; TTL is 30 = on.
    expect(screen.getByText(/0 means off — no soft-budget notification/i)).toBeInTheDocument()
    expect(screen.getByText(/0 means off — jobs are never stopped/i)).toBeInTheDocument()
    expect(screen.queryByText(/0 means never/i)).not.toBeInTheDocument()

    fireEvent.change(screen.getByLabelText(/snapshot ttl/i), { target: { value: '0' } })
    expect(await screen.findByText(/0 means never — snapshots are kept forever/i)).toBeInTheDocument()
  })

  it('distinguishes "0 = off" from "0 = the server default"', async () => {
    render(<ProjectSettingsPage />)
    const cap = await screen.findByLabelText(/briefing section cap/i)
    fireEvent.change(cap, { target: { value: '0' } })
    // briefing_max_bytes reads 0 as unset, so the copy must say so — not "off".
    expect(await screen.findByText(/not set.*default of 2048/i)).toBeInTheDocument()
  })

  it('sends the whole object on save, including a zero the human chose', async () => {
    render(<ProjectSettingsPage />)
    fireEvent.change(await screen.findByLabelText(/snapshot ttl/i), { target: { value: '0' } })
    await userEvent.click(screen.getByRole('button', { name: /save settings/i }))

    await waitFor(() => expect(puts()).toHaveLength(1))
    const body = puts()[0]!.body as Record<string, unknown>
    expect(body.snapshot_ttl_days).toBe(0)
    // Whole-object PUT: every field travels, not just the edited one.
    expect(body.base_image).toBe('core:1')
    expect(body.system_prompt).toBe('be helpful')
    // …but never the server-owned ones.
    expect('project' in body).toBe(false)
    expect('updated_at' in body).toBe(false)
  })

  it('blocks the save on a negative budget', async () => {
    render(<ProjectSettingsPage />)
    const soft = await screen.findByLabelText(/daily token budget — soft/i)
    fireEvent.change(soft, { target: { value: '-5' } })
    expect(await screen.findByText(/must not be negative/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /save settings/i })).toBeDisabled()
  })
})

describe('dirty tracking', () => {
  it('disables save until something changes, and again after saving', async () => {
    render(<ProjectSettingsPage />)
    await screen.findByLabelText(/base image/i)
    expect(screen.getByRole('button', { name: /save settings/i })).toBeDisabled()

    await userEvent.type(screen.getByLabelText(/base image/i), '2')
    expect(screen.getByRole('button', { name: /save settings/i })).toBeEnabled()

    await userEvent.click(screen.getByRole('button', { name: /save settings/i }))
    await waitFor(() => expect(puts()).toHaveLength(1))
    await waitFor(() =>
      expect(screen.getByRole('button', { name: /save settings/i })).toBeDisabled(),
    )
  })
})
