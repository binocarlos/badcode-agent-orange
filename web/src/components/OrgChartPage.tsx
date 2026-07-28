// OrgChartPage — the org chart, read-only, plus the propagation panel
// (operator-console design §6.1, §6.3, §6.5).
//
// A **patchbay, not a flowchart** (§3.7): orthogonal hairline routes, right-
// angled joints, event types set in mono *riding the wire*, name plates with a
// 1px rule instead of a card, frozen workers double-ruled in `steel` with the
// lock, schedules as 24-hour dials docked to the left of a plate.
//
// Hand-rolled SVG, no graph library (§6.2): the layout is OC1's pure
// `layoutOrgChart`, and this file only draws it. **No stored positions** (§6.3)
// — pan and zoom live in component state and nowhere else, so two people
// looking at the same project see the same chart and a chart that changed shape
// means the organisation changed shape.
//
// Read-only in this item: no drag, no toggles. Wiring and freezing on the
// canvas are OC4's, the conventions overlay is OC3's.
//
// Colour policy is spine.tsx's: the four named values come from the host theme
// when it has them (`consoleColor`) and fall back to the design's own tokens,
// so `web/` never depends on `examples/web`'s palette augmentation.

import { useCallback, useMemo, useRef, useState, type PointerEvent as ReactPointerEvent } from 'react'
import {
  Alert,
  Box,
  Button,
  Chip,
  Paper,
  Stack,
  TextField,
  Typography,
  useTheme,
  type Theme,
} from '@mui/material'
import type { ConfigApiOptions } from '../configApi.js'
import useEventsOverview from '../useEvents.js'
import useSchedules from '../useSchedules.js'
import useWorkers from '../useWorkers.js'
import {
  EVENT_DRAFT_TEMPLATE,
  blankEnvelope,
  parseEventDraft,
  type MatchableEvent,
} from '../events.js'
import {
  ORG_CHART_METRICS,
  PROPAGATION_CAVEAT,
  PROPAGATION_MAX_DEPTH,
  PROPAGATION_NOTHING_SUBSCRIBES,
  PROPAGATION_STOP_LINE,
  layoutOrgChart,
  propagateEvent,
  type OrgChartClock,
  type OrgChartLayout,
  type OrgChartNode,
  type OrgChartPip,
  type OrgChartWire,
} from '../orgchart.js'
import { SpineGlyph, consoleColor } from '../spine.js'

/** Identifiers are mono, content is prose (§3.4). */
const MONO = 'ui-monospace, SFMono-Regular, Menlo, monospace'

/** The design's §3.3 tokens, per mode — the fallback when a host theme carries
 *  no named entries. Same policy, and the same copy, as spine.tsx. */
const TOKENS = {
  light: { ember: '#B3541E', steel: '#2F6272', rose: '#A6376A', fault: '#8F2B2B' },
  dark: { ember: '#E0873F', steel: '#6FA6B8', rose: '#DF7BA4', fault: '#D96C6C' },
} as const

function token(theme: Theme, name: keyof (typeof TOKENS)['light']): string {
  return consoleColor(theme, name, TOKENS[theme.palette.mode === 'dark' ? 'dark' : 'light'][name])
}

export interface OrgChartPageProps extends ConfigApiOptions {
  /** Project id — for copy only; the token already scopes every read. */
  projectId?: string
  /** Rows per underlying list. Passed to useEventsOverview. */
  limit?: number
  /** Clock in unix seconds. Injectable so the state line is testable. */
  nowSeconds?: number
  /** Heading. Pass '' for none. */
  title?: string
}

export default function OrgChartPage({
  projectId,
  limit,
  nowSeconds,
  title = 'Org chart',
  ...apiOptions
}: OrgChartPageProps) {
  const theme = useTheme()
  const { workers, loading: workersLoading, error: workersError } = useWorkers(apiOptions)
  const overview = useEventsOverview({ ...apiOptions, limit, nowSeconds })
  const { schedules, error: schedulesError } = useSchedules(apiOptions)

  const layout = useMemo(
    () => layoutOrgChart(workers, overview.subscriptions, schedules, overview.events),
    [overview.events, overview.subscriptions, schedules, workers],
  )

  // `● running n/max`: live, from the deliveries the events view already reads.
  const running = useMemo(() => {
    const counts = new Map<string, number>()
    for (const job of overview.jobs) {
      if (job.status !== 'running' || job.worker === '') continue
      counts.set(job.worker, (counts.get(job.worker) ?? 0) + 1)
    }
    return counts
  }, [overview.jobs])

  // --- Propagation (§6.5) -------------------------------------------------
  const [traced, setTraced] = useState<{ label: string; event: MatchableEvent } | null>(null)
  const [draft, setDraft] = useState('')
  const [draftError, setDraftError] = useState<string | null>(null)

  const propagation = useMemo(
    () => (traced === null ? null : propagateEvent(traced.event, overview.subscriptions)),
    [overview.subscriptions, traced],
  )
  const litSubscriptions = useMemo(() => {
    const lit = new Set<string>()
    for (const hop of propagation?.hops ?? []) {
      for (const wake of hop.wakes) lit.add(wake.subscriptionId)
    }
    return lit
  }, [propagation])
  const litWorkers = useMemo(() => {
    const lit = new Set<string>()
    for (const hop of propagation?.hops ?? []) for (const wake of hop.wakes) lit.add(wake.worker)
    return lit
  }, [propagation])

  const tracePip = useCallback((pip: OrgChartPip) => {
    setDraftError(null)
    setTraced((prev) =>
      prev?.label === pip.type ? null : { label: pip.type, event: { type: pip.type, envelope: blankEnvelope() } },
    )
  }, [])

  const traceDraft = useCallback(() => {
    const parsed = parseEventDraft(draft)
    if (!parsed.ok) {
      setDraftError(parsed.error)
      setTraced(null)
      return
    }
    setDraftError(null)
    setTraced({ label: parsed.event.type, event: parsed.event })
  }, [draft])

  const error = workersError ?? overview.error ?? schedulesError

  return (
    <Box sx={{ p: 3 }}>
      <Stack direction="row" alignItems="baseline" spacing={2} sx={{ mb: 1 }}>
        {title !== '' && <Typography variant="h6">{title}</Typography>}
        <Typography variant="caption" color="text.secondary">
          workers, the events that wake them, and the clocks that fire them
        </Typography>
      </Stack>

      {error !== null && (
        <Alert severity="error" sx={{ mb: 2 }}>
          {error}
        </Alert>
      )}

      {!workersLoading && workers.length === 0 ? (
        <Paper variant="outlined" sx={{ p: 3, maxWidth: 620 }}>
          <Typography variant="subtitle1" sx={{ mb: 0.5 }}>
            This project has no workers yet
          </Typography>
          <Typography variant="body2" color="text.secondary">
            Nothing will run until something wakes them. Hire a worker on the Workers page — or
            start from an org chart, which hires a set of them and wires them together in one step.
          </Typography>
        </Paper>
      ) : (
        <Canvas
          layout={layout}
          theme={theme}
          running={running}
          litWires={litSubscriptions}
          litWorkers={litWorkers}
          tracedPip={traced?.label ?? null}
          onTracePip={tracePip}
        />
      )}

      <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mt: 1 }}>
        Positions are derived from the workers and subscriptions themselves and are never stored, so
        two people see the same chart and a chart that changed shape means the organisation changed
        shape.
      </Typography>

      <Propagation
        pips={layout.entryPips}
        tracedLabel={traced?.label ?? null}
        propagation={propagation}
        draft={draft}
        draftError={draftError}
        onDraftChange={setDraft}
        onTraceDraft={traceDraft}
        onTracePip={tracePip}
        theme={theme}
      />
      {projectId !== undefined && projectId !== '' && (
        <Typography variant="caption" color="text.disabled" sx={{ display: 'block', mt: 2 }}>
          project <Box component="span" sx={{ fontFamily: MONO }}>{projectId}</Box>
        </Typography>
      )}
    </Box>
  )
}

// ---------------------------------------------------------------------------
// The canvas
// ---------------------------------------------------------------------------

interface CanvasProps {
  layout: OrgChartLayout
  theme: Theme
  running: Map<string, number>
  litWires: Set<string>
  litWorkers: Set<string>
  tracedPip: string | null
  onTracePip: (pip: OrgChartPip) => void
}

const ZOOM_STEP = 1.25
const ZOOM_MIN = 0.4
const ZOOM_MAX = 3

function Canvas({ layout, theme, running, litWires, litWorkers, tracedPip, onTracePip }: CanvasProps) {
  // §6.3: pan and zoom are the ONLY state this screen has, and it dies with the
  // component. Nothing here is persisted, and no node is draggable.
  const [view, setView] = useState({ scale: 1, x: 0, y: 0 })
  const drag = useRef<{ x: number; y: number; ox: number; oy: number } | null>(null)

  const onPointerDown = (e: ReactPointerEvent<SVGSVGElement>) => {
    drag.current = { x: e.clientX, y: e.clientY, ox: view.x, oy: view.y }
    e.currentTarget.setPointerCapture?.(e.pointerId)
  }
  const onPointerMove = (e: ReactPointerEvent<SVGSVGElement>) => {
    const from = drag.current
    if (from === null) return
    setView((v) => ({ ...v, x: from.ox + (e.clientX - from.x), y: from.oy + (e.clientY - from.y) }))
  }
  const endDrag = () => {
    drag.current = null
  }
  const zoom = (factor: number) =>
    setView((v) => ({ ...v, scale: Math.min(ZOOM_MAX, Math.max(ZOOM_MIN, v.scale * factor)) }))

  const hairline = theme.palette.divider
  const ember = token(theme, 'ember')

  return (
    <Box>
      <Stack direction="row" spacing={1} sx={{ mb: 1 }} alignItems="center">
        <Button size="small" onClick={() => zoom(1 / ZOOM_STEP)} aria-label="zoom out">
          −
        </Button>
        <Button size="small" onClick={() => zoom(ZOOM_STEP)} aria-label="zoom in">
          +
        </Button>
        <Button size="small" onClick={() => setView({ scale: 1, x: 0, y: 0 })}>
          Reset view
        </Button>
        <Typography variant="caption" color="text.secondary">
          drag to pan
        </Typography>
      </Stack>
      <Box
        sx={{
          border: 1,
          borderColor: 'divider',
          bgcolor: 'background.paper',
          overflow: 'hidden',
          touchAction: 'none',
        }}
      >
        <Box
          component="svg"
          role="img"
          aria-label="org chart"
          data-testid="org-chart-canvas"
          viewBox={`0 0 ${layout.width} ${layout.height}`}
          preserveAspectRatio="xMidYMin meet"
          onPointerDown={onPointerDown}
          onPointerMove={onPointerMove}
          onPointerUp={endDrag}
          onPointerLeave={endDrag}
          sx={{ width: '100%', height: Math.min(640, layout.height + 40), display: 'block', cursor: 'grab' }}
        >
          <defs>
            <marker
              id="org-chart-arrow"
              viewBox="0 0 8 8"
              refX="7"
              refY="4"
              markerWidth="7"
              markerHeight="7"
              orient="auto-start-reverse"
            >
              <path d="M0 0 L8 4 L0 8 z" fill={hairline} />
            </marker>
            <marker
              id="org-chart-arrow-lit"
              viewBox="0 0 8 8"
              refX="7"
              refY="4"
              markerWidth="7"
              markerHeight="7"
              orient="auto-start-reverse"
            >
              <path d="M0 0 L8 4 L0 8 z" fill={ember} />
            </marker>
          </defs>
          <g transform={`translate(${view.x} ${view.y}) scale(${view.scale})`}>
            {layout.wires.map((wire) => (
              <Wire key={wire.id} wire={wire} theme={theme} lit={litWires.has(wire.subscriptionId)} />
            ))}
            {layout.entryPips.map((pip) => (
              <Pip
                key={pip.type}
                pip={pip}
                theme={theme}
                selected={tracedPip === pip.type}
                onSelect={() => onTracePip(pip)}
              />
            ))}
            {layout.clocks.map((clock) => (
              <Clock key={clock.id} clock={clock} theme={theme} />
            ))}
            {layout.nodes.map((node) => (
              <Plate
                key={node.name}
                node={node}
                theme={theme}
                running={running.get(node.name) ?? 0}
                lit={litWorkers.has(node.name)}
              />
            ))}
          </g>
        </Box>
      </Box>
    </Box>
  )
}

/** A subscription: an orthogonal hairline with its event type riding it. */
function Wire({ wire, theme, lit }: { wire: OrgChartWire; theme: Theme; lit: boolean }) {
  const ember = token(theme, 'ember')
  const stroke = lit ? ember : theme.palette.divider
  const points = wire.points.map((p) => `${p.x},${p.y}`).join(' ')
  const label = wire.back ? `${wire.label} ↺` : wire.label
  return (
    <g data-testid={`wire-${wire.id}`} opacity={wire.enabled ? 1 : 0.4}>
      <title>
        {`${wire.from ?? wire.fromPip} wakes ${wire.to} on ${wire.label}`}
        {wire.enabled ? '' : ' (disabled — it will not fire until you enable it)'}
        {wire.back ? ' — this edge closes a loop' : ''}
      </title>
      <polyline
        points={points}
        fill="none"
        stroke={stroke}
        strokeWidth={lit ? 2 : 1}
        markerEnd={`url(#${lit ? 'org-chart-arrow-lit' : 'org-chart-arrow'})`}
      />
      <text
        x={wire.labelX}
        y={wire.labelY - 4}
        textAnchor="middle"
        fontFamily={MONO}
        fontSize={10}
        fill={lit ? ember : theme.palette.text.secondary}
        stroke={theme.palette.background.paper}
        strokeWidth={3}
        paintOrder="stroke"
      >
        {label}
      </text>
    </g>
  )
}

/** A worker: a name plate with a 1px rule, never a card (§3.7). */
function Plate({
  node,
  theme,
  running,
  lit,
}: {
  node: OrgChartNode
  theme: Theme
  running: number
  lit: boolean
}) {
  const steel = token(theme, 'steel')
  const ember = token(theme, 'ember')
  const rule = node.frozen ? steel : lit ? ember : theme.palette.text.primary
  return (
    <g data-testid={`node-${node.name}`} opacity={node.enabled ? 1 : 0.55}>
      <title>{node.description === '' ? node.name : `${node.name} — ${node.description}`}</title>
      <rect
        x={node.x}
        y={node.y}
        width={node.width}
        height={node.height}
        fill={theme.palette.background.paper}
        stroke={rule}
        strokeWidth={1}
      />
      {/* Frozen is a DOUBLE rule in steel plus the lock: a sealed instrument,
          not a disabled row (§6.1). */}
      {node.frozen && (
        <rect
          x={node.x + 3}
          y={node.y + 3}
          width={node.width - 6}
          height={node.height - 6}
          fill="none"
          stroke={steel}
          strokeWidth={1}
        />
      )}
      <text x={node.x + 12} y={node.y + 22} fontFamily={MONO} fontSize={13} fill={theme.palette.text.primary}>
        {node.name}
      </text>
      {node.frozen && (
        <g transform={`translate(${node.x + node.width - 26} ${node.y + 10})`}>
          <SpineGlyph glyph="freeze" label="frozen — only a human may change it" size={14} />
        </g>
      )}
      <text
        x={node.x + 12}
        y={node.y + 40}
        fontSize={11}
        fill={theme.palette.text.secondary}
      >
        {truncate(node.description, 30)}
      </text>
      <text
        x={node.x + 12}
        y={node.y + 58}
        fontFamily={MONO}
        fontSize={11}
        fill={running > 0 ? ember : theme.palette.text.secondary}
      >
        {stateLine(node, running)}
      </text>
    </g>
  )
}

/** The state line (§6.1): `● running n/max` · `idle` · `disabled` · `frozen`. */
export function stateLine(
  node: Pick<OrgChartNode, 'enabled' | 'frozen' | 'maxInstances'>,
  running: number,
): string {
  if (!node.enabled) return 'disabled'
  if (running > 0) return `● running ${running}/${node.maxInstances}`
  if (node.frozen) return `frozen 0/${node.maxInstances}`
  return `idle 0/${node.maxInstances}`
}

function truncate(text: string, max: number): string {
  return text.length <= max ? text : `${text.slice(0, max - 1)}…`
}

/** A schedule: a 24-hour dial docked left of its plate, firing hours ticked. */
function Clock({ clock, theme }: { clock: OrgChartClock; theme: Theme }) {
  const ember = token(theme, 'ember')
  const r = clock.size / 2
  const cx = clock.x + r
  const cy = clock.y + r
  const ticks = []
  for (let hour = 0; hour < 24; hour += 1) {
    const on = clock.hours.includes(hour)
    const angle = (hour / 24) * Math.PI * 2 - Math.PI / 2
    const inner = on ? r - 5 : r - 3
    ticks.push(
      <line
        key={hour}
        x1={cx + Math.cos(angle) * inner}
        y1={cy + Math.sin(angle) * inner}
        x2={cx + Math.cos(angle) * (r - 1)}
        y2={cy + Math.sin(angle) * (r - 1)}
        stroke={on ? ember : theme.palette.divider}
        strokeWidth={on ? 2 : 1}
      />,
    )
  }
  return (
    <g data-testid={`clock-${clock.id}`} opacity={clock.enabled ? 1 : 0.45}>
      <title>
        {clock.hoursKnown
          ? `${clock.cron} — wakes ${clock.worker} at ${clock.hours.map((h) => `${String(h).padStart(2, '0')}:00`).join(', ')}`
          : `${clock.cron} — wakes ${clock.worker}; this cron's hours are not read here, so no hours are ticked`}
      </title>
      <circle cx={cx} cy={cy} r={r} fill="none" stroke={theme.palette.divider} strokeWidth={1} />
      {clock.hoursKnown ? ticks : (
        <text x={cx} y={cy + 4} textAnchor="middle" fontFamily={MONO} fontSize={11} fill={theme.palette.text.secondary}>
          ?
        </text>
      )}
    </g>
  )
}

/** An entry pip: an event type that enters the project from outside it. */
function Pip({
  pip,
  theme,
  selected,
  onSelect,
}: {
  pip: OrgChartPip
  theme: Theme
  selected: boolean
  onSelect: () => void
}) {
  const ember = token(theme, 'ember')
  const colour = selected ? ember : theme.palette.text.primary
  const bottom = pip.y + ORG_CHART_METRICS.pipHeight
  return (
    <g
      data-testid={`pip-${pip.type}`}
      role="button"
      tabIndex={0}
      aria-label={`trace ${pip.type}`}
      onClick={onSelect}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          onSelect()
        }
      }}
      style={{ cursor: 'pointer' }}
    >
      <title>
        {pip.external
          ? `${pip.type} — arrives from outside the project; seen in recent events`
          : `${pip.type} — arrives from outside the project; nothing in this project produces it, and none has been seen recently`}
      </title>
      <text
        x={pip.x}
        y={pip.y + 8}
        textAnchor="middle"
        fontFamily={MONO}
        fontSize={11}
        fill={colour}
        opacity={pip.external ? 1 : 0.6}
      >
        {pip.type}
      </text>
      <path
        d={`M${pip.x - 4} ${bottom - 6} L${pip.x + 4} ${bottom - 6} L${pip.x} ${bottom} z`}
        fill={colour}
        opacity={pip.external ? 1 : 0.6}
      />
    </g>
  )
}

// ---------------------------------------------------------------------------
// The propagation panel (§6.5)
// ---------------------------------------------------------------------------

function Propagation({
  pips,
  tracedLabel,
  propagation,
  draft,
  draftError,
  onDraftChange,
  onTraceDraft,
  onTracePip,
  theme,
}: {
  pips: OrgChartPip[]
  tracedLabel: string | null
  propagation: ReturnType<typeof propagateEvent> | null
  draft: string
  draftError: string | null
  onDraftChange: (text: string) => void
  onTraceDraft: () => void
  onTracePip: (pip: OrgChartPip) => void
  theme: Theme
}) {
  const ember = token(theme, 'ember')
  const fault = token(theme, 'fault')
  const byDepth = new Map((propagation?.hops ?? []).map((hop) => [hop.depth, hop]))
  const ended = propagation === null ? -1 : propagation.hops[propagation.hops.length - 1].depth

  return (
    <Box component="section" aria-label="What fires when this event arrives?" sx={{ mt: 4, maxWidth: 900 }}>
      <Typography variant="subtitle2" sx={{ textTransform: 'uppercase', letterSpacing: '0.08em', mb: 1 }}>
        What fires when this event arrives?
      </Typography>

      <Stack direction="row" spacing={1} flexWrap="wrap" useFlexGap sx={{ mb: 2 }}>
        {pips.length === 0 && (
          <Typography variant="body2" color="text.secondary">
            No event enters this project from outside it yet — paste one below to trace it.
          </Typography>
        )}
        {pips.map((pip) => (
          <Chip
            key={pip.type}
            label={pip.type}
            size="small"
            variant={tracedLabel === pip.type ? 'filled' : 'outlined'}
            onClick={() => onTracePip(pip)}
            sx={{ fontFamily: MONO, borderRadius: 0 }}
          />
        ))}
      </Stack>

      <Stack direction={{ xs: 'column', md: 'row' }} spacing={2} alignItems="flex-start">
        <Box sx={{ flex: 1, minWidth: 0, width: '100%' }}>
          <TextField
            label="…or paste an event"
            placeholder={EVENT_DRAFT_TEMPLATE}
            value={draft}
            onChange={(e) => onDraftChange(e.target.value)}
            multiline
            minRows={4}
            fullWidth
            size="small"
            slotProps={{ htmlInput: { style: { fontFamily: MONO, fontSize: 12 } } }}
          />
          <Button size="small" onClick={onTraceDraft} sx={{ mt: 1 }}>
            Trace this event
          </Button>
          {draftError !== null && (
            <Typography
              variant="caption"
              data-testid="paste-error"
              sx={{ display: 'block', mt: 1, color: fault }}
            >
              {draftError}
            </Typography>
          )}
        </Box>

        <Box sx={{ flex: 1, minWidth: 0, width: '100%' }} data-testid="depth-ruler">
          <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mb: 0.5 }}>
            depth
          </Typography>
          {propagation === null ? (
            <Typography variant="body2" color="text.secondary">
              Pick an entry pip, or paste an event, and this traces it hop by hop the way the router
              would.
            </Typography>
          ) : (
            <Box sx={{ borderLeft: 1, borderColor: 'divider', pl: 1.5 }}>
              {Array.from({ length: PROPAGATION_MAX_DEPTH + 1 }, (_, depth) => {
                const hop = byDepth.get(depth)
                const beyond = depth > ended
                return (
                  <Stack
                    key={depth}
                    direction="row"
                    spacing={1.5}
                    sx={{
                      py: 0.25,
                      borderTop: depth === PROPAGATION_MAX_DEPTH ? 2 : 0,
                      borderColor: fault,
                    }}
                  >
                    <Typography variant="body2" sx={{ fontFamily: MONO, color: 'text.disabled', width: 16 }}>
                      {depth}
                    </Typography>
                    <Box sx={{ flex: 1, minWidth: 0, pl: `${Math.min(depth, 6) * 12}px` }}>
                      {hop === undefined || beyond ? (
                        <Typography variant="body2" color="text.disabled">
                          {depth === PROPAGATION_MAX_DEPTH ? PROPAGATION_STOP_LINE : '·'}
                        </Typography>
                      ) : hop.wakes.length === 0 ? (
                        <Typography variant="body2" color="text.disabled">
                          {PROPAGATION_NOTHING_SUBSCRIBES}
                        </Typography>
                      ) : (
                        hop.wakes.map((wake) => (
                          <Typography
                            key={`${wake.subscriptionId}:${wake.from}:${wake.worker}`}
                            variant="body2"
                            sx={{ fontFamily: MONO, color: ember }}
                          >
                            {`${wake.eventType} ▸ ${wake.worker}`}
                          </Typography>
                        ))
                      )}
                      {depth === PROPAGATION_MAX_DEPTH && hop !== undefined && !beyond && (
                        <Typography variant="caption" sx={{ display: 'block', color: fault }}>
                          {PROPAGATION_STOP_LINE}
                        </Typography>
                      )}
                    </Box>
                  </Stack>
                )
              })}
            </Box>
          )}
          {propagation?.stopped === true && (
            <Alert severity="warning" sx={{ mt: 1 }}>
              This chain was still going at depth {PROPAGATION_MAX_DEPTH} — {PROPAGATION_STOP_LINE}.
              A chain that runs into the stop line is usually a loop.
            </Alert>
          )}
        </Box>
      </Stack>

      <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mt: 2 }}>
        {PROPAGATION_CAVEAT}
      </Typography>
    </Box>
  )
}
