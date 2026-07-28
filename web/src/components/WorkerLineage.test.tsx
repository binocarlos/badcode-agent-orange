// @vitest-environment jsdom
// LN1: the Lineage tab (design §7.1) — the config log filtered to one worker,
// folding the Configuration tab to a past version, and restoring it forward.

import React from 'react'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { AgentChatProvider } from '../AgentChatProvider.js'
import WorkersPage from './WorkersPage.js'

let originalFetch: typeof globalThis.fetch
let requests: { url: string; method: string; body: unknown }[] = []

const worker = {
  project: 'acme',
  name: 'email-answerer',
  description: 'answers mail',
  system_prompt: 'v3 — the live prompt',
  mcp_config: {},
  image: '',
  briefing: null,
  max_instances: 1,
  enabled: true,
  created_at: 1,
  updated_at: 1,
}

const configEvents = [
  {
    id: 'cfg-3',
    project: 'acme',
    actor_worker: 'email-reviewer',
    actor_session: 'sess-3',
    action: 'worker_prompt_write',
    payload: { name: 'email-answerer', system_prompt: 'v3 — the live prompt' },
    rationale: 'narrowing yesterday’s rule',
    created_at: 3000,
  },
  {
    id: 'cfg-2',
    project: 'acme',
    actor_worker: '',
    actor_session: '',
    action: 'worker_prompt_write',
    payload: { name: 'email-answerer', system_prompt: 'v2 — quote the ticket reference' },
    rationale: 'answers kept omitting the reference',
    created_at: 2000,
  },
  {
    id: 'other',
    project: 'acme',
    actor_worker: '',
    actor_session: '',
    action: 'worker_prompt_write',
    payload: { name: 'copy-editor', system_prompt: 'not this one' },
    rationale: 'belongs to another worker',
    created_at: 2500,
  },
  {
    id: 'cfg-1',
    project: 'acme',
    actor_worker: '',
    actor_session: '',
    action: 'worker_create',
    payload: { name: 'email-answerer', system_prompt: 'v1 — the original' },
    rationale: 'hired',
    created_at: 1000,
  },
]

beforeEach(() => {
  requests = []
  window.history.replaceState(null, '', '/?worker=email-answerer')
  originalFetch = globalThis.fetch
  globalThis.fetch = vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
    const u = String(url)
    const method = init?.method ?? 'GET'
    const body = init?.body ? JSON.parse(String(init.body)) : undefined
    requests.push({ url: u, method, body })
    const json = (v: unknown) =>
      new Response(JSON.stringify(v), { status: 200, headers: { 'Content-Type': 'application/json' } })
    if (u.includes('/agent/config-events')) return json({ config_events: configEvents })
    if (u.includes('/agent/images')) return json({ images: [], count: 0 })
    if (u.includes('/agent/sessions')) return json([])
    if (u.includes('/agent/workers/')) return json({ ...worker, ...(body as object) })
    if (u.includes('/agent/workers')) return json({ workers: [worker] })
    return json({})
  }) as typeof globalThis.fetch
})

afterEach(() => {
  globalThis.fetch = originalFetch
  vi.restoreAllMocks()
  window.history.replaceState(null, '', '/')
})

function renderPage() {
  return render(
    <AgentChatProvider config={{ apiBaseUrl: '', models: [{ id: 'm', label: 'M' }] }}>
      <WorkersPage projectId="acme" enableChat={false} />
    </AgentChatProvider>,
  )
}

const openLineage = async () => {
  renderPage()
  await screen.findByLabelText(/system prompt/i)
  await userEvent.click(screen.getByRole('tab', { name: 'Lineage' }))
}

describe('the lineage tab', () => {
  it('shows only this worker’s history, numbered, with its rationales', async () => {
    await openLineage()
    expect(await screen.findByText('v3')).toBeInTheDocument()
    expect(screen.getByText('v2')).toBeInTheDocument()
    expect(screen.getByText('v1')).toBeInTheDocument()
    expect(screen.getByText('narrowing yesterday’s rule')).toBeInTheDocument()
    expect(screen.queryByText('belongs to another worker')).not.toBeInTheDocument()
    expect(screen.getByText('2 rewrites · 3 distinct')).toBeInTheDocument()
  })

  it('names the worker that decided a rewrite, and the human who did not', async () => {
    await openLineage()
    expect(await screen.findByText(/by email-reviewer/)).toBeInTheDocument()
    expect(screen.getAllByText(/by a human \(UI or API\)/).length).toBeGreaterThan(0)
  })
})

describe('fold to a version', () => {
  it('shows the old prompt read-only, banner-marked as history', async () => {
    await openLineage()
    await userEvent.click(await screen.findByText('v2'))
    const banner = await screen.findByTestId('version-banner')
    expect(banner.textContent).toContain('Viewing v2 as of')
    expect(banner.textContent).toContain('this is history, not the live prompt')
    expect(screen.getByText('v2 — quote the ticket reference')).toBeInTheDocument()
    // Read-only: no editable prompt while folded.
    expect(screen.queryByLabelText(/system prompt/i)).not.toBeInTheDocument()
  })

  it('restores forward: the editor is pre-filled with the old text and a reason', async () => {
    await openLineage()
    await userEvent.click(await screen.findByText('v2'))
    await userEvent.click(await screen.findByRole('button', { name: 'Restore this version' }))

    const prompt = await screen.findByLabelText(/system prompt/i)
    expect(prompt).toHaveValue('v2 — quote the ticket reference')
    expect(screen.getByLabelText('Why?')).toHaveValue(
      'Restoring v2 of the prompt (config event cfg-2).',
    )

    await userEvent.click(screen.getByRole('button', { name: /save/i }))
    await waitFor(() => expect(requests.some((r) => r.method === 'PUT')).toBe(true))
    const put = requests.find((r) => r.method === 'PUT')!
    expect(put.url).toContain('/agent/workers/email-answerer')
    expect((put.body as { system_prompt: string }).system_prompt).toBe(
      'v2 — quote the ticket reference',
    )
    expect((put.body as { rationale: string }).rationale).toContain('cfg-2')
  })

  it('goes back to the live prompt without writing anything', async () => {
    await openLineage()
    await userEvent.click(await screen.findByText('v2'))
    await userEvent.click(await screen.findByRole('button', { name: 'Back to the live prompt' }))
    expect(await screen.findByLabelText(/system prompt/i)).toHaveValue('v3 — the live prompt')
    expect(requests.some((r) => r.method === 'PUT')).toBe(false)
  })
})
