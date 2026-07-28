# Triage (SC-1) — triage-smoke-6

Mock smoke for the triage runner: six tickets (two per trap kind) through arms A and B, scripted end to end. Proves the machinery, measures nothing.

> **These numbers are meaningless as a result.** In mock mode every
> dispatcher decision is authored into the mock script, so the accuracy
> columns measure the SCRIPT, not the org. What the run proves is that the
> machinery works: tickets reach the dispatcher, routes are parsed from
> deliverables, truth reaches only the frozen auditor, the critic’s rewrite
> changes the next decision, and every metric registers.
> `docs/AGENTS_RESEARCH.md` §7, Tier A.

- tickets: 6 (manifest `e2e/experiments/triage/manifest-smoke-6.json`, dataset seed 20260728)
- early/late window: 3 (asked for 10)
- daily_tokens_hard per arm: 5000000
- mock script: `e2e/mock-scripts/triage-smoke.json`

## Arms

- **A-critic-live** — dispatcher `tra-dispatch`, queues `tra-money` / `tra-uptime` / `tra-signin`, critic `tra-critic`, frozen auditor `tra-audit`. triage-lab@v1 as applied: the methodology critic reads every finish and may rewrite the dispatcher between tickets.
- **B-critic-off** — dispatcher `trb-dispatch`, queues `trb-money` / `trb-uptime` / `trb-signin`, critic `trb-critic` (subscription deleted after apply), frozen auditor `trb-audit`. the same org with the critic's subscription deleted after apply: nothing wakes it, so nothing rewrites. Scorer variance and the no-learning baseline.

## Metrics

```
arm            tickets  accuracy  accuracy_first  accuracy_last  accuracy_delta  trap_misroute_rate  trap_decoy_rate  ambiguity_confidence_rate  plain_accuracy  over_escalation_rate  unparseable  prompt_writes  freeze_refused  auditor_agreement  delivery_ok_rate  tokens_total
-------------  -------  --------  --------------  -------------  --------------  ------------------  ---------------  -------------------------  --------------  --------------------  -----------  -------------  --------------  -----------------  ----------------  ------------
A-critic-live  6        0.833     0.667           1              0.333           0.5                 0.5              0                          1               0                     0            6              6               1                  1                 960
B-critic-off   6        0.333     0.333           0.333          0               1                   1                1                          1               0                     0            0              0               1                  1                 600
```

`trap_misroute_rate` is the headline: of the misdirection tickets — whose vocabulary points at one
queue while the stated facts belong to another — the share the dispatcher did NOT route correctly.
`ambiguity_confidence_rate` is its restraint counterpart: tickets that state no routable fact, where
`escalate` is the correct answer and any confident queue is a guess. `over_escalation_rate` is the
price of that restraint, so an arm cannot win by escalating everything. `unparseable` routes stay in
every denominator. `auditor_agreement` is how often the in-project FROZEN route-auditor agreed with
this harness's own arithmetic — a scoreboard check, not the score.

## Per ticket

```
id   kind       expect    A-critic-live  B-critic-off
---  ---------  --------  -------------  ------------
t01  misdirect  outage    billing ✗      billing ✗
t02  plain      billing   billing ✓      billing ✓
t03  ambiguous  escalate  escalate ✓     access ✗
t04  misdirect  access    access ✓       outage ✗
t05  plain      outage    outage ✓       outage ✓
t06  ambiguous  escalate  escalate ✓     billing ✗
```

## Prompt lineage (config log)

### A-critic-live

- `t01` — `tra-critic` → `tra-dispatch`: the route was chosen from the wording rather than from a sentence stating a rule fact, and nothing was escalated
- `t02` — `tra-critic` → `tra-dispatch`: the route was chosen from the wording rather than from a sentence stating a rule fact, and nothing was escalated
- `t03` — `tra-critic` → `tra-dispatch`: the route was chosen from the wording rather than from a sentence stating a rule fact, and nothing was escalated
- `t04` — `tra-critic` → `tra-dispatch`: the route was chosen from the wording rather than from a sentence stating a rule fact, and nothing was escalated
- `t05` — `tra-critic` → `tra-dispatch`: the route was chosen from the wording rather than from a sentence stating a rule fact, and nothing was escalated
- `t06` — `tra-critic` → `tra-dispatch`: the route was chosen from the wording rather than from a sentence stating a rule fact, and nothing was escalated

### B-critic-off

_no prompt rewrites_

