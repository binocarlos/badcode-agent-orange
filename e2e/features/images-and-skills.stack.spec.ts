import { test, expect } from '@playwright/test'
import { newProjectClient, sessionPermalink, type ProjectClient } from '../helpers/api'
import { configEvents } from '../helpers/configlog'
import { MCPClient, projectOnlyMCP, sessionMCP } from '../helpers/mcp'
import { psql, lit } from '../helpers/stackdb'

// §13/§14 — curate then burn.
//
// A job installs and verifies tools in its own container, then either burns the
// result into a named image version (§13) or writes down how to do it as a
// skill (§14). Both are append-only at the tool surface: there is no update and
// no delete, so a pin can never rot under a worker that depends on it.
//
// These drive agentd's core MCP server directly with a minted session token
// (see helpers/mcp.ts). That is not a shortcut around the model — it is the
// only way to test the tools in mock mode, because the mock proxy serves a
// fixed script and can never emit a tool call. What it does test is everything
// the tool itself owns: the catalogue, the container write, the config log, and
// the refusals.

/** The skills directory inside a session container (§14.2). */
const SKILLS_DIR = '/workspace/.claude/skills'

/** A session with a live container, plus an MCP client carrying its token. */
async function sessionWithMCP(client: ProjectClient): Promise<{ id: string; mcp: MCPClient }> {
  const id = await client.createSession({ job: 'curate' })
  // A session has no container until its first turn, and both image_create and
  // skill_install act on that container.
  await client.sendMessage(id, 'hello')
  return { id, mcp: sessionMCP(client.project, id) }
}

test.describe('§14 — skills', () => {
  test.describe.configure({ mode: 'serial' })
  test.setTimeout(300_000)

  let client: ProjectClient

  test.beforeEach(async ({ request }) => {
    client = await newProjectClient(request, 'e2e-skills')
  })

  // A session holds a running container until it is deleted; without this the
  // daemon fills up and later snapshots fail for reasons that have nothing to
  // do with the code under test.
  test.afterEach(async () => {
    await client.cleanup()
  })

  test('a skill is written down once, then installed into a session', async () => {
    const { id: session, mcp } = await sessionWithMCP(client)

    const created = await mcp.callOK('skill_create', {
      name: 'render-social-video',
      markdown: '# Render social video\n\nUse ffmpeg with the house preset.',
      install_sh: 'echo "installing" > /tmp/render-social-video.log',
      labels: { kind: 'media', audience: 'content' },
    })
    // Provenance is stamped by core from the caller's token, never supplied.
    expect(created).toMatchObject({
      name: 'render-social-video',
      revision: 1,
      labels: { kind: 'media', audience: 'content' },
      created_by_session: session,
    })
    expect(created.session_url).toBe(sessionPermalink(client.project, session))

    // The catalogue can be read back by name and by selector.
    expect(await mcp.callOK('skill_get', { name: 'render-social-video' })).toMatchObject({
      markdown: created.markdown,
    })
    const listed = await mcp.callOK('skill_list', { label_selector: 'kind=media' })
    expect(listed.skills.map((s: { name: string }) => s.name)).toContain('render-social-video')

    // Installing lands the document in the session's skills directory…
    const installed = await mcp.callOK('skill_install', { name: 'render-social-video' })
    expect(installed).toMatchObject({
      name: 'render-social-video',
      installed: true,
      file_written: `${SKILLS_DIR}/render-social-video/SKILL.md`,
    })
    expect(installed.bytes_written).toBeGreaterThan(0)
    // …and the install script ran, with its exit status reported rather than assumed.
    expect(installed.script).toMatchObject({ ran: true, exit_code: 0 })
    // The document is only *loaded* on the next turn — a fact the tool states
    // rather than leaving the model to discover.
    expect(String(installed.note)).toContain('NEXT turn')
  })

  // §14.2 is explicit that a failed install must be a visible failure, never a
  // silent no-op. This is the test that would catch a regression to "log it and
  // return ok", which is the failure mode that costs a whole job silently.
  test('a failing install script is a loud failure, not a silent no-op', async () => {
    const { mcp } = await sessionWithMCP(client)

    await mcp.callOK('skill_create', {
      name: 'broken-installer',
      markdown: '# Broken\n\nIts installer exits non-zero.',
      install_sh: 'echo "about to fail" >&2\nexit 3',
    })

    const out = await mcp.call('skill_install', { name: 'broken-installer' })
    // The CALL succeeded; the TOOL failed. That distinction is the contract:
    // the model must see a failure it can act on, not a transport exception.
    expect(out.isError).toBe(true)
    expect(out.text).toContain('skill "broken-installer" was NOT successfully installed')
    // The exit status, or the model cannot tell a broken script from a missing one…
    expect(out.text).toContain('install script: exit status 3')
    // …the script's own stderr, which is where the reason actually lives…
    expect(out.text).toContain('about to fail')
    // …and an instruction not to carry on regardless, which is the behaviour
    // §14.2 is really protecting against.
    expect(out.text).toContain('Do not proceed as though this capability is available')
    // Partial success is reported as partial, not rounded to either end: the
    // document did land, and saying so is what stops a retry loop that keeps
    // rewriting a file that was never the problem.
    expect(out.text).toContain(`document: written to ${SKILLS_DIR}/broken-installer/SKILL.md`)
  })

  test('installing does not touch the project: §14.2 mutates the session only', async () => {
    const { mcp } = await sessionWithMCP(client)
    await mcp.callOK('skill_create', {
      name: 'quiet-skill',
      markdown: '# Quiet\n\nNo script.',
    })
    const before = await configEvents(client.project)
    expect(before.map((e) => e.action)).toEqual(['skill_create'])

    await mcp.callOK('skill_install', { name: 'quiet-skill' })

    // The asymmetry is the design decision worth guarding: creating a skill is
    // a project fact and is logged; installing one changes only this
    // container's filesystem, so the config log must NOT grow (§15.3 rule 3).
    const after = await configEvents(client.project)
    expect(after.map((e) => e.action)).toEqual(['skill_create'])
  })

  test('skill_create is logged with the acting session as its actor', async () => {
    const { id: session, mcp } = await sessionWithMCP(client)
    await mcp.callOK('skill_create', { name: 'logged-skill', markdown: '# Logged' })

    const [record] = await configEvents(client.project)
    expect(record.action).toBe('skill_create')
    // A tool call is not a human edit: the session is on the record, which is
    // what makes the changelog navigable back to the transcript (§15.2).
    expect(record.actor_session).toBe(session)
    expect(record.payload).toMatchObject({ name: 'logged-skill' })
  })

  test('a token naming no session cannot install into one', async () => {
    const { mcp } = await sessionWithMCP(client)
    await mcp.callOK('skill_create', { name: 'needs-a-session', markdown: '# X' })

    // A project-scoped token is a real credential for the project — it just is
    // not a session, and skill_install writes into a specific container.
    const out = await projectOnlyMCP(client.project).call('skill_install', { name: 'needs-a-session' })
    expect(out.isError).toBe(true)
    expect(out.text).toContain('skill_install can only be called from inside a session')
  })

  test('skill_list caps at 200 and says so', async () => {
    const { mcp } = await sessionWithMCP(client)
    // Seeded in SQL rather than through 201 tool calls: the cap is a property of
    // the read path, and 201 round trips through DinD would add four minutes to
    // the suite to prove the same thing.
    await psql(`
      INSERT INTO agent_skills (id, name, customer, visibility, owner_email, markdown, revision, labels, created_at, updated_at)
      SELECT 'bulk-' || g || '-' || ${lit(client.project)}, 'bulk-skill-' || g, ${lit(client.project)},
             'organizational', 'e2e@example.com', '# bulk', 1, '{"kind":"bulk"}'::jsonb,
             extract(epoch from now())::bigint, extract(epoch from now())::bigint
      FROM generate_series(1, 205) g;`)

    const listed = await mcp.callOK('skill_list')
    expect(listed.skills).toHaveLength(200)
    expect(listed.truncated).toBe(true)
    expect(String(listed.note)).toContain('label_selector')

    // A selector narrow enough to fit is not truncated — the escape hatch the
    // note points at actually works.
    const narrowed = await mcp.callOK('skill_list', { label_selector: 'kind=none-match' })
    expect(narrowed.skills).toHaveLength(0)
    expect(narrowed.truncated ?? false).toBe(false)
  })
})

test.describe('§13 — images', () => {
  test.describe.configure({ mode: 'serial' })
  test.setTimeout(300_000)

  let client: ProjectClient

  test.beforeEach(async ({ request }) => {
    client = await newProjectClient(request, 'e2e-images')
  })

  test.afterEach(async () => {
    await client.cleanup()
  })

  test('burning twice allocates versions 1 then 2, newest first, with provenance', async () => {
    const { id: session, mcp } = await sessionWithMCP(client)

    const first = await mcp.callOK('image_create', {
      name: 'marketing-tools',
      labels: { why: 'ffmpeg-and-fonts' },
    })
    expect(first).toMatchObject({ name: 'marketing-tools', version: 1, created_by_session: session })
    expect(first.session_url).toBe(sessionPermalink(client.project, session))

    const second = await mcp.callOK('image_create', {
      name: 'marketing-tools',
      labels: { why: 'adds-imagemagick' },
    })
    // Versions are allocated by core, monotonic and gap-free — a worker never
    // chooses one, so two concurrent burns cannot collide on a number.
    expect(second.version).toBe(2)

    const listed = await mcp.callOK('image_list')
    const mine = listed.images.filter((i: { name: string }) => i.name === 'marketing-tools')
    expect(mine.map((i: { version: number }) => i.version)).toEqual([2, 1])
    // Every row says who made it and from where, so a mystery image is one click
    // from the transcript that produced it (§13.2).
    for (const image of mine) {
      expect(image.created_by_session).toBe(session)
      expect(image.session_url).toBe(sessionPermalink(client.project, session))
    }
    // The labels are per version: they say why *this* burn happened.
    expect(mine[0].labels).toEqual({ why: 'adds-imagemagick' })
    expect(mine[1].labels).toEqual({ why: 'ffmpeg-and-fonts' })
  })

  test('an older version survives a newer burn, unchanged', async () => {
    const { mcp } = await sessionWithMCP(client)
    const pinned = await mcp.callOK('image_create', { name: 'pinned-tools', labels: { v: 'one' } })
    await mcp.callOK('image_create', { name: 'pinned-tools', labels: { v: 'two' } })

    const listed = await mcp.callOK('image_list', { label_selector: 'v=one' })
    // A worker pinned to `pinned-tools:1` still finds exactly what it pinned —
    // the append-only guarantee is what makes a pin safe (§13.3).
    expect(listed.images).toHaveLength(1)
    expect(listed.images[0]).toMatchObject({ name: 'pinned-tools', version: 1, labels: { v: 'one' } })
    expect(listed.images[0].created_at).toBe(pinned.created_at)
  })

  test('image_create is logged with the acting session as its actor', async () => {
    const { id: session, mcp } = await sessionWithMCP(client)
    await mcp.callOK('image_create', { name: 'logged-image' })

    const [record] = await configEvents(client.project)
    expect(record.action).toBe('image_create')
    expect(record.actor_session).toBe(session)
    expect(record.payload).toMatchObject({ name: 'logged-image', version: 1 })
  })

  test('a token naming no session cannot burn an image', async () => {
    await sessionWithMCP(client) // a container exists; the token is the problem
    const out = await projectOnlyMCP(client.project).call('image_create', { name: 'no-session' })
    expect(out.isError).toBe(true)
    expect(out.text).toContain('image_create can only be called from inside a session')
    // …and nothing was recorded for the attempt.
    expect(await configEvents(client.project)).toHaveLength(0)
  })
})

test.describe('§13/§14 — the append-only surface', () => {
  test.setTimeout(300_000)

  test('the catalogue offers no way to update or delete anything', async ({ request }) => {
    const client = await newProjectClient(request, 'e2e-append')
    const { mcp } = await sessionWithMCP(client)
    // One test in this describe, so cleanup is a finally rather than an
    // afterEach — the session's container must be released either way.
    try {
      const tools = await mcp.listTools()
      // Append-only is enforced by absence, which is the strongest way to enforce
      // it: there is no verb to call, so no prompt can talk a worker into it.
      for (const forbidden of [
        'image_update',
        'image_delete',
        'image_promote',
        'skill_update',
        'skill_delete',
      ]) {
        expect(tools, `${forbidden} must not exist — §13/§14 are append-only`).not.toContain(forbidden)
      }
      // What does exist is exactly the curate-then-burn surface.
      expect(tools).toEqual(
        expect.arrayContaining(['image_create', 'image_list', 'skill_create', 'skill_list', 'skill_get', 'skill_install']),
      )

      // A second skill_create under the same name is a new revision, not an edit
      // of the old one — append, even where the name repeats (§14.1).
      await mcp.callOK('skill_create', { name: 'evolving', markdown: '# v1' })
      const second = await mcp.callOK('skill_create', { name: 'evolving', markdown: '# v2' })
      expect(second.revision).toBe(2)
      expect(second.markdown).toContain('v2')
    } finally {
      await client.cleanup()
    }
  })
})
