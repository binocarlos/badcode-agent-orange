// Project settings — the browser-side mirror of `GET/PUT /agent/project-settings`
// (spec docs/product/01-session-config.md §5, engine: go/agentdb/project_settings.go).
//
// Everything here is pure: no React, no window, no fetch. The hook that talks to
// the API is useProjectSettings.ts; the page is components/ProjectSettingsPage.tsx.
//
// Two things this module exists to get right:
//
//  1. **JSON before save.** `mcp_config` and `attention_channel` are free-form
//     JSON objects. The editor must refuse to PUT text that is not a JSON object
//     — a malformed blob would either 400 or, worse, round-trip as a string and
//     silently disable every project MCP server. `parseJsonObject` is the gate.
//  2. **Zero is not "empty".** Four of the numeric settings treat 0 as a real,
//     meaningful value ("off" / "never"); two treat it as "unset" and the server
//     substitutes a default. Getting that backwards in the UI is how a human
//     accidentally uncaps their token spend. `PROJECT_SETTING_NUMERICS` carries
//     the semantics as data so the form renders the truth rather than a guess,
//     and it mirrors `ProjectSettings.normalize()` in the engine exactly.

/** Default endpoint for the project-settings routes (GET and PUT share it). */
export const PROJECT_SETTINGS_ENDPOINT = '/agent/project-settings'

/** Engine defaults — go/agentdb/project_settings.go. Kept in step by eye; the
 *  server is authoritative and its response overwrites these on first load. */
export const DEFAULT_MAX_CONCURRENT_JOBS = 4
export const DEFAULT_BRIEFING_MAX_BYTES = 2048
export const DEFAULT_SNAPSHOT_TTL_DAYS = 30

/** One project's settings row. Field names are the wire names (snake_case). */
export interface ProjectSettings {
  project: string
  base_image: string
  system_prompt: string
  mcp_config: Record<string, unknown>
  attention_channel: Record<string, unknown>
  max_concurrent_jobs: number
  daily_tokens_soft: number
  daily_tokens_hard: number
  briefing_max_bytes: number
  snapshot_ttl_days: number
  updated_at: number
}

/** The settings a project has before anything has been written for it — the
 *  same shape the server hands back for an unwritten project. */
export function defaultProjectSettings(project = ''): ProjectSettings {
  return {
    project,
    base_image: '',
    system_prompt: '',
    mcp_config: {},
    attention_channel: {},
    max_concurrent_jobs: DEFAULT_MAX_CONCURRENT_JOBS,
    daily_tokens_soft: 0,
    daily_tokens_hard: 0,
    briefing_max_bytes: DEFAULT_BRIEFING_MAX_BYTES,
    snapshot_ttl_days: DEFAULT_SNAPSHOT_TTL_DAYS,
    updated_at: 0,
  }
}

/** Fill in anything the server omitted, so the form never binds `undefined`
 *  to a controlled input (React would flip it to uncontrolled mid-edit). */
export function coerceProjectSettings(raw: unknown, project = ''): ProjectSettings {
  const base = defaultProjectSettings(project)
  if (!raw || typeof raw !== 'object') return base
  const r = raw as Record<string, unknown>
  const str = (k: keyof ProjectSettings, d: string) =>
    typeof r[k] === 'string' ? (r[k] as string) : d
  const num = (k: keyof ProjectSettings, d: number) =>
    typeof r[k] === 'number' && Number.isFinite(r[k] as number) ? (r[k] as number) : d
  const obj = (k: keyof ProjectSettings) =>
    r[k] && typeof r[k] === 'object' && !Array.isArray(r[k])
      ? (r[k] as Record<string, unknown>)
      : {}
  return {
    project: str('project', base.project),
    base_image: str('base_image', base.base_image),
    system_prompt: str('system_prompt', base.system_prompt),
    mcp_config: obj('mcp_config'),
    attention_channel: obj('attention_channel'),
    max_concurrent_jobs: num('max_concurrent_jobs', base.max_concurrent_jobs),
    daily_tokens_soft: num('daily_tokens_soft', base.daily_tokens_soft),
    daily_tokens_hard: num('daily_tokens_hard', base.daily_tokens_hard),
    briefing_max_bytes: num('briefing_max_bytes', base.briefing_max_bytes),
    snapshot_ttl_days: num('snapshot_ttl_days', base.snapshot_ttl_days),
    updated_at: num('updated_at', 0),
  }
}

// ---------------------------------------------------------------------------
// JSON editing
// ---------------------------------------------------------------------------

/** Result of parsing an editor's JSON text. Discriminated so callers cannot
 *  read `value` without having checked `ok` first. */
export type JsonParseResult =
  | { ok: true; value: Record<string, unknown> }
  | { ok: false; error: string }

/**
 * Parse the text of a JSON-object editor. Empty (or whitespace-only) text is
 * the empty object — "I cleared the field" must not read as a syntax error.
 * Arrays, strings and numbers are rejected: both settings are name-keyed maps,
 * and a JSON array would be accepted by `JSON.parse` and then silently mean
 * nothing to the server.
 */
export function parseJsonObject(text: string): JsonParseResult {
  const trimmed = text.trim()
  if (trimmed === '') return { ok: true, value: {} }
  let parsed: unknown
  try {
    parsed = JSON.parse(trimmed)
  } catch (err) {
    return { ok: false, error: err instanceof Error ? err.message : 'invalid JSON' }
  }
  if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) {
    return { ok: false, error: 'must be a JSON object, e.g. {"name": {...}}' }
  }
  return { ok: true, value: parsed as Record<string, unknown> }
}

/** Pretty-print an object for a JSON editor. An empty object renders as `{}`
 *  rather than `{\n}` so a fresh project shows a one-line, obviously-empty box. */
export function formatJsonObject(value: unknown): string {
  if (!value || typeof value !== 'object') return '{}'
  if (!Array.isArray(value) && Object.keys(value as object).length === 0) return '{}'
  return JSON.stringify(value, null, 2)
}

// ---------------------------------------------------------------------------
// Numeric settings — the §5 budget/cap fields and their zero semantics
// ---------------------------------------------------------------------------

/**
 * How a numeric setting behaves at zero.
 *   'meaningful' — 0 is a real setting the server stores and honours.
 *   'unset'      — 0 is read as "not supplied" and the server substitutes
 *                  `serverDefault` (ProjectSettings.normalize in the engine).
 */
export type ZeroSemantics = 'meaningful' | 'unset'

export interface NumericSettingSpec {
  /** Wire field name. */
  key:
    | 'max_concurrent_jobs'
    | 'daily_tokens_soft'
    | 'daily_tokens_hard'
    | 'briefing_max_bytes'
    | 'snapshot_ttl_days'
  /** Field label for the form. */
  label: string
  /** What the number counts, shown as the input's suffix. */
  unit: string
  /** One line explaining the setting when the value is non-zero. */
  help: string
  zeroSemantics: ZeroSemantics
  /** The sentence shown *instead of* `help` when the value is 0. */
  zeroHelp: string
  /** Only meaningful when zeroSemantics === 'unset'. */
  serverDefault: number
}

/**
 * The five numeric project settings, in the order the form shows them. The four
 * §5 budget/cap fields plus `max_concurrent_jobs`, which shares their shape.
 *
 * The `zeroSemantics`/`zeroHelp` pairs are a direct transcription of the
 * engine's `normalize()`: daily_tokens_soft/hard and snapshot_ttl_days keep a
 * written 0; max_concurrent_jobs and briefing_max_bytes read 0 as unset because
 * a literal zero there would deadlock the router / delete every briefing.
 */
export const PROJECT_SETTING_NUMERICS: readonly NumericSettingSpec[] = [
  {
    key: 'daily_tokens_soft',
    label: 'Daily token budget — soft',
    unit: 'tokens/day',
    help: 'Crossing this sends one notification to the attention channel per day. Nothing stops.',
    zeroSemantics: 'meaningful',
    zeroHelp: '0 means off — no soft-budget notification is ever sent.',
    serverDefault: 0,
  },
  {
    key: 'daily_tokens_hard',
    label: 'Daily token budget — hard',
    unit: 'tokens/day',
    help: 'Crossing this stops creation of non-interactive jobs until midnight. Interactive chat is never blocked.',
    zeroSemantics: 'meaningful',
    zeroHelp: '0 means off — jobs are never stopped for budget.',
    serverDefault: 0,
  },
  {
    key: 'briefing_max_bytes',
    label: 'Briefing section cap',
    unit: 'bytes/section',
    help: 'Byte cap applied to each injected briefing section independently, at composition time.',
    zeroSemantics: 'unset',
    zeroHelp: `0 is read as "not set" — the server applies its default of ${DEFAULT_BRIEFING_MAX_BYTES} bytes. There is no way to switch briefing off here.`,
    serverDefault: DEFAULT_BRIEFING_MAX_BYTES,
  },
  {
    key: 'snapshot_ttl_days',
    label: 'Snapshot TTL',
    unit: 'days',
    help: 'The reaper deletes a snapshot image this many days after it was created.',
    zeroSemantics: 'meaningful',
    zeroHelp: '0 means never — snapshots are kept forever and no reaper touches them.',
    serverDefault: 0,
  },
  {
    key: 'max_concurrent_jobs',
    label: 'Max concurrent jobs',
    unit: 'jobs',
    help: 'Router/scheduler concurrency cap for the whole project.',
    zeroSemantics: 'unset',
    zeroHelp: `0 is read as "not set" — the server applies its default of ${DEFAULT_MAX_CONCURRENT_JOBS}. Use 1 to serialise the project.`,
    serverDefault: DEFAULT_MAX_CONCURRENT_JOBS,
  },
]

/**
 * The helper line to show under a numeric field for the value currently typed —
 * the whole point of the spec's "0 means…" wording being legible to a human.
 */
export function describeNumericSetting(spec: NumericSettingSpec, value: number): string {
  if (value === 0) return spec.zeroHelp
  return spec.help
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

/** Field name → human-readable problem. Empty object means "safe to save". */
export type FieldErrors = Record<string, string>

/**
 * Client-side validation. Mirrors the engine's rules (negatives rejected,
 * everything else permitted) rather than inventing stricter ones — the server
 * stays authoritative, this just avoids a pointless round-trip and gives the
 * human the error next to the field instead of in a toast.
 */
export function validateProjectSettings(s: ProjectSettings): FieldErrors {
  const errors: FieldErrors = {}
  for (const spec of PROJECT_SETTING_NUMERICS) {
    const v = s[spec.key]
    if (!Number.isFinite(v) || !Number.isInteger(v)) {
      errors[spec.key] = 'must be a whole number'
    } else if (v < 0) {
      errors[spec.key] = 'must not be negative'
    }
  }
  return errors
}

/**
 * The PUT body. `project` and `updated_at` are dropped: the server takes the
 * project from the JWT and stamps the timestamp itself, and sending our own
 * values invites the illusion that either is client-settable.
 *
 * `rationale` is the operator's one-line reason, threaded into the config event
 * (design B3 / K2), and omitted when empty so an absent reason reads as absent
 * — the same contract `scheduleBody` has.
 */
export function projectSettingsBody(
  s: ProjectSettings,
  rationale = '',
): Omit<ProjectSettings, 'project' | 'updated_at'> & { rationale?: string } {
  const { project: _project, updated_at: _updatedAt, ...body } = s
  return rationale.trim() === '' ? body : { ...body, rationale: rationale.trim() }
}
