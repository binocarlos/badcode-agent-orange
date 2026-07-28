// WorkerLineage — design §7.1: the spine, filtered to one worker's prompt.
//
// Nearly free data, badly placed: every `worker_prompt_write` already carries
// the full new prompt and a mandatory rationale, and `buildChangelog` already
// diffs consecutive events of the same key. All this view does is put that
// history on the worker it describes, number the versions, and make two things
// one click away — the job that decided a rewrite, and the prompt as it was.
//
// Nothing here writes. "Restore this version" is a *forward* write: it hands
// the old text back to the ordinary editor with a pre-filled rationale naming
// the config event, and the operator saves it like any other edit. There is no
// revert route and there should not be one.

import {
  Alert,
  Box,
  Chip,
  CircularProgress,
  Link,
  Paper,
  Stack,
  Typography,
} from '@mui/material'
import { useState } from 'react'
import useConfigLog from '../useConfigLog.js'
import type { ConfigApiOptions } from '../configApi.js'
import {
  formatConfigTimestamp,
  workerLineage,
  type LineageEntry,
} from '../configLog.js'
import { DiffBlock } from './ChangelogView.js'
import BeforeAfterView from './BeforeAfterView.js'
import { useFeedWatermark } from '../watermark.js'
import {
  cumulativeHeading,
  cumulativeLineageDiff,
  lineageHeadEventId,
  useViewedVersions,
  type CumulativeLineageDiff,
} from '../lineageWaterline.js'
import { consoleTokenColor } from '../spine.js'

/** What the page needs to fold the Configuration tab to a past version. */
export interface LineageVersion {
  /** The config event that wrote this prompt. */
  eventId: string
  /** 1-based version number, oldest = v1. */
  version: number
  /** The prompt as it was. */
  prompt: string
  /** Unix milliseconds. */
  at: number
}

export interface WorkerLineageProps extends ConfigApiOptions {
  workerName: string
  /** Project id — builds the acting-session permalink when the server omits one. */
  projectId?: string
  /** Called when a job row / actor link is clicked. */
  onOpenSession?: (sessionId: string) => void
  /** Called when a version is selected — the page folds Configuration to it. */
  onSelectVersion?: (version: LineageVersion) => void
  /** The version currently folded to, so the row can mark itself. */
  selectedEventId?: string | null
  /**
   * The operator's watermark, unix ms — "when did you last look". Omitted, the
   * Desk's own mark is read (`watermark.ts`), because there is exactly one
   * answer to that question per operator and a second mark here would disagree
   * with the Desk's about when the last look was.
   */
  watermarkMs?: number
}

/** Filled disc = an agent did this; hollow disc = a human did (§3, glyph set). */
function ActorGlyph({ byWorker }: { byWorker: boolean }) {
  return (
    <Box
      aria-hidden
      sx={{
        width: 10,
        height: 10,
        borderRadius: '50%',
        flexShrink: 0,
        mt: '5px',
        border: 1,
        borderColor: (t) =>
          byWorker
            ? ((t.palette as { ember?: { main: string } }).ember?.main ?? t.palette.secondary.main)
            : 'text.secondary',
        bgcolor: (t) =>
          byWorker
            ? ((t.palette as { ember?: { main: string } }).ember?.main ?? t.palette.secondary.main)
            : 'background.paper',
      }}
    />
  )
}

export default function WorkerLineage({
  workerName,
  projectId = '',
  onOpenSession,
  onSelectVersion,
  selectedEventId = null,
  watermarkMs,
  ...apiOptions
}: WorkerLineageProps) {
  const log = useConfigLog({
    ...apiOptions,
    projectId,
    query: { entity: `worker:${workerName}` },
  })
  const lineage = workerLineage(log.entries, workerName)

  // "Since you last looked", from the one mark (doc 21 §4.2). Frozen for the
  // visit by useFeedWatermark, so the cumulative banner cannot vanish under an
  // operator who is mid-read.
  const watermark = useFeedWatermark('desk', projectId, watermarkMs)
  const cumulative = cumulativeLineageDiff(lineage, watermark.markMs)
  const headEventId = lineageHeadEventId(lineage)
  const viewed = useViewedVersions(projectId, workerName, headEventId)

  return (
    <Box sx={{ p: 3, maxWidth: 880 }}>
      <Stack direction="row" spacing={1} alignItems="baseline" sx={{ mb: 0.5 }}>
        <Typography variant="subtitle1" sx={{ fontFamily: 'monospace' }}>
          {workerName}
        </Typography>
        <Typography variant="caption" color="text.secondary">
          {lineage.summary}
        </Typography>
      </Stack>
      <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mb: 2 }}>
        Every change recorded against this worker, newest first. Versions count prompt writes,
        oldest is v1; “distinct” counts the rewrites that actually changed the text, so a rewrite
        that re-wrote the same words is not counted as progress.
      </Typography>

      {!log.available && (
        <Alert severity="info" sx={{ mb: 2 }}>
          This deployment does not serve <code>GET /agent/config-events</code>, so no lineage can
          be shown here. The log is still being written.
        </Alert>
      )}
      {log.available && log.error !== null && (
        <Alert severity="error" sx={{ mb: 2 }}>
          {log.error}
        </Alert>
      )}

      {log.loading ? (
        <Box sx={{ p: 3, display: 'flex', justifyContent: 'center' }}>
          <CircularProgress size={24} aria-label="Loading the lineage" />
        </Box>
      ) : lineage.entries.length === 0 ? (
        <Typography variant="body2" color="text.secondary">
          {log.available
            ? 'Nothing has changed this worker since the config log started.'
            : 'Nothing to show.'}
        </Typography>
      ) : (
        <>
          {cumulative && <CumulativeDiffBanner cumulative={cumulative} />}
          <Stack spacing={0} component="ol" sx={{ listStyle: 'none', p: 0, m: 0 }}>
            {lineage.entries.map((row) => (
              <LineageRow
                key={row.entry.id}
                row={row}
                selected={selectedEventId === row.entry.id}
                onOpenSession={onOpenSession}
                onSelectVersion={onSelectVersion}
                apiOptions={apiOptions}
                viewed={row.version !== null && viewed.isViewed(row.entry.id)}
                onToggleViewed={row.version !== null ? () => viewed.toggle(row.entry.id) : undefined}
              />
            ))}
          </Stack>
        </>
      )}
    </Box>
  )
}

/**
 * "Changes since your last review" — GitHub's transplant (§4.2), shown ONLY
 * when more than one rewrite landed after the watermark.
 *
 * With zero or one, the per-revision diffs below are already the right view and
 * this would repeat one of them under a grander heading. It opens by default:
 * an operator who has been away is here to read exactly this, and a diff they
 * have to click for is a diff they will not read.
 */
function CumulativeDiffBanner({ cumulative }: { cumulative: CumulativeLineageDiff }) {
  return (
    <Paper
      variant="outlined"
      data-testid="lineage-cumulative"
      sx={{
        p: 2,
        mb: 2,
        borderColor: (t) => consoleTokenColor(t, 'ember'),
      }}
    >
      <Typography variant="subtitle2" sx={{ color: (t) => consoleTokenColor(t, 'ember') }}>
        {cumulativeHeading(cumulative)}
      </Typography>
      <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mt: 0.5 }}>
        One diff from the prompt you last saw to the prompt running now. Each rewrite’s own diff,
        and the reason given for it, is on its row below.
      </Typography>
      <DiffBlock
        lines={cumulative.diff.lines}
        collapsible
        defaultOpen
        added={cumulative.diff.added}
        removed={cumulative.diff.removed}
        summaryNote="since you last looked"
      />
    </Paper>
  )
}

function LineageRow({
  row,
  selected,
  onOpenSession,
  onSelectVersion,
  apiOptions,
  viewed = false,
  onToggleViewed,
}: {
  row: LineageEntry
  selected: boolean
  onOpenSession?: (sessionId: string) => void
  onSelectVersion?: (version: LineageVersion) => void
  apiOptions: ConfigApiOptions
  viewed?: boolean
  onToggleViewed?: () => void
}) {
  const { entry } = row
  // §7.2 hangs off the rewrite it explains — opened per row, never all at once:
  // each one reads the events route.
  const [showBeforeAfter, setShowBeforeAfter] = useState(false)
  const isPromptWrite = entry.action === 'worker_prompt_write'
  return (
    <Paper
      component="li"
      variant="outlined"
      sx={{
        p: 2,
        mb: 1.5,
        borderColor: selected ? 'primary.main' : 'divider',
        opacity: viewed && !selected ? 0.72 : 1,
      }}
    >
      <Stack direction="row" spacing={1.5}>
        <ActorGlyph byWorker={row.byWorker} />
        <Box sx={{ minWidth: 0, flex: 1 }}>
          <Stack direction="row" spacing={1} alignItems="center" flexWrap="wrap" useFlexGap>
            <Typography variant="subtitle2">{entry.title}</Typography>
            {row.version !== null && (
              <Chip
                size="small"
                variant={selected ? 'filled' : 'outlined'}
                label={`v${row.version}`}
                sx={{ fontFamily: 'monospace' }}
                onClick={
                  onSelectVersion && row.prompt !== null
                    ? () =>
                        onSelectVersion({
                          eventId: entry.id,
                          version: row.version!,
                          prompt: row.prompt!,
                          at: entry.createdAt,
                        })
                    : undefined
                }
              />
            )}
            {row.duplicate && (
              <Typography variant="caption" color="text.secondary">
                same text as the previous version
              </Typography>
            )}
            {onToggleViewed && (
              // GitHub's "Viewed", and its invalidation (§4.2): the mark is
              // stamped against the head prompt, so the next rewrite clears
              // every mark rather than leaving a tick that has become a lie.
              // The label is one text node and carries the state on its own —
              // the dimming below is the secondary cue, not the only one.
              <Link
                component="button"
                type="button"
                variant="caption"
                aria-pressed={viewed}
                data-testid={`viewed-toggle-${row.entry.id}`}
                onClick={onToggleViewed}
              >
                {viewed ? '✓ viewed' : 'mark viewed'}
              </Link>
            )}
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
                    the job that decided it
                  </Link>
                ) : (
                  <Link href={entry.sessionPath} variant="caption">
                    the job that decided it
                  </Link>
                )}
              </>
            )}
          </Typography>

          <Box sx={{ mt: 1.5, pl: 1.5, borderLeft: 3, borderColor: 'divider' }}>
            <Typography variant="body2" sx={{ whiteSpace: 'pre-wrap' }}>
              {entry.rationale !== '' ? entry.rationale : 'No reason is recorded on this change.'}
            </Typography>
          </Box>

          {entry.diff && (
            <Box sx={{ mt: 1.5 }}>
              {/* Collapsed by default: the rationale is the point of the
                  lineage, and it is never folded — the diff is the detail
                  behind it (§4.2 disclosure). */}
              <DiffBlock
                lines={entry.diff.lines}
                collapsible
                added={entry.diff.added}
                removed={entry.diff.removed}
                summaryNote="against the previous version"
              />
            </Box>
          )}

          {isPromptWrite && (
            <Box sx={{ mt: 1 }}>
              <Link
                component="button"
                type="button"
                variant="caption"
                onClick={() => setShowBeforeAfter((v) => !v)}
              >
                {showBeforeAfter ? 'hide before / after' : 'what ran before / after'}
              </Link>
              {showBeforeAfter && (
                <BeforeAfterView
                  {...apiOptions}
                  configEvent={entry.event}
                  diff={entry.diff}
                  onOpenSession={onOpenSession}
                />
              )}
            </Box>
          )}
        </Box>
      </Stack>
    </Paper>
  )
}
