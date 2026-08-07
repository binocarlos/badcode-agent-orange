// SubscriptionEditor — the routing table of spec §8.3, one row at a time:
// event-type pattern, envelope filter, worker, rate limit, enabled.
//
// Two deliberate features, both about not shipping a subscription that never
// fires:
//
//  1. **The live match preview.** F1's `matchSubscriptions` is a pure function,
//     so the editor can show, against the events the project has actually seen,
//     whether this pattern would have matched any of them — before saving. A
//     subscription that matches nothing is the commonest configuration mistake
//     here and the hardest to notice, because nothing happens.
//
//  2. **The NL assist**, compiling "when the email-answerer worker finishes"
//     into `worker.finished` + `{worker: "email-answerer"}` at config time,
//     echoed for confirmation. Never in the firing path — what is saved and
//     matched is the compiled pattern and filter, nothing else.
//
// Filters are edited as JSON (the shape is a flat equality map and the engine
// stores it as jsonb) with per-key validation against the envelope's field
// list: a filter on a field that does not exist matches nothing for ever.

import { useMemo, useRef, useState } from 'react'
import {
  Alert,
  Autocomplete,
  Box,
  Button,
  Chip,
  Divider,
  FormControlLabel,
  FormHelperText,
  Stack,
  Switch,
  TextField,
  Typography,
} from '@mui/material'
import JsonObjectEditor from './JsonObjectEditor.js'
import NlAssistField from './NlAssistField.js'
import { formatJsonObject, parseJsonObject } from '../projectSettings.js'
import { compileEnvelopeFilter, type CompileResult, type FilterProposal } from '../nlAssist.js'
import { matchSubscriptions, type ProjectEvent, type Subscription } from '../events.js'
import {
  describeSubscription,
  newSubscriptionDraft,
  validateSubscription,
  type SubscriptionDraft,
} from '../subscriptions.js'

export interface SubscriptionEditorProps {
  /** The subscription being edited. Omit (with isNew) to create one. */
  subscription?: Subscription | null
  isNew?: boolean
  /** Save handler — the parent owns the API call (useSubscriptions().save).
   *  The second argument is the operator's one-line reason (design B3 / K2). */
  onSave: (draft: SubscriptionDraft, rationale: string) => void | Promise<unknown>
  /** Delete handler. Omitted ⇒ no delete button. */
  onDelete?: (id: string, rationale: string) => void | Promise<unknown>
  error?: string | null
  saving?: boolean
  /** Known worker names for the picker. Free text without them. */
  workerOptions?: string[]
  /**
   * Recent events, used for the "would this have matched?" preview. Read-only
   * and local: the preview is a pure function over data already fetched, and
   * never posts anything.
   */
  recentEvents?: ProjectEvent[]
  /** Compile a description into a pattern + filter. Defaults to the built-in. */
  compileFilterDescription?: (
    text: string,
  ) => CompileResult<FilterProposal> | Promise<CompileResult<FilterProposal>>
}

export default function SubscriptionEditor({
  subscription = null,
  isNew = false,
  onSave,
  onDelete,
  error = null,
  saving = false,
  workerOptions = [],
  recentEvents = [],
  compileFilterDescription = compileEnvelopeFilter,
}: SubscriptionEditorProps) {
  const seed = () => (subscription ? { ...subscription } : newSubscriptionDraft())
  const [draft, setDraft] = useState<SubscriptionDraft>(seed)
  const [filterText, setFilterText] = useState(() => formatJsonObject(seed().filter))
  const [filterError, setFilterError] = useState<string | null>(null)
  const [rationale, setRationale] = useState('')
  const [dirty, setDirty] = useState(false)

  // Re-seed on identity change, render-phase (see WorkerEditor's note).
  const seededFor = useRef<string | null>(null)
  const identity = isNew ? ' new' : (subscription?.id ?? ' none')
  if (seededFor.current !== identity) {
    seededFor.current = identity
    const next = seed()
    setDraft(next)
    setFilterText(formatJsonObject(next.filter))
    setFilterError(null)
    setRationale('')
    setDirty(false)
  }

  const update = (patch: Partial<SubscriptionDraft>) => {
    setDraft((prev) => ({ ...prev, ...patch }))
    setDirty(true)
  }

  const fieldErrors = useMemo(() => validateSubscription(draft), [draft])
  const canSave =
    !saving &&
    filterError === null &&
    Object.keys(fieldErrors).length === 0 &&
    rationale.trim() !== '' &&
    dirty
  const filterProblems = Object.entries(fieldErrors).filter(([k]) => k.startsWith('filter.'))

  // The dry run: which of the recent events this row would have woken a worker
  // for. Pure, local, and never a POST — the same function the events view uses.
  const wouldMatch = useMemo(
    () =>
      recentEvents.filter(
        (ev) =>
          matchSubscriptions({ type: ev.type, envelope: ev.envelope }, [draft as Subscription])[0]
            ?.matched === true,
      ),
    [draft, recentEvents],
  )

  const applyProposal = (value: FilterProposal) => {
    const filter = value.filter as Record<string, unknown>
    setFilterText(formatJsonObject(filter))
    setFilterError(null)
    update({
      filter,
      ...(value.event_type === undefined ? {} : { event_type: value.event_type }),
    })
  }

  const handleSave = () => {
    const parsed = parseJsonObject(filterText)
    if (!parsed.ok) {
      setFilterError(parsed.error)
      return
    }
    void onSave({ ...draft, filter: parsed.value }, rationale)
    setDirty(false)
  }

  return (
    <Box sx={{ p: 3, maxWidth: 880 }}>
      {error !== null && (
        <Alert severity="error" sx={{ mb: 2 }}>
          {error}
        </Alert>
      )}

      <Stack spacing={3}>
        <Box>
          <TextField
            label="Event type"
            fullWidth
            value={draft.event_type}
            placeholder="email.received or email.*"
            error={fieldErrors.event_type !== undefined}
            onChange={(e) => update({ event_type: e.target.value })}
            inputProps={{ 'aria-label': 'Event type' }}
            sx={{ maxWidth: 420, '& input': { fontFamily: 'monospace' } }}
          />
          <FormHelperText error={fieldErrors.event_type !== undefined}>
            {fieldErrors.event_type ??
              'An exact type, or a trailing * prefix. There are no other patterns — anything finer belongs in the reacting worker’s prompt.'}
          </FormHelperText>
        </Box>

        <Box>
          <Autocomplete
            freeSolo
            options={workerOptions}
            value={draft.worker}
            inputValue={draft.worker}
            onInputChange={(_e, value) => update({ worker: value })}
            renderInput={(params) => (
              <TextField
                {...params}
                label="Worker"
                placeholder="email-answerer"
                error={fieldErrors.worker !== undefined}
                inputProps={{ ...params.inputProps, 'aria-label': 'Worker' }}
              />
            )}
            sx={{ maxWidth: 420 }}
          />
          <FormHelperText error={fieldErrors.worker !== undefined}>
            {fieldErrors.worker ?? 'The worker a job is started for on every match.'}
          </FormHelperText>
        </Box>

        <JsonObjectEditor
          id="subscription-filter"
          label="Envelope filter"
          value={filterText}
          onChange={(text) => {
            setFilterText(text)
            setDirty(true)
            const parsed = parseJsonObject(text)
            setFilterError(parsed.ok ? null : parsed.error)
            if (parsed.ok) setDraft((prev) => ({ ...prev, filter: parsed.value }))
          }}
          error={filterError}
          rows={5}
          helperText='Equality on envelope fields only, e.g. {"worker": "email-answerer", "interactive": false}. Empty {} matches every event of the type.'
        />
        {filterProblems.map(([key, problem]) => (
          <FormHelperText key={key} error>
            {key.slice('filter.'.length)}: {problem}
          </FormHelperText>
        ))}

        <NlAssistField<FilterProposal>
          label="Describe what should trigger this"
          placeholder="when the email-answerer worker finishes"
          helperText={
            'Compiled to an event type and an envelope filter here, once, and checked by you ' +
            'before it is applied — the router only ever sees the compiled values above.'
          }
          compile={compileFilterDescription}
          applyLabel="Use this"
          onApply={applyProposal}
          renderProposal={(value) => (
            <Typography variant="body2" sx={{ fontFamily: 'monospace' }}>
              {value.event_type ?? '(type unchanged)'} {JSON.stringify(value.filter)}
            </Typography>
          )}
        />

        <Divider />

        <Box>
          <TextField
            label="Max firings per hour"
            type="number"
            size="small"
            value={String(draft.max_firings_per_hour)}
            error={fieldErrors.max_firings_per_hour !== undefined}
            onChange={(e) => {
              // An emptied box means zero, not NaN.
              const raw = e.target.value.trim()
              update({ max_firings_per_hour: raw === '' ? 0 : Number(raw) })
            }}
            inputProps={{ 'aria-label': 'Max firings per hour', min: 0 }}
            sx={{ maxWidth: 220 }}
          />
          <FormHelperText error={fieldErrors.max_firings_per_hour !== undefined}>
            {fieldErrors.max_firings_per_hour ??
              (draft.max_firings_per_hour === 0
                ? '0 = unlimited.'
                : `Excess deliveries are recorded rate_limited, with one subscription.throttled event per hour.`)}
          </FormHelperText>
        </Box>

        <Box>
          <FormControlLabel
            control={
              <Switch
                checked={draft.enabled}
                onChange={(e) => update({ enabled: e.target.checked })}
                inputProps={{ 'aria-label': 'Enabled' }}
              />
            }
            label={draft.enabled ? 'Enabled' : 'Disabled'}
          />
        </Box>

        <Alert severity="info" icon={false}>
          <Stack spacing={0.5}>
            <span>{describeSubscription(draft)}</span>
            {recentEvents.length > 0 && (
              <Typography variant="caption" color="text.secondary">
                Would have matched {wouldMatch.length} of the last {recentEvents.length} events.
                {wouldMatch.length === 0 && ' Nothing recent matches this — check the type and the filter.'}
              </Typography>
            )}
            {wouldMatch.length > 0 && (
              <Stack direction="row" spacing={0.5} flexWrap="wrap" useFlexGap>
                {[...new Set(wouldMatch.map((ev) => ev.type))].slice(0, 6).map((type) => (
                  <Chip key={type} size="small" label={type} />
                ))}
              </Stack>
            )}
          </Stack>
        </Alert>

        <Divider />

        <Box>
          <TextField
            label="Why?"
            fullWidth
            size="small"
            value={rationale}
            placeholder="the reviewer should see every answered mail"
            onChange={(e) => setRationale(e.target.value)}
            inputProps={{ 'aria-label': 'Why?' }}
          />
          <FormHelperText>
            {rationale.trim() === ''
              ? 'Required. One line, stored with the change in the config log — the changelog reads it next to who made it.'
              : 'Stored with the change in the config log, and shown in the changelog next to who made it.'}
          </FormHelperText>
        </Box>

        <Stack direction="row" spacing={2} alignItems="center">
          <Button variant="contained" disabled={!canSave} onClick={handleSave}>
            {saving ? 'Saving…' : isNew ? 'Create subscription' : 'Save subscription'}
          </Button>
          {onDelete && !isNew && draft.id !== '' && (
            <Button color="error" disabled={saving} onClick={() => void onDelete(draft.id, rationale)}>
              Delete
            </Button>
          )}
          {!dirty && !isNew && (
            <Typography variant="caption" color="text.secondary">
              No unsaved changes
            </Typography>
          )}
        </Stack>
      </Stack>
    </Box>
  )
}
