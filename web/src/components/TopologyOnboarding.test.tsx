// @vitest-environment jsdom
// T3: the start-from-a-topology flow against a stubbed /agent/topologies.
// The contract under test: required questions gate the preview, the preview
// gates the apply (`applicable: false` disables it, visibly), apply sends the
// resolved answers, and a 409 is rendered exactly as the server phrased it.

import React from 'react'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import TopologyOnboarding from './TopologyOnboarding.js'

let originalFetch: typeof globalThis.fetch
let requests: { url: string; method: string; body: unknown }[] = []
let previewResponse: Record<string, unknown>
let applyResponse: { status: number; body: unknown }

const catalogue = [
  {
    name: 'actor-critic',
    version: 'v1',
    description: 'An actor does the work; a critic rewrites its prompt.',
    questions: [
      { id: 'domain', prompt: 'What domain does the actor work in?', type: 'string', required: true },
      { id: 'freeze-critic', prompt: 'Freeze the critic?', type: 'bool', default: true },
      {
        id: 'cadence',
        prompt: 'How often should the actor run?',
        type: 'choice',
        choices: ['daily', 'weekly'],
        default: 'daily',
      },
    ],
  },
  {
    name: 'solo',
    version: 'v1',
    description: 'One worker, no supervision.',
    questions: [],
  },
]

const applicablePreview = () => ({
  topology: catalogue[0],
  bundle: {
    workers: [
      { name: 'actor', description: 'does the work', frozen: false },
      { name: 'critic', description: 'rewrites the prompt', frozen: true },
    ],
    subscriptions: [],
    schedules: [],
    preconditions: {},
  },
  diff: {
    new_workers: ['actor', 'critic'],
    colliding_workers: [],
    new_subscriptions: [{ event_type: 'worker.finished', worker: 'critic' }],
    new_schedules: [{ cron: '0 9 * * *', worker: 'actor', input: 'do the rounds' }],
    settings_fields: ['base_image'],
    memory_seeds: 2,
  },
  missing_images: [],
  missing_skills: [],
  applicable: true,
})

beforeEach(() => {
  requests = []
  previewResponse = applicablePreview()
  applyResponse = {
    status: 200,
    body: {
      workers: [{ name: 'actor' }, { name: 'critic', frozen: true }],
      subscriptions: [{ id: 's1' }],
      schedules: [{ id: 'c1' }],
      event: { id: 'ev-topo-1', action: 'topology_apply', created_at: 1 },
    },
  }
  originalFetch = globalThis.fetch
  globalThis.fetch = vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
    const u = String(url)
    const method = init?.method ?? 'GET'
    const body = init?.body ? JSON.parse(String(init.body)) : undefined
    requests.push({ url: u, method, body })
    const json = (v: unknown, status = 200) =>
      new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } })

    if (u.includes('/agent/topologies/preview')) return json(previewResponse)
    if (u.includes('/agent/topologies/apply')) {
      if (applyResponse.status !== 200) {
        return new Response(String(applyResponse.body), { status: applyResponse.status })
      }
      return json(applyResponse.body)
    }
    if (u.includes('/agent/topologies')) return json({ topologies: catalogue })
    return json({})
  }) as typeof globalThis.fetch
})

afterEach(() => {
  globalThis.fetch = originalFetch
  vi.restoreAllMocks()
})

const posts = (path: string) =>
  requests.filter((r) => r.method === 'POST' && r.url.includes(path))

/** Walk to the actor-critic question step. */
async function openQuestions() {
  render(<TopologyOnboarding />)
  await userEvent.click(await screen.findByRole('button', { name: /choose actor-critic/i }))
  await screen.findByLabelText(/what domain/i)
}

/** Fill the one required answer and land on the preview step. */
async function openPreview() {
  await openQuestions()
  await userEvent.type(screen.getByLabelText(/what domain/i), 'marketing')
  await userEvent.click(screen.getByRole('button', { name: /^preview$/i }))
  await screen.findByText(/what applying actor-critic@v1 would do/i)
}

describe('the catalogue', () => {
  it('lists every topology with version and description', async () => {
    render(<TopologyOnboarding />)
    expect(await screen.findByText('actor-critic')).toBeInTheDocument()
    expect(screen.getByText(/a critic rewrites its prompt/i)).toBeInTheDocument()
    expect(screen.getByText('solo')).toBeInTheDocument()
    expect(screen.getByText(/no supervision/i)).toBeInTheDocument()
  })
})

describe('the questions', () => {
  it('seeds the form from the questions: defaults pre-applied', async () => {
    await openQuestions()
    expect(screen.getByLabelText(/freeze the critic/i)).toBeChecked()
    // The choice default is rendered as the select's value.
    expect(screen.getByText('daily')).toBeInTheDocument()
  })

  it('blocks the preview until every required question is answered', async () => {
    await openQuestions()
    expect(screen.getByText('an answer is required')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /^preview$/i })).toBeDisabled()

    await userEvent.type(screen.getByLabelText(/what domain/i), 'marketing')
    expect(screen.queryByText('an answer is required')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: /^preview$/i })).toBeEnabled()
  })

  it('sends the resolved answers to the preview route', async () => {
    await openPreview()
    expect(posts('/agent/topologies/preview')).toHaveLength(1)
    expect(posts('/agent/topologies/preview')[0]!.body).toEqual({
      name: 'actor-critic',
      version: 'v1',
      answers: { domain: 'marketing', 'freeze-critic': true, cadence: 'daily' },
    })
  })
})

describe('the preview', () => {
  it('renders the diff: workers (with the lock badge), routes, clock, settings, memory', async () => {
    await openPreview()
    expect(screen.getByText(/workers to create \(2\)/i)).toBeInTheDocument()
    expect(screen.getByText('actor')).toBeInTheDocument()
    expect(screen.getByText('critic')).toBeInTheDocument()
    // The frozen critic carries the same badge the worker list uses.
    expect(screen.getByText('frozen')).toBeInTheDocument()
    expect(screen.getByText('worker.finished → critic')).toBeInTheDocument()
    expect(screen.getByText('0 9 * * * → actor')).toBeInTheDocument()
    expect(screen.getByText('base_image')).toBeInTheDocument()
    expect(screen.getByText(/2 seed entries/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /apply topology/i })).toBeEnabled()
  })

  it('visibly blocks apply when the topology is not applicable', async () => {
    previewResponse = {
      ...applicablePreview(),
      diff: {
        ...applicablePreview().diff,
        new_workers: ['actor'],
        colliding_workers: ['critic'],
      },
      missing_images: ['marketing-tools'],
      applicable: false,
    }
    await openPreview()

    expect(screen.getByText(/cannot be applied to this project/i)).toBeInTheDocument()
    expect(screen.getByText(/worker names already taken in this project: critic/i)).toBeInTheDocument()
    expect(screen.getByText(/images this topology needs.*marketing-tools/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /apply topology/i })).toBeDisabled()
    expect(posts('/agent/topologies/apply')).toHaveLength(0)
  })
})

describe('the apply', () => {
  it('posts the same resolved answers and reports the receipt', async () => {
    const onApplied = vi.fn()
    render(<TopologyOnboarding onApplied={onApplied} />)
    await userEvent.click(await screen.findByRole('button', { name: /choose actor-critic/i }))
    await userEvent.type(await screen.findByLabelText(/what domain/i), 'marketing')
    await userEvent.click(screen.getByRole('button', { name: /^preview$/i }))
    await screen.findByRole('button', { name: /apply topology/i })
    await userEvent.click(screen.getByRole('button', { name: /apply topology/i }))

    await screen.findByText(/applied actor-critic@v1/i)
    expect(posts('/agent/topologies/apply')).toHaveLength(1)
    expect(posts('/agent/topologies/apply')[0]!.body).toEqual({
      name: 'actor-critic',
      version: 'v1',
      answers: { domain: 'marketing', 'freeze-critic': true, cadence: 'daily' },
    })
    expect(screen.getByText(/2 workers, 1 subscription, 1 schedule/i)).toBeInTheDocument()
    // The receipt names the changelog action when no changelog link is wired.
    expect(screen.getByText('topology_apply')).toBeInTheDocument()
    expect(screen.getByText('ev-topo-1')).toBeInTheDocument()
    expect(onApplied).toHaveBeenCalledTimes(1)
  })

  it('links the changelog when the host wires it', async () => {
    const onOpenChangelog = vi.fn()
    render(<TopologyOnboarding onOpenChangelog={onOpenChangelog} />)
    await userEvent.click(await screen.findByRole('button', { name: /choose actor-critic/i }))
    await userEvent.type(await screen.findByLabelText(/what domain/i), 'marketing')
    await userEvent.click(screen.getByRole('button', { name: /^preview$/i }))
    await userEvent.click(await screen.findByRole('button', { name: /apply topology/i }))

    await userEvent.click(await screen.findByRole('button', { name: /see it in the changelog/i }))
    expect(onOpenChangelog).toHaveBeenCalledTimes(1)
  })

  it('renders a 409 refusal verbatim, still on the preview step', async () => {
    applyResponse = {
      status: 409,
      body: 'topology not applicable: worker critic already exists; nothing was changed',
    }
    await openPreview()
    await userEvent.click(screen.getByRole('button', { name: /apply topology/i }))

    expect(
      await screen.findByText(/worker critic already exists; nothing was changed/i),
    ).toBeInTheDocument()
    // Still on the preview — the human can go back and change answers.
    expect(screen.getByRole('button', { name: /apply topology/i })).toBeInTheDocument()
    await waitFor(() => expect(screen.queryByText(/applied actor-critic@v1/i)).not.toBeInTheDocument())
  })
})
