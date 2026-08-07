// EventReplayPanel — paste or replay an event, see which subscriptions would
// match it, and (deliberately, behind a confirm) actually emit it (learning
// L27, work-plan F1; readiness item F1 / RD17).
//
// The matcher half is config-time and pure: it answers "would this route?" and
// touches nothing. The emit half is the ONE place this package POSTs an event
// into a project (`POST /agent/events`). It used to be absent on purpose — a
// "test" button that QUIETLY posted a real event into a live org is a footgun —
// but the absence was worse: 11 of the 14 built-in topologies are event-driven
// only, so after applying one an operator had no way to make anything happen at
// all (docs/product/15-operator-console-design.md:258-266, "One button").
// The answer to the footgun is a LABELLED confirm, not silence: the dialog says
// in plain words that this writes a real event and may wake workers, and
// nothing is posted until it is accepted.
//
// The matcher models only the two rules §8.3 defines. Rate limits, budget stops
// and max_instances gating are left out on purpose: they depend on live
// counters, and a dry run that guessed at them would be confidently wrong.

import { useMemo, useState } from 'react'
import {
  Alert,
  Box,
  Button,
  Chip,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  List,
  ListItem,
  ListItemText,
  Stack,
  TextField,
  Typography,
} from '@mui/material'
import { useConfigApi, type ConfigApiOptions } from '../configApi.js'
import {
  coerceProjectEvent,
  EVENT_DRAFT_TEMPLATE,
  EVENT_ENDPOINTS,
  eventToDraftText,
  matchSubscriptions,
  parseEventDraft,
  type ProjectEvent,
  type Subscription,
} from '../events.js'

export interface EventReplayPanelProps extends ConfigApiOptions {
  subscriptions: Subscription[]
  /** The event currently selected in the list, offered as "replay this one". */
  selectedEvent?: ProjectEvent | null
  /** Initial editor contents. Defaults to EVENT_DRAFT_TEMPLATE. */
  initialText?: string
  /**
   * Offer the "Emit this event" button. Default true. A host that mounts this
   * panel purely as documentation (or for a reader who must not write) passes
   * false and gets the original dry-run-only panel back.
   */
  enableEmit?: boolean
  /** Override the ingestion endpoint (defaults to EVENT_ENDPOINTS.events). */
  eventsEndpoint?: string
  /** Called with the created event after a successful emit — reload the lists. */
  onEmitted?: (event: ProjectEvent) => void
}

export default function EventReplayPanel({
  subscriptions,
  selectedEvent,
  initialText = EVENT_DRAFT_TEMPLATE,
  enableEmit = true,
  eventsEndpoint = EVENT_ENDPOINTS.events,
  onEmitted,
  ...apiOptions
}: EventReplayPanelProps) {
  const [text, setText] = useState(initialText)
  const parsed = useMemo(() => parseEventDraft(text), [text])
  const matches = useMemo(
    () => (parsed.ok ? matchSubscriptions(parsed.event, subscriptions) : []),
    [parsed, subscriptions],
  )
  const matchedCount = matches.filter((m) => m.matched).length

  const { request } = useConfigApi(apiOptions)
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [emitting, setEmitting] = useState(false)
  const [emitError, setEmitError] = useState<string | null>(null)
  const [emitted, setEmitted] = useState<ProjectEvent | null>(null)

  const emit = async () => {
    if (!parsed.ok) return
    setEmitting(true)
    setEmitError(null)
    try {
      // Only {type, text} goes on the wire: core stamps the envelope and
      // ignores any envelope in the body (httpapi/events.go, ingestEventBody).
      // Sending the drafted envelope would imply it was honoured.
      const created = await request<unknown>(eventsEndpoint, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ type: parsed.event.type, text: parsed.event.text }),
      })
      const event = coerceProjectEvent(created)
      setEmitted(event)
      setConfirmOpen(false)
      onEmitted?.(event)
    } catch (err) {
      // The server's own text, not "HTTP 400" — these routes answer in
      // sentences a human can act on (configApi's convention).
      setEmitError(err instanceof Error ? err.message : 'failed to emit the event')
    } finally {
      setEmitting(false)
    }
  }

  return (
    <Box sx={{ p: 3, maxWidth: 960 }}>
      <Typography variant="subtitle2" sx={{ mb: 0.5 }}>
        Event replay / subscription test
      </Typography>
      <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mb: 2 }}>
        {enableEmit
          ? 'Matching is a dry run: editing this event posts nothing and starts no job. “Emit this event” is the exception — it writes a real event into the project, and asks first.'
          : 'A dry run. Nothing here is posted, no job is started, and no delivery row is written — this only asks the routing table what it would do.'}
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
        {enableEmit && (
          <Button
            size="small"
            variant="contained"
            color="warning"
            sx={{ ml: 'auto' }}
            disabled={!parsed.ok}
            onClick={() => {
              setEmitError(null)
              setConfirmOpen(true)
            }}
          >
            Emit this event
          </Button>
        )}
      </Stack>

      {emitted !== null && (
        <Alert severity="success" sx={{ mb: 2 }}>
          Emitted “{emitted.type}” — event id <code>{emitted.id}</code>. Open it in Events to
          follow the deliveries it produced.
        </Alert>
      )}

      <Dialog open={confirmOpen} onClose={() => (emitting ? undefined : setConfirmOpen(false))}>
        <DialogTitle>Emit a real event?</DialogTitle>
        <DialogContent>
          <DialogContentText component="div">
            <p>
              This is not a dry run. It writes a <strong>real</strong> event of type{' '}
              <code>{parsed.ok ? parsed.event.type : ''}</code> into this project, and any
              subscription that matches will <strong>wake its worker and start a job</strong>.
              Right now {matchedCount === 0
                ? 'no subscription matches, so nothing would start — the event is still recorded.'
                : `${matchedCount} of ${subscriptions.length} subscriptions match.`}
            </p>
            <p>
              Only <code>type</code> and <code>text</code> are sent. Core stamps the envelope
              (<code>source: external</code>, <code>depth: 0</code>) and ignores the one drafted
              above, so envelope filters will be tested against core&apos;s values, not yours.
            </p>
          </DialogContentText>
          {emitError !== null && (
            <Alert severity="error" sx={{ mt: 1 }}>
              {emitError}
            </Alert>
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setConfirmOpen(false)} disabled={emitting}>
            Cancel
          </Button>
          <Button variant="contained" color="warning" onClick={() => void emit()} disabled={emitting}>
            {emitting ? 'Emitting…' : 'Emit it'}
          </Button>
        </DialogActions>
      </Dialog>

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
