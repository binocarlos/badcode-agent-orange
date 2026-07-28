// actor-critic-vs-sham-vs-solo.ts — the demo comparison, and the reason the rig
// exists.
//
// Three arms on one two-round task:
//
//   actor-critic@v1  a critic that reads the work and rewrites the actor's
//                    prompt with a diagnosis
//   sham-critic@v1   the placebo: wiring pinned byte-for-byte to actor-critic's
//                    (reflect.DeepEqual, go/topology/shamcritic_test.go), a
//                    critic that only REORDERS the actor's instructions and
//                    says honestly in its rationale that the shuffle is
//                    arbitrary
//   solo@v1          one worker, nobody rewriting anything
//
// The arms differ in the critic's WORDS and nothing else, which is what makes
// actor-critic minus sham isolate diagnosis from churn (playbook C7: an
// improvement claim that has not beaten the sham critic is motion, not
// learning). Watch `prompt_writes` in the report: the sham matches actor-critic
// exactly — same number of rewrites, same rationale discipline — and scores
// zero on the property that matters. That is the control working.
//
// Everything the arms do is scripted in e2e/mock-scripts/experiments-compare.json.
// A ranking from this config is a statement about the RIG, not about org charts
// (AGENTS_RESEARCH §7, Tier A).
//
// Mock-script naming discipline (standing traps): every worker name carries a
// per-arm prefix (xpa-/xps-/xpz-) and no name is a substring of another, the
// critics' rules sit ABOVE their actors' (a critic's request body contains the
// actor's whole transcript), and the two prompt-state rules sit between them —
// below the critic that writes the marker into its own tool call, above the
// actor whose composed prompt then carries it.

import type { ProjectClient } from '../../helpers/api'
import type { TaskSpec } from '../spec'

/** The starting prompt every arm's worker gets. Two reorderable sentences. */
const SEED = 'You file catalogue notes. Close with a totals line. Open with the date line.'

/** The marker the genuine critic's rewrite adds — byte-for-byte with the mock script. */
const HEADLINE_MARKER = 'Headline:'

/** The marker the sham's reordering makes the actor emit — byte-for-byte with the script. */
const REORDER_MARKER = 'XPS-REORDERED'

/** The task, identical for both rounds: same request, so any change is the org's. */
const TASK_TEXT = 'File the catalogue note for the new apples.'

export const spec: TaskSpec = {
  id: 'actor-critic-vs-sham-vs-solo',
  description:
    'One two-round filing task through three org charts: a diagnosing critic, a placebo critic ' +
    'with identical wiring, and a lone worker. Ranked by whether round 2 gained the headline rule.',
  mockScript: 'e2e/mock-scripts/experiments-compare.json',
  rounds: [TASK_TEXT, TASK_TEXT],
  repetitions: 2,

  arms: [
    {
      id: 'actor-critic',
      topology: 'actor-critic',
      version: 'v1',
      answers: {
        'actor-name': 'xpa-scribe',
        'critic-name': 'xpa-editor',
        'actor-prompt-seed': SEED,
        criterion: 'every note opens with a headline line',
      },
      eventType: 'xpa-scribe.task',
      primaryWorker: 'xpa-scribe',
      // The critic's job is part of the round: the rewrite lands there.
      followOnDeliveries: 1,
    },
    {
      id: 'sham-critic',
      topology: 'sham-critic',
      version: 'v1',
      answers: {
        'actor-name': 'xps-clerk',
        'critic-name': 'xps-shuffler',
        'actor-prompt-seed': SEED,
      },
      eventType: 'xps-clerk.task',
      primaryWorker: 'xps-clerk',
      followOnDeliveries: 1,
    },
    {
      id: 'solo',
      topology: 'solo',
      version: 'v1',
      answers: { 'worker-name': 'xpz-hermit', 'prompt-seed': SEED },
      // solo@v1's only clock is cron, which no test can wait for, so the arm
      // adds an ordinary poke subscription after the apply — the same move
      // features/topologies.stack.spec.ts makes, and exactly what an operator
      // does to run a scheduled worker on demand.
      eventType: 'xpz.poke',
      primaryWorker: 'xpz-hermit',
      afterApply: async (client: ProjectClient) => {
        await client.createSubscription({ event_type: 'xpz.poke', worker: 'xpz-hermit' })
      },
    },
  ],

  properties: [
    {
      id: 'headline-rule',
      describe: "the round's output opens with the headline line the critic's rewrite asked for",
      holds: (obs) => obs.output.includes(HEADLINE_MARKER),
    },
    {
      id: 'reordered-only',
      describe: "the round's output shows the sham's reshuffled instruction order and no new content",
      holds: (obs) => obs.output.includes(REORDER_MARKER),
    },
    {
      id: 'output-changed',
      describe: "the round's output differs from the previous round's (churn, of any kind)",
      holds: (obs, previous) => previous !== undefined && obs.output !== previous.output,
    },
    {
      id: 'prompt-intact',
      describe: "the primary worker's prompt still contains every instruction it started with",
      holds: (obs) =>
        SEED.split('. ')
          .map((s) => s.replace(/\.$/, '').trim())
          .every((sentence) => obs.workerPromptAfter.includes(sentence)),
    },
  ],

  // The headline rule is the genuine improvement; ranking by it is the query
  // "which of these org charts actually landed the fix?".
  rankBy: 'prop:headline-rule',
  rankDirection: 'desc',
}

export default spec
