// triage.ts — the triage rig's entry point (work plan 13, SC1; doc 19 §3 SC-1).
//
//   ./e2e/experiments/triage/run.sh run triage-smoke-6   (mock, offline, free)
//   ./e2e/experiments/triage/run.sh run triage-24        (LIVE — tokens, attended)
//
// N tickets whose correct destination the harness is holding, through the same
// org chart wired two ways, scoring each route against a FACT rather than a
// judge. SC-1 exists because the calibration lab measures analytical discipline
// and barely exercises coordination — MAST's 37%, and the biggest failure class
// with no instrument pointed at it (doc 19 §1).
//
// **What a mock run proves.** Everything below runs identically against the
// scripted mock model, where every dispatcher decision is authored into the
// script. A mock report's accuracy columns measure the SCRIPT. They exist to
// prove the machinery — that tickets reach the dispatcher, routes are parsed
// from deliverables, truth reaches only the frozen auditor, sessions get swept,
// and every metric registers — so that a failure in the live run is a model
// failure rather than a harness one. `docs/AGENTS_RESEARCH.md` §7, Tier A.
//
// Structure follows the calibration rig: this file is I/O and orchestration
// only, runner.ts talks to the stack, metrics.ts does the arithmetic, and
// neither imports the other.

import { request as pwRequest } from '@playwright/test'
import * as fs from 'node:fs'
import * as path from 'node:path'
import { describe as describeOccupancy, measureSettled } from '../../helpers/occupancy'
import { buildReport, renderMarkdown, type ArmOutcome } from './metrics'
import { runArm } from './runner'
import type { TriageSpec } from './spec'
import { routingRules } from './text'
import { loadTruths } from './truths'

// Paths, resolved from the COMPILED location. __dirname is
// e2e/experiments/dist/experiments/triage, so the source directory is three
// levels up plus `triage`, and the repo root is five.
const SOURCE_DIR = path.resolve(__dirname, '../../..', 'triage')
const REPO_ROOT = path.resolve(__dirname, '../../../../..')

interface Args {
  config: string
  arms?: string[]
  limit?: number
  outDir: string
  datasetDir: string
  baseURL: string
  printConfig: boolean
}

function parseArgs(argv: string[]): Args {
  const args: Args = {
    config: '',
    // Beside the SOURCE, not beside the compiled JS.
    outDir: path.join(SOURCE_DIR, 'reports'),
    datasetDir: '',
    baseURL: process.env.STACK_BASE_URL || 'http://localhost:8080',
    printConfig: false,
  }
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i]
    if (a === '--arms') args.arms = argv[++i].split(',').map((s) => s.trim()).filter(Boolean)
    else if (a === '--limit') args.limit = Number(argv[++i])
    else if (a === '--out') args.outDir = path.resolve(argv[++i])
    else if (a === '--datasets') args.datasetDir = path.resolve(argv[++i])
    else if (a === '--base-url') args.baseURL = argv[++i]
    else if (a === '--print-config') args.printConfig = true
    else if (a.startsWith('-')) throw new Error(`unknown flag: ${a}`)
    else args.config = a
  }
  if (!args.config) throw new Error('usage: triage <config-name> [--arms a,b] [--limit N] [--out DIR]')
  return args
}

/** Loads a triage config module by name from ./configs. */
function loadSpec(name: string): TriageSpec {
  const file = path.join(__dirname, 'configs', `${name}.js`)
  if (!fs.existsSync(file)) {
    const available = fs
      .readdirSync(path.join(__dirname, 'configs'))
      .filter((f) => f.endsWith('.js'))
      .map((f) => f.replace(/\.js$/, ''))
    throw new Error(`no triage config named ${name}; available: ${available.join(', ') || '(none)'}`)
  }
  // eslint-disable-next-line @typescript-eslint/no-var-requires
  const mod = require(file) as { spec?: TriageSpec; default?: TriageSpec }
  const spec = mod.spec ?? mod.default
  if (!spec) throw new Error(`${name} exports neither \`spec\` nor a default export`)
  return spec
}

/**
 * Which arms to run.
 *
 * Optional arms are skipped unless named. Making the budget decision an explicit
 * argument rather than a default is the difference between spending more tokens
 * on purpose and by accident.
 */
function selectArms(spec: TriageSpec, wanted?: string[]): TriageSpec['arms'] {
  if (!wanted || wanted.length === 0) return spec.arms.filter((a) => !a.optional)
  const known = new Set(spec.arms.map((a) => a.id))
  for (const id of wanted) {
    if (!known.has(id)) throw new Error(`no arm ${id} in ${spec.id}; arms: ${[...known].join(', ')}`)
  }
  return spec.arms.filter((a) => wanted.includes(a.id))
}

async function main(): Promise<void> {
  const args = parseArgs(process.argv.slice(2))
  const spec = loadSpec(args.config)

  // run.sh asks for this before it touches agentd, so the mock-script path, the
  // manifest and the credential mode each have exactly one home: the config.
  if (args.printConfig) {
    process.stdout.write(
      `${JSON.stringify({
        id: spec.id,
        mode: spec.mode,
        mockScript: spec.mockScript ?? '',
        manifest: spec.manifest,
        datasetDir: spec.datasetDir,
      })}\n`,
    )
    return
  }

  const datasetDir = args.datasetDir || path.resolve(REPO_ROOT, spec.datasetDir)
  const truths = loadTruths(datasetDir)
  const tickets = args.limit ? truths.tickets.slice(0, args.limit) : truths.tickets
  const arms = selectArms(spec, args.arms)
  if (arms.length === 0) throw new Error('no arms selected')

  console.log(`── ${spec.id} (${spec.mode}): ${tickets.length} tickets × ${arms.length} arms`)
  console.log(`   tickets: ${datasetDir} (seed ${truths.seed})`)
  console.log(`   arms:    ${arms.map((a) => a.id).join(', ')}`)

  const ctx = await pwRequest.newContext({ baseURL: args.baseURL })
  const startedAt = new Date().toISOString()
  const outcomes: ArmOutcome[] = []
  const projects: Array<{ arm: string; project: string }> = []
  const logs: Record<string, unknown> = {}

  try {
    // Sequential on purpose: the arms share one agentd, one port pool and one
    // model. A comparison whose answer depends on load is not an instrument.
    for (const arm of arms) {
      console.log(`── arm ${arm.id}: ${arm.note}`)
      const started = Date.now()
      const run = await runArm(ctx, spec, arm, tickets, (line) => console.log(line))
      outcomes.push(run.outcome)
      projects.push({ arm: arm.id, project: run.project })
      logs[arm.id] = run.log
      console.log(`   arm ${arm.id} done in ${Math.round((Date.now() - started) / 1000)}s (${run.project})`)
    }
  } finally {
    await ctx.dispose()
  }

  const report = buildReport({
    run: {
      id: spec.id,
      description: spec.description,
      mode: spec.mode,
      ...(spec.mockScript ? { mockScript: spec.mockScript } : {}),
      manifest: spec.manifest,
      datasetSeed: truths.seed,
      window: spec.window,
      dailyTokensHard: spec.dailyTokensHard,
    },
    arms: arms.map((a) => ({
      id: a.id,
      note: a.note,
      dispatcher: a.dispatcher,
      queues: a.queues,
      critic: a.critic,
      auditor: a.auditor,
      criticDisabled: a.disableCritic === true,
      // The charter this arm was actually given. Recorded because "what the org
      // was told" is the one input a reader cannot reconstruct from the numbers,
      // and it is deterministic (worker names are fixed per arm).
      routingRules: routingRules(a.queues),
    })),
    outcomes,
  })

  fs.mkdirSync(args.outDir, { recursive: true })
  const jsonPath = path.join(args.outDir, `${spec.id}.report.json`)
  const mdPath = path.join(args.outDir, `${spec.id}.report.md`)
  const metaPath = path.join(args.outDir, `${spec.id}.run-metadata.json`)
  const logPath = path.join(args.outDir, `${spec.id}.run-log.json`)
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
  // The raw event and config logs per arm, ids and timestamps included. Not
  // diffed, and not the report — this is what gets copied into
  // docs/product/runs/<date>-triage/ after a live run.
  fs.writeFileSync(logPath, `${JSON.stringify(logs, null, 2)}\n`)

  console.log(`report:   ${jsonPath}`)
  console.log(`markdown: ${mdPath}`)
  console.log(`metadata: ${metaPath}`)
  console.log(`run log:  ${logPath}`)
  console.log(`stack after the run: ${occupancy}`)

  const aborted = outcomes.filter((o) => o.abortReason)
  if (aborted.length > 0) {
    console.error(`\n!! ${aborted.length} arm(s) aborted:`)
    for (const a of aborted) console.error(`   ${a.arm}: ${a.abortReason}`)
    process.exitCode = 1
  }
}

main().catch((e) => {
  console.error(e instanceof Error ? e.stack ?? e.message : String(e))
  process.exit(1)
})
