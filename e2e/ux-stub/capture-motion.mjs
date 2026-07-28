// capture-motion — the instrument for judging what a screenshot cannot.
//
// Doc 21's motion system (§5) can be unit-tested for its ARITHMETIC — which
// pulses are planned, how long they last, what reduced motion does — but not
// for whether a dot visibly leaves one plate and arrives at another. A single
// screenshot of an animation is a picture of one frame, usually an empty one.
//
// So this does three things a still pass cannot:
//   1. Records video of each surface while the burst scene delivers arrivals.
//   2. Samples a FILM STRIP — N frames at a fixed interval — so a mid-flight
//      dot is caught and its progress along the wire is visible frame to frame.
//   3. Runs the whole thing TWICE, once with prefers-reduced-motion: reduce,
//      so the static equivalents (§5's chevrons, counts, flash-only) can be
//      compared against the animated pass. A motion feature whose reduced pass
//      loses information has failed the review's own rule.
//
// Usage (from e2e/, with the stub already running under SCENE=burst):
//   SCENE=burst node ux-stub/stub-server.mjs &
//   node ux-stub/capture-motion.mjs
// Output: $SHOT_DIR/motion/{video,strip-*}.  Judge it with your eyes; nothing
// here asserts. It is a review instrument, not a test.

import { chromium } from 'playwright'
import { mkdirSync } from 'node:fs'

const OUT = (process.env.SHOT_DIR ?? '/tmp/ao-ux-shots') + '/motion'
const BASE = 'http://localhost:5181'
const FRAMES = Number(process.env.FRAMES ?? 12)
const INTERVAL = Number(process.env.INTERVAL_MS ?? 120)

const auth = JSON.stringify({
  email: 'kai@badcode.dev',
  projects: [{ id: 'badcode', token: 'eyJhbGciOiAiSFMyNTYiLCAidHlwIjogIkpXVCJ9.eyJleHAiOiA5OTk5OTk5OTk5LCAiY3VzdG9tZXIiOiAiYmFkY29kZSIsICJlbWFpbCI6ICJrYWlAYmFkY29kZS5kZXYifQ.stubsig' }],
  selectedProject: 'badcode',
})

/** A film strip: FRAMES screenshots INTERVAL apart, so motion is visible as
 *  displacement between consecutive frames. */
async function strip(page, name) {
  for (let i = 0; i < FRAMES; i++) {
    await page.screenshot({ path: `${OUT}/strip-${name}-${String(i).padStart(2, '0')}.png` })
    await page.waitForTimeout(INTERVAL)
  }
}

async function run(reduced) {
  const tag = reduced ? 'reduced' : 'motion'
  mkdirSync(`${OUT}/video-${tag}`, { recursive: true })
  const browser = await chromium.launch()
  const ctx = await browser.newContext({
    viewport: { width: 1440, height: 900 },
    colorScheme: 'light',
    reducedMotion: reduced ? 'reduce' : 'no-preference',
    recordVideo: { dir: `${OUT}/video-${tag}`, size: { width: 1440, height: 900 } },
  })
  const page = await ctx.newPage()
  await page.addInitScript((a) => localStorage.setItem('agent-orange-auth', a), auth)
  await fetch(`${BASE}/__reset`).catch(() => {})
  await page.goto(BASE)
  await page.waitForTimeout(1500)

  // Chart: the surface where deliveries travel wires.
  const chart = page.locator('[data-testid="nav-chart"]')
  if (await chart.count()) {
    await chart.click()
    await page.waitForTimeout(800)
    await strip(page, `chart-${tag}`)
  } else console.log(`nav-chart missing (${tag})`)

  // Desk: the surface where arrivals enter a feed.
  const desk = page.locator('[data-testid="nav-desk"]')
  if (await desk.count()) {
    await desk.click()
    await page.waitForTimeout(600)
    await strip(page, `desk-${tag}`)
  } else console.log(`nav-desk missing (${tag})`)

  await ctx.close() // flushes the video
  await browser.close()
  console.log(`${tag}: ${FRAMES * 2} frames + video → ${OUT}`)
}

mkdirSync(OUT, { recursive: true })
await run(false)
await run(true)
console.log('done — compare strip-*-motion-* against strip-*-reduced-*')
