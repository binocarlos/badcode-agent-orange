import { mkdirSync } from 'node:fs'
// Populated-state walkthrough: every view, light + dark, plus interactions
// (lineage tab, conventions toggle, propagation trace, event detail).
import { chromium } from 'playwright'
const dir = process.env.SHOT_DIR ?? '/tmp/ao-ux-shots'

const auth = JSON.stringify({
  email: 'kai@badcode.dev',
  projects: [{ id: 'badcode', token: 'eyJhbGciOiAiSFMyNTYiLCAidHlwIjogIkpXVCJ9.eyJleHAiOiA5OTk5OTk5OTk5LCAiY3VzdG9tZXIiOiAiYmFkY29kZSIsICJlbWFpbCI6ICJrYWlAYmFkY29kZS5kZXYifQ.stubsig' }],
  selectedProject: 'badcode',
})

mkdirSync(dir, { recursive: true })
const shot = (page, name) => page.screenshot({ path: `${dir}/pop-${name}.png` })
const go = async (page, view) => {
  await page.locator(`[data-testid="nav-${view}"]`).click()
  await page.waitForTimeout(700)
}

const browser = await chromium.launch()
for (const scheme of ['light', 'dark']) {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 }, colorScheme: scheme })
  await page.addInitScript((a) => localStorage.setItem('agent-orange-auth', a), auth)
  await page.goto('http://localhost:5181/')
  await page.waitForTimeout(1800)
  await shot(page, `desk-${scheme}`)

  await go(page, 'chart')
  await shot(page, `chart-${scheme}`)
  try {
    await page.getByLabel(/show conventions/i).click({ timeout: 3000 })
    await page.waitForTimeout(500)
    await shot(page, `chart-conventions-${scheme}`)
    await page.getByLabel(/show conventions/i).click()
  } catch (e) { console.log(`conventions toggle (${scheme}):`, e.message.split('\n')[0]) }

  if (scheme === 'light') {
    // propagation trace: click the email.received entry pip if clickable, else paste
    try {
      const pip = page.locator('text=email.received').first()
      await pip.click({ timeout: 3000 })
      await page.waitForTimeout(500)
      await shot(page, 'chart-trace-light')
    } catch (e) { console.log('trace click:', e.message.split('\n')[0]) }

    await go(page, 'workers')
    await page.locator('text=email-answerer').first().click()
    await page.waitForTimeout(600)
    await shot(page, 'worker-config-light')
    try {
      await page.getByRole('tab', { name: /lineage/i }).click({ timeout: 3000 })
      await page.waitForTimeout(700)
      await shot(page, 'worker-lineage-light')
    } catch (e) { console.log('lineage tab:', e.message.split('\n')[0]) }

    await go(page, 'events')
    await page.waitForTimeout(500)
    await shot(page, 'events-light')
    try {
      await page.locator('text=email.received').first().click({ timeout: 3000 })
      await page.waitForTimeout(500)
      await shot(page, 'event-detail-light')
    } catch (e) { console.log('event detail:', e.message.split('\n')[0]) }
    try {
      await page.getByRole('tab', { name: /jobs/i }).click({ timeout: 3000 })
      await page.waitForTimeout(500)
      await shot(page, 'jobs-light')
    } catch (e) { console.log('jobs tab:', e.message.split('\n')[0]) }
    try {
      await page.getByRole('tab', { name: /changelog/i }).click({ timeout: 3000 })
      await page.waitForTimeout(700)
      await shot(page, 'changelog-light')
    } catch (e) { console.log('changelog tab:', e.message.split('\n')[0]) }

    await go(page, 'automation')
    await shot(page, 'automation-light')

    await go(page, 'memory')
    await page.waitForTimeout(600)
    await shot(page, 'memory-light')
  }
  await page.close()
}
await browser.close()
console.log('done')
