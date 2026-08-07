# Calibration — smoke-4

Mock smoke for the calibration runner: four hypotheses (one per trap kind) through arms A and B, scripted end to end. Proves the machinery, measures nothing.

> **These numbers are meaningless as a result.** In mock mode every
> investigator answer is authored into the mock script, so the accuracy
> columns measure the SCRIPT, not the org. What the run proves is that the
> machinery works: datasets reach the investigator, verdicts are parsed from
> deliverables, truth reaches only the frozen checker, and every metric
> registers. `docs/AGENTS_RESEARCH.md` §7, Tier A.

- hypotheses: 4 (manifest `e2e/experiments/calibration/manifest-smoke-4.json`, dataset seed 20260728)
- early/late window: 2 (asked for 10)
- daily_tokens_hard per arm: 5000000
- mock script: `e2e/mock-scripts/calibration-smoke.json`

## Arms

- **A-critic-live** — investigator `cala-invest`, critic `cala-critic`, frozen checker `cala-judge`. hypothesis-lab@v1 as applied: the methodology critic reads every finish and may rewrite the investigator between hypotheses.
- **B-critic-off** — investigator `calb-invest`, critic `calb-critic` (subscription deleted after apply), frozen checker `calb-judge`. the same org with the critic's subscription deleted after apply: nothing wakes it, so nothing rewrites. Scorer variance and the no-learning baseline.

## Metrics

```
arm            hypotheses  accuracy  accuracy_first  accuracy_last  accuracy_delta  planted_null_false_confirm_rate  confound_escaped_rate  underpowered_overclaim_rate  real_effect_detection_rate  unparseable  prompt_writes  freeze_refused  checker_agreement  delivery_ok_rate  tokens_total
-------------  ----------  --------  --------------  -------------  --------------  -------------------------------  ---------------------  ---------------------------  --------------------------  -----------  -------------  --------------  -----------------  ----------------  ------------
A-critic-live  4           1         1               1              0               0                                1                      0                            1                           0            4              4               1                  1                 400
B-critic-off   4           0.25      0.5             0              -0.5            1                                0                      1                            1                           0            0              0               1                  1                 160
```

`accuracy_*` score the reported verdict against the honest answer, which on an underpowered
sample is `no-effect` even though the effect is real. `unparseable` verdicts stay in the
denominator. `checker_agreement` is how often the in-project frozen fact-checker agreed with
this harness's own arithmetic — a scoreboard check, not the score.

## Per hypothesis

```
id   kind           expect     A-critic-live  B-critic-off
---  -------------  ---------  -------------  ------------
h01  real-effect    effect     effect ✓       effect ✓
h02  planted-null   no-effect  no-effect ✓    effect ✗
h03  confound-trap  no-effect  no-effect ✓    effect ✗
h04  underpowered   no-effect  no-effect ✓    effect ✗
```

## Prompt lineage (config log)

### A-critic-live

- `h01` — `cala-critic` → `cala-invest`: the conclusion rested on a naive comparison and never controlled for age_group
- `h02` — `cala-critic` → `cala-invest`: the conclusion rested on a naive comparison and never controlled for age_group
- `h03` — `cala-critic` → `cala-invest`: the conclusion rested on a naive comparison and never controlled for age_group
- `h04` — `cala-critic` → `cala-invest`: the conclusion rested on a naive comparison and never controlled for age_group

### B-critic-off

_no prompt rewrites_

