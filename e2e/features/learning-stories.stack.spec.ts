import { test, expect } from '@playwright/test'
import { newProjectClient, poll, type ProjectClient } from '../helpers/api'
import { waitForConfigAction } from '../helpers/configlog'

// Learning stories — the deterministic offline gate on the §8.7 self-improvement
// loop (docs/product/11-learning-stories.md). One metamorphic relation, no judge:
//
//   MR-1: given the same task input, after the critic has rewritten the actor's
//   prompt, the actor's output satisfies a property it did not satisfy before —
//   and the config log contains that rewrite with its rationale.
//
// CAVEAT — doc 11 §2, binding on anyone quoting this suite:
//
//   These tests prove TRANSMISSION: the machinery of self-improvement works end
//   to end — a critic's edit reaches the next job's behaviour and is recorded.
//   They can NEVER prove DISCOVERY: the improvement asserted here is one we
//   wrote into the mock script ourselves; whether a real model would find it is
//   the question docs/AGENTS_RESEARCH.md exists to answer. A green run of this
//   suite must never be quoted as evidence that the system self-improves.
//
// # Why the mock makes the behavioural assertion rigorous
//
// The scripted mock model (go/modelproxy/script.go) selects its rule by
// substring match on the raw request body, which contains the composed system
// prompt — so it is a prompt-conditioned deterministic model. Round 0's prompt
// lacks the marker and a "naive" rule serves the bad answer; the critic's
// rewrite plants the marker; round 1's composed prompt carries it and a
// different rule answers. Nothing in the test simulates learning: the change
// travels through a real worker, a real event, a real `worker_prompt_write`,
// real composition. And because the switch happens on the request body, the
// changed behaviour is itself proof the composed prompt REACHED THE MODEL — it
// cannot be faked by a database write. The config-log read-back proves storage;
// the behaviour switch is the load-bearing delivery assertion.
//
// # The script, and the body-match footgun
//
// The script table lives in e2e/mock-scripts/learning-stories.json and the
// constants below must agree with it byte for byte. The match runs against the
// WHOLE request body, which replays prior turns — so the marker the critic emits
// inside its `worker_prompt_write` input leaks into every later request of the
// critic's own session. Hence the rules of doc 11 §3, followed here:
//   1. partition by worker name FIRST (the critic's rule sits above the actor's,
//      because the critic's requests contain the actor's name, transcript and —
//      after the tool call — the marker);
//   2. within the actor, split before/after with `absent` on the marker;
//   3. distinct, non-substring worker names per story (ls1-poet / ls1-reviewer);
//   4. sequencing is the assistant-message count only — `absent` is a match
//      predicate, never a second sequencer.
//
// Run with the script loaded (the tests skip without it):
//   ./e2e/run-stack-e2e.sh test mock --mock-script e2e/mock-scripts/learning-stories.json -- e2e/features/learning-stories.stack.spec.ts

const NEEDS_SCRIPT =
  'needs the scripted model: ./e2e/run-stack-e2e.sh test mock --mock-script e2e/mock-scripts/learning-stories.json -- e2e/features/learning-stories.stack.spec.ts'

// ── Shared helpers: seed a story's org, drive a round ───────────────────────

/** One story's cast: an actor doing the work, a critic watching it finish. */
interface StoryOrg {
  /** The worker whose behaviour the story improves. */
  actor: string
  actorPrompt: string
  /** The worker that rewrites the actor's prompt. Seeded even when disabled, so
   * the control differs from the treatment by exactly one flag. */
  critic: string
  criticPrompt: string
  /** The external event type that wakes the actor — rounds are driven by
   * POST /agent/events, never by schedules, so a round runs on demand. */
  eventType: string
  criticEnabled?: boolean
}

/**
 * Seeds one story's organisation into a run-scoped project: actor + critic and
 * the two subscriptions wiring them — the external trigger to the actor, and
 * the actor finishing to the critic. The envelope filter on the critic's
 * subscription is what keeps it from reacting to its own finish (§8.4).
 */
async function seedStoryOrg(client: ProjectClient, org: StoryOrg): Promise<void> {
  await client.putWorker(org.actor, {
    description: 'the worker this story improves',
    system_prompt: org.actorPrompt,
  })
  await client.putWorker(org.critic, {
    description: 'reviews the actor and retunes its prompt',
    system_prompt: org.criticPrompt,
    enabled: org.criticEnabled ?? true,
  })
  await client.createSubscription({ event_type: org.eventType, worker: org.actor })
  await client.createSubscription({
    event_type: 'worker.finished',
    worker: org.critic,
    filter: { worker: org.actor },
  })
}

/**
 * Drives one round: emits the trigger event and waits for the actor's delivery
 * to settle `ok`, then returns the actor's reply text. Every wait is a poll on
 * a happens-after record (delivery rows, persisted messages) — never a sleep.
 */
async function runActorRound(
  client: ProjectClient,
  eventType: string,
  text: string,
): Promise<{ eventId: string; sessionId: string; reply: string }> {
  const event = await client.postEvent({ type: eventType, text })
  const deliveries = await client.waitForDeliveries(
    (rows) => rows.length > 0 && rows.every((d) => d.status === 'ok' && d.session_id !== ''),
    { event_id: event.id, timeoutMs: 180_000 },
  )
  const sessionId = deliveries[0].session_id
  return { eventId: event.id, sessionId, reply: await assistantReply(client, sessionId) }
}

/** The actor's reply, read from the persisted transcript of its job session. */
async function assistantReply(client: ProjectClient, sessionId: string): Promise<string> {
  const messages = await poll(
    () => client.listMessages(sessionId),
    (rows) => rows.some((m) => m.role === 'assistant' && m.content.trim() !== ''),
    60_000,
    `an assistant reply in session ${sessionId}`,
  )
  return messages
    .filter((m) => m.role === 'assistant')
    .map((m) => m.content)
    .join('\n')
}

// ── MR-3 — no ghost learning (the control) ──────────────────────────────────

// The cheapest relation and the one most likely to catch a false green: with
// the critic disabled, the actor's behaviour on the same input is byte-identical
// across rounds. If this fails, some ambient nondeterminism — not the loop — is
// what the other stories would have been measuring.

const SOLOIST = 'ls0-soloist'
const OBSERVER = 'ls0-observer'
const LS0_EVENT = 'ls0.note.requested'
const SOLOIST_PROMPT = 'You sing one plain note whenever asked.'

test.describe('MR-3 — no ghost learning (control)', () => {
  test.describe.configure({ mode: 'serial' })
  test.setTimeout(300_000)

  let client: ProjectClient

  test.beforeEach(async ({ request }) => {
    client = await newProjectClient(request, 'e2e-ls-mr3')
  })

  // Every round holds a running container and a host port until deleted; the
  // pool is finite and the stack-teardown leak detector counts what is left.
  test.afterEach(async () => {
    await client.cleanup()
  })

  test("with the critic disabled, the actor's reply is byte-identical across rounds", async () => {
    test.skip(!process.env.STACK_MOCK_SCRIPT, NEEDS_SCRIPT)

    // The full org is seeded — worker, critic, both subscriptions — with the
    // critic switched off. The control differs from the treatment by exactly
    // that one flag, so anything that changes between rounds here is ambient.
    await seedStoryOrg(client, {
      actor: SOLOIST,
      actorPrompt: SOLOIST_PROMPT,
      critic: OBSERVER,
      criticPrompt:
        `You review ${SOLOIST}'s work and would use worker_prompt_write to retune it — but you are disabled.`,
      eventType: LS0_EVENT,
      criticEnabled: false,
    })

    const first = await runActorRound(client, LS0_EVENT, 'Sing the note, please.')
    const second = await runActorRound(client, LS0_EVENT, 'Sing the note, please.')

    expect(first.reply.trim()).not.toBe('')
    // Byte-identical, not merely similar: the mock is deterministic and the
    // loop is off, so ANY drift here is a rig problem the stories must not
    // inherit.
    expect(second.reply).toBe(first.reply)
    // Two different jobs produced it — this was two real rounds, not one read
    // twice.
    expect(second.sessionId).not.toBe(first.sessionId)

    // And no ghost rewrite happened anywhere: the config log carries no
    // worker_prompt_write, and the actor's stored prompt is exactly as seeded.
    expect(await client.configEvents({ action: 'worker_prompt_write' })).toEqual([])
    expect((await client.getWorker(SOLOIST)).system_prompt).toBe(SOLOIST_PROMPT)
  })
})

// ── S1 — the missing title (the canonical story) ────────────────────────────

// Round 0: the poet's poem has no title line. The poet finishing wakes the
// reviewer, whose scripted turn calls `worker_prompt_write` planting
// [LS1-TITLE-RULE] in the poet's prompt, with a rationale. Round 1: the same
// request produces a poem that opens with `Title:` — because the rewritten
// prompt reached the model, where a different mock rule now matches.

const POET = 'ls1-poet'
const REVIEWER = 'ls1-reviewer'
const LS1_EVENT = 'ls1.poem.requested'
const POET_PROMPT = 'You write short poems about whatever the event asks for.'
// The reviewer's prompt is the loop made of words: nothing in the engine knows
// about reviewing. (The mock script decides the tool call here, so this prompt
// is documentation of intent — the scripted call is what the model "chooses".)
const REVIEWER_PROMPT = [
  `You review ${POET}'s poems. If a poem has a systemic defect, use`,
  `worker_prompt_read and worker_prompt_write to amend ${POET}'s system prompt,`,
  'with a rationale saying what was wrong.',
].join('\n')
// Byte-for-byte agreements with e2e/mock-scripts/learning-stories.json:
/** The conspicuous, unlikely marker the rewrite plants; the mock's improved
 * rule matches on it and the naive rule carries it as `absent`. */
const TITLE_MARKER = 'LS1-TITLE-RULE'
/** The rationale the scripted `worker_prompt_write` carries (§15.5 makes it
 * mandatory on this action). */
const RATIONALE = 'the last poem shipped without a title line'
/** The property round 0 lacks and round 1 must have. */
const TITLE_LINE = 'Title:'

test.describe('S1 — the missing title', () => {
  test.describe.configure({ mode: 'serial' })
  test.setTimeout(300_000)

  let client: ProjectClient

  test.beforeEach(async ({ request }) => {
    client = await newProjectClient(request, 'e2e-ls-s1')
  })

  test.afterEach(async () => {
    await client.cleanup()
  })

  test("the critic's rewrite changes the actor's next job, and the log holds the rationale", async () => {
    test.skip(!process.env.STACK_MOCK_SCRIPT, NEEDS_SCRIPT)

    await seedStoryOrg(client, {
      actor: POET,
      actorPrompt: POET_PROMPT,
      critic: REVIEWER,
      criticPrompt: REVIEWER_PROMPT,
      eventType: LS1_EVENT,
    })
    expect((await client.getWorker(POET)).system_prompt).not.toContain(TITLE_MARKER)

    // ── Round 0: the defect ─────────────────────────────────────────────────
    const round0 = await runActorRound(client, LS1_EVENT, 'Write a short poem about rain on slate roofs.')
    expect(round0.reply.trim()).not.toBe('')
    expect(round0.reply, 'round 0 must exhibit the defect the critic will fix').not.toContain(TITLE_LINE)

    // ── The critic fires and rewrites the poet, through the real tool ───────
    // The poet finishing is a `worker.finished` event; the reviewer's
    // subscription starts a reviewer job; the scripted model calls
    // mcp__core__worker_prompt_write. The happens-after signal is the config
    // record the tool writes in the same transaction as the prompt (§15.4).
    const rewrite = await waitForConfigAction(client, 'worker_prompt_write', 180_000)
    expect(rewrite.rationale).toBe(RATIONALE)
    // Written BY the reviewer FROM its job — a worker editing a worker, no
    // human involved (a human edit logs no actor).
    expect(rewrite.actor_worker).toBe(REVIEWER)
    expect(rewrite.actor_session).not.toBe('')
    expect(rewrite.payload).toMatchObject({ name: POET })
    expect(String(rewrite.payload.system_prompt)).toContain(TITLE_MARKER)

    // Storage: the stored prompt now carries the rule. This proves the write
    // landed, NOT that the next job will see it — that is round 1's job.
    expect((await client.getWorker(POET)).system_prompt).toContain(TITLE_MARKER)

    // Let the reviewer's job settle before retiring its subscription, so the
    // teardown sweep is not racing a running container.
    const finished = await client.waitForEvents(
      (rows) => rows.some((e) => e.envelope.worker === POET),
      { type: 'worker.finished', timeoutMs: 120_000 },
    )
    const trigger = finished.find((e) => e.envelope.worker === POET)!
    await client.waitForDeliveries((rows) => rows.length > 0 && rows.every((d) => d.status === 'ok'), {
      event_id: trigger.id,
      timeoutMs: 120_000,
    })

    // Retire the critic before round 1. Two reasons: round 1's finish would
    // otherwise wake the reviewer again, whose script would write a SECOND
    // identical rewrite — making "the log holds exactly the rewrite" ambiguous
    // — and the improvement is already in the stored prompt, which is the whole
    // point: the lesson persists after the teacher is gone.
    const subs = await client.listSubscriptions()
    const criticSub = subs.find((s) => s.worker === REVIEWER)!
    await client.deleteSubscription(criticSub.id)

    // ── Round 1: the same request, answered differently ─────────────────────
    const round1 = await runActorRound(client, LS1_EVENT, 'Write a short poem about rain on slate roofs.')

    // THE LOAD-BEARING DELIVERY ASSERTION. The mock switches rules on the raw
    // request body, so `Title:` appearing here proves the rewritten composed
    // prompt reached the model on the next job — a claim no read-back of the
    // session row or the worker row could make (storage is not delivery; see
    // the Discovered Issues Log, audit 2026-07-26). The config-log check above
    // is the storage half; this is the half the loop is FOR.
    expect(round1.reply, "the critic's edit must change the actor's next behaviour").toContain(TITLE_LINE)
    expect(round1.reply).not.toBe(round0.reply)
    // A fresh job carried it, not a resumed one.
    expect(round1.sessionId).not.toBe(round0.sessionId)

    // Exactly the one rewrite, with its rationale — the audit trail of the
    // improvement is complete and unambiguous.
    const rewrites = await client.configEvents({ action: 'worker_prompt_write' })
    expect(rewrites).toHaveLength(1)
    expect(rewrites[0].id).toBe(rewrite.id)
    expect(rewrites[0].rationale).toBe(RATIONALE)
  })
})
