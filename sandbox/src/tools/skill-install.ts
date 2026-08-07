// skill-install.ts — the in-session half of `skill_install` (spec
// docs/product/08-images-and-skills.md §14.2).
//
// The tool itself lives on the HOST (agentd owns the skills catalogue and the
// tenancy boundary); the WORK is necessarily in the image, because a skill
// installs software into this container's filesystem. So agentd POSTs the
// skill's markdown and its install script down here and this module does two
// things, in this order:
//
//   1. writes the markdown to <skillsDir>/<name>/SKILL.md, where the harness
//      picks it up the way it picks up any Claude Code project skill;
//   2. runs install_sh, capturing its exit status, stdout and stderr.
//
// BOTH outcomes are reported. §14.2 is explicit that a failed install must be a
// visible failure the worker can react to and never a silent no-op, so nothing
// here swallows an error: a script that exits non-zero, times out, or cannot be
// spawned comes back as a result that says so, with its output attached.
//
// The document is written FIRST and stays written even when the script fails.
// That is deliberate: knowing how to do something is useful even when the
// software did not install, and the worker is told exactly which half worked.

import { spawn } from 'child_process';
import { mkdir, writeFile, rm } from 'fs/promises';
import { join } from 'path';
import { tmpdir } from 'os';

/**
 * Where the harness looks for project skills.
 *
 * The Claude Agent SDK is started with `cwd: /workspace` and
 * `settingSources: ['project']` (see harness/claude-agent-sdk.ts), so a project
 * skill is `<cwd>/.claude/skills/<name>/SKILL.md`. If either of those harness
 * settings ever changes, this constant must move with it — the two are one
 * decision spelled in two places, and nothing else connects them.
 */
export const SKILLS_DIR = '/workspace/.claude/skills';

/**
 * How long an install script may run before it is killed. Generous, because an
 * install may compile something; bounded, because a script that hangs would
 * otherwise hang the job with nothing to show for it. Slightly under agentd's
 * own client timeout, so the timeout is reported by the side that can see the
 * script's output.
 */
export const INSTALL_TIMEOUT_MS = 14 * 60 * 1000;

/**
 * How much of each stream is kept. Install scripts are chatty (apt, npm), and
 * the whole point of capturing output is that a human or a model reads it — so
 * the head and the TAIL are kept, the tail because that is where a failing
 * script says why.
 */
export const MAX_STREAM_CHARS = 20000;

/**
 * The skill-name charset, which must agree with `ValidateSkillName` in
 * go/agentdb/skills.go. It is re-checked here rather than trusted: the name
 * becomes a DIRECTORY under SKILLS_DIR, and a component that can traverse out
 * of it would write an arbitrary file into the container.
 */
const SKILL_NAME_RE = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/;

export interface SkillInstallRequest {
  name: string;
  markdown: string;
  install_sh?: string;
}

export interface SkillScriptResult {
  ran: boolean;
  exit_code: number;
  stdout: string;
  stderr: string;
  timed_out: boolean;
  error?: string;
}

export interface SkillInstallResult {
  ok: boolean;
  name: string;
  path: string;
  bytes_written: number;
  script: SkillScriptResult;
  error?: string;
}

export class InvalidSkillInstallError extends Error {}

/** Keep the head and the tail of a stream, marking what was dropped. */
export function truncateStream(s: string, max = MAX_STREAM_CHARS): string {
  if (s.length <= max) return s;
  const half = Math.floor(max / 2);
  return `${s.slice(0, half)}\n… (${s.length - max} characters omitted) …\n${s.slice(-half)}`;
}

const NOT_RUN: SkillScriptResult = {
  ran: false,
  exit_code: 0,
  stdout: '',
  stderr: '',
  timed_out: false,
};

/**
 * Run an install script, resolving (never rejecting) with what happened.
 *
 * It is written to a temp file and run with `bash <file>` rather than piped to
 * a shell's stdin: a script that reads stdin (an apt prompt, a `read`) would
 * otherwise consume its own source and behave differently here than it did in
 * the session where a worker wrote it. stdin is /dev/null for the same reason —
 * an interactive prompt must fail, not hang.
 */
export async function runInstallScript(
  script: string,
  opts: { cwd?: string; timeoutMs?: number } = {},
): Promise<SkillScriptResult> {
  const cwd = opts.cwd ?? '/workspace';
  const timeoutMs = opts.timeoutMs ?? INSTALL_TIMEOUT_MS;
  const scriptPath = join(await mkdtempish(), 'install.sh');
  await writeFile(scriptPath, script, { mode: 0o700 });

  return new Promise<SkillScriptResult>((resolve) => {
    let child;
    try {
      child = spawn('bash', [scriptPath], {
        cwd,
        stdio: ['ignore', 'pipe', 'pipe'],
        env: process.env,
        // Its own process group, so a timeout can kill the WHOLE tree. Killing
        // bash alone leaves whatever it started (the `sleep`, the download)
        // running and holding the pipes open, and 'close' never fires — the
        // timeout would hang the very call it exists to bound.
        detached: true,
      });
    } catch (err) {
      resolve({ ...NOT_RUN, ran: true, exit_code: -1, error: `could not start the install script: ${String(err)}` });
      return;
    }

    let stdout = '';
    let stderr = '';
    let timedOut = false;
    let settled = false;

    child.stdout?.on('data', (c) => { stdout += c.toString(); });
    child.stderr?.on('data', (c) => { stderr += c.toString(); });

    const killTree = () => {
      try {
        if (child.pid) process.kill(-child.pid, 'SIGKILL');
      } catch {
        // Group gone, or no process groups on this platform: fall back.
        try { child.kill('SIGKILL'); } catch { /* already dead */ }
      }
    };

    let giveUp: NodeJS.Timeout | undefined;
    const timer = setTimeout(() => {
      timedOut = true;
      killTree();
      // Last resort: if something still holds the pipes open after the group
      // was killed, report the timeout anyway rather than never returning.
      giveUp = setTimeout(() => {
        finish({
          ran: true,
          exit_code: -1,
          stdout: truncateStream(stdout),
          stderr: truncateStream(stderr),
          timed_out: true,
          error: `install script exceeded ${Math.round(timeoutMs / 1000)}s and did not die cleanly`,
        });
      }, 5000);
      giveUp.unref?.();
    }, timeoutMs);

    const finish = (result: SkillScriptResult) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      if (giveUp) clearTimeout(giveUp);
      void rm(scriptPath, { force: true }).catch(() => {});
      resolve(result);
    };

    child.on('error', (err) => {
      finish({
        ran: true,
        exit_code: -1,
        stdout: truncateStream(stdout),
        stderr: truncateStream(stderr),
        timed_out: timedOut,
        error: `install script could not be run: ${err.message}`,
      });
    });

    child.on('close', (code, signal) => {
      finish({
        ran: true,
        // A killed process reports a null code; -1 is "it did not exit
        // normally", which is never mistaken for success.
        exit_code: code === null ? -1 : code,
        stdout: truncateStream(stdout),
        stderr: truncateStream(stderr),
        timed_out: timedOut,
        error: timedOut
          ? `install script exceeded ${Math.round(timeoutMs / 1000)}s and was killed`
          : signal
            ? `install script was killed by ${signal}`
            : undefined,
      });
    });
  });
}

async function mkdtempish(): Promise<string> {
  const dir = join(tmpdir(), `skill-install-${process.pid}-${Date.now()}-${Math.random().toString(36).slice(2)}`);
  await mkdir(dir, { recursive: true, mode: 0o700 });
  return dir;
}

/**
 * Install one skill into this session: write the document, then run the script.
 *
 * Throws InvalidSkillInstallError for a malformed request (the route turns that
 * into a 400). Every other outcome — including a script that fails — RESOLVES,
 * carrying `ok: false` and the whole story, because "the install failed" is an
 * answer the caller must be given, not an exception to be lost.
 */
export async function installSkill(
  req: SkillInstallRequest,
  opts: { skillsDir?: string; cwd?: string; timeoutMs?: number } = {},
): Promise<SkillInstallResult> {
  const name = (req?.name ?? '').trim();
  if (!SKILL_NAME_RE.test(name)) {
    throw new InvalidSkillInstallError(
      `invalid skill name ${JSON.stringify(name)}: kebab-case only (lowercase alphanumerics and '-'), because it becomes a directory name`,
    );
  }
  const markdown = req?.markdown ?? '';
  if (markdown.trim() === '') {
    throw new InvalidSkillInstallError('markdown is required: a skill with no document teaches nothing');
  }

  const skillsDir = opts.skillsDir ?? SKILLS_DIR;
  const dir = join(skillsDir, name);
  const path = join(dir, 'SKILL.md');

  try {
    await mkdir(dir, { recursive: true });
    await writeFile(path, markdown, 'utf-8');
  } catch (err) {
    // The document is the half that makes the skill usable at all, so failing
    // to write it fails the install outright — the script is not attempted.
    return {
      ok: false,
      name,
      path: '',
      bytes_written: 0,
      script: { ...NOT_RUN },
      error: `could not write the skill document to ${path}: ${String(err)}`,
    };
  }

  const bytesWritten = Buffer.byteLength(markdown, 'utf-8');
  const script = (req.install_sh ?? '').trim();
  if (script === '') {
    return { ok: true, name, path, bytes_written: bytesWritten, script: { ...NOT_RUN } };
  }

  const result = await runInstallScript(req.install_sh as string, { cwd: opts.cwd, timeoutMs: opts.timeoutMs });
  return {
    ok: result.exit_code === 0 && !result.timed_out && !result.error,
    name,
    path,
    bytes_written: bytesWritten,
    script: result,
  };
}
