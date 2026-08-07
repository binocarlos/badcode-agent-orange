// BeforeAfterView — design §7.2, the product's thesis on one screen.
//
// Three columns around one prompt rewrite: the last thing the subject worker
// said before it, the rewrite itself (actor, rationale, diff), and the first
// thing it said after. Read-only; the join is the pure `beforeAfter` selector
// in learning.ts and everything here is rendering.
//
// The caveat line is rendered verbatim and never conditionally: these
// transcripts hold what the worker said, not what it did.

import { Alert, Box, CircularProgress, Link, Paper, Stack, Typography } from '@mui/material'
import useEventsOverview from '../useEvents.js'
import type { ConfigApiOptions } from '../configApi.js'
import type { ConfigEvent, ChangelogDiff } from '../configLog.js'
import { formatConfigTimestamp } from '../configLog.js'
import {
  beforeAfter,
  BEFORE_AFTER_CAVEAT,
  FINISHED_EVENT_TYPE,
  NO_JOB_YET,
  type BeforeAfterSide,
} from '../learning.js'
import { DiffBlock } from './ChangelogView.js'

export interface BeforeAfterViewProps extends ConfigApiOptions {
  /** The `worker_prompt_write` this view is about. */
  configEvent: ConfigEvent
  /** The rewrite's diff, when the caller already computed one (lineage does). */
  diff?: ChangelogDiff | null
  /** Called with a finished job's session id, when the envelope carries one. */
  onOpenSession?: (sessionId: string) => void
}

export default function BeforeAfterView({
  configEvent,
  diff = null,
  onOpenSession,
  ...apiOptions
}: BeforeAfterViewProps) {
  const overview = useEventsOverview({
    ...apiOptions,
    type: FINISHED_EVENT_TYPE,
  })
  const ba = beforeAfter(configEvent, overview.events)

  return (
    <Box sx={{ mt: 1.5 }}>
      {overview.error !== null && (
        <Alert severity="error" sx={{ mb: 1.5 }}>
          {overview.error}
        </Alert>
      )}
      {overview.loading ? (
        <Box sx={{ p: 2, display: 'flex', justifyContent: 'center' }}>
          <CircularProgress size={20} aria-label="Loading the jobs either side" />
        </Box>
      ) : (
        <Stack
          direction={{ xs: 'column', md: 'row' }}
          spacing={1.5}
          alignItems="stretch"
          sx={{ mb: 1 }}
        >
          <TranscriptColumn
            label="Before"
            side={ba.before}
            onOpenSession={onOpenSession}
          />
          <Paper variant="outlined" sx={{ p: 1.5, flex: 1, minWidth: 0 }}>
            <Typography
              variant="overline"
              color="text.secondary"
              sx={{ display: 'block', lineHeight: 1.6 }}
            >
              The rewrite · {formatConfigTimestamp(ba.atMs)}
            </Typography>
            <Typography variant="caption" color="text.secondary" sx={{ display: 'block' }}>
              {ba.actorWorker !== '' ? `${ba.actorWorker} said:` : 'A human edited it:'}
            </Typography>
            <Typography variant="body2" sx={{ mt: 0.5, whiteSpace: 'pre-wrap' }}>
              {ba.rationale !== '' ? ba.rationale : 'No reason is recorded on this change.'}
            </Typography>
            {diff && (
              <Box sx={{ mt: 1 }}>
                <Typography variant="caption" color="text.secondary">
                  +{diff.added} −{diff.removed} against the previous version
                </Typography>
                <DiffBlock lines={diff.lines} />
              </Box>
            )}
          </Paper>
          <TranscriptColumn label="After" side={ba.after} onOpenSession={onOpenSession} />
        </Stack>
      )}

      <Typography variant="caption" color="text.secondary" sx={{ display: 'block' }}>
        {BEFORE_AFTER_CAVEAT}
      </Typography>
    </Box>
  )
}

function TranscriptColumn({
  label,
  side,
  onOpenSession,
}: {
  label: string
  side: BeforeAfterSide
  onOpenSession?: (sessionId: string) => void
}) {
  const sessionId = side.event?.envelope.session_id ?? ''
  return (
    <Paper variant="outlined" sx={{ p: 1.5, flex: 1, minWidth: 0 }}>
      <Typography
        variant="overline"
        color="text.secondary"
        sx={{ display: 'block', lineHeight: 1.6 }}
      >
        {label}
        {side.atMs !== null ? ` · job ${formatConfigTimestamp(side.atMs)}` : ''}
      </Typography>
      {side.event === null ? (
        <Typography variant="body2" color="text.secondary">
          {NO_JOB_YET}
        </Typography>
      ) : (
        <>
          <Typography
            variant="body2"
            sx={{ whiteSpace: 'pre-wrap', maxHeight: 260, overflow: 'auto' }}
          >
            {side.event.text !== '' ? side.event.text : 'The job finished without saying anything.'}
          </Typography>
          {onOpenSession && sessionId !== '' && (
            <Link
              component="button"
              type="button"
              variant="caption"
              sx={{ mt: 0.5 }}
              onClick={() => onOpenSession(sessionId)}
            >
              open the job
            </Link>
          )}
        </>
      )}
    </Paper>
  )
}
