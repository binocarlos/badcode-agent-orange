// DeskPage — the landing view (decision K1; design §5.2).
//
// Three stacks, in the order the morning is actually read: *does anything want
// me? what changed? what broke?* Everything on the page is read-only and every
// item's action is to open the thing it names — this is a query, not an
// approval queue, and it holds no state of its own beyond one `localStorage`
// high-water mark for "since you last looked".
//
// The empty Desk is the FIRST-RUN state: a project with no workers is not shown
// "nothing to show", it is shown the two ways in: the org chart the topology
// flow builds, and chat.

import type { ReactNode } from 'react'
import { Alert, Box, Button, Link, Paper, Stack, Typography } from '@mui/material'
import useDesk, { type UseDeskOptions } from '../useDesk.js'
import {
  DESK_ASKS_CAVEAT,
  type DeskAsk,
  type DeskChange,
  type DeskTrouble,
} from '../desk.js'
import { formatTimestamp } from '../events.js'
import { formatConfigTimestamp } from '../configLog.js'
import { SpineGlyph, SpineRail, SpineRow } from '../spine.js'

export interface DeskPageProps extends UseDeskOptions {
  /** Project id — the high-water mark's key, and the permalink builder's. */
  projectId: string
  /** Open a session thread (typically useSessionPermalink().openSession). */
  onOpenSession?: (sessionId: string) => void
  /** Take the operator to the topology flow — the first-run "org chart" door. */
  onStartFromTopology?: () => void
  /** Take the operator to chat — the other first-run door. */
  onOpenChat?: () => void
  /** Heading. Pass '' for none. */
  title?: string
}

/** Identifiers are mono, content is prose (§3.4). */
const MONO = { fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace' }

export default function DeskPage({
  projectId,
  onOpenSession,
  onStartFromTopology,
  onOpenChat,
  title = 'Desk',
  ...deskOptions
}: DeskPageProps) {
  const { desk, loading, error, asksHaveMessages, workerCount, lastSeenMs, markSeen } = useDesk({
    ...deskOptions,
    projectId,
  })

  const firstRun = !loading && workerCount === 0
  const nothingAtAll =
    !loading && desk.asks.length === 0 && desk.changes.length === 0 && desk.trouble.length === 0

  return (
    <Box sx={{ p: 3, maxWidth: 900 }}>
      <Stack direction="row" alignItems="baseline" justifyContent="space-between" sx={{ mb: 2 }}>
        {title !== '' && <Typography variant="h6">{title}</Typography>}
        {desk.changes.length > 0 && (
          <Link component="button" type="button" variant="caption" onClick={markSeen}>
            Mark these changes as seen
          </Link>
        )}
      </Stack>

      {error !== null && (
        <Alert severity="error" sx={{ mb: 2 }}>
          {error}
        </Alert>
      )}

      {firstRun ? (
        <FirstRun onStartFromTopology={onStartFromTopology} onOpenChat={onOpenChat} />
      ) : (
        <Stack spacing={4}>
          <Section
            label="Asks"
            count={desk.asks.length}
            caption="nobody has answered these"
            empty="Nothing is waiting on you."
          >
            {!asksHaveMessages && desk.asks.length > 0 && (
              <Alert severity="info" sx={{ mb: 2 }}>
                This deployment does not serve <code>GET /agent/attention-requests</code>, so these
                asks show without the sentence the worker wrote. Open the thread to read it.
              </Alert>
            )}
            {desk.asks.map((ask) => (
              <AskRow key={ask.id} ask={ask} onOpenSession={onOpenSession} />
            ))}
            {desk.asks.length > 0 && (
              <Typography variant="caption" color="text.secondary" sx={{ display: 'block' }}>
                {DESK_ASKS_CAVEAT}
              </Typography>
            )}
          </Section>

          <Section
            label="Changes"
            count={desk.changes.length}
            caption={lastSeenMs === 0 ? 'everything recorded so far' : 'since you last looked'}
            empty={
              lastSeenMs === 0
                ? 'No configuration changes recorded.'
                : 'Nothing has changed since you last looked.'
            }
          >
            {desk.changes.map((change) => (
              <ChangeRow key={change.id} change={change} onOpenSession={onOpenSession} />
            ))}
          </Section>

          <Section
            label="Trouble"
            count={desk.trouble.length}
            caption=""
            empty="Nothing has failed."
          >
            {desk.trouble.map((item) => (
              <TroubleRow key={item.id} item={item} onOpenSession={onOpenSession} />
            ))}
          </Section>

          {nothingAtAll && (
            <Typography variant="body2" color="text.secondary">
              A quiet Desk means the fleet ran and nobody needed you — the Events view has the jobs
              it ran, and Automation has what will wake it next.
            </Typography>
          )}
        </Stack>
      )}
    </Box>
  )
}

/** One stack: its name, its count, the line that qualifies it, its rail. */
function Section({
  label,
  count,
  caption,
  empty,
  children,
}: {
  label: string
  count: number
  caption: string
  empty: string
  children?: ReactNode
}) {
  return (
    <Box component="section" aria-label={label}>
      <Stack direction="row" alignItems="baseline" spacing={1} sx={{ mb: 1.5 }}>
        <Typography variant="subtitle2" sx={{ textTransform: 'uppercase', letterSpacing: '0.08em' }}>
          {label}
        </Typography>
        <Typography variant="subtitle2" sx={MONO}>
          {count}
        </Typography>
        {caption !== '' && (
          <Typography variant="caption" color="text.secondary" sx={{ flex: 1, textAlign: 'right' }}>
            {caption}
          </Typography>
        )}
      </Stack>
      {count === 0 ? (
        <Typography variant="body2" color="text.secondary">
          {empty}
        </Typography>
      ) : (
        <SpineRail component="ol">{children}</SpineRail>
      )}
    </Box>
  )
}

function AskRow({ ask, onOpenSession }: { ask: DeskAsk; onOpenSession?: (id: string) => void }) {
  return (
    <SpineRow glyph="attention" component="li" glyphLabel="waiting for you">
      <Stack direction="row" spacing={1} alignItems="baseline" flexWrap="wrap" useFlexGap>
        <Typography variant="body2" sx={MONO}>
          {ask.headline}
        </Typography>
        {ask.expiresLabel !== '' && (
          <Typography variant="caption" color="text.secondary">
            {ask.expiresLabel}
          </Typography>
        )}
      </Stack>
      {ask.message !== '' && (
        <Typography variant="body2" sx={{ mt: 0.5, whiteSpace: 'pre-wrap' }}>
          {ask.message}
        </Typography>
      )}
      <Box sx={{ mt: 0.5 }}>
        <ThreadLink sessionId={ask.sessionId} url={ask.sessionUrl} onOpenSession={onOpenSession} />
      </Box>
    </SpineRow>
  )
}

function ChangeRow({
  change,
  onOpenSession,
}: {
  change: DeskChange
  onOpenSession?: (id: string) => void
}) {
  return (
    <SpineRow
      glyph={change.glyph}
      component="li"
      glyphLabel={change.byAgent ? 'a worker did this' : 'you did this'}
    >
      <Stack direction="row" spacing={1} alignItems="baseline" flexWrap="wrap" useFlexGap>
        <Typography variant="body2" sx={MONO}>
          {change.sentence}
        </Typography>
        <Typography variant="caption" color="text.secondary">
          {formatConfigTimestamp(change.createdAt)}
        </Typography>
        {change.diffLabel !== '' && (
          <Typography variant="caption" sx={MONO} color="text.secondary">
            {change.diffLabel}
          </Typography>
        )}
      </Stack>
      <Typography
        variant="body2"
        color={change.noReason ? 'text.disabled' : 'text.primary'}
        sx={{ mt: 0.5, whiteSpace: 'pre-wrap' }}
      >
        {change.reason}
      </Typography>
      {change.entry.actorSession !== '' && (
        <Box sx={{ mt: 0.5 }}>
          <ThreadLink
            sessionId={change.entry.actorSession}
            url={change.entry.sessionPath ?? ''}
            onOpenSession={onOpenSession}
            label="open the session that decided it"
          />
        </Box>
      )}
    </SpineRow>
  )
}

function TroubleRow({
  item,
  onOpenSession,
}: {
  item: DeskTrouble
  onOpenSession?: (id: string) => void
}) {
  return (
    <SpineRow
      glyph={item.glyph}
      component="li"
      glyphLabel={item.kind === 'freeze-refusal' ? 'a frozen worker refused a rewrite' : 'a failure'}
    >
      <Stack direction="row" spacing={1} alignItems="baseline" flexWrap="wrap" useFlexGap>
        <Typography variant="body2" sx={MONO}>
          {item.headline}
        </Typography>
        {item.sinceSeconds > 0 && (
          <Typography variant="caption" color="text.secondary">
            since {formatTimestamp(item.sinceSeconds)}
          </Typography>
        )}
      </Stack>
      <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5 }}>
        {item.detail}
      </Typography>
      {item.sessionId !== '' && (
        <Box sx={{ mt: 0.5 }}>
          <ThreadLink
            sessionId={item.sessionId}
            url=""
            onOpenSession={onOpenSession}
            label="open the last job"
          />
        </Box>
      )}
    </SpineRow>
  )
}

/** Open a thread: the host's handler when it has one, else the permalink. */
function ThreadLink({
  sessionId,
  url,
  onOpenSession,
  label = 'open thread',
}: {
  sessionId: string
  url: string
  onOpenSession?: (id: string) => void
  label?: string
}) {
  if (sessionId === '' && url === '') return null
  if (onOpenSession && sessionId !== '') {
    return (
      <Link
        component="button"
        type="button"
        variant="caption"
        onClick={() => onOpenSession(sessionId)}
      >
        {label}
      </Link>
    )
  }
  if (url === '') return null
  return (
    <Link href={url} variant="caption">
      {label}
    </Link>
  )
}

/**
 * The first-run Desk (K1): an empty project is shown what this system is made
 * of and the two doors into it, never a bare "nothing to show".
 */
function FirstRun({
  onStartFromTopology,
  onOpenChat,
}: {
  onStartFromTopology?: () => void
  onOpenChat?: () => void
}) {
  return (
    <Paper variant="outlined" sx={{ p: 3, maxWidth: 620 }}>
      <Typography variant="subtitle1" sx={{ mb: 0.5 }}>
        This project has no workers yet
      </Typography>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        The Desk answers three questions every morning — what wants you, what changed, what broke.
        It stays quiet until something is running. Start from an org chart, which hires a set of
        workers and wires them to each other in one step, or just talk to the agent.
      </Typography>
      <Stack direction="row" spacing={1} sx={{ mb: 2 }}>
        <Button size="small" variant="contained" onClick={onStartFromTopology} disabled={!onStartFromTopology}>
          Start from an org chart
        </Button>
        <Button size="small" onClick={onOpenChat} disabled={!onOpenChat}>
          Open chat
        </Button>
      </Stack>
      <Stack direction="row" spacing={2}>
        <Legend glyph="agent" text="a worker did it" />
        <Legend glyph="human" text="you did it" />
        <Legend glyph="attention" text="waiting for you" />
        <Legend glyph="failure" text="a failure" />
      </Stack>
    </Paper>
  )
}

function Legend({
  glyph,
  text,
}: {
  glyph: 'agent' | 'human' | 'attention' | 'failure'
  text: string
}) {
  return (
    <Stack direction="row" spacing={0.5} alignItems="center">
      <SpineGlyph glyph={glyph} label="" />
      <Typography variant="caption" color="text.secondary">
        {text}
      </Typography>
    </Stack>
  )
}
