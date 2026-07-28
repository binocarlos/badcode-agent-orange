// EventJobHistory — the job-history spine (§8.4 step 2, learning L29): event,
// worker, duration, status (including awaiting_human), tokens, session link.
//
// Two honesty notes, both visible in the UI rather than only here:
//
//   - `awaiting_human` is a pause, not an end. The engine leaves `ended_at`
//     unset for it, so the duration keeps counting and the row must not read as
//     finished.
//   - Tokens are not on the delivery row and there is no token-summary route,
//     so each cell costs one `GET /agent/session/{id}/query-events`. Only the
//     first `tokenAutoLoad` rows fetch on their own; the rest offer a button.
//     A table that silently fired a hundred requests would be worse than a
//     table that asks.
//
// Session links use buildSessionPath (F3), so a job is shareable, and a host
// with its own router intercepts the click with onOpenSession.

import React, { useRef } from 'react'
import {
  Alert,
  Box,
  Link,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
  Tooltip,
  Typography,
} from '@mui/material'
import {
  deliveryDurationSeconds,
  deliveryStatusSeverity,
  formatDuration,
  formatTimestamp,
  formatTokens,
  type JobRow,
} from '../events.js'
import DeliveryStatusChip from './DeliveryStatusChip.js'
import { buildSessionPath } from '../permalink.js'
import { useSessionTokens } from '../useEvents.js'
import type { ConfigApiOptions } from '../configApi.js'
import { diffStatuses, statusPulseSx } from '../feedhighlight.js'
import { coarseAgeLabel } from '../useElapsedTicker.js'
import usePrefersReducedMotion from '../useReducedMotion.js'

export interface EventJobHistoryProps extends ConfigApiOptions {
  jobs: JobRow[]
  projectId: string
  onOpenSession?: (sessionId: string) => void
  /** Heading. Pass '' for none. */
  title?: string
  /** How many rows fetch their token totals unprompted. Default 10. */
  tokenAutoLoad?: number
  /** Shown above the table when the fetched page was full. */
  truncated?: boolean
  /**
   * The page's shared clock, in unix SECONDS. When given, an unfinished row's
   * duration is recomputed against it, so a `running` job counts and a finished
   * one does not (doc 21 §4.2 — a ticking clock on a finished thing is a bug).
   * Omitted, the row shows whatever `buildJobRows` computed at fetch time.
   */
  nowSeconds?: number
}

export default function EventJobHistory({
  jobs,
  projectId,
  onOpenSession,
  title = 'Jobs',
  tokenAutoLoad = 10,
  truncated = false,
  nowSeconds,
  ...apiOptions
}: EventJobHistoryProps) {
  const reduced = usePrefersReducedMotion()

  // The table is already a PROJECTION — `buildJobRows` emits one row per
  // delivery id and sorts by `created_at`, never by "recently updated" — which
  // is §4.2's precondition for animating a status at all: the row stays where
  // it is and its chip changes in place. What was missing was the transition
  // itself, so this holds the previous status per delivery and pulses the rows
  // that landed somewhere new.
  const previous = useRef(new Map<string, string>())
  const { next, changed } = diffStatuses(
    previous.current,
    jobs.map((job) => ({ id: job.delivery.id, status: job.status })),
  )
  previous.current = next

  return (
    <Box>
      {title !== '' && (
        <Typography variant="subtitle2" sx={{ mb: 1.5 }}>
          {title}
        </Typography>
      )}

      {truncated && (
        <Alert severity="info" sx={{ mb: 2 }}>
          Showing the most recent page only — older jobs may not be listed.
        </Alert>
      )}

      {jobs.length === 0 ? (
        <Typography variant="body2" color="text.secondary">
          No jobs yet.
        </Typography>
      ) : (
        <Table size="small" aria-label="Job history">
          <TableHead>
            <TableRow>
              <TableCell>Event</TableCell>
              <TableCell>Worker</TableCell>
              <TableCell>Started</TableCell>
              <TableCell>Duration</TableCell>
              <TableCell>Status</TableCell>
              <TableCell align="right">Tokens</TableCell>
              <TableCell>Session</TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {jobs.map((job, index) => (
              <JobHistoryRow
                key={job.delivery.id}
                job={job}
                projectId={projectId}
                onOpenSession={onOpenSession}
                autoLoadTokens={index < tokenAutoLoad}
                nowSeconds={nowSeconds}
                justChanged={changed.has(job.delivery.id)}
                reduced={reduced}
                {...apiOptions}
              />
            ))}
          </TableBody>
        </Table>
      )}
    </Box>
  )
}

interface JobHistoryRowProps extends ConfigApiOptions {
  job: JobRow
  projectId: string
  onOpenSession?: (sessionId: string) => void
  autoLoadTokens: boolean
  nowSeconds?: number
  justChanged?: boolean
  reduced?: boolean
}

function JobHistoryRow({
  job,
  projectId,
  onOpenSession,
  autoLoadTokens,
  nowSeconds,
  justChanged = false,
  reduced = false,
  ...apiOptions
}: JobHistoryRowProps) {
  const running = job.status === 'running' || job.status === 'awaiting_human'
  // Recompute against the shared clock when there is one, so an open-ended job
  // counts. A terminal row has `ended_at`, so this returns the same total every
  // tick and the row stays still — the discipline is in the data, not in a
  // per-status branch here.
  const durationSeconds =
    nowSeconds === undefined
      ? job.durationSeconds
      : deliveryDurationSeconds(job.delivery, nowSeconds)
  return (
    <TableRow hover sx={statusPulseSx(justChanged ? job.status : '', reduced)}>
      <TableCell sx={{ fontFamily: 'monospace' }}>
        {job.eventType || <Muted>outside this page</Muted>}
      </TableCell>
      <TableCell>{job.worker || <Muted>subscription deleted</Muted>}</TableCell>
      <TableCell>
        {formatTimestamp(job.delivery.started_at) || <Muted>not started</Muted>}
      </TableCell>
      {/* The digits tick; a screen reader hears the coarse label instead, which
          changes every few minutes rather than every second (§4.2). */}
      <TableCell aria-label={running ? `running for ${coarseAgeLabel(durationSeconds ?? 0)}` : undefined}>
        <Box component="span" aria-hidden={running ? true : undefined}>
          {formatDuration(durationSeconds)}
          {running && durationSeconds !== null && (
            <Typography component="span" variant="caption" color="text.secondary">
              {' '}
              (so far)
            </Typography>
          )}
        </Box>
      </TableCell>
      <TableCell>
        <DeliveryStatusChip status={job.status} />
      </TableCell>
      <TableCell align="right">
        <JobTokensCell sessionId={job.sessionId} auto={autoLoadTokens} {...apiOptions} />
      </TableCell>
      <TableCell>
        {job.sessionId === '' ? (
          <Muted>none</Muted>
        ) : onOpenSession ? (
          <Link
            component="button"
            type="button"
            variant="body2"
            onClick={() => onOpenSession(job.sessionId)}
          >
            open
          </Link>
        ) : (
          <Link href={buildSessionPath(projectId, job.sessionId)} variant="body2">
            open
          </Link>
        )}
      </TableCell>
    </TableRow>
  )
}

interface JobTokensCellProps extends ConfigApiOptions {
  sessionId: string
  auto: boolean
}

function JobTokensCell({ sessionId, auto, ...apiOptions }: JobTokensCellProps) {
  const { totals, loading, error, load } = useSessionTokens(sessionId, { ...apiOptions, auto })

  if (sessionId === '') return <Muted>—</Muted>
  if (error !== null) {
    return (
      <Tooltip title={error}>
        <Box component="span" sx={{ color: 'error.main', fontSize: 13 }}>
          error
        </Box>
      </Tooltip>
    )
  }
  if (totals) {
    return (
      <Tooltip title={`${formatTokens(totals.input)} in / ${formatTokens(totals.output)} out`}>
        <Box component="span">{formatTokens(totals.total)}</Box>
      </Tooltip>
    )
  }
  if (loading) return <Muted>…</Muted>
  return (
    <Link component="button" type="button" variant="body2" onClick={() => void load()}>
      load
    </Link>
  )
}

function Muted({ children }: { children: React.ReactNode }) {
  return (
    <Typography component="span" variant="body2" color="text.secondary">
      {children}
    </Typography>
  )
}

/** Chip colour for a status — the shared severity bucket, so a host laying out
 *  its own table colours the six statuses identically. `awaiting_human` reads
 *  `default` here and is painted rose by DeliveryStatusChip (doc 21, X11): the
 *  MUI prop has no way to say rose. */
export function statusChipColor(
  status: string,
): 'success' | 'error' | 'warning' | 'info' | 'default' {
  return deliveryStatusSeverity(status)
}
