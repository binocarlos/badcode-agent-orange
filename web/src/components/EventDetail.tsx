// EventDetail — one event: its core-stamped envelope, its raw text, and the
// jobs it produced (§8.1, §8.4).
//
// The envelope is shown field by field rather than as pretty-printed JSON
// because it is the thing subscriptions filter on: a human debugging "why did
// nothing fire?" needs to read `interactive: true` at a glance, and the answer
// is usually one of these six values.

import React from 'react'
import { Alert, Box, Chip, Divider, Paper, Stack, Typography } from '@mui/material'
import { formatTimestamp, type JobRow, type ProjectEvent } from '../events.js'
import EventJobHistory from './EventJobHistory.js'
import type { ConfigApiOptions } from '../configApi.js'

export interface EventDetailProps extends ConfigApiOptions {
  event: ProjectEvent
  /** Jobs already joined for this event (buildJobRows, filtered by caller). */
  jobs: JobRow[]
  projectId: string
  onOpenSession?: (sessionId: string) => void
  /** How many job rows fetch their token totals unprompted. Default 10. */
  tokenAutoLoad?: number
}

export default function EventDetail({
  event,
  jobs,
  projectId,
  onOpenSession,
  tokenAutoLoad,
  ...apiOptions
}: EventDetailProps) {
  const env = event.envelope
  const fields: [string, string][] = [
    ['source', env.source],
    ['depth', String(env.depth)],
    ['worker', env.worker || '—'],
    ['session_id', env.session_id || '—'],
    ['interactive', String(env.interactive)],
    ['attention_requested', String(env.attention_requested)],
  ]
  if (env.reason) fields.push(['reason', env.reason])

  return (
    <Box sx={{ p: 3 }}>
      <Stack direction="row" spacing={1} alignItems="center" sx={{ mb: 0.5 }}>
        <Typography variant="h6" sx={{ fontFamily: 'monospace' }}>
          {event.type}
        </Typography>
        <Chip size="small" variant="outlined" label={event.delivered ? 'delivered' : 'undelivered'} />
      </Stack>
      <Typography variant="caption" color="text.secondary">
        {formatTimestamp(event.occurred_at)} · {event.id}
      </Typography>

      <Typography variant="subtitle2" sx={{ mt: 2.5, mb: 1 }}>
        Envelope
      </Typography>
      <Stack direction="row" spacing={1} useFlexGap flexWrap="wrap">
        {fields.map(([key, value]) => (
          <Chip
            key={key}
            size="small"
            variant="outlined"
            label={`${key}: ${value}`}
            sx={{ fontFamily: 'monospace' }}
          />
        ))}
      </Stack>
      <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mt: 1 }}>
        Stamped by core, never by the sender — a poster that could set depth or claim
        source&nbsp;=&nbsp;worker would defeat both the loop floor and every envelope filter.
      </Typography>

      <Typography variant="subtitle2" sx={{ mt: 2.5, mb: 1 }}>
        Text
      </Typography>
      {event.text === '' ? (
        <Typography variant="body2" color="text.secondary">
          (empty)
        </Typography>
      ) : (
        <Paper
          variant="outlined"
          sx={{
            p: 1.5,
            maxHeight: 260,
            overflow: 'auto',
            whiteSpace: 'pre-wrap',
            fontFamily: 'monospace',
            fontSize: 13,
          }}
        >
          {event.text}
        </Paper>
      )}

      <Divider sx={{ my: 3 }} />

      {jobs.length === 0 ? (
        <Alert severity="info">
          No deliveries for this event — either no subscription matched it, or the router has not
          reached it yet.
        </Alert>
      ) : (
        <EventJobHistory
          jobs={jobs}
          projectId={projectId}
          onOpenSession={onOpenSession}
          title="Jobs from this event"
          tokenAutoLoad={tokenAutoLoad}
          {...apiOptions}
        />
      )}
    </Box>
  )
}
