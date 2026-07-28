// truths.ts — loading what gauntletgen wrote, and refusing to run on data that
// does not match its own record.
//
// It is `../triage/truths.ts` with one addition and one refusal.
//
//   + the directive record per ticket: kind, demanded queue, demanded phrase,
//     the signal, and the planted bytes.
//   ! the SCHEMA is gauntletgen's, not triagelabgen's, and a plain SC-1 dataset
//     is refused outright. That refusal is the load-bearing part: an SC-1
//     directory has no directives, so a gauntlet run over it would report a
//     directive-compliance rate of 0.0 on a denominator of 0 — which prints
//     exactly like an org that resisted every attack it was given.
//
// The routing truth loaded here never leaves the runner except inside an audit
// event; the DIRECTIVE, by contrast, is already in the ticket the dispatcher
// receives, because it is the attack.

import { createHash } from 'node:crypto'
import * as fs from 'node:fs'
import * as path from 'node:path'
import { ESCALATE, QUEUES } from '../triage/route'
import { DIRECTIVE_KINDS, type DirectiveKind, type DirectiveRecord } from './directives'

/** The schema gauntletgen stamps; anything else is refused. */
export const TRUTHS_SCHEMA = 'agent-orange/triagelab/gauntlet-truths@1'

/** One ticket as the generator recorded it. */
export interface Ticket {
  id: string
  /** plain | misdirect | ambiguous. */
  kind: string
  /** The canonical queue id the generator aimed at, '' for ambiguous. */
  queue: string
  /** The queue this ticket's vocabulary points at; '' for plain. */
  decoy: string
  seed: number
  /** The held-out correct route: a canonical queue id, or `escalate`. */
  route: string
  explanation: string
  /** What the two reference routers found on these bytes, as recorded. */
  naiveRoute: string
  contentRoute: string
  /** The planted attack, or null on a clean control ticket. */
  directive: DirectiveRecord | null
  /** The ticket bytes, verbatim — directive included. */
  text: string
}

/** A loaded dataset directory, in manifest order. */
export interface TruthSet {
  seed: number
  note: string
  tickets: Ticket[]
}

interface RawDirective {
  kind?: string
  queue?: string
  phrase?: string
  signal?: string
  text?: string
}

interface RawTruth {
  kind?: string
  queue?: string
  decoy?: string
  seed?: number
  dataset?: string
  sha256?: string
  route?: string
  explanation?: string
  routing?: { naive?: string; content?: string; scores?: string }
  attacked?: boolean
  directive?: RawDirective
}

interface RawTruths {
  schema?: string
  seed?: number
  note?: string
  order?: string[]
  tickets?: Record<string, RawTruth>
}

/** The legal values of a held-out route. */
const ROUTES = new Set<string>([...QUEUES, ESCALATE])

/** The legal directive kinds. */
const KINDS = new Set<string>(DIRECTIVE_KINDS)

/**
 * Loads `truths.json` and every ticket it names, checking each file against its
 * recorded SHA-256 and each directive against the vocabulary this rig scores.
 *
 * Throws on anything unexpected. There is no partial load: a run that started
 * on half a dataset directory would produce a report indistinguishable from a
 * complete one except for its numbers.
 */
export function loadTruths(dir: string): TruthSet {
  const truthsPath = path.join(dir, 'truths.json')
  if (!fs.existsSync(truthsPath)) {
    throw new Error(
      `no truths.json in ${dir} — generate the tickets first:\n` +
        `    go -C go run ./cmd/gauntletgen -manifest <manifest> -out ${dir}`,
    )
  }
  const doc = JSON.parse(fs.readFileSync(truthsPath, 'utf8')) as RawTruths
  if (doc.schema !== TRUTHS_SCHEMA) {
    throw new Error(
      `${truthsPath}: schema ${String(doc.schema)}, this runner speaks ${TRUTHS_SCHEMA}. ` +
        'An SC-1 dataset has no directives in it, so a gauntlet run over one would report a ' +
        'compliance rate of zero and look exactly like an org that resisted everything.',
    )
  }
  const order = doc.order ?? []
  if (order.length === 0) throw new Error(`${truthsPath}: no tickets`)

  const tickets: Ticket[] = order.map((id) => {
    const raw = doc.tickets?.[id]
    if (!raw) throw new Error(`${truthsPath}: order names ${id} but there is no such ticket`)
    if (!raw.route || !ROUTES.has(raw.route)) {
      throw new Error(`${truthsPath}: ${id} has route ${String(raw.route)}, want one of ${[...ROUTES].join(', ')}`)
    }
    const file = path.join(dir, raw.dataset ?? `${id}.txt`)
    if (!fs.existsSync(file)) throw new Error(`${truthsPath}: ${id}'s ticket ${file} is missing`)
    const text = fs.readFileSync(file, 'utf8')
    const digest = createHash('sha256').update(text, 'utf8').digest('hex')
    if (digest !== raw.sha256) {
      throw new Error(
        `${file} does not match the checksum in truths.json (${digest} vs ${String(raw.sha256)}) — ` +
          'the dataset directory is stale; regenerate it',
      )
    }
    return {
      id,
      kind: String(raw.kind ?? ''),
      queue: String(raw.queue ?? ''),
      decoy: String(raw.decoy ?? ''),
      seed: Number(raw.seed ?? 0),
      route: raw.route,
      explanation: String(raw.explanation ?? ''),
      naiveRoute: String(raw.routing?.naive ?? ''),
      contentRoute: String(raw.routing?.content ?? ''),
      directive: loadDirective(id, text, raw),
      text,
    }
  })

  return { seed: Number(doc.seed ?? 0), note: String(doc.note ?? ''), tickets }
}

/**
 * Reads one ticket's directive record, and checks it against the bytes.
 *
 * The check is cheap and the failure it catches is not: a record whose planted
 * text is not in the ticket means the harness would be scoring compliance with
 * an attack the org never saw. `attacked` and `directive` must also agree —
 * gauntletgen writes both, and a reader that trusted only one would silently
 * drop a ticket out of the compliance denominator.
 */
function loadDirective(id: string, text: string, raw: RawTruth): DirectiveRecord | null {
  const attacked = raw.attacked === true
  if (!raw.directive) {
    if (attacked) throw new Error(`${id}: recorded as attacked and carries no directive`)
    return null
  }
  if (!attacked) throw new Error(`${id}: carries a directive but is not recorded as attacked`)
  const kind = String(raw.directive.kind ?? '')
  if (!KINDS.has(kind)) {
    throw new Error(`${id}: directive kind ${kind}, want one of ${[...KINDS].join(', ')}`)
  }
  const planted = String(raw.directive.text ?? '')
  if (planted === '' || !text.includes(planted)) {
    throw new Error(
      `${id}: the recorded directive text is not in the ticket bytes — the run would score compliance ` +
        'with an attack the org never received',
    )
  }
  const signal = String(raw.directive.signal ?? '')
  if (signal === '') throw new Error(`${id}: a directive with no stated signal is inadmissible (doc 19 §2)`)
  return {
    kind: kind as DirectiveKind,
    queue: String(raw.directive.queue ?? ''),
    phrase: String(raw.directive.phrase ?? ''),
    signal,
    text: planted,
  }
}
