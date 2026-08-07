# The comparison rig

*Work plan [`13-work-plan-self-improvement.md`](../../docs/product/13-work-plan-self-improvement.md)
item C1, executing the composition playbook's P5.*

One task. N org charts. M repetitions each. A ranked table with variance.

This is the harness that turns **"which arrangement of workers is best for this task?"** from a
debate into a query ([`12-composition-playbook.md`](../../docs/product/12-composition-playbook.md)
§4). It applies each topology to its own throwaway project, drives the same task through all of
them by emitting events, reads the outcome out of the event and config logs, and prints an
arm × metric table.

```sh
./e2e/run-stack-e2e.sh up mock                                    # once
./e2e/experiments/run.sh compare actor-critic-vs-sham-vs-solo     # the demo comparison
./e2e/experiments/run.sh test                                     # the offline unit layer, no stack
```

## The Tier A caveat — read this before quoting a number

Everything the rig can run **today** runs against the scripted mock model, and in mock mode each
arm's behaviour is *authored into the mock script*. A ranking produced here therefore proves the
**machinery**: that a topology can be applied, driven, observed, scored and compared, and that the
arms are distinguishable at all. It proves **nothing** about which org chart is better in the
world.

That is not a limitation to be worked around; it is the division of labour in
[`AGENTS_RESEARCH.md`](../../docs/AGENTS_RESEARCH.md) §7. Tier A is the deterministic gate — binary,
free, and it gates merges. Tier B is the graded instrument — a number with variance, run on demand,
never a CI gate. **Mock results prove transmission, never discovery.** The demo report in
`reports/` says so on its own face, and so does every table the rig prints.

## What the demo comparison shows

`configs/actor-critic-vs-sham-vs-solo.ts` runs one two-round filing task through three arms:

| arm | topology | what it is |
| --- | --- | --- |
| `actor-critic` | `actor-critic@v1` | a critic that reads the work and rewrites the actor's prompt with a diagnosis |
| `sham-critic` | `sham-critic@v1` | the placebo: **wiring pinned byte-for-byte to actor-critic's** (`reflect.DeepEqual`, `go/topology/shamcritic_test.go`), a critic that only reorders the actor's instructions and says so honestly |
| `solo` | `solo@v1` | one worker; nobody rewrites anything |

The two critic arms differ in the critic's **words** and nothing else. That is what makes
actor-critic *minus* sham isolate diagnosis from churn — playbook **C7**: an improvement claim that
has not beaten the sham critic is motion, not learning. In the report the sham matches the genuine
critic exactly on `prompt_writes` and on `prop:output-changed`, and scores zero on
`prop:headline-rule`. The control working is the interesting part of the table, not the winner.

`prop:prompt-intact` is there as the flat metric: it is 1.0 for every arm, and a metric that fails
to discriminate is worth printing rather than hiding.

## Shape of a comparison config

A config is a TypeScript module exporting a `TaskSpec` (see [`spec.ts`](./spec.ts)). It is TS
rather than JSON for one reason: the output-property checks are **predicates**.

```ts
export const spec: TaskSpec = {
  id: 'actor-critic-vs-sham-vs-solo',   // artifacts are named after this
  description: '…',
  mockScript: 'e2e/mock-scripts/experiments-compare.json',  // run.sh loads this into agentd
  rounds: [TASK_TEXT, TASK_TEXT],       // inbound event text per round; length = round count
  repetitions: 2,                       // independent runs per arm; below 2 there is no spread

  arms: [{
    id: 'actor-critic',                 // the row key in the report — keep it stable
    topology: 'actor-critic', version: 'v1',
    answers: { 'actor-name': 'xpa-scribe', … },
    eventType: 'xpa-scribe.task',       // what the rig emits to start a round
    primaryWorker: 'xpa-scribe',        // whose reply is "the output"
    followOnDeliveries: 1,              // the critic's job is part of the round
    afterApply: async (client) => { … },// ordinary config mutations (solo's poke subscription)
  }],

  properties: [{
    id: 'headline-rule',
    describe: '…',
    holds: (observation, previous) => observation.output.includes('Headline:'),
  }],

  rankBy: 'prop:headline-rule',
  rankDirection: 'desc',
}
```

Metrics reported for every arm: `rounds_completed`, `delivery_ok_rate`, `prompt_writes`, and one
`prop:<id>` rate per declared property. Each cell is `mean ±spread` across the repetitions.

## Files

| Path | What |
| --- | --- |
| `spec.ts` | The config shape. The seam between the two halves. |
| `report.ts` | **Pure.** Metrics, aggregation, variance, ranking, table, artifact. Imports nothing. |
| `report.test.ts` | The offline unit layer (`node:test`), fixture outcome tables, no stack. |
| `runner.ts` | The half that touches the stack: apply → drive rounds → read the logs → sweep. |
| `compare.ts` | CLI: load config, run arms sequentially, write artifacts, measure the port pool. |
| `configs/` | Comparison configs. `run.sh list` enumerates them. |
| `reports/` | The committed artifacts. `*.run-metadata.json` is gitignored — it is the volatile half. |

Two artifacts per run:

- `<id>.report.json` — **deterministic**. No timestamps, project names, session ids or event ids;
  every number rounded to 6 decimals. Two runs of the same config produce byte-identical files, and
  `diff`ing them is how the determinism claim is checked. A unit test asserts the exclusion.
- `<id>.report.md` — the same table with its legend, for reading.

The volatile facts (when it ran, which throwaway projects it used, what the port pool looked like
afterwards) go to `<id>.run-metadata.json`, which nobody diffs.

## Rules the rig obeys

- **Polls, never sleeps.** Every wait is on a delivery row, an event row or a config record.
- **A round is not over when the actor stops.** In a critic topology the rewrite lands in a job
  that starts *after* the actor finishes; `followOnDeliveries` makes the boundary real. Without it
  the config-log read would race the rewrite — intermittently, which is the worst kind of wrong.
- **Sessions hold a host port.** Every arm sweeps its project in a `finally`, and the run ends by
  measuring the pool and printing what it found.
- **Arms run sequentially.** They share one agentd, one port pool and one scripted model. A
  comparison whose answer depends on load is not a measuring instrument.
- **The mock script is restored on exit**, including failing runs — it is agentd-wide boot
  configuration, and leaving one loaded changes the model for everyone sharing the stack.

`run.sh compare` loads the script into agentd itself rather than going through
`run-stack-e2e.sh --mock-script`, because that flag is bolted to `test`, which runs playwright, and
the rig is deliberately not a playwright spec. The reload/restore discipline is copied from it.

## How L3 reuses this

[`14-calibration-runbook.md`](../../docs/product/14-calibration-runbook.md) — the first real-model
run of the hypothesis lab, **gated on Kai** — is the same skeleton with three substitutions:

| Runbook (§2–§3) | Here |
| --- | --- |
| Arms A / B / C (critic live, critic disabled, sham critic) | `spec.arms`; "critic disabled after apply" is an `afterApply` hook |
| 30 hypotheses over hypolab datasets, fixed seeds, shuffled kind order | `spec.rounds` — the per-round inbound event text |
| Per-hypothesis loop, all driven by emitted events (C6) | `runner.ts`'s round loop, unchanged |
| Accuracy curve, planted-null false-confirmation rate, confound escape rate | `spec.properties` — predicates over the round's output |
| Prompt lineage: every `worker_prompt_write` diff + rationale | already collected, per round, in `promptWrites` |
| ≥2 seeds per arm, variance reported | `spec.repetitions` and the `±spread` column |
| Ground truth lives harness-side, never in project memory (AGENTS_RESEARCH §4) | a predicate closes over it; the rig never writes it to the project |

What L3 additionally needs and this rig deliberately does **not** have: a real-model credential
mode (the runner refuses anything but a mock stack), token-spend accounting per arm, and the
per-hypothesis dated record under `docs/product/runs/`. None of those are precluded — the arm loop
and the report document both take new fields without reshaping.

`prop:` predicates are also the seam B1's Tier B grader plugs into: a grader is a property whose
verdict comes from a second model instead of a substring. The rig does not implement one and does
not stand in its way.
