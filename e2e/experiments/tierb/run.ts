// Tier B runner: candidates file + run config -> curve JSON + a printed table.
//
// This is the surface the (L3-gated) real run uses. It is deliberately thin —
// all the behaviour lives in collect/grade/score, which are the tested files.
//
//   node --experimental-strip-types e2e/experiments/tierb/run.ts \
//     --candidates run/candidates.json \
//     --config     run/config.json \
//     --out        run/curve.json
//
// The config file:
//   {
//     "seed": "2026-07-28-run-1",
//     "batchSize": 4,
//     "anchors": [{ "id": "...", "text": "..." }, { "id": "...", "text": "..." }],
//     "grader": { "type": "anthropic", "model": "claude-opus-5" }
//     // or:    { "type": "scripted", "rules": [{ "match": "Title:", "score": 4 }] }
//   }
//
// EXECUTION AGAINST A REAL MODEL IS GATED (13-work-plan-self-improvement.md,
// Wave 4 / L3): Kai's explicit go on credential mode and token ceiling. The
// `scripted` grader needs no credential and is what the offline suite uses.

import { readFileSync, realpathSync, writeFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { readCandidatesFile } from './collect.ts'
import type { Anchor, GradeConfig, GraderSeam, ScriptedRule } from './grade.ts'
import { anthropicGrader, runGrading, scriptedGrader } from './grade.ts'
import type { CurveReport } from './score.ts'
import { formatCurve, scoreRun } from './score.ts'
import type { CandidateSet } from './collect.ts'

export type GraderSpec =
  | { type: 'scripted'; rules?: ScriptedRule[]; base?: number; positionBias?: number }
  | { type: 'anthropic'; model?: string; maxTokens?: number; baseUrl?: string }

export interface RunConfigFile {
  seed: string
  batchSize?: number
  anchors: Anchor[]
  grader: GraderSpec
  criterion?: string
  task?: string
}

export function graderFromSpec(spec: GraderSpec): GraderSeam {
  switch (spec.type) {
    case 'scripted':
      return scriptedGrader({ rules: spec.rules, base: spec.base, positionBias: spec.positionBias })
    case 'anthropic':
      return anthropicGrader({ model: spec.model, maxTokens: spec.maxTokens, baseUrl: spec.baseUrl })
    default:
      throw new Error(`unknown grader type ${JSON.stringify((spec as { type: string }).type)}`)
  }
}

/** One end-to-end pass: build blind batches, grade them, score the curve. */
export async function runTierB(set: CandidateSet, config: GradeConfig): Promise<CurveReport> {
  const { batches, rankings } = await runGrading(set, config)
  return scoreRun({
    set,
    anchors: config.anchors,
    batches,
    rankings,
    seed: String(config.seed),
  })
}

function argOf(argv: readonly string[], name: string): string | undefined {
  const i = argv.indexOf(`--${name}`)
  return i >= 0 ? argv[i + 1] : undefined
}

async function main(argv: string[]): Promise<void> {
  const candidatesPath = argOf(argv, 'candidates')
  const configPath = argOf(argv, 'config')
  if (!candidatesPath || !configPath) {
    throw new Error('usage: run.ts --candidates <file> --config <file> [--out <file>]')
  }
  const set = readCandidatesFile(candidatesPath)
  const cfg = JSON.parse(readFileSync(configPath, 'utf8')) as RunConfigFile
  const report = await runTierB(set, {
    anchors: cfg.anchors,
    grader: graderFromSpec(cfg.grader),
    seed: cfg.seed,
    batchSize: cfg.batchSize,
    criterion: cfg.criterion,
    task: cfg.task,
  })
  const out = argOf(argv, 'out')
  if (out) writeFileSync(out, `${JSON.stringify(report, null, 2)}\n`, 'utf8')
  console.log(formatCurve(report))
}

/** True only when this file is the entry point — importing it must not run main. */
function invokedDirectly(): boolean {
  const entry = process.argv[1]
  if (!entry) return false
  try {
    return fileURLToPath(import.meta.url) === realpathSync(entry)
  } catch {
    return false
  }
}

if (invokedDirectly()) {
  main(process.argv.slice(2)).catch((err: unknown) => {
    console.error(err instanceof Error ? err.message : String(err))
    process.exitCode = 1
  })
}
