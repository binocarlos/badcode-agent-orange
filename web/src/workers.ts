// Workers — the browser-side mirror of the `/agent/workers` CRUD routes
// (spec docs/product/02-workers.md §6, engine: go/agentdb/workers.go,
// go/httpapi/workers.go).
//
// Pure: no React, no window, no fetch. The hooks live in useWorkers.ts and the
// components in components/Worker*.tsx.
//
// Validation policy: mirror the engine, do not out-legislate it. The server
// enforces exactly three things — kebab-case name, max_instances >= 1, no blank
// briefing selector — so this module enforces exactly those, plus a shape check
// on the image reference (§13's `name` / `name:version` grammar, which has no
// server-side validator yet because Track I is unbuilt). Anything stricter here
// would make the UI reject rows the API would happily accept.

/** Endpoint paths for the worker routes. Overridable per host, like DEFAULT_ENDPOINTS. */
export const WORKER_ENDPOINTS = {
  list: '/agent/workers',
  one: (name: string) => `/agent/workers/${encodeURIComponent(name)}`,
}

/** max_instances a worker gets when the caller does not choose one (§6.1). */
export const DEFAULT_MAX_INSTANCES = 1

/** The plain sentence the lock badge carries, everywhere it appears
 *  (10-topology-library.md §3) — one string, so the list, the editor and any
 *  host surface say exactly the same thing. */
export const FROZEN_SENTENCE = 'Frozen — cannot be changed by other workers.'

/** One configured worker. Field names are the wire names (snake_case).
 *  `briefing` is null-preserving: null/absent = "no selectors configured",
 *  [] = "an explicitly empty list" — the engine keeps those distinct. */
export interface Worker {
  project: string
  name: string
  description: string
  system_prompt: string
  mcp_config: Record<string, unknown>
  image: string
  briefing: string[] | null
  max_instances: number
  enabled: boolean
  /** Frozen — cannot be changed by other workers; only humans, through this
   *  (JWT-guarded) API, may edit or unfreeze it. The causal-isolation
   *  primitive for measurement instruments (10-topology-library.md §3). */
  frozen: boolean
  created_at: number
  updated_at: number
}

/** A worker being edited. Identical shape; the alias exists so component props
 *  read honestly ("this is unsaved") without a structurally different type. */
export type WorkerDraft = Worker

/** A blank worker with the spec's defaults — max_instances 1, enabled true. */
export function newWorkerDraft(project = ''): WorkerDraft {
  return {
    project,
    name: '',
    description: '',
    system_prompt: '',
    mcp_config: {},
    image: '',
    briefing: null,
    max_instances: DEFAULT_MAX_INSTANCES,
    enabled: true,
    frozen: false,
    created_at: 0,
    updated_at: 0,
  }
}

/** Fill anything the server omitted so the editor never binds `undefined` to a
 *  controlled input. `briefing` deliberately stays null when absent. */
export function coerceWorker(raw: unknown, project = ''): Worker {
  const base = newWorkerDraft(project)
  if (!raw || typeof raw !== 'object') return base
  const r = raw as Record<string, unknown>
  return {
    project: typeof r.project === 'string' ? r.project : base.project,
    name: typeof r.name === 'string' ? r.name : '',
    description: typeof r.description === 'string' ? r.description : '',
    system_prompt: typeof r.system_prompt === 'string' ? r.system_prompt : '',
    mcp_config:
      r.mcp_config && typeof r.mcp_config === 'object' && !Array.isArray(r.mcp_config)
        ? (r.mcp_config as Record<string, unknown>)
        : {},
    image: typeof r.image === 'string' ? r.image : '',
    briefing: Array.isArray(r.briefing) ? (r.briefing as unknown[]).map(String) : null,
    max_instances:
      typeof r.max_instances === 'number' && Number.isFinite(r.max_instances)
        ? r.max_instances
        : DEFAULT_MAX_INSTANCES,
    enabled: typeof r.enabled === 'boolean' ? r.enabled : true,
    frozen: typeof r.frozen === 'boolean' ? r.frozen : false,
    created_at: typeof r.created_at === 'number' ? r.created_at : 0,
    updated_at: typeof r.updated_at === 'number' ? r.updated_at : 0,
  }
}

// ---------------------------------------------------------------------------
// Identity
// ---------------------------------------------------------------------------

/** Kebab-case worker identity — the exact regexp the engine enforces. */
export const WORKER_NAME_PATTERN = /^[a-z0-9]+(-[a-z0-9]+)*$/

/** null when the name is acceptable, else the reason it is not. */
export function validateWorkerName(name: string): string | null {
  if (name === '') return 'name is required'
  if (!WORKER_NAME_PATTERN.test(name)) {
    return 'must be kebab-case: lowercase letters, digits, single hyphens (e.g. email-answerer)'
  }
  return null
}

// ---------------------------------------------------------------------------
// Image references (§13.3)
// ---------------------------------------------------------------------------

/** A parsed image reference. `version === null` means the bare, floating form:
 *  resolve to the latest version at launch time. */
export interface ImageRef {
  name: string
  version: number | null
}

/** Image-name grammar. Intentionally permissive (no server-side validator
 *  exists yet — Track I): a single segment of lowercase alphanumerics with
 *  internal `.`, `_` or `-`. Rejects whitespace, `:` and slashes, which are the
 *  mistakes that actually happen (pasting a full registry URL). */
const IMAGE_NAME_PATTERN = /^[a-z0-9]([a-z0-9._-]*[a-z0-9])?$/

/**
 * Parse `name` or `name:version`. Returns null for the empty string (a worker
 * with no image inherits `project_settings.base_image`) and for anything that
 * is not a legal reference — callers that need the reason use validateImageRef.
 */
export function parseImageRef(ref: string): ImageRef | null {
  const trimmed = ref.trim()
  if (trimmed === '') return null
  const colon = trimmed.indexOf(':')
  if (colon === -1) {
    return IMAGE_NAME_PATTERN.test(trimmed) ? { name: trimmed, version: null } : null
  }
  const name = trimmed.slice(0, colon)
  const rawVersion = trimmed.slice(colon + 1)
  if (!IMAGE_NAME_PATTERN.test(name)) return null
  if (!/^[0-9]+$/.test(rawVersion)) return null
  const version = Number(rawVersion)
  if (version < 1) return null
  return { name, version }
}

/** null when the reference is acceptable (including empty), else the reason. */
export function validateImageRef(ref: string): string | null {
  if (ref.trim() === '') return null
  if (parseImageRef(ref) === null) {
    return 'must be `name` (latest) or `name:version` (pinned), version a positive integer'
  }
  return null
}

/** How the reference will resolve at launch, in words — the image picker's
 *  helper text, so "floating vs pinned" is visible before a job runs. */
export function describeImageRef(ref: string, projectBaseImage = ''): string {
  const parsed = parseImageRef(ref)
  if (parsed === null) {
    if (ref.trim() === '') {
      return projectBaseImage
        ? `Unset — jobs launch from the project base image (${projectBaseImage}).`
        : 'Unset — jobs launch from the project base image, else the global default.'
    }
    return 'Not a valid image reference.'
  }
  if (parsed.version === null) {
    return `Floating: resolves to the latest version of "${parsed.name}" at launch time.`
  }
  return `Pinned: always version ${parsed.version} of "${parsed.name}".`
}

// ---------------------------------------------------------------------------
// Briefing selectors (§6.5 / §7.4)
// ---------------------------------------------------------------------------

/**
 * The engine's only rule for a briefing selector is that it is not blank — the
 * label-selector grammar itself (`=`, `!=`, `in`, `notin`, `exists`, `!`) is
 * parsed server-side at composition time. Re-implementing that parser here
 * would create a second source of truth that drifts, so this checks blankness
 * only and lets the server own the grammar.
 */
export function validateSelector(selector: string): string | null {
  if (selector.trim() === '') return 'selector must not be empty'
  return null
}

// ---------------------------------------------------------------------------
// Validation + wire body
// ---------------------------------------------------------------------------

/** Field name → human-readable problem. Briefing entries key as `briefing.<i>`. */
export type WorkerFieldErrors = Record<string, string>

export function validateWorker(w: WorkerDraft): WorkerFieldErrors {
  const errors: WorkerFieldErrors = {}
  const nameErr = validateWorkerName(w.name)
  if (nameErr) errors.name = nameErr
  const imageErr = validateImageRef(w.image)
  if (imageErr) errors.image = imageErr
  if (!Number.isInteger(w.max_instances)) errors.max_instances = 'must be a whole number'
  else if (w.max_instances < 1) errors.max_instances = 'must be at least 1'
  ;(w.briefing ?? []).forEach((sel, i) => {
    const err = validateSelector(sel)
    if (err) errors[`briefing.${i}`] = err
  })
  return errors
}

/**
 * The PUT body. `project`, `name` and the timestamps are dropped: the project
 * comes from the JWT and the name from the path, so echoing them back would
 * suggest the body could change them (it cannot). The image is trimmed —
 * trailing whitespace in an image pointer is a silent resolution failure.
 */
export function workerBody(w: WorkerDraft): {
  description: string
  system_prompt: string
  mcp_config: Record<string, unknown>
  image: string
  max_instances: number
  briefing: string[] | null
  enabled: boolean
  frozen: boolean
} {
  return {
    description: w.description,
    system_prompt: w.system_prompt,
    mcp_config: w.mcp_config ?? {},
    image: w.image.trim(),
    max_instances: w.max_instances,
    briefing: w.briefing,
    enabled: w.enabled,
    // Always sent explicitly: PUT is create-or-replace, and an omitted frozen
    // reads as false server-side — an accidental unfreeze, silently.
    frozen: w.frozen,
  }
}

// ---------------------------------------------------------------------------
// Router-free selection in the URL
// ---------------------------------------------------------------------------
//
// The worker a human is looking at belongs in the address bar, but this package
// must not impose a router (F3's rule). So: one query parameter, read and
// written through the History API by the component, with the parsing kept pure
// and tested here. A host with its own router ignores all of this and drives
// the components with the controlled `selected`/`onSelect` props instead.

/** Query parameter naming the selected worker, e.g. `?worker=email-answerer`. */
export const WORKER_QUERY_PARAM = 'worker'

/** The worker named by a query string, or null. Accepts `?a=b` or `a=b`. */
export function workerFromSearch(search: string): string | null {
  if (!search) return null
  const params = new URLSearchParams(search.startsWith('?') ? search.slice(1) : search)
  const name = params.get(WORKER_QUERY_PARAM)
  return name ? name : null
}

/**
 * `search` with the worker parameter set to `name`, or removed when name is
 * null. Other parameters are preserved — the settings page, a filter, whatever
 * else the host has in the URL must survive selecting a worker.
 */
export function buildWorkerSearch(search: string, name: string | null): string {
  const params = new URLSearchParams(search.startsWith('?') ? search.slice(1) : search)
  if (name === null) params.delete(WORKER_QUERY_PARAM)
  else params.set(WORKER_QUERY_PARAM, name)
  const out = params.toString()
  return out === '' ? '' : `?${out}`
}
