// TopologyOnboarding — the "start from a topology" flow (work plan 13 T3):
// pick a topology from the built-in catalogue → answer its questions →
// preview the diff against the project's current config → apply.
//
// The preview step is the contract, not decoration: apply is disabled whenever
// the server says `applicable: false` (a colliding worker name or a missing
// image/skill), and the reasons are shown where the disabled button is. Even
// then the server re-checks inside its transaction — a 409 from a race is
// rendered verbatim, exactly as the server phrased it.
//
// Router-free like every page here: the parent decides where "done" and
// "cancel" go. WorkersPage mounts it behind a sentinel selection; a host with
// its own router can mount it anywhere.

import React, { useState } from 'react'
import {
  Alert,
  Box,
  Button,
  Chip,
  CircularProgress,
  Divider,
  FormControlLabel,
  Link,
  List,
  ListItem,
  ListItemText,
  MenuItem,
  Paper,
  Stack,
  Switch,
  TextField,
  Tooltip,
  Typography,
} from '@mui/material'
import LockIcon from '@mui/icons-material/Lock'
import useTopologies from '../useTopologies.js'
import type { ConfigApiOptions } from '../configApi.js'
import { FROZEN_SENTENCE } from '../workers.js'
import { describeCron } from '../schedules.js'
import {
  initialTopologyAnswers,
  topologyAnswersBody,
  topologyRef,
  validateTopologyAnswers,
  type Topology,
  type TopologyAnswers,
  type TopologyApplyResult,
  type TopologyPreview,
} from '../topologies.js'

export interface TopologyOnboardingProps extends ConfigApiOptions {
  /** Called after a successful apply — reload the worker list here. */
  onApplied?: (result: TopologyApplyResult) => void
  /** Leave the flow (the Cancel button, and "View workers" once applied). */
  onClose?: () => void
  /** Open the project changelog, where the `topology_apply` receipt lives.
   *  Omitted ⇒ the success screen names the receipt instead of linking it. */
  onOpenChangelog?: () => void
}

type Step = 'pick' | 'questions' | 'preview' | 'done'

export default function TopologyOnboarding({
  onApplied,
  onClose,
  onOpenChangelog,
  ...apiOptions
}: TopologyOnboardingProps) {
  const api = useTopologies(apiOptions)

  const [step, setStep] = useState<Step>('pick')
  const [topology, setTopology] = useState<Topology | null>(null)
  const [answers, setAnswers] = useState<TopologyAnswers>({})
  const [preview, setPreview] = useState<TopologyPreview | null>(null)
  const [result, setResult] = useState<TopologyApplyResult | null>(null)

  const choose = (t: Topology) => {
    setTopology(t)
    setAnswers(initialTopologyAnswers(t.questions))
    setPreview(null)
    setStep('questions')
  }

  const runPreview = async () => {
    if (!topology) return
    const body = topologyAnswersBody(topology.questions, answers)
    const p = await api.preview(topology.name, topology.version, body)
    if (p) {
      setPreview(p)
      setStep('preview')
    }
  }

  const runApply = async () => {
    if (!topology) return
    const body = topologyAnswersBody(topology.questions, answers)
    const r = await api.apply(topology.name, topology.version, body)
    if (r) {
      setResult(r)
      setStep('done')
      onApplied?.(r)
    }
  }

  return (
    <Box sx={{ p: 3, maxWidth: 720 }}>
      <Typography variant="h6" sx={{ mb: 0.5 }}>
        Start from a topology
      </Typography>
      <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mb: 2 }}>
        A topology is a pre-built org chart: workers, the subscriptions and schedules that wake
        them, and any seed memory — applied to this project in one step, and recorded in the
        changelog as one entry.
      </Typography>

      {step === 'pick' && (
        <PickStep
          topologies={api.topologies}
          loading={api.loading}
          error={api.error}
          onChoose={choose}
          onClose={onClose}
        />
      )}

      {step === 'questions' && topology && (
        <QuestionStep
          topology={topology}
          answers={answers}
          onChange={(id, value) => setAnswers((prev) => ({ ...prev, [id]: value }))}
          previewing={api.previewing}
          previewError={api.previewError}
          onBack={() => setStep('pick')}
          onPreview={() => void runPreview()}
        />
      )}

      {step === 'preview' && topology && preview && (
        <PreviewStep
          preview={preview}
          applying={api.applying}
          applyError={api.applyError}
          onBack={() => setStep('questions')}
          onApply={() => void runApply()}
        />
      )}

      {step === 'done' && topology && result && (
        <DoneStep
          topology={topology}
          result={result}
          onClose={onClose}
          onOpenChangelog={onOpenChangelog}
        />
      )}
    </Box>
  )
}

// ---------------------------------------------------------------------------
// Step 1: the catalogue
// ---------------------------------------------------------------------------

function PickStep({
  topologies,
  loading,
  error,
  onChoose,
  onClose,
}: {
  topologies: Topology[]
  loading: boolean
  error: string | null
  onChoose: (t: Topology) => void
  onClose?: () => void
}) {
  return (
    <Stack spacing={2}>
      {error !== null && <Alert severity="error">{error}</Alert>}

      {loading ? (
        <Box sx={{ p: 3, display: 'flex', justifyContent: 'center' }}>
          <CircularProgress size={24} aria-label="Loading the topology catalogue" />
        </Box>
      ) : topologies.length === 0 && error === null ? (
        <Typography variant="body2" color="text.secondary">
          No topologies are available.
        </Typography>
      ) : (
        topologies.map((t) => (
          <Paper key={topologyRef(t)} variant="outlined" sx={{ p: 2 }}>
            <Stack direction="row" spacing={1} alignItems="center">
              <Typography variant="subtitle2">{t.name}</Typography>
              <Chip size="small" variant="outlined" label={t.version} sx={{ fontFamily: 'monospace' }} />
            </Stack>
            <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5 }}>
              {t.description}
            </Typography>
            <Button size="small" sx={{ mt: 1 }} onClick={() => onChoose(t)}>
              Choose {t.name}
            </Button>
          </Paper>
        ))
      )}

      {onClose && (
        <Box>
          <Button size="small" onClick={onClose}>
            Cancel
          </Button>
        </Box>
      )}
    </Stack>
  )
}

// ---------------------------------------------------------------------------
// Step 2: the interview
// ---------------------------------------------------------------------------

function QuestionStep({
  topology,
  answers,
  onChange,
  previewing,
  previewError,
  onBack,
  onPreview,
}: {
  topology: Topology
  answers: TopologyAnswers
  onChange: (id: string, value: string | boolean) => void
  previewing: boolean
  previewError: string | null
  onBack: () => void
  onPreview: () => void
}) {
  const errors = validateTopologyAnswers(topology.questions, answers)
  const canPreview = !previewing && Object.keys(errors).length === 0

  return (
    <Stack spacing={2}>
      <Stack direction="row" spacing={1} alignItems="center">
        <Typography variant="subtitle1">{topology.name}</Typography>
        <Chip size="small" variant="outlined" label={topology.version} sx={{ fontFamily: 'monospace' }} />
      </Stack>

      {previewError !== null && <Alert severity="error">{previewError}</Alert>}

      {topology.questions.length === 0 ? (
        <Typography variant="body2" color="text.secondary">
          This topology has no questions — preview it directly.
        </Typography>
      ) : (
        topology.questions.map((q) => {
          const value = answers[q.id]
          if (q.type === 'bool') {
            return (
              <FormControlLabel
                key={q.id}
                control={
                  <Switch
                    checked={value === true}
                    onChange={(e) => onChange(q.id, e.target.checked)}
                  />
                }
                label={q.prompt}
              />
            )
          }
          const text = typeof value === 'string' ? value : ''
          const common = {
            label: q.prompt,
            value: text,
            required: q.required,
            error: errors[q.id] !== undefined,
            helperText: errors[q.id],
            size: 'small' as const,
            onChange: (e: React.ChangeEvent<HTMLInputElement>) => onChange(q.id, e.target.value),
          }
          if (q.type === 'choice') {
            return (
              <TextField key={q.id} select {...common}>
                {!q.required && <MenuItem value="">(unanswered)</MenuItem>}
                {q.choices.map((c) => (
                  <MenuItem key={c} value={c}>
                    {c}
                  </MenuItem>
                ))}
              </TextField>
            )
          }
          return <TextField key={q.id} {...common} />
        })
      )}

      <Stack direction="row" spacing={1}>
        <Button size="small" onClick={onBack}>
          Back
        </Button>
        <Button size="small" variant="contained" disabled={!canPreview} onClick={onPreview}>
          Preview
        </Button>
      </Stack>
    </Stack>
  )
}

// ---------------------------------------------------------------------------
// Step 3: the diff
// ---------------------------------------------------------------------------

function PreviewStep({
  preview,
  applying,
  applyError,
  onBack,
  onApply,
}: {
  preview: TopologyPreview
  applying: boolean
  applyError: string | null
  onBack: () => void
  onApply: () => void
}) {
  const { diff, bundle } = preview
  const byName = new Map(bundle.workers.map((w) => [w.name, w]))

  return (
    <Stack spacing={2}>
      <Typography variant="subtitle1">
        What applying {topologyRef(preview.topology)} would do
      </Typography>

      {applyError !== null && <Alert severity="error">{applyError}</Alert>}

      {!preview.applicable && (
        <Alert severity="error">
          This topology cannot be applied to this project as it stands — resolve the problems
          below first. Nothing has been changed.
        </Alert>
      )}

      {diff.colliding_workers.length > 0 && (
        <Alert severity="error">
          Worker names already taken in this project:{' '}
          {diff.colliding_workers.join(', ')}
        </Alert>
      )}
      {preview.missing_images.length > 0 && (
        <Alert severity="error">
          Images this topology needs but the project does not have:{' '}
          {preview.missing_images.join(', ')}
        </Alert>
      )}
      {preview.missing_skills.length > 0 && (
        <Alert severity="error">
          Skills this topology needs but the project does not have:{' '}
          {preview.missing_skills.join(', ')}
        </Alert>
      )}

      <Section title={`Workers to create (${diff.new_workers.length})`}>
        {diff.new_workers.length === 0 ? (
          <None />
        ) : (
          <List dense disablePadding>
            {diff.new_workers.map((name) => {
              const w = byName.get(name)
              return (
                <ListItem key={name} disableGutters>
                  <ListItemText
                    primary={
                      <Stack direction="row" spacing={1} alignItems="center">
                        <span>{name}</span>
                        {w?.frozen && (
                          <Tooltip title={FROZEN_SENTENCE}>
                            <Chip
                              size="small"
                              color="info"
                              variant="outlined"
                              icon={<LockIcon />}
                              label="frozen"
                            />
                          </Tooltip>
                        )}
                      </Stack>
                    }
                    secondary={w?.description || undefined}
                    primaryTypographyProps={{ variant: 'body2' }}
                    secondaryTypographyProps={{ variant: 'caption' }}
                  />
                </ListItem>
              )
            })}
          </List>
        )}
      </Section>

      <Section title={`Subscriptions (${diff.new_subscriptions.length})`}>
        {diff.new_subscriptions.length === 0 ? (
          <None />
        ) : (
          <List dense disablePadding>
            {diff.new_subscriptions.map((s, i) => (
              <ListItem key={i} disableGutters>
                <ListItemText
                  primary={`${s.event_type} → ${s.worker}`}
                  primaryTypographyProps={{ variant: 'body2', fontFamily: 'monospace' }}
                />
              </ListItem>
            ))}
          </List>
        )}
      </Section>

      <Section title={`Schedules (${diff.new_schedules.length})`}>
        {diff.new_schedules.length === 0 ? (
          <None />
        ) : (
          <List dense disablePadding>
            {diff.new_schedules.map((s, i) => (
              <ListItem key={i} disableGutters>
                <ListItemText
                  primary={`${s.cron} → ${s.worker}`}
                  secondary={describeCron(s.cron) ?? s.input}
                  primaryTypographyProps={{ variant: 'body2', fontFamily: 'monospace' }}
                  secondaryTypographyProps={{ variant: 'caption' }}
                />
              </ListItem>
            ))}
          </List>
        )}
      </Section>

      {diff.settings_fields.length > 0 && (
        <Section title="Project settings it would set">
          <Stack direction="row" spacing={1} flexWrap="wrap" useFlexGap>
            {diff.settings_fields.map((f) => (
              <Chip key={f} size="small" label={f} sx={{ fontFamily: 'monospace' }} />
            ))}
          </Stack>
        </Section>
      )}

      {diff.memory_seeds > 0 && (
        <Section title="Memory">
          <Typography variant="body2">
            {diff.memory_seeds} seed {diff.memory_seeds === 1 ? 'entry' : 'entries'} would be
            written to project memory.
          </Typography>
        </Section>
      )}

      <Divider />
      <Stack direction="row" spacing={1}>
        <Button size="small" onClick={onBack}>
          Back
        </Button>
        <Button
          size="small"
          variant="contained"
          disabled={!preview.applicable || applying}
          onClick={onApply}
        >
          Apply topology
        </Button>
      </Stack>
    </Stack>
  )
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <Box>
      <Typography variant="subtitle2" sx={{ mb: 0.5 }}>
        {title}
      </Typography>
      {children}
    </Box>
  )
}

function None() {
  return (
    <Typography variant="body2" color="text.secondary">
      None.
    </Typography>
  )
}

// ---------------------------------------------------------------------------
// Step 4: the receipt
// ---------------------------------------------------------------------------

function DoneStep({
  topology,
  result,
  onClose,
  onOpenChangelog,
}: {
  topology: Topology
  result: TopologyApplyResult
  onClose?: () => void
  onOpenChangelog?: () => void
}) {
  return (
    <Stack spacing={2}>
      <Alert severity="success">
        Applied {topologyRef(topology)}: {result.workers.length}{' '}
        {result.workers.length === 1 ? 'worker' : 'workers'}, {result.subscriptions.length}{' '}
        {result.subscriptions.length === 1 ? 'subscription' : 'subscriptions'},{' '}
        {result.schedules.length} {result.schedules.length === 1 ? 'schedule' : 'schedules'}
        {result.memories.length > 0 &&
          `, ${result.memories.length} memory ${result.memories.length === 1 ? 'seed' : 'seeds'}`}
        .
      </Alert>

      <Typography variant="body2" color="text.secondary">
        The whole apply is one changelog entry
        {onOpenChangelog ? (
          <>
            {' — '}
            <Link component="button" type="button" variant="body2" onClick={onOpenChangelog}>
              see it in the changelog
            </Link>
            .
          </>
        ) : (
          <>
            {' '}
            (action <code>topology_apply</code>
            {result.event.id !== '' && (
              <>
                , id <code>{result.event.id}</code>
              </>
            )}
            ).
          </>
        )}
      </Typography>

      {onClose && (
        <Box>
          <Button size="small" variant="contained" onClick={onClose}>
            View workers
          </Button>
        </Box>
      )}
    </Stack>
  )
}
