// ScheduleEditor — the schedule half of spec §8.6: worker, cron, the
// instruction each firing delivers, and the enabled toggle. Work-plan item F2.
//
// Two things this screen exists to make obvious:
//
//  1. **`input` is the centre of gravity.** A schedule does not only say WHEN a
//     worker runs, it says WHAT IT IS TOLD each time — "10:00 → write the
//     morning tweet" and "17:00 → write the evening tweet" are two rows against
//     one worker. The field is therefore large and first-class, not a footnote.
//
//  2. **A cron expression is unreadable, so it is read back.** Every valid
//     expression is echoed in words under the field ("At 09:00, on weekdays."),
//     and the NL assist compiles a description INTO those five fields at config
//     time — never at firing time, and never without the human seeing the
//     result first.
//
// The editor holds its own draft and re-seeds on identity change, the same
// contract WorkerEditor documents.

import React, { useMemo, useRef, useState } from 'react'
import {
  Alert,
  Autocomplete,
  Box,
  Button,
  Divider,
  FormControlLabel,
  FormHelperText,
  Stack,
  Switch,
  TextField,
  Typography,
} from '@mui/material'
import NlAssistField from './NlAssistField.js'
import { compileCron, type CompileResult, type CronProposal } from '../nlAssist.js'
import {
  describeCron,
  newScheduleDraft,
  validateSchedule,
  type Schedule,
  type ScheduleDraft,
} from '../schedules.js'

export interface ScheduleEditorProps {
  /** The schedule being edited. Omit (with isNew) to create one. */
  schedule?: Schedule | null
  /** True when creating. */
  isNew?: boolean
  /** Save handler — the parent owns the API call (useSchedules().save). */
  onSave: (draft: ScheduleDraft, rationale: string) => void | Promise<unknown>
  /** Delete handler. Omitted ⇒ no delete button. */
  onDelete?: (id: string) => void | Promise<unknown>
  /** Save/delete failure from the parent. */
  error?: string | null
  saving?: boolean
  /** Known worker names for the picker. Free text without them. */
  workerOptions?: string[]
  /**
   * Compile a natural-language description into a cron expression. Defaults to
   * the deterministic built-in; a host may inject a model-backed compiler with
   * the same contract (propose, never save, refuse rather than guess).
   */
  compileCronDescription?: (text: string) => CompileResult<CronProposal> | Promise<CompileResult<CronProposal>>
}

export default function ScheduleEditor({
  schedule = null,
  isNew = false,
  onSave,
  onDelete,
  error = null,
  saving = false,
  workerOptions = [],
  compileCronDescription = compileCron,
}: ScheduleEditorProps) {
  const seed = () => (schedule ? { ...schedule } : newScheduleDraft())
  const [draft, setDraft] = useState<ScheduleDraft>(seed)
  const [rationale, setRationale] = useState('')
  const [dirty, setDirty] = useState(false)

  // Re-seed when the parent selects a different schedule. Render-phase, keyed
  // on identity rather than a useEffect, so the first paint of the new
  // selection already shows the new row.
  const seededFor = useRef<string | null>(null)
  const identity = isNew ? ' new' : (schedule?.id ?? ' none')
  if (seededFor.current !== identity) {
    seededFor.current = identity
    setDraft(seed())
    setRationale('')
    setDirty(false)
  }

  const update = (patch: Partial<ScheduleDraft>) => {
    setDraft((prev) => ({ ...prev, ...patch }))
    setDirty(true)
  }

  const fieldErrors = useMemo(() => validateSchedule(draft), [draft])
  const canSave = !saving && Object.keys(fieldErrors).length === 0 && dirty
  const cronSentence = describeCron(draft.cron)

  return (
    <Box sx={{ p: 3, maxWidth: 880 }}>
      {error !== null && (
        <Alert severity="error" sx={{ mb: 2 }}>
          {error}
        </Alert>
      )}

      <Stack spacing={3}>
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
                placeholder="tweet-author"
                error={fieldErrors.worker !== undefined}
                inputProps={{ ...params.inputProps, 'aria-label': 'Worker' }}
              />
            )}
            sx={{ maxWidth: 420 }}
          />
          <FormHelperText error={fieldErrors.worker !== undefined}>
            {fieldErrors.worker ??
              'The worker a job is started for on every firing. A schedule whose worker no longer exists is disabled, not retried.'}
          </FormHelperText>
        </Box>

        <Box>
          <TextField
            label="Cron"
            fullWidth
            value={draft.cron}
            placeholder="0 9 * * 1-5"
            error={fieldErrors.cron !== undefined}
            onChange={(e) => update({ cron: e.target.value })}
            inputProps={{ 'aria-label': 'Cron' }}
            sx={{ maxWidth: 420, '& input': { fontFamily: 'monospace' } }}
          />
          <FormHelperText error={fieldErrors.cron !== undefined}>
            {fieldErrors.cron ??
              cronSentence ??
              'Five fields: minute hour day-of-month month day-of-week, in the server’s time zone.'}
          </FormHelperText>
        </Box>

        <NlAssistField<CronProposal>
          label="Describe the timing"
          placeholder="every weekday at 09:00"
          helperText={
            'Compiled to five cron fields here, once, and checked by you before it is applied — ' +
            'the schedule itself always runs the expression above. Understood: “every 15 minutes”, ' +
            '“every day at 9am”, “every weekday at 17:30”, “on Mondays and Thursdays at 08:00”.'
          }
          compile={compileCronDescription}
          applyLabel="Use this cron"
          onApply={(value) => update({ cron: value.cron })}
          renderProposal={(value) => (
            <Typography variant="body2" sx={{ fontFamily: 'monospace' }}>
              {value.cron}
            </Typography>
          )}
        />

        <Divider />

        <Box>
          <TextField
            label="Instruction"
            multiline
            minRows={4}
            maxRows={20}
            fullWidth
            value={draft.input}
            placeholder="Write the morning tweet."
            onChange={(e) => update({ input: e.target.value })}
            inputProps={{ 'aria-label': 'Instruction' }}
          />
          <FormHelperText>
            {draft.input.trim() === ''
              ? 'Empty: the worker will wake with no instruction. This is what the trigger tells it each time it fires — say what you want done.'
              : 'This text becomes the event the worker receives. Changing it changes what the worker is asked on the next firing.'}
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
          <FormHelperText>
            Firings missed while the server was down are skipped, never replayed — a
            tweet-writer must not wake to a backlog of stale mornings.
          </FormHelperText>
        </Box>

        <Box>
          <TextField
            label="Rationale"
            fullWidth
            size="small"
            value={rationale}
            placeholder="moving the morning tweet an hour later; engagement peaks at 10"
            onChange={(e) => setRationale(e.target.value)}
            inputProps={{ 'aria-label': 'Rationale' }}
          />
          <FormHelperText>
            Optional commit message. It is stored with the change in the config log and shown in
            the changelog, next to who made it.
          </FormHelperText>
        </Box>

        <Divider />

        <Stack direction="row" spacing={2} alignItems="center">
          <Button variant="contained" disabled={!canSave} onClick={() => {
            void onSave(draft, rationale)
            setDirty(false)
          }}>
            {saving ? 'Saving…' : isNew ? 'Create schedule' : 'Save schedule'}
          </Button>
          {onDelete && !isNew && draft.id !== '' && (
            <Button color="error" disabled={saving} onClick={() => void onDelete(draft.id)}>
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
