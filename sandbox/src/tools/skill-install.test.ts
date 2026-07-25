import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { mkdtemp, readFile, rm, stat } from 'fs/promises'
import { join } from 'path'
import { tmpdir } from 'os'
import Fastify from 'fastify'
import {
  installSkill,
  runInstallScript,
  truncateStream,
  InvalidSkillInstallError,
  SKILLS_DIR,
} from './skill-install.js'
import { skillRoutes } from '../routes/skills.js'

// ---------------------------------------------------------------------------
// I3 — the in-session half of skill_install (§14.2).
//
// The one invariant everything here exists to protect: a failed install is a
// VISIBLE failure, never a silent no-op. The document write and the script run
// are reported separately and honestly, and a name that could escape the skills
// directory is refused before either happens.
// ---------------------------------------------------------------------------

let dir: string

beforeEach(async () => {
  dir = await mkdtemp(join(tmpdir(), 'skills-test-'))
})

afterEach(async () => {
  await rm(dir, { recursive: true, force: true })
})

describe('installSkill — the document', () => {
  it('writes the markdown where the harness looks for a project skill', async () => {
    const result = await installSkill(
      { name: 'render-social-video', markdown: '# Render social video\n\nUse ffmpeg.' },
      { skillsDir: dir },
    )
    expect(result.ok).toBe(true)
    expect(result.path).toBe(join(dir, 'render-social-video', 'SKILL.md'))
    expect(await readFile(result.path, 'utf-8')).toContain('Use ffmpeg.')
    expect(result.bytes_written).toBeGreaterThan(0)
    // No script means "knowledge only" — and it says so rather than pretending
    // something ran.
    expect(result.script.ran).toBe(false)
  })

  it('uses the harness settings path by default', () => {
    // The SDK runs with cwd=/workspace and settingSources:['project'], so a
    // project skill is <cwd>/.claude/skills/<name>/SKILL.md. Pinned because the
    // two halves of that decision live in different files.
    expect(SKILLS_DIR).toBe('/workspace/.claude/skills')
  })

  it('overwrites a previous revision of the same skill', async () => {
    await installSkill({ name: 'a-skill', markdown: 'v1' }, { skillsDir: dir })
    const second = await installSkill({ name: 'a-skill', markdown: 'v2' }, { skillsDir: dir })
    expect(await readFile(second.path, 'utf-8')).toBe('v2')
  })
})

describe('installSkill — name validation (it becomes a directory)', () => {
  const bad = ['', '..', '../escape', 'a/b', 'a\\b', 'Upper', 'a_b', 'a.b', '-lead', 'trail-', 'a b']
  for (const name of bad) {
    it(`refuses ${JSON.stringify(name)} before writing anything`, async () => {
      await expect(installSkill({ name, markdown: '# doc' }, { skillsDir: dir }))
        .rejects.toBeInstanceOf(InvalidSkillInstallError)
    })
  }

  it('refuses an empty document', async () => {
    await expect(installSkill({ name: 'a-skill', markdown: '   ' }, { skillsDir: dir }))
      .rejects.toBeInstanceOf(InvalidSkillInstallError)
  })

  it('does not create a directory for a rejected name', async () => {
    await expect(installSkill({ name: '../escape', markdown: '# doc' }, { skillsDir: dir })).rejects.toThrow()
    await expect(stat(join(dir, '..', 'escape'))).rejects.toThrow()
  })
})

describe('installSkill — the script', () => {
  it('runs install_sh and reports a clean exit', async () => {
    const result = await installSkill(
      { name: 'a-skill', markdown: '# doc', install_sh: 'echo installed-ok' },
      { skillsDir: dir, cwd: dir },
    )
    expect(result.ok).toBe(true)
    expect(result.script.ran).toBe(true)
    expect(result.script.exit_code).toBe(0)
    expect(result.script.stdout).toContain('installed-ok')
  })

  it('reports a FAILING script as a failure, with its output — never a silent no-op', async () => {
    const result = await installSkill(
      {
        name: 'a-skill',
        markdown: '# doc',
        install_sh: 'echo doing-the-thing\necho something-broke >&2\nexit 3',
      },
      { skillsDir: dir, cwd: dir },
    )
    expect(result.ok).toBe(false)
    expect(result.script.ran).toBe(true)
    expect(result.script.exit_code).toBe(3)
    expect(result.script.stdout).toContain('doing-the-thing')
    expect(result.script.stderr).toContain('something-broke')
    // The document is still written, and the caller is told so: knowing how is
    // useful even when the software did not install.
    expect(result.path).not.toBe('')
    expect(await readFile(result.path, 'utf-8')).toBe('# doc')
  })

  it('kills a script that runs too long and says so', async () => {
    const result = await installSkill(
      { name: 'a-skill', markdown: '# doc', install_sh: 'sleep 30' },
      { skillsDir: dir, cwd: dir, timeoutMs: 250 },
    )
    expect(result.ok).toBe(false)
    expect(result.script.timed_out).toBe(true)
    expect(result.script.error).toMatch(/exceeded/)
  })

  it('gives the script no stdin, so an interactive prompt fails instead of hanging', async () => {
    const result = await runInstallScript('read -r answer; echo "got:$answer"', { cwd: dir, timeoutMs: 5000 })
    expect(result.timed_out).toBe(false)
    expect(result.stdout).toContain('got:')
  })

  it('runs the script from a file, so a script that reads stdin does not eat itself', async () => {
    // Piping a script into `sh` makes its own text the stdin a `read` consumes.
    // Written to a file, `read` sees the empty stdin above and the whole script
    // still runs.
    const result = await runInstallScript('echo one\nread -r x\necho two', { cwd: dir, timeoutMs: 5000 })
    expect(result.stdout).toContain('one')
    expect(result.stdout).toContain('two')
  })
})

describe('truncateStream', () => {
  it('keeps short output whole', () => {
    expect(truncateStream('hello', 100)).toBe('hello')
  })

  it('keeps the head AND the tail — a failing script says why at the end', () => {
    const s = 'H'.repeat(50) + 'T'.repeat(50)
    const out = truncateStream(s, 20)
    expect(out.startsWith('H'.repeat(10))).toBe(true)
    expect(out.endsWith('T'.repeat(10))).toBe(true)
    expect(out).toContain('omitted')
  })
})

describe('POST /skills/install', () => {
  async function server() {
    const app = Fastify({ logger: false })
    await app.register(skillRoutes)
    return app
  }

  it('400s a malformed name without touching the filesystem', async () => {
    const app = await server()
    const res = await app.inject({
      method: 'POST',
      url: '/skills/install',
      payload: { name: '../escape', markdown: '# doc' },
    })
    expect(res.statusCode).toBe(400)
    expect(res.json().ok).toBe(false)
    expect(res.json().error).toMatch(/kebab-case/)
    await app.close()
  })

  it('400s a missing body', async () => {
    const app = await server()
    const res = await app.inject({ method: 'POST', url: '/skills/install', payload: { name: 'a-skill' } })
    expect(res.statusCode).toBe(400)
    expect(res.json().error).toMatch(/markdown is required/)
    await app.close()
  })

  it('reports a failed install as 200 + ok:false, so the caller can read the output', async () => {
    // A 5xx would hide the script's output behind a transport error; the
    // install outcome is an ANSWER about the container, not a server fault.
    const app = await server()
    const res = await app.inject({
      method: 'POST',
      url: '/skills/install',
      // A name that will not resolve under the real SKILLS_DIR either way: the
      // write fails because /workspace does not exist in the test process, and
      // that failure is still reported as a result, not thrown.
      payload: { name: 'a-skill', markdown: '# doc', install_sh: 'exit 1' },
    })
    expect(res.statusCode).toBe(200)
    expect(res.json().ok).toBe(false)
    await app.close()
  })
})
