import { test, expect } from '@playwright/test'
import {
  cleanupOpenedProjects,
  composer,
  gotoView,
  newSessionInChat,
  openFreshProject,
  selectedProject,
  sendAndSettle,
} from '../helpers/ui'
import { projectClient } from '../helpers/api'
import { configActions, waitForConfigEvents } from '../helpers/configlog'

// Browser e2e for the product UI mounted in examples/web/src/App.tsx:
// the project-settings page (B3), the workers page (C3) and the session
// permalink (F3), driven the way a human drives them.
//
// The API-level spec next door already proves the routes behave. What can only
// be proved here is that the UI *reaches* them correctly — and one thing in
// particular: a worker toggle must send the whole row back, so the config log
// records `worker_disable` rather than a generic `worker_update`. A UI that
// PUT only the changed field would still look right on screen and would
// quietly ruin the changelog.

test.describe('product UI', () => {
  test.describe.configure({ mode: 'serial' })

  // Sessions started through the UI belong to no client, and each holds a
  // running container until deleted (see ProjectClient.cleanup).
  test.afterEach(async ({ request }) => {
    await cleanupOpenedProjects(request)
  })

  test('project settings: edit, save, and survive a reload', async ({ page, request }) => {
    const project = await openFreshProject(page, 'e2e-ui-set')
    const api = await projectClient(request, project)
    await gotoView(page, 'settings')

    const prompt = page.getByLabel('Project system prompt')
    await expect(prompt).toBeVisible({ timeout: 15_000 })
    await prompt.fill('Answer customer email. Be brief.')
    await page.getByLabel('Base image').fill('acme/base:v1')
    // Every human edit carries a required one-line reason (decision K2): the
    // Save button stays disabled until "Why?" is non-empty. A test that skips
    // it does not exercise a stricter product — it never saves at all.
    await page.getByLabel('Why?').fill('e2e: pinning the prompt and the base image')

    await page.getByRole('button', { name: 'Save settings' }).click()
    // The page reports its own saved state — no unsaved changes left.
    await expect(page.getByText('No unsaved changes')).toBeVisible({ timeout: 15_000 })

    // Persistence is the claim, so prove it past a full reload rather than
    // against the component's own state.
    await page.reload()
    await expect(page.getByTestId('session-sidebar')).toBeVisible({ timeout: 30_000 })
    await gotoView(page, 'settings')
    await expect(page.getByLabel('Project system prompt')).toHaveValue('Answer customer email. Be brief.', {
      timeout: 15_000,
    })
    await expect(page.getByLabel('Base image')).toHaveValue('acme/base:v1')

    // One save is one config-log record (§15.3).
    expect(await configActions(api)).toEqual(['project_settings_put'])
  })

  test('worker: create, disable, edit — and the log says disable, not update', async ({ page, request }) => {
    const project = await openFreshProject(page, 'e2e-ui-wk')
    const api = await projectClient(request, project)
    await gotoView(page, 'workers')

    // ── create ──────────────────────────────────────────────────────────────
    await page.getByRole('button', { name: 'New worker' }).click()
    await page.getByLabel('Name').fill('email-answerer')
    await page.getByLabel('Description').fill('answers inbound email')
    await page.getByLabel('System prompt').fill('You answer customer email.')
    // K2 again: WorkerEditor's own Save is disabled until "Why?" is filled, and
    // the field is re-seeded empty every time the editor changes identity — so
    // each of the three saves below needs its own reason.
    await page.getByLabel('Why?').fill('e2e: hiring the email answerer')
    await page.getByRole('button', { name: 'Create worker' }).click()

    const row = page.getByRole('button', { name: /email-answerer/ })
    await expect(row).toBeVisible({ timeout: 15_000 })

    // ── disable: flip the switch, save the row ──────────────────────────────
    await page.getByLabel('Enabled').uncheck()
    await page.getByLabel('Why?').fill('e2e: standing it down for now')
    await page.getByRole('button', { name: 'Save worker' }).click()
    // The list marks it disabled, which is the human-visible half of the claim.
    await expect(row.getByText('disabled')).toBeVisible({ timeout: 15_000 })

    // ── edit the prompt ─────────────────────────────────────────────────────
    await page.getByLabel('System prompt').fill('You answer customer email. Be brief.')
    await page.getByLabel('Why?').fill('e2e: shorter answers')
    await page.getByRole('button', { name: 'Save worker' }).click()
    await expect(page.getByText('No unsaved changes')).toBeVisible({ timeout: 15_000 })

    // ── the log is the other half, and the reason this test exists ──────────
    const actions = (await waitForConfigEvents(api, 3)).map((e) => e.action).reverse()
    expect(
      actions,
      'the UI must send the whole worker row when toggling `enabled`; a partial ' +
        'PUT logs worker_update and the changelog stops saying what happened',
    ).toEqual(['worker_create', 'worker_disable', 'worker_update'])
  })

  test('a session permalink opens that session directly', async ({ page }) => {
    const project = await openFreshProject(page, 'e2e-ui-link')

    // Start a session and give it something to say, so "the right session" is
    // an assertion about content rather than about a URL echoing itself.
    // Settle before navigating: a turn still streaming has not been persisted,
    // and this test is about the link, not about the model's speed.
    await newSessionInChat(page)
    const replyText = await sendAndSettle(page, 'tell me a joke')
    expect(replyText).toContain('mock model proxy')

    // state → URL: the open session is already permalinked (F3).
    await expect(page).toHaveURL(new RegExp(`/p/${project}/s/[0-9a-f-]+$`), { timeout: 15_000 })
    const permalink = page.url()

    // URL → state: leave, come back by link alone, land on the same transcript.
    await page.goto('/')
    await expect(page.getByTestId('session-sidebar')).toBeVisible({ timeout: 30_000 })
    await page.goto(permalink)
    const replayed = page.locator('[data-role="assistant"]').last()
    await expect(replayed).toBeVisible({ timeout: 30_000 })
    await expect(replayed).toContainText('mock model proxy', { timeout: 30_000 })
  })

  test('a permalink naming another project selects that project', async ({ page }) => {
    // Start somewhere else entirely: a fresh project of our own.
    const mine = await openFreshProject(page, 'e2e-ui-proj')
    expect(await selectedProject(page)).toBe(mine)

    // A link into a project this account can reach switches to it, rather than
    // leaving the reader in the wrong project looking at a missing session.
    await page.goto('/p/apples-oranges/s/00000000-0000-0000-0000-000000000000')
    await expect(page.getByTestId('session-sidebar')).toBeVisible({ timeout: 30_000 })
    await expect
      .poll(() => selectedProject(page), { timeout: 15_000 })
      .toBe('apples-oranges')
  })
})

// Regression guard for a real defect this suite found and that commit 8faaa95
// fixed. It was red from 2026-07-25 until that fix landed.
//
// Reloading the page mid-answer used to lose the whole turn, the human's own
// message included: `persist()` handed the store the caller's *already
// cancelled* context, so a real sink rejected the write instantly and every
// collected event was discarded. Every existing unit test used a mock sink that
// ignores context, which is why nothing below this level caught it.
//
// (An earlier version of this comment also called the session's `running`
// status a symptom. It is not: `running` is the correct steady state for a
// resumable session — the archive loop destroys the container and leaves the
// row alone so `ensureRunning` can restore it.)
//
// It asserts the SERVER, not the DOM, on purpose: the UI can only replay what
// was written, so this is a question about persistence, not rendering. Keep it
// that way — a DOM assertion here would pass on a UI that renders an optimistic
// echo of a message the server never stored.
test.describe('product UI — interruption', () => {
  test.afterEach(async ({ request }) => {
    await cleanupOpenedProjects(request)
  })

  test('a turn interrupted by a reload is still persisted', async ({ page, request }) => {
    const project = await openFreshProject(page, 'e2e-ui-mid')
    // Create the session AND show it: "New session" leaves the shell on the
    // Desk, where the composer is not mounted at all.
    await newSessionInChat(page)

    const textarea = composer(page)
    await expect(textarea).toBeEnabled({ timeout: 120_000 })
    await textarea.fill('tell me a joke')
    await page.locator('button[aria-label="Send"]').click()

    // Wait for the answer to start, so the turn is genuinely underway…
    await expect(page.locator('[data-role="assistant"]').last()).toBeVisible({ timeout: 120_000 })
    const sessionId = page.url().split('/s/')[1]!
    // …then do what a human does: reload.
    await page.reload()
    await expect(page.getByTestId('session-sidebar')).toBeVisible({ timeout: 30_000 })

    const client = await projectClient(request, project)
    const stored = await client.listMessages(sessionId)
    expect(
      stored.length,
      'a turn interrupted by a reload must still leave the human message in the transcript',
    ).toBeGreaterThan(0)
    expect(stored.some((m) => m.role === 'user' && m.content.includes('tell me a joke'))).toBe(true)
  })
})
