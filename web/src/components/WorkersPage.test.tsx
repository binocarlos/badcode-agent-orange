// @vitest-environment jsdom
// C3: the worker list/editor/job-history/chat surface against a stubbed
// /agent/workers + /agent/sessions.

import React from 'react'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { AgentChatProvider } from '../AgentChatProvider.js'
import WorkersPage from './WorkersPage.js'
import WorkerEditor from './WorkerEditor.js'
import { coerceWorker } from '../workers.js'

let originalFetch: typeof globalThis.fetch
let requests: { url: string; method: string; body: unknown }[] = []
let workers: Record<string, unknown>[]
let sessions: Record<string, unknown>[]
let images: Record<string, unknown>[]

const worker = (name: string, extra: Record<string, unknown> = {}) => ({
  project: 'acme',
  name,
  description: `${name} does things`,
  system_prompt: `you are ${name}`,
  mcp_config: {},
  image: '',
  briefing: null,
  max_instances: 1,
  enabled: true,
  created_at: 1,
  updated_at: 1,
  ...extra,
})

beforeEach(() => {
  requests = []
  workers = [worker('email-answerer'), worker('copy-editor', { enabled: false })]
  sessions = [
    { id: 's1', title: 'Answered a mail', worker: 'email-answerer', created_at: 200, status: 'done' },
    { id: 's2', title: 'Edited copy', worker: 'copy-editor', created_at: 100, status: 'done' },
    { id: 's3', title: 'Plain chat', created_at: 50, status: 'done' },
  ]
  images = [
    { name: 'marketing-tools', version: 2, labels: {}, created_at: 2 },
    { name: 'marketing-tools', version: 1, labels: {}, created_at: 1 },
    { name: 'renderer', version: 1, labels: {}, created_at: 1 },
  ]
  window.history.replaceState(null, '', '/')
  originalFetch = globalThis.fetch
  globalThis.fetch = vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
    const u = String(url)
    const method = init?.method ?? 'GET'
    const body = init?.body ? JSON.parse(String(init.body)) : undefined
    requests.push({ url: u, method, body })
    const json = (v: unknown) =>
      new Response(JSON.stringify(v), { status: 200, headers: { 'Content-Type': 'application/json' } })

    if (u.includes('/agent/sessions')) return json(sessions)
    if (u.includes('/agent/images')) return json({ images, count: images.length })
    if (u.includes('/agent/topologies')) {
      return json({
        topologies: [
          { name: 'solo', version: 'v1', description: 'One worker, no supervision.', questions: [] },
        ],
      })
    }
    if (u.includes('/agent/workers/')) {
      const name = decodeURIComponent(u.split('/agent/workers/')[1]!)
      if (method === 'DELETE') {
        workers = workers.filter((w) => w.name !== name)
        return new Response(null, { status: 204 })
      }
      const stored = { ...worker(name), ...(body as object), name, project: 'acme' }
      const idx = workers.findIndex((w) => w.name === name)
      if (idx === -1) workers.push(stored)
      else workers[idx] = stored
      return json(stored)
    }
    if (u.includes('/agent/workers')) return json({ workers })
    if (u.includes('/agent/session')) return json({ id: 'new-session', status: 'active', workflowId: 'agent' })
    return json({})
  }) as typeof globalThis.fetch
})

afterEach(() => {
  globalThis.fetch = originalFetch
  vi.restoreAllMocks()
  window.history.replaceState(null, '', '/')
})

const puts = () => requests.filter((r) => r.method === 'PUT')

function renderPage(props: Partial<React.ComponentProps<typeof WorkersPage>> = {}) {
  return render(
    <AgentChatProvider config={{ apiBaseUrl: '', models: [{ id: 'm', label: 'M' }] }}>
      <WorkersPage projectId="acme" {...props} />
    </AgentChatProvider>,
  )
}

describe('worker list', () => {
  it('lists the project workers and flags the disabled ones', async () => {
    renderPage()
    expect(await screen.findByText('email-answerer')).toBeInTheDocument()
    expect(screen.getByText('copy-editor')).toBeInTheDocument()
    expect(screen.getByText('disabled')).toBeInTheDocument()
  })

  it('flags frozen workers with a lock badge', async () => {
    workers.push(worker('quality-scorer', { frozen: true }))
    renderPage()
    expect(await screen.findByText('quality-scorer')).toBeInTheDocument()
    expect(screen.getByText('frozen')).toBeInTheDocument()
  })

  it('puts the selected worker in the URL, router-free', async () => {
    renderPage()
    await userEvent.click(await screen.findByText('email-answerer'))
    await waitFor(() => expect(window.location.search).toBe('?worker=email-answerer'))
  })

  it('opens the worker named by the URL on mount', async () => {
    window.history.replaceState(null, '', '/?worker=copy-editor')
    renderPage()
    const prompt = await screen.findByLabelText(/system prompt/i)
    expect(prompt).toHaveValue('you are copy-editor')
  })

  it('leaves the URL alone when the host controls selection', async () => {
    const onSelect = vi.fn()
    renderPage({ selected: 'email-answerer', onSelect })
    await screen.findByLabelText(/system prompt/i)
    await userEvent.click(screen.getByText('copy-editor'))
    expect(onSelect).toHaveBeenCalledWith('copy-editor')
    expect(window.location.search).toBe('')
  })
})

// B4: the image field stops being blind free text — the page loads the
// project's catalogue and offers it — WITHOUT becoming a closed list.
describe('image catalogue (B4)', () => {
  it('offers each catalogued image name once in the picker', async () => {
    renderPage()
    await userEvent.click(await screen.findByText('email-answerer'))
    const field = await screen.findByLabelText('Image')
    await userEvent.click(field)
    const options = await screen.findAllByRole('option')
    expect(options.map((o) => o.textContent)).toEqual(['marketing-tools', 'renderer'])
  })

  it('still accepts an arbitrary registry reference — a suggestion list, not a constraint', async () => {
    renderPage()
    await userEvent.click(await screen.findByText('email-answerer'))
    const field = await screen.findByLabelText('Image')
    await userEvent.type(field, 'ghcr.io/acme/custom:5')
    expect(field).toHaveValue('ghcr.io/acme/custom:5')
  })

  it('leaves the field usable when the host mounts no catalogue route', async () => {
    images = []
    renderPage()
    await userEvent.click(await screen.findByText('email-answerer'))
    const field = await screen.findByLabelText('Image')
    await userEvent.type(field, 'marketing-tools')
    expect(field).toHaveValue('marketing-tools')
  })

  it('lets the host override the options it offers', async () => {
    renderPage({ imageOptions: ['host-supplied'] })
    await userEvent.click(await screen.findByText('email-answerer'))
    await userEvent.click(await screen.findByLabelText('Image'))
    const options = await screen.findAllByRole('option')
    expect(options.map((o) => o.textContent)).toEqual(['host-supplied'])
  })
})

describe('topology onboarding entry (T3)', () => {
  it('offers "start from a topology" prominently when the project is empty', async () => {
    workers = []
    renderPage()
    expect(await screen.findByText(/this project has no workers yet/i)).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: /start from a topology/i }))
    // The flow opens on the catalogue.
    expect(await screen.findByRole('button', { name: /choose solo/i })).toBeInTheDocument()
  })

  it('keeps the flow reachable in a populated project', async () => {
    renderPage()
    // No worker selected: the default panel still carries the entry point.
    expect(await screen.findByText(/select a worker/i)).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: /start from a topology/i }))
    expect(await screen.findByRole('button', { name: /choose solo/i })).toBeInTheDocument()
  })

  it('leaves the flow back to the worker list via Cancel', async () => {
    workers = []
    renderPage()
    // Wait for the settled empty-project panel before clicking: while workers
    // load, the default panel renders its own entry button, which detaches.
    await screen.findByText(/this project has no workers yet/i)
    await userEvent.click(screen.getByRole('button', { name: /start from a topology/i }))
    await userEvent.click(await screen.findByRole('button', { name: /^cancel$/i }))
    expect(await screen.findByText(/this project has no workers yet/i)).toBeInTheDocument()
  })
})

describe('worker editor', () => {
  it('saves the §6.1 fields the walkthrough added', async () => {
    renderPage()
    await userEvent.click(await screen.findByText('email-answerer'))
    await screen.findByLabelText(/system prompt/i)

    await userEvent.type(screen.getByLabelText('Image'), 'marketing-tools:3')
    const maxInstances = screen.getByLabelText(/max instances/i)
    await userEvent.clear(maxInstances)
    await userEvent.type(maxInstances, '4')
    await userEvent.click(screen.getByRole('button', { name: /add selector/i }))
    // Exact label: the row's remove button is "Remove briefing selector 1".
    await userEvent.type(await screen.findByLabelText('Briefing selector 1'), 'kind=house-style')

    await userEvent.click(screen.getByRole('button', { name: /save worker/i }))
    await waitFor(() => expect(puts()).toHaveLength(1))

    expect(puts()[0]!.url).toContain('/agent/workers/email-answerer')
    expect(puts()[0]!.body).toMatchObject({
      image: 'marketing-tools:3',
      max_instances: 4,
      briefing: ['kind=house-style'],
      enabled: true,
    })
  })

  it('freezes a worker from the settings page — the human path of F1', async () => {
    renderPage()
    await userEvent.click(await screen.findByText('email-answerer'))
    await screen.findByLabelText(/system prompt/i)

    await userEvent.click(screen.getByLabelText('Frozen'))
    // The plain sentence, verbatim (10-topology-library §3).
    expect(
      screen.getByText(/Frozen — cannot be changed by other workers\./),
    ).toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: /save worker/i }))
    await waitFor(() => expect(puts()).toHaveLength(1))
    expect(puts()[0]!.body).toMatchObject({ frozen: true })
  })

  it('unfreezes with the same switch, sending an explicit false', async () => {
    workers.push(worker('quality-scorer', { frozen: true }))
    window.history.replaceState(null, '', '/?worker=quality-scorer')
    renderPage()
    await screen.findByLabelText(/system prompt/i)
    expect(screen.getByText(/cannot be changed by other workers/)).toBeInTheDocument()

    await userEvent.click(screen.getByLabelText('Frozen'))
    await userEvent.click(screen.getByRole('button', { name: /save worker/i }))
    await waitFor(() => expect(puts()).toHaveLength(1))
    expect(puts()[0]!.body).toMatchObject({ frozen: false })
  })

  it('describes how the image reference will resolve', async () => {
    renderPage()
    await userEvent.click(await screen.findByText('email-answerer'))
    const image = await screen.findByLabelText('Image')

    await userEvent.type(image, 'marketing-tools')
    expect(await screen.findByText(/floating: resolves to the latest/i)).toBeInTheDocument()

    await userEvent.type(image, ':3')
    expect(await screen.findByText(/pinned: always version 3/i)).toBeInTheDocument()
  })

  it('blocks the save on a malformed image reference', async () => {
    renderPage()
    await userEvent.click(await screen.findByText('email-answerer'))
    await userEvent.type(await screen.findByLabelText('Image'), 'Bad Image')
    expect(await screen.findByText(/must be `name`/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /save worker/i })).toBeDisabled()
    expect(puts()).toHaveLength(0)
  })

  it('blocks the save on unparsable worker MCP JSON', async () => {
    renderPage()
    await userEvent.click(await screen.findByText('email-answerer'))
    const editor = await screen.findByLabelText(/MCP servers \(worker\)/i)
    await userEvent.clear(editor)
    await userEvent.type(editor, '{{oops')
    expect(await screen.findByText(/invalid json/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /save worker/i })).toBeDisabled()
  })

  it('collapses an emptied briefing list back to null, not []', async () => {
    renderPage()
    await userEvent.click(await screen.findByText('email-answerer'))
    await screen.findByLabelText(/system prompt/i)

    await userEvent.click(screen.getByRole('button', { name: /add selector/i }))
    await userEvent.type(await screen.findByLabelText('Briefing selector 1'), 'kind=x')
    await userEvent.click(screen.getByRole('button', { name: /remove briefing selector 1/i }))

    // Make the form dirty in a way that still validates, then save.
    await userEvent.type(screen.getByLabelText('Description'), '!')
    await userEvent.click(screen.getByRole('button', { name: /save worker/i }))
    await waitFor(() => expect(puts()).toHaveLength(1))
    expect((puts()[0]!.body as Record<string, unknown>).briefing).toBeNull()
  })

  it('re-seeds the form when a different worker is selected', async () => {
    renderPage()
    await userEvent.click(await screen.findByText('email-answerer'))
    expect(await screen.findByLabelText(/system prompt/i)).toHaveValue('you are email-answerer')
    await userEvent.click(screen.getByText('copy-editor'))
    await waitFor(() =>
      expect(screen.getByLabelText(/system prompt/i)).toHaveValue('you are copy-editor'),
    )
  })

  it('creates a worker, with the name editable only while new', async () => {
    renderPage()
    await screen.findByText('email-answerer')
    await userEvent.click(screen.getByRole('button', { name: /new worker/i }))

    const name = await screen.findByLabelText('Name')
    expect(name).toBeEnabled()
    await userEvent.type(name, 'Bad Name')
    expect(await screen.findByText(/kebab-case/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /create worker/i })).toBeDisabled()

    await userEvent.clear(name)
    await userEvent.type(name, 'new-worker')
    await userEvent.click(screen.getByRole('button', { name: /create worker/i }))
    await waitFor(() => expect(puts()).toHaveLength(1))
    expect(puts()[0]!.url).toContain('/agent/workers/new-worker')
  })

  it('deletes a worker and clears the selection', async () => {
    renderPage()
    await userEvent.click(await screen.findByText('email-answerer'))
    await screen.findByLabelText(/system prompt/i)
    await userEvent.click(screen.getByRole('button', { name: /^delete$/i }))

    await waitFor(() =>
      expect(requests.some((r) => r.method === 'DELETE' && r.url.includes('email-answerer'))).toBe(true),
    )
    expect(await screen.findByText(/select a worker/i)).toBeInTheDocument()
  })

  it('surfaces the server error verbatim instead of a generic failure', async () => {
    render(
      <WorkerEditor
        worker={coerceWorker(worker('email-answerer'), 'acme')}
        onSave={() => {}}
        error="invalid worker: max_instances must be >= 1, got 0"
      />,
    )
    expect(screen.getByText(/max_instances must be >= 1/i)).toBeInTheDocument()
  })
})

describe('job history', () => {
  it('shows only this worker’s sessions, newest first', async () => {
    renderPage()
    await userEvent.click(await screen.findByText('email-answerer'))
    await userEvent.click(await screen.findByRole('tab', { name: /jobs/i }))

    // The second list on the page is the job history (the first is the worker list).
    const lists = await screen.findAllByRole('list')
    const jobs = lists[lists.length - 1]!
    expect(within(jobs).getByText('Answered a mail')).toBeInTheDocument()
    expect(within(jobs).queryByText('Edited copy')).not.toBeInTheDocument()
    expect(within(jobs).queryByText('Plain chat')).not.toBeInTheDocument()
  })

  it('links each job at its canonical session permalink', async () => {
    renderPage()
    await userEvent.click(await screen.findByText('email-answerer'))
    await userEvent.click(await screen.findByRole('tab', { name: /jobs/i }))

    const link = await screen.findByRole('link', { name: /answered a mail/i })
    expect(link).toHaveAttribute('href', '/p/acme/s/s1')
  })

  it('hands the click to the host when onOpenSession is supplied', async () => {
    const onOpenSession = vi.fn()
    renderPage({ onOpenSession })
    await userEvent.click(await screen.findByText('email-answerer'))
    await userEvent.click(await screen.findByRole('tab', { name: /jobs/i }))
    await userEvent.click(await screen.findByText('Answered a mail'))
    expect(onOpenSession).toHaveBeenCalledWith('s1')
  })

  it('says so when a worker has no jobs', async () => {
    sessions = []
    renderPage()
    await userEvent.click(await screen.findByText('email-answerer'))
    await userEvent.click(await screen.findByRole('tab', { name: /jobs/i }))
    expect(await screen.findByText(/no jobs yet for this worker/i)).toBeInTheDocument()
  })
})

describe('chat with worker', () => {
  it('starts a session tagged with the worker, and never sends its prompt', async () => {
    renderPage()
    await userEvent.click(await screen.findByText('email-answerer'))
    await userEvent.click(await screen.findByRole('tab', { name: /chat/i }))
    await userEvent.click(await screen.findByRole('button', { name: /chat with email-answerer/i }))

    await waitFor(() =>
      expect(requests.some((r) => r.method === 'POST' && r.url.includes('/agent/session'))).toBe(true),
    )
    const create = requests.find((r) => r.method === 'POST' && r.url.includes('/agent/session'))!
    const body = create.body as Record<string, unknown>
    expect(body.worker).toBe('email-answerer')
    expect(body.customer).toBe('acme')
    // Composition is the server's job: the browser must not supply beliefs.
    expect(body.systemPrompt).toBeUndefined()
  })

  it('reuses the shared chat rather than rendering a second one', async () => {
    renderPage()
    await userEvent.click(await screen.findByText('email-answerer'))
    await userEvent.click(await screen.findByRole('tab', { name: /chat/i }))
    await userEvent.click(await screen.findByRole('button', { name: /chat with email-answerer/i }))

    // The ordinary AgentChat composer appears once the session exists — proof
    // the panel delegates instead of owning a message list of its own.
    expect(await screen.findByPlaceholderText(/type a message/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /send/i })).toBeInTheDocument()
  })
})
