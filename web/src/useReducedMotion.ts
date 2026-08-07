// usePrefersReducedMotion — the one place the console asks whether to move.
//
// Doc 21 §4.1's SMIL trap is why this is a hook and not a CSS media query:
// CSS cannot pause `<animateMotion>`, so every animation in the console has to
// be gateable from JS. A CSS-only gate would work for the transitions in this
// file's callers and then silently fail for the chart's pulses — one gate,
// used everywhere, is the version that stays honest.
//
// The reduced-motion branch is never "less": a transition is replaced by the
// same end state applied instantly (§5 — every animation has a static
// equivalent), never by removing the information the motion carried.

import { useEffect, useState } from 'react'

/** The media query, exported so tests and hosts can mock exactly this string. */
export const REDUCED_MOTION_QUERY = '(prefers-reduced-motion: reduce)'

function currentlyReduced(): boolean {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return false
  try {
    return window.matchMedia(REDUCED_MOTION_QUERY).matches
  } catch {
    // A host (or jsdom) with a stub matchMedia: motion is the safe default,
    // because the static equivalents are always rendered too.
    return false
  }
}

/**
 * True when the operator has asked for reduced motion. Reacts to the setting
 * changing mid-session (a real thing on macOS and Windows).
 */
export function usePrefersReducedMotion(): boolean {
  const [reduced, setReduced] = useState<boolean>(currentlyReduced)

  useEffect(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return
    let mql: MediaQueryList
    try {
      mql = window.matchMedia(REDUCED_MOTION_QUERY)
    } catch {
      return
    }
    const onChange = () => setReduced(mql.matches)
    onChange()
    if (typeof mql.addEventListener === 'function') {
      mql.addEventListener('change', onChange)
      return () => mql.removeEventListener('change', onChange)
    }
    // Safari <14 and some jsdom stubs.
    if (typeof mql.addListener === 'function') {
      mql.addListener(onChange)
      return () => mql.removeListener(onChange)
    }
    return
  }, [])

  return reduced
}

export default usePrefersReducedMotion
