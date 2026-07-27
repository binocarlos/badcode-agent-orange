# Work plan — self-improvement workstream

*Created 2026-07-27. Executes [`12-composition-playbook.md`](./12-composition-playbook.md) §3.
Format and rules follow [`06-work-plan.md`](./06-work-plan.md), which carried 37/37 items.*

## EXECUTION RULES

1. **Executors work in isolated worktrees**, one per item (or item group as marked). They do NOT
   edit this file — the orchestrator owns it. Surprises are reported as bullets in the final
   report, for the orchestrator to file in the Discovered Issues Log below.
2. **Run the item's validation commands verbatim before claiming done.** The orchestrator re-runs
   them after merge. "Done" without a green validation run is not done.
3. **Merges are sequential**; the orchestrator resolves conflicts and re-validates after each.
4. **The compose stack is a serial resource.** `./e2e/run-stack-e2e.sh` restarts agentd; only one
   validation run at a time, coordinated by the orchestrator. Executors may develop specs without
   the stack and must say clearly if they could not run them.
5. **Do not touch:** `.env` (holds real credentials), the shared test Postgres container
   `ao-test-pg` on `localhost:5433` (use it, never delete/recreate it), production seeding, and
   the real Anthropic API (mock mode only unless an item says otherwise).

## Standing traps (scar tissue — read before coding)

- **gorm `default:` tags**: NEVER on a column where zero/false is meaningful (GORM omits
  zero values when a default is declared). DDL defaults go in migration SQL; defaulting in
  `normalize()`/validate. See `Worker.Enabled` for the pattern.
- **Mock-script body-match leak** (`11-learning-stories.md` §3): the rule match runs against the
  whole request body, which replays prior turns — a marker emitted in a tool call matches later
  requests. Partition rules by worker name first; split before/after with `absent`; distinct
  non-substring worker names.
- **Contamination flows actor→critic through the transcript** (H0): `worker.finished` event text
  is the actor's ENTIRE transcript, so the critic's first user message contains the actor's name
  and full output. The critic's rules must sit ABOVE the actor's in the table — name partitioning
  alone is not enough; rule order carries it. (Flow is one-directional; the actor never sees the
  critic's text.)
- **A critic left subscribed re-fires its script on later rounds** (H0): a fresh critic session
  starts at turn 0 and serves the same scripted tool_use again, double-writing the rewrite.
  Retire the critic's subscription once its round settles, or give each round a differentiable
  critic rule. Bites any story counting config-log entries (S6, S8, S9).
- **Assert on happens-after signals, never sleeps.** Poll the delivery/event/config-log record.
- **Port pool**: every job holds a container + host port (ceiling 100). Delete sessions in
  teardown; `./e2e/run-stack-e2e.sh clean` clears leftovers.
- **The stack serves a BUILT web image** — UI edits are invisible until
  `docker compose up -d --build web`.
- **Migrations**: append to `agentMigrations`, next free number (check on merge — sibling items
  may claim the same number; orchestrator renumbers on conflict).
- **Where vs when to assert**: reading back a stored value proves storage, not delivery. Behaviour
  switches prove delivery.

---

## Wave 1 — Learning stories (playbook P.5)

*Goal: the deterministic gate exists and is green offline. No schema changes, no tokens.*
*Spec: [`11-learning-stories.md`](./11-learning-stories.md). Model: `go/modelproxy/script.go`.*
*Pattern to build on: `e2e/features/acceptance-loop.spec.ts` (seeds a §8.7 org already).*

- [x] **H0 — Harness + control + canonical story.** One composite mock script
  `e2e/mock-scripts/learning-stories.json` (worker-name-partitioned rule blocks); spec file
  `e2e/features/learning-stories.stack.spec.ts` with shared helpers (seed actor+critic+
  subscriptions into a run-scoped project; drive rounds by emitting events via `POST
  /agent/events` — never cron; await delivery outcomes). Implements **MR-3 (no ghost learning)**:
  critic disabled → actor behaviour byte-identical across rounds; and **S1 (missing title)**:
  round 0 lacks `Title:`, critic rewrites via `worker_prompt_write`, round 1 has it, config log
  holds the rewrite + rationale. File header carries the doc-11 §2 caveat: proves transmission,
  not discovery.
  *Validation:* `./e2e/run-stack-e2e.sh up mock` then
  `./e2e/run-stack-e2e.sh test mock --mock-script e2e/mock-scripts/learning-stories.json -- e2e/features/learning-stories.stack.spec.ts`
- [x] **S2–S9 — The remaining stories**, added to the same spec + script file, one describe block
  each, isolated run-scoped projects, distinct worker names per story:
  - **S2** forgotten sign-off; **S3** missing units (doc 11 §4 rows 2–3).
  - **S4** the unasked question — round 1 calls `request_human_attention`, delivery parks at
    `awaiting_human` (non-text observable).
  - **S5** planted null — round 1 reports "no significant effect".
  - **S6** no-regression (**MR-2**): second rewrite; S1's property still holds.
  - **S8** lineage: three rewrites → config log has exactly three, ordered, each with rationale;
    folding to round *k* reproduces round *k*'s prompt.
  - **S9** capstone: three cumulative improvements (title, sign-off, units) all hold
    simultaneously in the final round.
  *Validation:* same command as H0 (whole spec file).

## Wave 2 — Frozen workers (playbook P0) — runs in parallel with Wave 1

*Spec: [`10-topology-library.md`](./10-topology-library.md) §3. Touches `go/` + `web/` only — no
overlap with Wave 1's `e2e/` files.*

- [x] **F1 — Engine.** `Worker.Frozen bool` (NO gorm default tag), migration (next free number),
  freeze/unfreeze via the JWT-guarded HTTP API (config-logged through `WithConfigEvent`;
  `TestMutationsAreLogged` must adopt the new mutation). The core MCP server refuses
  `worker_prompt_write` / `worker_update` / `worker_delete` against a frozen worker with an error
  saying why; each refusal emits a project event `worker.freeze_refused` (C8: refusals are
  signals). Table tests + live-Postgres tests.
  *Validation:* `cd go && go build ./... && go vet ./... && go test ./...` and
  `AGENTKIT_TEST_POSTGRES_URL=postgres://postgres:test@localhost:5433/postgres?sslmode=disable go test ./agentdb/... ./cmd/agentd/... ./httpapi/... -count=1`
- [x] **F2 — UI.** Lock badge on frozen workers ("Frozen — cannot be changed by other workers"),
  freeze/unfreeze control on the worker settings page, changelog renders freeze/unfreeze events.
  *Validation:* `cd web && npm ci && npm run typecheck && npm test`
- [x] **S7 — Frozen-scorer story** (after F1+F2 merge + stack rebuild): critic attempts to rewrite
  a frozen worker; refused; prompt byte-identical; `worker.freeze_refused` event recorded.
  *Validation:* Wave 1's command (spec file includes S7).

## Wave 3 — Topology as data + first seeds (playbook P1–P2)

*Unlocked by D1 (built-in curated) and D2 (reference assets by name). Engine items start after
Wave 2 merges (shared files: `cmd/agentd`, `agentdb`, `web`).*

- [x] **T1 — Built-in topology registry + renderer.** Code-defined, versioned topologies (D1): a
  topology = name, version, description, question list (id, prompt, type, default), and a pure
  `Render(answers) → Bundle` where Bundle is rows of the EXISTING config types (workers,
  subscriptions, schedules, project-settings patch, memory seeds). Renderer is pure and
  table-tested; no I/O. Referenced images/skills are names only (D2) — rendering records them as
  preconditions, it does not create them.
  *Validation:* `cd go && go build ./... && go vet ./... && go test ./...`
- [x] **T2 — Preview + apply.** HTTP (JWT path): preview returns the bundle plus a diff against
  the project's current config and the unmet preconditions (missing images/skills) — applying
  with unmet preconditions fails loudly and changes nothing. Apply writes every row through the
  existing store mutations (each config-logged via `WithConfigEvent`), bracketed by a
  `topology.applied` config event naming topology@version and the answers. No new write paths.
  *Validation:* F1's go + live-Postgres commands.
- [x] **T3 — UI flow.** Empty-project state offers "start from a topology": pick → answer
  questions → preview diff → apply. Changelog renders `topology.applied`.
  *Validation:* `cd web && npm ci && npm run typecheck && npm test`
- [x] **T4–T7 — First four seeds**: Solo (control 1), Actor–Critic (4), Supervisor (5),
  Frozen-scorer harness (12; needs F1's `Frozen`). Each seed ships with a stack e2e proving:
  apply succeeds, the org chart matches the preview, and one round runs in mock mode.
  *Validation:* Wave 1's stack command plus a `topologies.stack.spec.ts`.

## Wave 4 — Hypothesis lab + calibration (playbook P3)

*Wave 3 done. L1/L2 are offline (mock) and proceed; L3's EXECUTION is the hard pause — no
real-model run without Kai's explicit go (credential mode is also his call: api-key spend vs
subscription-OAuth terms, AGENTS_RESEARCH §1).*

- [x] **L1 — Synthetic dataset generator + trap taxonomy.** Deterministic given a seed: datasets
  (CSV/JSON) + a held-out ground-truth answer per hypothesis. Traps: planted nulls (no effect),
  confounds (naive correlation says yes, controlled analysis says no), underpowered samples.
  Unit-tested properties: same seed → same bytes; traps actually trap (a naive estimator run on
  the generated data reaches the wrong conclusion; a correct one doesn't).
  *Validation:* whatever suite hosts it (go test or vitest), green + deterministic.
- [x] **L2 — hypothesis-lab@v1 topology seed** (catalogue entry 13): investigator +
  methodology-critic (holds worker_prompt_write on the investigator) + FROZEN fact-checker whose
  prompt says it compares conclusions against held-out truth it is given (never generates it).
  Ground truth lives outside the project (harness-side), per AGENTS_RESEARCH §4. Mock e2e in
  topologies.stack.spec.ts style: apply, one investigation round, critic rewrite lands, frozen
  boundary holds.
  *Validation:* go suite + the topologies stack command + learning-stories regression.
- [x] **L3 — Calibration runbook** — written: [`14-calibration-runbook.md`](./14-calibration-runbook.md).
  **EXECUTION remains GATED on Kai** (credential mode + token ceiling are the go-decision).** The Tier B protocol
  (AGENTS_RESEARCH §7) specialised to the lab: N hypotheses with known answers, accuracy on late
  vs early hypotheses, planted-null false-confirmation rate, run recording. Writing the runbook
  is unblocked; RUNNING it against a real model is not.

## Wave 5 — Remaining seeds, comparison rig, Tier B build (playbook P4, P5, P7)

*Onboarding (P6) shipped as T3. D3 decided (uncapped; e2e uses a narrowed port pool).*

- [x] **R1 — Control + spine seeds**: `solo-memory@v1` (control 2 — solo plus memory
  write/briefing), `sham-critic@v1` (control 3 — critic whose rewrites shuffle instruction order,
  rationale says so honestly), `assembly-line@v1` (entry 6 — chain via worker.finished
  subscriptions), `blackboard@v1` (entry 8 — N workers sharing labelled memory, no addressing).
  Each: go/topology definition + table tests + one e2e (apply, org matches preview, one mock
  round) in topologies.stack.spec.ts.
  *Validation:* go suite + both stack commands (topologies + learning-stories regression).
- [ ] **R2 — Remaining work seeds**: `debate@v1` (entry 7 — N debaters subscribe to the same
  event, aggregator judges), `self-organizing@v1` (entry 9 — workers hold worker_create;
  UNCAPPED per D3; its e2e runs with `--port-pool` narrowed), `temporal-hierarchy@v1` (entry 10 —
  strategist on a slow schedule rewrites operator prompts), `escalation@v1` (entry 11 — worker +
  request_human_attention + attention channel).
  *Validation:* as R1.
- [ ] **C1 — Comparison rig**: harness script (e2e/experiments/) that runs ONE task through N
  topologies × M seeds in mock, collects per-arm outcome tables from the event/config logs, and
  emits a ranked report with variance. Deterministic in mock; the same rig drives the L3
  calibration when ungated.
  *Validation:* rig runs green in mock against ≥3 seeded topologies; report artifact committed.
- [ ] **B1 — Tier B graded-harness build** (AGENTS_RESEARCH §7): same-stories runner with a
  grader seam (blind, shuffled, ranked, anchor items); offline test with a scripted grader.
  EXECUTION against real models gated with L3.
  *Validation:* offline harness tests green.

## Decisions (D1–D4) — DECIDED by Kai, 2026-07-27

- **D1: built-in curated set first.** Authorable/shareable topologies deferred; the registry is
  code-defined and versioned so authorability can be added later without rework.
- **D2: reference assets by name.** A topology names images/skills as preconditions; applying
  with unmet preconditions fails loudly. It never creates catalogue entries.
- **D3: UNCAPPED self-organizing pool.** Topology 9 relies on the existing project-level brakes
  only (`MaxConcurrentJobs`, daily token caps) — most faithful to the research question, highest
  runaway risk, accepted. Note: the Wave 4 hard pause before real-model spend still applies; in
  mock mode a runaway costs containers/ports, not money — seed its e2e with a narrowed port pool.
- **D4: plain JWT unfreezes.** Unfreeze is an ordinary config mutation; the config log already
  records who and when. No extra ceremony.

## Discovered Issues Log

- **The e2e rig already had the mock-script affordance this plan needs** —
  `run-stack-e2e.sh test --mock-script FILE` loads a script for one run and restores the plain
  model after, refusing malformed scripts loudly. Wave 1 needs no runner changes.
- **The scheduler already fires through the event spine** (`CreateProjectEvent` + shared dispatch
  gate), so simulated time needed nothing built. Recorded as standing rule C6.
- (H0) **`e2e/mock-scripts/README.md` contradicted the mechanism the suite runs on** — it still
  said system-prompt markers never match, but the 2026-07-26 fix-prompt repair resolved that, and
  S1's round-1 switch depends on exactly that matching. README fixed in tree; the green run is
  the proof.
- (H0) **Transcript contamination and critic re-firing** — promoted to Standing traps above.
- (H0) **Scriptless runs skip cleanly** — the suite gates on `STACK_MOCK_SCRIPT`, so an ordinary
  `test mock` run reports 2 skipped and stays green; the stories cost nothing when not asked for.
- (F1) **There is no `worker_delete` MCP tool to refuse** — §9 never gave workers one (the header
  says so); deletion is JWT-only. The refusal matrix is worker_update + worker_prompt_write, and
  the retire path a worker actually holds (`worker_update {enabled:false}`) IS refused when
  frozen. S7 must not assert a delete refusal.
- (F1) **`worker_create` needed no frozen check** — its unconditional name-collision refusal
  ("hiring is not overwriting") already protects frozen workers; pinned by test.
- (F1) **Enforcement is MCP-seam-only, deliberately** — store and `SetWorkerPrompt` stay
  permissive because the human HTTP path shares them; a store-level check would block humans too.
  `worker_prompt_write` gained a read-before-write it previously lacked.
- (F1) **`worker.freeze_refused` emission is best-effort** — an event-store failure logs and the
  refusal still stands; a failed signal must not become a successful write. Pinned by test.
- (F1) **A write flipping both `enabled` and `frozen` logs as plain `worker_update`** — neither
  verb alone would be honest; pinned in the adoption table test.
- (F2) **`web/` pins the action vocabulary by count** (`toHaveLength`, now 18) — every future
  config verb must touch configLog.test.ts, `entityKindForAction`, `configChangePhrase` (fixture
  per action) and `configLog.ts`.
- (F2) **`WorkerEditor.tsx` contains literal NUL bytes** (pre-existing) — `'\x00new'`/`'\x00none'`
  sentinels make git treat it as binary, so its diffs are invisible. Normalise someday.
- (S2–S9) **Critic-rule differentiators must anchor on the actor's OUTPUT, not the property
  string** — the rewrite's own tool_use input contains the property, so an `absent` keyed on it
  flips mid-session between the critic's turn 0 and turn 1. Key on strings only the improved
  output contains. This is the working recipe for multi-firing critics (S6/S8/S9 fire 2–3×).
- (S2–S9) **`envelope.session_id` is the join key for multi-round stories** — "first
  worker.finished for worker X" goes ambiguous past one round.
- (S2–S9) **`composed_prompt` turns S8's fold into a delivery assertion** — round k's composed
  prompt contains rewrite k's payload, proving the log reproduces what the job ran with, not what
  the worker row says now.
- (S2–S9 + T1 + F1) **Executor worktrees keep coming up on stale wip bases** — three agents in a
  row found their assigned worktree branch NOT on product-layer and had to branch explicitly. The
  "verify your base first" instruction is now mandatory boilerplate in every executor brief.
- (T1) **Purity forced project-agnostic rows**: rendered Worker/Subscription/Schedule/Memory rows
  leave Project/ID/timestamps zero; T2's apply stamps them. Pinned by
  `TestRegisteredBundlesAreProjectAgnostic`, which iterates every registered topology — future
  seeds inherit the check for free, as does render-time cron validation via `agentdb.ParseCron`
  (a rendered cron can never be refused at apply time).
- (T1) **`SettingsPatch` is zero-means-keep, but `PutProjectSettings` is whole-object** — T2 must
  read-current → overlay non-zero → write whole. Corollary: zero-is-meaningful settings
  (`daily_tokens_*`, `snapshot_ttl_days`) are unreachable through this patch shape; no current
  seed needs them, but it is a real limit.
- (T2) **The action verb is `topology_apply`, not the plan's `topology.applied`** — dots name
  routable events; §15.3 vocabulary is uniformly `entity_verb`. T3's changelog work is already
  done in T2's commit (19-action pin, entity kind `topology`, filter preset).
- (T2) **One-transaction apply via a tx-bound Store clone**: every existing mutation nests as a
  gorm SAVEPOINT, so rows go through the unmodified store methods — no new write path. The
  `topology_apply` bracket is written LAST (highest seq proves every row event landed). The real
  hazard was post-commit hooks firing on savepoint release — solved by collecting during the tx
  and replaying after commit; a refused apply emits zero hooks. Fault-injection pins rollback.
- (T2) **Preview writes nothing, pinned structurally** — the seam's only mutating method is never
  called; apply refusals are 409 with in-tx authoritative re-checks for the race window.
- (T3) **No examples/web changes needed** — WorkersPage is already the shell's workers view, so
  the onboarding flow ships through the existing mount (`#topology` sentinel, same trick as
  `#new`). Empty projects get a prominent "Start from a topology" panel.
- (T3) **`coerceSchedule(raw, project?)` mistypes under `array.map`** (index lands in `project`)
  — needs a lambda; same footgun for `coerceWorker`. Worth a sweep someday.
- (T3) **A broken preview can never wave an apply through** — absent/garbled `applicable` coerces
  to `false` deliberately; bool questions always carry an explicit value ("optional bool left
  unanswered" is unrepresentable from the form, documented in topologies.ts).
- (S7) **The frozen flag alone differentiates the critic's two firings** — both serve the
  identical scripted tool call; frozen decides its fate. The refusal round doubles as proof the
  MCP `isError` path does not wedge a scripted session.
- (S7) **`toggleWorkerEnabled` in e2e/helpers/api.ts was silently thawing** — its
  read-modify-write body omitted `frozen`, so any enable/disable toggle on a frozen worker would
  have unfrozen it (PUT replaces). Fixed in passing; nothing was green-by-accident since no
  earlier test froze workers. The generalisation: every read-modify-write helper must carry ALL
  fields, and each new worker field must sweep the helpers.
- (S7) **A human edit logs with empty `actor_worker`** — asserted, contrasting with the critic's
  rewrite record naming the actor. The config log distinguishes people from workers for free.
- (T4–T7) **Workers cannot emit typed events — the supervisor catalogue row is unimplementable
  as written.** The only routable worker output is `worker.finished` (whole transcript), so the
  seed renders honestly: specialists subscribe to the dispatcher's finishes and addressing is a
  `ROUTE-TO: <name>` transcript line, with the limit stated in the prompts. A real `event_emit`
  core MCP tool is the future fix; doc 10's entry 5 needs a footnote.
- (T4–T7) **Mutual name contamination defeats rule ordering** (supervisor: roster names
  specialists, specialists name the dispatcher) — key rules on the renderer-guaranteed identity
  phrase `You are <name>,` instead. New variant of the body-match trap.
- (T4–T7) **The mock-script naming trap is now enforced at render time** — `checkSeedWorkerNames`
  refuses duplicate/substring worker names in answers, and supervisor refuses `worker.*` inbound
  events (a dispatcher subscribed to worker.finished would be woken by its own specialists).
- (T4–T7) **A schedule-only seed cannot be driven on demand** (solo): its e2e adds a poke
  subscription after apply. Any future seed whose only clock is cron inherits this pattern.
- (T4–T7) **frozen-scorer@v1 differs from actor-critic@v1 only by the instrument** — the critic
  prompt is byte-identical (pinned by test), so any behavioural difference between the two
  topologies in an experiment is attributable to the scorer's presence. The comparison-rig
  property, established by construction.
- (L1) **Verdict is a separate return, never a Dataset field** — no rendering of a dataset can
  leak the answer; pinned by `TestDatasetBytesCarryNoVerdict`. The bundle likewise carries no
  truth channel (3 workers, 3 subscriptions, nothing else).
- (L1) **math/rand is not a determinism guarantee across Go releases** — hypolab carries its own
  splitmix64 with golden-byte tests enforcing the decision.
- (L1) **A small fixture can fail to carry its own trap** — the first N=40 confound sample had
  naive NOT significant; settled on N=120/seed 13 (naive z=+3.65 confirms, controlled z=−0.48
  nulls), with `TestE2EFixtureBytes` pinning the e2e CSV to generator output AND re-proving the
  trap property on those exact bytes. Trap-property tests run on pinned documented seeds, not
  "any seed" — the honest instrument shows its α (planted-null seed 9 hits z=+2.87).
- (L2) **A scripted tool call is a contamination channel to its TARGET** — the critic's freeze
  attempt puts the checker's name in the critic's later request bodies, so checker rules key on
  the identity phrase and the critic prompt deliberately never names the checker (pinned by
  test). Generalises T6's trick to any worker whose tool call names another.
- (R1) **The memory tool is `memory_create`** (not `memory_append`); blank label values are legal
  in K8s label grammar, so memory seeds refuse blank at render time.
- (R1) **There is no memories HTTP API** — nothing under `/agent/*` lists memories. Tests prove
  memory writes via the next round's briefing (composed_prompt) and tool-result echoes. A list
  endpoint would firm up several assertions; noted as possible future work.
- (R1) **Tool DESCRIPTIONS ride in every request body** — `created_by_worker` appears in the
  `image_list` description, so keying a mock rule on "the tool result contains X" must first
  check X against the always-present tool schemas. Same family: agentd pretty-prints tool
  results but the harness re-marshals compact, so wire-keying must use the escaped compact form.
- (R1) **A prompt that quotes its own briefing heading defeats naive toContain** — count
  occurrences (1 = quotation, 2 = real section). And blackboard round-1 is genuinely racy by
  design (a slow sibling may already see the fast sibling's note): round discrimination must come
  from event text, never briefing presence.
- (R1) **sham-critic's wiring is reflect.DeepEqual-pinned to actor-critic's** — the control arms
  differ only in the critic's words, which is what makes A-minus-sham isolate learning from
  churn (C7 by construction).
- (R1) **Topology tests can import the root agentkit package** — solo-memory pins its quoted
  briefing heading against `agentkit.DefaultBriefingHeading`, so a compose rename trips a test
  instead of orphaning prompt text.

