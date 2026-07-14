import { test, expect, Page } from '@playwright/test'

// Full-stack e2e: runs against the docker-compose stack (web on :8080) stood up
// by run-stack-e2e.sh with the docker-compose.stack-e2e.yml overlay:
//   - password login enabled (AGENTKIT_TEST_LOGIN=test@example.com:orange-e2e)
//   - project map {"test@example.com": ["apples-oranges", "pears-plums"]}
//   - mock model unless CLAUDE_CODE_OAUTH_TOKEN was exported in the host env
//
// This is the goal signal for the login/projects/sessions/subscription work:
// login → pick project → new session → "tell me a joke" → streamed reply →
// replay after reload → project namespacing.

const TEST_EMAIL = 'test@example.com'
const TEST_PASSWORD = 'orange-e2e'
const PROJECT_A = 'apples-oranges'
const PROJECT_B = 'pears-plums'
// Minted at runtime via the wildcard grant. Run-scoped so repeated runs
// against one long-lived stack can never collide.
const PROJECT_NEW = `grapes-kiwis-${Date.now().toString(36)}`
const MOCK_SENTINEL = 'mock model proxy'
// Set by run-stack-e2e.sh: mock | api-key | subscription.
const MODE = process.env.STACK_E2E_MODE || 'mock'
const REAL_MODEL = MODE !== 'mock'

const SESSION_CREATE_TIMEOUT = 120_000
const REPLY_TIMEOUT = 120_000

async function login(page: Page) {
  await page.goto('/')
  // A previous run against this long-lived stack may have left auth state
  // behind — without this the login form never appears on re-runs.
  await page.evaluate(() => localStorage.clear())
  await page.reload()
  await expect(page.getByTestId('login-email')).toBeVisible({ timeout: 30_000 })
  await page.getByTestId('login-email').fill(TEST_EMAIL)
  await page.getByTestId('login-password').fill(TEST_PASSWORD)
  await page.getByTestId('login-submit').click()
}

// Installs a MutationObserver that records every distinct text length the last
// assistant message passes through. Unlike polling, this catches every DOM
// paint — a fast model can stream a whole reply between two poll ticks.
async function armStreamObserver(page: Page): Promise<void> {
  await page.evaluate(() => {
    const w = window as unknown as { __streamLens: number[] }
    w.__streamLens = []
    const observer = new MutationObserver(() => {
      const els = document.querySelectorAll('[data-role="assistant"]')
      const el = els[els.length - 1]
      const len = el ? (el.textContent ?? '').length : 0
      if (len > 0 && w.__streamLens[w.__streamLens.length - 1] !== len) w.__streamLens.push(len)
    })
    observer.observe(document.body, { subtree: true, childList: true, characterData: true })
  })
}

// Waits until the last assistant message is non-empty and stable, then returns
// its text plus the distinct lengths the observer saw. >= 2 lengths proves the
// reply rendered incrementally (streaming), not as one final paint.
async function watchAssistantStreaming(page: Page): Promise<{ lengths: number[]; text: string }> {
  const message = page.locator('[data-role="assistant"]').last()
  await expect(message).toBeVisible({ timeout: REPLY_TIMEOUT })
  let last = ''
  let stableSince = Date.now()
  const deadline = Date.now() + REPLY_TIMEOUT
  while (Date.now() < deadline) {
    const text = (await message.textContent()) ?? ''
    if (text !== last) {
      last = text
      stableSince = Date.now()
    } else if (text.length > 0 && Date.now() - stableSince > 3_000) {
      break
    }
    await page.waitForTimeout(50)
  }
  const lengths = await page.evaluate(() => (window as unknown as { __streamLens: number[] }).__streamLens ?? [])
  return { lengths, text: last }
}

test.describe('standalone stack', () => {
  test.describe.configure({ mode: 'serial' })

  // Sessions created through the UI, deleted in teardown so repeated runs
  // against one long-lived stack (run-stack-e2e.sh up + test loops) stay clean.
  const createdSessions: string[] = []

  test.afterEach(async ({ request }) => {
    if (createdSessions.length === 0) return
    const loginResp = await request.post('/auth/password', {
      data: { email: TEST_EMAIL, password: TEST_PASSWORD },
    })
    if (!loginResp.ok()) return
    const { projects } = (await loginResp.json()) as { projects: Array<{ id: string; token: string }> }
    const token = projects.find((p) => p.id === PROJECT_A)?.token
    if (!token) return
    for (const sid of createdSessions.splice(0)) {
      await request
        .delete(`/agent/session/${sid}`, { headers: { Authorization: `Bearer ${token}` } })
        .catch(() => {})
    }
  })

  test('login → project → new session → joke streams → replay → namespacing', async ({ page }) => {
    // Record every session the UI creates, for teardown.
    page.on('response', (resp) => {
      if (resp.request().method() === 'POST' && resp.url().endsWith('/agent/session') && resp.ok()) {
        resp
          .json()
          .then((j: { id?: string }) => {
            if (j?.id) createdSessions.push(j.id)
          })
          .catch(() => {})
      }
    })

    // ── Login with the fixed test credentials ──────────────────────────────
    await login(page)

    // ── Project picker shows both mapped projects ──────────────────────────
    await expect(page.getByTestId('project-picker')).toBeVisible({ timeout: 15_000 })
    await expect(page.getByTestId(`project-option-${PROJECT_A}`)).toBeVisible()
    await expect(page.getByTestId(`project-option-${PROJECT_B}`)).toBeVisible()

    // ── Wildcard grant: create a brand-new project (it auto-selects, empty) ─
    await page.getByTestId('new-project-input').fill(PROJECT_NEW)
    await page.getByTestId('new-project-create').click()
    await expect(page.getByTestId('session-sidebar')).toBeVisible({ timeout: 15_000 })
    await expect(page.getByTestId('session-row')).toHaveCount(0)

    // ── Switch to project A for the main flow ──────────────────────────────
    await page.getByTestId('project-switcher').selectOption(PROJECT_A)

    // ── Sidebar renders; start a new session ───────────────────────────────
    await expect(page.getByTestId('session-sidebar')).toBeVisible({ timeout: 15_000 })
    await page.getByTestId('new-session').click()

    // ── Send the prompt ─────────────────────────────────────────────────────
    // Real models get a longer ask: a short joke can stream end-to-end in a few
    // hundred ms, too fast to observe more than one paint.
    const prompt = REAL_MODEL
      ? 'tell me a long, detailed joke with a slow buildup — at least 150 words'
      : 'tell me a joke'
    const textarea = page.locator('textarea')
    await expect(textarea).toBeVisible({ timeout: SESSION_CREATE_TIMEOUT })
    await expect(textarea).toBeEnabled({ timeout: SESSION_CREATE_TIMEOUT })
    await armStreamObserver(page)
    await textarea.fill(prompt)
    await page.locator('button[aria-label="Send"]').click()

    // ── Streamed reply ──────────────────────────────────────────────────────
    const { lengths, text } = await watchAssistantStreaming(page)
    expect(text.length).toBeGreaterThan(0)
    expect(lengths.length).toBeGreaterThanOrEqual(2) // incremental rendering observed
    if (REAL_MODEL) {
      expect(text).not.toContain(MOCK_SENTINEL)
    } else {
      expect(text).toContain(MOCK_SENTINEL)
    }

    // ── Session is listed for this project ──────────────────────────────────
    await expect(page.getByTestId('session-row').first()).toBeVisible({ timeout: 15_000 })

    // ── Reload: still logged in, session listed, transcript replays ────────
    await page.reload()
    await expect(page.getByTestId('session-sidebar')).toBeVisible({ timeout: 30_000 })
    const row = page.getByTestId('session-row').first()
    await expect(row).toBeVisible({ timeout: 15_000 })
    await row.click()
    const replayed = page.locator('[data-role="assistant"]').last()
    await expect(replayed).toBeVisible({ timeout: 30_000 })
    expect(((await replayed.textContent()) ?? '').length).toBeGreaterThan(0)

    // ── Switch to project B: empty session list (namespacing) ──────────────
    await page.getByTestId('project-switcher').selectOption(PROJECT_B)
    await expect(page.getByTestId('session-sidebar')).toBeVisible({ timeout: 30_000 })
    await expect(page.getByTestId('session-row')).toHaveCount(0, { timeout: 15_000 })
  })
})
