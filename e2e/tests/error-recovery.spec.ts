import { test, expect } from '@playwright/test'
import { loadMockScript, resetMock } from '../helpers/mock-client.js'
import { createErrorRecoveryScript } from '../helpers/mock-scripts.js'

const SESSION_CREATE_TIMEOUT = 60_000
const RENDER_TIMEOUT = 30_000

test.describe('Error Recovery', () => {
  test.describe.configure({ mode: 'serial' })

  test.beforeEach(async () => {
    await resetMock()
  })

  test('agent retries after tool error', async ({ page }) => {
    await loadMockScript(
      createErrorRecoveryScript('cat /nonexistent', 'File not found', 'I found an alternative approach that worked.'),
    )

    await page.goto('/')
    const textarea = page.locator('textarea')
    await textarea.fill('Read the config file')
    await page.locator('button[aria-label="Send"]').click()

    await expect(page.locator('[data-role="assistant"]').first()).toBeVisible({
      timeout: SESSION_CREATE_TIMEOUT,
    })

    await expect(page.getByText('alternative approach')).toBeVisible({ timeout: RENDER_TIMEOUT })
  })

  test('graceful display when mock has no script', async ({ page }) => {
    await page.goto('/')
    const textarea = page.locator('textarea')
    await textarea.fill('Hello with no mock')
    await page.locator('button[aria-label="Send"]').click()

    await page.waitForTimeout(5000)
    await expect(page.locator('textarea')).toBeVisible()
  })
})
