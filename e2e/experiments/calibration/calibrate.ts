// calibrate.ts — the calibration rig's entry point (work plan 13, L3R).
//
//   ./e2e/experiments/calibration/run.sh run smoke-4          (mock, offline, free)
//   ./e2e/experiments/calibration/run.sh run calibration-30   (LIVE — tokens, attended)
//
// Plays docs/product/14-calibration-runbook.md §2's loop: 30 hypotheses whose
// answers the harness is holding, through the same org chart wired three ways,
// scoring each conclusion against a FACT rather than a judge. That is the whole
// point of calibrating here first — a flat accuracy line means "the loop did not
// improve investigation", not "the instrument is blind" (§1).
//
// **What a mock run proves.** Everything below runs identically against the
// scripted mock model, where every investigator answer is authored into the
// script. A mock report's accuracy columns measure the SCRIPT. They exist to
// prove the machinery — that datasets reach the investigator, verdicts are
// parsed from deliverables, truth reaches only the frozen checker, sessions get
// swept, and every metric registers — so that a failure in the live run is a
// model failure rather than a harness one. `docs/AGENTS_RESEARCH.md` §7, Tier A.
//
// Structure follows the C1 rig: this file is I/O and orchestration only,
// runner.ts talks to the stack, metrics.ts does the arithmetic, and neither
// imports the other.

import { request as pwRequest } from '@playwright/test'
import * as fs from 'node:fs'
import * as path from 'node:path'
import { describe as describeOccupancy, measureSettled } from '../../helpers/occupancy'
import { buildReport, renderMarkdown, type ArmOutcome } from './metrics'
import { runArm } from './runner'
import type { CalibrationSpec } from './spec'
import { loadTruths } from './truths'

// Paths, resolved from the COMPILED location. __dirname is
// e2e/experiments/dist/experiments/calibration, so the source directory is
// three levels up plus `calibration`, and the repo root is five.
const SOURCE_DIR = path.resolve(__dirname, '../../..', 'calibration')
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
  if (!args.config) throw new Error('usage: calibrate <config-name> [--arms a,b] [--limit N] [--out DIR]')
  return args
}

/** Loads a calibration config module by name from ./configs. */
function loadSpec(name: string): CalibrationSpec {
  const file = path.join(__dirname, 'configs', `${name}.js`)
  if (!fs.existsSync(file)) {
    const available = fs
      .readdirSync(path.join(__dirname, 'configs'))
      .filter((f) => f.endsWith('.js'))
      .map((f) => f.replace(/\.js$/, ''))
    throw new Error(`no calibration config named ${name}; available: ${available.join(', ') || '(none)'}`)
  }
  // eslint-disable-next-line @typescript-eslint/no-var-requires
  const mod = require(file) as { spec?: CalibrationSpec; default?: CalibrationSpec }
  const spec = mod.spec ?? mod.default
  if (!spec) throw new Error(`${name} exports neither \`spec\` nor a default export`)
  return spec
}

/**
 * Which arms to run.
 *
 * Optional arms (C, the sham) are skipped unless named. The runbook calls A+B
 * the minimum honest run and C "if budget allows"; making the budget decision
 * an explicit argument rather than a default is the difference between spending
 * a third more tokens on purpose and by accident.
 */
function selectArms(spec: CalibrationSpec, wanted?: string[]): CalibrationSpec['arms'] {
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
  const hypotheses = args.limit ? truths.hypotheses.slice(0, args.limit) : truths.hypotheses
  const arms = selectArms(spec, args.arms)
  if (arms.length === 0) throw new Error('no arms selected')

  console.log(`── ${spec.id} (${spec.mode}): ${hypotheses.length} hypotheses × ${arms.length} arms`)
  console.log(`   datasets: ${datasetDir} (seed ${truths.seed})`)
  console.log(`   arms:     ${arms.map((a) => a.id).join(', ')}`)

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
      const run = await runArm(ctx, spec, arm, hypotheses, (line) => console.log(line))
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
      covariatesHint: spec.covariatesHint,
    },
    arms: arms.map((a) => ({
      id: a.id,
      note: a.note,
      investigator: a.investigator,
      critic: a.critic,
      checker: a.checker,
      criticDisabled: a.disableCritic === true,
      criticShammed: a.criticPromptOverride !== undefined,
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
  // Runbook §4's record: the raw event and config logs per arm, ids and
  // timestamps included. Not diffed, and not the report — this is what gets
  // copied into docs/product/runs/<date>-calibration/ after a live run.
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
