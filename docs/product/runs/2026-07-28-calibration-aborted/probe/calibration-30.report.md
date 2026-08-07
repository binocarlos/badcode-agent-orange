# Calibration — calibration-30

Runbook §2: 30 hypolab hypotheses (10 real-effect, 8 planted-null, 8 confound-trap, 4 underpowered, shuffled) through hypothesis-lab@v1 with the critic live (A), with the critic unsubscribed (B), and optionally with a sham critic (C). Ground truth lives in the harness and reaches the project only inside the frozen fact-checker's own check events.

> Live run against a real model. One seed unless the record says otherwise;
> read `docs/product/14-calibration-runbook.md` §5 before drawing a conclusion.

- hypotheses: 3 (manifest `e2e/experiments/calibration/manifest-30.json`, dataset seed 20260728)
- early/late window: 1 (asked for 10)
- daily_tokens_hard per arm: 3000000

## Arms

- **A-critic-live** — investigator `cala-invest`, critic `cala-critic`, frozen checker `cala-judge`. hypothesis-lab@v1 as applied: the methodology critic reads every finish and may rewrite the investigator between hypotheses.

## Metrics

```
arm            hypotheses  accuracy  accuracy_first  accuracy_last  accuracy_delta  planted_null_false_confirm_rate  confound_escaped_rate  underpowered_overclaim_rate  real_effect_detection_rate  unparseable  prompt_writes  freeze_refused  checker_agreement  delivery_ok_rate  tokens_total
-------------  ----------  --------  --------------  -------------  --------------  -------------------------------  ---------------------  ---------------------------  --------------------------  -----------  -------------  --------------  -----------------  ----------------  ------------
A-critic-live  3           1         1               1              0               0                                1                      0                            1                           0            0              0               1                  1                 35132
```

`accuracy_*` score the reported verdict against the honest answer, which on an underpowered
sample is `no-effect` even though the effect is real. `unparseable` verdicts stay in the
denominator. `checker_agreement` is how often the in-project frozen fact-checker agreed with
this harness's own arithmetic — a scoreboard check, not the score.

## Per hypothesis

```
id   kind           expect     A-critic-live
---  -------------  ---------  -------------
h01  real-effect    effect     effect ✓
h02  confound-trap  no-effect  no-effect ✓
h03  planted-null   no-effect  no-effect ✓
```

## Prompt lineage (config log)

### A-critic-live

_no prompt rewrites_

