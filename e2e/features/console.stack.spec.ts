import { test, expect, type Page } from '@playwright/test'
import { ALL_VIEWS, cleanupOpenedProjects, gotoView, openFreshProject } from '../helpers/ui'
import { projectClient, type ProjectClient } from '../helpers/api'
import { configEvents, waitForConfigAction } from '../helpers/configlog'

// Browser e2e for the operator console (docs 15/16/21): the Desk, the org
// chart, and the two write gestures that only exist on the canvas.
//
// Everything here was previously proved only by unit tests and by a human
// looking at screenshots. The live pass (doc 21) drove READS; what had never
// run under a real pointer is the pair of chart write flows — wiring two
// workers together and freezing one — both of which refuse to proceed without
// a reason. That refusal is the console's whole thesis (K2), so it is the part
// most worth pinning: a regression that made the reason optional would leave
// every screen looking correct and quietly gut the changelog.
//
// Conventions borrowed from product-ui.stack.spec.ts: run-scoped projects, and
// an afterEach that deletes sessions (each holds a container and a host port).

// Worker names are deliberately NOT substrings of one another — `mentionsWorkerName`
// and several assertions match on names, and `book-keeper` matching inside
// `keeper` has bitten this codebase before (doc 16, OC3).
const WRITER = 'cx-scribe'
const CRITIC = 'cx-judge'
const THIRD = 'cx-herald'

const CRITERION = 'every blurb opens with a headline line'
const SEED = 'You write product blurbs for the fruit catalogue.'

/** The chart canvas — every plate, wire and label lives inside it. */
function canvas(page: Page) {
  return page.getByTestId('org-chart-canvas')
}

/**
 * Opens a plate's action menu through the keyboard path.
 *
 * The plate is focusable and answers Enter precisely so this is testable
 * without synthesising a drag; the pointer path opens the same menu (§K3).
 */
async function openNodeMenu(page: Page, worker: string): Promise<void> {
  const plate = page.getByTestId(`node-${worker}`)
  await expect(plate).toBeVisible({ timeout: 15_000 })
  await plate.focus()
  await plate.press('Enter')
  await expect(page.getByRole('menu', { name: `${worker} actions` })).toBeVisible({ timeout: 10_000 })
}

/** Applies actor-critic@v1 through the onboarding flow, as a human would. */
async function applyActorCriticViaUI(page: Page): Promise<void> {
  await gotoView(page, 'workers')
  await page.getByRole('button', { name: 'Start from a topology' }).first().click()
  await page.getByRole('button', { name: 'Choose actor-critic' }).click()

  await page.getByLabel(/Name for the actor/).fill(WRITER)
  await page.getByLabel(/What should the actor do/).fill(SEED)
  await page.getByLabel(/Name for the critic/).fill(CRITIC)
  await page.getByLabel(/What does good mean/).fill(CRITERION)

  await page.getByRole('button', { name: 'Preview' }).click()
  const apply = page.getByRole('button', { name: 'Apply topology' })
  await expect(apply).toBeEnabled({ timeout: 30_000 })
  await apply.click()
  // The done step is the happens-after signal that the apply committed.
  await expect(page.getByRole('button', { name: 'View workers' })).toBeVisible({ timeout: 30_000 })
}

test.describe('operator console', () => {
  test.describe.configure({ mode: 'serial' })
  test.setTimeout(300_000)

  test.afterEach(async ({ request }) => {
    await cleanupOpenedProjects(request)
  })

  // ── (a) every view renders ─────────────────────────────────────────────────
  //
  // Cheap, and it is the test that would have caught the nav squash and any
  // page that throws on an empty project. A view that crashes renders nothing
  // and the nav button stays unselected, so "is the button now contained" is
  // a real assertion about the page having mounted.
  test('all eight views render for a fresh project, and the Desk shows its first run', async ({ page }) => {
    await openFreshProject(page, 'e2e-cx-nav')

    // The Desk is the landing view (App.tsx defaults to it) and an empty
    // project must get the first-run panel, never a bare "nothing to show".
    await expect(page.getByText('This project has no workers yet')).toBeVisible({ timeout: 30_000 })
    await expect(page.getByRole('button', { name: 'Start from an org chart' })).toBeVisible()

    const pageErrors: string[] = []
    page.on('pageerror', (e) => pageErrors.push(String(e)))

    for (const view of ALL_VIEWS) {
      await gotoView(page, view)
      // Each nav button is `contained` while its view is open; Playwright sees
      // that as the MUI class, so assert on what the operator can see instead:
      // the view switched and the app did not blank out.
      await expect(page.getByTestId(`nav-${view}`)).toBeVisible()
      await expect(page.getByTestId('session-sidebar')).toBeVisible()
    }

    expect(pageErrors, 'no view may throw while rendering an empty project').toEqual([])
  })

  // ── (b) + (c) the topology flow, the chart it draws, and traffic on a wire ──
  test('applying actor-critic@v1 draws the chart, records why, and counts traffic', async ({ page, request }) => {
    const project = await openFreshProject(page, 'e2e-cx-top')
    const api: ProjectClient = await projectClient(request, project)

    await applyActorCriticViaUI(page)

    // ── the chart: two plates, two wires ────────────────────────────────────
    await gotoView(page, 'chart')
    await expect(canvas(page)).toBeVisible({ timeout: 30_000 })
    await expect(page.getByTestId(`node-${WRITER}`)).toBeVisible({ timeout: 30_000 })
    await expect(page.getByTestId(`node-${CRITIC}`)).toBeVisible()

    // Scoped to the canvas on purpose: the wire-proposal DIALOG also carries a
    // testid starting `wire-`, so an unscoped ^wire- count is a trap.
    const wires = canvas(page).locator('[data-testid^="wire-"]')
    await expect(wires).toHaveCount(2, { timeout: 30_000 })

    // ── the Desk: a seeded org must record its reasons ──────────────────────
    //
    // This is doc 21's live finding turned into a regression: a topology that
    // applied the zero ConfigWrite gave a brand-new operator five
    // "(no reason given)" rows on the one screen that exists to keep the
    // system honest. ApplyTopology now defaults the rationale.
    await gotoView(page, 'desk')
    await expect(page.getByText('CHANGES', { exact: false }).first()).toBeVisible({ timeout: 30_000 })
    await expect(page.getByText('seeded from actor-critic@v1').first()).toBeVisible({ timeout: 30_000 })
    await expect(page.getByText('(no reason given)')).toHaveCount(0)

    // The log agrees with the screen.
    const applied = await waitForConfigAction(api, 'topology_apply')
    expect(applied.rationale).toBe('seeded from actor-critic@v1')

    // ── (c) real traffic puts a count on the wire ───────────────────────────
    //
    // Posted through the API helper rather than the UI: this asserts that the
    // CHART reflects the backend, so the event must arrive by a path the chart
    // has nothing to do with.
    const event = await api.postEvent({ type: `${WRITER}.task`, text: 'Write a blurb about the new apples.' })
    const delivered = await api.waitForDeliveries(
      (rows) => rows.some((d) => d.status === 'ok'),
      { event_id: event.id, timeoutMs: 240_000 },
    )
    expect(delivered.some((d) => d.status === 'ok')).toBe(true)

    // The chart polls, so this is a poll-until-visible, not a sleep-then-read.
    await gotoView(page, 'chart')
    await expect(canvas(page).locator('[data-testid^="traffic-"]').first()).toContainText('↳ ×1', {
      timeout: 60_000,
    })
  })

  // ── (d) wiring two workers together, under a real pointer ──────────────────
  test('wiring two workers from the chart demands a why, and the changelog keeps it', async ({ page, request }) => {
    const project = await openFreshProject(page, 'e2e-cx-wire')
    const api = await projectClient(request, project)

    // Two unrelated workers, so the wire this test draws is the only one.
    await api.putWorker(WRITER, { system_prompt: SEED, description: 'writes blurbs' })
    await api.putWorker(THIRD, { system_prompt: 'You announce finished work.', description: 'announces' })

    await gotoView(page, 'chart')
    await expect(page.getByTestId(`node-${WRITER}`)).toBeVisible({ timeout: 30_000 })

    await openNodeMenu(page, WRITER)
    await page.getByRole('menuitem', { name: `Wire ${WRITER} to another worker…` }).click()

    const dialog = page.getByTestId('wire-proposal')
    await expect(dialog).toBeVisible({ timeout: 10_000 })

    // The filter names the source — the caption says so, and it is what stops
    // the unfiltered worker.finished self-edge cycle (doc 16, OC1/OC4).
    await expect(dialog.getByTestId('proposal-filter')).toContainText(WRITER)

    // The reason is mandatory: with the target chosen and the why empty, the
    // commit button must still refuse.
    await dialog.getByLabel('Wake which worker?').click()
    await page.getByRole('option', { name: THIRD }).click()
    const wireIt = dialog.getByRole('button', { name: 'Wire it up' })
    await expect(wireIt, 'a wire with no reason must not be committable (K2)').toBeDisabled()

    const why = 'the herald should announce whatever the scribe finishes'
    await dialog.getByLabel('Why are you wiring this?').fill(why)
    await expect(wireIt).toBeEnabled()
    await wireIt.click()
    await expect(dialog).toBeHidden({ timeout: 30_000 })

    // The subscription exists, filtered to the source…
    const subs = await api.listSubscriptions()
    const wired = subs.find((s) => s.worker === THIRD)
    expect(wired, `a subscription waking ${THIRD} must exist`).toBeTruthy()
    expect(wired!.event_type).toBe('worker.finished')
    expect(wired!.filter).toMatchObject({ worker: WRITER })

    // …and the reason survived into the config log, which is the point.
    const created = await waitForConfigAction(api, 'subscription_create')
    expect(created.rationale).toBe(why)

    // The chart now draws it.
    await expect(canvas(page).locator('[data-testid^="wire-"]')).toHaveCount(1, { timeout: 30_000 })
  })

  // ── (e) freezing a worker from the chart ───────────────────────────────────
  test('freezing a worker from the chart records worker_freeze with its reason', async ({ page, request }) => {
    const project = await openFreshProject(page, 'e2e-cx-froz')
    const api = await projectClient(request, project)

    await api.putWorker(CRITIC, { system_prompt: 'You score answers.', description: 'the judge' })

    await gotoView(page, 'chart')
    await openNodeMenu(page, CRITIC)
    await page.getByRole('menuitem', { name: `Freeze ${CRITIC}` }).click()

    const dialog = page.getByTestId('worker-toggle-dialog')
    await expect(dialog).toBeVisible({ timeout: 10_000 })

    // K2 again: the freeze toggle asks for a reason in the same words as the
    // wire cut, and refuses without one (doc 16, OC4).
    const confirm = dialog.getByRole('button', { name: `Freeze ${CRITIC}` })
    await expect(confirm, 'a freeze with no reason must not be committable').toBeDisabled()

    const why = 'measurement instrument for the fee experiment'
    await dialog.getByLabel('Why?').fill(why)
    await expect(confirm).toBeEnabled()
    await confirm.click()
    await expect(dialog).toBeHidden({ timeout: 30_000 })

    // The store agrees…
    await expect
      .poll(async () => (await api.getWorker(CRITIC)).frozen, { timeout: 30_000 })
      .toBe(true)

    // …the log names the act, not a generic update…
    const frozen = await waitForConfigAction(api, 'worker_freeze')
    expect(frozen.rationale).toBe(why)
    const actions = (await configEvents(api)).map((e) => e.action)
    expect(actions, 'a freeze must never be logged as a bare worker_update').toContain('worker_freeze')

    // …and the plate says so, with the lock the design reserves for it.
    await expect(
      page.getByLabel('frozen — only a human may change it').first(),
    ).toBeVisible({ timeout: 30_000 })
  })
})
