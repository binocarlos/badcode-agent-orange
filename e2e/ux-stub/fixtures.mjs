// Populated fixture data for the review stub server: a believable BadCode
// email org (actor-critic + archivist + frozen scorer + a scheduled writer)
// mid-morning, with history. Times: NOW is fixed so screenshots reproduce.
const NOW = 1785254400 // 2026-07-28 ~16:00 UTC, unix seconds
const s = (secsAgo) => NOW - secsAgo
const ms = (secsAgo) => (NOW - secsAgo) * 1000

const P = 'badcode'

const PROMPT_V10 = `You are email-answerer, BadCode's inbound email desk.
Read the customer's email and draft a reply.
Always answer in the customer's language.
Keep replies under 200 words.
Sign off with your own name and the team's.`

const PROMPT_V11 = `You are email-answerer, BadCode's inbound email desk.
Always quote the ticket reference in the first line.
Read the customer's email and draft a reply.
Always answer in the customer's language.
Keep replies under 200 words.
Sign off with your own name and the team's.`

const PROMPT_V12 = `You are email-answerer, BadCode's inbound email desk.
Quote the ticket reference in the first line when one exists;
otherwise open with the customer's name.
Read the customer's email and draft a reply.
Always answer in the customer's language.
Keep replies under 200 words.
Sign off with your own name and the team's.`

export const workers = { workers: [
  { project: P, name: 'email-answerer', description: 'answers inbound customer mail', system_prompt: PROMPT_V12, mcp_config: {}, image: 'toolbox', briefing: null, max_instances: 1, enabled: true, frozen: false },
  { project: P, name: 'email-reviewer', description: "reviews answers; rewrites the answerer's prompt when it finds a systemic problem", system_prompt: 'You are email-reviewer. Read email-answerer transcripts; when a problem repeats, rewrite its prompt via worker_prompt_write with a rationale. ROUTE-TO: archivist when a decision is worth keeping.', mcp_config: {}, image: '', briefing: null, max_instances: 1, enabled: true, frozen: false },
  { project: P, name: 'archivist', description: 'keeps the rolling summary fresh', system_prompt: 'You are archivist. Read finished transcripts and append kind=rolling-summary memories for email-answerer.', mcp_config: {}, image: '', briefing: null, max_instances: 1, enabled: true, frozen: false },
  { project: P, name: 'fee-scorer', description: 'grades answers against held-out fee tables', system_prompt: 'You are fee-scorer. Compare each answer against the held-out truth you are given. Never generate the truth yourself.', mcp_config: {}, image: '', briefing: null, max_instances: 1, enabled: true, frozen: true },
  { project: P, name: 'social-writer', description: 'posts the morning and evening note', system_prompt: 'You are social-writer. Write the note the schedule asks for.', mcp_config: {}, image: 'toolbox:3', briefing: null, max_instances: 2, enabled: true, frozen: false },
  { project: P, name: 'invoice-parser', description: 'extracts totals from supplier invoices', system_prompt: 'You are invoice-parser.', mcp_config: {}, image: 'toolbox:9', briefing: null, max_instances: 1, enabled: true, frozen: false },
]}

export const subscriptions = { subscriptions: [
  { id: 'sub-1', project: P, event_type: 'email.received', filter: {}, worker: 'email-answerer', max_firings_per_hour: 0, enabled: true, created_at: s(86400 * 16), updated_at: s(86400 * 16) },
  { id: 'sub-2', project: P, event_type: 'worker.finished', filter: { worker: 'email-answerer' }, worker: 'email-reviewer', max_firings_per_hour: 0, enabled: true, created_at: s(86400 * 16), updated_at: s(86400 * 16) },
  { id: 'sub-3', project: P, event_type: 'worker.finished', filter: { worker: 'email-answerer' }, worker: 'archivist', max_firings_per_hour: 12, enabled: true, created_at: s(86400 * 16), updated_at: s(86400 * 16) },
  { id: 'sub-4', project: P, event_type: 'worker.finished', filter: { worker: 'email-reviewer' }, worker: 'fee-scorer', max_firings_per_hour: 0, enabled: true, created_at: s(86400 * 12), updated_at: s(86400 * 12) },
  { id: 'sub-5', project: P, event_type: 'invoice.received', filter: {}, worker: 'invoice-parser', max_firings_per_hour: 0, enabled: true, created_at: s(86400 * 9), updated_at: s(86400 * 9) },
]}

export const schedules = { schedules: [
  { id: 'sched-1', project: P, worker: 'social-writer', cron: '0 9 * * *', input: 'Write the morning note: one observation from yesterday worth sharing.', enabled: true, provision_failures: 0, last_provision_error: '', created_at: s(86400 * 16), updated_at: s(86400 * 16) },
  { id: 'sched-2', project: P, worker: 'social-writer', cron: '0 17 * * *', input: 'Write the evening note.', enabled: true, provision_failures: 0, last_provision_error: '', created_at: s(86400 * 16), updated_at: s(86400 * 16) },
  { id: 'sched-3', project: P, worker: 'invoice-parser', cron: '30 2 * * *', input: 'Sweep the invoices folder.', enabled: false, provision_failures: 5, last_provision_error: 'compose: worker.image toolbox:9 names no image in the §13 catalogue', created_at: s(86400 * 9), updated_at: s(3600 * 9) },
]}

const env = (over = {}) => ({ depth: 0, source: 'external', worker: '', session_id: '', interactive: false, attention_requested: false, ...over })

export const events = { events: [
  { id: 'ev-11', project: P, type: 'worker.finished', text: 'Assistant: RIDLEY-2231 — I compared the invoice to our fee table and the amounts differ by £120...', envelope: env({ depth: 1, source: 'worker', worker: 'email-answerer', session_id: 'sess-a4', attention_requested: true }), occurred_at: s(9600), created_at: s(9600), delivered: true },
  { id: 'ev-10', project: P, type: 'email.received', text: 'From: finance@ridley.co\nSubject: Invoice query RIDLEY-2231\n\nYour invoice does not match our PO...', envelope: env(), occurred_at: s(10200), created_at: s(10200), delivered: true },
  { id: 'ev-9', project: P, type: 'worker.freeze_refused', text: 'Refused worker_prompt_write against frozen worker "fee-scorer".', envelope: env({ depth: 3, source: 'core', worker: 'email-reviewer', session_id: 'sess-r7' }), occurred_at: s(46000), created_at: s(46000), delivered: false },
  { id: 'ev-8', project: P, type: 'worker.finished', text: 'Assistant: reviewed 3 answers; rewrote email-answerer (rationale: narrowing yesterday\'s rule)...', envelope: env({ depth: 2, source: 'worker', worker: 'email-reviewer', session_id: 'sess-r6' }), occurred_at: s(37200), created_at: s(37200), delivered: true },
  { id: 'ev-7', project: P, type: 'worker.finished', text: 'Assistant: TICKET-8812 — Hi Jane, thanks for getting in touch about the missing credit note...', envelope: env({ depth: 1, source: 'worker', worker: 'email-answerer', session_id: 'sess-a3' }), occurred_at: s(38000), created_at: s(38000), delivered: true },
  { id: 'ev-6', project: P, type: 'email.received', text: 'From: jane@acme.co\nSubject: Missing credit note\n\nWe still have not received...', envelope: env(), occurred_at: s(38800), created_at: s(38800), delivered: true },
  { id: 'ev-5', project: P, type: 'invoice.received', text: 'Supplier invoice PDF: velocity-couriers-jul.pdf', envelope: env(), occurred_at: s(50400), created_at: s(50400), delivered: true },
  { id: 'ev-4', project: P, type: 'schedule', text: 'Write the morning note: one observation from yesterday worth sharing.', envelope: env({ source: 'schedule', worker: 'social-writer' }), occurred_at: s(25200), created_at: s(25200), delivered: true },
]}

export const deliveries = { deliveries: [
  { id: 'del-11', project: P, event_id: 'ev-11', subscription_id: 'sub-2', session_id: 'sess-r8', status: 'running', started_at: s(180), ended_at: 0, created_at: s(200), updated_at: s(180) },
  { id: 'del-10', project: P, event_id: 'ev-10', subscription_id: 'sub-1', session_id: 'sess-a4', status: 'awaiting_human', started_at: s(10100), ended_at: 0, created_at: s(10200), updated_at: s(9600) },
  { id: 'del-9', project: P, event_id: 'ev-7', subscription_id: 'sub-3', session_id: 'sess-arch2', status: 'ok', started_at: s(37900), ended_at: s(37700), created_at: s(38000), updated_at: s(37700) },
  { id: 'del-8', project: P, event_id: 'ev-8', subscription_id: 'sub-4', session_id: 'sess-f2', status: 'ok', started_at: s(37100), ended_at: s(36900), created_at: s(37200), updated_at: s(36900) },
  { id: 'del-7', project: P, event_id: 'ev-7', subscription_id: 'sub-2', session_id: 'sess-r6', status: 'ok', started_at: s(37900), ended_at: s(37300), created_at: s(38000), updated_at: s(37300) },
  { id: 'del-6', project: P, event_id: 'ev-6', subscription_id: 'sub-1', session_id: 'sess-a3', status: 'ok', started_at: s(38700), ended_at: s(38100), created_at: s(38800), updated_at: s(38100) },
  { id: 'del-5', project: P, event_id: 'ev-5', subscription_id: 'sub-5', session_id: '', status: 'failed', started_at: s(50300), ended_at: s(50290), created_at: s(50400), updated_at: s(50290) },
  { id: 'del-4', project: P, event_id: 'ev-4', subscription_id: 'sub-1', session_id: 'sess-s1', status: 'rate_limited', started_at: 0, ended_at: 0, created_at: s(25200), updated_at: s(25200) },
]}

export const configEvents = { config_events: [
  { id: 'cfg-9', project: P, actor_worker: '', actor_session: '', action: 'schedule_update', payload: { id: 'sched-3', worker: 'invoice-parser', cron: '30 2 * * *', input: 'Sweep the invoices folder.', enabled: false }, rationale: 'disabled after 5 consecutive provision failures; last: compose: worker.image toolbox:9 names no image in the §13 catalogue', created_at: ms(3600 * 9) },
  { id: 'cfg-8', project: P, actor_worker: 'email-reviewer', actor_session: 'sess-r6', action: 'worker_prompt_write', payload: { name: 'email-answerer', system_prompt: PROMPT_V12 }, rationale: "narrowing yesterday's rule: reference only when one exists — two replies opened with a bare 'Ticket:' line for chats that had no ticket", created_at: ms(37000) },
  { id: 'cfg-7', project: P, actor_worker: 'archivist', actor_session: 'sess-arch2', action: 'image_create', payload: { name: 'toolbox', version: 4, labels: { kind: 'curated' } }, rationale: 'burned after installing the pdf skill', created_at: ms(43000) },
  { id: 'cfg-6', project: P, actor_worker: 'email-reviewer', actor_session: 'sess-r2', action: 'worker_prompt_write', payload: { name: 'email-answerer', system_prompt: PROMPT_V11 }, rationale: 'answers kept omitting the ticket reference, so the rule is now first — the last four replies all needed a follow-up to establish the case', created_at: ms(120000) },
  { id: 'cfg-5', project: P, actor_worker: '', actor_session: '', action: 'worker_update', payload: { name: 'email-answerer', system_prompt: PROMPT_V10, enabled: true, frozen: false }, rationale: '', created_at: ms(86400 * 3) },
  { id: 'cfg-4', project: P, actor_worker: '', actor_session: '', action: 'worker_freeze', payload: { name: 'fee-scorer', frozen: true }, rationale: 'measurement instrument for the fee experiment — nothing may tune its own judge', created_at: ms(86400 * 11) },
  { id: 'cfg-3', project: P, actor_worker: '', actor_session: '', action: 'topology_apply', payload: { topology: 'actor-critic@v1', answers: { actor: 'email-answerer', critic: 'email-reviewer' } }, rationale: 'seeded the email desk', created_at: ms(86400 * 16) },
]}

export const attention = { attention_requests: [
  { id: 'att-1', project: P, session_id: 'sess-a4', worker: 'email-answerer', message: "Reply drafted for the Ridley invoice query, but the amount doesn't match our records. Send as-is, or hold it?", session_url: 'http://localhost:8080/p/badcode/s/sess-a4', channel: 'webhook', delivered: true, expires_at: 0, created_at: s(9600), answered_at: 0, timed_out_at: 0 },
  { id: 'att-2', project: P, session_id: 'sess-s2', worker: 'social-writer', message: 'Three versions of the evening note drafted. Which goes out at 17:00?', session_url: 'http://localhost:8080/p/badcode/s/sess-s2', channel: 'none', delivered: false, expires_at: s(-18000), created_at: s(68400), answered_at: 0, timed_out_at: 0 },
]}

export const memories = { memories: [
  { id: 'mem-5', labels: { kind: 'rolling-summary', worker: 'email-answerer' }, snippet: 'Open threads: RIDLEY-2231 (amount dispute, awaiting Kai). Recent lesson: always quote the ticket reference when one exists. Tone: customers respond faster to a first-line reference…', score: 0, created_by_worker: 'archivist', created_by_session: 'sess-arch2', created_at: ms(37600) },
  { id: 'mem-4', labels: { kind: 'prompt-revision', worker: 'email-answerer' }, snippet: PROMPT_V11.slice(0, 220) + '…', score: 0, created_by_worker: 'email-reviewer', created_by_session: 'sess-r6', created_at: ms(37000) },
  { id: 'mem-3', labels: { kind: 'lesson', worker: 'email-reviewer', name: 'ticket-reference-rule' }, snippet: 'When a reply omits the ticket reference the customer replies asking which case — cost is one full round-trip.', score: 0, created_by_worker: 'email-reviewer', created_by_session: 'sess-r2', created_at: ms(121000) },
  { id: 'mem-2', labels: { kind: 'rolling-summary', worker: 'email-answerer' }, snippet: 'Yesterday: 9 answered, 1 escalated. Watch: Ridley fee table v3 in force since July.', score: 0, created_by_worker: 'archivist', created_by_session: 'sess-arch1', created_at: ms(110000) },
]}

export const sessions = { sessions: [
  { id: 'sess-a4', customer: P, worker: 'email-answerer', created_at: s(10100), updated_at: s(9600) },
  { id: 'sess-a3', customer: P, worker: 'email-answerer', created_at: s(38700), updated_at: s(38100) },
  { id: 'sess-r6', customer: P, worker: 'email-reviewer', created_at: s(37900), updated_at: s(37300) },
]}
