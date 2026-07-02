# Slice B — reconciliation brief (plan ⇄ Foundation)

**Date:** 2026-07-01
**Read with:** `docs/superpowers/plans/2026-06-30-slice-B-real-model.md` (the plan) and
`go/orchestrator/contracts.go` (the frozen truth). This brief OVERRIDES the plan where they conflict.

Slice B puts a real model behind the `Model` seam: a `ModelRouter` (tier→Model), an Anthropic
`Model` over the Messages API (API-key auth), and a `SpendMeter` monthly ceiling. All code lands in
package `go/orchestrator` alongside the Foundation. `ScriptedModel` stays as the offline double.

## What the Foundation already provides (DO NOT re-declare — it will duplicate-declare and fail)

`contracts.go` already declares, in package `orchestrator`:
- **`ModelTier`** + `TierFull`/`TierMid`/`TierCheap` (§3). → **SKIP plan Task 2 entirely. Do NOT create
  `tier.go`.** (You MAY keep the plan's `tier_test.go` `TestModelTierFrozenStrings` as coverage — it
  just asserts the existing constants — but the type/constants themselves are already defined.)
- **`SpendMeter` interface** (§5): `Charge(ctx, tokens int64, usd float64) error` /
  `Spent(ctx) (usd float64, err error)`. → In plan Task 3, **create `spendmeter.go` but OMIT the
  `type SpendMeter interface { ... }` block.** Keep `MemSpendMeter`, `NewMemSpendMeter`,
  `ErrSpendCeiling`, and `var _ SpendMeter = (*MemSpendMeter)(nil)`.
- **`ModelRouter` interface** (§5): `For(tier ModelTier) Model`. → In plan Task 6, **create
  `router.go` but OMIT the `type ModelRouter interface { ... }` block.** Keep `TierRouter`,
  `NewTierRouter`, `errModel`, `For`, and `var _ ModelRouter = (*TierRouter)(nil)`.
- **`Model` interface** (§5) — consumed as-is by `AnthropicModel`/`errModel`; the plan does not
  re-declare it. Fine.

The plan's own "Contract gaps found" #1 and #3 predicted exactly this — the Foundation is the "single
home" they asked for, so consume from it.

## Confirmed model IDs (use these; do not rely on training memory)

Current model IDs for the `DefaultModelIDs()` map (plan Task 1 / Task 7):
`full = claude-opus-4-8`, `mid = claude-sonnet-5`, `cheap = claude-haiku-4-5`. These match the plan's
defaults — keep them. Keep the plan's `DefaultPricing()` values as documented, env-overridable
estimates (they drive the ceiling estimate, not a safety gate). Model IDs are env-overridable via
`AGENTKIT_MODEL_FULL`/`_MID`/`_CHEAP` (plan Task 7) — keep that. Do not invoke the claude-api skill;
these IDs are confirmed from the current environment.

## Build these (plan Tasks 3–8, with the two "omit the interface" edits above)

- **Task 3** — `spendmeter.go` (MemSpendMeter + ErrSpendCeiling, NO interface decl) + test.
- **Task 4** — `anthropic.go` (`AnthropicModel`, `Pricing`) over `net/http`, API-key `x-api-key` auth,
  **stdlib only — no Anthropic SDK**. Offline test via `httptest.Server`. As written.
- **Task 5** — extend `anthropic_test.go`: metered dispatch (probe halts before HTTP; charge records
  actual usage×pricing). As written.
- **Task 6** — `router.go` (TierRouter + errModel, NO interface decl) + test.
- **Task 7** — config-driven router: `RouterConfig`, `DefaultModelIDs`, `ModelIDsFromEnv`,
  `DefaultPricing`, `NewAnthropicRouter`. As written (with the confirmed IDs above).
- **Task 8** — `anthropic_smoke_test.go` behind `//go:build anthropic_smoke` (skips without
  `ANTHROPIC_API_KEY`; excluded from the default build). As written.

## Guardrails / definition of done

- Consume `Model`/`ModelTier`/`SpendMeter`/`ModelRouter` VERBATIM from `contracts.go` — if you think
  one needs changing, STOP and report (contract change = escalate, not a local edit).
- **Offline only:** no test hits the real API; the only live-endpoint code is the build-tagged smoke
  test. No real API key used or committed.
- stdlib only — no new deps (verify `go.mod` unchanged; `go build` proves it).
- `go build ./...`, `go vet ./...`, `go test ./orchestrator/...` all green at the end.
- Liftability: no host-app imports. Do NOT commit, push, or touch `main` — leave changes in the
  working tree for the parent agent to review and commit. Do not modify `migration-reference/`.
