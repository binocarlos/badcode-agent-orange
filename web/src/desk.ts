// The Desk fold — the three stacks of the operator's morning, computed
// read-time from data the product layer already keeps (operator-console design
// `docs/product/15-operator-console-design.md` §5.2).
//
// Three questions, in this order, every morning: *does anything want me? what
// changed? what broke?* — so `buildDesk` answers them as `{asks, changes,
// trouble}` and nothing else. The Desk stores nothing and decides nothing: it
// is a query over `event_deliveries`, `attention_requests`, `config_events`,
// `project_events` and the schedule rows, and every item's action is to open
// the thing it names.
//
// Pure: no React, no window, no fetch, no clock. `nowSeconds` and `lastSeenMs`
// are parameters precisely because a screen whose content depends on the wall
// clock is untestable — and this one is a table of ages.
//
// Two unit systems meet here, deliberately and awkwardly:
//   - deliveries, events, schedules and attention requests stamp unix SECONDS;
//   - config events stamp unix MILLISECONDS (J1).
// Hence `nowSeconds` *and* `lastSeenMs`. Do not "tidy" one into the other.
//
// The copy every field carries follows design §11: the operator's own
// vocabulary, no forward write called an undo, and a failure that has no reason
// column says so rather than showing a blank cell.

import {
  buildChangelog,
  type ChangelogEntry,
  type ConfigEvent,
} from './configLog.js'
import {
  deliveryDurationSeconds,
  formatDuration,
  type EventDelivery,
  type ProjectEvent,
  type Subscription,
} from './events.js'
import type { Schedule } from './schedules.js'

/** The read route the Asks stack needs (design B1; go/httpapi/attention.go). */
export const ATTENTION_ENDPOINTS = {
  /** GET — `?state=open|all`, `?limit=`; `{"attention_requests": [...]}`. */
  list: '/agent/attention-requests',
}

// ---------------------------------------------------------------------------
// The attention request (go/agentdb.AttentionRequest's JSON, verbatim)
// ---------------------------------------------------------------------------

/**
 * One `request_human_attention` call: the message a worker wrote for a person.
 *
 * This is where the *sentence* lives — the delivery row knows only that it is
 * parked. Without this list the Asks stack still renders, just without the
 * words, which is most of its value.
 */
export interface AttentionRequest {
  id: string
  project: string
  session_id: string
  worker: string
  message: string
  /** Permalink minted at request time, so a later base-URL change cannot
   *  rewrite history. */
  session_url: string
  /** Where the notification went: `webhook`, or `none` for log-only. */
  channel: string
  delivered: boolean
  /** Unix SECONDS; 0 = never expires. */
  expires_at: number
  created_at: number
  /** Unix SECONDS; 0 while still open. */
  answered_at: number
  timed_out_at: number
}

const num = (v: unknown, fallback = 0): number =>
  typeof v === 'number' && Number.isFinite(v) ? v : fallback
const str = (v: unknown, fallback = ''): string => (typeof v === 'string' ? v : fallback)
const bool = (v: unknown, fallback = false): boolean => (typeof v === 'boolean' ? v : fallback)

export function coerceAttentionRequest(raw: unknown): AttentionRequest {
  const r = raw && typeof raw === 'object' && !Array.isArray(raw) ? (raw as Record<string, unknown>) : {}
  return {
    id: str(r.id),
    project: str(r.project),
    session_id: str(r.session_id),
    worker: str(r.worker),
    message: str(r.message),
    session_url: str(r.session_url),
    channel: str(r.channel),
    delivered: bool(r.delivered),
    expires_at: num(r.expires_at),
    created_at: num(r.created_at),
    answered_at: num(r.answered_at),
    timed_out_at: num(r.timed_out_at),
  }
}

/** Open = nobody has answered it and the sweep has not timed it out. */
export function isAttentionRequestOpen(
  r: Pick<AttentionRequest, 'answered_at' | 'timed_out_at'>,
): boolean {
  return r.answered_at === 0 && r.timed_out_at === 0
}

// ---------------------------------------------------------------------------
// The closed glyph set (design §3.6)
// ---------------------------------------------------------------------------

/** Spine glyphs, as the Desk names them. Rendered by spine.tsx. */
export const DESK_GLYPHS = ['agent', 'human', 'attention', 'failure', 'freeze'] as const
export type DeskGlyph = (typeof DESK_GLYPHS)[number]

// ---------------------------------------------------------------------------
// Stack 1 — asks
// ---------------------------------------------------------------------------

/**
 * The honest handling of the parked-row wart, stated once and rendered by the
 * page: a delivery parked at `awaiting_human` never leaves that status, so the
 * stack is computed from the *request*, not from the row.
 */
export const DESK_ASKS_CAVEAT =
  'An ask leaves this stack when its request is answered or times out. The delivery row itself ' +
  'stays parked at awaiting_human — nothing rewrites it.'

/** One thing waiting for a person. */
export interface DeskAsk {
  /** Stable key: the delivery this ask parks. */
  id: string
  deliveryId: string
  requestId: string
  /** The worker that asked; '' when neither the request nor the subscription
   *  names one. */
  worker: string
  sessionId: string
  /** The permalink the request minted; '' when it carries none. */
  sessionUrl: string
  /** What the worker actually wrote, verbatim. */
  message: string
  /** Always `awaiting_human` — the vocabulary stays the vocabulary (§11.6). */
  status: string
  /** How long it has been waiting, in seconds. The clock genuinely runs. */
  waitingSeconds: number
  /** `2h 40m`. */
  waitingLabel: string
  /** Seconds until the request expires; null when it never expires. */
  expiresInSeconds: number | null
  /** `expires in 5h`, `expired`, or '' when it never expires. */
  expiresLabel: string
  /** `email-answerer · awaiting_human · 2h 40m`. */
  headline: string
  glyph: DeskGlyph
  createdAt: number
}

// ---------------------------------------------------------------------------
// Stack 2 — changes
// ---------------------------------------------------------------------------

/** One configuration change made since the operator last looked. */
export interface DeskChange {
  id: string
  /** The full changelog entry, diff machinery and all (§15.10). */
  entry: ChangelogEntry
  /** `email-reviewer`, or `you` for a human/API edit. */
  actor: string
  /** True when a worker made it — the ember mark (§3.2). */
  byAgent: boolean
  /** `rewrote`, `retuned`, `published`… — the short verb §11.6 uses. */
  verb: string
  /** `email-answerer`, `schedule daily-brief`, `project settings`. */
  subject: string
  /** `email-reviewer rewrote email-answerer`. */
  sentence: string
  /** The rationale, or the design's honest `(no reason given)`. */
  reason: string
  /** True when the rationale is blank — §8's patchy rationales, said plainly. */
  noReason: boolean
  /** `+4 −1 lines`, or '' when the change carried no prompt diff. */
  diffLabel: string
  glyph: DeskGlyph
  /** Unix MILLISECONDS. */
  createdAt: number
}

/** The short verb §11.6 wants in a sentence, per config action. */
export function deskChangeVerb(action: string): string {
  switch (action) {
    case 'worker_create':
      return 'hired'
    case 'worker_update':
      return 'updated'
    case 'worker_enable':
      return 'enabled'
    case 'worker_disable':
      return 'disabled'
    case 'worker_freeze':
      return 'froze'
    case 'worker_unfreeze':
      return 'unfroze'
    case 'worker_delete':
      return 'retired'
    case 'worker_prompt_write':
    case 'project_prompt_write':
      return 'rewrote'
    case 'project_settings_put':
      return 'changed'
    case 'subscription_create':
    case 'schedule_create':
      return 'created'
    case 'subscription_update':
    case 'schedule_update':
      return 'retuned'
    case 'subscription_delete':
    case 'schedule_delete':
      return 'deleted'
    case 'image_create':
    case 'skill_create':
      return 'published'
    case 'topology_apply':
      return 'applied'
    default:
      return action
  }
}

/** What a change acted on, named as the operator controls it (§11.1). */
export function deskChangeSubject(entry: ChangelogEntry): string {
  const { kind, name } = entry.entity
  switch (kind) {
    case 'worker':
      return name
    case 'project-prompt':
      return 'the project prompt'
    case 'project-settings':
      return 'project settings'
    case 'subscription':
      return name === '' ? 'a subscription' : `subscription ${name}`
    case 'schedule':
      return name === '' ? 'a schedule' : `schedule ${name}`
    case 'image':
    case 'skill':
      return name
    case 'topology':
      return name === '' ? 'a topology' : `topology ${name}`
    default:
      return name === '' ? entry.action : name
  }
}

// ---------------------------------------------------------------------------
// Stack 3 — trouble
// ---------------------------------------------------------------------------

/** The three failure shapes the docs warn about, each with its own sentence. */
export type DeskTroubleKind = 'failed-deliveries' | 'schedule-halted' | 'freeze-refusal'

export interface DeskTrouble {
  id: string
  kind: DeskTroubleKind
  glyph: DeskGlyph
  /** `3 deliveries failed · worker invoice-parser`. */
  headline: string
  /** The honest second line: what is recorded, and what is not. */
  detail: string
  /** The worker this is about; '' when the group has no worker. */
  worker: string
  count: number
  /** Unix SECONDS of the oldest member of the group; 0 when unknown. */
  sinceSeconds: number
  /** The most recent session involved, for an "open last job" link; '' if none. */
  sessionId: string
}

/**
 * Said once per failed-delivery group when the group has nothing to say.
 * The column exists since engine migration 037 (RD20); a row can still be blank
 * — it failed before that migration, or the failure path recorded nothing — and
 * a fabricated reason would be worse than this sentence.
 */
export const DESK_NO_DELIVERY_REASON =
  'No reason is recorded on a delivery row for this group. ' +
  'The agentd log and the last job are where the reason is.'

/** Why a refusal is on this screen at all (playbook C8). */
export const DESK_FREEZE_REFUSAL_NOTE =
  'A frozen worker is an instrument. An agent trying to edit the thing that scores it is a ' +
  'signal worth reading, not an error to clear.'

/**
 * How many consecutive failed starts disable a schedule — the engine's
 * `agentdb.ScheduleMaxProvisionFailures`. Mirrored, not imported: this package
 * never depends on `go/`.
 */
export const SCHEDULE_MAX_PROVISION_FAILURES = 5

// ---------------------------------------------------------------------------
// The fold
// ---------------------------------------------------------------------------

export interface BuildDeskInput {
  deliveries: EventDelivery[]
  events: ProjectEvent[]
  subscriptions: Subscription[]
  configEvents: ConfigEvent[]
  attentionRequests: AttentionRequest[]
  /**
   * Schedule rows, read for their `provision_failures` streak. Optional
   * because a host without the schedules route still gets the other two
   * stacks; the halted-schedule line simply never appears.
   */
  schedules?: Schedule[]
  /** The clock, in unix seconds. */
  nowSeconds: number
  /**
   * High-water mark for the Changes stack, in unix MILLISECONDS. 0 means "the
   * operator has never looked", which shows everything fetched — a first visit
   * is not an empty screen.
   */
  lastSeenMs: number
  /** Used to build actor-session permalinks, as buildChangelog does. */
  projectId?: string
  /**
   * How many already-seen changes to keep BELOW the waterline. Default 10.
   *
   * §3's critique of this screen: "since you last looked" was a filter, not a
   * visible line — the operator could see what was new but never what it was
   * new relative to. The window is still the window (`changes` is unchanged);
   * these are the few rows underneath it that give the divider something to
   * divide.
   */
  earlierChangesLimit?: number
}

export interface Desk {
  asks: DeskAsk[]
  changes: DeskChange[]
  /**
   * Changes at or before the mark, newest first, capped. Empty when the
   * operator has never looked — a waterline at the top of the list, with
   * everything below it, says nothing.
   */
  earlierChanges: DeskChange[]
  trouble: DeskTrouble[]
}

/**
 * The whole Desk, in one pure fold.
 *
 * Asks are `awaiting_human` deliveries joined to *open* attention requests, by
 * session: a delivery whose request has been answered or timed out drops out of
 * the stack, which is the read-time answer to the parked-row wart. We do not
 * fix the row; we stop showing a stale row as if it were live.
 *
 * Changes are the changelog windowed to "since you last looked". The window is
 * applied *after* `buildChangelog`, never before: a diff is computed against
 * the previous state of the same key, and that previous state is usually older
 * than the window. Filtering first would silently produce a first-ever-version
 * entry for every prompt rewrite.
 *
 * Trouble collects the failure shapes with the sentences the docs already
 * wrote — a failed delivery has no reason column, a halted schedule has
 * `last_provision_error` precisely so a human can recover, and a freeze refusal
 * is a signal rather than a fault.
 */
export function buildDesk(input: BuildDeskInput): Desk {
  const { fresh, earlier } = buildDeskChangeStacks(input)
  return {
    asks: buildDeskAsks(input),
    changes: fresh,
    earlierChanges: earlier,
    trouble: buildDeskTrouble(input),
  }
}

/**
 * Newest open request per session — a worker can ask twice in one session, and
 * the live question is the last one it asked.
 *
 * Exported because the shell's Desk badge counts asks, and an ask is this join,
 * not "an open attention request" (doc 21, X7): a request whose delivery has
 * moved on is not on the Desk, and a badge that counts one thing while the
 * stack lists another is a number an operator learns to distrust.
 */
export function openRequestsBySession(
  attentionRequests: AttentionRequest[],
): Map<string, AttentionRequest> {
  const openBySession = new Map<string, AttentionRequest>()
  for (const request of attentionRequests) {
    if (!isAttentionRequestOpen(request) || request.session_id === '') continue
    const held = openBySession.get(request.session_id)
    if (
      !held ||
      request.created_at > held.created_at ||
      (request.created_at === held.created_at && request.id > held.id)
    ) {
      openBySession.set(request.session_id, request)
    }
  }
  return openBySession
}

/**
 * How many asks the Desk would show, without building the rest of the fold.
 *
 * The same predicate `buildDeskAsks` applies, and deliberately the same
 * function: two implementations of "is this an ask" is exactly how the badge
 * and the stack came to disagree in the first place.
 */
export function countAsks(
  deliveries: EventDelivery[],
  attentionRequests: AttentionRequest[],
): number {
  const openBySession = openRequestsBySession(attentionRequests)
  return deliveries.filter((d) => d.status === 'awaiting_human' && openBySession.has(d.session_id))
    .length
}

function buildDeskAsks(input: BuildDeskInput): DeskAsk[] {
  const { deliveries, subscriptions, attentionRequests, nowSeconds } = input

  const openBySession = openRequestsBySession(attentionRequests)

  const workerBySubscription = new Map(subscriptions.map((s) => [s.id, s.worker]))

  const asks: DeskAsk[] = []
  for (const delivery of deliveries) {
    if (delivery.status !== 'awaiting_human') continue
    const request = openBySession.get(delivery.session_id)
    // No open request ⇒ answered, timed out, or never asked. Either way this
    // row is not a live question, so it is not on the Desk.
    if (!request) continue

    const waitingSeconds =
      deliveryDurationSeconds(delivery, nowSeconds) ??
      Math.max(0, nowSeconds - (request.created_at || nowSeconds))
    const worker = request.worker || workerBySubscription.get(delivery.subscription_id) || ''
    const expiresInSeconds = request.expires_at > 0 ? request.expires_at - nowSeconds : null

    asks.push({
      id: delivery.id,
      deliveryId: delivery.id,
      requestId: request.id,
      worker,
      sessionId: delivery.session_id,
      sessionUrl: request.session_url,
      message: request.message,
      status: delivery.status,
      waitingSeconds,
      waitingLabel: formatDuration(waitingSeconds),
      expiresInSeconds,
      expiresLabel:
        expiresInSeconds === null
          ? ''
          : expiresInSeconds <= 0
            ? 'expired'
            : `expires in ${formatDuration(expiresInSeconds)}`,
      headline: `${worker === '' ? 'a worker' : worker} · ${delivery.status} · ${formatDuration(waitingSeconds)}`,
      glyph: 'attention',
      createdAt: request.created_at,
    })
  }

  // Newest first, like every other list in this UI; ties by id so the order is
  // deterministic under a second-resolution clock.
  return asks.sort((a, b) => b.createdAt - a.createdAt || a.id.localeCompare(b.id))
}

/** The default depth of the "earlier" tail under the waterline. */
export const DESK_EARLIER_CHANGES_LIMIT = 10

function buildDeskChangeStacks(input: BuildDeskInput): {
  fresh: DeskChange[]
  earlier: DeskChange[]
} {
  const { lastSeenMs, earlierChangesLimit = DESK_EARLIER_CHANGES_LIMIT } = input
  const all = buildDeskChanges(input)
  const fresh = all.filter((change) => change.createdAt > lastSeenMs)
  // Never looked ⇒ everything is "new", so there is nothing for a line to
  // separate and no tail to show. Same rule `waterlineIndex` applies.
  if (lastSeenMs <= 0) return { fresh: all, earlier: [] }
  const earlier = all.filter((change) => change.createdAt <= lastSeenMs)
  return { fresh, earlier: earlierChangesLimit <= 0 ? [] : earlier.slice(0, earlierChangesLimit) }
}

function buildDeskChanges(input: BuildDeskInput): DeskChange[] {
  const { configEvents, projectId = '' } = input
  return buildChangelog(configEvents, { projectId })
    .map((entry): DeskChange => {
      const byAgent = entry.actorWorker !== ''
      const actor = byAgent ? entry.actorWorker : 'you'
      const verb = deskChangeVerb(entry.action)
      const subject = deskChangeSubject(entry)
      const noReason = entry.rationale.trim() === ''
      return {
        id: entry.id,
        entry,
        actor,
        byAgent,
        verb,
        subject,
        sentence: `${actor} ${verb} ${subject}`.trimEnd(),
        // Not a scold: the screen telling the truth about §8's patchy
        // rationales, which is the argument for asking for one.
        reason: noReason ? '(no reason given)' : entry.rationale,
        noReason,
        diffLabel: entry.diff ? `+${entry.diff.added} −${entry.diff.removed} lines` : '',
        glyph: byAgent ? 'agent' : 'human',
        createdAt: entry.createdAt,
      }
    })
}

function buildDeskTrouble(input: BuildDeskInput): DeskTrouble[] {
  const { deliveries, events, subscriptions, schedules = [] } = input
  const out: DeskTrouble[] = []

  // ── failed deliveries, grouped by worker ─────────────────────────────────
  const workerBySubscription = new Map(subscriptions.map((s) => [s.id, s.worker]))
  const failures = new Map<
    string,
    { count: number; since: number; sessionId: string; last: number; reason: string }
  >()
  for (const delivery of deliveries) {
    if (delivery.status !== 'failed') continue
    const worker = workerBySubscription.get(delivery.subscription_id) ?? ''
    const at = delivery.created_at || delivery.started_at || 0
    const reason = (delivery.failure_reason ?? '').trim()
    const held = failures.get(worker)
    if (!held) {
      failures.set(worker, {
        count: 1,
        since: at,
        sessionId: delivery.session_id,
        last: at,
        reason,
      })
      continue
    }
    held.count += 1
    if (at !== 0 && (held.since === 0 || at < held.since)) held.since = at
    if (at >= held.last) {
      held.last = at
      if (delivery.session_id !== '') held.sessionId = delivery.session_id
      // The freshest reason wins — a group is read to answer "what is wrong
      // NOW" — but a blank never overwrites one that says something.
      if (reason !== '') held.reason = reason
    } else if (held.reason === '') {
      held.reason = reason
    }
  }
  for (const [worker, group] of [...failures.entries()].sort(
    (a, b) => b[1].count - a[1].count || a[0].localeCompare(b[0]),
  )) {
    out.push({
      id: `failed:${worker}`,
      kind: 'failed-deliveries',
      glyph: 'failure',
      headline:
        worker === ''
          ? `${group.count} ${group.count === 1 ? 'delivery' : 'deliveries'} failed · the subscription that started them is gone`
          : `${group.count} ${group.count === 1 ? 'delivery' : 'deliveries'} failed · worker ${worker}`,
      // The engine's own words when it has any (RD20); the honest fallback when
      // it has none. The count is in the headline, so this line is only ever
      // "why".
      detail: group.reason === '' ? DESK_NO_DELIVERY_REASON : group.reason,
      worker,
      count: group.count,
      sinceSeconds: group.since,
      sessionId: group.sessionId,
    })
  }

  // ── schedules halted by the five-strike rule ─────────────────────────────
  for (const schedule of [...schedules]
    .filter((s) => (s.provision_failures ?? 0) >= SCHEDULE_MAX_PROVISION_FAILURES)
    .sort((a, b) => a.worker.localeCompare(b.worker) || a.id.localeCompare(b.id))) {
    const failuresCount = schedule.provision_failures ?? 0
    const reason = (schedule.last_provision_error ?? '').trim()
    out.push({
      id: `schedule:${schedule.id}`,
      kind: 'schedule-halted',
      glyph: 'failure',
      headline: `schedule ${schedule.worker} (${schedule.cron}) disabled after ${failuresCount} failed starts`,
      detail:
        reason === ''
          ? 'No reason is recorded on the schedule row — last_provision_error is empty.'
          : `last reason: ${reason}`,
      worker: schedule.worker,
      count: failuresCount,
      sinceSeconds: schedule.updated_at,
      sessionId: '',
    })
  }

  // ── freeze refusals ──────────────────────────────────────────────────────
  const refusals = new Map<
    string,
    { target: string; actor: string; count: number; since: number; sessionId: string; last: number }
  >()
  for (const event of events) {
    if (event.type !== 'worker.freeze_refused') continue
    const target = frozenTargetFromText(event.text)
    const actor = event.envelope.worker
    // The key's separator must be a character no worker name can contain. It
    // was a raw NUL, which made this whole file grep as binary; U+001F holds
    // the same guarantee and does not.
    const key = `${target}\u001f${actor}`
    const at = event.occurred_at || event.created_at || 0
    const held = refusals.get(key)
    if (!held) {
      refusals.set(key, {
        target,
        actor,
        count: 1,
        since: at,
        sessionId: event.envelope.session_id,
        last: at,
      })
      continue
    }
    held.count += 1
    if (at !== 0 && (held.since === 0 || at < held.since)) held.since = at
    if (at >= held.last) {
      held.last = at
      if (event.envelope.session_id !== '') held.sessionId = event.envelope.session_id
    }
  }
  for (const group of [...refusals.values()].sort(
    (a, b) => b.count - a.count || a.target.localeCompare(b.target) || a.actor.localeCompare(b.actor),
  )) {
    const target = group.target === '' ? 'a frozen worker' : group.target
    const from = group.actor === '' ? '' : ` from ${group.actor}`
    out.push({
      id: `freeze:${group.target}:${group.actor}`,
      kind: 'freeze-refusal',
      glyph: 'freeze',
      headline: `${target} refused ${group.count} ${group.count === 1 ? 'rewrite' : 'rewrites'}${from}`,
      detail: DESK_FREEZE_REFUSAL_NOTE,
      worker: group.target,
      count: group.count,
      sinceSeconds: group.since,
      sessionId: group.sessionId,
    })
  }

  return out
}

/**
 * The frozen worker a refusal was aimed at, read out of the event text.
 *
 * The engine writes `Refused <tool> against frozen worker "fee-scorer".` and
 * puts the *attempting* worker in the envelope, so the target exists only in
 * the sentence. Parsing prose is not lovely; the alternative is not naming the
 * instrument that was defended, which is the whole point of the line. A text
 * this does not recognise yields '' and the item reads "a frozen worker".
 */
export function frozenTargetFromText(text: string): string {
  const m = /frozen worker "([^"]+)"/.exec(text) ?? /frozen worker “([^”]+)”/.exec(text)
  return m ? m[1]! : ''
}
