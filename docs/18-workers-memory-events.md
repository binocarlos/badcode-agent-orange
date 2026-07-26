# 18 — Workers, memory, and events (operator's guide)

How to run the product layer: configure a project, hire workers, wire what wakes them, and
understand what they remember. This is the **operating** guide — for *why* it is shaped this way,
and for anything normative, the spec is `docs/product/` (start at
[`00-overview.md`](product/00-overview.md); the entry point is
[`17-product-spec.md`](product/17-product-spec.md)). Section markers like §7.6 below point into it.

Docs `01`–`15` describe the engine underneath (sessions, containers, images, events, the stack).
This document sits on top of them.

---

## 0. Prerequisites, and the one trap

**The product layer needs Postgres.** agentd wires it only when `DATABASE_URL` is set. On the
sqlite fallback (no `DATABASE_URL`) you get a working chat runtime and nothing else:

| Without `DATABASE_URL` | Effect |
| --- | --- |
| Event router | never runs — events are never routed, subscriptions never fire |
| Scheduler | never runs — schedules never fire |
| Core MCP server (`memory_*`, `worker_*`, `image_*`, …) | **not mounted at all** |
| Project settings / workers | resolved by nothing — project prompt, base image and MCP defaults silently do not apply |
| `POST /agent/attention` | not mounted (404) |

agentd says so at boot (`no DATABASE_URL — event routing, schedules and request_human_attention
are unavailable`, and `core mcp DISABLED (no DATABASE_URL)`), but nothing fails loudly at
*use* time, so a stack accidentally running on sqlite looks healthy and does nothing. The
compose stack always sets `DATABASE_URL`; see [`15-standalone-stack.md`](15-standalone-stack.md).

Within Postgres, pgvector is optional (§7.6.5 — search degrades to keyword + recency). Memory
itself is not: all three memory store methods return `ErrMemoryRequiresPostgres` on sqlite rather
than silently forgetting.

---

## 1. The shape

```
PROJECT  (the only hard namespace — comes from the JWT, never from a request body)
  defaults: base image · project prompt · MCP config · budgets          §5
  WORKERS: {name, system_prompt, mcp_config, image, max_instances, briefing, enabled}   §6
      ▲ woken by: subscriptions on events · schedules (cron + input text) · a human chatting
      ▼
  ROUTER composes a JOB → a SESSION in its own container                §6.2, §8.4
      tools = worker's MCP servers + the core tools (memory/images/skills/management)
      finishes → worker.finished event (full transcript) → other workers may react
  SUBSTRATES, all append-only, labeled, provenance-carrying:
      memories §7 · images §13 · skills §14 · config log §15
```

A *project* is just a namespace string (the `customer` claim on the JWT). Everything below is
scoped to it in code; no route or tool takes a project parameter.

---

## 2. Configure a project

`GET` / `PUT /agent/project-settings` (whole-object PUT, no patch semantics), or the project
settings page in the UI. The row is created lazily; `GET` before any write returns the defaults.

| Field | Meaning |
| --- | --- |
| `base_image` | default launch image for the project (`''` → the global `AGENTKIT_IMAGE`). A **curated image name** — `toolbox` or `toolbox:1`, §7 — resolves through the image catalogue exactly as `worker.image` does; anything the catalogue does not hold is used as a literal registry reference, so `ghcr.io/acme/base:v1` still works |
| `system_prompt` | prepended to every worker's prompt at composition |
| `mcp_config` | MCP servers granted to **all** workers — no per-worker filtering |
| `attention_channel` | where `request_human_attention` posts: `{"kind":"webhook","url":…,"headers":{…}}`. Header values may be whole-value `${VAR}` references resolved from agentd's env. Unset → the tool still succeeds and only logs |
| `max_concurrent_jobs` | per-project cap on non-interactive jobs (default 4) |
| `daily_tokens_soft` | one attention-channel notification per day when crossed (0 = off) |
| `daily_tokens_hard` | stops non-interactive job creation until midnight, stack-local (0 = off) |
| `briefing_max_bytes` | byte cap **per injected briefing section** (default 2048) |
| `snapshot_ttl_days` | days before the snapshot reaper deletes a snapshot image (default 30; 0 = never) |

Interactive chat is exempt from both budgets and from `max_concurrent_jobs` — a blown budget
must never lock a human out of talking to their workers.

The project prompt is also writable from inside a session with `project_prompt_write` (§9), which
is how a consultant worker improves it.

### Credentials for MCP servers

MCP config stored in the database only ever **names** a variable —
`{"env": {"GMAIL_API_KEY": "${GMAIL_API_KEY}"}}`. The value lives in the operator's environment
and travels agentd → session container through the `AGENTKIT_MCP_ENV` allowlist (§4.4, §11 below).
A `${VAR}` that is unset — or exported but empty — fails the MCP server loudly at spawn, so a
misconfigured credential never silently authenticates as anonymous.

---

## 3. Hire a worker

`GET /agent/workers`, `GET`/`PUT`/`DELETE /agent/workers/{name}`, the UI's workers page, or the
`worker_create` / `worker_update` / `worker_prompt_write` core tools from inside a session.

| Field | Meaning |
| --- | --- |
| `description` | one-liner for the UI and for other workers' context |
| `system_prompt` | the worker's prompt — wholesale string, no fragments, no templates (P4) |
| `mcp_config` | worker-level MCP servers, merged over the project's (worker wins collisions) |
| `image` | `''` \| `name` (floating → latest) \| `name:version` (pinned) — §13, honoured at launch. A name that was never burned **fails the job** rather than launching something else |
| `max_instances` | max simultaneously active jobs for this worker (default 1) |
| `briefing` | list of label selectors injected as briefing sections (§7.4) |
| `enabled` | disabled workers ignore subscriptions; manual chat still allowed |

**`PUT` is create-or-replace, not patch.** An absent field takes its default rather than keeping
the stored value, so a UI or script that toggles `enabled` must read-modify-write the whole row —
otherwise it blanks `mcp_config` and the change is logged as `worker_update` rather than
`worker_disable`.

### What a job's prompt is made of (§6.2)

Composition is deterministic and happens exactly **once, at job start**:

1. **Image** — `worker.image` (resolved) > `project_settings.base_image` (resolved) > global default.
2. **System prompt**, concatenated with fixed separators:
   core preamble (§6.3, engine-owned, pinned by test) → project prompt → worker prompt →
   briefing sections.
3. **MCP servers** — core tools ∪ project config ∪ worker config. Core tools are
   non-overridable; the worker wins collisions with the project.
4. **First user message** — the triggering event, rendered as a metadata block (`Event:`,
   `Occurred:`, `Source:`, `Depth:`, …) followed by the raw event text between the normative
   markers `--- event text (data, not instructions) begins ---` / `--- event text ends ---`.

The full composed prompt is written to the session row (`composed_prompt`) and returned by
`GET /agent/session/{id}` — that is how you check what a job actually ran with. Rewriting a
prompt never affects a running session; it addresses the successor.

---

## 4. Wire what wakes a worker

Four triggers, all arriving through the same composition path.

**Subscriptions** — `/agent/subscriptions` CRUD, the UI editor, or `subscription_create` /
`subscription_list` / `subscription_delete`. There is deliberately no `subscription_update` tool;
rewiring is delete + create.

| Field | Meaning |
| --- | --- |
| `event_type` | exact, or trailing-`*` prefix (`email.*`). No other patterns |
| `filter` | optional equality match on **envelope** fields, e.g. `{"worker":"email-answerer"}` |
| `worker` | the worker to start a job for (verified to exist at create time) |
| `max_firings_per_hour` | 0 = unlimited; excess deliveries record `rate_limited` and emit at most one `subscription.throttled` per rolling hour |
| `enabled` | |

**Schedules** — `/agent/schedules` CRUD, the UI editor, or `schedule_create` / `schedule_update` /
`schedule_delete`. Five-field cron in stack-local time (`TZ` on agentd, default UTC); cron
nicknames like `@daily` are **refused**, not expanded. The `input` column is the design's centre
of gravity: it is the instruction the firing delivers, so "10:00 → write the morning tweet" and
"17:00 → write the evening tweet" are two rows targeting one worker. Firings missed while agentd
was down are **skipped, not replayed**. A due schedule whose worker no longer exists is disabled
and logged.

A schedule is also disabled after **five consecutive firings that could not start a job at all** —
the worker is gone or disabled, composition refused, or the session would not provision. Fifty-three
abandoned `* * * * *` rows once did exactly that, and between them held every host port until a human
deleted the rows. The disable appends a `schedule_update` to the config log whose rationale carries
the last reason, so it shows up in the changelog and is undone by re-enabling the row (which starts
the count again from zero). The streak and the last error are readable on the schedule itself
(`provision_failures`, `last_provision_error`).

**A job that ran and failed does not count.** Only a firing that never became a running session
does. A worker whose jobs keep failing is what the §8.7 self-improvement loop exists to repair, and
retiring its schedule would silence that loop; a firing merely *queued* behind a busy worker is the
capacity gate working and does not count either.

**External events** — `POST /agent/events {type, text}`, project from the JWT. Note that with
schedules in core, *scheduled pull usually beats pushed events*: give a worker an email MCP tool
and a schedule saying "every 2 hours, check the inbox", rather than building a connector.

**A human** — chatting to a session in the UI. (See §9: the "chat with this worker" button does
not yet compose the worker's prompt.)

### Events core emits itself (§8.2)

`worker.finished` (text = the full rendered transcript — *the* composition primitive),
`worker.failed` (`reason: "error"` or `"lost"`), `human.attention.timeout`,
`subscription.throttled`, `config.changed`. `worker.finished` fires for worker jobs only, never
for plain sessions, and interactive chats emit it too — subscriptions that shouldn't react to
chats filter on `interactive`.

### What the router will and won't do (§8.4)

- **Depth floor.** Every event carries `depth` (triggering job's depth + 1; external = 0). The
  router refuses depth > 8 and logs loudly. This is the only runaway protection.
- **Per-project cap.** `max_concurrent_jobs`, interactive jobs exempt.
- **Per-worker gate.** A delivery dispatches only while the worker's active jobs are below
  `max_instances`; excess deliveries stay `pending` and go FIFO as instances free. Router and
  scheduler share one gate, so a schedule firing queues like any other delivery.
- **Budget.** Checked before every non-interactive job. A budget the store cannot evaluate
  **fails open** (logged loudly, the job runs) — stopping a whole workforce because Postgres
  hiccuped is the larger harm.
- **Lease reaper.** Sessions carry a lease renewed while the sandbox streams; an expired lease
  is marked failed and emits `worker.failed` with `reason:"lost"`.

Delivery status is exactly `pending|running|ok|failed|awaiting_human|rate_limited`. A **cancelled**
turn records `ok` (nothing better exists in that closed vocabulary, and the lease is released).
A job whose turn called `request_human_attention` and left the request open ends at
**`awaiting_human`** with no `ended_at` — a pause, not a completion. A failed turn still records
`failed`: a crash does not get to look like patience. A parked delivery holds **no** capacity slot
(`max_concurrent_jobs` and `max_instances` count `running` only), so a human who never replies
cannot retire a worker. Nothing currently moves a delivery *out* of `awaiting_human` — a human
replying resumes the session but leaves the old job-history row parked; see "known limitations".
`GET /agent/events` and `GET /agent/deliveries` are the read paths the UI's events view uses.

---

## 5. Memory

An append-only, labeled, project-scoped store (§7). There is no update and no delete — not as an
oversight but as the design: a memory records a moment, and "changing" one means appending a newer
one.

- **Labels** are a flat `map[string]string`, Kubernetes-style: keys and values are **identifiers**
  (≤63 chars, no `/`-prefixed segment), at most 32 per memory. Free text — an email subject — is
  rejected loudly. *Labels are identifiers; content is content.*
- **Selectors** are Kubernetes selector semantics exactly: `worker=x`, `kind!=y`,
  `kind in (summary, lesson)`, `exists thread`, `!archived`, comma = AND. No OR, no nesting — run
  two searches.
- **Search (§7.6) is a fixed contract, not a set of knobs.** Project filters first, always, in
  code; then the selector. With no `query` text results are newest-first. With `query` text two
  legs run — Postgres full-text over content, and pgvector cosine over the embedding — fused with
  Reciprocal Rank Fusion, recency as tiebreak only. No embeddings configured ⇒ the semantic leg is
  skipped and the shape of the result is unchanged. There is no distance threshold, so a search
  always returns up to `limit` rows: a low fused score means "nothing good", not "no match".
- **`name=` is the KV convention.** The current value of `name=x` is the newest memory carrying
  that label; updating it is appending. `memory_current(name)` reads it in one word.
- **Provenance is part of every result** — which worker wrote it, in which session, with a
  clickable permalink (`<AGENTKIT_PUBLIC_BASE_URL>/p/<project>/s/<session>`).

### How memory reaches a prompt

Core performs **exactly one kind of memory read on its own**: the briefing lookups at composition
time. For each of — the built-in default selector `kind=rolling-summary, worker=<name>`, plus each
selector in the worker's `briefing` column — core takes the *newest match* and injects it as its
own headed section (the default one is headed `Your memory briefing`, the extra ones
`Your memory briefing: <selector>`), each independently capped at `briefing_max_bytes` with a
truncation marker when it trims. Nothing else. Every other read is a
worker calling `memory_search` because its prompt told it to.

**Producing the rolling summary is a worker's job, not core's.** The canonical arrangement is an
archivist subscribed to `worker.finished` whose prompt says "read the transcript, store what is
worth keeping with sensible labels, and append a fresh `kind=rolling-summary` memory for the
subject worker". No archivist wired ⇒ no summary ⇒ workers simply run without a briefing. Core
never auto-archives anything. Reference prompts live in
[`docs/product/07-reference-prompts.md`](product/07-reference-prompts.md).

A briefing section that fails to load costs that section and is logged — a worker with a stale
briefing works; one that cannot start does not.

---

## 6. The core tools

agentd serves one MCP server named `core` at `/mcp`, authenticated by the session's own token
(the project scope comes from the token, so a session physically cannot cross projects). Tools
reach the model as `mcp__core__<name>`. Every job is told about it automatically; you do not
configure it.

| Group | Tools |
| --- | --- |
| Memory (§7.3) | `memory_create` `memory_search` `memory_get` `memory_current` |
| Workers & prompts (§9) | `worker_list` `worker_create` `worker_update` `worker_prompt_read` `worker_prompt_write` `project_prompt_read` `project_prompt_write` |
| Wiring (§8) | `subscription_list` `subscription_create` `subscription_delete` `schedule_list` `schedule_create` `schedule_update` `schedule_delete` |
| Images (§13) | `image_create` `image_list` |
| Skills (§14) | `skill_create` `skill_list` `skill_get` `skill_install` |
| History (§15) | `config_history` |
| Humans (§9) | `request_human_attention` |

Notable absences, all deliberate: no `memory_update`/`memory_delete`, no `worker_delete` (retiring
is `worker_update(name, {enabled:false})`; hard delete stays HTTP/UI-only), no
`subscription_update`, no `restore_project`.

Every mutation tool validates, writes, then **reads the row back and echoes it** — malformed input
fails loudly and nothing is half-written. `rationale` is **required** on `worker_prompt_write` and
`project_prompt_write`, optional elsewhere.

`request_human_attention(message, expires_in?)` posts `{message, session_url}` to the project's
`attention_channel`, stamps the session and the `worker.finished` envelope, echoes the permalink,
and the worker ends its turn. The human clicks through to the ordinary chat and whatever they type
is the worker's next message. There is no approval queue and no pending-items UI — the thread is
the review surface. A misconfigured channel never fails the worker's turn; only `delivered:false`
differs.

---

## 7. Images and skills

Both are append-only, labeled, project-scoped records with provenance (§13, §14).

An **image** is `name:version` — `image_create(name, labels)` snapshots the *calling session's*
current environment and allocates the next version under that name; a bare `name` reference means
"latest", `name:version` pins. Nothing about a session's filesystem survives unless an agent
explicitly burns it; there is no ambient accumulation.

A **skill** is a markdown document plus an optional `install_sh`. `skill_install(name)` writes the
markdown into the harness's skills directory (`/workspace/.claude/skills/<name>/SKILL.md`) and
runs the script in the container, reporting **both** outcomes — file written, script exit status
and output — so a failed install is visible rather than a silent no-op. A skill installed mid-turn
is usable on the **next** turn (the SDK loads skills at query start).

The sanctioned workflow is *curate then burn*: open a session on a vanilla image, `skill_install`
what you want, check it works, `image_create("toolbox", {...})`, then point workers at `toolbox`.
There is no `skills` column on workers and core never auto-installs anything — which skills a
worker installs is its prompt's business (P1).

**Adoption is a separate, visible act.** Burning a new version changes what a floating pointer
resolves to; it never repoints a worker by itself and never *creates* a pointer. Moving a worker
onto an image is `worker_update(name, {image: …})` (or the UI's image field), which is a
config-evented mutation like any other — so "when did this worker start running on the toolbox
image, and who decided that?" is a query. The pointer is resolved **at launch, every launch**, so
a floating `toolbox` follows curation and a pinned `toolbox:1` does not. A launch also stamps
`last_resumed_at` on the version it used, which is how an operator sees that an image due for
reaping is still in daily use (it does **not** extend the expiry — §5 sets that at burn time).

The launch chain, in full, is `explicit image > worker pointer > custom image id >
project_settings.base_image > global default`. A worker job arrives with the pointer already
resolved by composition; anything else on a worker resolves it at launch.

**A curated image name is legal in `project_settings.base_image` too**, and resolves through the
same catalogue — the same string must not mean two different things in two columns. The three
middle links fail in deliberately different ways:

| Link | A name the catalogue does not hold | A name it holds but cannot serve (reaped, unmaterialisable, database down) |
| --- | --- | --- |
| `worker.image` | fails the job | fails the job |
| `project_settings.base_image` | used verbatim as a registry reference | **fails the launch** |
| custom image id (legacy) | logs, session still starts | logs, session still starts |

The middle row is what keeps `ghcr.io/acme/base:v1` and the stack's own `agentkit-sandbox:dev`
working: only "that is not one of mine" falls through to a literal pull. A name the catalogue
*does* know and cannot produce fails, because quietly launching something else is the drift §13
exists to prevent. Either way the error names the setting and the value —

```
ensure image present: project_settings.base_image = "definitely-not-an-image:v9" (project "acme")
names no image in the §13 catalogue, so it was used as a literal registry reference and that
reference failed: Error response from daemon: …
```

— so a mistyped or unburned image is a ten-second diagnosis rather than "the session never
started". Nothing is validated at **write** time: a pointer may legitimately name an image
curation is about to publish (the same rule `worker.image` follows), and no write-time check can
tell a typo from a registry reference that simply is not in the catalogue.

`image_create` and `skill_create` are configuration mutations and appear in the config log;
`skill_install` changes the *session*, not the project, and writes no config event.
`image_list`/`skill_list` cap at the 200 newest and say so in the result.

---

## 8. The config log

Every management mutation appends a `config_events` record **in the same transaction** as the
projection-table write, and emits a routable `config.changed` event **after commit** (§15). The
payload is the full new state, never a diff. Deletes append too, carrying the final state.

What that gives you:

- **`config_history(query)`** from inside a session — newest-first, filtering on `entity`,
  `action` (exact or trailing-`*`), `actor_worker`, `since`/`until`, `limit`; every record carries
  the acting session's permalink.
- **`GET /agent/config-events`** over HTTP, paged with `?before_seq=` (the log's only total
  order — do not page on a timestamp). GET only: a config event exists solely as the shadow of a
  real mutation, so there is no way to forge one.
- **The changelog view** in the UI, with prompt diffs computed at read time and rationales shown
  as the commit messages they are.
- **Restore is a forward operation.** "Put the prompt back to how it was on the 18th" is an
  ordinary `worker_prompt_write` whose rationale names the config-event id. Nothing is truncated;
  the regression and the revert both stay in the record. There is no destructive restore, by
  design.

Caveats worth knowing before you read payloads: `config_events.created_at` is **milliseconds**
(most `agent_*` tables use seconds), and payload timestamps are not authoritative — use the
config event's own `created_at`. Edits made over HTTP carry no `rationale` today (no route has a
field for it), so a human edit logs an empty *why*; the MCP tools are the path that records one.

---

## 9. Known limitations

Stated plainly because each one will otherwise be discovered the hard way.

- **A pointer at an image nobody burned fails the job, on purpose.** `worker_update` accepts any
  syntactically valid pointer (a worker may be pointed at an image curation is about to publish),
  and launch-time resolution then refuses an unknown name, a version the TTL reaper tombstoned, or
  one whose bytes cannot be materialised. The delivery is marked `failed` and **no session is
  created** — §13.3 forbids falling back to the project default, because a worker that was pointed
  at an environment and quietly got a different one is the drift §13 exists to prevent. The reason
  is in agentd's log (`[dispatch] delivery … failed: compose: …`); the delivery row itself carries
  no reason column.
- **"Chat with this worker" opens a plain session.** The create-session HTTP body has no `worker`
  field, so the UI's chat tab produces an uncomposed session (no worker prompt, no briefing) —
  never a forged one. It starts working the moment the create path composes.
- **Sessions hold a running container until the session is deleted.** Nothing reaps them on a
  timer. They accumulate, and past roughly a hundred a stack starts failing to provision new
  sessions. That used to read as "session has no running instance and no snapshot", which looks
  exactly like a product bug and is not one; it now says the host is at capacity and names the
  port pool. Delete sessions you are finished with — see `docs/15-standalone-stack.md`.
- **Deleting a session mid-create is now safe, but stale orphans are still not swept.** A delete
  that lands while the create is still pulling or provisioning used to leave the container
  behind holding a port, invisible to everything that starts from the database; the create now
  cancels itself and tears down what it built. What is *still* not reaped is a container orphaned
  by a previous `agentd` process, because the only safe predicate is in-process ("this delete
  cancelled this create") — see `docs/15-standalone-stack.md` § "Deleting a session mid-create no
  longer leaks its port" for why the row-is-missing predicate was rejected.
- **A session that fails to start now says why.** The reason is recorded on the session row
  (`agent_sessions.create_error`), logged by `agentd`, returned as `create_error` from
  `GET /agent/session/{id}`, and prefixed onto the next message's error — so a mis-typed
  `project_settings.base_image` names the setting, its value, the project and which of the two
  §13 interpretations it was given, instead of claiming the session was lost. Only a session with
  no instance, no snapshot, no recorded reason and a host with room still says **"must be
  re-created"**. Capacity is asked of the environment *live* and outranks the stored reason,
  because a stored capacity error would go stale; conversely a capacity failure never overwrites a
  recorded configuration reason. Details and the exact strings: `docs/15-standalone-stack.md`.
- **`POST /agent/project-token` returns 501** in the standalone stack (the issuer seam is not
  wired), so a headless event poster needs an ordinary JWT for now.
- **Session tokens expire after an hour, jobs do not.** An expired-but-signature-valid token is
  accepted while its session row still exists and matches the project; an expired token for an
  unknown session is 401.
- **`GET /agent/sessions` has no server-side worker filter**, so the UI's per-worker job history
  filters one 200-row page client-side and says so.
- **Semantic memory search is off in the shipped stack.** See §11.
- **Tool calls are absent from `worker.finished` transcripts** — the rehydration renderer skips
  tool events, and it is reused rather than duplicated.
- **A delivery parked at `awaiting_human` never leaves that status.** The human clicks the
  permalink and replies, and the *session* resumes exactly as §9 intends — but the reply arrives
  through the ordinary chat path, which knows nothing about deliveries, so the job-history row
  stays parked with no `ended_at`. It is a display wart, not a stall: the parked row holds no
  capacity slot, the lease reaper only touches `running` rows, and the worker keeps running new
  jobs. Closing it would mean either a resume hook on the message path or extending the attention
  sweep — and `expires_in`-less requests, which are the common case, are invisible to that sweep.
  Deliberately not fixed by growing an approval state machine, which §9 explicitly deletes.

---

## 10. The acceptance loop, if you want to see it work

The scenario the whole design exists to serve (§8.7): two workers, `email-answerer` and
`email-reviewer`; a subscription `email.received → email-answerer`; a subscription
`worker.finished {worker: email-answerer} → email-reviewer`; the reviewer's prompt telling it to
amend the answerer's prompt via `worker_prompt_write` when it sees a systemic problem. Post an
`email.received` event and the loop runs: answerer job → reviewer job → prompt rewritten →
`config_events` record with its rationale → `config.changed` event → the next job composes from
the new prompt.

It runs offline. `AGENTKIT_MOCK_MODEL_SCRIPT` (§11) is what lets the mock model emit a `tool_use`,
which is what makes an offline test able to prove a worker rewrote a worker. The e2e lives under
`e2e/features/` and runs against the compose stack via `playwright.stack.config.ts` — not the
legacy `e2e/tests/` rig.

The first real deployment (§8.8) is the BadCode marketing manager: seed exactly *one* worker whose
prompt describes the workforce that should exist, give it a daily schedule saying "reconcile the
workforce", and it builds the rest from its own prompt. Reconciliation-as-idempotent-instruction
means there is no bootstrap code path at all.

---

## 11. Environment variables

Everything below is read by `agentd`. `.env.example` and `docker-compose.yml` carry the full
commentary; this is the product-layer subset.

| Variable | Effect |
| --- | --- |
| `DATABASE_URL` | Postgres connection string. **Set = the product layer exists; unset = it does not** (see §0). The compose stack always sets it |
| `AGENTKIT_MCP_ENV` | Comma-separated **names** of variables agentd forwards into every session container, so the sandbox can resolve `${VAR}` in MCP config. Allowlist only: naming one of agentd's own secrets (`AGENTKIT_JWT_SECRET`, `ANTHROPIC_API_KEY`, `DATABASE_URL`, …) is a **boot error**. A name that is unset is warned about and not forwarded |
| `AGENTKIT_PUBLIC_BASE_URL` | Externally reachable base of the web UI; permalinks are minted as `<base>/p/<project>/s/<session>`. Default `http://localhost:8080`. Not `AGENTKIT_SELF_URL` — that is a DinD bridge address only containers can reach. Must be absolute http(s), no query or fragment, or agentd refuses to boot |
| `AGENTKIT_EMBEDDING_BACKEND` | `none` (default) or `mock`. A typo is a boot error, never a silent fall back. **Not forwarded by `docker-compose.yml`**, so the shipped stack always runs `none` — memory search is keyword + recency until you add the line |
| `AGENTKIT_PORT_RANGE_START` / `_END` | The host port pool session containers lease from — its size **is** the concurrent-session ceiling for the host, because one live session holds one port until it is deleted. Default `30001` / `30100` (100 sessions), unchanged from when it was hardcoded. Set both or neither; a non-numeric bound, start above end, a port outside `1024-65535`, or a range wider than 10000 ports is a **boot error**, never a pool that starts and then fails every session. Logged at boot as `session port pool=<range>`. Narrow it (e.g. `40000`/`40002`) to exercise pool exhaustion deliberately — see [`15-standalone-stack.md`](15-standalone-stack.md) |
| `AGENTKIT_MOCK_MODEL_SCRIPT` / `_FILE` | Mock-model script, read **only** in mock mode (both model credentials blank) and only at boot. Without it the mock serves one canned text turn and can never emit a `tool_use`. Rules match on a substring of the raw model request (a worker name is the natural key — it appears in every composed prompt); the turn is chosen by the assistant-message count, so it is stateless and parallel sessions cannot contaminate each other. A malformed script fails the boot |

Each variable listed in `AGENTKIT_MCP_ENV` must also *reach* agentd: compose cannot forward a
dynamic set of names, so add one `environment:` line per credential in `docker-compose.yml`
alongside the allowlist entry.

Also relevant, documented in [`15-standalone-stack.md`](15-standalone-stack.md): `BASE_IMAGE`
(→ `AGENTKIT_IMAGE`), `AGENTKIT_JWT_SECRET`, the login variables, `TZ` (schedules are
stack-local), and the GCP storage backends.
