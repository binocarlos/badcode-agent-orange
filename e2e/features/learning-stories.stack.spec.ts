import { test, expect } from '@playwright/test'
import { newProjectClient, poll, type ConfigEvent, type ProjectClient } from '../helpers/api'
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

/**
 * The project's `worker_prompt_write` records, OLDEST FIRST, once at least
 * `count` of them exist. The config log is the happens-after signal for a
 * scripted rewrite: the record commits in the same transaction as the prompt
 * (§15.4), so its presence means the next composed job will carry the new text.
 */
async function waitForRewrites(
  client: ProjectClient,
  count: number,
  timeoutMs = 180_000,
): Promise<ConfigEvent[]> {
  const rows = await poll(
    () => client.configEvents({ action: 'worker_prompt_write' }),
    (found) => found.length >= count,
    timeoutMs,
    `${count} worker_prompt_write record(s) in ${client.project}`,
  )
  return [...rows].reverse()
}

/**
 * Waits for the critic job woken by this actor session's finish to settle
 * `ok`, so a later teardown or subscription retirement is not racing a running
 * container. The finish event is found by the actor session's id — with
 * multiple rounds in flight, "the first worker.finished" is ambiguous and this
 * is not.
 */
async function settleCriticRound(client: ProjectClient, actorSessionId: string): Promise<void> {
  const finished = await client.waitForEvents(
    (rows) => rows.some((e) => e.envelope.session_id === actorSessionId),
    { type: 'worker.finished', timeoutMs: 120_000 },
  )
  const trigger = finished.find((e) => e.envelope.session_id === actorSessionId)!
  await client.waitForDeliveries((rows) => rows.length > 0 && rows.every((d) => d.status === 'ok'), {
    event_id: trigger.id,
    timeoutMs: 120_000,
  })
}

/**
 * Retires the critic's subscription(s). A still-subscribed critic re-fires on
 * every later round and — because a fresh session serves its script from turn
 * 0 — double-writes the rewrite, making every "the log holds exactly N"
 * assertion ambiguous. Retiring also states the point of the loop: the lesson
 * lives in the stored prompt and survives the teacher.
 */
async function retireCritic(client: ProjectClient, critic: string): Promise<void> {
  const subs = await client.listSubscriptions()
  for (const sub of subs.filter((s) => s.worker === critic)) {
    await client.deleteSubscription(sub.id)
  }
}

/**
 * The whole of an S1-shaped story (one defect, one rewrite, one improvement) —
 * S2, S3 and S5 are S1 with a different property, so they share this driver
 * and differ only in their casts, markers and property assertions.
 */
async function runSingleRewriteStory(
  client: ProjectClient,
  opts: {
    org: StoryOrg
    task: string
    /** The conspicuous marker the rewrite plants (byte-agreed with the script). */
    marker: string
    /** The §15.5-mandatory rationale the scripted rewrite carries. */
    rationale: string
    expectDefect: (round0Reply: string) => void
    expectImproved: (round1Reply: string) => void
  },
): Promise<void> {
  await seedStoryOrg(client, opts.org)
  expect((await client.getWorker(opts.org.actor)).system_prompt).not.toContain(opts.marker)

  // Round 0: the defect the critic will fix.
  const round0 = await runActorRound(client, opts.org.eventType, opts.task)
  expect(round0.reply.trim()).not.toBe('')
  opts.expectDefect(round0.reply)

  // The critic fires and rewrites the actor through the real tool; the config
  // record is the happens-after signal (§15.4).
  const rewrite = await waitForConfigAction(client, 'worker_prompt_write', 180_000)
  expect(rewrite.rationale).toBe(opts.rationale)
  expect(rewrite.actor_worker).toBe(opts.org.critic)
  expect(rewrite.actor_session).not.toBe('')
  expect(rewrite.payload).toMatchObject({ name: opts.org.actor })
  expect(String(rewrite.payload.system_prompt)).toContain(opts.marker)
  // Storage half: the stored prompt carries the rule. Round 1 is the delivery half.
  expect((await client.getWorker(opts.org.actor)).system_prompt).toContain(opts.marker)

  await settleCriticRound(client, round0.sessionId)
  await retireCritic(client, opts.org.critic)

  // Round 1: the same task, answered differently — the load-bearing delivery
  // assertion (the mock switches rules on the request body, so the changed
  // behaviour proves the rewritten composed prompt reached the model).
  const round1 = await runActorRound(client, opts.org.eventType, opts.task)
  opts.expectImproved(round1.reply)
  expect(round1.reply).not.toBe(round0.reply)
  expect(round1.sessionId).not.toBe(round0.sessionId)

  // Exactly the one rewrite, with its rationale.
  const rewrites = await client.configEvents({ action: 'worker_prompt_write' })
  expect(rewrites).toHaveLength(1)
  expect(rewrites[0].id).toBe(rewrite.id)
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

// ── S2 — the forgotten sign-off ─────────────────────────────────────────────

// S1 with a different property: round 0's reply goes out unsigned, the editor
// plants [LS2-SIGNOFF-RULE], and the sign-off line appears only after the
// rewrite.

const ANSWERER = 'ls2-answerer'
const LS2_EDITOR = 'ls2-editor'
const LS2_EVENT = 'ls2.reply.requested'
const ANSWERER_PROMPT = 'You answer customer emails about their parcels.'
// Byte-for-byte agreements with e2e/mock-scripts/learning-stories.json:
const LS2_MARKER = 'LS2-SIGNOFF-RULE'
const LS2_RATIONALE = 'the last reply went out unsigned'
const LS2_SIGNOFF = '-- The Answering Desk'
const LS2_TASK = 'A customer asks when their parcel ships.'

test.describe('S2 — the forgotten sign-off', () => {
  test.describe.configure({ mode: 'serial' })
  test.setTimeout(300_000)

  let client: ProjectClient

  test.beforeEach(async ({ request }) => {
    client = await newProjectClient(request, 'e2e-ls-s2')
  })

  test.afterEach(async () => {
    await client.cleanup()
  })

  test('the sign-off line appears only after the rewrite', async () => {
    test.skip(!process.env.STACK_MOCK_SCRIPT, NEEDS_SCRIPT)

    await runSingleRewriteStory(client, {
      org: {
        actor: ANSWERER,
        actorPrompt: ANSWERER_PROMPT,
        critic: LS2_EDITOR,
        criticPrompt: [
          `You review ${ANSWERER}'s replies. If a reply has a systemic defect, use`,
          `worker_prompt_read and worker_prompt_write to amend ${ANSWERER}'s system prompt,`,
          'with a rationale saying what was wrong.',
        ].join('\n'),
        eventType: LS2_EVENT,
      },
      task: LS2_TASK,
      marker: LS2_MARKER,
      rationale: LS2_RATIONALE,
      expectDefect: (reply) => {
        expect(reply, 'round 0 must go out unsigned').not.toContain(LS2_SIGNOFF)
      },
      expectImproved: (reply) => {
        expect(reply, "the editor's sign-off rule must reach the next reply").toContain(LS2_SIGNOFF)
      },
    })
  })
})

// ── S3 — the missing units ──────────────────────────────────────────────────

// Round 0 reports a bare number; the calibrator plants [LS3-UNITS-RULE]; round
// 1 reports the same number with its units.

const GAUGE = 'ls3-gauge'
const CALIBRATOR = 'ls3-calibrator'
const LS3_EVENT = 'ls3.reading.requested'
const GAUGE_PROMPT = 'You report the reading from the cold chamber.'
// Byte-for-byte agreements with e2e/mock-scripts/learning-stories.json:
const LS3_MARKER = 'LS3-UNITS-RULE'
const LS3_RATIONALE = 'the reading was a bare number with no units'
const LS3_UNITS = '20°C'
const LS3_TASK = 'Report the chamber temperature reading.'

test.describe('S3 — the missing units', () => {
  test.describe.configure({ mode: 'serial' })
  test.setTimeout(300_000)

  let client: ProjectClient

  test.beforeEach(async ({ request }) => {
    client = await newProjectClient(request, 'e2e-ls-s3')
  })

  test.afterEach(async () => {
    await client.cleanup()
  })

  test('the units appear only after the rewrite', async () => {
    test.skip(!process.env.STACK_MOCK_SCRIPT, NEEDS_SCRIPT)

    await runSingleRewriteStory(client, {
      org: {
        actor: GAUGE,
        actorPrompt: GAUGE_PROMPT,
        critic: CALIBRATOR,
        criticPrompt: [
          `You review ${GAUGE}'s readings. If a reading has a systemic defect, use`,
          `worker_prompt_read and worker_prompt_write to amend ${GAUGE}'s system prompt,`,
          'with a rationale saying what was wrong.',
        ].join('\n'),
        eventType: LS3_EVENT,
      },
      task: LS3_TASK,
      marker: LS3_MARKER,
      rationale: LS3_RATIONALE,
      expectDefect: (reply) => {
        expect(reply, 'round 0 must report a bare number').toContain('20')
        expect(reply).not.toContain('°C')
      },
      expectImproved: (reply) => {
        expect(reply, "the calibrator's units rule must reach the next reading").toContain(LS3_UNITS)
      },
    })
  })
})

// ── S4 — the unasked question ───────────────────────────────────────────────

// The one story asserted on a NON-TEXT observable (doc 11 §4 row 4, MAST
// FM-2.2). Round 0: the planner guesses at an ambiguous request and the
// delivery settles `ok`. The mentor plants [LS4-ASK-RULE]. Round 1: the same
// request makes the planner call the core tool `request_human_attention`, and
// the delivery PARKS at `awaiting_human` with no `ended_at` — a pause, not a
// finish (§8.4, §9). The assertions are the delivery status and the
// `worker.finished` envelope stamp, exactly the mechanism acceptance-loop.spec.ts
// pins — never the reply text.

const PLANNER = 'ls4-planner'
const MENTOR = 'ls4-mentor'
const LS4_EVENT = 'ls4.booking.requested'
const PLANNER_PROMPT = 'You handle booking requests for the office.'
// Byte-for-byte agreements with e2e/mock-scripts/learning-stories.json:
const LS4_MARKER = 'LS4-ASK-RULE'
const LS4_RATIONALE = 'the planner guessed at an ambiguous request instead of asking'
const LS4_TASK = 'Book a room for the demo.'

test.describe('S4 — the unasked question', () => {
  test.describe.configure({ mode: 'serial' })
  test.setTimeout(300_000)

  let client: ProjectClient

  test.beforeEach(async ({ request }) => {
    client = await newProjectClient(request, 'e2e-ls-s4')
  })

  test.afterEach(async () => {
    await client.cleanup()
  })

  test('after the rewrite the planner asks, and the delivery parks at awaiting_human', async () => {
    test.skip(!process.env.STACK_MOCK_SCRIPT, NEEDS_SCRIPT)

    await seedStoryOrg(client, {
      actor: PLANNER,
      actorPrompt: PLANNER_PROMPT,
      critic: MENTOR,
      criticPrompt: [
        `You review ${PLANNER}'s bookings. If the planner has a systemic bad habit, use`,
        `worker_prompt_read and worker_prompt_write to amend ${PLANNER}'s system prompt,`,
        'with a rationale saying what was wrong.',
      ].join('\n'),
      eventType: LS4_EVENT,
    })
    expect((await client.getWorker(PLANNER)).system_prompt).not.toContain(LS4_MARKER)

    // ── Round 0: the guess — the job runs to completion without asking ──────
    const round0 = await runActorRound(client, LS4_EVENT, LS4_TASK)
    expect(round0.reply.trim()).not.toBe('')
    // The round-0 envelope says no attention was requested: the planner
    // finished as if its guess were an answer.
    const finished0 = await client.waitForEvents(
      (rows) => rows.some((e) => e.envelope.session_id === round0.sessionId),
      { type: 'worker.finished', timeoutMs: 120_000 },
    )
    expect(
      finished0.find((e) => e.envelope.session_id === round0.sessionId)!.envelope.attention_requested,
    ).toBe(false)

    // ── The mentor's rewrite lands, with its rationale ──────────────────────
    const rewrite = await waitForConfigAction(client, 'worker_prompt_write', 180_000)
    expect(rewrite.rationale).toBe(LS4_RATIONALE)
    expect(rewrite.actor_worker).toBe(MENTOR)
    expect(rewrite.payload).toMatchObject({ name: PLANNER })
    expect(String(rewrite.payload.system_prompt)).toContain(LS4_MARKER)
    expect((await client.getWorker(PLANNER)).system_prompt).toContain(LS4_MARKER)

    await settleCriticRound(client, round0.sessionId)
    await retireCritic(client, MENTOR)

    // ── Round 1: the same ambiguous request now pauses for a human ──────────
    // No runActorRound here: that helper waits for `ok`, and the whole point of
    // this round is that the delivery must NOT reach `ok`.
    const event = await client.postEvent({ type: LS4_EVENT, text: LS4_TASK })
    const parked = await client.waitForDeliveries(
      (rows) => rows.some((d) => d.status === 'awaiting_human'),
      { event_id: event.id, timeoutMs: 180_000 },
    )
    const waiting = parked.find((d) => d.status === 'awaiting_human')!
    // Awaiting a human is a pause, not an end (§8.4): the delivery is parked
    // open-ended, which is what lets a human answer hours later.
    expect(waiting.ended_at).toBe(0)
    // A fresh job did this, not the round-0 one.
    expect(waiting.session_id).not.toBe(round0.sessionId)

    // …and the envelope carries the §9 stamp, so a downstream reader can tell
    // knowingly-half-done work from finished work.
    const finished1 = await client.waitForEvents(
      (rows) => rows.some((e) => e.envelope.session_id === waiting.session_id),
      { type: 'worker.finished', timeoutMs: 120_000 },
    )
    expect(
      finished1.find((e) => e.envelope.session_id === waiting.session_id)!.envelope.attention_requested,
    ).toBe(true)

    // Exactly the one rewrite taught it.
    expect(await client.configEvents({ action: 'worker_prompt_write' })).toHaveLength(1)
  })
})

// ── S5 — the planted null ───────────────────────────────────────────────────

// The failure that matters most in the hypothesis lab (doc 11 §4 row 5): round
// 0 the analyst confirms a hypothesis the data does not support; the auditor
// plants [LS5-RIGOUR-RULE]; round 1 the same question is answered "no
// significant effect".

const ANALYST = 'ls5-analyst'
const AUDITOR = 'ls5-auditor'
const LS5_EVENT = 'ls5.analysis.requested'
const ANALYST_PROMPT = 'You analyse experiment results for the team.'
// Byte-for-byte agreements with e2e/mock-scripts/learning-stories.json:
const LS5_MARKER = 'LS5-RIGOUR-RULE'
const LS5_RATIONALE = 'the analyst confirmed a hypothesis the data does not support'
const LS5_TASK = 'Did the new headline change click-through? Report the result.'

test.describe('S5 — the planted null', () => {
  test.describe.configure({ mode: 'serial' })
  test.setTimeout(300_000)

  let client: ProjectClient

  test.beforeEach(async ({ request }) => {
    client = await newProjectClient(request, 'e2e-ls-s5')
  })

  test.afterEach(async () => {
    await client.cleanup()
  })

  test('after the rigour rule, the same result is reported as no significant effect', async () => {
    test.skip(!process.env.STACK_MOCK_SCRIPT, NEEDS_SCRIPT)

    await runSingleRewriteStory(client, {
      org: {
        actor: ANALYST,
        actorPrompt: ANALYST_PROMPT,
        critic: AUDITOR,
        criticPrompt: [
          `You audit ${ANALYST}'s conclusions. If a conclusion has a systemic defect, use`,
          `worker_prompt_read and worker_prompt_write to amend ${ANALYST}'s system prompt,`,
          'with a rationale saying what was wrong.',
        ].join('\n'),
        eventType: LS5_EVENT,
      },
      task: LS5_TASK,
      marker: LS5_MARKER,
      rationale: LS5_RATIONALE,
      expectDefect: (reply) => {
        expect(reply, 'round 0 must confirm the unsupported hypothesis').toContain('confirmed')
        expect(reply).not.toContain('No significant effect')
      },
      expectImproved: (reply) => {
        expect(reply, "the auditor's rigour rule must change the verdict").toContain('No significant effect')
        expect(reply).not.toContain('confirmed')
      },
    })
  })
})

// ── S6 — no regression (MR-2) ───────────────────────────────────────────────

// MR-2 (improvement is monotone): a property established by rewrite n still
// holds after rewrite n+1. TWO sequential rewrites land on the same actor —
// the title rule, then the sign-off rule — and after the second, BOTH
// properties hold. This catches fix-A-break-B.
//
// The critic fires twice, so the re-firing trap bites here first: each firing
// is a fresh session serving its script from turn 0. The script gives the two
// firings DIFFERENTIABLE rules keyed on the actor's transcript (round 0's
// lacks "Title: The Harvest", round 1's carries it), and the critic is retired
// before the final round so "exactly two rewrites" stays unambiguous.

const DRAFTER = 'ls6-drafter'
const LS6_EDITOR = 'ls6-editor'
const LS6_EVENT = 'ls6.draft.requested'
const DRAFTER_PROMPT = 'You draft notices about whatever the event asks for.'
// Byte-for-byte agreements with e2e/mock-scripts/learning-stories.json:
const LS6_TITLE_MARKER = 'LS6-TITLE-RULE'
const LS6_SIGNOFF_MARKER = 'LS6-SIGNOFF-RULE'
const LS6_TITLE_RATIONALE = 'the draft shipped without a title line'
const LS6_SIGNOFF_RATIONALE = "the draft shipped without the drafter's sign-off"
const LS6_SIGNOFF = '-- The Drafter'
const LS6_TASK = 'Draft the harvest notice.'

test.describe('S6 — no regression (MR-2)', () => {
  test.describe.configure({ mode: 'serial' })
  test.setTimeout(480_000)

  let client: ProjectClient

  test.beforeEach(async ({ request }) => {
    client = await newProjectClient(request, 'e2e-ls-s6')
  })

  test.afterEach(async () => {
    await client.cleanup()
  })

  test('the first rewrite still holds after the second', async () => {
    test.skip(!process.env.STACK_MOCK_SCRIPT, NEEDS_SCRIPT)

    await seedStoryOrg(client, {
      actor: DRAFTER,
      actorPrompt: DRAFTER_PROMPT,
      critic: LS6_EDITOR,
      criticPrompt: [
        `You review ${DRAFTER}'s notices, one defect per review. Use worker_prompt_read`,
        `and worker_prompt_write to amend ${DRAFTER}'s system prompt, keeping every`,
        'rule already there, with a rationale saying what was wrong.',
      ].join('\n'),
      eventType: LS6_EVENT,
    })

    // ── Round 0: neither property ───────────────────────────────────────────
    const round0 = await runActorRound(client, LS6_EVENT, LS6_TASK)
    expect(round0.reply.trim()).not.toBe('')
    expect(round0.reply).not.toContain(TITLE_LINE)
    expect(round0.reply).not.toContain(LS6_SIGNOFF)

    // ── Rewrite 1: the title rule ───────────────────────────────────────────
    const [first] = await waitForRewrites(client, 1)
    expect(first.actor_worker).toBe(LS6_EDITOR)
    expect(first.rationale).toBe(LS6_TITLE_RATIONALE)
    expect(first.payload).toMatchObject({ name: DRAFTER })
    expect(String(first.payload.system_prompt)).toContain(LS6_TITLE_MARKER)
    expect(String(first.payload.system_prompt)).not.toContain(LS6_SIGNOFF_MARKER)
    await settleCriticRound(client, round0.sessionId)

    // ── Round 1: the title holds; still unsigned ────────────────────────────
    const round1 = await runActorRound(client, LS6_EVENT, LS6_TASK)
    expect(round1.reply).toContain(TITLE_LINE)
    expect(round1.reply).not.toContain(LS6_SIGNOFF)
    expect(round1.reply).not.toBe(round0.reply)

    // ── Rewrite 2: the sign-off rule, ON TOP of the title rule ──────────────
    const [, second] = await waitForRewrites(client, 2)
    expect(second.rationale).toBe(LS6_SIGNOFF_RATIONALE)
    // MR-2 in storage: the second payload carries BOTH rules — the amendment
    // extended the prompt rather than replacing it.
    expect(String(second.payload.system_prompt)).toContain(LS6_TITLE_MARKER)
    expect(String(second.payload.system_prompt)).toContain(LS6_SIGNOFF_MARKER)
    const stored = (await client.getWorker(DRAFTER)).system_prompt
    expect(stored).toContain(LS6_TITLE_MARKER)
    expect(stored).toContain(LS6_SIGNOFF_MARKER)
    await settleCriticRound(client, round1.sessionId)
    await retireCritic(client, LS6_EDITOR)

    // ── Round 2: MR-2 in behaviour — BOTH properties hold ───────────────────
    const round2 = await runActorRound(client, LS6_EVENT, LS6_TASK)
    expect(round2.reply, "rewrite 2 must not regress rewrite 1's property").toContain(TITLE_LINE)
    expect(round2.reply, "rewrite 2's own property must hold too").toContain(LS6_SIGNOFF)
    expect(round2.sessionId).not.toBe(round1.sessionId)

    // Exactly two rewrites, in order.
    const rewrites = await waitForRewrites(client, 2)
    expect(rewrites).toHaveLength(2)
    expect(rewrites[0].seq).toBeLessThan(rewrites[1].seq)
  })
})

// ── S8 — the lineage ────────────────────────────────────────────────────────

// Proves P8 for the loop: three rewrites leave a complete, ordered, replayable
// audit trail. The config log holds exactly those three `worker_prompt_write`
// records, each with a rationale, and folding the log — taking the payload of
// rewrite k as the prompt — reproduces exactly what round k's job actually ran
// with (asserted against `composed_prompt` on the session rows, the string the
// model really received).

const SCRIBE = 'ls8-scribe'
const CURATOR = 'ls8-curator'
const LS8_EVENT = 'ls8.summary.requested'
const SCRIBE_PROMPT = "You post the day's ledger summary."
// Byte-for-byte agreements with e2e/mock-scripts/learning-stories.json:
const LS8_MARKERS = ['LS8-TITLE-RULE', 'LS8-SIGNOFF-RULE', 'LS8-DATE-RULE']
const LS8_TASK = "Post the day's ledger summary."

test.describe('S8 — the lineage', () => {
  test.describe.configure({ mode: 'serial' })
  test.setTimeout(600_000)

  let client: ProjectClient

  test.beforeEach(async ({ request }) => {
    client = await newProjectClient(request, 'e2e-ls-s8')
  })

  test.afterEach(async () => {
    await client.cleanup()
  })

  test("three rewrites leave an ordered trail that reproduces each round's prompt", async () => {
    test.skip(!process.env.STACK_MOCK_SCRIPT, NEEDS_SCRIPT)

    await seedStoryOrg(client, {
      actor: SCRIBE,
      actorPrompt: SCRIBE_PROMPT,
      critic: CURATOR,
      criticPrompt: [
        `You curate ${SCRIBE}'s summaries, one defect per review. Use worker_prompt_read`,
        `and worker_prompt_write to amend ${SCRIBE}'s system prompt, keeping every rule`,
        'already there, with a rationale saying what was wrong.',
      ].join('\n'),
      eventType: LS8_EVENT,
    })

    // Three rounds, each triggering one rewrite. After rewrite k the stored
    // prompt is captured — the live value the log's payload must reproduce.
    const rounds: Array<{ sessionId: string; reply: string }> = []
    const storedAfter: string[] = []
    for (let k = 0; k < 3; k++) {
      const round = await runActorRound(client, LS8_EVENT, LS8_TASK)
      rounds.push(round)
      const rewrites = await waitForRewrites(client, k + 1)
      storedAfter.push((await client.getWorker(SCRIBE)).system_prompt)
      expect(rewrites).toHaveLength(k + 1)
      await settleCriticRound(client, round.sessionId)
    }
    await retireCritic(client, CURATOR)

    // ── Exactly three, in order, each with a rationale ──────────────────────
    const log = await waitForRewrites(client, 3)
    expect(log).toHaveLength(3)
    for (const [i, record] of log.entries()) {
      expect(record.actor_worker).toBe(CURATOR)
      expect(record.payload).toMatchObject({ name: SCRIBE })
      expect(record.rationale.trim(), `rewrite ${i} must say why`).not.toBe('')
      expect(String(record.payload.system_prompt)).toContain(LS8_MARKERS[i])
    }
    expect(log[0].seq).toBeLessThan(log[1].seq)
    expect(log[1].seq).toBeLessThan(log[2].seq)
    // The three rationales are three different lessons, not one repeated.
    expect(new Set(log.map((r) => r.rationale)).size).toBe(3)

    // ── Replaying the log reproduces the prompt sequence ────────────────────
    // The payload of rewrite k IS the prompt that was stored after rewrite k —
    // the log alone is enough to reconstruct every prompt the worker ever had.
    expect(log.map((r) => String(r.payload.system_prompt))).toEqual(storedAfter)
    // Each rewrite extends the last: the lineage folds forward without loss.
    expect(storedAfter[1].startsWith(storedAfter[0])).toBe(true)
    expect(storedAfter[2].startsWith(storedAfter[1])).toBe(true)

    // ── Folding to round k reproduces round k's prompt ──────────────────────
    // `composed_prompt` on the session row is what the job really ran with
    // (§6.2). Round 0 ran on the seed; round k (k>0) ran on the payload of
    // rewrite k — read from the LOG, not from the worker row.
    const composed0 = (await client.getSession(rounds[0].sessionId)).composed_prompt ?? ''
    expect(composed0).toContain(SCRIBE_PROMPT)
    expect(composed0).not.toContain(LS8_MARKERS[0])
    for (const k of [1, 2]) {
      const composed = (await client.getSession(rounds[k].sessionId)).composed_prompt ?? ''
      expect(composed, `round ${k} must have run on the prompt rewrite ${k} wrote`).toContain(
        String(log[k - 1].payload.system_prompt),
      )
      expect(composed).not.toContain(LS8_MARKERS[k])
    }
  })
})

// ── S9 — the capstone ───────────────────────────────────────────────────────

// Three cumulative improvements — title, sign-off, units — transmitted in
// sequence, and the FINAL round's output satisfies all three simultaneously.
// This is the turn-count discipline test: every critic rule is scripted to
// exactly two turns (the rewrite, the acknowledgement), keyed to one round by
// what the actor's transcript does and does not yet contain — running past a
// rule's turns serves the canned text (a worker gone quiet), never a stray
// rewrite.

const REPORTER = 'ls9-reporter'
const COACH = 'ls9-coach'
const LS9_EVENT = 'ls9.report.requested'
const REPORTER_PROMPT = 'You publish the morning weather report for the station.'
// Byte-for-byte agreements with e2e/mock-scripts/learning-stories.json:
const LS9_MARKERS = ['LS9-TITLE-RULE', 'LS9-SIGNOFF-RULE', 'LS9-UNITS-RULE']
const LS9_SIGNOFF = '-- Station Nine'
const LS9_UNITS = '20°C'
const LS9_TASK = 'Publish the morning weather report.'

test.describe('S9 — the capstone', () => {
  test.describe.configure({ mode: 'serial' })
  test.setTimeout(600_000)

  let client: ProjectClient

  test.beforeEach(async ({ request }) => {
    client = await newProjectClient(request, 'e2e-ls-s9')
  })

  test.afterEach(async () => {
    await client.cleanup()
  })

  test('three cumulative improvements all hold in the final round', async () => {
    test.skip(!process.env.STACK_MOCK_SCRIPT, NEEDS_SCRIPT)

    await seedStoryOrg(client, {
      actor: REPORTER,
      actorPrompt: REPORTER_PROMPT,
      critic: COACH,
      criticPrompt: [
        `You coach ${REPORTER}, one lesson per review. Use worker_prompt_read and`,
        `worker_prompt_write to amend ${REPORTER}'s system prompt, keeping every rule`,
        'already there, with a rationale saying what was wrong.',
      ].join('\n'),
      eventType: LS9_EVENT,
    })

    // ── Round 0: none of the three properties ───────────────────────────────
    const round0 = await runActorRound(client, LS9_EVENT, LS9_TASK)
    expect(round0.reply.trim()).not.toBe('')
    expect(round0.reply).not.toContain(TITLE_LINE)
    expect(round0.reply).not.toContain(LS9_SIGNOFF)
    expect(round0.reply).not.toContain('°C')
    await waitForRewrites(client, 1)
    await settleCriticRound(client, round0.sessionId)

    // ── Round 1: the title arrived; nothing else yet ────────────────────────
    const round1 = await runActorRound(client, LS9_EVENT, LS9_TASK)
    expect(round1.reply).toContain(TITLE_LINE)
    expect(round1.reply).not.toContain(LS9_SIGNOFF)
    expect(round1.reply).not.toContain('°C')
    await waitForRewrites(client, 2)
    await settleCriticRound(client, round1.sessionId)

    // ── Round 2: title and sign-off; units still missing ────────────────────
    const round2 = await runActorRound(client, LS9_EVENT, LS9_TASK)
    expect(round2.reply).toContain(TITLE_LINE)
    expect(round2.reply).toContain(LS9_SIGNOFF)
    expect(round2.reply).not.toContain('°C')
    await waitForRewrites(client, 3)
    await settleCriticRound(client, round2.sessionId)
    await retireCritic(client, COACH)

    // ── The final round: all three lessons hold SIMULTANEOUSLY ──────────────
    const round3 = await runActorRound(client, LS9_EVENT, LS9_TASK)
    expect(round3.reply, 'lesson 1 (title) must still hold').toContain(TITLE_LINE)
    expect(round3.reply, 'lesson 2 (sign-off) must still hold').toContain(LS9_SIGNOFF)
    expect(round3.reply, 'lesson 3 (units) must hold').toContain(LS9_UNITS)
    // Four distinct jobs carried the four rounds.
    expect(new Set([round0, round1, round2, round3].map((r) => r.sessionId)).size).toBe(4)

    // Exactly three lessons taught, in order, each with a rationale — and the
    // final payload carries all three rules (the improvements are cumulative
    // in storage as well as in behaviour).
    const log = await waitForRewrites(client, 3)
    expect(log).toHaveLength(3)
    expect(log[0].seq).toBeLessThan(log[1].seq)
    expect(log[1].seq).toBeLessThan(log[2].seq)
    for (const record of log) expect(record.rationale.trim()).not.toBe('')
    for (const marker of LS9_MARKERS) {
      expect(String(log[2].payload.system_prompt)).toContain(marker)
    }
  })
})
