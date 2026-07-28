# The triage rig (SC-1)

*Work plan [`13-work-plan-self-improvement.md`](../../../docs/product/13-work-plan-self-improvement.md)
item **SC1**, building the instrument for
[`19-scenario-library.md`](../../../docs/product/19-scenario-library.md) §3 SC-1.*

Generated support tickets whose correct destination the harness already knows, through the same org
chart wired two ways. The scorer is a **fact, not a judge**. A flat headline here means *"the loop
did not improve routing"*, never *"the instrument is blind"*.

It exists because the calibration lab (SC-0) measures analytical discipline and barely exercises
coordination — three workers, a linear flow, no routing decisions — while MAST puts coordination at
**37% of multi-agent failure**. This is the first instrument pointed at that.

```sh
# the mock smoke: free, offline, proves the machinery
./e2e/run-stack-e2e.sh up mock
./e2e/experiments/triage/run.sh run triage-smoke-6

# the offline unit layer, no stack
./e2e/experiments/triage/run.sh test
```

## The question

Doc 19 §3: **does the critic teach the dispatcher routing RULES, or routing CONFIDENCE?**

Those two outcomes look identical in an accuracy column and opposite in this rig's headline pair:

| Metric | What it catches |
| --- | --- |
| `trap_misroute_rate` | Of the **misdirection** tickets — vocabulary points at one queue, the stated facts belong to another — the share routed wrong. This falling is *rules*. |
| `ambiguity_confidence_rate` | Of the **ambiguity** tickets — no rule fits, `escalate` is correct — the share answered with a confident queue. This rising is *confidence*. |
| `over_escalation_rate` | Of every ticket that WAS decidable, the share escalated. The price of restraint, so an arm cannot win by escalating everything. |

`escalate` is a route, not a refusal to give one. That distinction is the entire ambiguity trap, and
it is stated in the dispatcher's charter, in `go/topology/triagelab.go`, and in the metrics.

## The two channels a ticket has

`go/triagelab` generates every ticket from two disjoint pools, and the disjointness is the
instrument:

- **Surface vocabulary** — the nouns a keyword router indexes (`invoice`, `outage`, `sso`).
- **Stated facts** — what the charter's content rules actually name: an HTTP status in the 500s, a
  monetary amount that does not match an agreed one, somebody who cannot sign in.

No vocabulary term appears in any fact template and no fact phrase is a vocabulary term
(`routers_test.go` pins both directions over the pools themselves, not just over sampled seeds). A
**misdirection** ticket takes its surface from a decoy queue and its facts from the true one; an
**ambiguity** ticket states no fact at all.

### The traps trap, and the record proves it

`go/triagelab` ships two reference routers — `NaiveKeywordRoute` (argmax over vocabulary; it cannot
escalate, which is its defining weakness) and `ContentRuleRoute` (the charter, mechanised). Over the
committed 24-ticket manifest:

| | plain (8) | misdirect (10) | ambiguous (6) | total |
| --- | --- | --- | --- | --- |
| naive keyword router | 8 ✓ | **0 ✓** | **0 ✓** | 8 / 24 |
| content-rule router | 8 ✓ | 10 ✓ | 6 ✓ | 24 / 24 |

Every misdirection ticket lands on **exactly the decoy**, with a keyword margin of ≥4 hits for the
decoy against **0** for the true queue. `triagelabgen -verify` (on by default) refuses to write a
ticket that does not carry its kind's property, and `truths.json` records what both routers found on
those exact bytes, so the claim stays checkable after the fact.

Trap properties are proven on the **pinned, documented seeds actually shipped** — never on "any
seed". That rule is L1's scar tissue: a small fixture can fail to carry its own trap.

## The arms

| Arm | Difference from the applied topology | Isolates |
| --- | --- | --- |
| `A-critic-live` | none | the thing under test |
| `B-critic-off` | the critic's subscription is deleted after apply | no-learning baseline (MR-3 at scale) |

One **ordinary operator mutation made after the apply**, so both arms render from the identical
topology and differ in exactly one nameable way. B deletes the *subscription* rather than the
worker, so even the composed prompts stay identical.

There is deliberately no sham arm. Churn-vs-learning (playbook C7) already has an instrument in the
calibration rig; a third arm here would triple the tokens to re-answer a settled question.

## The org: `triage-lab@v1` (catalogue entry 14)

Six workers, mirroring `hypothesis-lab@v1`'s proven shape:

```
                     ticket event
                          │
                   ┌──────▼───────┐
                   │  dispatcher  │
                   └──────┬───────┘
              worker.finished (whole transcript)
          ┌──────────┬────┴─────┬───────────┐
          ▼          ▼          ▼           ▼
       queue 1    queue 2    queue 3    methodology-critic
                                         (worker_prompt_write
                                          on the dispatcher only)

     audit event (stated route + held-out truth) ──► route-auditor  [FROZEN]
```

- **Routing is a `ROUTE-TO: <queue>` line** in the dispatcher's transcript, not a typed event.
  Workers cannot emit typed events (the T4–T7 finding); the only routable output is
  `worker.finished`, whose text is the whole transcript. Every queue receives the same one and acts
  only when the line names it. The prompts say exactly this, because a prompt promising machinery
  that does not exist teaches the model to fail.
- **The critic holds `worker_prompt_write` on the dispatcher only** and never names the auditor or
  the queues — prompts become event text downstream, and a travelling name matches the wrong
  mock-script rule (L2, generalised in R2).
- **The auditor ships frozen** and nothing subscribes to its events. Truth reaches it through one
  channel, once per ticket, and terminates there.

## Ground truth, and where it is allowed to go

`go/triagelab` returns the dataset and the truth as separate values; `go/cmd/triagelabgen` writes the
ticket files and one `truths.json` beside them. `truths.json` is read by the runner and **never
enters a project** — except one field of it, inside one event, addressed to the frozen auditor. The
ticket event carries no truth at all; unit tests assert both directions on the generated text
(`TestDatasetBytesCarryNoTruth`, `TestTicketFilesCarryNoTruth`, and route.test.ts's own check on the
event text).

Unlike hypolab there is **no gap between truth and expected answer**. hypolab records both `verdict`
and `expected_verdict` because an underpowered sample has a real effect and a null is still the
honest report; triage has one truth, because escalating IS the correct answer to an ambiguous
ticket rather than a refusal to answer.

## The output contract

There is no route to score unless the dispatcher emits one, so its charter and every ticket event
carry the same contract: a final line reading exactly `ROUTE-TO: <one of this arm's queue workers>`
or `ROUTE-TO: escalate`.

- It reaches the charter through the topology's `routing-rules` answer, which is the only channel
  `triage-lab@v1` offers into that prompt.
- It is **parsed from the deliverable** — the last assistant message of the dispatcher's job — never
  from `worker.finished` text, which is the whole transcript and contains the charter's own
  `ROUTE-TO: <queue-name>` template. (B1's live foot-gun.)
- The charter's placeholder is refused explicitly: a dispatcher that echoed `<queue-name>` has not
  routed anything.
- A route naming a worker this arm does not hold is `unparseable`, **not** a wrong queue —
  output-contract breakage is a different failure from misrouting, and folding them together would
  inflate the headline with the wrong thing.
- A reply with no contract line is `unparseable`, counted, and left in the accuracy denominator. It
  is never guessed at from prose. If the critic rewrites the contract away, that count is how the
  run says so — a finding, which the rig will not repair.

## Files

| Path | What |
| --- | --- |
| `spec.ts` | The config shape: arms, manifest, window, ceiling, mode. |
| `arms.ts` | The two arms and their worker names. |
| `route.ts` | **Pure.** Route and auditor-call parsing, the worker-name↔queue-id mapping, and the event markers. |
| `text.ts` | **Pure.** The ticket event, the audit event and the charter's routing rules — the only place truth is written into event text. |
| `metrics.ts` | **Pure.** Every doc 19 §3 number, the tables, the artifact. Imports only `../report`'s arithmetic. |
| `truths.ts` | Loads `truths.json` + the tickets, refusing a directory that does not match its own checksums. |
| `runner.ts` | The half that touches the stack: apply → ceiling → wire → per-ticket loop → sweep. |
| `triage.ts` | CLI: load config, run arms sequentially, write artifacts, measure the port pool. |
| `configs/` | `triage-smoke-6` (mock) and `triage-24` (live). `run.sh list` enumerates them. |
| `manifest-24.json` | The SC-1 ticket list with **every seed pinned** — the record. |
| `manifest-smoke-6.json` | The smoke's six. |
| `datasets/` | Generated by `run.sh`, gitignored. Deterministic from the manifests. |
| `reports/` | The committed artifacts. `*.run-metadata.json` and `*.run-log.json` are the volatile halves. |

Compilation is the C1 rig's: `run.sh build` delegates to `../run.sh build`, which owns
`experiments/tsconfig.json` and already sweeps this directory. **One tsconfig, one `dist/`** — the
C1↔B1 collision in the work plan's Discovered Issues Log is what that rule exists for. CommonJS
import style, no `.ts` import extensions.

## Rules the rig obeys

- **Polls, never sleeps.** Every wait is on a delivery row, an event row or a config record.
- **A ticket is not over when the dispatcher stops.** In arm A the critic's rewrite lands in a job
  that starts *after* the dispatcher finishes; the next ticket would race it. (C1's round-boundary
  lesson.)
- **The fan-out is part of the round too.** One dispatcher finish wakes THREE queues as well as the
  critic. Waiting only for the critic would leave three containers being born as the sweep ran —
  exactly the shape of leak that empties a 100-port pool.
- **Sessions are swept per ticket**, not per arm: six sessions × 24 tickets × 2 arms is 288
  containers against a pool of 100.
- **Arms run sequentially.** One agentd, one port pool, one model.
- **The report is deterministic**: no timestamps, project names, session ids or event ids, every
  number rounded to 6 decimals. Two runs of `triage-smoke-6` produce byte-identical `report.json`
  and `report.md`; that diff is how the claim is checked, and a unit test pins the exclusion.
- **The mock script is restored on exit**, including failing runs — it is agentd-wide boot
  configuration.

## Two token ceilings, on purpose

`daily_tokens_hard` is set on every arm project **and** the runner keeps its own running total. The
engine gate **queues** further dispatches inside a project (the right product behaviour, live since
TOK1), while the runner's own count **aborts the experiment**. A ceiling that queues is not a stop
button.

## What the mock smoke proves — and what it does not

`configs/triage-smoke-6.ts` runs six tickets — two per trap kind, a trap first — through arms A and
B against the scripted mock model. It exercises every mechanism the live run depends on: the apply,
the frozen auditor, the whole-object settings PUT, arm B's deleted subscription, a generated ticket
reaching the dispatcher, a route parsed from a **deliverable** (not a transcript), the fan-out to
three queues settled and swept, the critic's refused freeze attempt and its rewrite, the rewrite
changing the next ticket's route, `escalate` being reachable and scored, decision+truth reaching the
auditor, per-ticket session sweeping, and every metric registering.

**Its numbers are meaningless as a result.** Arm A improves and arm B does not because the mock
script says so. `docs/AGENTS_RESEARCH.md` §7: Tier A proves transmission, never discovery. The
report markdown says the same thing on its own face, and the committed artifact in `reports/` is
there as a machinery fixture, not as evidence about org charts.

What it buys is worth the run: **a failure in the live run is then a model failure rather than a
harness failure.**

## Mock-script discipline (`e2e/mock-scripts/triage-smoke.json`)

Rule order is **auditor → critic → queues → dispatcher**, and it is load-bearing. `script.test.ts`
asserts it offline.

- **Auditor** rules key on `[TRI-AUDIT-<ticket>-<ROUTE>]`, a marker the harness stamps on the audit
  event. It encodes the ticket and the *stated* route — both facts the auditor legitimately receives
  — and deliberately **not** whether the harness thinks that was right, which would make
  `auditor_agreement` a tautology. It carries no arm tag, so one set of nine rules serves both arms.
- **The critic's** rule keys on `You review tra-dispatch`, a phrase only its own composed prompt
  holds. Its body carries the dispatcher's entire transcript, so it must sit above every dispatcher
  rule (the body-match trap).
- **The queues** key on their identity phrase `You are <name>,`. They sit **above** the dispatcher
  too, which is the one thing here that will surprise a reader of `supervisor@v1`'s script: a
  queue's body is the dispatcher's transcript, so it also carries the dispatcher's ticket marker,
  and a marker-keyed dispatcher rule placed above would answer the queues' requests.
- **The dispatcher's** rules key on `[TRI-<ARM>-<ticket>]` and come last. Unlike calibration's arms,
  which shared a `VERDICT` token, two triage arms cannot share a reply — a `ROUTE-TO` line names a
  *worker*, and the two arms have different ones. So the ticket marker carries the arm.
- Arm A's rules come in pairs split with `absent: "[TRI-RULE-APPLIED]"`: the vocabulary answer
  before the critic's rewrite, the rule-following one after. The rewrite arriving in the composed
  prompt is what flips them, which makes arm A's curve a **delivery** assertion.
- The critic's freeze attempt names the auditor but never its identity phrase, so it cannot
  contaminate the auditor's rules (L2's finding, generalised).
- The critic is **not** narrowed by an `absent`, so it re-fires on every ticket of arm A and writes
  the same amendment each time. That is deliberate and it is the calibration smoke's shape: it keeps
  `freeze_refused` and `prompt_writes` non-zero on every ticket. Remember C1's finding —
  `prompt_writes` is a **churn** metric, not a quality one.
