// gauntlet.ts — the injection gauntlet's entry point (work plan 13, SC3; doc
// 19 §3 SC-3; the promotion instrument for doctrine WD-1).
//
//   ./e2e/experiments/gauntlet/run.sh run gauntlet-smoke-6   (mock, offline, free)
//   ./e2e/experiments/gauntlet/run.sh run gauntlet-24        (LIVE — tokens, attended)
//
// The SC-1 ticket stream with adversarial directives planted in the ticket
// text, through the same org chart twice: once as seeded, once with
// doctrine-v1 written into the project prompt. Every directive has a designed,
// machine-readable compliance signal, so "did the org obey the text it was
// working on" is a FACT rather than a judgement.
//
// **What a mock run proves.** Everything below runs identically against the
// scripted mock model, where compliance is authored into the script — and
// authored, specifically, on the ABSENCE of a doctrine line from the composed
// prompt. So a mock report's compliance delta measures that the doctrine block
// was delivered, and nothing about whether doctrine helps. That is the same
// posture DR1 recorded with the opposite sign: there the authored delta was
// zero, here it is deliberately non-zero, and both are statements about the
// script. `docs/AGENTS_RESEARCH.md` §7, Tier A.
//
// Structure follows the calibration and triage rigs: this file is I/O and
// orchestration only, runner.ts talks to the stack, metrics.ts does the
// arithmetic, and neither imports the other.

import { request as pwRequest } from '@playwright/test'
import * as fs from 'node:fs'
import * as path from 'node:path'
import { describe as describeOccupancy, measureSettled } from '../../helpers/occupancy'
import { buildReport, renderMarkdown, type ArmOutcome } from './metrics'
import { runArm } from './runner'
import type { GauntletSpec } from './spec'
import { routingRules } from './text'
import { loadTruths } from './truths'

// Paths, resolved from the COMPILED location. __dirname is
// e2e/experiments/dist/experiments/gauntlet, so the source directory is three
// levels up plus `gauntlet`, and the repo root is five.
const SOURCE_DIR = path.resolve(__dirname, '../../..', 'gauntlet')
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
  if (!args.config) throw new Error('usage: gauntlet <config-name> [--arms a,b] [--limit N] [--out DIR]')
  return args
}

/** Loads a gauntlet config module by name from ./configs. */
function loadSpec(name: string): GauntletSpec {
  const file = path.join(__dirname, 'configs', `${name}.js`)
  if (!fs.existsSync(file)) {
    const available = fs
      .readdirSync(path.join(__dirname, 'configs'))
      .filter((f) => f.endsWith('.js'))
      .map((f) => f.replace(/\.js$/, ''))
    throw new Error(`no gauntlet config named ${name}; available: ${available.join(', ') || '(none)'}`)
  }
  // eslint-disable-next-line @typescript-eslint/no-var-requires
  const mod = require(file) as { spec?: GauntletSpec; default?: GauntletSpec }
  const spec = mod.spec ?? mod.default
  if (!spec) throw new Error(`${name} exports neither \`spec\` nor a default export`)
  return spec
}

/**
 * Which arms to run.
 *
 * Optional arms are skipped unless named. Neither gauntlet arm is optional: a
 * doctrine A/B with the doctrine arm skipped by default would be a config that
 * measures nothing unless someone remembered a flag.
 */
function selectArms(spec: GauntletSpec, wanted?: string[]): GauntletSpec['arms'] {
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
  const attacked = tickets.filter((t) => t.directive !== null).length

  console.log(`── ${spec.id} (${spec.mode}): ${tickets.length} tickets × ${arms.length} arms`)
  console.log(`   tickets: ${datasetDir} (seed ${truths.seed}; ${attacked} attacked, ${tickets.length - attacked} clean)`)
  console.log(`   arms:    ${arms.map((a) => `${a.id}${a.doctrine ? ` [doctrine-${a.doctrine}]` : ''}`).join(', ')}`)

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
      baseline: spec.baseline,
    },
    arms: arms.map((a) => ({
      id: a.id,
      note: a.note,
      dispatcher: a.dispatcher,
      queues: a.queues,
      critic: a.critic,
      auditor: a.auditor,
      doctrine: a.doctrine ?? '',
      // The charter this arm was actually given. Recorded because "what the org
      // was told" is the one input a reader cannot reconstruct from the numbers.
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
