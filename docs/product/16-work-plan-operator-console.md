# Work plan — operator console

*Created 2026-07-28. Executes [`15-operator-console-design.md`](./15-operator-console-design.md)
(§12 build order, decisions K1–K6, backend asks B1–B5). Format and rules follow
[`06-work-plan.md`](./06-work-plan.md) and [`13-work-plan-self-improvement.md`](./13-work-plan-self-improvement.md).
Intended execution: a workflow of cheap executors — every item is written to be implementable
without wider context than this file, the design doc, and the files it names.*

## EXECUTION RULES

1. **Executors work in isolated worktrees**, one per item (or item group as marked). They do NOT
   edit this file — the orchestrator owns it. Surprises are reported as bullets in the final
   report, for the orchestrator to file in the Discovered Issues Log below.
2. **Verify your base first.** Three executors in a row in the last plan found their worktree
   branch NOT on `product-layer`. First command in every worktree:
   `git log --oneline -1` and confirm the tip matches the orchestrator's stated base.
3. **Run the item's validation commands verbatim before claiming done.** The orchestrator re-runs
   them after merge. "Done" without a green validation run is not done.
4. **Merges are sequential**; the orchestrator resolves conflicts and re-validates after each.
5. **The compose stack is a serial resource.** Only the orchestrator runs
   `docker compose up -d --build web` and browser checks; executors develop against vitest/jsdom
   and must say clearly if a visual claim is untested.
6. **Do not touch:** `.env` (real credentials), `e2e/experiments/` (owned elsewhere), the shared
   test Postgres container `ao-test-pg` on `localhost:5433` (use it, never delete/recreate it),
   `sandbox/` (out of scope entirely), and the real Anthropic API.
7. **Design authority:** where this plan and `15-operator-console-design.md` disagree, the design
   doc wins on *what*; this plan wins on *sequencing and validation*.

## Standing traps (scar tissue — read before coding)

- **`web/` pins the config-action vocabulary by count** (`configLog.test.ts`, `toHaveLength(19)`).
  No item in this plan adds a config verb; if you think yours does, stop and report instead.
- **`WorkerEditor.tsx` contains literal NUL bytes** (`'\x00new'`/`'\x00none'` sentinels) — git
  treats it as binary and its diffs are invisible. Touch it carefully and describe your change in
  the commit message, because the diff won't.
- **`PUT` is create-or-replace, not patch** — any read-modify-write of a worker must carry ALL
  fields including `frozen` (an earlier helper silently thawed frozen workers). Same for
  subscriptions and settings.
- **`coerceSchedule(raw, project?)` mistypes under `array.map`** (index lands in `project`) —
  always wrap in a lambda. Same footgun for `coerceWorker`.
- **Lockfiles differ by package.** `web/` uses `npm ci` (package-lock.json only).
  `examples/web/` uses **yarn only** (yarn.lock only — `deploy/web.Dockerfile` depends on it);
  if you add a dependency there, the updated `yarn.lock` goes in the same commit. Never run npm
  in `examples/web/`.
- **`web/` is a component library: no router, no new runtime deps.** New pages follow the
  established pattern — controlled/uncontrolled `selected`/`onSelect`, query params written
  through the History API, popstate listener (copy the shape from `WorkersPage.tsx`). Anything
  that would be a new `dependencies` entry in `web/package.json` needs orchestrator sign-off;
  the default answer is no (hand-roll it).
- **Every new export goes through `web/src/index.ts`** — component + props type + pure module
  functions, matching the existing export blocks.
- **gorm `default:` tags: NEVER** on a column where zero/false is meaningful. DDL defaults in
  migration SQL; defaulting in `normalize()`. (Go items only.)
- **Migrations**: append to `agentMigrations`, next free number; sibling items may claim the same
  number — orchestrator renumbers on conflict. (Go items only.)
- **The stack serves a BUILT web image** — UI edits are invisible in the browser until
  `docker compose up -d --build web` (orchestrator-only, rule 5).
- **`agentdb` live-Postgres cases skip silently** without `AGENTKIT_TEST_POSTGRES_URL` — a green
  plain run does not prove the Postgres paths. Go items must run both commands.
- **Assert on happens-after signals, never sleeps** in any test that waits.
- **CI pre-existing break, not yours:** `.github/workflows/ci.yml`'s `web` job runs
  `yarn install --frozen-lockfile` but `web/` has no yarn.lock on this branch. Known; do not fix
  it inside a console item; do not "restore" a yarn.lock.

## Design tokens (single source for every item below)

From `15-operator-console-design.md` §3. Light / dark:

| token | light | dark | means |
| --- | --- | --- | --- |
| paper | `#FBFAF9` | `#0E1114` | ground |
| ink | `#12161A` | `#E8EAEC` | text, hairlines (greys derive at 8/14/40/64%) |
| ember | `#B3541E` | `#E0873F` | an agent did this |
| steel | `#2F6272` | `#6FA6B8` | instrument / frozen |
| rose | `#A6376A` | `#DF7BA4` | awaiting a human — never rendered as error |
| fault | `#8F2B2B` | `#D96C6C` | failures |

Type: identifiers (names, event types, selectors, cron, statuses, refs, diffs) in IBM Plex Mono;
content (rationales, prompts, descriptions, event text) in Instrument Sans; fonts bundled in
`examples/web` only (K5), `system-ui`/`ui-monospace` fallbacks so `web/` never depends on them.
Spine glyphs (closed set): filled disc = agent, hollow disc = human, diamond = awaiting human,
cross = failure, lock = freeze refusal.

---

## Wave 0 — unlock what exists (one item, merge first)

- [x] **V1 — Mount the built pages; fix the stale comment.**
  `examples/web/src/App.tsx`: extend the `View` union and `ViewNav` with **Events** →
  `<EventsPage projectId={project} onOpenSession={showSession} />` and **Automation** →
  `<AutomationPage projectId={project} workerOptions={...} />` (worker names via `useWorkers`).
  Keep `data-testid={`nav-${key}`}` naming. Do NOT pass `fetchConfigEvents` — the route exists.
  In `web/src/configLog.ts` and `web/src/components/ChangelogView.tsx`, rewrite the "THE ROUTE
  DOES NOT EXIST YET" header comments and the in-UI alert copy: `GET /agent/config-events` is
  mounted (`go/httpapi/config_events.go`); the injectable `ConfigEventFetcher` seam stays, now
  described as a host-override rather than a stopgap. No behaviour change in the library beyond
  the alert text.
  *Validation:* `cd web && npm ci && npm run typecheck && npm test` and
  `cd examples/web && yarn && yarn typecheck && yarn build`

## Wave 1 — theme + engine seams (all parallel; none touch the same files)

- [x] **TH1 — The theme and the fonts.** `examples/web/` only. Download Instrument Sans
  (400/500/600) and IBM Plex Mono (400/500) as woff2 into `examples/web/src/fonts/`; `@font-face`
  in a new `fonts.css` imported from `main.tsx`. Replace `createTheme()` with a themed
  `createTheme({...})` in a new `theme.ts`: palette from the token table above (light + dark via
  `useMediaQuery('(prefers-color-scheme: dark)')` and two theme objects), `typography` per the
  identifier/content rule (`fontFamily` = Instrument Sans stack; expose the mono stack as a theme
  extension for `sx` use), hairline `divider`, radius 2, and MUI component defaults kept quiet
  (no elevation on Paper, square chips). Semantic mapping: `success`→ember is WRONG — leave MUI
  severity colours alone; add the four named colours to `palette` as custom entries
  (`ember`, `steel`, `rose`, `fault`) with a module augmentation in `theme.ts`.
  *Validation:* `cd examples/web && yarn && yarn typecheck && yarn build`. Report (do not fix)
  any `web/` component whose hardcoded colour fights the theme.
- [x] **E1 — Attention read route (design B1).** `go/`: `GET /agent/attention-requests`, project
  from the JWT claim like every sibling route; query `?state=open|all` (default open — rows with
  `answered_at`=0 and `timed_out_at`=0), `?limit=`. Serve from the existing
  `Store.ListOpenAttentionRequests` (add a `ListAttentionRequests` variant for `all`). Response
  envelope `{"attention_requests": [...]}` matching the sibling routes' shape. Table tests +
  live-Postgres tests; adopt into `httpapi`'s route-listing pattern.
  *Validation:* `cd go && go build ./... && go vet ./... && go test ./...` and
  `AGENTKIT_TEST_POSTGRES_URL='postgres://postgres:test@localhost:5433/postgres?sslmode=disable' go test ./agentdb/... ./cmd/agentd/... ./httpapi/... -count=1`
- [x] **E2 — Memories read route (design B2).** `go/`: `GET /agent/memories?selector=&query=&limit=`
  over the existing memory search (§7.6 contract exactly — do not add knobs; selector errors
  return 400 with the parser's own message). Response `{"memories": [...]}` including labels and
  provenance fields. Postgres-only like the store itself: on a non-Postgres dialect return the
  same 501 posture as `/agent/project-token`. Tests as E1.
  *Validation:* as E1.
- [x] **E3 — Rationales on human mutations (design B3, decision K2).** Go: accept optional
  `rationale` in the request body of `PUT /agent/workers/{name}`, `POST/PUT /agent/subscriptions`,
  `PUT /agent/project-settings`, and as `?rationale=` on the three DELETEs — threaded into the
  existing `WithConfigEvent` write exactly as the schedules routes already do (copy that shape;
  no new config verbs, no schema change). Web: `WorkerEditor`, `SubscriptionEditor`,
  `ProjectSettingsPage` gain the same one-line "Why?" field `ScheduleEditor` already has, required
  non-empty on save (matching K2: edits carry a reason), threaded through `workers.ts` /
  `subscriptions.ts` / `projectSettings.ts` save paths and the hooks. Mind the NUL-byte trap in
  WorkerEditor and the PUT-carries-all-fields trap.
  *Validation:* both E1 commands AND `cd web && npm ci && npm run typecheck && npm test`
- [x] **E4 — Images and skills read routes (design B4).** `go/`: `GET /agent/images`,
  `GET /agent/skills` over the existing list store methods (200-newest cap, and the response says
  so, matching the MCP tools). Then `web/`: `WorkersPage` feeds `imageOptions` from the new route
  (a small `useImages` hook; the editor's field stops being blind free text but still accepts
  arbitrary refs — a registry reference is legal).
  *Validation:* as E3 (both suites).
- [x] **E5 — Server-side worker filter on sessions (design B5).** `go/`: `?worker=` on
  `GET /agent/sessions`. `web/`: `useWorkers.ts`'s `useWorkerJobs` uses it and drops the
  "filters one page client-side" caveat copy in `WorkerJobHistory`.
  *Validation:* as E3 (both suites).

## Wave 2 — the spine and the Desk (after V1 + TH1 + E1 merge)

- [x] **DK1 — Spine primitives + the Desk fold (pure).** `web/src/spine.tsx`: the rail + glyph
  components (closed glyph set from the token table; rail = 1px, glyphs on `background.paper` to
  mask it), presentational only. `web/src/desk.ts`: pure
  `buildDesk({deliveries, events, subscriptions, configEvents, attentionRequests, nowSeconds, lastSeenMs})`
  → `{asks, changes, trouble}` implementing design §5.2: asks = `awaiting_human` deliveries
  joined to open attention requests (a delivery whose request is no longer open drops out —
  the read-time answer to the parked-row wart); changes = config events newer than `lastSeenMs`,
  each carrying the existing `ChangelogEntry` diff machinery; trouble = failed deliveries grouped
  by worker, schedules with `provision_failures ≥ 5` (read from the schedules list), and
  `worker.freeze_refused` events. Every constructed sentence follows design §11 copy rules —
  including "No reason is recorded on a delivery row". Heavy vitest coverage in the existing
  table style; no fetch, no React state in `desk.ts`.
  *Validation:* `cd web && npm ci && npm run typecheck && npm test`
- [x] **DK2 — DeskPage + landing (decision K1).** `web/src/components/DeskPage.tsx` renders
  DK1's three stacks on the spine, with a `useDesk` hook (fetches via the existing hooks + a new
  `useAttentionRequests` against E1; renders without the route by omitting ask messages).
  `lastSeenMs` in `localStorage`, keyed by project. Empty-Desk state is the FIRST-RUN state:
  when the project has no workers it points at the topology flow ("Start from an org chart") and
  at Chat — never a bare "nothing to show". `examples/web/App.tsx`: `View` gains `"desk"`, it is
  the **default view**, nav order Desk · Chart(later) · Workers · Events · Automation · Settings,
  Desk badge = open asks count.
  *Validation:* DK1's command + `cd examples/web && yarn && yarn typecheck && yarn build`

## Wave 3 — lineage (after Wave 2 merges; touches WorkersPage)

- [x] **LN1 — Worker lineage tab + fold-to-version.** `WorkersPage` gains a **Lineage** tab
  (Configuration · Jobs · Lineage · Chat): the spine filtered to entity key `worker:<name>`,
  reusing `buildChangelog` + a new pure `workerLineage(entries, workerName)` in `configLog.ts`
  (version numbering = count of prompt-carrying events, oldest = v1; "n rewrites, m distinct"
  dedupes identical prompt texts). Each entry: actor (ember mark if `actor_worker`), rationale,
  diff (existing `DiffBlock`), links to the acting session. Fold-to-version: selecting a version
  shows that prompt read-only in the Configuration tab with the banner "Viewing v<k> as of
  <time> — this is history, not the live prompt" and a **Restore this version** action that
  pre-fills the editor with the old text and a rationale naming the config event id — an
  ordinary save, never a special route.
  *Validation:* `cd web && npm ci && npm run typecheck && npm test`

## Wave 4 — the chart (OC1 first; OC2/OC3 after it merges)

- [x] **OC1 — `layoutOrgChart`, pure (decision K6).** `web/src/orgchart.ts`:
  `layoutOrgChart(workers, subscriptions, schedules, recentEvents)` → typed
  `{nodes, wires, clocks, entryPips, width, height}` with numeric coordinates. Layered layout:
  rank = longest path from an entry pip (external event types seen in `recentEvents` plus
  subscription event types with no producing worker), order within rank alphabetically then
  crossing-minimised by one barycenter pass, orthogonal wire routes, ALL tie-breaks by name so
  output is deterministic (pin by test: same input → deep-equal output). Cycles must not hang:
  break at the lexicographically-smallest back edge and mark it `back: true`. No React, no DOM,
  no randomness. Table tests over the 13 seed shapes' worker/subscription rows (hand-declared
  fixtures — do NOT import from `go/`).
  *Validation:* `cd web && npm ci && npm run typecheck && npm test`
- [x] **OC2 — OrgChartPage, read-only, + propagation.** `web/src/components/OrgChartPage.tsx`
  renders OC1's output as SVG in the design's schematic style (§6.1: name plates 1px-ruled, mono
  names, prose descriptions, event types riding wires, frozen = double rule in steel + lock,
  schedule dials, state line `● running n/max` from live deliveries). No stored positions
  (design §6.3); pan/zoom in component state only. **Propagation panel** (design §6.5): select
  an entry pip or paste an event → chain the existing `matchSubscriptions` hop by hop against
  the depth ruler 0…8 with the stop line at 8; the not-modelled caveat line (rate limits,
  max_instances, budget) rendered once, verbatim. `examples/web`: `View` gains `"chart"`.
  *Validation:* OC1's command + `cd examples/web && yarn && yarn typecheck && yarn build`
- [ ] **OC3 — Conventions overlay (decision K4).** Pure `inferConventions(workers)` in
  `orgchart.ts`: scans each worker's `system_prompt` for (a) other workers' exact names and
  (b) `ROUTE-TO:` lines; emits dashed edges each carrying the matched prompt line verbatim.
  Rendered by OrgChartPage behind an **off-by-default** toggle labelled "Show conventions";
  every dashed edge's tooltip quotes its source line plus "convention — written in a prompt,
  not enforced by the engine". Substring hygiene: match on word boundaries so `keeper` never
  matches `book-keeper` (this exact trap is in the mock-script lore).
  *Validation:* `cd web && npm ci && npm run typecheck && npm test`

## Wave 5 — direct manipulation (after OC2 + E3 merge)

- [ ] **OC4 — Wire + freeze on the canvas (decision K3).** Drag from node A to node B opens the
  proposal card (design §6.4): sentence-form summary, exact `event_type`/`filter` fields shown in
  mono, **required** "Why are you wiring this?" — submitted through `useSubscriptions.save` with
  the E3 rationale, never a canvas-only path. Clicking a wire offers "Stop waking <worker> when
  <event>" (delete + rationale; wording per design rule: never "undo"). Node toggles for
  enabled and frozen through the workers save path (read-modify-write ALL fields — the thawing
  trap). Schedules are NOT editable here (K3): clock clicks deep-link to Automation. Keyboard
  path for every gesture (a wire can be created from a node's menu, not only by drag).
  *Validation:* `cd web && npm ci && npm run typecheck && npm test`

## Wave 6 — learning surfaces (parallel; independent files)

- [x] **BA1 — Before / after.** From a lineage entry or a Desk change: three columns —
  the subject worker's last `worker.finished` transcript before the rewrite, the rewrite
  (actor, rationale, diff), the first after. Pure `beforeAfter(configEvent, events)` selector in
  a new `web/src/learning.ts` (join on envelope `worker` + timestamps; return nulls when a side
  hasn't happened yet and render "no job has run since"). Caveat line rendered, verbatim:
  tool calls are absent from these transcripts — this shows what the worker said, never what it
  did. Component `BeforeAfterView.tsx`, mounted from LN1's entries.
  *Validation:* `cd web && npm ci && npm run typecheck && npm test`
- [x] **BR1 — Bench report viewer.** `web/src/components/BenchReportView.tsx` +
  pure `parseBenchReport(json)` in `learning.ts` accepting the comparison rig's `report.json`
  shape (fixture: copy `e2e/experiments/reports/actor-critic-vs-sham-vs-solo.report.json` into
  the test as a fixture file — do not import across packages, do not touch `e2e/experiments/`).
  File-drop input; no backend. Editorial rules are hard requirements (design §7.3): the Tier A
  banner is not dismissable; `prompt_writes` renders in a muted column labelled "churn", never
  first, never sortable-to-first by default; spread always shown; identical-rewrite dedupe noted.
  Mounted as a tab on EventsPage or its own view — orchestrator's call at merge.
  *Validation:* `cd web && npm ci && npm run typecheck && npm test`

## Wave 7 — memory browser (after E2 merges)

- [ ] **MB1 — Memory browser + briefing preview.** `web/src/memories.ts` (pure: selector-string
  builder/parser mirroring the K8s grammar the engine enforces — equality, `!=`, `in`, `exists`,
  `!`, comma-AND, NO or; invalid clause errors phrased like the parser's) + `useMemories` +
  `MemoryBrowserPage.tsx`: selector bar as chips, newest-first, RRF note when a text query is
  present ("a low score means nothing good, not no match"), the semantic-leg-off notice when
  applicable, `name=` convention rendered current-value-first with superseded folded beneath,
  provenance link per row. **Briefing preview** on the worker page (LN1's tab bar gains nothing;
  it goes in Configuration): for each of the worker's briefing selectors plus the default
  `kind=rolling-summary, worker=<name>`, fetch the newest match via E2 and render it at
  `briefing_max_bytes` with the truncation marker where it would fall. `examples/web`: `View`
  gains `"memory"`.
  *Validation:* `cd web && npm ci && npm run typecheck && npm test` +
  `cd examples/web && yarn && yarn typecheck && yarn build`

## Orchestrator checklist (not executor work)

- After each wave that touches `examples/web`: `docker compose up -d --build web`, then a
  Playwright screenshot pass (the shoot-script pattern from the design session: light + dark +
  390px, per-section) and a visual check against the deck before the next wave starts.
- Directory contract for parallel executors: Wave 1 items are disjoint by construction; in
  Wave 6, `learning.ts` is shared between BA1 and BR1 — BA1 creates it, BR1 rebases on BA1's
  merge (or the orchestrator merges BA1 first; stated in both briefs).
- After the final wave: sweep `describeDeliveryStatus`/`deliveryStatusSeverity` copy against the
  design's rose-not-warning rule for `awaiting_human`, and re-run the full matrix:
  `cd go && go build ./... && go vet ./... && go test ./...`,
  `cd web && npm ci && npm run typecheck && npm test`,
  `cd examples/web && yarn && yarn typecheck && yarn build`,
  plus the live-Postgres command from E1.
- e2e stack specs for V1/DK2/OC2 (nav reachable, desk renders, chart renders) belong in
  `e2e/features/` and run under `./e2e/run-stack-e2e.sh` — orchestrator-run, serial, AFTER the
  waves merge; not part of any executor item.

## Decisions carried in (from the design doc — do not relitigate)

- **K1** Desk is the landing view; its empty state is the first-run state.
- **K2** Human edits carry a required one-line reason (E3).
- **K3** Canvas edits = wire + freeze only; schedules stay on the row.
- **K4** Conventions overlay ships opt-in, labelled, quoting its source line.
- **K5** Instrument Sans + IBM Plex Mono bundled in `examples/web` only.
- **K6** The chart lives in `web/` from the start; layout is a pure, deterministic module.

## Discovered Issues Log

*(orchestrator-owned; executors report surprises in their final reports)*

- **(all Wave 0+1) Workflow worktrees come up on a stale `wip` base** — the doc-13 trap, now
  confirmed for workflow-created worktrees too: all seven came up on `dc49595 "wip"` (pre-product-
  layer). The first run's grounding check stopped every executor cleanly; the fix is a mandatory
  brief step: if the plan file is absent, `git reset --hard <base-sha>` your own worktree branch.
  Every later wave's brief must carry the current base sha.
- **(TH1) `git stash` is SHARED across worktrees** — TH1's stash pop returned V1's files; V1's
  work was recovered from the stash and both commits verified clean. New rule for every brief:
  executors never run `git stash`.
- **(orchestrator) Never `--amend` on the shared branch** — another session committed in the
  window between a commit and its amend, and the amend rewrote *their* commit; repaired from the
  reflog (`git reset --hard` to their original). Corollary: no `git add -A` at the repo root
  either — it swept another agent's untracked doc into the orchestrator commit in the first
  place. Stage explicit paths only.
- **(V1/TH1) `cd examples/web && yarn typecheck` was red at the clean base** — 34 unused-local
  errors: examples/web's tsconfig checks web/src through the alias with `noUnusedLocals`, which
  `web/`'s own typecheck does not. Fixed by the orchestrator (mechanical unused-import removals,
  WorkerEditor edited bytes-safe). The verbatim validation now passes for future shell items.
- **(V1) The ChangelogView unavailable-alert copy is pinned** by EventsPage.test.tsx (`/does not
  serve it yet/i`) — reword around that phrase or update the test with it.
- **(V1) Nav is now six buttons at 280px** (七 with Chart) — flex:1 each; check for squash in the
  Wave 2 screenshot pass. Two more stale route comments remain (EventsPage.tsx prop doc,
  useConfigLog.ts field doc) — trivial follow-up.
- **(TH1) Instrument Sans ships as ONE variable woff2 (400–600)**, not three statics; IBM Plex
  Mono is two statics. Latin subsets, 59.7 kB total. `theme.monoFontFamily` is the mono stack
  for `sx` use; ember/steel/rose/fault are palette entries via module augmentation.
- **(TH1) 18 chat-side web/ components carry hardcoded light-mode colours** that will fight the
  dark theme (worst: `rgba(0,0,0,0.06)` hairlines, ArtifactTreeView's status dots). The
  product-layer pages are clean. Not fixed — needs its own sweep item if dark mode matters soon.
- **(E1) `ListOpenAttentionRequests` had no limit** — new query struct keeps Limit=0 = unlimited
  to preserve the helper's semantics; >0 goes through clampLimit (cap 1000).
- **(E2) httpapi gained an optional `MemoryEmbedder` seam** (nil = keyword+recency degrade,
  same posture as the MCP read path) rather than importing extension/embedding — one small seam
  beyond the item text, wired in cmd/agentd via EmbedOrDegrade.
- **(E3) "The three DELETEs" resolved to workers/subscriptions/SCHEDULES** (project-settings has
  no DELETE); the schedule DELETE previously read no rationale at all and useSchedules.remove
  documented the refusal — both fixed, comment rewritten. Label inconsistency left standing:
  ScheduleEditor says "Rationale" (optional), the three new fields say "Why?" (required).
  `withRationale` lives in configApi.ts, deliberately not exported (that file's stated contract
  beats the index.ts rule).
- **(E4) Catalogue timestamps are unix SECONDS** (unlike config events' ms) — UI formatting must
  not assume ms. `GET /agent/skills?label_selector=` works on sqlite; `GET /agent/images` with a
  selector 400s there (jsonb selectors are Postgres-only; mirrors the store).
- **(E5) `useWorkerJobs` keeps its client-side filter as a guard** for hosts overriding the
  endpoint; `truncated` now means "this worker alone has ≥ limit jobs".
- **(orchestrator) The httpapi route-registration blocks (Config/New/Endpoints/Mux) conflicted
  in every pairwise merge** — union resolution each time; gofmt+build+vet before committing the
  resolution. Expected again for any future route.
- **(DK1) `worker.freeze_refused` does not name the DEFENDED worker in a field** — the envelope
  carries the attacker; the target is only in the prose text. `desk.ts` parses it
  (`frozenTargetFromText`); if the event ever gains a target field, delete that regex.
- **(DK1) `web/`'s Schedule type lacked the runtime-state fields** — `provision_failures` /
  `last_provision_error` added as optionals to coerceSchedule so the Desk's halted-schedule line
  can exist. `buildDesk` takes optional `schedules` + `projectId` beyond the stated signature.
- **(DK2) The shell's Desk badge counts OPEN ATTENTION REQUESTS**, a superset of asks (an ask
  additionally needs a parked delivery) — App.tsx calls useAttentionRequests directly rather
  than lifting the whole fold. Flagged as approximate, arguably truer; revisit if it confuses.
- **(DK2) The first-run "Start from an org chart" button routes to Workers, not into the flow**
  — WorkersPage's `#topology` sentinel is module-private. Follow-up: export it or add a
  `startInTopology` prop.
- **(DK2/spine) `web/` resolves ember/steel/rose/fault from the host theme when present and
  falls back to the design tokens** — the library never imports examples/web's augmentation.
- **(LN1) `DiffBlock` was private in ChangelogView** — now exported (item said "the existing
  DiffBlock"). `WorkerEditor` gained `initialRationale` (seeds the Why? field and dirty=true)
  so Restore can pre-fill; NUL-binary diff, change described in the commit message.
- **(LN1) index.ts name collision**: the component `WorkerLineage` vs the pure result type —
  the type ships as `WorkerLineageData`.
- **(orchestrator) Third stale route comment found** (web/src/index.ts config-log export block),
  joining EventsPage.tsx and useConfigLog.ts. Sweep all three in some later item.
- **(orchestrator) Wave 2+3 merged conflict-free**; web 607→673 tests. Screenshot pass still
  deferred — the e2e stack (another session's) holds port 8080.
- **(OC1) Event "producers" are inferred, documented, exported** (`subscriptionProducers`):
  `filter.worker` naming a real worker wins; an UNFILTERED `worker.finished`/`worker.failed`
  subscription fans in from every worker (and self-edges its own subscriber — a legal hand-wired
  cycle); anything else is an entry pip. All 13 seeds use the first case.
- **(OC1) Silent drops, deliberate**: a subscription targeting a nonexistent worker and a
  schedule pointing at a missing worker draw nothing — a real config problem the chart stays
  silent about; candidates for a Desk trouble line, not a chart line.
- **(OC1) `*/` inside a block comment**: documenting cron `*` syntax closed the comment and
  produced 20 misleading tsc errors. Prose only when documenting cron in TS comments.
- **(OC2) The running-count join goes delivery→subscription→worker** (`buildJobRows`), so a
  delivery whose subscription aged out of the 100-row page is invisible to `n/max`. Not claimed.
- **(OC2) Nav is now SEVEN buttons at 280px** — the screenshot pass is overdue and blocking on
  the shared stack; check squash before calling the shell done.
- **(OC2) The chart is a dead end until OC4** (no click-through to workers/Automation) — scope
  discipline, K3 owns actions.
- **(BA1) Config events are ms, project events are SECONDS** — `learning.ts` exports `eventMs()`
  and a regression test; ANY future join of the config log against events hits this.
- **(BA1) The subject worker is `configEntity(ev).name`, never `actor_worker`** — the reviewer
  rewrites the answerer; using the actor shows the wrong worker's transcripts. Pinned by test.
- **(BR1) The churn rule is enforced in the DATA**: `prompt_writes` is stripped from
  metricColumns and returned only as a separate `churn` field — no view or future sort can rank
  by it. Fixture proves the design's point: 4 writes, 1 distinct.
- **(BR1) `web/src/__fixtures__/` is a new convention** (first JSON fixture; resolveJsonModule
  already on). Bench mounts as a fifth EventsPage tab behind `enableBench` (default true).
- **(orchestrator) Wave 4+6 merged conflict-free**; web 673→874 tests (144 of them the layout
  module alone).
