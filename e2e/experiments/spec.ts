// spec.ts — the shape of a comparison: what task, through which org charts,
// how many times, and what counts as a good outcome.
//
// A comparison config is a TypeScript module exporting a `TaskSpec`, not JSON,
// for one reason: the output-property checks are PREDICATES. "Did round 2's
// output gain the headline rule?" is a function of the round, and pushing it
// into a config DSL would buy nothing but a worse language. Everything else in
// the spec is data.
//
// The spec deliberately says nothing about how a metric is scored or ranked —
// that lives in report.ts, which never sees the stack. This file is the seam
// between the two halves.

import type { ProjectClient } from '../helpers/api'
import type { PromptWriteRecord } from './report'

/** What a property predicate is allowed to look at. */
export interface RoundObservation {
  /** 1-based. */
  round: number
  /** The inbound event text this round was driven with. */
  taskText: string
  /** Delivery statuses for the round's inbound event. */
  deliveryStatuses: string[]
  /** The primary worker's reply for this round. */
  output: string
  /** Rewrites the config log gained while this round ran. */
  promptWrites: PromptWriteRecord[]
  /** The primary worker's stored system prompt once the round had settled. */
  workerPromptAfter: string
}

/**
 * One output-property check, evaluated per round for every arm.
 *
 * `previous` is the round before this one in the same run (undefined on round
 * 1), which is what makes "unchanged since last round" expressible — the
 * control arms' defining property, and one no single-round view can state.
 */
export interface PropertySpec {
  id: string
  describe: string
  holds(observation: RoundObservation, previous?: RoundObservation): boolean
}

/**
 * One arm: a topology, its answers, and how to wake it.
 *
 * `afterApply` exists because not every seed can be driven on demand as
 * applied — solo@v1's only clock is cron, so its arm wires an ordinary
 * subscription after the apply, exactly as `features/topologies.stack.spec.ts`
 * does. That is an operator mutation, not a change to the topology.
 */
export interface ArmSpec {
  /** Short label; it is the row key in the report, so keep it stable. */
  id: string
  topology: string
  version: string
  answers: Record<string, unknown>
  /** The event type the rig emits to start a round. */
  eventType: string
  /** Whose reply is "the output" and whose prompt is tracked. */
  primaryWorker: string
  /** Deliveries the inbound event must settle before the round is over (default 1). */
  deliveriesPerRound?: number
  /**
   * Deliveries the primary worker's `worker.finished` must settle before the
   * round is over (default 0). This is the critic's job: a round is not over
   * until the critic that reacts to it has finished, or the next round would
   * race the rewrite.
   */
  followOnDeliveries?: number
  /** Ordinary config mutations to make after the apply, before any round. */
  afterApply?(client: ProjectClient): Promise<void>
}

export interface TaskSpec {
  /** Filename-safe id; the report artifacts are named after it. */
  id: string
  description: string
  /**
   * The mock script this task's arms are scripted by, as a repo-relative path.
   * Recorded rather than loaded: the runner cannot install it (it is agentd
   * boot configuration), so it prints the command and refuses to guess.
   */
  mockScript: string
  /** The inbound event text for each round, in order. Length = round count. */
  rounds: string[]
  /** How many independent runs of each arm. Below 2 there is no spread to report. */
  repetitions: number
  arms: ArmSpec[]
  properties: PropertySpec[]
  /** Metric key to rank by, e.g. `prop:headline-rule` (see report.metricKeys). */
  rankBy: string
  rankDirection?: 'desc' | 'asc'
}
