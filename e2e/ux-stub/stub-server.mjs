// Serve examples/web/dist + stub the agentd API with empty-but-valid JSON,
// so the logged-in ProjectWorkspace renders its real empty states.
import { createServer } from 'node:http'
import { workers, subscriptions, schedules, events, deliveries, configEvents, attention, memories, sessions } from './fixtures.mjs'
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

createServer((req, res) => {
  const path = req.url.split('?')[0]
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
