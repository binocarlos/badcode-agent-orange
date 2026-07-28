// ChangelogView — the config log rendered chronologically (§15.10).
//
// "The organisation's changelog, written by construction rather than by
// discipline." So it is rendered as a changelog: newest first, each entry a
// headline (what changed), a commit message (the rationale), the actor, and —
// for prompt rewrites — a read-time diff against the previous state of the same
// key. Every entry deep-links to the session that decided it, which is the
// question the log exists to answer.
//
// The diff is computed here, in the browser, from full-state payloads. §15.2 is
// explicit that this is where diffs belong: the log stores whole states so that
// folding is last-writer-wins with no merge algebra, and the changelog is the
// only place a diff is ever wanted.
//
// The route is mounted: `GET /agent/config-events` (go/httpapi/config_events.go),
// and this view reads it with no host wiring. A host that serves the log from
// somewhere else can still pass `fetchConfigEvents` (see configLog.ts for the
// exact contract) — an override, not a stopgap. If a deployment answers 404/501
// this view says plainly that the log is written but not served there, rather
// than rendering an empty history that reads as "nothing has changed".

import React, { useState } from 'react'
import {
  Alert,
  Box,
  Chip,
  CircularProgress,
  Link,
  MenuItem,
  Paper,
  Stack,
  TextField,
  Typography,
} from '@mui/material'
import useConfigLog, { type UseConfigLogOptions } from '../useConfigLog.js'
import { formatConfigTimestamp, type ChangelogEntry, type DiffLine } from '../configLog.js'

export interface ChangelogViewProps extends Omit<UseConfigLogOptions, 'query'> {
  /** Heading. Pass '' for none. */
  title?: string
  /** Called when an entry's actor session is clicked; falls back to a link. */
  onOpenSession?: (sessionId: string) => void
  /** Hide the action/actor filter row (a host with its own filters). */
  hideFilters?: boolean
}

/** Filter presets: the whole vocabulary, plus the prefix groups §15.9 allows. */
const ACTION_FILTERS: { value: string; label: string }[] = [
  { value: '', label: 'Every change' },
  { value: 'worker_*', label: 'Workers' },
  { value: 'worker_prompt_write', label: 'Prompt rewrites (workers)' },
  { value: 'project_*', label: 'Project prompt & settings' },
  { value: 'subscription_*', label: 'Subscriptions' },
  { value: 'schedule_*', label: 'Schedules' },
  { value: 'image_create', label: 'Images published' },
  { value: 'skill_create', label: 'Skills published' },
  { value: 'topology_apply', label: 'Topologies applied' },
]

export default function ChangelogView({
  title = 'Changelog',
  onOpenSession,
  hideFilters = false,
  ...options
}: ChangelogViewProps) {
  const [action, setAction] = useState('')
  const [actorWorker, setActorWorker] = useState('')
  const log = useConfigLog({ ...options, query: { action, actorWorker } })

  return (
    <Box sx={{ p: 3, maxWidth: 960 }}>
      {title !== '' && (
        <Typography variant="h6" sx={{ mb: 0.5 }}>
          {title}
        </Typography>
      )}
      <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mb: 2 }}>
        Every management mutation, newest first. Rationales are the commit messages; prompt
        rewrites are diffed against the previous version of the same prompt.
      </Typography>

      {!log.available && (
        <Alert severity="info" sx={{ mb: 2 }}>
          The config log is being written, but this deployment does not serve it yet:{' '}
          <code>GET /agent/config-events</code> answered as unmounted here. The route ships
          mounted in <code>agentd</code>; a host that serves the log from somewhere else can
          supply a <code>fetchConfigEvents</code> function instead.
        </Alert>
      )}
      {log.available && log.error !== null && (
        <Alert severity="error" sx={{ mb: 2 }}>
          {log.error}
        </Alert>
      )}

      {!hideFilters && (
        <Stack direction="row" spacing={2} sx={{ mb: 2 }}>
          <TextField
            select
            size="small"
            label="Show"
            value={action}
            onChange={(e) => setAction(e.target.value)}
            sx={{ minWidth: 240 }}
          >
            {ACTION_FILTERS.map((f) => (
              <MenuItem key={f.value || 'all'} value={f.value}>
                {f.label}
              </MenuItem>
            ))}
          </TextField>
          <TextField
            size="small"
            label="Made by worker"
            placeholder="any"
            value={actorWorker}
            onChange={(e) => setActorWorker(e.target.value)}
            helperText="Blank includes human and API edits, which log no actor."
          />
        </Stack>
      )}

      {log.loading ? (
        <Box sx={{ p: 3, display: 'flex', justifyContent: 'center' }}>
          <CircularProgress size={24} aria-label="Loading the changelog" />
        </Box>
      ) : log.entries.length === 0 ? (
        <Typography variant="body2" color="text.secondary">
          {log.available ? 'No configuration changes recorded.' : 'Nothing to show.'}
        </Typography>
      ) : (
        <Stack spacing={2} component="ol" sx={{ listStyle: 'none', p: 0, m: 0 }}>
          {log.entries.map((entry) => (
            <ChangelogEntryCard
              key={entry.id}
              entry={entry}
              onOpenSession={onOpenSession}
            />
          ))}
        </Stack>
      )}
    </Box>
  )
}

function ChangelogEntryCard({
  entry,
  onOpenSession,
}: {
  entry: ChangelogEntry
  onOpenSession?: (sessionId: string) => void
}) {
  const [showPayload, setShowPayload] = useState(false)
  return (
    <Paper component="li" variant="outlined" sx={{ p: 2 }}>
      <Stack direction="row" spacing={1} alignItems="center" flexWrap="wrap" useFlexGap>
        <Typography variant="subtitle2">{entry.title}</Typography>
        <Chip size="small" variant="outlined" label={entry.action} sx={{ fontFamily: 'monospace' }} />
      </Stack>

      <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mt: 0.5 }}>
        {formatConfigTimestamp(entry.createdAt)}
        {' · '}
        {entry.actorWorker ? `by ${entry.actorWorker}` : 'by a human (UI or API)'}
        {entry.sessionPath && (
          <>
            {' · '}
            {onOpenSession && entry.actorSession ? (
              <Link
                component="button"
                type="button"
                variant="caption"
                onClick={() => onOpenSession(entry.actorSession)}
              >
                the session that decided it
              </Link>
            ) : (
              <Link href={entry.sessionPath} variant="caption">
                the session that decided it
              </Link>
            )}
          </>
        )}
      </Typography>

      {entry.rationale !== '' && (
        <Box
          sx={{
            mt: 1.5,
            pl: 1.5,
            borderLeft: 3,
            borderColor: 'divider',
            whiteSpace: 'pre-wrap',
          }}
        >
          <Typography variant="body2">{entry.rationale}</Typography>
        </Box>
      )}

      {entry.diff && (
        <Box sx={{ mt: 1.5 }}>
          <Typography variant="caption" color="text.secondary">
            +{entry.diff.added} −{entry.diff.removed} against the previous version
          </Typography>
          <DiffBlock lines={entry.diff.lines} />
        </Box>
      )}

      <Box sx={{ mt: 1.5 }}>
        <Link
          component="button"
          type="button"
          variant="caption"
          onClick={() => setShowPayload((v) => !v)}
        >
          {showPayload ? 'Hide full state' : 'Show full state'}
        </Link>
        {showPayload && (
          <Paper
            variant="outlined"
            sx={{
              mt: 1,
              p: 1.5,
              maxHeight: 320,
              overflow: 'auto',
              whiteSpace: 'pre-wrap',
              fontFamily: 'monospace',
              fontSize: 12,
            }}
          >
            {JSON.stringify(entry.event.payload, null, 2)}
          </Paper>
        )}
      </Box>
    </Paper>
  )
}

/** The diff, rendered the way a diff is read: +/− gutters, monospaced. */
function DiffBlock({ lines }: { lines: DiffLine[] }) {
  return (
    <Paper
      variant="outlined"
      aria-label="Prompt diff"
      sx={{ mt: 0.5, maxHeight: 360, overflow: 'auto', fontFamily: 'monospace', fontSize: 12 }}
    >
      {lines.map((line, i) => (
        <Box
          key={i}
          sx={{
            px: 1,
            whiteSpace: 'pre-wrap',
            bgcolor:
              line.type === 'add'
                ? 'success.light'
                : line.type === 'del'
                  ? 'error.light'
                  : 'transparent',
            opacity: line.type === 'ctx' ? 0.7 : 1,
          }}
        >
          {line.type === 'add' ? '+' : line.type === 'del' ? '-' : ' '}
          {line.text}
        </Box>
      ))}
    </Paper>
  )
}

/** Re-exported for a host building its own filter control. */
export { ACTION_FILTERS }
