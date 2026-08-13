// Headless check of a Google Identity Services client ID's ORIGIN allowlist.
//
// The stack signs in with GIS's ID-token flow (google.accounts.id.initialize +
// renderButton), which uses "Authorized JavaScript origins" and NO redirect
// URIs. GIS refuses an unlisted origin with a very specific console error, so
// serving a page on the origin and reading the console is a real test of the
// OAuth client's configuration — something gcloud cannot inspect.
//
// IMPORTANT — localhost is exempt. Google does not enforce the JavaScript-origin
// allowlist for loopback origins, so http://localhost:<anyport> renders the
// button whether or not it is listed. Verified 2026-08-13: localhost:8080,
// :8081 and :9977 all passed against this project's client ID, while
// http://lvh.me:8080 (a public name resolving to 127.0.0.1, and NOT listed) was
// refused with "[GSI_LOGGER]: The given origin is not allowed for the given
// client ID". So a green on localhost proves the client ID is real and nothing
// more; use a non-loopback origin to actually test the allowlist.
//
// usage: node check-google-origin.mjs <clientId> <origin> [<origin>...]
//   cd e2e && node tools/check-google-origin.mjs "$GOOGLE_CLIENT_ID" https://your.domain
import { chromium } from '@playwright/test'
import http from 'node:http'

const [clientId, ...origins] = process.argv.slice(2)
if (!clientId || origins.length === 0) {
  console.error('usage: node check.mjs <clientId> <origin> [<origin>...]')
  process.exit(2)
}

const page_html = (cid) => `<!doctype html><html><body><div id="btn"></div>
<script src="https://accounts.google.com/gsi/client" async defer></script>
<script>
window.__state = 'loading';
const t = setInterval(() => {
  if (window.google && window.google.accounts && window.google.accounts.id) {
    clearInterval(t);
    try {
      google.accounts.id.initialize({ client_id: ${JSON.stringify(cid)}, callback: () => {} });
      google.accounts.id.renderButton(document.getElementById('btn'), { theme: 'outline', size: 'large' });
      window.__state = 'initialized';
    } catch (e) { window.__state = 'threw: ' + e.message; }
  }
}, 100);
</script></body></html>`

const serve = (port, html) =>
  new Promise((resolve) => {
    const s = http.createServer((_req, res) => {
      res.writeHead(200, { 'content-type': 'text/html' })
      res.end(html)
    })
    s.listen(port, '127.0.0.1', () => resolve(s))
  })

const browser = await chromium.launch()
const results = []

for (const origin of origins) {
  const port = Number(new URL(origin).port || 80)
  const server = await serve(port, page_html(clientId))
  const ctx = await browser.newContext()
  const page = await ctx.newPage()
  const logs = []
  page.on('console', (m) => logs.push(m.text()))
  page.on('pageerror', (e) => logs.push('pageerror: ' + e.message))

  try {
    await page.goto(origin, { waitUntil: 'load', timeout: 30000 })
    // GIS needs a moment to fetch its config and validate the origin.
    await page.waitForTimeout(6000)
  } catch (e) {
    logs.push('navigation failed: ' + e.message)
  }

  const state = await page.evaluate(() => window.__state).catch(() => 'unknown')
  const buttonRendered = await page
    .evaluate(() => document.getElementById('btn')?.children.length > 0)
    .catch(() => false)

  const originRejected = logs.some(
    (l) => /origin is not allowed|not allowed for the given client|invalid_client|origin_mismatch/i.test(l),
  )
  const unknownClient = logs.some((l) => /deleted_client|invalid client|unregistered/i.test(l))

  results.push({ origin, state, buttonRendered, originRejected, unknownClient, logs })

  await ctx.close()
  await new Promise((r) => server.close(r))
}

await browser.close()

const isLoopback = (o) => /^https?:\/\/(localhost|127\.0\.0\.1|\[::1\])(:|$)/i.test(o)

let bad = 0
for (const r of results) {
  const verdict = r.unknownClient
    ? 'CLIENT ID NOT RECOGNISED'
    : r.originRejected
      ? 'ORIGIN NOT AUTHORISED'
      : r.buttonRendered
        ? isLoopback(r.origin)
          ? 'OK for local dev — but loopback is EXEMPT from the allowlist, so this ' +
            'does not prove the origin is listed (see the note at the top)'
          : 'OK — origin authorised, button rendered'
        : 'INCONCLUSIVE (no button, no explicit rejection)'
  if (!/^OK/.test(verdict)) bad++
  console.log(`\n── ${r.origin}\n   ${verdict}`)
  console.log(`   gis state: ${r.state}, button rendered: ${r.buttonRendered}`)
  const notable = r.logs.filter((l) => /gsi|origin|client|error|401|403/i.test(l))
  for (const l of notable.slice(0, 6)) console.log(`   | ${l.slice(0, 200)}`)
}
process.exit(bad > 0 ? 1 : 0)
