# Spec — Events, schedules, and routing

**Part of the product spec.** Entry point and binding principles: [`../17-product-spec.md`](../17-product-spec.md).
The deterministic between-workers mechanisms, plus the two worked examples. Section numbers (§) are kept from the original single-file spec, so cross-references
like §7.6 or §8.8 anywhere in the repo still resolve — the entry point has the full map.

---

## 8. Events, schedules, and routing

The deterministic between-workers mechanisms. Kept intentionally tiny: a subscriptions table,
a schedules table, and a loop that turns matches into jobs.

### 8.1 Event shape

An event is a **name and a text payload** — deliberately nothing more that a sender controls:

```json
{
  "id": "uuid",
  "project": "badcode",
  "type": "email.received",           // dot-namespaced, free-form for external events
  "text": "From: ...\nSubject: ...",  // the raw text a worker will read as its instruction/input
  "occurred_at": 1789000000,
  "envelope": {                        // stamped by CORE, never by the sender
    "depth": 1,                        // loop floor (§8.4)
    "source": "worker|external|schedule",
    "worker": "email-answerer",       // when source=worker
    "session_id": "…",
    "interactive": false,
    "attention_requested": false
  }
}
```

Keeping the payload as raw text (not structured JSON) is a P1 decision: the worker's prompt
decides how to read it, and every trigger — human message, inbound email, schedule input,
another worker's transcript — arrives through the same shape. Stored append-only in table
`project_events` (project, type indexed) — the audit trail a future consultant mines.

### 8.2 Internal events (emitted by core)

Exactly two to start; growing this list requires a design conversation:

- **`worker.finished`** — a job's query completed and the session went idle (the runtime
  already knows this moment: the `query_complete` event / idle-archive path in `runner.go`).
  Its `text` **is the full rendered transcript** (reusing the `rehydrateConversation`
  rendering — user/assistant turns + tool summaries); the envelope carries `worker`,
  `session_id`, `interactive`, and `attention_requested`. This is *the* composition primitive:
  email-answerer finishes → review-consultant wakes with the whole exchange in its first
  message. Interactive chats emit it too (each completed turn is a finished job) —
  subscriptions that shouldn't react to chats filter on `interactive`. `attention_requested`
  is true when the job called `request_human_attention` that turn, so reviewers/archivists can
  skip work that is deliberately half-done awaiting a human.
- **`worker.failed`** — the session errored terminally. `text` = the error; envelope as above.

Note `worker.finished` fires only for *worker* jobs, not plain vanilla sessions.

### 8.3 Subscriptions

Table `subscriptions`:

| column | type | meaning |
| --- | --- | --- |
| `project` | text | |
| `id` | uuid | |
| `event_type` | text | exact match or trailing-`*` prefix match (`email.*`); no other patterns |
| `filter` | jsonb | optional equality match on **envelope** fields (`{"worker":"email-answerer"}`) — enough to express "when *the email worker* finishes"; anything smarter belongs in the reacting worker's prompt ("if this doesn't concern you, finish immediately") |
| `worker` | text | the worker to start a job for |
| `enabled` | boolean | |

Managed via HTTP (`/agent/subscriptions` CRUD), the UI, and the management MCP tool (§7 core
tools include `subscription_list/create/delete`) — a consultant must be able to rewire the org.

### 8.4 The router

A small loop inside agentd (not a new binary — we deleted the last standalone daemon on
purpose):

1. `INSERT` event (ingestion API or internal emitter) with `delivered=false`.
2. Router polls (or LISTEN/NOTIFY on Postgres; polling is fine at our scale) undelivered
   events, matches subscriptions, and for each match creates a job: compose (§6.2) → create
   session → send the rendered event as the first message. Marks delivered per subscription in
   `event_deliveries` (event_id, subscription_id, session_id, status) — at-least-once with an
   idempotency guard on (event_id, subscription_id).
3. **Loop safety (the one hard floor, P1-compatible because it's resource safety, not
   opinion):** each event carries `depth` (triggering job's depth + 1, external = 0); the
   router refuses depth > 8 and logs loudly. Plus a per-project concurrent-jobs cap
   (default 4, in `project_settings`). No other governor — runaway prompt design is a prompt
   problem, but infinite loops and fork-bombs are physics.

### 8.5 External ingestion

`POST /agent/events {type, text}` (project from JWT; also mintable with a long-lived project
token so a mail-forwarder or webhook bridge can post without a browser login). Note that with
schedules (§8.6) in core, **scheduled pull beats pushed events for most integrations**: rather
than building an email-event connector, give the secretary worker an email MCP tool and a
schedule "every 2 hours: *check the inbox and respond to anything that needs it*". Ingestion
remains for genuinely push-shaped sources, but it is no longer the primary trigger path.

### 8.6 Schedules

Cron is a core primitive (this supersedes the earlier "external cron posts events" position —
the manager pattern in §8.8 requires workers to manage schedules as data, which an external
crontab cannot offer). Table `schedules`:

| column | type | meaning |
| --- | --- | --- |
| `project` | text | |
| `id` | uuid | |
| `worker` | text | the worker to start a job for |
| `cron` | text | standard 5-field cron expression; stack-local time (`TZ` on agentd, default UTC) |
| `input` | text | **the instruction this trigger delivers** — becomes the event text |
| `enabled` | boolean | |
| `updated_at` | bigint | |

The `input` column is the design's centre of gravity: the schedule doesn't just say *when* a
worker runs, it says *what it is told each time*. "10:00 → write the morning tweet" and
"17:00 → write the evening tweet" are two rows targeting one worker. Changing a worker's
cadence, or what it is asked on each firing, is a data edit any worker can make with the
`schedule_*` tools (§9) — which is precisely how a manager retunes its workforce.

Mechanics: a scheduler loop inside agentd (same process as the router) wakes each minute,
finds due enabled entries, and starts a job per entry with event
`{type: "schedule.fired", text: input}`, envelope `{source: "schedule", depth: 0}` — flowing
through the identical composition path (§6.2) as every other trigger. Firings missed while
agentd is down are **skipped, not replayed** (a tweet-writer must not wake to a backlog of
stale mornings); this is documented behaviour, and per-project `max_concurrent_jobs` applies.
Managed via `GET/PUT/DELETE /agent/schedules`, the UI, and the `schedule_*` tools.

### 8.7 Worked example (the acceptance scenario)

1. Project `acme` has workers `email-answerer` (prompt: how to answer; MCP: `gmail`) and
   `email-reviewer` (prompt: "review answered threads for tone; if you find a systemic
   problem, use `worker_prompt_read`/`worker_prompt_write` to amend email-answerer's system
   prompt with concrete guidance, and record a `kind=lesson` memory"), plus archivist per §7.4.
2. Subscription `email.received` → `email-answerer`; subscription `worker.finished`
   `{worker: email-answerer}` → `email-reviewer` (and → `archivist`).
3. A hundred emails flow through. The reviewer, seeing transcript after transcript, eventually
   writes "do not be curt; always acknowledge the customer's frustration" into the answerer's
   prompt. The next email is answered less rudely. **That end-to-end loop — behaviour changed
   by a worker editing a worker's prompt, no human, no deploy — is the definition of done for
   this entire spec.**

### 8.8 Reference use case: the BadCode marketing manager

The first real deployment (project `badcode` — BadCode is Kai, main developer, and Jack, lead
creative designer), and the scenario every feature above must serve. It also answers the
chicken-and-egg question — how do you go from no workers to a workforce?

1. **Seeding is human and minimal.** A human creates exactly *one* worker in the UI:
   `marketing-manager`. Its system prompt embodies the current marketing strategy (background
   on BadCode, the funnel strategy, platform posture) **and describes the workforce that
   should exist** — which workers, what their prompts should say, on what schedules, reacting
   to what. Nothing else is seeded.
2. **The manager reconciles, idempotently.** Two schedules drive it:
   - daily — input: *"Reconcile the workforce: ensure every worker, schedule, and subscription
     described in your system prompt exists and matches; create or update via your tools.
     Report what you changed."* On first firing there are no workers, so it builds the org
     (tweet author, Instagram image maker, secretary…) from its own prompt. Every later firing
     is a no-op unless the prompt has evolved. Reconciliation-as-idempotent-instruction means
     no bootstrap code path exists at all.
   - weekly — input: *"Critique your own system prompt: search memory for prompt revisions,
     published content, and lessons; judge the strategy's effectiveness; rewrite your prompt
     to be the most effective, complete version of itself."* This is the consultant loop
     folded into the manager (P2 — it's just a prompt telling a worker to improve a prompt,
     including its own).
3. **Content workers run staged autonomy.** The tweet author's prompt ends: *"generate the
   tweet, then call `request_human_attention` to get sign-off before posting."* The human gets
   a channel ping with the session link, reads the draft in the ordinary chat UI, replies
   "post it" (or starts a live back-and-forth). Months later, granting full autonomy is the
   manager editing one sentence out of that prompt — no approval machinery ever existed.
4. **Capability comes from the image.** The `badcode` project's base image bakes in the
   BadCode repository, its content-generation scripts, and its Claude skills (run
   non-interactively), plus MCP servers for the social platforms with `${VAR}` credentials
   (§4.4) — the atoms (custom images + MCP config) doing exactly what they were built for.
5. **Everything the manager does is data.** Workers, prompts, schedules, subscriptions — all
   rows it edits through core tools. There is no manager feature in Agent Orange, and no
   role/authorization model inside a project (any worker may adjust anything; the project is
   the only boundary — single-operator posture, §10).
