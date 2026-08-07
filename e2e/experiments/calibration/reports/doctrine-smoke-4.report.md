# Calibration — doctrine-smoke-4

Mock smoke for the doctrine axis: the four smoke hypotheses through arm A and through arm A with doctrine-v1 written into the project prompt. Proves the block reaches composed prompts; measures nothing.

> **These numbers are meaningless as a result.** In mock mode every
> investigator answer is authored into the mock script, so the accuracy
> columns measure the SCRIPT, not the org. What the run proves is that the
> machinery works: datasets reach the investigator, verdicts are parsed from
> deliverables, truth reaches only the frozen checker, and every metric
> registers. `docs/AGENTS_RESEARCH.md` §7, Tier A.

- hypotheses: 4 (manifest `e2e/experiments/calibration/manifest-smoke-4.json`, dataset seed 20260728)
- early/late window: 2 (asked for 10)
- daily_tokens_hard per arm: 5000000
- mock script: `e2e/mock-scripts/calibration-doctrine-smoke.json`

## Arms

- **A-critic-live** — investigator `cala-invest`, critic `cala-critic`, frozen checker `cala-judge`, doctrine none. hypothesis-lab@v1 as applied: the methodology critic reads every finish and may rewrite the investigator between hypotheses.
- **A-critic-live-doctrine-v1** — investigator `cald-invest`, critic `cald-critic`, frozen checker `cald-judge`, doctrine v1. the same org as A with docs/product/doctrine/doctrine-v1.md written into the project prompt after apply, so the block rides every worker's composed prompt. One operator mutation from A.

## Metrics

```
arm                        hypotheses  accuracy  accuracy_first  accuracy_last  accuracy_delta  planted_null_false_confirm_rate  confound_escaped_rate  underpowered_overclaim_rate  real_effect_detection_rate  unparseable  prompt_writes  freeze_refused  checker_agreement  delivery_ok_rate  tokens_total
-------------------------  ----------  --------  --------------  -------------  --------------  -------------------------------  ---------------------  ---------------------------  --------------------------  -----------  -------------  --------------  -----------------  ----------------  ------------
A-critic-live              4           1         1               1              0               0                                1                      0                            1                           0            4              4               1                  1                 400
A-critic-live-doctrine-v1  4           1         1               1              0               0                                1                      0                            1                           0            4              4               1                  1                 400
```

`accuracy_*` score the reported verdict against the honest answer, which on an underpowered
sample is `no-effect` even though the effect is real. `unparseable` verdicts stay in the
denominator. `checker_agreement` is how often the in-project frozen fact-checker agreed with
this harness's own arithmetic — a scoreboard check, not the score.

## Per hypothesis

```
id   kind           expect     A-critic-live  A-critic-live-doctrine-v1
---  -------------  ---------  -------------  -------------------------
h01  real-effect    effect     effect ✓       effect ✓
h02  planted-null   no-effect  no-effect ✓    no-effect ✓
h03  confound-trap  no-effect  no-effect ✓    no-effect ✓
h04  underpowered   no-effect  no-effect ✓    no-effect ✓
```

## Prompt lineage (config log)

### A-critic-live

- `h01` — `cala-critic` → `cala-invest`: the conclusion rested on a naive comparison and never controlled for age_group
- `h02` — `cala-critic` → `cala-invest`: the conclusion rested on a naive comparison and never controlled for age_group
- `h03` — `cala-critic` → `cala-invest`: the conclusion rested on a naive comparison and never controlled for age_group
- `h04` — `cala-critic` → `cala-invest`: the conclusion rested on a naive comparison and never controlled for age_group

### A-critic-live-doctrine-v1

- `h01` — `cald-critic` → `cald-invest`: the conclusion rested on a naive comparison and never controlled for age_group
- `h02` — `cald-critic` → `cald-invest`: the conclusion rested on a naive comparison and never controlled for age_group
- `h03` — `cald-critic` → `cald-invest`: the conclusion rested on a naive comparison and never controlled for age_group
- `h04` — `cald-critic` → `cald-invest`: the conclusion rested on a naive comparison and never controlled for age_group

