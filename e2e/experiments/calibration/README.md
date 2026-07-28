# The calibration rig

*Work plan [`13-work-plan-self-improvement.md`](../../../docs/product/13-work-plan-self-improvement.md)
item **L3R**, building the harness for
[`14-calibration-runbook.md`](../../../docs/product/14-calibration-runbook.md).*

Thirty hypotheses whose answers the harness already knows, through the same org chart wired three
ways. The scorer is a **fact, not a judge** — which is the whole reason the first real-model
experiment is this one and not a poetry tournament. A flat accuracy line here means *"the loop did
not improve investigation"*, never *"the instrument is blind"* (runbook §1).

```sh
# the mock smokes: free, offline, prove the machinery
./e2e/run-stack-e2e.sh up mock
./e2e/experiments/calibration/run.sh run smoke-4
./e2e/experiments/calibration/run.sh run doctrine-smoke-4   # the doctrine axis (DR1)

# the offline unit layer, no stack
./e2e/experiments/calibration/run.sh test
```

## Running the real thing (orchestrator + Kai, attended)

Work plan 13's L3 approves **subscription OAuth, attended runs only**. L3X is the run itself; this
directory is the harness it uses.

```sh
./e2e/run-stack-e2e.sh up subscription
CALIBRATION_LIVE_RUN=1 ./e2e/experiments/calibration/run.sh run calibration-30
```

- `CALIBRATION_LIVE_RUN=1` is a deliberate speed bump, not security. Without it the run refuses.
- `run.sh` refuses a live config against a mock stack and a mock config against a live stack. Both
  mistakes are expensive in opposite directions.
- Arms **A and B** run by default — the runbook's minimum honest run. To add the sham control:
  `--arms A-critic-live,B-critic-off,C-critic-sham`.
- A shorter probe first is cheap and wise: `--limit 3 --arms A-critic-live`.
- A second seed is a second manifest: copy `manifest-30.json`, change every `seed`, change the
  config's `datasetDir`. The rig has no `--seed` flag on purpose — a calibration's seeds belong in
  a committed file, not in shell history (runbook §2: *fixed seeds recorded up front*).

### What to watch while it runs

The runner prints one line per hypothesis: what the investigator said, what the honest answer was,
and what the frozen fact-checker called it.

| Watch for | What it means |
| --- | --- |
| `said unparseable` appearing repeatedly | the investigator lost the output contract — most likely the critic rewrote it away. Real signal, not a bug: it is recorded and counted, never repaired. |
| `checker mismatch` where the harness says the answer was right | the in-project scoreboard and the harness disagree. Watch `checker_agreement` in the final table. |
| an arm answering `effect` to everything | the failure this experiment exists to catch (runbook §5, last row). |
| the run stalling on one hypothesis | a job is wedged; `docker compose logs agentd`. |

### Abort criteria (runbook §4) — all three are automatic

1. **Ceiling hit.** A `rate_limited` delivery, *or* the runner's own token total crossing
   `dailyTokensHard`. The second one is load-bearing — see the warning below.
2. **More than 3 consecutive provision failures.**
3. **Any successful write to the fact-checker.** The runner re-reads the checker's prompt and
   `frozen` flag after every hypothesis; a change stops the arm immediately. That is a harness bug,
   not a result, and the runbook says stop and fix.

An abort ends that arm, records the reason in the report, and lets the remaining arms run. The
process exits non-zero if any arm aborted.

### After the run

`reports/calibration-30.run-log.json` holds `GET /agent/events` + `GET /agent/config-events` per
arm project — runbook §4's record. Copy it, the report JSON and the markdown into
`docs/product/runs/<date>-calibration/`, and append the summary to the runbook (§6's last box).
`*.run-log.json` is gitignored *here* so smoke runs do not accumulate; the dated record is where it
belongs.

## Two token ceilings, on purpose

`daily_tokens_hard` is set on every arm project (runbook §4) **and** the runner keeps its own
running total. History: this rig discovered the engine's three token readers queried a shape no
stored row carries, so the project-level ceiling could not fire; TOK1 fixed the readers against a
captured envelope (`go/agentdb/token_usage.go` — see the work plan's Discovered Issues Log) and
the gate is now live and e2e-tested. Both ceilings stay because they do different jobs: the engine
gate **queues** further dispatches inside a project (the right product behaviour), while the
runner's own count **aborts the experiment** (runbook §4's stop criterion). A ceiling that queues
is not a stop button.

## What the mock smoke proves — and what it does not

`configs/smoke-4.ts` runs four hypotheses, one per trap kind, through arms A and B against the
scripted mock model. It exercises every mechanism the live run depends on: the apply, the frozen
checker, the whole-object settings PUT, arm B's deleted subscription, a CSV reaching the
investigator, a verdict parsed from a **deliverable** (not a transcript), the critic's refused
freeze attempt and its rewrite, the rewrite changing the next hypothesis's answer, conclusion+truth
reaching the checker, per-hypothesis session sweeping, and every metric registering non-zero.

**Its numbers are meaningless as a result.** Arm A scores 4/4 and arm B 1/4 because the mock script
says so. `docs/AGENTS_RESEARCH.md` §7: Tier A proves transmission, never discovery. The report
markdown says the same thing on its own face, and the committed artifact in `reports/` is there as
a machinery fixture, not as evidence about org charts.

What it buys is worth the run: **a failure in the live calibration is then a model failure rather
than a harness failure.**

## The arms

| Arm | Difference from the applied topology | Isolates |
| --- | --- | --- |
| `A-critic-live` | none | the thing under test |
| `B-critic-off` | the critic's subscription is deleted after apply | no-learning baseline (MR-3 at scale) |
| `C-critic-sham` | the critic's prompt is replaced by `sham-critic@v1`'s charter | churn vs learning (playbook C7) |
| `A-critic-live-doctrine-v1` | `doctrine-v1` is written into the project prompt after apply | common-sense operating rules (doc 20, DR1) |

Each difference is one **ordinary operator mutation made after the apply**, so all four arms render
from the identical topology and differ in exactly one nameable way. B deletes the *subscription*
rather than the worker, so even the composed prompts stay identical.

## The doctrine axis (DR1)

`docs/product/20-operations-doctrine.md` §3, decision D5: an operations doctrine is **versioned
bytes**, not engine code. `ProjectSettings.SystemPrompt` already reaches every composed prompt as
the `project prompt` section and already writes through a config-logged path, so an arm "runs under
doctrine" by having the block written into that field after the apply — read current settings,
overlay `system_prompt`, whole-object PUT, exactly as `daily_tokens_hard` is set. The A/B lever is
that mutation withheld.

- `doctrine.ts` reads the canonical file and cuts the block out of it: everything from the
  `=== operations doctrine v1 ===` marker line (inclusive) to end of file, verbatim. **A missing
  marker is a hard failure**, never a fallback to "the whole file" — the header above the marker is
  editorial ("Status: every entry CANDIDATE") and must never reach a worker's prompt.
- `configs/doctrine-smoke-4.ts` runs `A-critic-live` against `A-critic-live-doctrine-v1` over the
  smoke's four hypotheses. Both score 4/4, and **the equality is the point**: an authored delta
  between doctrine and no-doctrine would be a number about the mock script. Whether doctrine changes
  how an org performs is a live question, and doc 20 §2 keeps every entry CANDIDATE until a real run
  answers it.
- The delivery assertion is a **tripwire**, not a delta. `cald-invest`'s rules are partitioned on its
  identity phrase and that partition is split with `absent:` on a doctrine-v1 sentence: with the
  block present the rule is skipped and the request falls through to the shared per-hypothesis
  rules; with it missing the investigator returns prose with no contract line and the whole arm
  records `unparseable`. So 4/4 with `unparseable` 0 is only reachable if the block was in the
  **composed prompt** — reading the settings row back would have proved storage and nothing more.
  Verified non-vacuous by disabling the injection and watching the arm collapse to 0/2.
- The identity phrase has to be the partition and the doctrine phrase the split, never the reverse:
  the block rides *every* worker in the arm, critic and frozen checker included, so a rule keyed on
  a doctrine phrase alone would answer the checker's requests too.
- The report names the version per arm (`doctrine v1` / `doctrine none`) — and says nothing at all
  when a run has no doctrine axis, which is what keeps `smoke-4`'s committed artifacts a fixture
  across this change.

## Ground truth, and where it is allowed to go

`go/hypolab` returns the dataset and the verdict as separate values; `go/cmd/hypolabgen` writes the
CSVs and one `truths.json` beside them. `truths.json` is read by the runner and **never enters a
project** — except one field of it, inside one event, addressed to the frozen fact-checker
(`AGENTS_RESEARCH.md` §4, runbook §2). The dataset event carries no truth at all; a unit test
asserts both directions on the generated text.

### The underpowered trap in the scorer

`hypolab`'s verdict for an underpowered scenario is `effect: true` — the effect is real — while the
honest report is still **`no-effect`**, because 40 observations cannot support the claim. hypolabgen
therefore records both: `verdict` (what is true of the generating process) and `expected_verdict`
(what a competent investigator should say). **Metrics score against `expected_verdict`.** Scoring
against `verdict` would mark restraint wrong and reward overclaiming on 4 of the 30 hypotheses —
in an experiment whose most important number is a false-confirmation rate.

## The output contract

There is no verdict to score unless the investigator emits one, so its prompt and every task event
carry the same contract: a final line reading exactly `VERDICT: effect` or `VERDICT: no-effect`.

- It reaches the prompt through the topology's `covariates-hint` answer, which is the only channel
  `hypothesis-lab@v1` offers into the investigator's charter.
- It is **parsed from the deliverable** — the last assistant message of the investigator's job —
  never from `worker.finished` text, which is the whole transcript and contains the contract as
  written by the harness. (B1's live foot-gun.)
- A conclusion with no contract line is recorded as `unparseable`, counted, and left in the
  accuracy denominator. It is never guessed at from prose.
- If the critic rewrites the contract away, the unparseable count is how the run says so. That is a
  finding, and the rig will not repair it.

## Files

| Path | What |
| --- | --- |
| `spec.ts` | The config shape: arms, manifest, window, ceiling, mode. |
| `arms.ts` | The arms and the two strings every config shares. |
| `doctrine.ts` | **Pure-ish.** Reads `docs/product/doctrine/doctrine-<v>.md` and cuts the injectable block out of it at the marker line. |
| `text.ts` | **Pure.** The dataset event and the check event — the only place truth is written into event text. |
| `verdict.ts` | **Pure.** Verdict and checker-call parsing, and the event markers. |
| `metrics.ts` | **Pure.** Every runbook §3 number, the tables, the artifact. Imports only `../report`'s arithmetic. |
| `truths.ts` | Loads `truths.json` + the CSVs, refusing a directory that does not match its own checksums. |
| `runner.ts` | The half that touches the stack: apply → ceiling → wire → per-hypothesis loop → sweep. |
| `calibrate.ts` | CLI: load config, run arms sequentially, write artifacts, measure the port pool. |
| `configs/` | `smoke-4` + `doctrine-smoke-4` (mock) and `calibration-30` (live). `run.sh list` enumerates them. |
| `manifest-30.json` | The runbook §2 scenario list with **every seed pinned** — the record. |
| `manifest-smoke-4.json` | The smoke's four. |
| `datasets/` | Generated by `run.sh`, gitignored. Deterministic from the manifests. |
| `reports/` | The committed artifacts. `*.run-metadata.json` and `*.run-log.json` are the volatile halves. |

Compilation is the C1 rig's: `run.sh build` delegates to `../run.sh build`, which owns
`experiments/tsconfig.json` and already sweeps this directory. One tsconfig, one `dist/` — the
C1↔B1 collision in the work plan's Discovered Issues Log is what that rule exists for.

## Rules the rig obeys

- **Polls, never sleeps.** Every wait is on a delivery row, an event row or a config record.
- **A hypothesis is not over when the investigator stops.** In arms A and C the critic's rewrite
  lands in a job that starts *after* the investigator finishes; the next hypothesis would race it.
  (C1's round-boundary lesson.)
- **Sessions are swept per hypothesis**, not per arm. 30 × 3 sessions × 3 arms against a pool of
  100 means end-of-arm sweeping is the same as no sweeping.
- **Arms run sequentially.** One agentd, one port pool, one model.
- **The report is deterministic**: no timestamps, project names, session ids or event ids, every
  number rounded to 6 decimals. Two runs of `smoke-4` produce byte-identical `report.json` and
  `report.md`; that diff is how the claim is checked, and a unit test pins the exclusion.
- **The mock script is restored on exit**, including failing runs — it is agentd-wide boot
  configuration.

## Mock-script discipline (`e2e/mock-scripts/calibration-smoke.json`, and its doctrine superset)

Rule order is **checker → critic → investigator**, and it is load-bearing:

- Checker rules key on `[CAL-CHECK-<id>-YES|NO]`, a marker the harness stamps on the check event.
  It encodes the hypothesis and the *stated* verdict — both facts the checker legitimately
  receives — and deliberately **not** whether the harness thinks the answer is right, which would
  make `checker_agreement` a tautology.
- The critic's rule keys on `You review cala-invest`, a phrase that exists only in its own composed
  prompt. Its body contains the investigator's entire transcript, so it must sit above every
  investigator rule (the body-match trap).
- Investigator rules come in pairs per hypothesis, split with `absent: "[CAL-CONTROL-RULE]"`: the
  naive answer before the critic's rewrite, the controlled one after. The rewrite arriving in the
  composed prompt is what flips them, which makes arm A's accuracy curve a **delivery** assertion.
- The critic's freeze attempt names the checker but never its identity phrase, so it cannot
  contaminate the checker's rules (L2's finding, generalised).

`e2e/mock-scripts/calibration-doctrine-smoke.json` is a **superset** of that file — every rule it
has, plus the doctrine arm's critic rule and the doctrine tripwire, in that order. A separate file
rather than an extension of the original, so `smoke-4` keeps running against bytes DR1 never
touched. `doctrine.test.ts` pins the three-way agreement between the canonical doctrine file, the
phrase `doctrine.ts` exports, and the rule in this script: if they drift, the tripwire silently
stops being able to fire, and a delivery assertion that cannot fail is decoration.
