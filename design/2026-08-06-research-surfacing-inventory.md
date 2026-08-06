# Research-surfacing inventory — what the research produced, where the code stands, what applying it means

> **What this file is.** An inventory of everything Agent Orange's research corpus produced,
> surfaced for an item-by-item conversation between Kai and Claude. For each distinct research
> product — a finding, a principle, a lesson class, a doctrine entry, a measured result, or a
> discovered issue that generalises beyond where it was found — it records three things and
> **only** three things:
>
> 1. **What the research says** (faithful, with doc §/entry cite).
> 2. **Where the code stands today** — applied / partial / absent / contradicted — *verified
>    against the code*, never trusted from a doc's status line (this repo has a documented history
>    of status-line rot).
> 3. **What applying it would concretely mean**, in specifics (named files, seams, mechanisms).
>
> **This file contains no verdicts on whether anything is worth doing.** That is the conversation
> this file exists to feed. It deliberately over-includes: a candidate dismissed in ten seconds
> costs nothing; one filtered out silently costs the thing we didn't know existed.
>
> **Not an implementation plan.** No tickets, no execution order, no engine code was changed to
> produce it. This was `/plan-feature` repurposed for surfacing.
>
> **The one calibration to carry into every line below.** Exactly one live real-model run has ever
> happened (2026-07-28, and it aborted — `docs/product/runs/2026-07-28-calibration-aborted/`).
> Everything else in this repository — every rig, seed, doctrine entry, green suite — is
> **mock-proven**: honest about *transmission*, silent about whether any of it helps a real model
> do better work. Read every "built" and "green" through that lens.

**Provenance.** Compiled 2026-08-06 from six parallel reader sweeps, each of which verified doc
claims against the working tree at `product-layer` @ `6f2ecfa`. Every item carries the source
reader's IDs in brackets — `IN-*` (instruments/harnesses), `DOC-*` (doctrine + scenarios),
`PL-*` (product-layer work-plan log), `RD-*`/`RC-*` (readiness), `CO-*` (operator console),
`SI-*` (self-improvement) — so the full three-part detail with file:line evidence is traceable
back to the reader reports in the source conversation. Raw haul was ~270 verified candidates;
this dedupes them into distinct generalising items across 12 clusters.

---

## Corrections to the corpus itself (surfaced by verification)

Small, load-bearing, and worth fixing wherever this inventory's conclusions land. These are places
the corpus is wrong about itself, found only because the readers checked prose against code.

- **"49 RD findings" is wrong — there are 29.** `docs/AGENTS_RESEARCH.md:385` says 49;
  `:351` says RD1–RD29, which is correct (verified: contiguous, no gaps, no duplicates). [RD-slice]
- **RD9's file path is wrong.** Doc 22 implies `go/cmd/agentd/snapshot_reaper.go`; the file is
  `go/snapshot_reaper.go`. [RD-9]
- **RD29's Worker field count is off by one.** Doc 22 says 13 fields; the struct
  (`go/agentdb/workers.go:78`) and its browser mirror (`web/src/workers.ts:74-97`) both have 12 —
  so there is genuinely *no* wire drift today, and the "13" reads as a drift signal that isn't
  one. [RD-29]
- **Console ticket R5c's diagnosis is wrong in two ways.** It claims "non-UTF8 bytes" in three
  files including `DeskPage.tsx`; all four named files decode as clean UTF-8, `DeskPage.tsx` has
  no NULs at all, and two of the three "offenders" (`useEvents.ts`, `desk.ts`) carry exactly the
  same live NUL-as-key-separator sentinels the ticket protects `WorkerEditor.tsx` *for*. Following
  R5c literally would edit live cache-key semantics. [CO-35]
- **Console ticket R6b is contradicted by the code.** It says the memory selector "is a plain text
  field"; chips-per-clause shipped in MB1 (`MemoryBrowserPage.tsx:118-134`). An empty-state
  screenshot was promoted into a ticket. [CO-37]
- **Doc 13 (R2) says the topology library is 13 seeds; the registry has 14** (`triage-lab@v1`
  added by SC1; `e2e/experiments/README.md:107` agrees it's 14). [SI-14, IN-1, DOC-13]
- **The runbook's §6 go-decision checklist is entirely unticked despite a run having happened**
  (`14-calibration-runbook.md:97-100`). [SI-27]
- **`web/`'s action-vocabulary count pin has moved 18→19** (`configLog.test.ts:38`) after
  `topology_apply` was added. [SI-52, CO-slice]

---

## How the clusters are organised

1. The research's own core (§2–§7 of AGENTS_RESEARCH), applied to the code
2. The one live run's lessons (gates the next run)
3. Silent success — the dominant failure class
4. Delivery-not-storage — proven, but hand-rolled per rig
5. Doctrine reaches no one
6. Engine seams the research kept hitting
7. Gates that don't gate
8. The operator console
9. Status rot as a live class
10. The trap register — decisions a refactor would "fix"
11. Spec decisions parked for Kai, never taken
12. Process & tooling lessons

Then three roll-up tables for the already-ticketed backlogs (RD1–29, console R1–R6, L3H/L3M),
with **verified** status.

---

# Cluster 1 — The research's own core, applied to the code

The literature in AGENTS_RESEARCH §§2–7 is self-contained reasoning. These items ask: does the
code embody it? The largest single research→code gaps in the whole inventory are here.

### 1.1 De-anchoring is implemented nowhere [IN-22 — flagged highest-leverage in its slice]
- **Research:** §2 names de-anchoring as the *decisive* mitigation for judge–truth divergence —
  require the judge to commit its own answer before it sees the candidate; false-positive rate
  collapsed 0.72→0.01, and as a training reward it prevented the basin forming at all. R2 requires
  it ("the scorer answers the brief itself before seeing any candidate"); doc 10 §4 entry 12 lists
  "de-anchored pairwise scoring" as *part of* the frozen-scorer harness.
- **Code today: ABSENT.** `grep -rni "de-anchor|deanchor|before it sees|commit its own"` over
  `e2e/experiments/` and `go/` returns zero hits. `tierb/grade.ts` implements blinding, shuffling,
  anchors and ranking (§7 rules 1–3) but the grader sees candidates with no prior commitment of
  its own. Worse: `go/topology/frozenscorer.go:83-88`'s `scorerPrompt` asks for `Score: N/5` — an
  **absolute** score, precisely what R2 argues against, in the seed named after the harness.
- **Applying means:** (i) a first grader turn that answers the brief before the batch is shown, in
  `tierb/grade.ts`; (ii) rewriting `scorerPrompt` to pairwise-against-reference. The mitigation the
  literature calls decisive is present in neither the harness nor the seed built to embody it.

### 1.2 R2's absolute→pairwise scoring [IN-22, DOC-slice]
- **Research:** R2 — absolute 1–10 judging suffers ceiling compression, heavy-tailed variance, and
  scale drift; reference-anchored Elo (pairwise vs a fixed reference, expressed as win probability)
  is materially more stable and yields uncertainty estimates without resampling.
- **Code today: PARTIAL/CONTRADICTED.** Tier B *does* rank rather than score
  (`tierb/grade.ts` — "the seam's return type is an ordering of labels … no place to put a 0-10
  score, deliberately"). But the `frozen-scorer@v1` seed emits `Score: N/5`, and no rig runs that
  seed through a pairwise protocol.
- **Applying means:** rewrite `scorerPrompt`; run `frozen-scorer@v1` through the C1 rig with the
  ranking protocol Tier B already implements.

### 1.3 R3's two-capability split and swap tests [IN-23, SI-slice]
- **Research:** R3 — harness-*updating* capability (can the critic produce good edits?) vs
  harness-*benefit* capability (can the worker exploit them?). Without the split, a flat curve
  doesn't tell you which half failed. §5's metrics table lists "Swap tests (round-k prompt ×
  round-0 config)".
- **Code today: ABSENT.** `tierb/README.md:151-154` says so plainly. No rig implements a swap arm.
- **Applying means:** an arm that applies round-k's prompt to a round-0 configuration. The seam
  exists — `spec.arms`'s `afterApply` hook already maps "critic disabled after apply"
  (`experiments/README.md:136`).

### 1.4 R4's derived-rubric artifact is collected but never measured [IN-24, DOC-21]
- **Research:** R4 — model-generated rubrics run ~27 points behind human-authored ones; the rubric
  the system *derives* from a vague goal is itself a primary experimental artifact: diff round-k's
  prompt against round-0's and read what criteria it invented. §5 lists prompt-diff, prompt-length,
  and embedding-dispersion metrics.
- **Code today: PARTIAL — collected, not analysed.** Every rig captures `promptWrites` with
  rationales per round and annotates the rationale as "the derived-rubric artifact"
  (`calibration/runner.ts:453` etc.). But `grep -rni "dispersion|prompt_length|instruction_count"`
  over `e2e/experiments/` → zero. No diff metric, no length metric, no dispersion metric.
- **Applying means:** three cheap pure functions over data already in every report — round-0↔k
  prompt diff, prompt length / instruction count per round, and embedding dispersion (pgvector is
  already wired). §5 names all three as detectors for verbosity duct-tape and diversity collapse.

### 1.5 §5's build-this-first metric is the least-built [IN-25]
- **Research:** §5's metrics table calls "internal critic score vs frozen score" the §2 divergence
  signature — "visible within a couple of rounds and cheap" — and says outright: **"The second row
  is the one to build first. It is the cheapest signal we have and it fails loudly."**
- **Code today: PARTIAL / re-aimed.** Calibration and triage compute `checker_agreement` /
  `auditor_agreement` — instrument-vs-truth, a *different* quantity. Nothing compares the critic's
  own assessment to the frozen score. The `frozen-scorer@v1` seed renders both a scorer (`Score:
  N/5`) and a critic, but no rig runs it and no metric pairs the two numbers.
- **Applying means:** run `frozen-scorer@v1` through the C1 rig and add a metric pairing critic
  verdict with scorer score per round. The doc's own priority ordering puts this first.

### 1.6 Controls exist but aren't exercised: sham off by default, one seed [SI-20, DOC-9, C7]
- **Research:** C7/§5 controls — solo, solo+memory, sham-critic arms; multiple seeds; variance
  reported. "An improvement claim that hasn't beaten the sham is motion, not learning." The sham
  measured result (C1 rig): placebo ties the genuine critic exactly on `prompt_writes` (2±0),
  separating only on outcome predicates.
- **Code today: PARTIAL.** The sham topology is `reflect.DeepEqual`-pinned identical to
  actor-critic (`go/topology/shamcritic.go`), and arm C is built (`calibration/arms.ts:41-56`) —
  but **optional by default**: `calibrate.ts:99` filters out `a.optional` unless named, so the
  default live run is A+B only. Seeds: **one** (`manifest-30.json`, `seed: 20260728`); the runbook
  says "≥2 seeds if budget allows; otherwise say 'one seed' loudly," and the metrics renderer does
  emit that caveat.
- **Applying means:** run arm C explicitly and produce a second manifest at a different base seed
  so "variance" is a measured column, not a caveat sentence.

### 1.7 The 14-topology library has produced zero comparative data [SI-14, DOC-13/OM-6]
- **Research:** C1 — structure beats model quality as the first lever; the org chart is a variable
  to *search*, not a constant to enshrine (MAST: ~79% of multi-agent failures are organisational).
  OM-6 ("the org chart is a variable, not a doctrine") is the only `candidate` OM entry.
- **Code today: APPLIED as apparatus, never exercised as a search.** 14 topologies self-register;
  the comparison rig (`e2e/experiments/compare.ts`) runs. But every live config
  (`calibration-30`, `triage-24`, `gauntlet-24`) is a *within-topology* A/B (critic on/off,
  doctrine on/off), never *across-topology*. **Zero real-model comparisons of org charts have ever
  run**, and the C1 `compare` command has no live config at all.
- **Applying means:** a topology axis in the arm spec (arms already carry a `TopologyBody`), so an
  arm can name `supervisor@v1` vs `assembly-line@v1` vs `blackboard@v1` on one ticket stream.
  Doc 19's SC-1 proposes exactly this race. Port pool caps concurrency at 100.

### 1.8 C3's licence for Tier B has not actually been granted [SI-16]
- **Research:** C3 — prefer facts to judges; calibrate the instrument on a domain with held-out
  truth *before* trusting it on unverifiable domains. §6 makes this the sequencing rule: prove the
  instrument can see improvement we *know* is real, then run poetry.
- **Code today: the gating condition is unmet.** The fact-scored instrument ran once and saw **no**
  improvement — because there was none to see (the ceiling result), not because it's blind. So the
  licence C3 was meant to grant Tier B has not been granted; a flat line on a verifiable task is
  supposed to *precede* the unverifiable one, distinguishing "no improvement" from "blind
  instrument," and that discrimination hasn't happened.
- **Applying means:** a decision point, not a code change — L3M (harder manifest) first, so the
  instrument gets a chance to show a nonzero delta on known truth before Tier B is trusted.

---

# Cluster 2 — The one live run's lessons (gates the next run)

The 2026-07-28 calibration is the only contact with reality. It produced one measured result and
three named defects. Because triage and gauntlet share the runner, **L3H gates three live
experiments, not one** (`triage-24`, `gauntlet-24` are both `mode:'live'`, both never run).

### 2.1 The ceiling result — the most decision-relevant fact in the corpus [SI-1, DOC-36, lesson-class-7]
- **Research:** probe 3/3 then arm A to h08 = **11/11 correct across all four hypothesis kinds**,
  **zero `worker_prompt_write` in 11 rounds** — the critic declined every round on the record ("No
  methodological amendment required") because the investigator was already correct from hypothesis
  one. Arm A never diverged from arm B, so "A ≈ B" was diagnosable before B ran. Conclusion: at
  this model strength, n=400 datasets with clean planted effects don't discriminate between org
  charts; re-running the manifest buys an expensive null.
- **Code today: corroborated by the committed evidence.** `arm-a-config-events.json` holds exactly
  11 config events and no `worker_prompt_write`; `arm-a-project-events.json` holds 27
  `worker.finished` (the critic *did* fire 9 times). The manifest is live and unchanged
  (`manifest-30.json`, seed 20260728).
- **Applying means:** nothing from the *finding* itself — it is L3M's input. It forbids one action
  (re-running `calibration-30` as-is) and specifies the replacement (a harder manifest verified
  offline). Ceiling effects hide in green results: 11/11 with zero rewrites is a task with no room,
  not a working loop.

### 2.2 L3H(a) — an empty assistant reply must be a result, not a hang [SI-5, SI-28, IN-28, RD-6-adjacent]
- **Research:** h09's frozen checker finished in 6s with `status=ok` and a session holding only the
  user message; the investigator before it ended mid-turn at 76s (vs 160–240s norm). Because the
  status said `ok`, the runbook's `rate_limited` abort never fired. Must be recorded as a failed
  hypothesis counting toward abort criteria.
- **Code today: NOT addressed — and the root is in the engine.** `go/cmd/agentd/dispatch.go:337-360`
  sets `DeliveryOK` and downgrades only on `runErr != nil` or `SessionAwaitsHuman`; nothing
  consults whether the session produced an assistant message. `agentdb/events.go:212` has no
  "ran but produced nothing" state. Harness side, all three rigs' `deliverable()` throw on the
  180s poll deadline.
- **Applying means:** two independent changes. **Engine:** a delivery outcome (or a
  `produced_output`/message-count fact on the row) distinguishing "ended with no assistant output"
  — touches `dispatch.go:337`, `agentdb/events.go:212-242`, a migration, and the mutation-log
  adoption. **Harness:** `deliverable()` returns `''` instead of throwing, plus a
  `consecutiveEmpties` counter beside `consecutiveFailures` (threshold undecided in the doc). Lands
  in three rig files unless done as a shared module.

### 2.3 L3H(b) — no throw may bypass the report [SI-29, IN-28]
- **Research:** ~135k tokens of real spend produced zero report artifacts because a poll-timeout
  throw bypassed the report writer — which already knows how to abort *an arm*, record the reason,
  and let other arms run. Prove the fix with a fault-injection test.
- **Code today: NOT started; the escape hatch is one line.** `calibration/runner.ts:146`
  `if (!(e instanceof AbortRun)) throw e` — a plain `Error` escapes `runArm`, escapes the arm
  loop, and lands in `main().catch` → `process.exit(1)` before any report is written. The abort
  path itself is already correct; the fix is to route more errors into it. Identical at
  `triage/runner.ts:147`, `gauntlet/runner.ts:151`.
- **Applying means:** convert the catch to record any error as `abortReason` (distinguishing
  "aborted by criterion" from "crashed"), let the arm loop continue; add a `runner.test.ts` with an
  injectable client whose `listMessages` throws mid-arm, asserting a report still exists. The
  runner must accept an injectable client (small seam change — it currently builds its own).

### 2.4 L3H(c) — persist the transcript as the run proceeds [SI-30, DOC-32]
- **Research:** `agent_query_events` held ZERO rows for every session of the run — sessions are
  swept per hypothesis and deleted before anything archives them, so the harness run-log is the
  only transcript record and a crash destroys it. This generalises: **per-unit session sweeping
  destroys transcripts across all three rigs.**
- **Code today: NOT started.** Single write at `calibrate.ts:214` (and the triage/gauntlet
  analogues); sweep at `runner.ts:376-380` with the stated reason "30×3 sessions would exhaust the
  pool."
- **Applying means:** append a JSONL line per hypothesis before the sweep. Two design points: the
  surviving transcript source is the **project event stream** (`worker.finished` text, ~278KB
  survived run 1), not `agent_query_events`, so the append reads what `readLog` reads; and the
  run-log is deliberately *not* the byte-diffed artifact, so appending can't break determinism.
  **Do not** "fix" this by retaining sessions — that reintroduces the port-pool ceiling.

### 2.5 L3M — a discriminating manifest, with a genuine internal tension [SI-31, DOC-36, lesson-class-7]
- **Research:** today's hypolab is too easy (11/11). Generate hypotheses a competent first attempt
  gets *wrong* often enough to leave room — smaller effect sizes, lower n, multi-way or
  partially-observed confounds — and **verify difficulty offline before spending a token.**
- **Code today: knobs half there.** Expressible now: `n` and `effect_size` (per-scenario fields in
  the manifest). **Not** expressible: multi-way/partially-observed confounds — `Row` is
  `{Treated, CovariateHigh, Outcome}`, one binary covariate, confound strength hardcoded
  (`hypolab.go:223`). `verifyTrap` proves a *correct estimator* wins — the opposite of what L3M
  needs (whether a *model* loses).
- **Applying means:** three separable pieces. (1) manifest-only lower n / smaller effect — hours,
  probably insufficient alone. (2) `go/hypolab` gains spec fields for confound strength / a second
  or partially-observed covariate — real change to `Row`, both estimators, golden-byte tests, and
  every committed dataset regenerates. (3) A difficulty gate — which `verifyTrap` structurally
  cannot be. **The tension:** L3M asks to verify difficulty offline, but no offline procedure can
  verify difficulty-against-a-model; the honest options are a cheap live probe or accepting that
  difficulty is only knowable by spending. L3X's own conclusion argues for pointing the loop at
  real underspecified work rather than engineering harder synthetic puzzles.

### 2.6 The runbook's go-checklist and protocol drift [SI-24, SI-27]
- **Research:** three abort criteria (ceiling / >3 provision failures / any successful write to the
  fact-checker); the §6 go-decision checklist.
- **Code today:** three criteria implemented (ceiling fires two ways — `rate_limited` status *and*
  harness token count, because the engine gate queues rather than stops); the L3X signature (empty
  session, 76s vs 240s turn) is covered by **none** of them, though the harness had the data to
  notice. The §6 checklist is entirely unticked despite the run.
- **Applying means:** add the empty-reply criterion (2.2) and consider a wall-clock/turn-duration
  guard; tick/date or re-scope the checklist as per-run.

---

# Cluster 3 — Silent success (the dominant failure class)

Doc 22's whole reason for existing, and it recurs across every slice. Verified count:
**3 of 29 RD findings fixed, 26 open — all seven blockers open.** The full roll-up is at the end;
here are the *generalising* items and the one security finding.

### 3.1 The silent-success class itself, and the fixture rule [RC-1, RC-2, SI-13, CO-24]
- **Research:** the threat is not crashes but "something reports success, or reads zero, or quietly
  does nothing, and nothing fails at use time." Root cause, every time: a reader and a writer
  disagreed and only the reader was tested — usually against a fixture no production writer could
  produce. Standing rule: **a fixture must be captured from a real writer; an invented fixture is a
  silent-success generator.** Judgement test: "if this were broken now, what would tell us?"
- **Code today: applied as a lesson at ~3 sites, absent as a mechanism.** The class is named only
  in this doc and in comments at fixed sites (`token_usage.go:98-106`, `mcpserver.go:478-497`).
  No lint, CI check, or test-naming convention enforces the judgement test. The capture rule is
  applied at exactly one place (`token_usage.go`'s captured envelope); four mock writers
  (`mock-server/streaming.ts`, `go/mockmodel`, `go/modelproxy`, `go/modelproxy/script.go`) still
  hand-emit usage shapes. **The console's ux-stub fixture mirrors the *reader***
  (`ux-stub/README.md`: "Wire shapes mirror the web/src coercers") — the exact anti-pattern,
  mitigated only by one live-stack pass.
- **Applying means:** a repo-wide sweep for fixtures with no captured provenance (start with the
  mock writers); a fixture-provenance convention or CI check; a capture mode on `stub-server.mjs`
  that records real `agentd` responses into `fixtures.mjs`. That last converts the console's main
  verification instrument from testimony into evidence.

### 3.2 Cross-project read: `Messages` and `QueryEvents` have auth but no tenancy check [PL-6 — security]
- **Research:** the `unblock` entry (2026-07-25) fixed the tenancy gap on `GET /agent/session/{id}`
  and explicitly flagged: "worth deciding whether `Messages`, `QueryEvents` and the artifact routes
  want the same treatment now that session rows carry project content."
- **Code today: the artifact routes were fixed; these two were not.** `go/httpapi/history.go:22`
  and `:69` call `h.identify` for authentication, then query by `SessionID` alone with no project
  comparison. Any authenticated principal of any project can read any session's full message
  history and raw query events by id. The fix helper (`ownsSession`) already exists and is used two
  files over (`artifacts_handler.go:39`).
- **Applying means:** add `ownsSession` to both handlers plus negative tests. The one item in this
  inventory that is a security finding rather than a design question.

### 3.3 The missing-column family [PL-14, PL-48, RD-15, RD-20]
- **Research:** the same shape three times — a good diagnostic exists in memory and dies at a
  storage boundary. `event_deliveries` has no failure-reason column (E1/E3/I4) and no token column
  (F1); `agent_sessions` had no `create_error` until migration 032 fixed exactly this.
- **Code today: confirmed absent.** `EventDelivery` (`events.go:255-272`) has no reason/error
  field, while `dispatch.go` passes rich reason strings to `d.fail(...)` that have nowhere durable
  to go. `GetSessionTokenSummary` exists (`sessions.go:262`) with no HTTP caller; the jobs table
  fetches query-events per row (`web/src/events.ts:41`). `create_error` is served
  (`lifecycle.go:125`) with **zero** UI consumers.
- **Applying means:** a `failure_reason` column populated from the string that already exists in
  `d.fail(...)`, surfaced in the jobs UI; a `tokens` column or a token-summary route; read
  `create_error` wherever `status === "error"` renders.

### 3.4 An event matching no subscription vanishes silently [RD-19]
- **Research:** the router loops subscriptions, skips non-matches, marks the event delivered — zero
  matches is byte-identical to a healthy no-op: no log, no row. Write-time validation checks the
  event *type's shape* but never that the worker exists or the type is known (`worker.faild` is
  accepted); unknown filter keys silently never match. Same shape for briefing selectors: an empty
  selector is skipped silently, so a typo and "nothing written yet" are indistinguishable.
  "'My worker didn't wake up' has no observable signal at all."
- **Code today: OPEN, all three parts.** `router.go:236-247` (no zero-match branch);
  `events.go:474-499` (no worker-existence, no known-type check); `router.go:416-420` (unknown key
  → false); `compose.go:222-233` (`ErrMemoryNotFound` skipped without a log, by explicit decision).
- **Applying means:** one log line at the router's zero-match point and at `compose.go:224`, plus
  write-time validation of worker existence and filter keys.

### 3.5 Detectors that fail safe in the wrong direction [PL-3, RD-26, RD-27]
- **Research:** "a detector that fails safe in the wrong direction is worse than none, because it
  is trusted." `runningContainers()` returned 0 on any error (a Docker hiccup certified a leak
  clean). The pattern recurs in the browser: a dropped SSE stream reports the turn *finished* so a
  truncated answer reads as complete (RD26); when the attention route fails the Desk says "Nothing
  is waiting on you" and the badge reads 0 (RD27).
- **Code today:** the e2e-rig instances fixed; the browser instances OPEN and verified
  (`useAgentSession.ts:460-471,672-676`; `useAttentionRequests.ts:81-85`,
  `useDesk.ts:219-221,250`). The same fail-open decisions exist deliberately elsewhere — the budget
  gate (PL-22), `BuildBriefingSections` (PL-23) — see cluster 10.
- **Applying means:** distinguish "probe failed" from "idle/absent" at each browser site; drive the
  attention fallback off "did this load succeed" rather than `available`. The correct pattern is
  already in-repo (`TopologyOnboarding.tsx:186`, `useConfigLog`'s not-wired-vs-failed split).

### 3.6 A failed load renders first-run onboarding over an established project [RD-28]
- **Research:** the workers hook leaves the initial `[]` on failure; `DeskPage` computes
  `firstRun = !loading && workerCount === 0` and replaces the whole Desk — so an operator with a
  dozen workers and live jobs is invited to "start from a topology." Same at `WorkersPage`,
  `WorkerList` (which has no error path at all).
- **Code today: OPEN, three sites** (`useWorkers.ts:54-56`; `DeskPage.tsx:113,137`;
  `WorkersPage.tsx:123`; `WorkerList.tsx:55-60`).
- **Applying means:** gate the three empty states on `error === null`, copying the in-repo pattern.

### 3.7 Verify a negative with a second, differently-built method [RC-3, RD-25]
- **Research:** "a tool reporting 'no matches' is not evidence of absence." GNU grep silently skips
  files it deems binary; five `web/src` files qualify, so **every clean search over that directory
  has been unreliable** — including the audits' own. The orchestrator's first verification used
  `$'\x00'`, which bash collapses to empty, reporting *every* file affected.
- **Code today: still live, reproduced this session.** `grep -c $'\x00'` over the five files
  returns nothing (suppression); `perl -ne 'print if /\x00/'` finds exactly those five and no
  others. The NULs are a legitimate composite-key idiom (`\0` separator, chosen because NUL can't
  appear in user data). See also CO-35, SI-52.
- **Applying means:** replace the five literal bytes with `\0` escapes (behaviour-identical) and
  add a CI guard failing on any raw NUL in tracked source; a documented convention that
  "zero occurrences" findings state which second method confirmed them.

### 3.8 The readiness bar, durability table, and audit programme (concepts) [RC-4, RC-6, RC-7, RC-8]
- **Research:** the readiness bar is explicitly *not* "no known bugs" — five clauses (no silent
  failures on first-user paths; nothing a user made disappears without being told + a written
  durability table; every reachable error says what to do next; first-run works end to end without
  hand-editing; every ceiling has a test that observes it fire, proven by breaking it). The
  durability table (13 rows) and the "read-only sweep → filed evidence → item" audit programme are
  reusable methods. Base-rate calibration: "everything we are confident about is mock-shaped."
- **Code today:** the bar is met by clause only partially (clauses 1 and 4 contradicted by open
  RDs); the durability table lives *only* in doc 22, not in `docs/18` where an operator would look;
  the audit programme produced 29 findings and is undocumented as a repeatable method; no sweep has
  covered the sandbox, the e2e rigs, or `installations/`.
- **Applying means:** treat the five clauses as the release checklist; promote a distilled
  durability table into `docs/18`; name the sweeps not yet run.

### 3.9 `memory_create` reports `embedded:true` while storing no embedding [RD-3]
- **Research:** the flag is derived from whether the *embedder* returned a vector, not from what
  was *stored*; the column can genuinely be absent (migration 022 swallows a failed
  `CREATE EXTENSION vector` — "exactly what happens on managed Postgres … the GCP deployment
  direction"). Memories are append-only, so those rows can never be embedded later. A `sync.Once`
  latches false on one transient error for the process lifetime.
- **Code today: OPEN, all three parts** (`memories.go:137,373-386`; `mcp_memory.go:288`;
  `migrations.go:293-304`).
- **Applying means:** distinguish "query failed" from "column absent"; derive `embedded` from what
  the store wrote; fail the create when a vector was supplied and can't be stored. Needs
  live-Postgres with the vector column absent to validate — a GCP-shaped risk.

---

# Cluster 4 — Delivery-not-storage (proven, but hand-rolled per rig)

The single most load-bearing idea in the research (OM-9 / C5), and the discipline that makes the
whole test story honest — but it lives in per-rig comments and one-off manual acts, not shared
infrastructure.

### 4.1 The collapse experiments are manual, not committed artifacts [SI-6, SI-7, DOC-16]
- **Research:** breaking the split marker collapsed arm A onto arm B's *exact* numbers while
  `prompt_writes`/`freeze_refused` held at 6 — the write happened, was config-logged, never reached
  the model (SC1 → OM-9). SC3's non-vacuity: disable the doctrine injection and the doctrine arm
  reproduces the control in *every* column. "That collapse, not the delta, is what the smoke is
  for."
- **Code today:** the discipline is encoded in runner comments and enforced by construction, not by
  a test. The non-vacuity proofs (split-marker break, injection disable) were **manual, one-off**
  validations recorded in doc 13's Resume point, not committed.
- **Applying means:** a second config per rig (e.g. `gauntlet-smoke-6-collapsed`) whose report is
  committed alongside the normal one, so non-vacuity is a diffable artifact. Today re-verifying it
  requires a human to break a string and remember to put it back.

### 4.2 The break-your-own-tripwire pattern is re-implemented three times [DOC-16, SI-15, IN-slice]
- **Research:** OM-9 — prove your delivery assertions non-vacuous by breaking them. The
  instrument-unmoved assertion (C2) is likewise repeated: `calibration/runner.ts:135-142` compares
  the checker's prompt and `frozen` flag against a seed snapshot every round; triage and gauntlet
  duplicate it.
- **Code today:** three near-identical runners each hand-roll the tripwire and the unmoved-assert.
  Nothing engine-side enforces either; the next rig can silently omit them.
- **Applying means:** a shared helper in `e2e/experiments/` a rig *must* call to register its
  delivery tripwire, plus a test that the tripwire fires negative when disabled; a shared
  `assertInstrumentUnmoved`. Makes the C2/OM-9 guarantees one implementation.

### 4.3 Attribute-by-actor is encoded in the gauntlet only [SI-8, DOC-17/OM-10]
- **Research:** OM-10 — where a legitimate worker moves the same counter a signal reads, a
  project-wide total reports the innocent as guilty. SC3's measured proof: predicted totals were
  *wrong* (7 vs actual 8) while predicted *attributed* counts were exact. This is the one OM entry
  cited by name inside `go/` and enforced (`mcpserver.go:116` — empty `ActorWorker` means a human,
  RD4 guard; `web/src/configLog.ts` renders it).
- **Code today:** the gauntlet pairs `freeze_refused`/`freeze_refused_directed` and
  `prompt_writes`/`dispatcher_config_writes`. Calibration and triage have **only** the project-wide
  counters, though `runner.ts:463` already reads and discards `r.actor_worker`.
- **Applying means:** add attributed variants to the calibration and triage metric sets (filter
  config events by `actor_worker`, project events by `envelope.worker`) — the data is already
  fetched. Cheap, and it's the difference between a number an experiment can predict and one it
  can't.

### 4.4 The determinism check itself was unsound [SI-4, lesson-class-5]
- **Research:** two concurrent prompt writers (critic + dispatcher) race for the config log's next
  `seq`; every metric was identical, only the lineage array moved. Two runs on one machine agreed;
  a third from another checkout didn't. **Same-machine repetition cannot detect this class** —
  which matters because "byte-identical reports" is the reproducibility check the whole methodology
  rests on.
- **Code today: fixed in the gauntlet only** (`b6b5137`, actor-grouped stable sort). Calibration's
  lineage is still a flat seq-order flatten (`metrics.ts:365`); it has one writer today so it
  doesn't bite, but a two-writer calibration config inherits the bug. Same shape in triage.
- **Applying means:** lift the gauntlet's actor-grouped stable sort into the shared `../report`
  module; run the determinism check from two different checkouts (nothing in `run.sh` enforces it).

### 4.5 Reports print their own epistemic status — generalise it [SI-40, DOC-5]
- **Research:** "never quote smoke tables as findings." A doctrine mock smoke shows delta 0 *by
  construction* (calibration); the gauntlet's delta is authored *non-zero* and "equally meaningless
  as a finding — the report must say so on its face." The transferable rule: a renderer should
  print its own epistemic status, because the artifact outlives the conversation.
- **Code today: applied per-rig, unusually well** (`calibration/metrics.ts:303-319` mock/live
  blockquotes; the two rigs' opposite-sign warnings). Each rig re-authors the caveat.
- **Applying means:** generalise the "authored delta" / mock-caveat warning into the shared
  `report.ts` so any mock-mode report carries it.

---

# Cluster 5 — Doctrine reaches no one

The injection mechanism works and is well-defended *inside the rigs*. Outside them it touches
nothing, no promotion has ever happened, and every entry is `candidate`.

### 5.1 Doctrine reaches no real user; no operator surface mentions it [DOC-3, RD-21]
- **Research:** doc 20 §1 — the worker doctrine block is "the general level of common sense,
  whichever the goal is," the default starting place for people with no experience arranging an AI
  org.
- **Code today: not at all, for anyone outside the rigs.** `grep -rn doctrine web/src
  examples/web/src` → zero; `grep -rn doctrine go/topology/*.go` → zero. Both callers performing
  the mutation are experiment rigs (`calibration/doctrine.ts`, `gauntlet/runner.ts`). The console's
  project-prompt editor is a bare textarea with no template or insert affordance; `docs/18` never
  mentions doctrine exists. §8.8's first seeding would run with an empty project prompt.
- **Applying means:** a section in `docs/18`; a "start from doctrine-v1" template/button in
  `ProjectSettingsPage.tsx`; or an opt-in checkbox on topology apply seeding the project prompt.
  **The honest tension** (§1's own discipline): every one of these ships an *unmeasured* candidate
  to a real project — but the alternative is doctrine staying a lab artifact permanently.

### 5.2 The promotion ladder has no machinery [DOC-2, DOC-4]
- **Research:** the status ladder (`candidate` → `evidenced-offline` → `law` → `demoted`) and its
  protocol — wholesale A/B first, then single-line ablation to attribute, then a generality check
  on a second scenario, results landing dated with the status table changing *in the same commit*.
- **Code today: the ladder is prose; no artifact carries a status.** No status field, table, or
  lint. The A/B lever exists (both rigs) but the *ablation* machinery does not: `loadDoctrine`
  returns the whole block from marker to EOF with no line-selection API, so "rerun with single
  lines removed" has no implementation. The "same commit" rule is unenforced.
- **Applying means:** an ablation API in both `doctrine.ts` files (`loadDoctrine(v, {omit:[2]})`)
  plus an arm-id convention; a run-record schema under `docs/product/runs/` naming version + omitted
  lines; optionally a CI check that any `law` row cites an existing run path.

### 5.3 Every WD entry is `candidate`; no A/B has ever promoted one [DOC-18..27]
- **Research:** WD-1..WD-10 exist as 21 lines in `doctrine-v1.md` and rows in doc 20 §5. WD-1's
  instrument (SC-3 gauntlet) is *built*; the §6.2.4 boundary held against a real model on
  2026-07-26 — **for one session, not an org**, which is the gap SC-3 exists to close.
- **Code today:** none is represented in `go/`/`sandbox/`/`web/` as behaviour. WD-1's nearest engine
  kin is the core preamble's weaker boundary (`compose.go:549-551`) and the verbatim event markers
  (`compose.go:44-47`). WD-6 (never work around a frozen worker) is the best-instrumented and
  enforced; WD-3 ("no effect" is valid) has the strongest real-model datum — h05's underpowered
  sample answered `no-effect` unprompted, arguing the *baseline already has* WD-3 at this model
  strength.
- **Applying means:** run the gauntlet against a real model to move WD-1 off `candidate`; note the
  preamble/doctrine overlap means a doctrine-vs-none A/B is an A/B of *incremental* boundary
  strength, not boundary-vs-none.

### 5.4 Falsifiability + design constraints are honoured in the artifact, unenforced by anything [DOC-7]
- **Research:** §6 — doctrine must contain no task instructions, no topology wiring, nothing
  contradicting the preamble, and **no unfalsifiable virtues** ("if no scenario could ever demote
  it, it is decoration"). §5 — short, imperative, worker-facing, and *silent on tools a worker may
  not hold* (says "escalate," not `request_human_attention`).
- **Code today: honoured in `doctrine-v1.md`, unenforced.** No test for "doctrine mentions a tool
  name," no byte-length budget, no non-contradiction check.
- **Applying means:** a cheap `doctrine.test.ts` asserting the block names no core-MCP tool
  (enumerable from `main.go`'s `srv.register` lines) and stays under a byte ceiling; that every WD
  entry names an existing instrument.

### 5.5 Three scenarios unbuilt — but their org halves already exist [DOC-39, DOC-40, DOC-41, DOC-42]
- **Research:** SC-2 (pipeline QA), SC-4 (escalation economy — the scorer is a *frontier*, accuracy
  *and* spend), SC-5 (retention — superseded facts + plausible-from-priors distractors, the only
  design that separates "the org remembered" from "the model knew"). Build order: SC-1 then SC-3,
  then SC-2/4/5 stay catalogued "until the first two produce results worth acting on."
- **Code today:** SC-1, SC-3 built; SC-2/4/5 have no code. But **all three unbuilt scenarios race
  topologies that are already registered** — so the org half is done and only the instrument half
  (generator + truth + scorer + trap verifier + rig directory) is missing, ~one `go/<lab>/` package
  + one `cmd/<lab>gen` + one `e2e/experiments/<name>/` each. The build gate ("until the first two
  produce results") is unmet in the strongest sense: SC-1/SC-3 have produced *zero* results
  (smokes aren't results). By its own rule the library is correctly stalled, and the unblocking
  action is a real-model run, not more building.
- **Applying means:** for each, the instrument half only; SC-4 carries the TOK1 caveat (spend axis
  readable only post-fix); SC-5's distractor design generalises to any memory benchmark.

---

# Cluster 6 — Engine seams the research kept hitting

Recurring walls where the research had to work around something the engine doesn't offer.

### 6.1 Workers cannot emit typed events [SI-38, DOC-11/OM-4, DOC-14/OM-7, PL-40]
- **Research:** the only routable worker output is `worker.finished` (the whole transcript), so
  addressing is a `ROUTE-TO: <name>` line parsed out of prose (OM-4: "contracts live in
  deliverables; never parse a transcript"). Filed independently by four slices. Consequences:
  debate's aggregator needs 2N subscriptions; temporal-hierarchy's review channel must be memory;
  grading `worker.finished` without slicing the deliverable grades transcripts *including worker
  names*, past the blinding. Also: `worker.finished` transcripts carry no tool summaries,
  contradicting §8.2 — so the reviewer in the acceptance loop reads a transcript with the actions
  removed (PL-40).
- **Code today:** the registered MCP tool set has no `event_emit`; `reconstructConversation` skips
  all tool events (`runner.go:1497-1530`). The `ROUTE-TO:` convention is in four topologies + two
  rig parsers.
- **Applying means:** `event_emit` is one `mcp_*.go` file + one `srv.register(...)` line (CLAUDE.md
  states the recipe) plus depth/loop-guard decisions the event spine already has machinery for. It
  would let addressing be typed, retire the 2N-subscription workaround, remove the
  transcript-vs-deliverable footgun at its source, and (with tool summaries in the transcript)
  restore the acceptance loop's evidence base.

### 6.2 The mock-script format's structural limits [IN-15, IN-16, IN-17, IN-18, SI-33, SI-34, SI-46]
- **Research:** four distinct traps in the prompt-scripted-test grammar, all currently prevented by
  convention rather than by the parser:
  - **Ordering** is a correctness precondition (four different orderings now exist), enforced only
    by per-rig tests that aren't in CI (IN-15).
  - **The JSON-encoding trap** — a `match` phrase containing `"`, `\`, or newline is escaped in
    flight and the rule goes silently always-false, "the worst failure mode this format has"
    (IN-16, SI-46). Preventable in `ParseScriptTable` in ~5 lines; the precedent for refuse-at-boot
    already exists (`DisallowUnknownFields`).
  - **The rule-order inversion with consumer count** — the correct order *inverts* depending on the
    topology's fan-out shape, which the script file can't see (IN-17, SI-33).
  - **The two-predicate limit** — `identity ∧ ticket ∧ doctrine-present` needs three slots, a rule
    has two (`Match`, `Absent`); this is exactly why the SC-3 transcript injection channel is
    currently unmeasurable (IN-18).
- **Code today:** `modelproxy.Rule` has exactly `Match` and `Absent`, first-match-wins over raw
  body bytes; no ordering knowledge, no JSON-escape inspection, no all-of semantics. Has bitten in
  five+ items (H0, T4–T7, L2, R2, SC1, DR1).
- **Applying means:** `match:[]string`/`absent:[]string` (all-of) unblocks the SC-3 transcript
  channel; a shared linter over any script table catches body-match leaks (later rule's `match` ⊂
  earlier rule's output) and JSON-escape hazards from the file alone; normalise (unescape) the body
  before matching, or offer `matchJSON`.

### 6.3 The simulated-time invariant holds but nothing guards it [IN-30, SI-19, DOC-14/OM-7]
- **Research:** C6/§7 — a schedule firing is an ordinary project event through the ordinary
  dispatch gate; **no future trigger may fire through a private path**, or its consumers stop being
  testable offline.
- **Code today: APPLIED, structurally.** The scheduler writes a real project event
  (`scheduler.go:241`) then calls the shared gate; the router matches events and calls the same
  gate; both converge on the single `StartJob` at `dispatch.go:324` (the only non-test call site).
  The one deliberate exemption (interactive chat) is documented. But **no test asserts that no
  *second* `StartJob` call site appears** — the guarantee rests on review discipline.
- **Applying means:** a cheap grep-style guard in the spirit of `TestMutationsAreLogged`,
  enumerating dispatch entry points and asserting exactly one. Makes the rule survive a new
  contributor.

### 6.4 `worker.freeze_refused` is emitted and glyphed, but there's no aggregate count [IN-7, SI-21/C8, CO-52]
- **Research:** doc 10 §3 / C8 — "count and surface those attempts; they are close to free and
  among the most interesting numbers the experiment produces." A worker editing the thing that
  scores it is the reward-hacking hypothesis in its most literal form.
- **Code today: PARTIAL.** Emission is real and best-effort (`workers.go:115`,
  `mcp_management.go:725-745`); a steel-lock glyph renders per-event on the Desk feed. **No
  aggregate count anywhere in `web/` or `go/httpapi/`** — the only counting is harness-side. The
  event doesn't even name its *defended* worker in a field; `desk.ts` parses the target out of
  prose via `frozenTargetFromText` (CO-52). So the reward-hacking signal is detected by
  string-matching prose and visible only to whoever runs a rig.
- **Applying means:** a project-level tally (per worker, per attacker) in the console — the rigs
  prove the number is cheap and non-zero (gauntlet smoke: `freeze_refused 8`); a `target` field on
  the event (then delete the regex); a changelog/event filter preset (the machinery exists in
  `web/src/configLog.ts`).

### 6.5 Two token ceilings because the engine ceiling queues rather than stops [IN-19, SI-3, SI-51, DOC-15/OM-8]
- **Research:** OM-8 — "set the brakes before the run and verify they can physically fire; ours
  were inert for a month" (TOK1). A product brake (protect the customer, degrade gracefully) and an
  experiment brake (stop and tell me) are different mechanisms and cannot be the same code.
- **Code today: APPLIED, with the scar documented.** The engine gate is correct (soft notifies
  once/day, hard *queues*, interactive exempt, fail-open); the harness keeps its own token total
  and aborts (`calibration/runner.ts:120-127`) precisely because a queue is not a stop button. TOK1
  is ticked; `token_usage.go` is the single home for the shape, since extended for cache tokens.
- **Applying means:** nothing to fix — recorded as a trap: any future "consolidate the two
  ceilings" cleanup silently removes the experiment abort. The engine still has no *stop* semantic
  for a project that blew its budget; any unattended production seeding inherits the queue.

---

# Cluster 7 — Gates that don't gate

The corpus repeatedly describes things as merge gates or regression floors that CI does not run.
Load-bearing fact: `ci.yml` has exactly three jobs (Go, Sandbox, Web); no stack e2e, no
`e2e/experiments/**` offline tests.

### 7.1 The learning stories don't gate merges [IN-10]
- **Research:** doc 11 / §7 Tier A — the deterministic gate that "gates merges" and "establishes
  the regression floor."
- **Code today: CONTRADICTED.** No stack e2e job in `ci.yml`; the suite runs only when a human
  types `./e2e/run-stack-e2e.sh test --mock-script …`. The one thing in the corpus described as a
  merge gate is not one.
- **Applying means:** a CI job bringing up compose in mock mode and running the stack specs (cost:
  a DinD CI runner).

### 7.2 Every rig's pure offline test layer is unwired from CI [IN-11]
- **Research:** each rig separates a pure half (`report.ts`, `metrics.ts`, `directives.ts`,
  `verdict.ts`, `route.ts`, `score.ts`, `grade.ts`) from the stack half so the pure half can run
  anywhere. These pin the mock-script rule *ordering* and the doctrine three-way agreement — the
  assertions whose silent failure mode is "tripwire can never fire."
- **Code today: ABSENT from CI.** They run via `run.sh test` / `node --test`; nothing invokes them
  automatically. The resume point cites "60/65/38" as a *manual* count.
- **Applying means:** one CI job on Node ≥22.6 running the pure tests.

### 7.3 `e2e/` has no typecheck [IN-12, SI-42, SI-43]
- **Research:** stated limitation — Node strips types but doesn't check them; `e2e/` has no
  `typecheck` script, so every scoring predicate in `directives.ts`/`metrics.ts`/`verdict.ts` is
  unchecked TypeScript. `tierb/` is *excluded* from the one experiments tsconfig (a permanent scar
  from the C1+B1 directory collision), so it's the only untypechecked rig; `e2e/features/` and
  `e2e/helpers/` are outside the sweep entirely.
- **Code today: ABSENT, documented as such** (`tierb/README.md:63-66`;
  `experiments/tsconfig.json:37,43`).
- **Applying means:** an `e2e/` typecheck script; extend a tsconfig over `e2e/helpers/` (the shared
  surface every rig imports — a type error there is a hazard for all four); the tierb `.ts`-import
  incompatibility is a mechanical rewrite.

### 7.4 gofmt isn't gated and offenders have re-accumulated [PL-26]
- **Research:** the wave-1 note "worth a formatting sweep."
- **Code today:** `gofmt -l go/` reports two *new* offenders written after that note
  (`sessions_worker_filter_test.go`, `triagelab/content.go`); `ci.yml` runs `go vet` but never
  `gofmt`. The class demonstrably recurs without a gate.
- **Applying means:** add the gate.

### 7.5 The console has zero stack-level e2e specs [CO-30, R2]
- **Research:** doc 23 R2 — feature specs for the console, pre-naming the traps (happens-after not
  sleeps; delete sessions in teardown for the port pool; the stack serves a *built* image; worker
  names must not be substrings).
- **Code today: ABSENT.** `ls e2e/features/console*` → nothing; no spec touches the eight-view
  console. **The entire console — 8 views, ~646 of the web tests' worth of new surface — has no
  stack-level regression cover.** Combined with the ux-stub being deliberately out of CI (CO-27),
  the console's write paths are the least-evidenced part of a heavily-tested repo (CO-55).
- **Applying means:** the five R2 scenarios (a–e), including drag-to-wire under a real pointer
  (never tested outside jsdom — CO-45).

### 7.6 jsdom proves the fallback, never the feature; the motion instrument is out of CI [CO-45, CO-22, CO-27]
- **Research:** jsdom exercises the SMIL fallback branch only (no SMIL clock); "no test proves a
  dot travels — the capture rig is the only evidence, which is why it's committed." The frame-hash
  check (`md5sum strip-*.png | sort -u | wc -l`; 1 = nothing animates) is a portable, one-line,
  dependency-free objective check that "nothing encoded only in motion" holds — deliberately not in
  CI ("a design-review instrument, not a test suite").
- **Code today:** 1223 green jsdom tests do not prove the console animates; the only visual
  evidence is one live-stack pass and a hand-authored fixture, and R2 (the regression net) is
  unbuilt.
- **Applying means:** a live tension to surface, not resolve here — the console's only visual
  evidence comes from an instrument nothing runs automatically.

### 7.7 Five `web/src` files are invisible to grep and git-diff [RD-25, SI-52, PL-slice]
- Covered at 3.7. The three files under git's 8000-byte threshold render as `Binary files differ`
  with 0 insertions/deletions — "a PR touching the worker editor form shows a reviewer nothing at
  all." Second-order: any tool round-tripping these as text drops the NUL, collapsing
  previously-distinct cache keys with nothing failing at that moment.

---

# Cluster 8 — The operator console

A self-contained research body (design principles + motion research) that generalises well, plus
verified ticket states and one structural gap.

### 8.1 Transferable design principles [CO-1..CO-9, CO-21]
Applied in code, and reusable beyond the console:
- **Authorship is a colour** (CO-1) — let an existing free discriminator (empty `actor_worker` =
  human) own the palette's one job. Applied (`spine.tsx:49`).
- **Two structures, two views; everything else is a lens** (CO-2) — but `SpineGap` is exported and
  rendered by nothing (the export-without-consumer failure mode).
- **Identifiers mono, content prose** (CO-3) — typography-as-type-checking; the visual channel
  warns before validation does.
- **The deliberate risk + restraint clause** (CO-4) — budget distinctiveness to exactly one surface
  (the schematic) and enforce quiet everywhere else. Applied.
- **Derived-never-persisted view state** (CO-5) — "no view may hold state the config log doesn't,"
  auditable by grepping for storage calls in view components. Applied.
- **Direct manipulation as proposal, not mutation** (CO-6) — dissolves the append-only-vs-direct
  tension into a wording-and-routing discipline (every canvas write goes through the form's route;
  mandatory reason *because* the gesture is cheap; never call a forward write "undo"). Applied.
- **Mandatory rationale + the theatre test** (CO-7) — a required-field policy must cover
  *machine-initiated* writes too, or the one screen that proves the policy is where it visibly
  fails (fixed live: `topology_apply.go:197` now defaults a rationale).
- **No graph library** (CO-9) — the reusable argument is three-legged (bundle + cross-package
  lockfile cost + aesthetic fit), not just "smaller."
- **Prior-art take/avoid list** (CO-21) — reusable as a review checklist; notably "no KPI row,
  because we have no honest quality number and a KPI row would be the capturable metric C2 warns
  corrupts the loop."

### 8.2 The motion research [CO-10..CO-22]
A self-contained transferable body, mostly applied:
- Looping vs one-shot is a *semantic* choice; the four motion rules (caused / terminates / N
  discrete ≠ a stream / nothing encoded only in motion) — rule 1 is a lint on *static* glyphs too,
  a generalization the live pass discovered (an unwired pip drew a chevron into empty canvas).
- Reduced motion as a *substitute that can be better* (persistent border + `NEW` chip), offered to
  everyone as a "calm" preference.
- Failure is a state painted from data, not a timer (survives reload/remount/screenshot).
- One integer (`last_seen_seq`) derives four renderings, defended by a test asserting the component
  does *not* grow its own storage key (a guard test for an architectural rule — rare and cheap).
- Ticking durations tick only when the elapsed *is* the operator's decision input; `rate_limited`
  does **not** count down because the row carries no retry-after — declining to fabricate the
  feature's input and citing the project's own rule (needs a backend `retry_after` column to build
  it honestly).
- Time gaps rendered as gaps, bucketed never scaled — flagged as the line's one *original* design
  contribution ("no design system documents this") and **the one not wired up** (`SpineGap` has no
  consumer; CO-19/CO-2).

### 8.3 The fixture rig as a research product [CO-23..CO-28]
- Populated-fixture review as a method surfaced 12 defects empty states couldn't (CO-23).
- The rig sits next to the "fixture invents the schema it asserts against" trap and its own README
  admits it mirrors the *reader* (CO-24) — see 3.1; the highest-leverage change is a capture mode.
- The rig shoots no chat view — R1's own dark-mode validation was done by a throwaway harness that
  was then deleted, so the ~18 chat components have theme colours no rig can see (CO-25).
- Fixture artifacts annotated as impossible states (`running 3/1` against `max_instances:1`) —
  worth generalising; hides a secondary find (the chart silently renders n>max) (CO-26).

### 8.4 Storyboard A — an entire design section dropped at the design→work-plan boundary [CO-39, CO-40]
- **Research:** §4 specifies the six-screen first-run journey (empty chart as hero; each seed as its
  own miniature schematic with `CONTROL`/`WORK`/`INSTRUMENT` eyebrows; live redraw as you type;
  preview shown twice as chart *and* list; a "Wake it now — send a test event" button called "a
  real gap, not a nicety"). K1 made the first-run state load-bearing.
- **Code today: ABSENT.** `TopologyOnboarding.tsx` predates the console line entirely; no
  `layoutOrgChart`, no SVG, no eyebrows, no live redraw, no "Wake it now." The empty chart is a
  plain `<Paper>`, not the drawn canvas. **Why:** doc 15 §12's build order (W0–W7) never listed
  Storyboard A; doc 16 executed §12 faithfully; nothing downstream cross-checked the design's own §
  list against the work plan's item list. DK2's first-run button also routes to Workers, not into
  the flow.
- **Applying means:** two separable things — (1) the concrete first-run journey (§A5's "Wake it
  now" needs only `POST /agent/events`, which exists — the same wall the e2e suite hit in T4–T7);
  (2) the generalizable lesson: a build order sketched inside a design doc is where design sections
  get dropped silently; a coverage check at the design→work-plan boundary would have caught it.

### 8.5 Console ticket close-out (R1–R6) — see roll-up table. R1 done (verified). R2–R6 open, with
two wrong diagnoses (R5c, R6b) noted in "Corrections" above.

### 8.6 Console process/scar-tissue that generalises [CO-41..CO-54]
Worktree/shared-branch hazards (CO-41; same cluster as PL-54, SI-44); the one-text-node RTL hazard
(CO-42); unit-split hazards ×4 (CO-43; same as PL-25); assert-absence pins colliding with later
items, *expected not a defect* (CO-44); the four colour-sweep traps, including that the ticket's own
grep was insufficient and eyes closed the gap (CO-46); word-boundary matching (`\b` treats `-` as a
boundary, so `\bkeeper\b` matches `book-keeper` — generalises the mock-script naming trap to every
prompt-scanning heuristic, CO-50); the read-time compensation template for the `awaiting_human`
wart (CO-53); the complete approximation→confusion→fix→debt-filed arc for the Desk badge (CO-54).

---

# Cluster 9 — Status rot as a live class

The same defect as silent success, aimed at the docs themselves: a confident statement true when
written that silently stopped being true. (lesson-class-1 and -6.)

### 9.1 Docs asserting missing capabilities that exist [RD-23]
- **Research:** four stale claims in `docs/18`, all "this doesn't exist" when it does.
- **Code today: OPEN, four sub-claims confirmed:** `snapshot_ttl_days` called "inert … agentd runs
  no reaper" at `docs/18:68,503` while `:559` in the *same file* documents the env var driving that
  reaper; "no `GET /agent/images` route" while `httpapi.go:305` registers it; `.env.example:72-75`
  describes the pre-GC port lifecycle; `installations/README.md:74-75` claims `.claude/` is
  gitignored while `git check-ignore` says it isn't.
- **Applying means:** one commit deleting the false claims and repointing the stale link.

### 9.2 Docs that contradict the code (the code wins) [RD-31, PL-29, PL-30, PL-31]
- **Research:** `gc.go`'s "nothing a user can see is lost" (RD12); `dispatch.go:351`'s "the
  backstop" (RD7 — by that line the lease is already released); `router.go:513-516`'s at-most-once
  justification broken by `ReleaseSessionLease` ignoring `RowsAffected`; `snapshot_reaper.go:21`
  implying `last_resumed_at` bears on expiry (RD9). Plus `resolveLaunchImage`'s two opposite failure
  contracts side by side (PL-29), and B2's dead precedence going live unannounced so a misconfigured
  `base_image` now fails *every* session (PL-31).
- **Code today: OPEN.** Two confirmed directly (`leases.go:69-80`; `snapshot_reaper.go:21`).
- **Applying means:** each prose fix rides with its item — a coupling rule as much as a work item.

### 9.3 Status-line rot as a corrected class [IN-31, IN-1, IN-8, IN-9, SI-27]
- **Research:** three research docs carried "nothing is built" headers false for days (corrected
  2026-07-29 at the top). But the bodies stayed stale: doc 10 §6's checklist unticked under a BUILT
  header; doc 10 §7's questions "open" though decided in work-plan 13; doc 11 §4's table lists 8
  stories where 9 ship; the runbook's go-checklist unticked despite a run.
- **Applying means:** a status convention where the checklist and the header can't disagree — e.g.
  work-plan 13 is the only place with checkboxes and design docs link to it (the healthier pattern
  CLAUDE.md already states: log beats doc, code beats log).

### 9.4 The method that works: correct the agent-facing file in the same commit as the finding [RD-35, PL-55]
- **Research:** `CLAUDE.md`'s stale CI warning was corrected in the same commit that recorded it
  ("since every agent reads that file"). Docs also went stale "in the dangerous direction" — telling
  readers a setting works when it does nothing (`AGENTKIT_EMBEDDING_BACKEND`). The verification
  method is the transferable part: `docker compose config` substitution would have caught the drop.
- **Applying means:** `docker compose config` substitution checks for every forwarded var; the GCP
  record (CLAUDE.md aligned to MIGRATION.md rather than to reality) still wants human confirmation.

---

# Cluster 10 — The trap register

~12 deliberate decisions defended only by a comment and a test, which a well-meaning refactor would
"fix," reversing the decision. Nothing registers them as a set. (lesson: structural-not-a-branch
invariants, PL cross-cutting note 2.)

- **`Persona` stays empty on routed jobs** — setting both gives one session two worker identities
  that can drift; the single most "improvable-looking" decision in the engine (PL-28).
- **The budget gate fails *open*** — a budget the store can't evaluate logs and runs, because
  stopping a whole workforce on a Postgres hiccup is the larger harm; a reviewer would naturally
  "close" it (PL-22, RD-33).
- **A capacity failure is never stored** — it would be guaranteed to go stale; the e2e asserts
  `create_error` is *empty* after a capacity failure, "exactly what a later refactor would improve
  away" (PL-57).
- **No gorm `default:` tag on any column where 0/false is meaningful** — DDL defaults go in
  migration SQL, defaulting in `normalize()`; applies to every later table with 0-meaningful columns
  (PL-57, SI-32).
- **DST correctness rests on `scheduled_for` being a local wall-clock minute**, not a timestamp —
  nothing in the schedule path would look wrong to someone who changed the key's type (PL-36).
- **The lease reaper keys on the lease, never on session status** — `running` is the correct steady
  state for a resumable session; renewal uses `UpdateColumn` so it never bumps `updated_at` (which
  the idle-archive loop reads) (PL-34).
- **`schedule_firings` methods are named to dodge the conformance classifier** — a name-based
  heuristic reads "Schedule" as configuration; renaming for clarity trips a test whose message won't
  explain why (PL-35).
- **Interactive sessions are exempt from every gate structurally, not by a branch** — so the daily
  token budget does not bound interactive chat at all; the first production user is a human chatting
  (PL-33).
- **Provisioning-vs-job streak is structural, not a classifier** — a job that ran and failed can't
  decrement the schedule-disable streak; a refactor converting it to a branch breaks the guarantee
  (PL-58).
- **The engine gate queues; the experiment needs its own stop** — consolidating the two ceilings
  removes the experiment abort (SI-51, cluster 6.5).
- **Compose-once: `composed_prompt` runs verbatim every turn, deliberately not combined with the
  provider** — combining would let a mid-job `worker_prompt_write` leak into a running session
  (PL-28).
- **Depth collapses to 0 if any future job starter forgets to stamp `session_id` before the first
  message** — the loop-floor runaway protection would become dead code; held by a hook and three log
  entries, nothing structural (PL-32).

**Applying means (whole cluster):** decide whether per-case comment+test is the accepted permanent
design, or introduce a register / category so a refactor sees them as a set. Several (PL-32, IN-30)
could become cheap grep-style guard tests.

---

# Cluster 11 — Spec decisions parked for Kai, never taken

Each was implemented as specified and flagged for a human decision that hasn't happened. These are
genuinely yours, not an executor's.

- **§6.2.4 markers are forgeable** [PL-9, DOC-slice] — event text containing `--- event text ends
  ---` closes the untrusted-data block early; the fix (per-job nonce or line-escaping) changes the
  normative marker text and re-pins the compose bytes. Directly load-bearing for the
  marketing-manager's first job (reading email); the live smoke's injection win did *not* attempt
  marker forgery.
- **Preset-append vs replacement** [PL-8] — the composed prompt is `append`ed to Claude Code's
  stock `claude_code` preset, so the preamble's "you may be running with no human present" competes
  with preset text written for an interactive CLI. The one live observation tested conflict against
  injected *event text*, not against the stock preset.
- **`systemPrompt` accepted but never persisted/used** [PL-7] — `POST /agent/session` forwards it
  and, with no `Worker`, discards it silently.
- **Nothing moves a delivery out of `awaiting_human`** [PL-12, CO-53, RD-slice] — the session
  resumes but the job-history row stays parked with `ended_at:0`; a resume hook would re-grow the
  approval machinery §9 deleted.
- **A disabled worker's refused deliveries record `failed`** [PL-13] — and `enabled:false` is the
  *intended* retirement path (no `worker_delete` tool), so every trigger afterward accumulates a
  failed-looking row; the status vocabulary is closed and pinned, so a `refused` value is a spec
  amendment. Coupled to PL-46 (no `subscription_update`; possibly-unintended §9 omissions).
- **The sqlite fallback silently drops product columns** [PL-15] — `worker`/`composed_prompt`
  absent, so router/schedules/core-MCP/settings degrade with nothing failing at use time. Decide:
  extend sqlitestore, or make agentd refuse to start the product layer on it.
- **`POST /agent/project-token` 501s + route-name collision** [PL-21] — blocks headless event
  posting, which is how any real external integration (email, webhooks) would feed the marketing
  manager.
- **Session archive snapshots accumulate forever** [PL-10, RD-14] — the reaper sweeps only the §13
  catalogue; `snapshot_handle` rows carry no TTL, and the idle-archive loop now *increases* their
  production rate. Same asymmetry on delete: the cascade destroys the index while the bytes stay on
  the bill (`ArtifactStore` has no Delete).
- **`image_create` overwrites the calling session's own resume handle** [PL-11] — publish and
  resume-point become the same object; now interacts with the idle-archive loop in a way nobody has
  traced.
- **The proposed §13.5 `base_image` amendment was never applied** [PL-30] — behaviour implemented,
  spec text not; the code is the authority until then.
- **Postgres credentials undocumented + volume initialises once** [RD-22] — zero `POSTGRES` vars in
  `.env.example`; setting a password after first `up` re-renders `DATABASE_URL` but not the DB, and
  agentd dies with raw gorm text.
- **Config-event guard is opt-in and never armed in production** [PL-18, RD-13] — every track has
  landed (precondition met) but the reaper documents it must *not* run with the guard armed, so
  flipping it is no longer one line; needs the GC-write escape (PL-19) first. The fold has no
  production caller and diverges from reality in three ways — and "the config log is what the
  doctrine work treats as the record of truth."
- **The conformance exemption list grew 5→13; the predicted GC-write escape was never built**
  [PL-19] — the mechanism is healthy but the prediction that reasoned exemptions would substitute
  for structure came true three times over.

---

# Cluster 12 — Process & tooling lessons

Currently prose in briefs; several could be mechanical checks. (lesson-classes 5, 8.)

- **Worktrees keep coming up on stale wip bases** — five occurrences (SI-44, CO-41, PL-54). A
  `git merge-base --is-ancestor product-layer HEAD` assertion in `run.sh` or a pre-commit hook
  would make it mechanical.
- **`git stash` is shared across worktrees; never `--amend` on a shared branch; stage narrowly**
  (CO-41, PL-54) — promoted into doc 23's EXECUTION RULES; generalises to any multi-agent worktree
  workflow.
- **Executor budget-death is a distinct failure mode from code failure** (CO-47) — the salvage
  protocol (quarantine on a named branch, mark UNVALIDATED, re-run from base, let the re-run
  adopt-or-replace) recovered ~85% of the work.
- **Parallel-executor plans reliably collide at shared registration blocks and independently-invented
  shared constants** (CO-48) — fix pattern: unify onto one, then pin the duplicate's remaining
  constant equal to the survivor by test.
- **Making a hardcoded limit configurable for testability paid within the hour** (PL-49) — the port
  range change exercised a rotted failure path immediately and found a worse bug next door; candidate
  constants: `memorySnippetLen`, `memoryRRFK`, the 200-row list caps, `DefaultMaxInstances`.
- **Per-run overrides must restore on failure** (PL-50) — under `set -e` a `RETURN` trap doesn't
  fire on exit, so every failing scripted run left its mock script loaded for the next; the class
  (shell state leaking between runs of a shared stack) is unguarded elsewhere.
- **`run-stack-e2e.sh clean` and the Docker-removal race** (PL-51) — script fixed; the *engine-side*
  half is open (agentd still fatals on a transient reclaim error, a production concern on any restart
  during container churn).
- **Branches testing against shared Postgres must land migrations promptly** (PL-53) — the
  concurrency hazard is fixed (advisory lock); the process hazard remains (parallel-agent model still
  in use). CLAUDE.md carries the warning.
- **e2e session leakage: no `track()` helper; Playwright's positional arg is a substring filter**
  (PL-52, CO-30) — ~11 leaked sessions per run, each holding a row and a port until idle-timeout.
- **The schedule-resilience test costs a third of suite time**, product-floored (a firing is one
  wall-clock minute, no catch-up) — can't be optimised away without changing the product (PL-56).
- **A property test that iterates the registry gives every future entry the check for free**
  (SI-49) — `TestRegisteredBundlesAreProjectAgnostic` + render-time cron validation; "the cheapest
  quality mechanism in the product layer."
- **Assert on happens-after signals, never sleeps** (SI-41, PL-2) — the rigs do this
  (`followOnDeliveries`); four non-rig specs still sleep (`schedule-resilience`, `config-and-workers`
  2s, `port-pool` 45s/15s), some legitimately timing tests, some maybe not.
- **Every read-modify-write helper must carry ALL fields** (SI-36, PL-slice) — a whole-object PUT
  helper that omits `frozen` silently thaws; nothing fails when a new field is added and a helper
  isn't updated. A typed exhaustive `WorkerBody` would make the compiler carry it. Same shape:
  whole-object PUT + zero-means-keep patch requires read→overlay→write-whole discipline (SI-37), and
  zero-meaningful settings (`daily_tokens_*`, `snapshot_ttl_days`) are *unreachable* through
  `SettingsPatch` — would bite the first topology wanting to ship a token cap.

---

# Roll-up tables (already-ticketed backlogs, verified status)

Included so nothing is invisible; individual generalising items from these are surfaced above under
their clusters.

## Doc 22 readiness — RD1–RD29 (verified: 3 fixed, 26 open)

| ID | Subject | Claimed | **Verified** |
| --- | --- | --- | --- |
| RD1 | Schedule self-disables on transient DB error | fixed | **fixed** (3 sites) |
| RD2/2b | Token ledger counted only uncached input | fixed | **fixed** end-to-end |
| RD3 | `memory_create` reports embedded while storing none | open | **open** (→3.9) |
| RD4 | Worker change recorded as human edit | fixed | **fixed** (both seams) |
| RD5 | **Session delete destroys conversation, one click, no confirm** | open · blocker | **open** |
| RD6 | **Crash mid-turn loses the model's response** | open · blocker | **open, worse** (replay drains+deletes buffer) |
| RD7 | Job wedges in `running` forever, holds two slots | open · blocker | **open** |
| RD8 | Container with no session row leaks a port forever | open · blocker | **open** |
| RD9 | Snapshot in daily use reaped out from under the user | open | **open** (doc's path wrong) |
| RD10 | Two drains double-dispatch one delivery | open | **open** |
| RD11 | Missed schedule occurrences vanish (by stated design) | open | **open** |
| RD12 | Idle-archive mislabels artifacts as lost | open | **open** |
| RD13 | Config-log append-only enforced only in tests; fold no prod caller | open | **open** (→11) |
| RD14 | Every archive cycle orphans a full image archive | open | **open** (→11) |
| RD15 | "This ran" without "what it said" | open | **open** (→3.3) |
| RD16 | Transcript ordering is a coin toss within a second | open | **open** |
| RD17 | **No way to fire the first job from the UI** | open · blocker | **open** |
| RD18 | **Mock mode invisible, and it's the default** | open · blocker | **open** |
| RD19 | Event matching no subscription vanishes silently | open | **open** (→3.4) |
| RD20 | Failure reasons never reach the user | open | **open** (→3.3) |
| RD21 | Doctrine reaches no user today | open | **open** (→5.1) |
| RD22 | Postgres creds undocumented; volume inits once | open | **open** (→11) |
| RD23 | Docs assert missing capabilities that exist | open | **open** (→9.1) |
| RD24 | Restart makes the model remember a turn the user can't see | open · blocker | **open** |
| RD25 | Five UI files invisible to grep/git-diff | open | **open** (→3.7) |
| RD26 | Dropped SSE reports a truncated answer as complete | open | **open** (→3.5) |
| RD27 | Attention-route failure → "Nothing is waiting on you", badge 0 | open | **open** (→3.5) |
| RD28 | Failed load renders onboarding over an established project | open | **open** (→3.6) |
| RD29 | Wire-shape drift has no guard (no drift *yet*; Worker is 12 fields not 13) | open | **open** |

Plus three un-numbered readiness items: RD-30 (further unclassified-error sites, found and reported,
not fixed — no handle to be ticked); RD-32 (three named sweeps from the RD1/2/4 work that were never
run: error paths that discard available data; specs whose arithmetic mirrors the implementation's;
comments justifying behaviour by conditions the wiring no longer permits); RD-34 (Kai's local tree
only — `secrets/gcp-key.json` is a directory).

## Console close-out — doc 23 R1–R6 (verified)

| ID | Subject | **Verified** |
| --- | --- | --- |
| R1 | Chat-side dark-mode sweep | **DONE** (commits `63b17a6`+`6f2ecfa`; 1223 tests confirmed) |
| R2 | e2e feature specs for the console | **ABSENT** (→7.5) |
| R3 | Turn the live tail on (shell wiring) | **DONE 2026-08-06** by the parallel session (`054121e`+`3be884f`; `App.tsx:267,285` pass `refreshMs`) — landed *after* this inventory's sweep |
| R4 | Document the two stack boot traps | **DONE 2026-08-06** by the parallel session (README-stack.md "two traps" §, CLAUDE.md pointer) — landed *after* this inventory's sweep |
| R5a | Retire the hand-copied token tables | **NOT DONE** (two copies remain; `CONSOLE_TOKENS` exported, unused) |
| R5b | Stale route comment | **NOT DONE** (`index.ts:257-259`; route is mounted) |
| R5c | "non-UTF8 bytes" | **NOT DONE — diagnosis wrong** (→Corrections; it's the NUL idiom) |
| R6a | `rate_limited` countdown | backend genuinely absent (no `retry_after`) (→CO-18) |
| R6b | Memory selector as chips | **CONTRADICTED — chips shipped** (→Corrections) |

## Self-improvement open items — L3H / L3M (verified: neither started)

| ID | Subject | **Verified** | Cluster |
| --- | --- | --- | --- |
| L3H(a) | Empty assistant reply → recorded abort, not a 180s hang | **not started** (3 rig files + engine root) | 2.2 |
| L3H(b) | No throw may bypass the report | **not started** (one-line escape hatch) | 2.3 |
| L3H(c) | Persist transcript as the run proceeds | **not started** | 2.4 |
| L3M | A discriminating manifest (with the offline-difficulty tension) | **not started** | 2.5 |

---

*End of inventory. Traceability: every bracketed ID resolves to a full three-part entry with
file:line evidence in the six reader reports from the source conversation.*
