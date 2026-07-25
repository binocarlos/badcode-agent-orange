import { expect, type Page } from '@playwright/test'
import { TEST_EMAIL, TEST_PASSWORD, uniqueProject } from './api'

// Browser fixtures for the example app (examples/web/src/App.tsx).
//
// The app is a state machine, not a router: loading → login → project picker →
// workspace, and inside the workspace a three-way view switch (chat / workers /
// settings). These helpers drive that machine so a spec can say "open the
// workers view of a fresh project" in one line.
//
// Selectors are the app's own `data-testid`s where it has them, and accessible
// roles/labels for the library components, which ship none (see e2e/README.md).

/** View switch ids in App.tsx's ViewNav. */
export type View = 'chat' | 'workers' | 'settings'

/**
 * Logs in with the stack-e2e password account, clearing any auth state a
 * previous run left in localStorage — without this the login form never
 * appears on a re-run against a long-lived stack.
 */
export async function loginUI(page: Page): Promise<void> {
  await page.goto('/')
  await page.evaluate(() => localStorage.clear())
  await page.reload()
  await expect(page.getByTestId('login-email')).toBeVisible({ timeout: 30_000 })
  await page.getByTestId('login-email').fill(TEST_EMAIL)
  await page.getByTestId('login-password').fill(TEST_PASSWORD)
  await page.getByTestId('login-submit').click()
  await expect(page.getByTestId('project-picker')).toBeVisible({ timeout: 30_000 })
}

/**
 * Logs in and creates a brand-new project through the picker's wildcard grant,
 * landing in the workspace. Returns the project id so a spec can assert against
 * the API and the config log for the same project.
 */
export async function openFreshProject(page: Page, prefix = 'e2e-ui'): Promise<string> {
  const project = uniqueProject(prefix)
  await loginUI(page)
  await page.getByTestId('new-project-input').fill(project)
  await page.getByTestId('new-project-create').click()
  await expect(page.getByTestId('session-sidebar')).toBeVisible({ timeout: 30_000 })
  return project
}

/** Switches the workspace view and waits for the button to read as selected. */
export async function gotoView(page: Page, view: View): Promise<void> {
  await page.getByTestId(`nav-${view}`).click()
}

/** The project the switcher currently shows — proof of which project is active. */
export async function selectedProject(page: Page): Promise<string> {
  return page.getByTestId('project-switcher').inputValue()
}

/**
 * Sends a prompt and waits until the assistant's reply has stopped growing.
 *
 * The settling matters: a turn is only fully persisted once it ends, and a
 * reload taken mid-stream leaves the chat blank until the stream resolves (see
 * e2e/README.md). Any test that reloads or navigates after talking must wait
 * here first, or it is racing the model rather than testing the feature.
 */
export async function sendAndSettle(page: Page, prompt: string, timeoutMs = 120_000): Promise<string> {
  const textarea = page.locator('textarea').first()
  await expect(textarea).toBeEnabled({ timeout: timeoutMs })
  await textarea.fill(prompt)
  await page.locator('button[aria-label="Send"]').click()

  const reply = page.locator('[data-role="assistant"]').last()
  await expect(reply).toBeVisible({ timeout: timeoutMs })

  let last = ''
  let stableSince = Date.now()
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    const text = (await reply.textContent()) ?? ''
    if (text !== last) {
      last = text
      stableSince = Date.now()
    } else if (text.length > 0 && Date.now() - stableSince > 3_000) {
      return last
    }
    await page.waitForTimeout(100)
  }
  throw new Error(`assistant reply never settled within ${timeoutMs}ms; last text: ${last.slice(0, 200)}`)
}
