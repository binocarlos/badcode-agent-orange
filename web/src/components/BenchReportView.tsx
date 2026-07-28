// BenchReportView — read a comparison-rig `report.json` (work-plan BR1,
// operator-console design §7.3).
//
// No backend: the rig lives in `e2e/` and runs outside the product, so a report
// arrives as a dropped file. That is deliberate — a viewer that takes a file
// needs zero engine work and is useful the day it ships.
//
// The editorial rules of §7.3 are hard requirements, not styling:
//
//   - the Tier A banner is the first thing on the screen and CANNOT be
//     dismissed. Mock mode proves the machinery transmits a difference, never
//     that the system discovered one;
//   - churn (`prompt_writes`) is never the headline number and never sortable
//     to the front — the rig's own demo report is the proof, where the sham
//     critic and the genuine critic tie at 2 ±0 and only the property
//     predicates separate them. It renders muted, last, labelled "churn",
//     always beside the outcome column;
//   - spread is always rendered, never averaged away. In mock every spread is
//     0; a non-zero one is an alarm, not a data point;
//   - identical rewrites are deduped and counted separately, because
//     SetWorkerPrompt has no no-op short-circuit.
//
// Presentational: it holds the loaded report in component state and nothing
// else. All the reading is `parseBenchReport`.

import { useCallback, useRef, useState } from 'react'
import {
  Alert,
  Box,
  Button,
  Chip,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
  Typography,
  useTheme,
} from '@mui/material'
import {
  CHURN_METRIC,
  describeRewrites,
  formatMeanSpread,
  parseBenchReport,
  type BenchReport,
  type BenchRow,
} from '../benchreport.js'
import { consoleColor } from '../spine.js'

/** §7.3, verbatim. Never dismissable, always first. */
export const TIER_A_BANNER =
  'mock mode proves the machinery transmits a difference, never that the system discovered one.'

/** The note that keeps "47 rewrites" from being read as 47 ideas. */
export const DEDUPE_NOTE =
  'Identical rewrites are logged again — SetWorkerPrompt has no no-op short-circuit — so a rewrite count is deduped and shown as "n rewrites · m distinct".'

/** The spread rule, rendered when a mock report shows movement it should not. */
export const SPREAD_ALARM =
  'A non-zero spread in mock mode is an alarm, not a data point: the arms should be deterministic.'

const monoSx = { fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace' }

const columnLabel = (key: string): string =>
  key.startsWith('prop:') ? key.slice('prop:'.length) : key

export interface BenchReportViewProps {
  /** A report to show without a file drop (tests, or a host that has one). */
  report?: BenchReport | null
}

export default function BenchReportView({ report: initial = null }: BenchReportViewProps) {
  const theme = useTheme()
  const [report, setReport] = useState<BenchReport | null>(initial)
  const [error, setError] = useState<string | null>(null)
  const [dragging, setDragging] = useState(false)
  const inputRef = useRef<HTMLInputElement | null>(null)

  const load = useCallback(async (file: File) => {
    try {
      const text = await file.text()
      setReport(parseBenchReport(text))
      setError(null)
    } catch (e) {
      setReport(null)
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [])

  const muted = theme.palette.text.secondary
  const emberColor = consoleColor(theme, 'ember', theme.palette.mode === 'dark' ? '#E0873F' : '#B3541E')

  return (
    <Stack spacing={2} sx={{ p: 3 }}>
      {/* Not dismissable — no onClose, by design (§7.3). */}
      <Alert severity="warning" data-testid="bench-tier-a-banner">
        <Typography variant="body2">
          <strong>Tier A.</strong> {TIER_A_BANNER}
        </Typography>
      </Alert>

      <Box
        data-testid="bench-dropzone"
        onDragOver={(e) => {
          e.preventDefault()
          setDragging(true)
        }}
        onDragLeave={() => setDragging(false)}
        onDrop={(e) => {
          e.preventDefault()
          setDragging(false)
          const file = e.dataTransfer?.files?.[0]
          if (file) void load(file)
        }}
        sx={{
          border: 1,
          borderStyle: 'dashed',
          borderColor: dragging ? 'primary.main' : 'divider',
          borderRadius: 1,
          p: 2,
        }}
      >
        <Stack direction="row" spacing={2} alignItems="center" flexWrap="wrap">
          <Typography variant="body2" color="text.secondary">
            Drop a comparison report (<Box component="span" sx={monoSx}>report.json</Box>) here.
          </Typography>
          <Button size="small" variant="outlined" onClick={() => inputRef.current?.click()}>
            Choose file
          </Button>
          <input
            ref={inputRef}
            type="file"
            accept="application/json,.json"
            data-testid="bench-file-input"
            style={{ display: 'none' }}
            onChange={(e) => {
              const file = e.target.files?.[0]
              if (file) void load(file)
            }}
          />
        </Stack>
      </Box>

      {error !== null && (
        <Alert severity="error" data-testid="bench-error">
          {error}
        </Alert>
      )}

      {report === null ? (
        <Typography variant="body2" color="text.secondary">
          No report loaded. The rig writes one per run under{' '}
          <Box component="span" sx={monoSx}>
            e2e/experiments/reports/
          </Box>
          .
        </Typography>
      ) : (
        <Stack spacing={2}>
          <Box>
            <Typography variant="h6" sx={monoSx}>
              {report.task.id}
            </Typography>
            <Typography variant="body2" color="text.secondary">
              {report.task.description}
            </Typography>
            <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mt: 0.5 }}>
              {report.task.repetitions} repetitions × {report.task.rounds} rounds · ranked by{' '}
              <Box component="span" sx={monoSx}>
                {report.task.rankBy}
              </Box>{' '}
              ({report.task.rankDirection}) · mock script{' '}
              <Box component="span" sx={monoSx}>
                {report.task.mockScript}
              </Box>
            </Typography>
          </Box>

          {report.hasSpread && (
            <Alert severity="warning" data-testid="bench-spread-alarm">
              {SPREAD_ALARM}
            </Alert>
          )}

          <Box sx={{ overflowX: 'auto' }}>
            <Table size="small" data-testid="bench-table">
              <TableHead>
                <TableRow>
                  <TableCell>#</TableCell>
                  <TableCell>arm</TableCell>
                  <TableCell align="right">reps</TableCell>
                  {report.metricColumns.map((key) => (
                    <TableCell key={key} align="right" sx={monoSx}>
                      {columnLabel(key)}
                      {key === report.task.rankBy && (
                        <Typography
                          variant="caption"
                          sx={{ display: 'block', color: muted, fontFamily: 'inherit' }}
                        >
                          outcome
                        </Typography>
                      )}
                    </TableCell>
                  ))}
                  {/* Churn last, always: never the headline number. */}
                  <TableCell align="right" sx={{ color: muted }} data-testid="bench-churn-header">
                    churn
                    <Typography variant="caption" sx={{ display: 'block', ...monoSx }}>
                      {CHURN_METRIC}
                    </Typography>
                  </TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {report.rows.map((row) => (
                  <BenchTableRow
                    key={row.arm}
                    row={row}
                    columns={report.metricColumns}
                    rankBy={report.task.rankBy}
                    muted={muted}
                    ember={emberColor}
                  />
                ))}
              </TableBody>
            </Table>
          </Box>

          <Typography variant="caption" color="text.secondary" data-testid="bench-dedupe-note">
            {DEDUPE_NOTE}
          </Typography>

          {report.properties.length > 0 && (
            <Box>
              <Typography variant="subtitle2">Properties</Typography>
              <Stack spacing={0.5} sx={{ mt: 0.5 }}>
                {report.properties.map((p) => (
                  <Typography key={p.id} variant="body2" color="text.secondary">
                    <Box component="span" sx={monoSx}>
                      {p.id}
                    </Box>{' '}
                    — {p.describe}
                  </Typography>
                ))}
              </Stack>
            </Box>
          )}

          <Box>
            <Typography variant="subtitle2">Arms</Typography>
            <Stack direction="row" spacing={1} flexWrap="wrap" sx={{ mt: 0.5 }}>
              {report.arms.map((a) => (
                <Chip
                  key={a.id}
                  size="small"
                  variant="outlined"
                  sx={monoSx}
                  label={`${a.id} · ${a.topology} · ${a.primaryWorker}`}
                />
              ))}
            </Stack>
          </Box>
        </Stack>
      )}
    </Stack>
  )
}

function BenchTableRow({
  row,
  columns,
  rankBy,
  muted,
  ember,
}: {
  row: BenchRow
  columns: string[]
  rankBy: string
  muted: string
  ember: string
}) {
  return (
    <TableRow data-testid={`bench-row-${row.arm}`}>
      <TableCell sx={monoSx}>{row.rank}</TableCell>
      <TableCell sx={monoSx}>
        {row.arm}
        <Typography variant="caption" sx={{ display: 'block', color: muted, ...monoSx }}>
          {row.topology}
        </Typography>
      </TableCell>
      <TableCell align="right" sx={monoSx}>
        {row.reps}
      </TableCell>
      {columns.map((key) => {
        const m = row.metrics[key]
        return (
          <TableCell
            key={key}
            align="right"
            sx={{ ...monoSx, fontWeight: key === rankBy ? 600 : 400 }}
          >
            {m === undefined ? '—' : formatMeanSpread(m.mean, m.spread)}
          </TableCell>
        )
      })}
      <TableCell align="right" sx={{ ...monoSx, color: muted }}>
        {row.churn === null ? '—' : formatMeanSpread(row.churn.mean, row.churn.spread)}
        <Typography
          variant="caption"
          sx={{ display: 'block', color: row.rewrites > 0 ? ember : muted }}
        >
          {describeRewrites(row)}
        </Typography>
      </TableCell>
    </TableRow>
  )
}
