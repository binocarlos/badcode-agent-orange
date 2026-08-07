// compare.ts — the comparison rig's entry point (C1).
//
//   ./e2e/experiments/run.sh compare actor-critic-vs-sham-vs-solo
//
// Runs ONE task through N topologies × M repetitions against the running mock
// stack, and emits a ranked report with variance. This is the harness that
// turns "which org chart is best for this task?" into a query
// (docs/product/12-composition-playbook.md §4).
//
// **Tier A caveat, stated here because this is the file people run.** In mock
// mode every arm's behaviour is authored into the mock script. A ranking
// produced here proves the *machinery* — that applying a topology, driving it
// with events, and reading the outcome out of the logs distinguishes the arms
// at all. It proves nothing about which org chart is better in the world. The
// same rig, pointed at a real model with a frozen scorer, is what would
// (docs/product/14-calibration-runbook.md); that run is gated on Kai.
//
// Structure: this file is I/O and orchestration only. runner.ts talks to the
// stack, report.ts does the arithmetic, and neither imports the other.

import { request as pwRequest } from '@playwright/test'
import * as fs from 'node:fs'
import * as path from 'node:path'
import { describe as describeOccupancy, measureSettled } from '../helpers/occupancy'
import { buildReport, renderMarkdown, type RunOutcome } from './report'
import { runArm } from './runner'
import type { TaskSpec } from './spec'

interface Args {
  config: string
  reps?: number
  outDir: string
  baseURL: string
  printMockScript: boolean
}

function parseArgs(argv: string[]): Args {
  const args: Args = {
    config: '',
    // Beside the SOURCE, not beside the compiled JS: __dirname is
    // experiments/dist/experiments, so two levels up is experiments/.
    outDir: path.resolve(__dirname, '../../reports'),
    baseURL: process.env.STACK_BASE_URL || 'http://localhost:8080',
    printMockScript: false,
  }
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i]
    if (a === '--reps') args.reps = Number(argv[++i])
    else if (a === '--out') args.outDir = path.resolve(argv[++i])
    else if (a === '--base-url') args.baseURL = argv[++i]
    else if (a === '--print-mock-script') args.printMockScript = true
    else if (a.startsWith('-')) throw new Error(`unknown flag: ${a}`)
    else args.config = a
  }
  if (!args.config) throw new Error('usage: compare <config-name> [--reps N] [--out DIR]')
  return args
}

/** Loads a comparison config module by name from ./configs. */
function loadSpec(name: string): TaskSpec {
  const file = path.join(__dirname, 'configs', `${name}.js`)
  if (!fs.existsSync(file)) {
    const available = fs
      .readdirSync(path.join(__dirname, 'configs'))
      .filter((f) => f.endsWith('.js'))
      .map((f) => f.replace(/\.js$/, ''))
    throw new Error(`no comparison config named ${name}; available: ${available.join(', ') || '(none)'}`)
  }
  // eslint-disable-next-line @typescript-eslint/no-var-requires
  const mod = require(file) as { spec?: TaskSpec; default?: TaskSpec }
  const spec = mod.spec ?? mod.default
  if (!spec) throw new Error(`${name} exports neither \`spec\` nor a default export`)
  return spec
}

async function main(): Promise<void> {
  const args = parseArgs(process.argv.slice(2))
  const spec = loadSpec(args.config)

  // run.sh asks for this before it loads the script into agentd, so the path
  // has exactly one home: the config module.
  if (args.printMockScript) {
    process.stdout.write(`${spec.mockScript}\n`)
    return
  }

  const repetitions = args.reps ?? spec.repetitions
  if (!Number.isInteger(repetitions) || repetitions < 1) {
    throw new Error(`repetitions must be a positive integer, got ${repetitions}`)
  }
  if (repetitions < 2) {
    console.warn('! repetitions < 2: the report will show no spread, because there is none to show')
  }

  const ctx = await pwRequest.newContext({ baseURL: args.baseURL })
  const startedAt = new Date().toISOString()
  const runs: RunOutcome[] = []
  const projects: Array<{ arm: string; repetition: number; project: string }> = []

  try {
    // Sequential on purpose. The arms share one agentd, one port pool and one
    // scripted model; running them concurrently would make the result depend on
    // scheduling, and a comparison rig whose answer depends on load is not a
    // measuring instrument.
    for (const arm of spec.arms) {
      for (let rep = 1; rep <= repetitions; rep++) {
        const label = `${arm.id} rep ${rep}/${repetitions}`
        console.log(`── ${label}: ${arm.topology}@${arm.version}`)
        const started = Date.now()
        const run = await runArm(ctx, { ...spec, repetitions }, arm, rep)
        runs.push(run.outcome)
        projects.push({ arm: arm.id, repetition: rep, project: run.project })
        console.log(`   ${label} done in ${Math.round((Date.now() - started) / 1000)}s (${run.project})`)
      }
    }
  } finally {
    await ctx.dispose()
  }

  const report = buildReport({
    task: {
      id: spec.id,
      description: spec.description,
      mockScript: spec.mockScript,
      rounds: spec.rounds.length,
      repetitions,
      rankBy: spec.rankBy,
      rankDirection: spec.rankDirection ?? 'desc',
    },
    arms: spec.arms.map((a) => ({
      id: a.id,
      topology: `${a.topology}@${a.version}`,
      eventType: a.eventType,
      primaryWorker: a.primaryWorker,
      answers: a.answers,
    })),
    properties: spec.properties.map((p) => ({ id: p.id, describe: p.describe })),
    runs,
  })

  fs.mkdirSync(args.outDir, { recursive: true })
  const jsonPath = path.join(args.outDir, `${spec.id}.report.json`)
  const mdPath = path.join(args.outDir, `${spec.id}.report.md`)
  const metaPath = path.join(args.outDir, `${spec.id}.run-metadata.json`)
  fs.writeFileSync(jsonPath, `${JSON.stringify(report, null, 2)}\n`)
  fs.writeFileSync(mdPath, `${renderMarkdown(report)}\n`)

  console.log(`\n${report.table}\n`)

  // Everything volatile goes here and nowhere else: the report is diffed
  // byte-for-byte between two runs to prove determinism, so a timestamp or a
  // run-scoped project name inside it would defeat the check it exists for.
  let occupancy = 'not measured'
  try {
    occupancy = describeOccupancy(await measureSettled())
  } catch (e) {
    occupancy = `could not measure: ${e instanceof Error ? e.message : String(e)}`
  }
  fs.writeFileSync(
    metaPath,
    `${JSON.stringify({ startedAt, finishedAt: new Date().toISOString(), baseURL: args.baseURL, projects, occupancy }, null, 2)}\n`,
  )

  console.log(`report:   ${jsonPath}`)
  console.log(`markdown: ${mdPath}`)
  console.log(`metadata: ${metaPath}`)
  console.log(`stack after the run: ${occupancy}`)
}

main().catch((e) => {
  console.error(e instanceof Error ? e.stack ?? e.message : String(e))
  process.exit(1)
})
