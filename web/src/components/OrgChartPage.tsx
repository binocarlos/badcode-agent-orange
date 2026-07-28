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
// The conventions overlay (OC3, §6.6, decision K4) is OFF by default, labelled,
// and every dashed edge quotes the prompt line it was read from.
//
// Direct manipulation (OC4, §6.4, decision K3) is **wire + freeze only**, and
// every gesture is a PROPOSAL, never a mutation:
//
//   - drag A → B, or a node's menu, opens the proposal card: the sentence
//     first, the exact `event_type`/`filter` in mono beneath it, and a
//     MANDATORY "Why are you wiring this?" — submitted through
//     `useSubscriptions.save` with E3's rationale, the same route the form
//     uses. There is no canvas-only endpoint and no optimistic local graph.
//   - clicking a wire offers "Stop waking <worker> when <event>". Cutting is
//     never called undo (§6.4 rule 3, §11 rule 2): it is a forward write and
//     the button says what it does.
//   - a node's menu toggles enabled and frozen through `useWorkers.save`, which
//     carries ALL fields including `frozen` — an omitted `frozen` on a
//     create-or-replace PUT is a silent thaw.
//   - schedules are NOT editable here (K3). A clock is a deep link to
//     Automation, where that row lives.
//
// Every one of those is reachable from the keyboard: plates, wires and clocks
// are focusable, Enter/Space acts, and a wire can be proposed from a node's
// menu without ever dragging.
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
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  FormControlLabel,
  Menu,
  MenuItem,
  Paper,
  Stack,
  Switch,
  TextField,
  Typography,
  useTheme,
  type Theme,
} from '@mui/material'
import type { ConfigApiOptions } from '../configApi.js'
import useEventsOverview from '../useEvents.js'
import useSchedules from '../useSchedules.js'
import useSubscriptions from '../useSubscriptions.js'
import useWorkers from '../useWorkers.js'
import { newSubscriptionDraft } from '../subscriptions.js'
import { FROZEN_SENTENCE, type Worker } from '../workers.js'
import {
  EVENT_DRAFT_TEMPLATE,
  blankEnvelope,
  parseEventDraft,
  type MatchableEvent,
} from '../events.js'
import {
  CONVENTION_CAVEAT,
  ORG_CHART_METRICS,
  assignLabelLanes,
  labelWidth,
  PROPAGATION_CAVEAT,
  PROPAGATION_MAX_DEPTH,
  PROPAGATION_NOTHING_SUBSCRIBES,
  PROPAGATION_STOP_LINE,
  inferConventions,
  layoutOrgChart,
  propagateEvent,
  type OrgChartClock,
  type OrgChartConvention,
  type OrgChartLayout,
  type OrgChartNode,
  type OrgChartPip,
  type OrgChartWire,
} from '../orgchart.js'
import { consoleColor } from '../spine.js'

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
  /**
   * Deep link to the Automation page (K3: schedules are edited on their row,
   * never on the canvas). Given a handler, a clock becomes a link to the
   * schedule it draws; without one it stays a dial and says where to go.
   */
  onOpenAutomation?: (scheduleId: string, worker: string) => void
}

export default function OrgChartPage({
  projectId,
  limit,
  nowSeconds,
  title = 'Org chart',
  onOpenAutomation,
  ...apiOptions
}: OrgChartPageProps) {
  const theme = useTheme()
  const {
    workers,
    loading: workersLoading,
    error: workersError,
    save: saveWorker,
  } = useWorkers(apiOptions)
  const overview = useEventsOverview({ ...apiOptions, limit, nowSeconds })
  const { schedules, error: schedulesError } = useSchedules(apiOptions)
  // The canvas is a SECOND FRONT END onto the ordinary routes (§6.4 rule 1) —
  // the same hook the subscription form uses, no canvas-only path.
  const subscriptionsApi = useSubscriptions(apiOptions)

  const layout = useMemo(
    () => layoutOrgChart(workers, overview.subscriptions, schedules, overview.events),
    [overview.events, overview.subscriptions, schedules, workers],
  )

  // The conventions overlay (§6.6, K4): inferred from the prompts, OFF until
  // the operator asks for it, and never confused with a wire.
  const [showConventions, setShowConventions] = useState(false)
  const conventions = useMemo(() => inferConventions(workers), [workers])

  // `● running n/max`: live, from the deliveries the events view already reads.
  const running = useMemo(() => {
    const counts = new Map<string, number>()
    for (const job of overview.jobs) {
      if (job.status !== 'running' || job.worker === '') continue
      counts.set(job.worker, (counts.get(job.worker) ?? 0) + 1)
    }
    return counts
  }, [overview.jobs])

  // A parked ask is a STATE of the worker, not only a row on the Desk (§2 X9):
  // a plate holding an unanswered `request_human_attention` must not read
  // `idle`. Same deliveries, same join, no second fetch.
  const awaiting = useMemo(() => {
    const counts = new Map<string, number>()
    for (const job of overview.jobs) {
      if (job.status !== 'awaiting_human' || job.worker === '') continue
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

  // --- Direct manipulation (§6.4, K3) -------------------------------------
  // Three proposals, one at a time. Nothing here mutates until the operator
  // has written a reason and pressed the button that names the write.
  const [proposal, setProposal] = useState<WireProposal | null>(null)
  const [cut, setCut] = useState<OrgChartWire | null>(null)
  const [toggle, setToggle] = useState<{ worker: Worker; field: 'enabled' | 'frozen' } | null>(null)
  const [busy, setBusy] = useState(false)

  const workerByName = useMemo(() => new Map(workers.map((w) => [w.name, w])), [workers])

  const wireItUp = useCallback(
    async (target: string, rationale: string) => {
      if (proposal === null) return
      setBusy(true)
      const saved = await subscriptionsApi.save(
        {
          ...newSubscriptionDraft(projectId ?? ''),
          event_type: WIRE_EVENT_TYPE,
          filter: wireFilter(proposal.from),
          worker: target,
        },
        rationale,
      )
      setBusy(false)
      if (saved === null) return
      setProposal(null)
      void overview.reload()
    },
    [overview, projectId, proposal, subscriptionsApi],
  )

  const stopWaking = useCallback(
    async (rationale: string) => {
      if (cut === null) return
      setBusy(true)
      const gone = await subscriptionsApi.remove(cut.subscriptionId, rationale)
      setBusy(false)
      if (!gone) return
      setCut(null)
      void overview.reload()
    },
    [cut, overview, subscriptionsApi],
  )

  const flipWorker = useCallback(
    async (rationale: string) => {
      if (toggle === null) return
      setBusy(true)
      // Read-modify-write the WHOLE row: PUT is create-or-replace, and a body
      // that dropped `frozen` would thaw a frozen worker on its way past.
      const saved = await saveWorker(
        { ...toggle.worker, [toggle.field]: !toggle.worker[toggle.field] },
        rationale,
      )
      setBusy(false)
      if (saved === null) return
      setToggle(null)
    },
    [saveWorker, toggle],
  )

  const openNodeToggle = useCallback(
    (name: string, field: 'enabled' | 'frozen') => {
      const worker = workerByName.get(name)
      if (worker === undefined) return
      setToggle({ worker, field })
    },
    [workerByName],
  )

  const error = workersError ?? overview.error ?? schedulesError ?? subscriptionsApi.error

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
          awaiting={awaiting}
          litWires={litSubscriptions}
          litWorkers={litWorkers}
          tracedPip={traced?.label ?? null}
          onTracePip={tracePip}
          conventions={conventions}
          showConventions={showConventions}
          onShowConventions={setShowConventions}
          onPropose={setProposal}
          onCutWire={setCut}
          onToggleWorker={openNodeToggle}
          onOpenAutomation={onOpenAutomation}
        />
      )}

      {/* The proposal card (§6.4): a sentence, the exact fields, a mandatory
          reason — and only then a write, through the ordinary route. */}
      {proposal !== null && (
        <WireProposalCard
          proposal={proposal}
          workers={workers.map((w) => w.name)}
          busy={busy}
          onCancel={() => setProposal(null)}
          onSubmit={wireItUp}
        />
      )}
      {cut !== null && (
        <ReasonDialog
          testId="cut-wire-dialog"
          title={cutWireTitle(cut)}
          sentence={`This writes a new record: the subscription stops existing, and the config log keeps what it was. ${cut.to} keeps every other way it is woken.`}
          reasonLabel="Why are you stopping this?"
          confirmLabel={`Stop waking ${cut.to}`}
          busy={busy}
          onCancel={() => setCut(null)}
          onConfirm={stopWaking}
        />
      )}
      {toggle !== null && (
        <ReasonDialog
          testId="worker-toggle-dialog"
          title={toggleTitle(toggle.worker, toggle.field)}
          sentence={toggleSentence(toggle.worker, toggle.field)}
          reasonLabel="Why?"
          confirmLabel={toggleTitle(toggle.worker, toggle.field)}
          busy={busy}
          onCancel={() => setToggle(null)}
          onConfirm={flipWorker}
        />
      )}

      {showConventions && (
        <Typography
          variant="caption"
          color="text.secondary"
          data-testid="conventions-caveat"
          sx={{ display: 'block', mt: 1 }}
        >
          {conventions.length === 0
            ? `No prompt in this project names another worker. Dashed edges would be ${CONVENTION_CAVEAT}.`
            : `${conventions.length} dashed edge(s) read out of the prompts — ${CONVENTION_CAVEAT}. Each one quotes the line it came from.`}
        </Typography>
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
  awaiting: Map<string, number>
  litWires: Set<string>
  litWorkers: Set<string>
  tracedPip: string | null
  onTracePip: (pip: OrgChartPip) => void
  conventions: OrgChartConvention[]
  showConventions: boolean
  onShowConventions: (show: boolean) => void
  onPropose: (proposal: WireProposal) => void
  onCutWire: (wire: OrgChartWire) => void
  onToggleWorker: (name: string, field: 'enabled' | 'frozen') => void
  onOpenAutomation?: (scheduleId: string, worker: string) => void
}

const ZOOM_STEP = 1.25
const ZOOM_MIN = 0.4
const ZOOM_MAX = 3

function Canvas({
  layout,
  theme,
  running,
  awaiting,
  litWires,
  litWorkers,
  tracedPip,
  onTracePip,
  conventions,
  showConventions,
  onShowConventions,
  onPropose,
  onCutWire,
  onToggleWorker,
  onOpenAutomation,
}: CanvasProps) {
  // §6.3: pan and zoom are the ONLY view state this screen has, and it dies
  // with the component. No node position is stored, and no node is draggable —
  // dragging a node's plate starts a WIRE, never a move (§6.4's "what direct
  // manipulation must not grow into").
  const [view, setView] = useState({ scale: 1, x: 0, y: 0 })
  const drag = useRef<{ x: number; y: number; ox: number; oy: number } | null>(null)

  // The in-flight wire gesture: which plate it started on, and which plate the
  // pointer is over now. Both die on pointer-up; neither is a mutation.
  const [wiringFrom, setWiringFrom] = useState<string | null>(null)
  const [over, setOver] = useState<string | null>(null)
  // Which plate's menu is open — the keyboard path to every gesture.
  const [menuFor, setMenuFor] = useState<{ node: OrgChartNode; anchor: DOMRect } | null>(null)

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
    setWiringFrom(null)
    setOver(null)
  }

  const startWiring = (node: OrgChartNode) => {
    drag.current = null
    setWiringFrom(node.name)
    setOver(null)
  }
  /** Pointer-up on a plate: a completed drag A → B is a PROPOSAL, not a write. */
  const dropWiring = (node: OrgChartNode) => {
    if (wiringFrom !== null && wiringFrom !== node.name) {
      onPropose({ from: wiringFrom, to: node.name })
    }
    setWiringFrom(null)
    setOver(null)
  }
  const zoom = (factor: number) =>
    setView((v) => ({ ...v, scale: Math.min(ZOOM_MAX, Math.max(ZOOM_MIN, v.scale * factor)) }))

  const hairline = theme.palette.divider
  const ember = token(theme, 'ember')

  // Conventions get a lane of their OWN, one line below the dashed edge they
  // belong to (§2 X2): the overlay must never fight the wires' riding labels,
  // and two conventions crossing the same patch of canvas must not fight each
  // other. Same pure lane assignment the layout uses for the wires.
  const conventionLabels = useMemo(() => {
    const nodes = new Map(layout.nodes.map((n) => [n.name, n]))
    const placed = conventions.flatMap((convention) => {
      const from = nodes.get(convention.from)
      const to = nodes.get(convention.to)
      if (from === undefined || to === undefined) return []
      const start = flank(from, to)
      const end = flank(to, from)
      return [
        {
          convention,
          text: convention.kind === 'route-to' ? 'ROUTE-TO ⇢' : 'names ⇢',
          x: Math.round((start.x + end.x) / 2),
          y: Math.round((start.y + end.y) / 2),
        },
      ]
    })
    const lanes = assignLabelLanes(
      placed.map((label) => ({
        x: label.x,
        y: label.y,
        width: labelWidth(label.text, CONVENTION_LABEL_CHAR_WIDTH),
      })),
    )
    return placed.map((label, index) => ({
      ...label,
      y: label.y + (1 + lanes[index]) * ORG_CHART_METRICS.labelLineHeight,
    }))
  }, [conventions, layout.nodes])

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
        <Box sx={{ flex: 1 }} />
        {/* §6.6 / K4: opt-in, off by default, and labelled in the operator's
            own words. It is a heuristic and it announces itself. */}
        <FormControlLabel
          control={
            <Switch
              size="small"
              checked={showConventions}
              onChange={(e) => onShowConventions(e.target.checked)}
            />
          }
          label={<Typography variant="caption">Show conventions</Typography>}
        />
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
              <Wire
                key={wire.id}
                wire={wire}
                theme={theme}
                lit={litWires.has(wire.subscriptionId)}
                onCut={() => onCutWire(wire)}
              />
            ))}
            {showConventions &&
              conventions.map((convention) => (
                <ConventionEdge
                  key={convention.id}
                  convention={convention}
                  theme={theme}
                  from={layout.nodes.find((n) => n.name === convention.from)}
                  to={layout.nodes.find((n) => n.name === convention.to)}
                />
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
              <Clock
                key={clock.id}
                clock={clock}
                theme={theme}
                onOpenAutomation={
                  onOpenAutomation === undefined
                    ? undefined
                    : () => onOpenAutomation(clock.id, clock.worker)
                }
              />
            ))}
            {/* The gesture in flight: a dashed ghost from the plate the drag
                started on to the plate under the pointer. Nothing is written
                until the proposal card is answered. */}
            {wiringFrom !== null && over !== null && over !== wiringFrom && (
              <GhostWire
                from={layout.nodes.find((n) => n.name === wiringFrom)}
                to={layout.nodes.find((n) => n.name === over)}
                theme={theme}
              />
            )}
            {layout.nodes.map((node) => (
              <Plate
                key={node.name}
                node={node}
                theme={theme}
                running={running.get(node.name) ?? 0}
                awaiting={awaiting.get(node.name) ?? 0}
                lit={litWorkers.has(node.name)}
                wiring={wiringFrom !== null}
                isWiringSource={wiringFrom === node.name}
                onStartWiring={() => startWiring(node)}
                onEnter={() => setOver(node.name)}
                onDrop={() => dropWiring(node)}
                onOpenMenu={(anchor) => setMenuFor({ node, anchor })}
              />
            ))}
            {/* The label layer, drawn LAST and on top of everything (§2 X2).
                Wires and conventions run under the plates, and a label painted
                with them disappears under the plate it points at; the lanes
                come from the layout, so what the tests pin is what is drawn. */}
            <g data-testid="org-chart-labels">
              {layout.wires.map((wire) => (
                <WireLabel key={wire.id} wire={wire} theme={theme} lit={litWires.has(wire.subscriptionId)} />
              ))}
              {showConventions &&
                conventionLabels.map((label) => (
                  <ConventionLabel key={label.convention.id} {...label} theme={theme} />
                ))}
            </g>
          </g>
        </Box>
      </Box>

      {/* The keyboard (and right-hand) path to every canvas gesture (§6.4): a
          wire can always be proposed from here, without a drag. */}
      <Menu
        open={menuFor !== null}
        onClose={() => setMenuFor(null)}
        anchorReference="anchorPosition"
        anchorPosition={
          menuFor === null
            ? undefined
            : { top: menuFor.anchor.bottom || 0, left: menuFor.anchor.left || 0 }
        }
        MenuListProps={{ 'aria-label': `${menuFor?.node.name ?? ''} actions` }}
      >
        <MenuItem
          onClick={() => {
            if (menuFor !== null) onPropose({ from: menuFor.node.name, to: '' })
            setMenuFor(null)
          }}
        >
          {`Wire ${menuFor?.node.name ?? ''} to another worker…`}
        </MenuItem>
        <MenuItem
          onClick={() => {
            if (menuFor !== null) onToggleWorker(menuFor.node.name, 'enabled')
            setMenuFor(null)
          }}
        >
          {`${menuFor?.node.enabled === false ? 'Enable' : 'Disable'} ${menuFor?.node.name ?? ''}`}
        </MenuItem>
        <MenuItem
          onClick={() => {
            if (menuFor !== null) onToggleWorker(menuFor.node.name, 'frozen')
            setMenuFor(null)
          }}
        >
          {`${menuFor?.node.frozen === true ? 'Unfreeze' : 'Freeze'} ${menuFor?.node.name ?? ''}`}
        </MenuItem>
      </Menu>
    </Box>
  )
}

/** The drag in flight, drawn as a dashed ghost. Never a wire until it is one. */
function GhostWire({
  from,
  to,
  theme,
}: {
  from: OrgChartNode | undefined
  to: OrgChartNode | undefined
  theme: Theme
}) {
  if (from === undefined || to === undefined) return null
  const start = flank(from, to)
  const end = flank(to, from)
  return (
    <line
      data-testid="ghost-wire"
      x1={start.x}
      y1={start.y}
      x2={end.x}
      y2={end.y}
      stroke={token(theme, 'ember')}
      strokeWidth={2}
      strokeDasharray="3 3"
    />
  )
}

/** A subscription: an orthogonal hairline with its event type riding it.
 *  Clicking (or Enter/Space on) one offers to stop it — a forward write. */
function Wire({
  wire,
  theme,
  lit,
  onCut,
}: {
  wire: OrgChartWire
  theme: Theme
  lit: boolean
  onCut: () => void
}) {
  const ember = token(theme, 'ember')
  const stroke = lit ? ember : theme.palette.divider
  const points = wire.points.map((p) => `${p.x},${p.y}`).join(' ')
  return (
    <g
      data-testid={`wire-${wire.id}`}
      opacity={wire.enabled ? 1 : 0.4}
      role="button"
      tabIndex={0}
      aria-label={`${cutWireTitle(wire)}…`}
      style={{ cursor: 'pointer' }}
      onClick={onCut}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          onCut()
        }
      }}
    >
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
      {/* A hairline is a hard thing to hit; this is the same route, fat and
          invisible, so the pointer target matches the drawing. */}
      <polyline points={points} fill="none" stroke="transparent" strokeWidth={12} />
    </g>
  )
}

/** The text that rides a wire, drawn in the label layer on top of everything.
 *  Its lane comes from the layout (§2 X2); this only draws where it is told.
 *  Pointer-transparent, so the wire underneath stays the click target. */
function WireLabel({ wire, theme, lit }: { wire: OrgChartWire; theme: Theme; lit: boolean }) {
  const ember = token(theme, 'ember')
  return (
    <text
      data-testid={`label-${wire.id}`}
      x={wire.labelX}
      y={wire.labelY - 4}
      textAnchor="middle"
      fontFamily={MONO}
      fontSize={10}
      opacity={wire.enabled ? 1 : 0.4}
      fill={lit ? ember : theme.palette.text.secondary}
      stroke={theme.palette.background.paper}
      strokeWidth={3}
      paintOrder="stroke"
      style={{ pointerEvents: 'none' }}
    >
      {wire.back ? `${wire.label} ↺` : wire.label}
    </text>
  )
}

/**
 * A convention: a DASHED grey edge, never a wire (§6.6, K4).
 *
 * It is drawn straight, flank to flank, so it cannot be mistaken for one of the
 * orthogonal routes the router will actually walk, and its tooltip quotes the
 * prompt line it was read from plus the caveat, verbatim.
 */
function ConventionEdge({
  convention,
  theme,
  from,
  to,
}: {
  convention: OrgChartConvention
  theme: Theme
  from: OrgChartNode | undefined
  to: OrgChartNode | undefined
}) {
  if (from === undefined || to === undefined) return null
  const start = flank(from, to)
  const end = flank(to, from)
  return (
    <g data-testid={`convention-${convention.id}`}>
      <title>{`"${convention.line}" — ${CONVENTION_CAVEAT}`}</title>
      <line
        x1={start.x}
        y1={start.y}
        x2={end.x}
        y2={end.y}
        stroke={theme.palette.text.secondary}
        strokeWidth={1}
        strokeDasharray="4 4"
        opacity={0.7}
        markerEnd="url(#org-chart-arrow)"
      />
    </g>
  )
}

/** A convention's caption. Its own lane, and its own layer above the plates —
 *  drawn with the dashed edge it belongs to, it slid under a plate (§2 X2). */
const CONVENTION_LABEL_CHAR_WIDTH = 5.5

function ConventionLabel({
  convention,
  text,
  x,
  y,
  theme,
}: {
  convention: OrgChartConvention
  text: string
  x: number
  y: number
  theme: Theme
}) {
  return (
    <text
      data-testid={`label-convention-${convention.id}`}
      x={x}
      y={y}
      textAnchor="middle"
      fontFamily={MONO}
      fontSize={9}
      fill={theme.palette.text.secondary}
      stroke={theme.palette.background.paper}
      strokeWidth={3}
      paintOrder="stroke"
      style={{ pointerEvents: 'none' }}
    >
      {text}
    </text>
  )
}

/** The point on `node`'s edge facing `towards` — the side, when they are mostly
 *  side by side; the top or bottom otherwise. */
function flank(node: OrgChartNode, towards: OrgChartNode): { x: number; y: number } {
  const cx = node.x + node.width / 2
  const cy = node.y + node.height / 2
  const dx = towards.x + towards.width / 2 - cx
  const dy = towards.y + towards.height / 2 - cy
  if (Math.abs(dx) * node.height >= Math.abs(dy) * node.width) {
    return { x: dx >= 0 ? node.x + node.width : node.x, y: cy }
  }
  return { x: cx, y: dy >= 0 ? node.y + node.height : node.y }
}

/**
 * A worker: a name plate with a 1px rule, never a card (§3.7).
 *
 * Pointer-down on a plate starts a WIRE (the plate itself never moves — §6.3),
 * pointer-up on another plate proposes it; Enter, Space or a click opens the
 * node menu, which offers the same wire plus the enabled/frozen toggles. The
 * plate is focusable, so none of that needs a pointer at all.
 */
function Plate({
  node,
  theme,
  running,
  awaiting,
  lit,
  wiring,
  isWiringSource,
  onStartWiring,
  onEnter,
  onDrop,
  onOpenMenu,
}: {
  node: OrgChartNode
  theme: Theme
  running: number
  awaiting: number
  lit: boolean
  wiring: boolean
  isWiringSource: boolean
  onStartWiring: () => void
  onEnter: () => void
  onDrop: () => void
  onOpenMenu: (anchor: DOMRect) => void
}) {
  const steel = token(theme, 'steel')
  const ember = token(theme, 'ember')
  const rose = token(theme, 'rose')
  const rule = node.frozen ? steel : lit || isWiringSource ? ember : theme.palette.text.primary
  const openMenu = (target: Element | null) =>
    onOpenMenu(
      target?.getBoundingClientRect?.() ?? ({ top: 0, left: 0, bottom: 0 } as unknown as DOMRect),
    )
  return (
    <g
      data-testid={`node-${node.name}`}
      opacity={node.enabled ? 1 : 0.55}
      role="button"
      tabIndex={0}
      aria-label={`${node.name} — open actions, or drag to another worker to wire them`}
      style={{ cursor: wiring ? 'crosshair' : 'pointer' }}
      onPointerDown={(e) => {
        e.stopPropagation()
        onStartWiring()
      }}
      onPointerEnter={onEnter}
      onPointerUp={(e) => {
        e.stopPropagation()
        onDrop()
      }}
      onClick={(e) => {
        if (isWiringSource || !wiring) openMenu(e.currentTarget)
      }}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          openMenu(e.currentTarget)
        }
      }}
    >
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
      {/* X1: the lock is drawn HERE, in SVG user units. `SpineGlyph` is a
          `Box component="svg"` sized by CSS, and CSS sizing does not apply to
          an <svg> nested inside an <svg> — it rendered at the 300×150 default,
          a giant clipped disc in the corner of the canvas. Same shape as
          spine.tsx's freeze glyph, scaled by hand onto this grid. */}
      {node.frozen && (
        <g
          role="img"
          aria-label="frozen — only a human may change it"
          data-glyph="freeze"
          transform={`translate(${node.x + node.width - 26} ${node.y + 10}) scale(${LOCK_SIZE / 12})`}
        >
          <title>frozen — only a human may change it</title>
          <path d="M4 5.2V3.9a2 2 0 0 1 4 0v1.3" fill="none" stroke={steel} strokeWidth={1.3} />
          <rect x={2.8} y={5.2} width={6.4} height={5} rx={0.8} fill={steel} />
        </g>
      )}
      {/* X9: a worker holding an unanswered ask wears the rose diamond, the
          same mark the Desk uses. A parked ask is a state, not a Desk row. */}
      {awaiting > 0 && (
        <g
          role="img"
          aria-label={`${node.name} is waiting for a human`}
          data-glyph="attention"
          transform={`translate(${node.x + node.width - (node.frozen ? 46 : 26)} ${node.y + 10}) scale(${LOCK_SIZE / 12})`}
        >
          <title>{`${node.name} is parked at awaiting_human — it is waiting for a person, and nothing else will move it`}</title>
          <path d="M6 1.5 10.5 6 6 10.5 1.5 6Z" fill={rose} />
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
        fill={awaiting > 0 ? rose : running > 0 ? ember : theme.palette.text.secondary}
      >
        {stateLine(node, running, awaiting)}
      </text>
    </g>
  )
}

/** The size of a plate's corner glyphs, in SVG user units — NOT CSS px, which
 *  is the whole of X1: a nested `<svg>` ignores CSS width and height. */
const LOCK_SIZE = 14

/**
 * The state line (§6.1): `● running n/max` · `idle` · `disabled` · `frozen`,
 * and `◆ awaiting human n` when the worker is holding an unanswered ask (X9).
 *
 * A parked ask outranks everything but `disabled`, and it is additive with
 * `running`, because both are true and the operator needs both: one instance
 * is waiting for a person while another still works.
 */
export function stateLine(
  node: Pick<OrgChartNode, 'enabled' | 'frozen' | 'maxInstances'>,
  running: number,
  awaiting = 0,
): string {
  if (!node.enabled) return 'disabled'
  const parked = awaiting > 0 ? `◆ awaiting human ${awaiting}` : ''
  if (running > 0) {
    const live = `● running ${running}/${node.maxInstances}`
    return parked === '' ? live : `${live} · ${parked}`
  }
  if (parked !== '') return parked
  if (node.frozen) return `frozen 0/${node.maxInstances}`
  return `idle 0/${node.maxInstances}`
}

function truncate(text: string, max: number): string {
  return text.length <= max ? text : `${text.slice(0, max - 1)}…`
}

/** What a dead clock says, in the operator's words (§2 X10, §11 rule 1). */
export const HALTED_CLOCK_SENTENCE =
  'this clock is dead: five failed provisions in a row disabled it, and it will not fire again until someone re-enables it'

/** A schedule: a 24-hour dial docked left of its plate, firing hours ticked.
 *  NOT editable here (K3) — it is a deep link to the row on Automation. */
function Clock({
  clock,
  theme,
  onOpenAutomation,
}: {
  clock: OrgChartClock
  theme: Theme
  onOpenAutomation?: () => void
}) {
  const ember = token(theme, 'ember')
  const fault = token(theme, 'fault')
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
    <g
      data-testid={`clock-${clock.id}`}
      opacity={clock.enabled ? 1 : 0.45}
      role={onOpenAutomation === undefined ? undefined : 'button'}
      tabIndex={onOpenAutomation === undefined ? undefined : 0}
      aria-label={
        onOpenAutomation === undefined
          ? undefined
          : `open ${clock.worker}'s schedule on the Automation page`
      }
      style={onOpenAutomation === undefined ? undefined : { cursor: 'pointer' }}
      onPointerDown={onOpenAutomation === undefined ? undefined : (e) => e.stopPropagation()}
      onClick={onOpenAutomation}
      onKeyDown={
        onOpenAutomation === undefined
          ? undefined
          : (e) => {
              if (e.key === 'Enter' || e.key === ' ') {
                e.preventDefault()
                onOpenAutomation()
              }
            }
      }
    >
      <title>
        {clock.hoursKnown
          ? `${clock.cron} — wakes ${clock.worker} at ${clock.hours.map((h) => `${String(h).padStart(2, '0')}:00`).join(', ')}`
          : `${clock.cron} — wakes ${clock.worker}; this cron's hours are not read here, so no hours are ticked`}
        {clock.halted ? ` — ${HALTED_CLOCK_SENTENCE}` : ''}
        {onOpenAutomation === undefined
          ? ' — schedules are edited on the Automation page'
          : ' — open it on the Automation page'}
      </title>
      <circle
        cx={cx}
        cy={cy}
        r={r}
        fill="none"
        stroke={clock.halted ? fault : theme.palette.divider}
        strokeWidth={1}
      />
      {/* X10: five failed provisions in a row and the engine stops the clock.
          A dead clock drawn as an ordinary dial is the chart lying. */}
      {clock.halted && (
        <g role="img" aria-label={`${clock.worker}'s clock is dead`} data-glyph="failure">
          <path
            d={`M${cx - r * 0.55} ${cy - r * 0.55}L${cx + r * 0.55} ${cy + r * 0.55}M${cx + r * 0.55} ${cy - r * 0.55}L${cx - r * 0.55} ${cy + r * 0.55}`}
            stroke={fault}
            strokeWidth={2}
            strokeLinecap="round"
            fill="none"
          />
        </g>
      )}
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
      // A pip is small, and the canvas underneath it starts a pan on
      // pointer-down; the pip's own click must survive that (§8 W1: pips are
      // clickable and run the propagation trace).
      onPointerDown={(e) => e.stopPropagation()}
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
      {/* X4: a wired pip's caption was the SAME TEXT as the riding label on the
          wire leaving it, 60px apart. The wire's label is the one that names
          the event where the event travels, so it wins; an unwired pip (an
          external event nothing subscribes to) keeps its caption, because
          nothing else on the canvas would name it. */}
      {!pip.wired && (
        <text
          data-testid={`pip-caption-${pip.type}`}
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
      )}
      {/* A triangle is a hard thing to hit; the hit area is not. */}
      <rect
        x={pip.x - 14}
        y={pip.y}
        width={28}
        height={ORG_CHART_METRICS.pipHeight}
        fill="transparent"
      />
      <path
        d={`M${pip.x - 4} ${bottom - 6} L${pip.x + 4} ${bottom - 6} L${pip.x} ${bottom} z`}
        fill={colour}
        opacity={pip.external ? 1 : 0.6}
      />
    </g>
  )
}

// ---------------------------------------------------------------------------
// Direct manipulation (§6.4, decision K3)
// ---------------------------------------------------------------------------

/** A wire the operator has gestured at but not yet written. `to` is '' when the
 *  gesture started from a node's menu and the target is still to be chosen. */
export interface WireProposal {
  from: string
  to: string
}

/** The only event a canvas wire is ever built on: one worker finishing wakes
 *  another. Anything else — an external type, a `worker.failed` rescue path —
 *  is a subscription with more thought in it than a drag, and belongs on the
 *  Automation page's form. */
export const WIRE_EVENT_TYPE = 'worker.finished'

/**
 * The filter a canvas wire carries: the SOURCE worker, always.
 *
 * This is not decoration. An unfiltered `worker.finished` subscription matches
 * every worker finishing — including the subscriber's own finish, which wires
 * the new worker to itself and loops (OC1's finding). Naming the source is what
 * makes the drawn wire mean what the drag said.
 */
export function wireFilter(from: string): Record<string, unknown> {
  return { worker: from }
}

/** "When email-answerer finishes, wake email-reviewer" — §11 rule 1: the
 *  operator's vocabulary first, the exact fields underneath. */
export function wireSentence(from: string, to: string): string {
  return to === ''
    ? `When ${from} finishes, wake … (choose a worker)`
    : `When ${from} finishes, wake ${to}`
}

/** How an arriving event reads in a sentence: "email-answerer finishes". */
function arrivalPhrase(wire: OrgChartWire): string {
  if (wire.from === null) return `${wire.eventType} arrives`
  if (wire.eventType === 'worker.finished') return `${wire.from} finishes`
  if (wire.eventType === 'worker.failed') return `${wire.from} fails`
  return `${wire.from} emits ${wire.eventType}`
}

/** The wire-cut wording (§6.4 rule 3, §11 rule 2): it says what it does, and
 *  the word "undo" never appears — the write goes forward, like every other. */
export function cutWireTitle(wire: OrgChartWire): string {
  return `Stop waking ${wire.to} when ${arrivalPhrase(wire)}`
}

/** The enabled/frozen toggle's button, in the vocabulary of §3 of the spec. */
export function toggleTitle(worker: Worker, field: 'enabled' | 'frozen'): string {
  if (field === 'frozen') return `${worker.frozen ? 'Unfreeze' : 'Freeze'} ${worker.name}`
  return `${worker.enabled ? 'Disable' : 'Enable'} ${worker.name}`
}

/** What the toggle actually does, in one sentence. */
export function toggleSentence(worker: Worker, field: 'enabled' | 'frozen'): string {
  if (field === 'frozen') {
    return worker.frozen
      ? `Other workers will be able to rewrite ${worker.name}'s prompt again.`
      : `${FROZEN_SENTENCE} Only a human, through this API, may edit or unfreeze it.`
  }
  return worker.enabled
    ? `${worker.name} stops being woken by any event or schedule. Its subscriptions and schedules are left exactly as they are.`
    : `${worker.name} starts being woken again by the events and schedules it already has.`
}

/** The proposal card (§6.4): a sentence, the exact fields in mono, a MANDATORY
 *  reason, and a button that names the write. */
function WireProposalCard({
  proposal,
  workers,
  busy,
  onCancel,
  onSubmit,
}: {
  proposal: WireProposal
  workers: string[]
  busy: boolean
  onCancel: () => void
  onSubmit: (target: string, rationale: string) => void
}) {
  const [target, setTarget] = useState(proposal.to)
  const [why, setWhy] = useState('')
  const ready = target !== '' && why.trim() !== '' && !busy
  return (
    <Dialog open onClose={onCancel} maxWidth="sm" fullWidth data-testid="wire-proposal">
      <DialogTitle>{wireSentence(proposal.from, target)}</DialogTitle>
      <DialogContent>
        <TextField
          select
          fullWidth
          size="small"
          label="Wake which worker?"
          value={target}
          onChange={(e) => setTarget(e.target.value)}
          sx={{ mt: 1, mb: 2 }}
        >
          {workers
            .filter((name) => name !== proposal.from)
            .map((name) => (
              <MenuItem key={name} value={name} sx={{ fontFamily: MONO }}>
                {name}
              </MenuItem>
            ))}
        </TextField>

        <Stack spacing={0.5} sx={{ fontFamily: MONO, fontSize: 13, mb: 2 }}>
          <Box>
            <Box component="span" sx={{ color: 'text.secondary' }}>
              event_type{'  '}
            </Box>
            {WIRE_EVENT_TYPE}
          </Box>
          <Box data-testid="proposal-filter">
            <Box component="span" sx={{ color: 'text.secondary' }}>
              filter{'      '}
            </Box>
            {JSON.stringify(wireFilter(proposal.from))}
          </Box>
        </Stack>
        <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mb: 2 }}>
          The filter names {proposal.from}, so only {proposal.from} finishing wakes this worker. An
          unfiltered {WIRE_EVENT_TYPE} would wake it when any worker finishes — including itself.
        </Typography>

        <TextField
          fullWidth
          size="small"
          required
          label="Why are you wiring this?"
          value={why}
          onChange={(e) => setWhy(e.target.value)}
          helperText="Recorded on the config event. The drag is the cheap part; the reason is the record."
        />
      </DialogContent>
      <DialogActions>
        <Button onClick={onCancel}>Cancel</Button>
        <Button variant="contained" disabled={!ready} onClick={() => onSubmit(target, why.trim())}>
          Wire it up
        </Button>
      </DialogActions>
    </Dialog>
  )
}

/** One confirmation, one required reason — shared by the wire cut and the
 *  enabled/frozen toggles, so all three ask in the same words (§11 rule 6). */
function ReasonDialog({
  testId,
  title,
  sentence,
  reasonLabel,
  confirmLabel,
  busy,
  onCancel,
  onConfirm,
}: {
  testId: string
  title: string
  sentence: string
  reasonLabel: string
  confirmLabel: string
  busy: boolean
  onCancel: () => void
  onConfirm: (rationale: string) => void
}) {
  const [why, setWhy] = useState('')
  return (
    <Dialog open onClose={onCancel} maxWidth="sm" fullWidth data-testid={testId}>
      <DialogTitle>{title}</DialogTitle>
      <DialogContent>
        <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
          {sentence}
        </Typography>
        <TextField
          fullWidth
          size="small"
          required
          label={reasonLabel}
          value={why}
          onChange={(e) => setWhy(e.target.value)}
          helperText="Recorded on the config event."
        />
      </DialogContent>
      <DialogActions>
        <Button onClick={onCancel}>Cancel</Button>
        <Button
          variant="contained"
          disabled={why.trim() === '' || busy}
          onClick={() => onConfirm(why.trim())}
        >
          {confirmLabel}
        </Button>
      </DialogActions>
    </Dialog>
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
