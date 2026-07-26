import { test } from '@playwright/test'
import { newProjectClient } from '../helpers/api'

test('probe: does the schedule fire and what does the job see?', async ({ request }) => {
  test.setTimeout(300_000)
  const c = await newProjectClient(request, 'e2e-probe-mgr')
  await c.putWorker('marketing-manager', {
    description: 'm',
    system_prompt: 'You own BadCode marketing. [G1-MARKER-RECONCILE]\nThe workforce should include a tweet-author.',
  })
  await c.createSchedule({ worker: 'marketing-manager', cron: '* * * * *', input: 'Reconcile the workforce.' })
  const start = Date.now()
  let ds: any[] = []
  while (Date.now() - start < 150_000) {
    ds = await c.listDeliveries({})
    if (ds.length) break
    await new Promise((r) => setTimeout(r, 3000))
  }
  console.log('DELIVERIES=' + JSON.stringify(ds.map((d) => ({ st: d.status, sid: d.session_id.slice(0,8), sched: d.schedule_id ? 'yes' : 'no' }))))
  console.log('EVENTS=' + JSON.stringify((await c.listEvents({})).map((e) => e.type)))
  for (const d of ds) {
    if (!d.session_id) continue
    const s = await c.getSession(d.session_id)
    console.log('SESSION worker=' + s.worker + ' hasMarker=' + (s.composed_prompt || '').includes('G1-MARKER-RECONCILE'))
    const msgs = await c.listMessages(d.session_id)
    console.log('MSGS=' + JSON.stringify(msgs.map((m) => m.role + ':' + m.content.slice(0, 90))))
  }
  console.log('WORKERS=' + JSON.stringify((await c.listWorkers()).map((w) => w.name)))
  await c.cleanup()
})
