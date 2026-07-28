// FeedLiveness — the three small pieces every auto-updating list on this
// console shares (doc 21 §4.2, §5-M4): the waterline, the "N new" pill, and the
// pause toggle.
//
// The pill is not a nicety. WCAG 2.2.2 (Pause, Stop, Hide) applies to a list
// that inserts rows on its own, and staging arrivals behind a pill IS the pause
// mechanism — X removed auto-refresh because "tweets would disappear from view
// mid-read", and every log live-tail since has landed on the same shape. The
// explicit `Pause live updates` toggle is the second half of that obligation,
// and it is offered to everyone, not only to reduced-motion users.
//
// A11y, per §4.2: the pill is `role="status"` carrying ONE debounced summary
// ("3 new deliveries, 1 failed"), never a per-item announcement. The waterline
// is a `<hr>`-shaped separator with its own label, so a screen reader reaching
// it hears where the news begins instead of stepping over a decoration.

import { useEffect, useRef, useState } from 'react'
import { Box, Button, FormControlLabel, Switch, Typography, type Theme } from '@mui/material'
import { consoleTokenColor } from '../spine.js'
import usePrefersReducedMotion from '../useReducedMotion.js'

// ---------------------------------------------------------------------------
// The waterline
// ---------------------------------------------------------------------------

export interface FeedWaterlineProps {
  /** `New since 14:32` — build it with `waterlineLabel(markMs)`. */
  label: string
  /** Rendered inside an `<ol>`; pass 'li' so the list stays valid. */
  component?: 'div' | 'li'
}

/**
 * A full-width rule labelled with the moment the operator last looked.
 *
 * Frozen for the visit by construction: it renders where `waterlineIndex` puts
 * it, and that index is computed from a mark that does not move until the
 * operator marks the surface seen. A divider that chased new arrivals down the
 * page would be a decoration.
 */
export function FeedWaterline({ label, component = 'div' }: FeedWaterlineProps) {
  return (
    <Box
      component={component}
      role="separator"
      aria-label={label}
      data-testid="feed-waterline"
      sx={{
        display: 'flex',
        alignItems: 'center',
        gap: 1,
        my: 1.5,
        listStyle: 'none',
        color: (theme: Theme) => consoleTokenColor(theme, 'ember'),
      }}
    >
      <Box sx={{ flex: '0 0 auto', fontSize: 11, letterSpacing: '0.08em', textTransform: 'uppercase' }}>
        {label}
      </Box>
      <Box sx={{ flex: 1, height: '1px', bgcolor: 'currentColor', opacity: 0.5 }} />
    </Box>
  )
}

// ---------------------------------------------------------------------------
// The "N new" pill
// ---------------------------------------------------------------------------

/** How long the summary waits before it settles, so a burst announces once. */
export const PILL_DEBOUNCE_MS = 700

export interface NewItemsPillProps {
  /** How many are staged. 0 renders nothing at all. */
  count: number
  /** The one sentence — build it with `newItemsSummary`. */
  summary: string
  /** Flush the staging buffer into the list. */
  onShow: () => void
  /** Milliseconds the summary is debounced by. Default PILL_DEBOUNCE_MS. */
  debounceMs?: number
}

/**
 * The pinned "N new" pill.
 *
 * Two audiences, one element: sighted operators get a button that inserts the
 * staged rows when THEY choose, and screen readers get one `role="status"`
 * summary, debounced, so a burst of eleven arrivals is one announcement rather
 * than eleven interruptions.
 *
 * The debounce is why the announced text is separate state from `count`: the
 * button must update instantly (it is a control), the announcement must not.
 */
export function NewItemsPill({
  count,
  summary,
  onShow,
  debounceMs = PILL_DEBOUNCE_MS,
}: NewItemsPillProps) {
  const [announced, setAnnounced] = useState('')
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(() => {
    if (timer.current !== null) clearTimeout(timer.current)
    if (summary === '') {
      setAnnounced('')
      return
    }
    timer.current = setTimeout(() => setAnnounced(summary), debounceMs)
    return () => {
      if (timer.current !== null) clearTimeout(timer.current)
    }
  }, [debounceMs, summary])

  return (
    <>
      {/* Always mounted: a live region inserted at the moment it gains text is
          a live region screen readers may never announce. */}
      <Box role="status" aria-live="polite" sx={visuallyHidden}>
        {announced}
      </Box>
      {count > 0 && (
        <Box sx={{ display: 'flex', justifyContent: 'center', py: 0.5 }}>
          <Button
            size="small"
            variant="outlined"
            onClick={onShow}
            data-testid="new-items-pill"
            sx={{ borderRadius: 999, textTransform: 'none' }}
          >
            {summary}
          </Button>
        </Box>
      )}
    </>
  )
}

const visuallyHidden = {
  position: 'absolute',
  width: 1,
  height: 1,
  overflow: 'hidden',
  clip: 'rect(0 0 0 0)',
  whiteSpace: 'nowrap',
} as const

// ---------------------------------------------------------------------------
// Auto-flush at head
// ---------------------------------------------------------------------------

/**
 * True while the top of the list is on screen.
 *
 * IntersectionObserver, not `scrollTop`: the research is explicit that the
 * scroll-position version gets the sticky-header and zoom cases wrong, and this
 * decides whether rows are allowed to insert under the operator's eyes. A host
 * without IntersectionObserver (jsdom, older browsers) gets `true` — auto-flush
 * on, which is the pre-W4 behaviour and never loses an item.
 */
export function useAtHead(ref: { current: Element | null }): boolean {
  const [atHead, setAtHead] = useState(true)

  useEffect(() => {
    const node = ref.current
    if (!node) return
    if (typeof IntersectionObserver !== 'function') return
    const observer = new IntersectionObserver(
      (entries) => {
        const entry = entries[entries.length - 1]
        if (entry) setAtHead(entry.isIntersecting)
      },
      { threshold: 0 },
    )
    observer.observe(node)
    return () => observer.disconnect()
  }, [ref])

  return atHead
}

// ---------------------------------------------------------------------------
// The pause toggle
// ---------------------------------------------------------------------------

export interface PauseLiveUpdatesProps {
  paused: boolean
  onChange: (paused: boolean) => void
  /**
   * What this surface would do if it were not paused, in the operator's words.
   * Rendered as the caption, so the toggle never claims to pause something that
   * is not happening.
   */
  caption?: string
}

/**
 * `Pause live updates` — WCAG 2.2.2's mechanism, for everyone.
 *
 * The switch is deliberately not tied to `prefers-reduced-motion`: reduced
 * motion is about animation, this is about content changing under you, and the
 * research found the second is the one that actually loses an operator's place.
 * The hook is exported beside it because a reduced-motion operator arriving on
 * a live surface should start paused.
 */
export function PauseLiveUpdates({ paused, onChange, caption }: PauseLiveUpdatesProps) {
  return (
    <Box>
      <FormControlLabel
        control={
          <Switch
            size="small"
            checked={paused}
            onChange={(e) => onChange(e.target.checked)}
            inputProps={{ 'aria-label': 'Pause live updates' }}
          />
        }
        label={<Typography variant="caption">Pause live updates</Typography>}
      />
      {caption !== undefined && caption !== '' && (
        <Typography variant="caption" color="text.secondary" sx={{ display: 'block' }}>
          {caption}
        </Typography>
      )}
    </Box>
  )
}

/** Should a surface start paused? Yes when the operator asked for less motion. */
export function useDefaultPaused(): boolean {
  return usePrefersReducedMotion()
}

export default FeedWaterline
