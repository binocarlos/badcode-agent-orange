// Memories — the browser-side mirror of the read-only `/agent/memories` route
// (operator-console design B2 + §8; engine: go/httpapi/memories.go, store:
// go/agentdb/memories.go, grammar: go/agentdb/labels.go, spec §7.1–§7.6).
//
// Pure: no React, no window, no fetch. The hook lives in useMemories.ts.
//
// Read-only all the way down, because memory is append-only (§7.1): a memory is
// written by a worker through its tools and is never edited, so "updating" a
// value means appending another row with the same labels. That is why the
// `name=` convention below folds superseded values under the current one
// instead of pretending there is one editable record.
//
// The selector grammar here is a MIRROR, not a second parser: the engine's
// ParseLabelSelector is the authority and this file exists so an operator sees
// their mistake before the round trip, phrased the way the server phrases it.
// Where the two could drift, the server wins — the route reports its own
// message and the UI shows that verbatim.

import type { Worker } from './workers.js'
import { formatCompactTime } from './timefmt.js'

/** Endpoint paths for the memory read route. Overridable per host, like
 *  WORKER_ENDPOINTS. */
export const MEMORY_ENDPOINTS = {
  list: '/agent/memories',
}

// ---------------------------------------------------------------------------
// Rows
// ---------------------------------------------------------------------------

/** One search result. Field names are the wire names (snake_case).
 *
 *  `snippet` is not the whole memory: the store returns at most
 *  MEMORY_SNIPPET_CHARS characters per row (`MemoryGet` returns everything, and
 *  there is no HTTP route for it). `created_at` is unix MILLISECONDS — unlike
 *  the images/skills catalogue, which is seconds. */
export interface MemoryRow {
  id: string
  labels: Record<string, string>
  snippet: string
  score: number
  created_by_worker: string
  created_by_session: string
  created_at: number
}

/** What the store caps a search snippet at (agentdb/memories.go). Anything
 *  longer is cut by the ROUTE, before any cap this UI applies. */
export const MEMORY_SNIPPET_CHARS = 500

const num = (v: unknown, fallback = 0): number =>
  typeof v === 'number' && Number.isFinite(v) ? v : fallback
const str = (v: unknown, fallback = ''): string => (typeof v === 'string' ? v : fallback)

/** String→string labels only; anything else in the map is dropped rather than
 *  rendered as "[object Object]". */
function coerceLabels(raw: unknown): Record<string, string> {
  if (!raw || typeof raw !== 'object') return {}
  const out: Record<string, string> = {}
  for (const [k, v] of Object.entries(raw as Record<string, unknown>)) {
    if (typeof v === 'string') out[k] = v
  }
  return out
}

export function coerceMemory(raw: unknown): MemoryRow {
  const base: MemoryRow = {
    id: '',
    labels: {},
    snippet: '',
    score: 0,
    created_by_worker: '',
    created_by_session: '',
    created_at: 0,
  }
  if (!raw || typeof raw !== 'object') return base
  const r = raw as Record<string, unknown>
  return {
    id: str(r.id),
    labels: coerceLabels(r.labels),
    snippet: str(r.snippet),
    score: num(r.score),
    created_by_worker: str(r.created_by_worker),
    created_by_session: str(r.created_by_session),
    created_at: num(r.created_at),
  }
}

/** Memory timestamps are unix MILLISECONDS (§7.1). Project events are seconds
 *  and the images/skills catalogue is seconds — mixing them silently renders
 *  1970 or the year 55000, so this is its own named formatter. */
export function formatMemoryTimestamp(ms: number | null | undefined): string {
  if (!ms) return ''
  return formatCompactTime(ms)
}

// ---------------------------------------------------------------------------
// The selector grammar (spec §7.2 — Kubernetes semantics, conjunction only)
// ---------------------------------------------------------------------------

/** The comparison one requirement performs. Same spellings as the engine's
 *  LabelOperator, so a chip's operator can be read straight off the wire. */
export type MemoryOperator = '=' | '!=' | 'in' | 'notin' | 'exists' | '!'

/** One term of a selector: a chip in the selector bar. */
export interface MemoryRequirement {
  key: string
  op: MemoryOperator
  /** One value for =/!=, one-or-more for in/notin, none for exists/!. */
  values: string[]
}

/** The result of parsing a selector string. `error` is null when it parsed;
 *  when it is set, `requirements` holds the terms parsed BEFORE the bad one, so
 *  a half-typed bar can still show its good chips. */
export interface MemorySelectorParse {
  requirements: MemoryRequirement[]
  error: string | null
}

/** There is no OR in this grammar and there is not going to be one — §7.2 says
 *  a caller who needs OR runs two searches. Rendered next to the selector bar
 *  so nobody types `|` and wonders. */
export const NO_OR_NOTE = 'There is no OR — a selector is an AND of clauses. Run two searches.'

const LABEL_MAX_LEN = 63
// The engine's charset (agentdb/labels.go): a single name segment, no DNS
// prefix, which is what keeps values unambiguous inside this grammar.
const LABEL_RE = /^[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$/

/** Mirrors ValidateLabelKey, message for message. */
export function labelKeyError(k: string): string | null {
  if (k === '') return 'label key must not be empty'
  if (k.length > LABEL_MAX_LEN) {
    return `label key "${k}" is ${k.length} chars, max ${LABEL_MAX_LEN}`
  }
  if (!LABEL_RE.test(k)) {
    return `label key "${k}" is invalid: must be alphanumeric, optionally containing '-', '_' or '.', and start and end alphanumeric`
  }
  return null
}

/** Mirrors ValidateLabelValue. The empty value is legal (K8s allows it). */
export function labelValueError(v: string): string | null {
  if (v === '') return null
  if (v.length > LABEL_MAX_LEN) {
    return `label value "${v}" is ${v.length} chars, max ${LABEL_MAX_LEN}`
  }
  if (!LABEL_RE.test(v)) {
    return `label value "${v}" is invalid: must be alphanumeric, optionally containing '-', '_' or '.', and start and end alphanumeric`
  }
  return null
}

/** Split on top-level commas — commas inside a set's parentheses belong to the
 *  set, not to the conjunction. Mirrors splitTerms. */
function splitTerms(s: string): { terms: string[]; error: string | null } {
  const terms: string[] = []
  let buf = ''
  let depth = 0
  const flush = () => {
    const t = buf.trim()
    buf = ''
    if (t !== '') terms.push(t)
  }
  for (const r of s) {
    if (r === '(') {
      depth++
      buf += r
    } else if (r === ')') {
      depth--
      if (depth < 0) return { terms, error: `selector "${s}": unbalanced ')'` }
      buf += r
    } else if (r === ',' && depth === 0) {
      flush()
    } else {
      buf += r
    }
  }
  if (depth !== 0) return { terms, error: `selector "${s}": unbalanced '('` }
  flush()
  return { terms, error: null }
}

const SET_TERM_RE = /^(\S+)\s+(in|notin)\s*\(([\s\S]*)\)$/

/** Parse one term. Mirrors parseRequirement, including its error wording. */
export function parseRequirement(
  term: string,
): { requirement: MemoryRequirement | null; error: string | null } {
  const bad = (error: string) => ({ requirement: null, error })

  // !key — must not exist.
  if (term.startsWith('!')) {
    const key = term.slice(1).trim()
    const err = labelKeyError(key)
    if (err) return bad(`selector term "${term}": ${err}`)
    return { requirement: { key, op: '!', values: [] }, error: null }
  }

  // exists key — must exist (the spec's spelling of the bare-key form).
  if (/^exists\s/.test(term)) {
    const key = term.slice('exists'.length).trim()
    const err = labelKeyError(key)
    if (err) return bad(`selector term "${term}": ${err}`)
    return { requirement: { key, op: 'exists', values: [] }, error: null }
  }

  // key in (a, b) / key notin (a)
  const m = SET_TERM_RE.exec(term)
  if (m) {
    const key = m[1]
    const keyErr = labelKeyError(key)
    if (keyErr) return bad(`selector term "${term}": ${keyErr}`)
    const op: MemoryOperator = m[2] === 'notin' ? 'notin' : 'in'
    const values: string[] = []
    for (const raw of m[3].split(',')) {
      const v = raw.trim()
      if (v === '') return bad(`selector term "${term}": empty value in set`)
      const valErr = labelValueError(v)
      if (valErr) return bad(`selector term "${term}": ${valErr}`)
      values.push(v)
    }
    if (values.length === 0) {
      return bad(`selector term "${term}": ${m[2]} requires at least one value`)
    }
    return { requirement: { key, op, values }, error: null }
  }

  // key!=value / key==value / key=value — longest token first, as the engine does.
  for (const [token, op] of [
    ['!=', '!='],
    ['==', '='],
    ['=', '='],
  ] as [string, MemoryOperator][]) {
    const i = term.indexOf(token)
    if (i >= 0) {
      const key = term.slice(0, i).trim()
      const val = term.slice(i + token.length).trim()
      const keyErr = labelKeyError(key)
      if (keyErr) return bad(`selector term "${term}": ${keyErr}`)
      const valErr = labelValueError(val)
      if (valErr) return bad(`selector term "${term}": ${valErr}`)
      return { requirement: { key, op, values: [val] }, error: null }
    }
  }

  // Bare key — must exist.
  const err = labelKeyError(term)
  if (err) return bad(`selector term "${term}" is not a valid requirement: ${err}`)
  return { requirement: { key: term, op: 'exists', values: [] }, error: null }
}

/**
 * Parse selector text into chips. The empty string (or whitespace) parses to
 * no requirements, which matches everything — the browser's newest-first view.
 *
 *	worker=email-answerer,kind!=raw-transcript
 *	kind in (summary, lesson)
 *	thread notin (spam)
 *	exists thread          (or the bare form: thread)
 *	!archived
 */
export function parseMemorySelector(s: string): MemorySelectorParse {
  const { terms, error } = splitTerms(s)
  if (error !== null) return { requirements: [], error }
  const requirements: MemoryRequirement[] = []
  for (const term of terms) {
    const parsed = parseRequirement(term)
    if (parsed.error !== null || parsed.requirement === null) {
      return { requirements, error: parsed.error }
    }
    requirements.push(parsed.requirement)
  }
  return { requirements, error: null }
}

/** One requirement back to its canonical text — the chip's label, and half of
 *  the round trip a chip delete performs. */
export function formatRequirement(req: MemoryRequirement): string {
  switch (req.op) {
    case 'exists':
      return req.key
    case '!':
      return `!${req.key}`
    case 'in':
    case 'notin':
      return `${req.key} ${req.op} (${req.values.join(', ')})`
    default:
      return `${req.key}${req.op}${req.values[0] ?? ''}`
  }
}

/** Requirements back to a selector string: the builder half of the module, and
 *  what the bar sends to the route after a chip is added or removed. */
export function buildMemorySelector(reqs: MemoryRequirement[]): string {
  return reqs.map(formatRequirement).join(',')
}

// ---------------------------------------------------------------------------
// Relevance, honestly (§7.6)
// ---------------------------------------------------------------------------

/** Shown whenever a free-text query is present. §7.6 fuses keyword and vector
 *  legs by RRF and has NO distance threshold, so the bottom of a result list is
 *  "the best of a bad lot", not "a match". */
export const RRF_NOTE =
  'Ranked by RRF over a keyword and a semantic leg, with recency as a tiebreak. There is no relevance threshold: a low score means nothing good matched, not that these rows are close.'

/** Shown alongside RRF_NOTE when the results look keyword-only — see
 *  semanticLegLooksOff. The route degrades silently by design (§7.6.5). */
export const SEMANTIC_OFF_NOTE =
  'The semantic leg is off unless an embedding provider is configured on this host; these results are keyword and recency only.'

/**
 * A heuristic, and labelled as one: the route never says whether it embedded
 * the query, so the only signal available here is the ranking itself. With both
 * legs fused, RRF scores are dense and rarely tie; a pure keyword leg produces
 * the reciprocal-rank ladder 1/(k+1), 1/(k+2), … which has no repeats either —
 * so the honest test is whether ANY row carries a term of the query at all.
 * When several rows do not, the semantic leg is the only thing that could have
 * put them there, and we say nothing; when none of them do, we show the notice.
 */
export function semanticLegLooksOff(rows: MemoryRow[], query: string): boolean {
  const q = query.trim().toLowerCase()
  if (q === '' || rows.length === 0) return false
  const words = q.split(/\s+/).filter((w) => w.length > 2)
  if (words.length === 0) return false
  return !rows.some((row) => {
    const hay = row.snippet.toLowerCase()
    return words.some((w) => hay.includes(w))
  })
}

// ---------------------------------------------------------------------------
// The `name=` convention (§7.5 / design §8)
// ---------------------------------------------------------------------------

/** A named value and its history: the newest row is the current value, every
 *  older row with the same name is superseded, not deleted. */
export interface NamedMemory {
  name: string
  current: MemoryRow
  /** Older rows for the same name, newest first. */
  superseded: MemoryRow[]
}

export interface FoldedMemories {
  /** Rows carrying a `name` label, grouped, in first-seen order. */
  named: NamedMemory[]
  /** Everything else, in the order the server returned it. */
  rest: MemoryRow[]
}

/**
 * Fold the `name=` KV convention: current value first, superseded values
 * beneath it. Appending IS updating here (§7.1), so a browser that showed five
 * rows called `tone-of-voice` would be showing five values where the project
 * has one — the newest.
 *
 * Order within a name is by `created_at` descending, ties broken by id so the
 * fold is deterministic.
 */
export function foldNamedMemories(rows: MemoryRow[]): FoldedMemories {
  const groups = new Map<string, MemoryRow[]>()
  const order: string[] = []
  const rest: MemoryRow[] = []
  for (const row of rows) {
    const name = row.labels.name
    if (!name) {
      rest.push(row)
      continue
    }
    const existing = groups.get(name)
    if (existing) existing.push(row)
    else {
      groups.set(name, [row])
      order.push(name)
    }
  }
  const named: NamedMemory[] = []
  for (const name of order) {
    const rowsForName = [...groups.get(name)!].sort(
      (a, b) => b.created_at - a.created_at || a.id.localeCompare(b.id),
    )
    named.push({ name, current: rowsForName[0], superseded: rowsForName.slice(1) })
  }
  return { named, rest }
}

// ---------------------------------------------------------------------------
// The briefing preview (design §8, engine: go/compose.go BuildBriefingSections)
// ---------------------------------------------------------------------------

/** The heading core puts over the default section (compose.go). */
export const DEFAULT_BRIEFING_HEADING = 'Your memory briefing'

/** The built-in selector every worker gets whether or not it configures any
 *  others (§7.4). Byte-for-byte the engine's RollingSummarySelector. */
export function rollingSummarySelector(worker: string): string {
  return `kind=rolling-summary,worker=${worker}`
}

/** The marker core appends to a section it had to cut (§7.4). Appended AFTER
 *  the cap, so the cap bounds the memory content, not the note. */
export function briefingTruncationMarker(maxBytes: number): string {
  return `\n\n[… briefing section truncated at ${maxBytes} bytes]`
}

/** One section of the preview: the selector that fills it and its heading. */
export interface BriefingSlot {
  selector: string
  heading: string
  /** True for the built-in rolling-summary selector. */
  builtin: boolean
}

/**
 * The selectors core would read for this worker, in the order it reads them
 * (compose.go): the built-in default first, then the worker's own `briefing`
 * entries, deduplicated — a worker that lists the rolling summary explicitly
 * gets one section, not two.
 */
export function briefingSlots(worker: Pick<Worker, 'name' | 'briefing'>): BriefingSlot[] {
  const slots: BriefingSlot[] = []
  const seen = new Set<string>()
  const add = (selector: string, heading: string, builtin: boolean) => {
    if (selector === '' || seen.has(selector)) return
    seen.add(selector)
    slots.push({ selector, heading, builtin })
  }
  if (!worker.name) return slots
  add(rollingSummarySelector(worker.name), DEFAULT_BRIEFING_HEADING, true)
  for (const raw of worker.briefing ?? []) {
    const sel = raw.trim()
    add(sel, `${DEFAULT_BRIEFING_HEADING}: ${sel}`, false)
  }
  return slots
}

/**
 * Apply the §7.4 byte cap to one section, the way core does: UTF-8 bytes (not
 * JS string length — a multi-byte rune costs more than one), cut on a rune
 * boundary, marker appended after the cap.
 *
 * Returns the capped text and where the cut fell, so the preview can show the
 * marker in place rather than only claiming a cut happened.
 */
export function capBriefingContent(
  content: string,
  maxBytes: number,
): { text: string; truncated: boolean; bytes: number } {
  const bytes = new TextEncoder().encode(content)
  if (maxBytes <= 0 || bytes.length <= maxBytes) {
    return { text: content, truncated: false, bytes: bytes.length }
  }
  let cut = maxBytes
  // 0b10xxxxxx is a continuation byte — walk back to the start of the rune.
  while (cut > 0 && (bytes[cut] & 0xc0) === 0x80) cut--
  const head = new TextDecoder().decode(bytes.slice(0, cut))
  return {
    text: head + briefingTruncationMarker(maxBytes),
    truncated: true,
    bytes: bytes.length,
  }
}

/** Rendered under a preview section whose text arrived at the snippet cap: what
 *  the operator is reading was cut by the READ ROUTE before the briefing cap
 *  could apply, so the preview under-states what core would inject. */
export const SNIPPET_CAVEAT = `The browse route returns at most ${MEMORY_SNIPPET_CHARS} characters of a memory, so a longer section is shown short here — core injects the whole thing, up to the byte cap.`
