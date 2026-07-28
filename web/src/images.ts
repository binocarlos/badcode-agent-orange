// Images — the browser-side mirror of the read-only `/agent/images` route
// (operator-console design B4; engine: go/httpapi/catalogue.go, catalogue:
// docs/product/08-images-and-skills.md §13).
//
// Pure: no React, no window, no fetch. The hook lives in useImages.ts.
//
// Read-only on purpose, all the way down. A version is burned from INSIDE a
// session (`image_create`) and is never overwritten, so there is nothing here
// that creates, edits or deletes one — the browser has no container to
// snapshot.
//
// The catalogue makes the worker editor's image field better-informed, not
// narrower: §13.3 says a bare registry reference is a legal value, so the
// options are suggestions for an autocomplete that still accepts free text.

/** Endpoint paths for the catalogue routes. Overridable per host, like
 *  WORKER_ENDPOINTS. */
export const IMAGE_ENDPOINTS = {
  list: '/agent/images',
}

/** One catalogue version. Field names are the wire names (snake_case).
 *  Deliberately no registry handle: it is how the engine finds the bytes. */
export interface ProjectImage {
  name: string
  /** The second half of the §13.2 identity `name:version`; starts at 1. */
  version: number
  labels: Record<string, string>
  created_by_worker: string
  created_by_session: string
  created_at: number
}

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

export function coerceImage(raw: unknown): ProjectImage {
  const base: ProjectImage = {
    name: '',
    version: 0,
    labels: {},
    created_by_worker: '',
    created_by_session: '',
    created_at: 0,
  }
  if (!raw || typeof raw !== 'object') return base
  const r = raw as Record<string, unknown>
  return {
    name: str(r.name),
    version: num(r.version),
    labels: coerceLabels(r.labels),
    created_by_worker: str(r.created_by_worker),
    created_by_session: str(r.created_by_session),
    created_at: num(r.created_at),
  }
}

/**
 * The picker's suggestions, in the order the catalogue came back (newest
 * first): each distinct NAME once, unpinned.
 *
 * Bare names and not `name:version`, because that is what an operator almost
 * always wants — a worker pointed at a bare name picks up the newest burn at
 * launch (§13.3), and offering every pin would bury that behind a list that
 * grows with every snapshot. Pinning stays available: the field is free text.
 */
export function imageOptionsFrom(images: ProjectImage[]): string[] {
  const seen = new Set<string>()
  const out: string[] = []
  for (const img of images) {
    if (!img.name || seen.has(img.name)) continue
    seen.add(img.name)
    out.push(img.name)
  }
  return out
}
