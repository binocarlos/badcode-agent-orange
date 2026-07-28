// triage-24.ts — the live SC-1 run (doc 19 §3 SC-1). Twenty-four tickets, arms
// A and B, against a real model. Costs tokens; attended.
//
//   ./e2e/run-stack-e2e.sh up subscription
//   TRIAGE_LIVE_RUN=1 ./e2e/experiments/triage/run.sh run triage-24
//
// The question it asks (doc 19 §3): does the critic teach the dispatcher
// routing RULES — a better `trap_misroute_rate` on the misdirection tickets —
// or merely routing CONFIDENCE, which shows up as a worse
// `ambiguity_confidence_rate` while accuracy barely moves? Those two outcomes
// look identical in an accuracy column and opposite in this rig's headline
// pair, which is the whole reason the pair exists.
//
// The run itself is not scheduled here. Work plan 13's L3 posture applies
// verbatim: attended, Kai's go, ceilings set, dated record under
// docs/product/runs/. This config is the harness that run would use.

import { ARM_A, ARM_B } from '../arms'
import type { TriageSpec } from '../spec'

export const spec: TriageSpec = {
  id: 'triage-24',
  description:
    'SC-1: 24 generated tickets (8 plain, 10 misdirection traps covering all six queue/decoy pairs, ' +
    '6 ambiguity traps) through triage-lab@v1 with the critic live (A) and with the critic ' +
    'unsubscribed (B). Routes are scored against held-out truth the harness never puts in the project.',
  mode: 'live',
  manifest: 'e2e/experiments/triage/manifest-24.json',
  datasetDir: 'e2e/experiments/triage/datasets/triage-24',
  // Eight-ticket windows: the manifest spreads the trap kinds evenly, so the
  // first eight and last eight hold comparable mixes and "late minus early"
  // compares like with like.
  window: 8,
  // A rate-limit guard, not a budget. Six sessions a ticket × 24 tickets × 2
  // arms is the shape to size against; the runner also keeps its own running
  // total and ABORTS on it, because the engine gate queues rather than stops.
  dailyTokensHard: 4_000_000,
  arms: [ARM_A, ARM_B],
}

export default spec
