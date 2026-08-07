// doctrine.ts — the operations doctrine block, read from its canonical file.
//
// `docs/product/20-operations-doctrine.md` §3 (decision D5) says doctrine ships
// as versioned BYTES in this repo and reaches workers as one ordinary operator
// mutation on the project prompt — no engine code, no new seam. This file is the
// rig's half of that: it reads the canonical file and hands the runner a string.
//
// Three rules, all of them the reason this is a module and not two lines inline:
//
//   1. **The file is the authority, never a copy.** A doctrine block pasted into
//      a config would drift from `docs/product/doctrine/doctrine-v1.md` the first
//      time either changed, and the run record would then name a version it did
//      not inject. The bytes are read at run time from the one file.
//   2. **The marker line is the contract.** Everything from
//      `=== operations doctrine v1 ===` (inclusive) to end of file is the block;
//      everything above it is prose ABOUT the doctrine — a title, an HTML comment
//      about immutability — and must never reach a worker. A missing marker is a
//      hard failure, not a silent fallback to "the whole file": injecting the
//      editorial header would put "Status: every entry CANDIDATE" into the
//      composed prompt of every worker in the project.
//   3. **The phrase the mock keys on lives here too.** `DOCTRINE_DELIVERY_PHRASE`
//      is the substring a mock-script rule uses to prove the block reached a
//      COMPOSED PROMPT rather than merely a settings row (the standing trap:
//      "reading back a stored value proves storage, not delivery"). It is
//      asserted to be part of the canonical bytes by doctrine.test.ts, so the
//      script and the doctrine cannot drift apart without a red test.

import * as fs from 'node:fs'
import * as path from 'node:path'

/** Doctrine versions this rig knows how to inject. One file per version. */
export type DoctrineVersion = 'v1'

/**
 * Repo root, resolved from the COMPILED location.
 *
 * `__dirname` is `e2e/experiments/dist/experiments/calibration`, so the root is
 * five levels up — the same arithmetic calibrate.ts does, for the same reason.
 */
const REPO_ROOT = path.resolve(__dirname, '../../../../..')

/** Where a version's canonical bytes live, repo-relative. */
export function doctrinePath(version: DoctrineVersion): string {
  return `docs/product/doctrine/doctrine-${version}.md`
}

/**
 * The marker line that opens the injected block.
 *
 * Written into the canonical file by doc 20 §5 and quoted in its own header
 * comment ("Rigs inject everything below the marker line, verbatim").
 */
export function doctrineMarker(version: DoctrineVersion): string {
  return `=== operations doctrine ${version} ===`
}

/**
 * The block, cut out of a doctrine file's full text.
 *
 * Pure so the marker rule is checkable without a filesystem. The marker must
 * start a line — a mention of the marker inside a sentence is prose about the
 * doctrine, not the doctrine — and everything from that line to EOF is returned
 * verbatim, trailing newline included. The composer trims whitespace around the
 * project-prompt section anyway (`go/compose.go`), so verbatim costs nothing and
 * keeps the file and the injected bytes comparable with `diff`.
 */
export function extractDoctrine(fileText: string, marker: string): string {
  const lines = fileText.split('\n')
  const at = lines.findIndex((line) => line.trimEnd() === marker)
  if (at === -1) {
    throw new Error(
      `doctrine: no marker line ${JSON.stringify(marker)} in the doctrine file. ` +
        'The marker is the contract between the doc and the rig: everything from it to end of file ' +
        'is what gets injected, and without it the rig would have to guess (and would inject the ' +
        "file's editorial header into every worker's prompt). Fix the file, not this loader.",
    )
  }
  return lines.slice(at).join('\n')
}

/** Reads a version's canonical bytes and returns the injectable block. */
export function loadDoctrine(version: DoctrineVersion): string {
  const file = path.join(REPO_ROOT, doctrinePath(version))
  let text: string
  try {
    text = fs.readFileSync(file, 'utf8')
  } catch (e) {
    throw new Error(
      `doctrine: cannot read ${doctrinePath(version)} (looked in ${file}): ` +
        `${e instanceof Error ? e.message : String(e)}`,
    )
  }
  const block = extractDoctrine(text, doctrineMarker(version))
  if (block.trim() === doctrineMarker(version)) {
    throw new Error(`doctrine: ${doctrinePath(version)} has a marker line and nothing after it`)
  }
  return block
}

/**
 * The substring a mock-script rule keys on to prove DELIVERY.
 *
 * It is doctrine-v1's WD-2 sentence (output contracts are met exactly), chosen
 * for three properties, all load-bearing:
 *
 *   * it is inside the injected block, so it can only appear in a composed
 *     prompt if the block reached it;
 *   * it is one line, and contains no `"`, `\` or newline — the mock matches a
 *     raw substring against the JSON-encoded request body, and a phrase carrying
 *     any of those would be escaped in the body and never match (a quiet
 *     always-false rule is the worst possible tripwire);
 *   * nothing else in the calibration rig, the topology seed or the event text
 *     says it, so a rule keyed on it selects doctrine-carrying requests only.
 *
 * doctrine.test.ts pins it against the canonical file AND against the mock
 * script that uses it, so the three cannot drift.
 */
export const DOCTRINE_DELIVERY_PHRASE =
  'If your charter states an output contract, your final message must satisfy it'
