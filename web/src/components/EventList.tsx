// EventList — the project's recent events, newest first (§8.1).
//
// A presentational list: it receives events and reports selection. The fetch
// lives in useEventsOverview, one level up, because the jobs table and the
// replay panel read the same page of data and must agree on it.
//
// W4 gave it the three things a live list owes an operator (doc 21 §4.2): a
// WATERLINE at the mark, so "what is new" is a line on the screen and not a
// filter; a staged "N new" PILL, so arrivals never insert themselves under a
// reading eye; and an authorship-tinted ENTRANCE on the rows that did arrive,
// with a persistent `NEW` marker instead under reduced motion. Every one of
// those is derived from the one watermark integer the page holds — the list
// itself still decides nothing.

import { Fragment, useRef } from 'react'
import {
  Box,
  Chip,
  List,
  ListItem,
  ListItemButton,
  ListItemText,
  Stack,
  Typography,
} from '@mui/material'
import { formatTimestamp, type ProjectEvent } from '../events.js'
import { newItemsSummary, waterlineIndex, waterlineLabel } from '../watermark.js'
import {
  highlightSx,
  highlightMarker,
  NEW_MARKER_LABEL,
  type HighlightTone,
} from '../feedhighlight.js'
import { attentionColor } from './DeliveryStatusChip.js'
import usePrefersReducedMotion from '../useReducedMotion.js'
import useStagedFeed from '../useStagedFeed.js'
import { FeedWaterline, NewItemsPill, useAtHead } from './FeedLiveness.js'

export interface EventListProps {
  events: ProjectEvent[]
  selected: string | null
  loading?: boolean
  onSelect: (id: string | null) => void
  /** The operator's mark for this surface, unix MILLISECONDS. 0 = never looked. */
  lastSeenMs?: number
  /** The shared clock, unix ms — only used to phrase the waterline's label. */
  nowMs?: number
  /** True when live updates are paused: nothing inserts, nothing highlights. */
  paused?: boolean
}

export default function EventList({
  events,
  selected,
  loading,
  onSelect,
  lastSeenMs = 0,
  nowMs,
  paused = false,
}: EventListProps) {
  const reduced = usePrefersReducedMotion()
  // The head sentinel: rows may only insert themselves while the top of the
  // list is on screen (§4.2 — IntersectionObserver, never scrollTop).
  const headRef = useRef<HTMLDivElement | null>(null)
  const atHead = useAtHead(headRef)
  const feed = useStagedFeed(events, (e) => e.id, { atHead, paused })

  // An event stamped after the mark is new. Event rows are unix SECONDS and
  // the mark is milliseconds — the conversion happens here, where the unit of
  // the row is known, as everywhere else in this package.
  const divider = waterlineIndex(feed.visible, (e) => e.occurred_at * 1000, lastSeenMs)

  return (
    <Box>
      <Box sx={{ px: 2, py: 1.5 }} ref={headRef}>
        <Typography variant="subtitle2">Events</Typography>
      </Box>
      <NewItemsPill
        count={feed.stagedCount}
        summary={newItemsSummary(feed.stagedCount, 'event')}
        onShow={feed.flush}
      />
      {events.length === 0 ? (
        <Typography variant="body2" color="text.secondary" sx={{ px: 2, pb: 2 }}>
          {loading ? 'Loading events…' : 'No events yet.'}
        </Typography>
      ) : (
        // role="log" (§4.2): chronological, implicitly polite, and NOT
        // role="feed", whose keyboard contract this list does not implement.
        <List disablePadding aria-label="Events" role="log">
          {feed.visible.map((event, index) => (
            <Fragment key={event.id}>
              {/* The waterline is its own row, not a decoration inside one:
                  it separates the list, so it sits between two items. */}
              {index === divider && (
                <ListItem disablePadding sx={{ px: 2 }}>
                  <FeedWaterline label={waterlineLabel(lastSeenMs, nowMs)} />
                </ListItem>
              )}
              <ListItem disablePadding sx={eventRowSx(event, feed.arrivals, reduced)}>
              <ListItemButton
                selected={event.id === selected}
                onClick={() => onSelect(event.id === selected ? null : event.id)}
              >
                <ListItemText
                  primary={
                    <Stack direction="row" spacing={0.75} alignItems="center">
                      <Box component="span" sx={{ fontFamily: 'monospace' }}>
                        {event.type}
                      </Box>
                      {event.envelope.source !== '' && (
                        <Chip size="small" variant="outlined" label={event.envelope.source} />
                      )}
                      {event.envelope.attention_requested && (
                        <Chip
                          size="small"
                          label="attention"
                          sx={{
                            bgcolor: (t) => attentionColor(t),
                            color: (t) => t.palette.getContrastText(attentionColor(t)),
                          }}
                        />
                      )}
                      {highlightMarker({
                        active: feed.arrivals.has(event.id),
                        tone: eventTone(event),
                        reduced,
                      }) && <Chip size="small" variant="outlined" label={NEW_MARKER_LABEL} />}
                    </Stack>
                  }
                  secondary={
                    [
                      formatTimestamp(event.occurred_at),
                      `depth ${event.envelope.depth}`,
                      event.envelope.worker ? `from ${event.envelope.worker}` : '',
                      event.delivered ? '' : 'undelivered',
                    ]
                      .filter(Boolean)
                      .join(' · ') || undefined
                  }
                  primaryTypographyProps={{ variant: 'body2', component: 'div', noWrap: true }}
                  secondaryTypographyProps={{ variant: 'caption' }}
                />
              </ListItemButton>
              </ListItem>
            </Fragment>
          ))}
        </List>
      )}
    </Box>
  )
}

/** Authorship, as the spine reads it: an event that asked for a person is rose,
 *  anything a worker emitted is ember, anything else is the neutral ink. */
function eventTone(event: ProjectEvent): HighlightTone {
  if (event.envelope.attention_requested) return 'ask'
  return event.envelope.source === 'worker' ? 'agent' : 'human'
}

function eventRowSx(event: ProjectEvent, arrivals: ReadonlySet<string>, reduced: boolean) {
  return highlightSx({ active: arrivals.has(event.id), tone: eventTone(event), reduced })
}
