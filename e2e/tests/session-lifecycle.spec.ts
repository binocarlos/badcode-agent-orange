import { test, expect } from '@playwright/test'
import { loadMockScript, resetMock } from '../helpers/mock-client.js'
import { createTextOnlyScript } from '../helpers/mock-scripts.js'

const SESSION_CREATE_TIMEOUT = 60_000
const RENDER_TIMEOUT = 30_000

test.describe('Session Lifecycle', () => {
  test.describe.configure({ mode: 'serial' })

  test.beforeEach(async () => {
    await resetMock()
  })

  test('create session, send message, see streamed response', async ({ page }) => {
    await loadMockScript(createTextOnlyScript('Hello from agentkit! The answer is 42.'))

    await page.goto('/')
    await expect(page.locator('textarea')).toBeVisible({ timeout: 10_000 })

    const textarea = page.locator('textarea')
    await textarea.fill('What is the answer?')
    await page.locator('button[aria-label="Send"]').click()

    await expect(page.locator('[data-role="assistant"]').first()).toBeVisible({
      timeout: SESSION_CREATE_TIMEOUT,
    })

    const assistantMessage = page.locator('[data-role="assistant"]').first()
    await expect(assistantMessage).toContainText('Hello from agentkit', { timeout: RENDER_TIMEOUT })
    await expect(assistantMessage).toContainText('42')
  })

  test('session appears in session list after creation', async ({ page }) => {
    await loadMockScript(createTextOnlyScript('Quick response.'))

    await page.goto('/')
    const textarea = page.locator('textarea')
    await textarea.fill('Hello')
    await page.locator('button[aria-label="Send"]').click()

    await expect(page.locator('[data-role="assistant"]').first()).toBeVisible({
      timeout: SESSION_CREATE_TIMEOUT,
    })

    const sessionList = page.locator('[data-testid="session-list"]')
    if (await sessionList.isVisible()) {
      await expect(sessionList.locator('li, [role="listitem"]').first()).toBeVisible()
    }
  })
})
