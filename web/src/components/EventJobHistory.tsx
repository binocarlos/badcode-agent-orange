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

import React from 'react'
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
}

export default function EventJobHistory({
  jobs,
  projectId,
  onOpenSession,
  title = 'Jobs',
  tokenAutoLoad = 10,
  truncated = false,
  ...apiOptions
}: EventJobHistoryProps) {
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
}

function JobHistoryRow({
  job,
  projectId,
  onOpenSession,
  autoLoadTokens,
  ...apiOptions
}: JobHistoryRowProps) {
  const running = job.status === 'running' || job.status === 'awaiting_human'
  return (
    <TableRow hover>
      <TableCell sx={{ fontFamily: 'monospace' }}>
        {job.eventType || <Muted>outside this page</Muted>}
      </TableCell>
      <TableCell>{job.worker || <Muted>subscription deleted</Muted>}</TableCell>
      <TableCell>
        {formatTimestamp(job.delivery.started_at) || <Muted>not started</Muted>}
      </TableCell>
      <TableCell>
        {formatDuration(job.durationSeconds)}
        {running && job.durationSeconds !== null && (
          <Typography component="span" variant="caption" color="text.secondary">
            {' '}
            (so far)
          </Typography>
        )}
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
