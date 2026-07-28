# 21 — Console UX review: the populated-state critique, and motion

*Written 2026-07-28, immediately after doc 16 completed (all 16 items merged). Status:
**review + proposal — nothing here is built.** Method: the built console was walked through with a
realistic populated fixture org (the doc-15 email desk: actor-critic + archivist + frozen scorer +
scheduled writer, with history, an open ask, a parked delivery, a five-strike schedule and a
freeze refusal) and screenshotted in both themes. Screenshots: `docs/product/ux-review/`.
The brief for this review, from Kai: when a human looks at this system it is hard to see what is
going on — **motion should carry the movement of work along the edges**, and each surface should
borrow the best visualisation tricks findable in prior art.*

---

## 1. The verdict in one paragraph

The bones are right. The Desk reads as designed (authorship marks work; rationales read as commit
messages; the honest copy is doing its job), lineage genuinely looks like a commit history, and
the chart's information is all present. What is missing is exactly what the brief names: **the
console is a set of photographs of a system that is actually a film.** Nothing moves, nothing
enters, nothing transitions — a delivery that is `running` looks identical to one that has been
dead for an hour; a new ask appears only if you re-render and re-read. And the populated states
expose seven real rendering/copy defects that the empty states could not show. This document
files the defects (§2), critiques each surface (§3), records the prior-art research (§4), and
proposes the motion system (§5) with a work-item sketch (§6).

## 2. Defects found by the populated walkthrough (fix before any glisten)

| # | Where | Defect | Evidence |
| --- | --- | --- | --- |
| X1 | Chart | **Giant clipped disc in the canvas corner** — the frozen plate's lock is `SpineGlyph` (`Box component="svg"` sized by CSS), and CSS sizing does not apply to an `<svg>` nested inside SVG, so it renders at the 300×150 default, huge and clipped. Both themes | `pop-chart-light.png` bottom-right |
| X2 | Chart | **Wire-label collision** — two subscriptions sharing a rank (`worker.finished {worker: email-answerer}` → archivist AND reviewer) overprint their riding labels into garbage; the conventions overlay makes it worse (its `ROUTE-TO` label clips under the reviewer's plate) | `pop-chart-light.png`, `pop-chart-conventions-light.png` mid-canvas |
| X3 | Chart | **Schedule dials float** — dials overlap neighbouring plates and read as stray circles, not docked clocks, whenever plates are adjacent in a rank | `pop-chart-light.png` around `invoice-parser`/`social-writer` |
| X4 | Chart | **Entry-pip label duplicated** — the pip caption and the first wire's riding label are the same text 60px apart | `pop-chart-light.png` top |
| X5 | Lineage | **"2 rewrites · 3 distinct"** — distinct > rewrites reads as broken arithmetic. Cause: a `worker_update` carrying a prompt counts as a *version* but not a *rewrite*, while "distinct" counts distinct prompt texts across versions. Recount/relabel: "3 versions · 2 rewrites, both distinct" | `pop-worker-lineage-light.png` header |
| X6 | Everywhere diffs render | **Diff colours are raw MUI `success.light`/`error.light`** — saturated green/red bands, the exact un-themed look the design forbids. Should be ember/fault *tints* per the design deck | `pop-worker-lineage-light.png` |
| X7 | Shell | **Desk badge ≠ Asks count** — badge says 2 (open attention requests), Asks stack says 1 (requests joined to parked deliveries). The DK2 approximation confuses the moment both are visible | `pop-desk-light.png` |
| X8 | All time renders | **Verbose US-locale timestamps** (`7/28/2026, 6:43:20 AM`) where the design shows compact times. Wants: time-of-day for today, day+time for this week, date beyond — and ticking relative ages on open states | every screenshot |
| X9 | Chart | **A parked ask is invisible** — `email-answerer` holds the Ridley ask yet reads `idle 0/1`. The rose state exists on the Desk but not on the plate | `pop-chart-light.png` |
| X10 | Chart | **Trouble is invisible** — `invoice-parser`'s five-strike-disabled schedule renders as a normal dial; nothing on the chart says its clock is dead | `pop-chart-light.png` |
| X11 | Job tables | `awaiting_human` chips still MUI-amber (filed in doc 16; restated here because the fixtures make it visible) | `pop-desk-light.png` trouble stack |
| X12 | Desk/lineage | **The rail is nearly invisible** — the signature element reads as floating dots; at 1px `ink-20` it disappears at normal reading distance | `pop-desk-light.png` |

None of these are deep; X1–X4 are one focused chart-rendering item, X5–X8 are small display-logic
items, X9–X10 are small state-plumbing items on data the chart already fetches.

## 3. Surface-by-surface critique

**Desk** (`pop-desk-light.png`) — the strongest screen, close to the deck. What it lacks is
*liveness*: the ask's age is stamped once, not ticking; a new change appears with no entrance;
"since you last looked" is a filter, not a visible waterline in the list. The three stacks also
render at equal visual weight — the deck's intent was Asks loudest.

**Chart** (`pop-chart-*.png`) — all information present, no *state* legible at a glance beyond
`running`. The four rendering defects (X1–X4) dominate first impressions. Deeper critique: the
chart shows **structure** but nothing of **traffic** — you cannot see that `email.received` fired
twice today, that a delivery is queued, or which wire carried the last event. This is the brief's
core complaint, and §5 is the answer.

**Lineage** — reads as intended (a commit history). Two gaps: no motion when folding to a version
(the context switch is instant and disorienting), and the diff palette (X6).

**Memory** (`pop-memory-light.png`) — clean; the `name=` KV headline works. The selector bar is
an empty text field where the deck promised chips-per-clause; usable, but the teaching function
is missing.

**Events/Automation** — functional tables; the events list has no live tail (new events appear
only on reload), and a `running` delivery's duration is stamped at render, not counting.

## 4. Prior art (researched 2026-07-28)

*Two research passes were run — animated flow on node-link graphs, and live-feed/status-lifecycle
motion. Full reports live with the session; the durable conclusions:*

### 4.1 Flow on graphs

The framing decision the survey converges on: **looping motion asserts an ongoing state;
one-shot motion asserts a discrete event.** Our deliveries are discrete, so edges are silent when
idle and one-shot on delivery; nodes, which do have a genuine running state, may loop. React
Flow's default `animated` edge is a permanent dash loop — the wrong default for us. (Honest
negative result: the argument "continuous edge animation implies streaming when events are
discrete" is not written down anywhere findable; the adjacent evidence is n8n's stuck-animation
bug report, the spinner-trust literature, and animation-fatigue studies, all pointing one way.)

Four rules, verbatim from the research:

1. **Motion must be caused** — traceable to a specific event with a timestamp; can't name the
   event ⇒ delete the animation.
2. **Motion must terminate** — loops only for states that genuinely persist, and they must
   visibly stop (a viewer's only way to tell "running" from "stuck" is that motion stops).
3. **N discrete deliveries ≠ a stream** — ≤3 concurrent particles per wire; past that, a static
   count (`↳ ×14`). A dense particle train is the visual grammar of a pipe, and we have no pipe.
4. **Nothing encoded only in motion** — every state must survive a still screenshot.

Mechanics, ranked:

- **One-shot dot traversal** — SMIL `<animateMotion begin="indefinite">` fired per delivery via
  `beginElement()` (measurably the cheapest: route06 cut hover frame-drops ~5→2–3 by moving an
  ERD from dash-loops to this), or WAAPI + CSS `offset-path` if we want cancellation and
  `.finished` promises. Speed-normalise: `dur = clamp(pathLength/600, .35, .9)s` (constant
  ~600px/s — fixed durations make long wires look faster). Spline easing (the one controlled
  study on animated edge drawings found easing measurably improved topology-task accuracy;
  participants preferred slow). **Never `rotate="auto"`** on orthogonal wires — a chevron snaps
  90° at every corner. *Verify before building:* `offset-path` on SVG `<circle>` children in
  Firefox/Safari; if it holds, WAAPI beats SMIL long-term (SMIL has no clear owner).
- **Wire flash-and-decay** — stroke colour+width transition, fast-in (60ms) slow-out (450ms). A
  colour change, not a position change, so it *is* the reduced-motion substitute (Comeau:
  substitute, don't delete). Coalesce bursts — WCAG 2.3.1 caps flashing at 3/sec.
- **Static direction chevrons on every wire, always** — direction becomes a permanent property
  and the chart is legible with zero motion (Node-RED's counts-and-glyphs posture: it
  deliberately animates nothing and shows per-port queue counts instead).
- **Running node** = slow (~2s) opacity-only breathe on a hairline bracket, plus a mono status
  line that carries the meaning — Carbon's rule: shape/colour = status, motion = only "still
  going". **No spinners** (documented trust erosion on long indeterminate waits; ours run
  minutes). No scale, no glow (SVG filters are expensive and kill the schematic).
- **Failure is a state, not an event** — the wire flashes to fault and *stays*. Never animate a
  failure away.
- **Trace** = dim-the-rest (~0.22) + mono hop numbers + outcome-by-colour (Datadog/Jaeger's
  critical-path idiom; Temporal's green/red), optional staggered draw-in (`pathLength="1"`,
  `stroke-dashoffset 1→0`, ~120ms/hop). Reduced motion drops only the stagger — zero information
  lost, which is the tell it is the right pattern. Hop numbers make causality reconstructible
  from a screenshot, which motion never allows.
- **The SMIL trap:** CSS cannot pause `<animateMotion>` — `prefers-reduced-motion` has no effect
  on it. Gate in JS: `matchMedia` and don't call `beginElement()` (fire the flash instead), or
  `svgRoot.pauseAnimations()`.
- **Timing:** 350–600ms per traversal (NN/g band 100–500ms; 1s ceiling). Avoid rAF +
  `getPointAtLength` particles entirely (worst measured performance; buys nothing at our scale).

### 4.2 Feeds and status lifecycles

Two framing facts drive everything:

- **WCAG 2.2.2 (Pause, Stop, Hide) applies to auto-updating feeds** — a list that auto-inserts
  is auto-updating content, so the "N new items" pill is not a nicety, it is the *pause
  mechanism*, and a "Pause live updates" toggle must exist for everyone, not only
  reduced-motion users.
- **An event-sourced feed gets "since you last looked" almost free**: one monotonic
  `last_seen_seq` per (operator, surface) derives all four of — the waterline divider position,
  the "N new" count, which items animate on entry, and which get highlighted. One integer, four
  renderings.

The patterns, condensed:

- **Pinned "N new" pill, no auto-insert** (X/Twitter removed auto-refresh because "tweets would
  disappear from view mid-read"; Slack, GitHub, every log live-tail): arrivals land in a staging
  buffer; auto-flush only when the viewport is already at head (IntersectionObserver, not
  scrollTop). Doubles as the screen-reader target — one debounced `role="status"` summary ("3
  new deliveries, 1 failed"), never per-item announcements.
- **Highlight-then-fade** (the 37signals Yellow Fade Technique — *not* yellow here: ember-soft
  for agent-authored arrivals, rose-soft for asks; yellow collides with `rate_limited`).
  800ms hold + 1.5s decay; cap concurrent highlights (20 arrivals highlight the block boundary,
  not 20 rows). Reduced-motion replacement is *better*, not lesser: a persistent 2px left border
  + `NEW` chip cleared on watermark advance.
- **Two cheap-to-get-wrong corollaries**: animate on *arrival*, never on render (gate on a
  "stream hydrated" flag or the whole backfill fades on load and the signal dies permanently);
  dedupe by event id across SSE reconnects (a replay re-fires every highlight — the most common
  live-feed motion bug; our one-reducer-for-live-and-replay discipline is what catches it).
- **Status transitions need a projection first**: `pending→running→ok` arrives as N events
  projecting onto ONE row keyed by delivery id — render the raw log and you get three rows and
  nothing to animate. Then: chip crossfades 120–160ms (the chip's change is the payload); the
  row takes one destination-tinted pulse ~1.2s, severity-weighted, which is what peripheral
  vision catches. Colour alone fails — Carbon: ≥3 of {colour, shape, text, position}.
- **Position stability (zero cost, prevents the worst failure)**: a row's sort position must
  never depend on its status. Sort by created; let status change in place. Sorting by
  recently-updated teleports the exact row the operator is watching.
- **The waterline**: a full-width divider at `last_seen_seq` labelled `New since 09:12`,
  **frozen for the duration of the visit** (a divider that chases you down the page is useless);
  advance on unload or explicit mark-read. GitHub's transplantable refinements: "changes since
  your last review" as a *cumulative diff default* when a prompt was rewritten twice since you
  looked, and a **Viewed state that auto-invalidates when the thing changes again** — without
  invalidation, "viewed" quietly becomes a lie.
- **Time gaps rendered as gaps** — no design system documents this; it is ours to design. Bucket,
  never scale linearly (a 14h overnight gap must not push everything off-screen): <5min tight;
  5min–2h medium + faint elapsed label; >2h a **dashed/broken rail segment** — "nothing happened
  here" as distinct from "the list continues". Day dividers sticky; timestamps as `3h` (drop
  "ago"); absolute time in `<time datetime>` + tooltip.
- **Ticking durations — the literature is more negative than expected** (elapsed "accentuates
  the passing of each and every second"). The rule that survives: tick only when knowing the
  elapsed *is* the operator's decision input. So: `running` ticks (1s under 2min, then 60s
  granularity); `awaiting_human` ticks prominently and **escalates** (amber at 1h, rose→fault at
  4h) — the number is the call to action, an SLA on the operator; `ok`/`failed` are static
  totals (a ticking clock on a finished thing is a bug); `rate_limited` counts *down* when
  retry-after is known. One shared app-level ticker, never per-row intervals; compute from
  server `started_at`. A11y: the ticking text is `aria-hidden`, a coarse static label on the
  container refreshes every few minutes — otherwise screen readers announce every second.
- **Long agent jobs**: past 10s a spinner is inadequate and percent-done is unavailable
  (unbounded, non-linear work), so the honest affordance is **step count + last-step label +
  elapsed** — which maps directly onto the event spine. On completion report start/stop/elapsed
  and what was produced, not "Done" (NN/g long-wait guidance).
- **Diff disclosure**: collapsed by default behind `+12 −4` + first-hunk summary
  (`<details>/<summary>` for free keyboard semantics); height animation via the
  `grid-template-rows: 0fr→1fr` trick (child `overflow:hidden; min-height:0`), 200ms.
  The rationale is never collapsed — it is the point of the lineage.
- **ARIA**: the Desk stacks and lineage rail are `role="log"` (chronological, implicit polite),
  NOT `role="feed"` (which drags in a keyboard contract we don't need); chips and the pill are
  `role="status"`.


## 5. The motion system (proposal)

One principle, then the mechanics: **motion is spent on exactly two truths — "work is moving" and
"this changed while you looked away" — and every animation has a static equivalent.** Anything
else stays still. Plus one obligation the research surfaced: auto-updating surfaces get a
**"Pause live updates" toggle for everyone** (WCAG 2.2.2 — the pill below is the mechanism).

**M0 — Chevrons + the still-screenshot floor (chart, no motion).** Every wire gains a static
mid-point direction chevron; wires carry `↳ ×n` counts when traffic exceeds what pulses can
honestly show. The chart must answer "what moved today" from a screenshot before any animation
lands — this is the reduced-motion floor everything else degrades to.

**M1 — The pulse (chart).** One delivery = one 3px dot in the authorship colour traversing the
wire once — speed-normalised (~600px/s, 350–600ms), spline-eased, `rotate` fixed, ≤3 concurrent
per wire (beyond that, the `×n` count takes over). Arrival = the wire's flash-and-decay (60ms
in / 450ms out) + a brief brightening of the target plate's state line; **a failed delivery
flashes the wire to fault and stays**. On chart open, the last ~10 deliveries replay as staggered
pulses. Implementation: SMIL `begin="indefinite"`/`beginElement()` or WAAPI `offset-path`
(verify FF/Safari on SVG children first — if it holds, prefer WAAPI); reduced-motion gate **in
JS** (CSS cannot pause SMIL), falling back to flash-only, then to M0's counts.

**M2 — Running and waiting states (chart).** `running` = slow (~2s) opacity-only breathe on the
plate's ember dot + the mono status line carrying elapsed (`● running · 4m 12s`, ticking per the
M4 discipline). `awaiting_human` = the rose diamond, static — a pause must look like a pause —
with its *age* ticking and escalating. A five-strike-dead schedule = fault cross on its dial
(X10). No spinners anywhere. Reduced motion: no loop, dot at full opacity; the text carries it.

**M3 — Trace (propagation panel + canvas).** Dim the non-matching graph to ~0.22, mono hop
numbers ①②③ at each hop, outcome by colour; staggered draw-in (~120ms/hop) as the only motion,
dropped wholesale under reduced motion with zero information lost. Canvas pips become clickable
and run the same trace.

**M4 — Feed liveness (Desk + Events, shared infrastructure).** In build order: (1) a persisted
`last_seen_seq` per operator+surface — one integer that derives the waterline divider (full-width,
labelled `New since 09:12`, **frozen for the visit**), the "N new" count, and what animates;
(2) the pinned **"N new" pill** — arrivals stage in a buffer, auto-flush only when the viewport
is at head; (3) entrance = slide-fade 150–200ms decelerate + authorship-tinted hold-then-decay
(ember-soft agent / rose-soft ask; 800ms hold, 1.5s fade; block-boundary-capped; **gated on
stream-hydrated** so backfill never fades, deduped by event id across SSE reconnects);
(4) status chips crossfade ~140ms + one destination-tinted row pulse, on a table that is a
**projection keyed by delivery id** (precondition) and **sorted by created, never by updated**
(position stability); (5) the shared elapsed ticker — one app-level interval; `running` ticks
then coarsens to 60s, `awaiting_human` ticks prominently and escalates amber→fault at 1h/4h,
terminal states are static totals; ticking text `aria-hidden` with a coarse label refreshed
occasionally. Stacks are `role="log"`; the pill is `role="status"` with one debounced summary.
Asks get one degree more visual weight (rose left border, larger message type). Reduced motion:
persistent `NEW` border+chip instead of the fade — offered to everyone as a setting, since the
research suggests it is arguably better.

**M5 — Lineage.** Day dividers + `<time datetime>`; diffs collapsed behind `+n −m` + summary
(`<details>`, `0fr→1fr` height animation, 200ms; rationale never collapsed). Fold-to-version
slides under the history banner (150ms) instead of teleporting. Two GitHub transplants: when a
prompt was rewritten more than once since your waterline, default to the **cumulative diff**
with per-revision diffs beneath; and a **Viewed state that auto-invalidates** when the prompt
changes again. Diff palette moves to ember/fault tints (X6).

**M6 — Long-job affordance (deliveries, later).** Past ~10s, a running row shows **step count +
last-step label + elapsed** (percent-done is dishonest for unbounded agent work); completion
reports start/stop/elapsed and what was produced. Data comes from the event spine; may need a
query-events summary — flag as possibly backend-touching.

All of M0–M6: CSS transitions + SMIL/WAAPI only (no animation library — the no-new-deps rule),
every loop and entrance gated on `prefers-reduced-motion` with the named static equivalents, and
the pause toggle available regardless of OS preference.

## 6. Proposed work items (for doc 16's owner to adopt as a follow-up wave)

- **P1 — Chart rendering fixes**: X1 (inline the lock as a plain `<path>` sized in SVG units),
  X2 (label lanes: offset riding labels per-wire within a shared run; conventions labels get
  their own lane below), X3 (dock dials into a reserved gutter column left of the plate rank),
  X4 (drop the duplicate pip caption).
- **P2 — Display-logic fixes**: X5 version/rewrite recount + relabel; X7 badge = asks count
  (lift the join, or fetch deliveries in the shell hook); X8 one shared compact time formatter
  (today→`14:32`, this week→`Mon 14:32`, else date) + ticking ages hook; X11 rose chips; X12
  rail weight up one step.
- **P3 — Chart state plumbing**: X9 rose diamond for parked asks (deliveries already fetched),
  X10 fault mark on dead clocks (schedules already fetched), pip-click-to-trace.
- **P4 — Feed liveness infrastructure + Desk/Events motion** (M4) — in the research's build
  order: `last_seen_seq` → waterline → pill → highlight module (with the reduced-motion
  persistent-marker fallback) → shared ticker → status projection + crossfade. The delivery-id
  projection and created-at sort order are preconditions, not polish.
- **P5 — Chart motion** (M0 first — chevrons + counts, no animation, immediate win — then M1,
  M2, M3). The research's spike order: chevrons ~30min, flash ~1h, one-shot dot ~2–3h, breathe +
  status line ~2h, trace ~half day. Verify `offset-path`-on-SVG in FF/Safari before choosing
  SMIL vs WAAPI.
- **P6 — Lineage disclosure + fold** (M5, X6) — cumulative-diff-since-waterline, auto-invalidating
  Viewed, collapsed diffs, the fold slide, diff palette.
- **P7 — Long-job step affordance** (M6) — last, possibly backend-touching.

P1–P3 are pure fixes and can execute exactly like doc 16's waves. P4–P6 are the design's motion
budget spent; each should land with vitest coverage of the *logic* (what animates when, reduced-
motion branches) and a screenshot/video pass for the *feel*, which jsdom cannot judge.

## 7. What was NOT reviewed

Chat view (its dark-mode hardcoded colours are already filed in doc 16), the topology onboarding
flow's populated collision states, drag-to-wire under a real pointer (jsdom-only so far — needs a
stack session), and everything against the real backend (the walkthrough ran on a fixture stub;
the compose stack was held by another session throughout).

---

## 8. Execution plan (added 2026-07-28, approved by Kai)

**Rules: doc 16's EXECUTION RULES, Standing traps and Discovered Issues Log apply verbatim** —
isolated worktrees, base-sha reset, no `git stash`, no scope expansion, validation before done,
every new `web/src` export through `index.ts`, no new runtime deps (all motion is CSS
transitions + SMIL/WAAPI). Where an item below says "per §2/§4/§5", the named section of THIS
document is the spec.

*Validation for every item:* `cd web && npm ci && npm run typecheck && npm test`; items touching
`examples/web` add `cd examples/web && yarn && yarn typecheck && yarn build`.

### Wave A — fixes (parallel; disjoint files)

- [x] **W1 — Chart rendering + state fixes (P1+P3).** In `orgchart.ts`/`OrgChartPage.tsx` (+
  tests): X1 lock as an inline `<path>` in SVG units (kill the nested-svg default-size bug);
  X2 label lanes — riding labels offset per-wire within a shared run, conventions labels in
  their own lane, no overprint at any of the 13 seed shapes (add a two-subscriptions-one-rank
  regression fixture); X3 dials docked in a reserved gutter column so they can never overlap a
  plate; X4 drop the duplicate pip caption; X9 rose diamond on a plate whose worker has an
  `awaiting_human` delivery (deliveries are already fetched); X10 fault cross on a dial whose
  schedule is disabled with `provision_failures >= 5`; canvas pips clickable → run the existing
  propagation trace.
- [x] **W2 — Display-logic fixes (P2).** X5: recount/relabel lineage header ("N versions · M
  rewrites, K distinct" with distinct ≤ rewrites; fix `workerLineage`); X7: Desk badge = asks
  count (lift the ask-join into the shell's data path — `useDesk` already computes it; do NOT
  fetch twice); X8: one shared compact time formatter module (`web/src/timefmt.ts`: today →
  `14:32`, this week → `Mon 14:32`, else `21 Jul 2026`; used by Desk, lineage, changelog,
  events, memory — replace the raw `toLocaleString` calls) with an `agoShort` helper (`3h`, no
  "ago"); X11: `awaiting_human` chips render rose (theme-aware, not MUI warning) wherever
  delivery chips appear; X12: spine rail one step darker.
- [x] **W3 — Lineage disclosure, waterline-independent parts (P6a).** X6: `DiffBlock` moves to
  ember/fault tints with theme fallbacks (spine.tsx's palette-resolution pattern) — applies
  everywhere DiffBlock renders; diffs in feeds collapsed by default behind `+n −m` + first-hunk
  summary using `<details>/<summary>`, height-animated via `grid-template-rows: 0fr→1fr` (child
  `overflow:hidden; min-height:0`), 200ms, snap under reduced motion; the rationale is NEVER
  collapsed; fold-to-version slides under the history banner (150ms) instead of teleporting.

### Wave B — motion (after Wave A merges)

- [ ] **W4 — Feed liveness (P4), per §4.2/§5-M4 in the research's build order.**
  (1) `last_seen_seq` watermark per operator+surface (localStorage, keyed like useDesk's
  mark; use config-event/event ordering as the sequence); (2) waterline divider, frozen for the
  visit, labelled `New since <time>`; (3) pinned "N new" pill per stack/list — arrivals stage,
  auto-flush only when viewport is at head (IntersectionObserver), pill is `role="status"` with
  one debounced summary; (4) shared highlight module: slide-fade entrance 150–200ms decelerate +
  authorship-tinted hold-then-decay (ember-soft agent / rose-soft ask, 800ms+1.5s), gated on
  stream-hydrated, deduped by id, block-boundary-capped; reduced-motion = persistent `NEW`
  border+chip (also available to everyone as a "calm" preference); (5) one shared elapsed
  ticker: `running` ticks then coarsens to 60s, `awaiting_human` ticks prominently and escalates
  (amber ≥1h, fault ≥4h), terminal states static, ticking text `aria-hidden` with coarse
  container labels; (6) deliveries table: projection keyed by delivery id, sorted by created
  (never by updated), chip crossfade ~140ms + one destination-tinted row pulse; (7) a "Pause
  live updates" toggle on Desk + Events. Desk stacks/lineage rail get `role="log"`.
- [ ] **W5 — Chart motion (P5), per §4.1/§5-M0..M3, in the spike order.** M0 chevrons
  (`marker-mid` or midpoint `▸`, rotation fixed per segment — never auto) + `↳ ×n` traffic
  counts on wires (from the fetched deliveries; this is the reduced-motion floor); wire
  flash-and-decay (60ms in / 450ms out, coalesced ≤3/sec, fault flash STAYS); one-shot dot
  traversal per delivery — feature-detect `offset-path` on an SVG circle, use WAAPI if it
  holds, else SMIL `begin="indefinite"`+`beginElement()`; speed-normalised
  `clamp(len/600,.35,.9)s`, spline easing, ≤3 concurrent per wire; chart-open replay of the
  last ≤10 deliveries staggered ~80ms; `running` breathe (2s opacity-only) + ticking status
  line; trace draw-in (`pathLength="1"` dashoffset, ~120ms/hop) + dim-to-0.22 + mono hop
  numbers + outcome colours. ALL motion gated in JS via one `usePrefersReducedMotion` (CSS
  cannot pause SMIL); reduced motion = flash-only, then counts.

### Wave C — the waterline-dependent tail (after Wave B merges)

- [ ] **W6 — Lineage waterline features + long-job affordance (P6b+P7, frontend only).**
  Cumulative diff since the operator's watermark as the default when >1 rewrite since last look
  (per-revision diffs beneath); a Viewed state per version that auto-invalidates when the prompt
  changes again; long-running delivery rows show step count + last-step label (derived from the
  session's query-events, which EventJobHistory already fetches for tokens) — no backend change;
  completion rows show static start/stop/elapsed and "what was produced" via the session link.

### Discovered Issues Log (waves A–C)

*(orchestrator-owned)*

- **(W1) X2 was two bugs**: lane collisions AND a paint-order bug — labels were drawn before the
  plates and buried under them. All labels now live in one `org-chart-labels` layer drawn last,
  pointer-transparent (the wire polyline stays the click target). Label testids are
  `label-<id>`, not `wire-label-*` (OC2 pins `^wire-` by count).
- **(W1) X4's caption drop is conditional** — an unwired pip keeps its caption (`Pip.wired`);
  charts with any schedule are 36px/column wider (the gutter is reserved chart-wide to keep
  ranks on one grid). Pips gained a hit rect + pointerdown stopPropagation (pan was eating pip
  clicks — the "clickable pips" plumbing already existed).
- **(W2) X5's `distinct` was redefined, not relabelled** — now counts rewrites that changed the
  text (≤ rewrites by construction); a revert loop A→B→A is 2 distinct rewrites.
- **(W2) X7 costs one duplicate deliveries fetch** while the Desk is open (badge hook +
  useDesk); the join itself is shared. W4's feed infrastructure is the place to collapse it.
- **(W2) `deliveryStatusSeverity('awaiting_human')` is now 'default'** (pin updated); rose is
  painted by the new `DeliveryStatusChip`. `timefmt.ts` is deliberately locale-independent
  (fixed 24h forms — a locale-dependent formatter is a different design per browser).
- **(W3) No matchMedia mock pattern existed** (the brief claimed one); `useReducedMotion.ts` is
  the new shared gate — W4/W5 must copy its test pattern. jsdom reports untinted backgrounds as
  `rgba(0, 0, 0, 0)`. EventsPage's diff-summary assertion requires the `<summary>` line to stay
  ONE text node. Ember/fault tokens now copied in a third place — if a fourth appears, spine.tsx
  should export the token table.
- **(orchestrator) Wave A merged conflict-free; web 959→1046 tests.** Fixture re-shoot confirms
  every X-defect visually fixed: lock glyph correct, labels laned, rose diamond + awaiting count
  on the plate, fault cross on the dead clock, badge=asks, compact times, visible rail.
