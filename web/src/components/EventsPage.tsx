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

import React, { useCallback, useEffect, useMemo, useState } from 'react'
import { Alert, Box, Divider, Stack, Tab, Tabs, Typography } from '@mui/material'
import useEventsOverview, { type UseEventsOverviewOptions } from '../useEvents.js'
import { buildEventSearch, eventFromSearch } from '../events.js'
import type { ConfigEventFetcher } from '../configLog.js'
import EventList from './EventList.js'
import EventDetail from './EventDetail.js'
import EventJobHistory from './EventJobHistory.js'
import EventReplayPanel from './EventReplayPanel.js'
import ChangelogView from './ChangelogView.js'

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
  /** How many job rows fetch their token totals unprompted. Default 10. */
  tokenAutoLoad?: number
  /**
   * Where the changelog gets its records while `GET /agent/config-events` does
   * not exist (J2/J3). Omit once the route is mounted.
   */
  fetchConfigEvents?: ConfigEventFetcher
}

type TabKey = 'events' | 'jobs' | 'replay' | 'changelog'

export default function EventsPage({
  projectId,
  selected: controlledSelected,
  onSelect,
  syncUrl = true,
  onOpenSession,
  enableChangelog = true,
  tokenAutoLoad,
  fetchConfigEvents,
  ...options
}: EventsPageProps) {
  const { events, subscriptions, jobs, loading, error, truncated } = useEventsOverview(options)

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
      </Box>
    </Stack>
  )
}
