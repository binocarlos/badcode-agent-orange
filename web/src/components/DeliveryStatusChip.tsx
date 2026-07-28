// DeliveryStatusChip — one chip for a delivery status, everywhere (doc 21, X11).
//
// The design says `awaiting_human` is ROSE and never an alarm colour: a worker
// waiting for a person is a pause, not a fault. MUI's `Chip color` prop cannot
// say rose — it knows five semantic buckets and none of them is a fourth named
// console colour — so this chip paints that one status from the theme with
// `sx`, resolving `palette.rose` where the host declares it and falling back to
// the design token otherwise. That is spine.tsx's pattern, and `consoleColor`
// is spine.tsx's function: `web/` never imports the host's augmentation.
//
// Every other status keeps its ordinary MUI bucket, so the chips stay legible
// in any host theme and only the one the design overrules is overruled.

import { Chip, Tooltip, type SxProps, type Theme } from '@mui/material'
import { describeDeliveryStatus, deliveryStatusSeverity } from '../events.js'
import { consoleColor } from '../spine.js'
import { statusCrossfadeSx } from '../feedhighlight.js'
import usePrefersReducedMotion from '../useReducedMotion.js'

/** The design's §3.3 rose, per mode — the fallback when a host theme has no
 *  named entry. Kept in step with spine.tsx's copy of the token table. */
const ROSE = { light: '#A6376A', dark: '#DF7BA4' } as const

/** The rose a chip is painted with under one theme. */
export function attentionColor(theme: Theme): string {
  return consoleColor(theme, 'rose', ROSE[theme.palette.mode === 'dark' ? 'dark' : 'light'])
}

export interface DeliveryStatusChipProps {
  /** One of the six statuses the engine writes. */
  status: string
  /** Wrap in the status's explanatory sentence. Default true. */
  describe?: boolean
}

/**
 * The status chip. `awaiting_human` is rose and filled; the rest take the MUI
 * bucket, outlined only for `pending` (nothing has happened to it yet).
 */
export default function DeliveryStatusChip({ status, describe = true }: DeliveryStatusChipProps) {
  const attention = status === 'awaiting_human'
  const reduced = usePrefersReducedMotion()
  // `key={status}` remounts the chip when the status lands, so the 140ms
  // crossfade runs on the transition and never on an ordinary re-render — the
  // chip's change IS the payload of a status transition (§4.2).
  const paint: SxProps<Theme> | undefined = attention
    ? {
        bgcolor: (theme: Theme) => attentionColor(theme),
        color: (theme: Theme) => theme.palette.getContrastText(attentionColor(theme)),
      }
    : undefined
  const chip = (
    <Chip
      key={status}
      size="small"
      label={status}
      data-status={status}
      color={attention ? undefined : deliveryStatusSeverity(status)}
      variant={status === 'pending' ? 'outlined' : 'filled'}
      sx={[
        ...(paint ? [paint] : []),
        statusCrossfadeSx(reduced),
      ]}
    />
  )
  return describe ? <Tooltip title={describeDeliveryStatus(status)}>{chip}</Tooltip> : chip
}
