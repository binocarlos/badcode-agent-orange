// Serve examples/web/dist + stub the agentd API with empty-but-valid JSON,
// so the logged-in ProjectWorkspace renders its real empty states.
import { createServer } from 'node:http'
import { workers, subscriptions, schedules, events, deliveries, configEvents, attention, memories, sessions } from './fixtures.mjs'
import { burstDeliveries, burstEvents, resetBurst } from './scene-burst.mjs'
import { readFileSync, existsSync } from 'node:fs'
import { extname, join } from 'node:path'

const DIST = new URL('../../examples/web/dist', import.meta.url).pathname
const TYPES = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.woff2': 'font/woff2', '.svg': 'image/svg+xml', '.png': 'image/png' }

const api = {
  '/auth/config': { modes: ['password'], google_client_id: '' },
  '/agent/workers': workers,
  '/agent/events': events,
  '/agent/deliveries': deliveries,
  '/agent/subscriptions': subscriptions,
  '/agent/schedules': schedules,
  '/agent/config-events': configEvents,
  '/agent/attention-requests': attention,
  '/agent/sessions': sessions,
  '/agent/topologies': { topologies: [] },
  '/agent/images': { images: [{ name: 'toolbox', version: 4, labels: { kind: 'curated' }, created_at: 1785211400 }] },
  '/agent/skills': { skills: [] },
  '/agent/memories': memories,
  '/agent/project-settings': {},
}

// SCENE=burst turns the still life into an arriving one: deliveries and events
// GROW across successive fetches, which is the only way a browser can be shown
// motion that is caused by arrival rather than by render (doc 21 §4.1 rule 1).
const BURST = process.env.SCENE === 'burst'
const dynamic = BURST
  ? { '/agent/deliveries': () => burstDeliveries(), '/agent/events': () => burstEvents() }
  : {}

createServer((req, res) => {
  const path = req.url.split('?')[0]
  // A reset hook so a capture run can replay the sequence from zero.
  if (path === '/__reset') {
    if (BURST) resetBurst()
    res.writeHead(200, { 'content-type': 'application/json' })
    res.end('{"reset":true}')
    return
  }
  if (dynamic[path]) {
    res.writeHead(200, { 'content-type': 'application/json' })
    res.end(JSON.stringify(dynamic[path]()))
    return
  }
  if (api[path]) {
    res.writeHead(200, { 'content-type': 'application/json' })
    res.end(JSON.stringify(api[path]))
    return
  }
  let file = join(DIST, path === '/' ? 'index.html' : path)
  if (!existsSync(file)) file = join(DIST, 'index.html') // SPA fallback
  res.writeHead(200, { 'content-type': TYPES[extname(file)] ?? 'application/octet-stream' })
  res.end(readFileSync(file))
}).listen(5181, () => console.log('stub on :5181'))
