// EventsPage — the observability surface that replaces the deleted watchapi
// cockpit (work-plan F1): recent events, the deliveries they produced, the jobs
// those became, a config-time subscription test, and the changelog (§15.10).
//
// Read-only by construction. Nothing on this screen writes anything: the event
// routes it calls are the two GETs E1 added plus the subscriptions list, and the
// replay panel is a pure function. Editing subscriptions and schedules is F2.
//
// Router-free, F3's way: the selected event is one query parameter written
// through the History API, so a host that already has a router passes
// `selected` + `onSelect` and this component never touches the URL.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Alert, Box, Divider, Stack, Tab, Tabs, Typography } from '@mui/material'
import useEventsOverview, { type UseEventsOverviewOptions } from '../useEvents.js'
import { buildEventSearch, deliveryDurationSeconds, eventFromSearch } from '../events.js'
import type { ConfigEventFetcher } from '../configLog.js'
import { useFeedWatermark } from '../watermark.js'
import useElapsedTicker, { tickIntervalForRows } from '../useElapsedTicker.js'
import usePrefersReducedMotion from '../useReducedMotion.js'
import EventList from './EventList.js'
import EventDetail from './EventDetail.js'
import EventJobHistory from './EventJobHistory.js'
import EventReplayPanel from './EventReplayPanel.js'
import ChangelogView from './ChangelogView.js'
import BenchReportView from './BenchReportView.js'
import { PauseLiveUpdates } from './FeedLiveness.js'

/** The surface name this page's watermark is stored under (watermark.ts). */
export const EVENTS_SURFACE = 'events'

export interface EventsPageProps extends UseEventsOverviewOptions {
  /** Project id — scopes the session permalinks this page builds. */
  projectId: string
  /**
   * Controlled selection (an event id). Pass it with onSelect when the host
   * owns routing; doing so also disables this component's own URL writing.
   */
  selected?: string | null
  onSelect?: (id: string | null) => void
  /** Write `?event=` into the URL. Ignored when `selected` is controlled. */
  syncUrl?: boolean
  /** Called when a session link is clicked — typically useSessionPermalink().openSession. */
  onOpenSession?: (sessionId: string) => void
  /** Render the changelog tab. Default true. */
  enableChangelog?: boolean
  /**
   * Render the bench tab — the comparison rig's report viewer (BR1). Default
   * true. It has no backend at all: a report is a dropped file.
   */
  enableBench?: boolean
  /** How many job rows fetch their token totals unprompted. Default 10. */
  tokenAutoLoad?: number
  /**
   * Where the changelog gets its records while `GET /agent/config-events` does
   * not exist (J2/J3). Omit once the route is mounted.
   */
  fetchConfigEvents?: ConfigEventFetcher
  /**
   * Poll the lists every N ms. 0 (the default) never polls — a library
   * component that starts fetching on a timer because it was mounted is a
   * surprise, so a host that wants a live tail asks for one.
   */
  refreshMs?: number
  /** Show the "Pause live updates" toggle. Default: only while polling. */
  showPauseToggle?: boolean
}

type TabKey = 'events' | 'jobs' | 'replay' | 'changelog' | 'bench'

export default function EventsPage({
  projectId,
  selected: controlledSelected,
  onSelect,
  syncUrl = true,
  onOpenSession,
  enableChangelog = true,
  enableBench = true,
  tokenAutoLoad,
  fetchConfigEvents,
  refreshMs = 0,
  showPauseToggle,
  ...options
}: EventsPageProps) {
  const reduced = usePrefersReducedMotion()
  // A reduced-motion operator starts paused: the request is not only about
  // animation, it is about not being chased down the page.
  const [paused, setPaused] = useState(reduced)
  const overview = useEventsOverview(options)
  const { events, deliveries, subscriptions, jobs, loading, error, truncated } = overview

  // The mark for THIS surface — the same integer scheme the Desk uses, under a
  // different surface name (watermark.ts), never a second storage scheme.
  const watermark = useFeedWatermark(EVENTS_SURFACE, projectId)

  // One ticker for the page, at the cadence its own rows ask for: per second
  // while something is young and running, per minute after, and no timer at
  // all once everything has finished.
  const [tickMs, setTickMs] = useState(0)
  // A caller that pinned `nowSeconds` pinned the whole page's clock, ticker
  // included — a table of durations must not tick against a different `now`
  // from the one its rows were joined with.
  const nowMs = useElapsedTicker({
    intervalMs: tickMs,
    paused,
    nowMs: options.nowSeconds === undefined ? undefined : options.nowSeconds * 1000,
  })
  const nowSeconds = Math.floor(nowMs / 1000)
  const wantedTick = useMemo(
    () =>
      tickIntervalForRows(
        deliveries.map((d) => ({
          status: d.status,
          elapsedSeconds: deliveryDurationSeconds(d, nowSeconds) ?? 0,
        })),
      ),
    [deliveries, nowSeconds],
  )
  useEffect(() => setTickMs(wantedTick), [wantedTick])

  // The poll, held in a ref so its interval is created once per cadence rather
  // than rebuilt every render (`reload`'s identity changes with the hook's).
  const reloadRef = useRef(overview.reload)
  reloadRef.current = overview.reload
  useEffect(() => {
    if (refreshMs <= 0 || paused) return
    const id = setInterval(() => void reloadRef.current(), refreshMs)
    return () => clearInterval(id)
  }, [paused, refreshMs])

  const showPause = showPauseToggle ?? refreshMs > 0

  const controlled = controlledSelected !== undefined
  const hasWindow = typeof window !== 'undefined'
  const urlEnabled = !controlled && syncUrl && hasWindow

  const [internalSelected, setInternalSelected] = useState<string | null>(() =>
    urlEnabled ? eventFromSearch(window.location.search) : null,
  )
  const [tab, setTab] = useState<TabKey>('events')

  const selected = controlled ? controlledSelected! : internalSelected

  const select = useCallback(
    (id: string | null) => {
      if (!controlled) setInternalSelected(id)
      if (urlEnabled) {
        const search = buildEventSearch(window.location.search, id)
        window.history.pushState(null, '', window.location.pathname + search + window.location.hash)
      }
      onSelect?.(id)
    },
    [controlled, onSelect, urlEnabled],
  )

  // Back/forward moves the selection when we own the URL. Subscribing to a
  // browser event with a matching unsubscribe is what useEffect is for (unlike
  // one-shot init, which this package does with a render-phase ref-guard).
  useEffect(() => {
    if (!urlEnabled) return
    const onPop = () => setInternalSelected(eventFromSearch(window.location.search))
    window.addEventListener('popstate', onPop)
    return () => window.removeEventListener('popstate', onPop)
  }, [urlEnabled])

  const current = useMemo(
    () => events.find((e) => e.id === selected) ?? null,
    [events, selected],
  )
  const jobsForCurrent = useMemo(
    () => (current ? jobs.filter((j) => j.delivery.event_id === current.id) : []),
    [current, jobs],
  )

  // ConfigApiOptions travel to the children; the fetch-shaping options do not.
  const { apiBaseUrl, getAuthToken } = options
  const apiOptions = { apiBaseUrl, getAuthToken }

  return (
    <Stack sx={{ height: '100%', minHeight: 0 }}>
      <Tabs value={tab} onChange={(_e, v: TabKey) => setTab(v)} sx={{ px: 2 }}>
        <Tab value="events" label="Events" />
        <Tab value="jobs" label="Jobs" />
        <Tab value="replay" label="Replay" />
        {enableChangelog && <Tab value="changelog" label="Changelog" />}
        {enableBench && <Tab value="bench" label="Bench" />}
        {showPause && (
          <Box sx={{ ml: 'auto', alignSelf: 'center', pr: 2 }}>
            <PauseLiveUpdates paused={paused} onChange={setPaused} />
          </Box>
        )}
      </Tabs>
      <Divider />

      {error !== null && (
        <Alert severity="error" sx={{ m: 2 }}>
          {error}
        </Alert>
      )}

      <Box sx={{ flex: 1, minHeight: 0, overflow: 'hidden' }}>
        {tab === 'events' && (
          <Stack direction="row" sx={{ height: '100%', minHeight: 0 }}>
            <Box
              sx={{
                width: 320,
                flexShrink: 0,
                borderRight: 1,
                borderColor: 'divider',
                overflowY: 'auto',
              }}
            >
              <EventList
                events={events}
                selected={selected}
                loading={loading}
                onSelect={select}
                lastSeenMs={watermark.markMs}
                nowMs={nowMs}
                paused={paused}
              />
            </Box>
            <Box sx={{ flex: 1, minWidth: 0, overflowY: 'auto' }}>
              {current === null ? (
                <Box sx={{ p: 3 }}>
                  <Typography variant="body2" color="text.secondary">
                    Select an event to see its envelope, its text, and the jobs it started.
                  </Typography>
                </Box>
              ) : (
                <EventDetail
                  event={current}
                  jobs={jobsForCurrent}
                  projectId={projectId}
                  onOpenSession={onOpenSession}
                  tokenAutoLoad={tokenAutoLoad}
                  {...apiOptions}
                />
              )}
            </Box>
          </Stack>
        )}

        {tab === 'jobs' && (
          <Box sx={{ p: 3, height: '100%', overflowY: 'auto' }}>
            <EventJobHistory
              jobs={jobs}
              projectId={projectId}
              onOpenSession={onOpenSession}
              title="Job history"
              truncated={truncated}
              tokenAutoLoad={tokenAutoLoad}
              nowSeconds={nowSeconds}
              {...apiOptions}
            />
          </Box>
        )}

        {tab === 'replay' && (
          <Box sx={{ height: '100%', overflowY: 'auto' }}>
            <EventReplayPanel subscriptions={subscriptions} selectedEvent={current} />
          </Box>
        )}

        {tab === 'changelog' && enableChangelog && (
          <Box sx={{ height: '100%', overflowY: 'auto' }}>
            <ChangelogView
              projectId={projectId}
              fetchConfigEvents={fetchConfigEvents}
              onOpenSession={onOpenSession}
              {...apiOptions}
            />
          </Box>
        )}
        {tab === 'bench' && enableBench && (
          <Box sx={{ height: '100%', overflowY: 'auto' }}>
            <BenchReportView />
          </Box>
        )}
      </Box>
    </Stack>
  )
}
