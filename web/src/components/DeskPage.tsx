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

import { useEffect, useState, type ReactNode } from 'react'
import { Alert, Box, Button, Chip, Link, Paper, Stack, Typography } from '@mui/material'
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
import { newItemsSummary, waterlineLabel } from '../watermark.js'
import {
  highlightSx,
  highlightMarker,
  NEW_MARKER_LABEL,
  type HighlightTone,
} from '../feedhighlight.js'
import { ageEscalation, coarseAgeLabel } from '../useElapsedTicker.js'
import usePrefersReducedMotion from '../useReducedMotion.js'
import useStagedFeed from '../useStagedFeed.js'
import { FeedWaterline, NewItemsPill, PauseLiveUpdates } from './FeedLiveness.js'

export interface DeskPageProps extends UseDeskOptions {
  /**
   * Show the "Pause live updates" toggle. Default: only when the Desk is
   * actually polling (`refreshMs > 0`) — a switch that pauses nothing is a lie,
   * and WCAG 2.2.2's obligation only exists where content updates on its own.
   */
  showPauseToggle?: boolean
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
  /**
   * Reports how many asks this Desk is showing, whenever that changes.
   *
   * The shell's badge is that number (X7). Before W4 the shell fetched the two
   * lists again through `useAsksCount` while the Desk had them already; now the
   * Desk hands its count up and the shell stands the second reader down. One
   * fetch, and — still the point of X7 — one definition of "an ask".
   */
  onAsksCount?: (count: number) => void
}

/** Identifiers are mono, content is prose (§3.4). */
const MONO = { fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace' }

/** Off-screen but announced — the coarse label beside an `aria-hidden` age. */
const VISUALLY_HIDDEN = {
  position: 'absolute',
  width: 1,
  height: 1,
  overflow: 'hidden',
  clip: 'rect(0 0 0 0)',
  whiteSpace: 'nowrap',
} as const

export default function DeskPage({
  projectId,
  onOpenSession,
  onStartFromTopology,
  onOpenChat,
  title = 'Desk',
  showPauseToggle,
  onAsksCount,
  ...deskOptions
}: DeskPageProps) {
  const reduced = usePrefersReducedMotion()
  // A reduced-motion operator lands on a paused surface: the setting is about
  // motion, but the operator asking for it is asking not to be chased.
  const [paused, setPaused] = useState(reduced)
  const {
    desk,
    loading,
    error,
    asksHaveMessages,
    asksRouteAvailable,
    workerCount,
    lastSeenMs,
    markSeen,
    nowMs,
  } = useDesk({
      ...deskOptions,
      projectId,
      paused: deskOptions.paused ?? paused,
    })

  const askCount = desk.asks.length
  useEffect(() => {
    onAsksCount?.(askCount)
  }, [askCount, onAsksCount])

  const polling = (deskOptions.refreshMs ?? 0) > 0
  const showPause = showPauseToggle ?? polling

  // Arrivals stage rather than insert. Both growing stacks go behind a pill;
  // Trouble does not, because a failure appearing quietly is the one thing this
  // screen must never do.
  const changesFeed = useStagedFeed(desk.changes, (c) => c.id, { paused })
  const asksFeed = useStagedFeed(desk.asks, (a) => a.id, { paused })

  // `error === null` is load-bearing, not defensive: a failed worker list stays
  // the initial `[]`, and without this gate an established project's Desk is
  // replaced wholesale by "start from a topology" (RD28). The banner above says
  // what went wrong; the panel would say something confident and false.
  const firstRun = !loading && error === null && workerCount === 0
  // Same gate, same reason: "the fleet ran and nobody needed you" is a claim
  // about the fleet, and three empty lists from three failed fetches are not
  // evidence for it.
  const nothingAtAll =
    !loading &&
    error === null &&
    desk.asks.length === 0 &&
    desk.changes.length === 0 &&
    desk.trouble.length === 0

  return (
    <Box sx={{ p: 3, maxWidth: 900 }}>
      <Stack direction="row" alignItems="baseline" justifyContent="space-between" sx={{ mb: 2 }}>
        {title !== '' && <Typography variant="h6">{title}</Typography>}
        <Stack direction="row" spacing={2} alignItems="center">
          {showPause && <PauseLiveUpdates paused={paused} onChange={setPaused} />}
          {desk.changes.length > 0 && (
            <Link component="button" type="button" variant="caption" onClick={markSeen}>
              Mark these changes as seen
            </Link>
          )}
        </Stack>
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
                {asksRouteAvailable ? (
                  <>
                    <code>GET /agent/attention-requests</code> did not answer, so these asks are
                    rebuilt from the parked jobs and show without the sentence the worker wrote.
                    Open the thread to read it.
                  </>
                ) : (
                  <>
                    This deployment does not serve <code>GET /agent/attention-requests</code>, so
                    these asks show without the sentence the worker wrote. Open the thread to read
                    it.
                  </>
                )}
              </Alert>
            )}
            <NewItemsPill
              count={asksFeed.stagedCount}
              summary={newItemsSummary(asksFeed.stagedCount, 'ask')}
              onShow={asksFeed.flush}
            />
            {asksFeed.visible.map((ask) => (
              <AskRow
                key={ask.id}
                ask={ask}
                onOpenSession={onOpenSession}
                arrived={asksFeed.arrivals.has(ask.id)}
                reduced={reduced}
              />
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
            after={
              desk.earlierChanges.length > 0 ? (
                // The waterline and the few changes the operator had already
                // read. Outside the stack's own count deliberately: "Nothing
                // has changed since you last looked" is still the honest line
                // when nothing has, and these sit under it as context.
                <>
                  <FeedWaterline label={waterlineLabel(lastSeenMs, nowMs)} />
                  <SpineRail component="ol">
                    {desk.earlierChanges.map((change) => (
                      <ChangeRow key={change.id} change={change} onOpenSession={onOpenSession} />
                    ))}
                  </SpineRail>
                </>
              ) : undefined
            }
            caption={lastSeenMs === 0 ? 'everything recorded so far' : 'since you last looked'}
            empty={
              lastSeenMs === 0
                ? 'No configuration changes recorded.'
                : 'Nothing has changed since you last looked.'
            }
          >
            <NewItemsPill
              count={changesFeed.stagedCount}
              summary={newItemsSummary(changesFeed.stagedCount, 'change')}
              onShow={changesFeed.flush}
            />
            {changesFeed.visible.map((change) => (
              <ChangeRow
                key={change.id}
                change={change}
                onOpenSession={onOpenSession}
                arrived={changesFeed.arrivals.has(change.id)}
                reduced={reduced}
              />
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
  after,
}: {
  label: string
  count: number
  caption: string
  empty: string
  children?: ReactNode
  /** Rendered below the stack, inside the same region — the waterline and the
   *  already-read tail beneath it (doc 21 §4.2). */
  after?: ReactNode
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
        // role="log", not role="feed" (§4.2): a chronological list that grows,
        // which is implicitly polite. `feed` would drag in a keyboard contract
        // (article navigation) the Desk does not implement.
        <SpineRail component="ol" role="log" aria-label={label}>
          {children}
        </SpineRail>
      )}
      {after}
    </Box>
  )
}

function AskRow({
  ask,
  onOpenSession,
  arrived = false,
  reduced = false,
}: {
  ask: DeskAsk
  onOpenSession?: (id: string) => void
  arrived?: boolean
  reduced?: boolean
}) {
  // §4.2: an ask's age ticks, and escalates — the number is an SLA on the
  // operator, not a progress bar. The escalation is a word AND a colour on top
  // of the number, never instead of it.
  const escalation = ageEscalation(ask.waitingSeconds)
  const ageColor =
    escalation === 'fault' ? 'error.main' : escalation === 'amber' ? 'warning.main' : undefined
  return (
    <SpineRow
      glyph="attention"
      component="li"
      glyphLabel="waiting for you"
      sx={highlightSx({ active: arrived, tone: 'ask', reduced })}
    >
      <Stack direction="row" spacing={1} alignItems="baseline" flexWrap="wrap" useFlexGap>
        {/* The headline carries the ticking age, so it is hidden from screen
            readers; the coarse label beside it says the same thing without
            announcing every second (§4.2). */}
        <Typography variant="body2" sx={MONO} aria-hidden>
          {ask.headline}
        </Typography>
        <Box sx={VISUALLY_HIDDEN}>
          {`${ask.worker === '' ? 'a worker' : ask.worker} · ${ask.status} · waiting ${coarseAgeLabel(ask.waitingSeconds)}`}
        </Box>
        {ageColor !== undefined && (
          <Typography variant="caption" sx={{ color: ageColor }}>
            {escalation === 'fault' ? 'waiting over 4h' : 'waiting over 1h'}
          </Typography>
        )}
        {highlightMarker({ active: arrived, tone: 'ask', reduced }) && (
          <Chip size="small" variant="outlined" label={NEW_MARKER_LABEL} />
        )}
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
  arrived = false,
  reduced = false,
}: {
  change: DeskChange
  onOpenSession?: (id: string) => void
  arrived?: boolean
  reduced?: boolean
}) {
  // Authorship decides the tint, exactly as it decides the glyph (§3.2).
  const tone: HighlightTone = change.byAgent ? 'agent' : 'human'
  return (
    <SpineRow
      glyph={change.glyph}
      component="li"
      glyphLabel={change.byAgent ? 'a worker did this' : 'you did this'}
      sx={highlightSx({ active: arrived, tone, reduced })}
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
        {highlightMarker({ active: arrived, tone, reduced }) && (
          <Chip size="small" variant="outlined" label={NEW_MARKER_LABEL} />
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
