import { test, expect } from '@playwright/test'
import { loadMockScript, resetMock } from '../helpers/mock-client.js'
import { createTextOnlyScript } from '../helpers/mock-scripts.js'

const SESSION_CREATE_TIMEOUT = 60_000
const RENDER_TIMEOUT = 30_000

test.describe('Session Isolation', () => {
  test.describe.configure({ mode: 'serial' })

  test.beforeEach(async () => {
    await resetMock()
  })

  test('two sessions receive independent responses', async ({ page }) => {
    await loadMockScript(createTextOnlyScript('Response from session one.'))

    await page.goto('/')
    const textarea = page.locator('textarea')
    await textarea.fill('First session message')
    await page.locator('button[aria-label="Send"]').click()

    await expect(page.locator('[data-role="assistant"]').first()).toBeVisible({
      timeout: SESSION_CREATE_TIMEOUT,
    })
    await expect(page.locator('[data-role="assistant"]').first()).toContainText('Response from session one', {
      timeout: RENDER_TIMEOUT,
    })

    await resetMock()
    await loadMockScript(createTextOnlyScript('Response from session two.'))

    await page.goto('/')
    const textarea2 = page.locator('textarea')
    await textarea2.fill('Second session message')
    await page.locator('button[aria-label="Send"]').click()

    await expect(page.locator('[data-role="assistant"]').first()).toBeVisible({
      timeout: SESSION_CREATE_TIMEOUT,
    })
    await expect(page.locator('[data-role="assistant"]').first()).toContainText('Response from session two', {
      timeout: RENDER_TIMEOUT,
    })
  })
})
