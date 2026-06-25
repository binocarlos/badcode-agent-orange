import { test, expect } from '@playwright/test'
import { loadMockScript, resetMock } from '../helpers/mock-client.js'
import { createBashScript } from '../helpers/mock-scripts.js'
import { MockScriptBuilder } from '../helpers/mock-scripts.js'

const SESSION_CREATE_TIMEOUT = 60_000
const RENDER_TIMEOUT = 30_000

test.describe('Multi-Turn Tool Calls', () => {
  test.describe.configure({ mode: 'serial' })

  test.beforeEach(async () => {
    await resetMock()
  })

  test('bash tool call renders with command output', async ({ page }) => {
    await loadMockScript(createBashScript('echo "hello world"', 'The command output was: hello world'))

    await page.goto('/')
    const textarea = page.locator('textarea')
    await textarea.fill('Run echo hello world')
    await page.locator('button[aria-label="Send"]').click()

    await expect(page.locator('[data-role="assistant"]').first()).toBeVisible({
      timeout: SESSION_CREATE_TIMEOUT,
    })

    await expect(page.getByText('Run Command').or(page.getByText('Bash'))).toBeVisible({
      timeout: RENDER_TIMEOUT,
    })
  })

  test('three-turn conversation with thinking', async ({ page }) => {
    const script = new MockScriptBuilder('thinking-multi', 'Thinking + multi-turn')
      .addThinking('Let me analyze this request carefully...')
      .addText('I need to check something first.')
      .addBashTool('ls /workspace')
      .nextTurn()
      .addText('Here are the workspace contents. Everything looks good.')
      .build()

    await loadMockScript(script)

    await page.goto('/')
    const textarea = page.locator('textarea')
    await textarea.fill('What files are in the workspace?')
    await page.locator('button[aria-label="Send"]').click()

    await expect(page.locator('[data-role="assistant"]').first()).toBeVisible({
      timeout: SESSION_CREATE_TIMEOUT,
    })

    await expect(page.getByText('Everything looks good')).toBeVisible({ timeout: RENDER_TIMEOUT })
  })
})
