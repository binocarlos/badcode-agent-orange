// DeliveryStatusChip — one chip for a delivery status, everywhere (doc 21, X11).
//
// Painted from the console's own palette, not MUI's semantic buckets, because
// the design's palette has no green: §3.3 names exactly four working colours
// (ember = an agent, steel = instrument, rose = waiting on you, fault = broken)
// and spends none of them on "normal". A job table where most rows are a loud
// success green spends the reader's attention on the eight rows that are fine
// and leaves nothing for the two that are not — the opposite of what the Desk's
// three stacks are for.
//
// So the mapping is:
//
//   ok             quiet, outlined, neutral  — finished is the expected case
//   pending        quiet, outlined, neutral  — nothing has happened to it yet
//   running        ember, filled             — matches the chart's running dot
//   awaiting_human rose, filled              — a pause, never an alarm colour
//   rate_limited   fault, OUTLINED           — work was dropped, but nothing broke
//   failed         fault, filled             — the one thing that should shout
//
// Colours resolve through spine.tsx's `consoleTokenColor`, so a host that
// declares `palette.ember`/`rose`/`fault` gets its own values and `web/` never
// imports the host's augmentation. Anything outside the six-status vocabulary
// falls back to MUI's bucket rather than guessing.

import { Chip, Tooltip, type Theme } from '@mui/material'
import {
  describeDeliveryStatus,
  deliveryStatusSeverity,
  isDeliveryStatus,
} from '../events.js'
import { consoleTokenColor, type ConsoleTokenName } from '../spine.js'
import { statusCrossfadeSx, type ConsoleSx } from '../feedhighlight.js'
import usePrefersReducedMotion from '../useReducedMotion.js'

/** How each status is painted. `null` token ⇒ quiet neutral, no console colour. */
const STATUS_PAINT: Record<string, { token: ConsoleTokenName | null; filled: boolean }> = {
  ok: { token: null, filled: false },
  pending: { token: null, filled: false },
  running: { token: 'ember', filled: true },
  awaiting_human: { token: 'rose', filled: true },
  rate_limited: { token: 'fault', filled: false },
  failed: { token: 'fault', filled: true },
}

/** The rose a waiting chip is painted with — kept for callers that tint to
 *  match it (the Desk's ask rows). */
export function attentionColor(theme: Theme): string {
  return consoleTokenColor(theme, 'rose')
}

export interface DeliveryStatusChipProps {
  /** One of the six statuses the engine writes. */
  status: string
  /** Wrap in the status's explanatory sentence. Default true. */
  describe?: boolean
}

export default function DeliveryStatusChip({ status, describe = true }: DeliveryStatusChipProps) {
  const reduced = usePrefersReducedMotion()
  const paint = isDeliveryStatus(status) ? STATUS_PAINT[status] : undefined

  // `key={status}` remounts the chip when the status lands, so the 140ms
  // crossfade runs on the transition and never on an ordinary re-render — the
  // chip's change IS the payload of a status transition (§4.2).
  const sx: ConsoleSx[] = []
  if (paint?.token) {
    sx.push(
      paint.filled
        ? {
            bgcolor: (theme: Theme) => consoleTokenColor(theme, paint.token as ConsoleTokenName),
            color: (theme: Theme) =>
              theme.palette.getContrastText(consoleTokenColor(theme, paint.token as ConsoleTokenName)),
          }
        : {
            color: (theme: Theme) => consoleTokenColor(theme, paint.token as ConsoleTokenName),
            borderColor: (theme: Theme) => consoleTokenColor(theme, paint.token as ConsoleTokenName),
          },
    )
  } else if (paint) {
    // Quiet: the chip is a label, not a signal.
    sx.push({ color: 'text.secondary', borderColor: 'divider' })
  }
  sx.push(statusCrossfadeSx(reduced))

  const chip = (
    <Chip
      key={status}
      size="small"
      label={status}
      data-status={status}
      // A console-painted status takes no MUI bucket; an unknown one still does.
      color={paint ? undefined : deliveryStatusSeverity(status)}
      variant={paint && !paint.filled ? 'outlined' : paint ? 'filled' : 'filled'}
      sx={sx}
    />
  )
  return describe ? <Tooltip title={describeDeliveryStatus(status)}>{chip}</Tooltip> : chip
}
