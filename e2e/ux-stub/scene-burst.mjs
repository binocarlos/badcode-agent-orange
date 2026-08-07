// scene-burst — the motion scene (doc 21 §5 M1).
//
// The base fixture is a still life: it proves LAYOUT. Motion needs ARRIVAL —
// a delivery that was not there on the previous fetch — and it needs a wire
// carrying more traffic than the ≤3-concurrent-pulses cap allows, so the
// "past three, switch to a static ×n count" rule (§4.1 rule 3) is actually
// exercised rather than merely unit-tested.
//
// So this scene is a FUNCTION OF FETCH COUNT, not a constant: each successive
// GET /agent/deliveries reveals more rows on the email-answerer → email-reviewer
// wire. Point a browser at the stub with SCENE=burst and the chart sees
// deliveries arriving exactly as it would against a live agentd.

import { deliveries as baseDeliveries, events as baseEvents } from './fixtures.mjs'

const NOW = 1785254400
const s = (secsAgo) => NOW - secsAgo

/** How many extra deliveries each successive fetch reveals. The steps are the
 *  interesting cardinalities: 1 (a single pulse), 2 (two in flight), then 4 and
 *  6 (past MAX_CONCURRENT_PULSES=3, where the ×n count must take over). */
const REVEAL_STEPS = [0, 1, 2, 4, 6]

/** Extra deliveries, all on sub-2 (worker.finished{email-answerer} → email-reviewer),
 *  newest last. Statuses vary so the chip crossfade and the fault-flash-stays
 *  rule both get a subject. */
const burst = [
  { id: 'brst-1', event_id: 'ev-7', subscription_id: 'sub-2', session_id: 'sess-b1', status: 'running', started_at: s(50), ended_at: 0 },
  { id: 'brst-2', event_id: 'ev-7', subscription_id: 'sub-2', session_id: 'sess-b2', status: 'running', started_at: s(40), ended_at: 0 },
  { id: 'brst-3', event_id: 'ev-7', subscription_id: 'sub-2', session_id: 'sess-b3', status: 'ok', started_at: s(35), ended_at: s(20) },
  { id: 'brst-4', event_id: 'ev-7', subscription_id: 'sub-2', session_id: 'sess-b4', status: 'failed', started_at: s(30), ended_at: s(18) },
  { id: 'brst-5', event_id: 'ev-7', subscription_id: 'sub-2', session_id: 'sess-b5', status: 'running', started_at: s(12), ended_at: 0 },
  { id: 'brst-6', event_id: 'ev-6', subscription_id: 'sub-3', session_id: 'sess-b6', status: 'running', started_at: s(6), ended_at: 0 },
].map((d) => ({ project: 'badcode', created_at: d.started_at + 1, updated_at: d.started_at, ...d }))

let fetches = 0

/** Deliveries for the Nth fetch — grows, then holds at the full set. */
export function burstDeliveries() {
  const step = REVEAL_STEPS[Math.min(fetches, REVEAL_STEPS.length - 1)]
  fetches += 1
  return { deliveries: [...burst.slice(0, step), ...baseDeliveries.deliveries] }
}

/** Events grow alongside, so the Desk/Events feeds also see arrivals (§5 M4). */
export function burstEvents() {
  const step = REVEAL_STEPS[Math.min(fetches, REVEAL_STEPS.length - 1)]
  const extra = burst.slice(0, step).map((d, i) => ({
    id: `ev-brst-${i + 1}`,
    project: 'badcode',
    type: 'worker.finished',
    text: `Assistant: reviewed answer ${i + 1}…`,
    envelope: { depth: 2, source: 'worker', worker: 'email-reviewer', session_id: d.session_id, interactive: false, attention_requested: false },
    occurred_at: d.started_at,
    created_at: d.started_at,
    delivered: true,
  }))
  return { events: [...extra, ...baseEvents.events] }
}

/** Reset between capture runs so a fresh browser sees the sequence from zero. */
export function resetBurst() {
  fetches = 0
}
