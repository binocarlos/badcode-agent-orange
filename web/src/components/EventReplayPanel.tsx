// EventReplayPanel — paste or replay an event and see which subscriptions would
// match it (learning L27, work-plan F1).
//
// CONFIG-TIME ONLY. This panel has no POST, no ingestion button, and no way to
// reach the firing path: it answers "would this route?" and nothing else. That
// is the whole design constraint — a "test" button that quietly posted a real
// event into a project would be a footgun in exactly the situation people reach
// for it (debugging a live org).
//
// The matcher it uses is pure (events.ts) and models only the two rules §8.3
// defines. Rate limits, budget stops and max_instances gating are left out on
// purpose: they depend on live counters, and a dry run that guessed at them
// would be confidently wrong.

import { useMemo, useState } from 'react'
import {
  Alert,
  Box,
  Button,
  Chip,
  List,
  ListItem,
  ListItemText,
  Stack,
  TextField,
  Typography,
} from '@mui/material'
import {
  EVENT_DRAFT_TEMPLATE,
  eventToDraftText,
  matchSubscriptions,
  parseEventDraft,
  type ProjectEvent,
  type Subscription,
} from '../events.js'

export interface EventReplayPanelProps {
  subscriptions: Subscription[]
  /** The event currently selected in the list, offered as "replay this one". */
  selectedEvent?: ProjectEvent | null
  /** Initial editor contents. Defaults to EVENT_DRAFT_TEMPLATE. */
  initialText?: string
}

export default function EventReplayPanel({
  subscriptions,
  selectedEvent,
  initialText = EVENT_DRAFT_TEMPLATE,
}: EventReplayPanelProps) {
  const [text, setText] = useState(initialText)
  const parsed = useMemo(() => parseEventDraft(text), [text])
  const matches = useMemo(
    () => (parsed.ok ? matchSubscriptions(parsed.event, subscriptions) : []),
    [parsed, subscriptions],
  )
  const matchedCount = matches.filter((m) => m.matched).length

  return (
    <Box sx={{ p: 3, maxWidth: 960 }}>
      <Typography variant="subtitle2" sx={{ mb: 0.5 }}>
        Event replay / subscription test
      </Typography>
      <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mb: 2 }}>
        A dry run. Nothing here is posted, no job is started, and no delivery row is written —
        this only asks the routing table what it would do.
      </Typography>

      <Stack direction="row" spacing={1} sx={{ mb: 1.5 }}>
        <Button
          size="small"
          disabled={!selectedEvent}
          onClick={() => selectedEvent && setText(eventToDraftText(selectedEvent))}
        >
          {selectedEvent ? `Load “${selectedEvent.type}”` : 'Load selected event'}
        </Button>
        <Button size="small" onClick={() => setText(EVENT_DRAFT_TEMPLATE)}>
          Reset to template
        </Button>
      </Stack>

      <TextField
        label="Event JSON"
        value={text}
        onChange={(e) => setText(e.target.value)}
        multiline
        minRows={10}
        fullWidth
        error={!parsed.ok}
        helperText={
          parsed.ok
            ? 'Ingestion (POST /agent/events) accepts only {type, text} — core stamps the envelope. The envelope here lets you test filters against an event core would have stamped.'
            : parsed.error
        }
        inputProps={{ 'aria-label': 'Event JSON', spellCheck: false }}
        sx={{ '& textarea': { fontFamily: 'monospace', fontSize: '0.8125rem' } }}
      />

      <Box sx={{ mt: 3 }}>
        {!parsed.ok ? null : subscriptions.length === 0 ? (
          <Alert severity="info">
            This project has no subscriptions, so nothing can match. Create one first.
          </Alert>
        ) : (
          <>
            <Typography variant="subtitle2" sx={{ mb: 1 }}>
              {matchedCount === 0
                ? 'No subscription would match — this event would start no jobs.'
                : `${matchedCount} of ${subscriptions.length} subscriptions would match.`}
            </Typography>
            <List disablePadding aria-label="Subscription match results">
              {matches.map(({ subscription, matched, reason }) => (
                <ListItem key={subscription.id} disableGutters>
                  <ListItemText
                    primary={
                      <Stack direction="row" spacing={1} alignItems="center">
                        <Chip
                          size="small"
                          color={matched ? 'success' : 'default'}
                          variant={matched ? 'filled' : 'outlined'}
                          label={matched ? 'match' : 'no match'}
                        />
                        <Box component="span" sx={{ fontFamily: 'monospace' }}>
                          {subscription.event_type}
                        </Box>
                        <Typography component="span" variant="body2" color="text.secondary">
                          → {subscription.worker}
                        </Typography>
                      </Stack>
                    }
                    secondary={reason}
                    primaryTypographyProps={{ component: 'div' }}
                    secondaryTypographyProps={{ variant: 'caption' }}
                  />
                </ListItem>
              ))}
            </List>
            <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mt: 2 }}>
              Matching only. Rate limits, the per-project concurrent-jobs cap, budget stops and a
              worker’s max_instances all depend on live counters and are not simulated — a match
              here can still queue as pending.
            </Typography>
          </>
        )}
      </Box>
    </Box>
  )
}
