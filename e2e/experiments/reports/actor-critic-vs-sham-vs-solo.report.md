# Comparison — actor-critic-vs-sham-vs-solo

One two-round filing task through three org charts: a diagnosing critic, a placebo critic with identical wiring, and a lone worker. Ranked by whether round 2 gained the headline rule.

**Tier A (mock) result: this proves the machinery transmits a difference, never that the
system discovered one** — the improvement is authored into the mock script
(`docs/AGENTS_RESEARCH.md` §7).

- rounds per run: 2
- repetitions per arm: 2
- ranked by: `prop:headline-rule` (desc)
- mock script: `e2e/mock-scripts/experiments-compare.json`

## Ranking

```
#  arm           reps  rounds_completed  delivery_ok_rate  prompt_writes  prop:headline-rule  prop:reordered-only  prop:output-changed  prop:prompt-intact
-  ------------  ----  ----------------  ----------------  -------------  ------------------  -------------------  -------------------  ------------------
1  actor-critic  2     2 ±0              1 ±0              2 ±0           0.5 ±0              0 ±0                 0.5 ±0               1 ±0
2  sham-critic   2     2 ±0              1 ±0              2 ±0           0 ±0                0.5 ±0               0.5 ±0               1 ±0
2  solo          2     2 ±0              1 ±0              0 ±0           0 ±0                0 ±0                 0 ±0                 1 ±0
```

Cells are `mean ±spread` across repetitions. In mock mode every spread must be 0.

## Arms

- **actor-critic** — `actor-critic@v1`, woken by `xpa-scribe.task`, primary worker `xpa-scribe`
- **sham-critic** — `sham-critic@v1`, woken by `xps-clerk.task`, primary worker `xps-clerk`
- **solo** — `solo@v1`, woken by `xpz.poke`, primary worker `xpz-hermit`

## Properties

- **headline-rule** — the round's output opens with the headline line the critic's rewrite asked for
- **reordered-only** — the round's output shows the sham's reshuffled instruction order and no new content
- **output-changed** — the round's output differs from the previous round's (churn, of any kind)
- **prompt-intact** — the primary worker's prompt still contains every instruction it started with

## Prompt rewrites recorded (config log)

### actor-critic (repetition 1)

- `xpa-editor` → `xpa-scribe`: the note shipped without a headline line
- `xpa-editor` → `xpa-scribe`: the note shipped without a headline line

### sham-critic (repetition 1)

- `xps-shuffler` → `xps-clerk`: an arbitrary reshuffle of the same instructions: order changed, meaning untouched, nothing was found wrong
- `xps-shuffler` → `xps-clerk`: an arbitrary reshuffle of the same instructions: order changed, meaning untouched, nothing was found wrong

### solo (repetition 1)

_no prompt rewrites_

