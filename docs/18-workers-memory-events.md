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
sqlite fallback you get a working chat runtime and nothing else: no event router, no scheduler,
no core MCP server, no project settings or workers, and `POST /agent/attention` 404s. agentd
says so at boot (`no DATABASE_URL — event routing, schedules and request_human_attention are
unavailable`, and `core mcp DISABLED (no DATABASE_URL)`), but nothing fails loudly at *use*
time, so a stack accidentally running on sqlite looks healthy and does nothing.

The compose stack always sets `DATABASE_URL`. The component-by-component table, and the rest of
the stack's wiring, live in [`15-standalone-stack.md`](15-standalone-stack.md) § "The store" —
that document owns *running* the stack; this one owns *operating* what runs on it.

Within Postgres, pgvector is optional (§7.6.5 — search degrades to keyword + recency). Memory
itself is not: every memory store method returns `ErrMemoryRequiresPostgres` against a
non-Postgres dialect rather than silently forgetting.

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
| `max_concurrent_jobs` | per-project cap on non-interactive jobs (0 ⇒ the default, 4) |
| `daily_tokens_soft` | one attention-channel notification per day when crossed (0 = off). The "once" is in-memory, so an agentd restart can re-notify, and the notice carries no `session_url` |
| `daily_tokens_hard` | stops non-interactive job creation until midnight, stack-local (0 = off) |
| `briefing_max_bytes` | byte cap **per injected briefing section** (0 ⇒ the default, 2048) |
| `snapshot_ttl_days` | stamps `expires_at` on each image burned by `image_create` (default 30; 0 = never). **Inert in the standalone stack** — agentd runs no reaper, so nothing acts on the expiry (see §9) |

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

Four triggers. **Three of them compose a job; the fourth does not.** Subscriptions, schedules
and external events all reach a worker through the router → dispatch gate → `ComposeJob`, so
they get the core preamble, the briefing and the event as a first message. A human chatting starts
an ordinary session, which gets none of those — see §9. (Since the embedding work it *does* get the
**core tools**; that was the one gap, and it is closed. See §6.)

**Schedules now have a second mode.** A schedule row targets **either** a worker — each firing
dispatches a fresh job into a fresh session — **or** a named session (`target_session`), where each
firing sends `input` to that existing session as its next message. See the `target_session` rows in
the schedules paragraph below.

**Subscriptions** — `/agent/subscriptions` CRUD, the UI editor, or `subscription_create` /
`subscription_list` / `subscription_delete`. There is deliberately no `subscription_update` tool;
rewiring is delete + create.

| Field | Meaning |
| --- | --- |
| `event_type` | exact, or trailing-`*` prefix (`email.*`). No other patterns |
| `filter` | optional equality match on **envelope** fields, e.g. `{"worker":"email-answerer"}` |
| `worker` | the worker to start a job for. `subscription_create` verifies it exists; the HTTP route and the store do **not**, so a subscription written over HTTP against a missing worker is only discovered when it fires |
| `max_firings_per_hour` | 0 = unlimited; excess deliveries record `rate_limited` and emit at most one `subscription.throttled` per rolling hour |
| `enabled` | |

**Schedules** — `/agent/schedules` CRUD, the UI editor, or `schedule_create` / `schedule_update` /
`schedule_delete`. Five-field cron in stack-local time (`TZ` on agentd, default UTC); cron
nicknames like `@daily` are **refused**, not expanded. A row carries **either** `worker` **or**
`target_session` (the NAME of an existing session), never both and never neither — the store
enforces the XOR (`go/agentdb/schedules.go:207-220`). A session-mode firing restores the session if
it was archived, and **skips rather than queues** if a turn is already in flight. The web console
cannot yet author one (`web/src/schedules.ts:321` hard-requires a worker), so session schedules are
API/MCP-only; `schedule_create` accepts `target_session`, `schedule_update` deliberately does not.
Full semantics, including exactly what a resumed session does and does not refresh:
[`19-embedding.md`](19-embedding.md) § 4. The `input` column is the design's centre
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
capacity gate working and does not count either. Nor does an infrastructure error from the gate
itself — that says the database hiccuped, not that the schedule is broken.

**The residual risk, stated because it is real.** The gate cannot tell "this schedule is
abandoned" from "this host is full": a session that fails to provision is a failed dispatch
either way, and `ErrNoCapacity` is not special-cased. So a genuine host-wide capacity crunch
lasting five minutes disables **every** schedule that fires during it, not only the broken ones.
The streak and reason are on each row, and re-enabling starts the count from zero, so recovery is
a re-enable per schedule — but you have to know to look. Judged the better trade against 53
abandoned rows holding every port on the host.

**External events** — `POST /agent/events {type, text}`, project from the JWT. Note that with
schedules in core, *scheduled pull usually beats pushed events*: give a worker an email MCP tool
and a schedule saying "every 2 hours, check the inbox", rather than building a connector.

**A human** — chatting to a session in the UI. (See §9: the "chat with this worker" button does
not yet compose the worker's prompt.)

### Events core emits itself (§8.2)

`worker.finished` (text = the full rendered transcript — *the* composition primitive),
`worker.failed` (`reason: "error"` or `"lost"`), `human.attention.timeout`,
`subscription.throttled`, `config.changed`.

`worker.finished` fires **only for sessions whose `worker` column is set**, which today means
routed jobs and nothing else — a plain chat emits nothing. The envelope carries `interactive`,
and a subscription that should not react to human chats filters on it; but since the only path
that sets `worker` also creates a delivery, no *interactive* `worker.finished` is currently
reachable. The field is the seam for when "chat with this worker" composes (§9), not a case you
have to handle today. A cancelled turn emits neither `worker.finished` nor `worker.failed`.

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
  is marked failed and emits `worker.failed` with `reason:"lost"`. It keys on the lease and
  never on session status, so an interrupted-but-resumable turn is invisible to it.
- **Interactive chat is not gated at all.** It never becomes a delivery, so it is structurally
  invisible to every check above — depth, caps, budget. That is the design (a blown budget must
  not lock a human out), not a branch that could be flipped.
- **Admission is not serialised.** Router and scheduler can both read capacity before either
  writes `running`, so the caps can be briefly over-admitted by one or two jobs. Known and
  logged, not fixed.

Delivery status is exactly `pending|running|ok|failed|awaiting_human|rate_limited`. A **cancelled**
turn records `ok` (nothing better exists in that closed vocabulary, and the lease is released).
A job whose turn called `request_human_attention` and left the request open ends at
**`awaiting_human`** with no `ended_at` — a pause, not a completion. A failed turn still records
`failed`: a crash does not get to look like patience. A parked delivery holds **no** capacity slot
(`max_concurrent_jobs` and `max_instances` count `running` only), so a human who never replies
cannot retire a worker. Nothing currently moves a delivery *out* of `awaiting_human` — a human
replying resumes the session but leaves the old job-history row parked; see "known limitations".

A delivery for a **disabled** worker also records `failed`. Since `worker_update(enabled:false)`
is the intended way to retire a worker (there is no `worker_delete` tool), every event that
matches a retired worker's subscription keeps adding a failed-looking row to job history for as
long as that subscription exists. Delete the subscription too, or expect the noise. A failed
delivery records **no reason** — `event_deliveries` has no such column, so the cause is only in
agentd's log (`[dispatch] delivery … failed: …`).

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
- **`memories.created_at` is milliseconds**, like `config_events.created_at` and unlike the
  `agent_*` tables, which use seconds. Anything joining them must not assume one unit.

### How memory reaches a prompt

Core performs **exactly one kind of memory read on its own**, and **exactly one kind of write**.
The write is the prompt-revision record: `worker_prompt_write` and `project_prompt_write` each
append a memory labelled `kind=prompt-revision` plus `worker=<name>` or `scope=project`, carrying
the rationale and the **superseded prompt** verbatim — so a bad rewrite is searchable and can be
put back by writing the old text again. If that memory fails to store, the tool still succeeds
and reports `prompt_revision: {stored: false}` with the error; the prompt is already live by then.

The read is the briefing lookups at composition
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
reach the model as `mcp__core__<name>`.

**Every session gets these tools now — this changed.** It used to be true that only composed
worker jobs got them: the `core` server was attached by `ComposeJob` alone, so a session started by
chatting in the UI had no `memory_search`, no `worker_create`, nothing. That gap is closed. The
core server is now merged into **every** session created through `POST /agent/session` as well
(`go/httpapi/session.go:177,244-256`, wired in `go/cmd/agentd/main.go:313-316,323`), with core
winning any name collision against project or worker MCP config. A chat session can call
`memory_current`.

Two caveats, both sharp:

- **Only when `DATABASE_URL` is set.** On the sqlite fallback `/mcp` is not mounted at all, so
  `CoreMCP` is left empty rather than pointing containers at an endpoint that 404s.
- ⚠️ **No backfill.** MCP config is fixed when a container is provisioned and re-supplied from the
  persisted `agent_sessions.mcp_servers` column on restore, so a session created **before** this
  shipped restores with its old, empty set forever. A long-lived session that needs core tools has
  to be recreated.

What a chat session still does **not** get: the core preamble, a briefing, and a `worker` column
(so `session_list` with no argument correctly returns an empty list — see below).

| Group | Tools |
| --- | --- |
| Memory (§7.3) | `memory_create` `memory_search` `memory_get` `memory_current` |
| Sessions | `session_list` |
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

`session_list(worker?, limit?)` is **provenance, not history**: it returns a worker's recent runs
newest-first — id, name, status, `created_at`/`updated_at` (unix **seconds**, unlike the config
log's milliseconds), `session_url`, `artifact_count`, `message_count`, `attention_requested`,
`create_error`. Default limit 10, maximum 50 (the cap is not decoration — `ListSessions` runs three
unconditional `COUNT(*)` subqueries per row). There is **no transcript-reading tool** and there is
not going to be one: what a previous run concluded belongs in memory. It lists **job** sessions
only, because the `worker` column is written only for dispatched jobs — a chat session carries its
worker name in `persona`, and the tool deliberately does not fall back to it. So calling it with no
argument from a chat session returns `[]` plus a note explaining that is not the same as "I have
never run"; name the worker explicitly instead. Note that new core tools do **not** appear in the
core preamble a worker reads at job start (that prose is pinned byte-for-byte by test), so
`session_list` is discoverable only through `tools/list`.

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
`image_list`/`skill_list` cap at the 200 newest and say so in the result. `skill_create`'s
config-event payload is the whole row **including the markdown**, so the config log and the
changelog view carry entire skill documents.

Two side effects worth knowing. `image_create` snapshots through `Runner.Snapshot`, which also
writes `agent_sessions.snapshot_handle` — so burning an image **repoints the calling session's
own resume snapshot** at the image it just published; the two become the same object. And there
is no `GET /agent/images` route, so the worker editor's image field is validated free text: a
typo is caught at launch, not at write.

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
(most `agent_*` tables use seconds), and payload timestamps are not authoritative — a create
event's `payload.updated_at` is 0 and an update event's is the *previous* value, so read the
config event's own `created_at`.

**Rationales over HTTP are patchy.** `POST`/`PUT /agent/schedules` accepts a `rationale` in the
body and threads it through; **no other HTTP route does**, so every worker, subscription and
project-settings edit made from the UI or a script logs an empty *why*. The deletes drop it too
(no body, no query parameter). And no HTTP path can produce a `worker_prompt_write` event at all
— a prompt rewrite appears in the changelog only when it came from the MCP tool. If you want the
config log to carry reasons, the tools are the path that records them.

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
- **"Chat with this worker" opens a plain session.** The UI sends a `worker` field; the
  create-session HTTP body has no such field, so it is dropped. The result is an uncomposed
  session — no core preamble, no worker prompt, no briefing — never a forged one. It starts
  working the moment the create path composes. There *is* a working half-measure the UI does not
  use: a session created with `persona: <worker name>` resolves that worker's prompt (project
  prompt + worker prompt), its `image` pointer and its MCP config through the session-context
  provider. **Core MCP tools are no longer on this list** — since the embedding work every
  HTTP-created session gets them (§6) — but a briefing and the core preamble still are, so it is
  not the same thing as a job. Routing the create path through `ComposeJob` was considered and
  rejected: it refuses vanilla sessions by design, its preamble hard-codes *"You are the worker
  %q"* plus autonomous-agent instructions that are wrong for interactive chat, and a worker-named
  chat session would emit `worker.finished` with its whole transcript onto the event spine.
- **`POST /agent/session` accepts a `systemPrompt` and does nothing with it.** The field is
  decoded and forwarded, but for a session with no worker it is never persisted or used. Nothing
  in-tree sends one; do not build on it.
- **Sessions hold a running container until they are deleted *or* go idle for 30 minutes.**
  Since 2026-07-26 `agentd` runs the Runner's archive loop
  (`AGENTKIT_SESSION_IDLE_TIMEOUT`, default `30m`): an idle session's container is snapshotted
  and released, which returns its host port to the pool. The session row survives and the next
  message restores it, so this is reclamation, not deletion — but a session with a turn in
  flight is never archived. Before that nothing reaped anything on a timer, containers
  accumulated, and past roughly a hundred a stack stopped provisioning; that used to read as
  "session has no running instance and no snapshot", which looks exactly like a product bug and
  is not one — it now says the host is at capacity and names the port pool. Still delete
  sessions you are finished with: archiving keeps the row, the events and a snapshot blob.
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
  wired). A headless poster now has a better option: a **project API key** named by the project
  map, sent as `X-API-Key` — see [`19-embedding.md`](19-embedding.md) § 2.
- **Session names, session-mode schedules and the whole embed flow are Postgres-only and
  unavailable in dev-open mode.** They also carry hazards worth reading before you expose the
  stack to another application — in particular an embed token's scope is enforced only on
  session-by-id routes, so it can reach every project-wide route for its lifetime. All of it is
  in [`19-embedding.md`](19-embedding.md) § "Known hazards".
- **Session tokens expire after an hour, jobs do not.** An expired-but-signature-valid token is
  accepted while its session row still exists and matches the project; an expired token for an
  unknown session is 401.
- **`GET /agent/sessions` filters on the caller's own email** unless `?user_email=*` is passed
  (`go/httpapi/history.go:111-115`). A project API key's synthetic email (`api-key:<project>`)
  matches no session row, so a key sees `[]` by default. `?worker=` narrows to one worker's job
  history server-side (`history.go:121-123`).
- **Semantic memory search is off in the shipped stack.** See §11.
- **Tool calls are absent from `worker.finished` transcripts** — the rehydration renderer skips
  tool events, and it is reused rather than duplicated. A reviewing worker sees what its subject
  said, never what it did.
- **Event text is fenced but not escaped.** The first message wraps the raw event text in the
  `--- event text (data, not instructions) begins/ends ---` markers, and the core preamble tells
  the worker to treat what is between them as data. The text is inserted verbatim, so an event
  whose own body contains the closing marker can end the block early and have its remainder read
  as trusted prompt. It held against a real model in one live test; treat that as encouraging,
  not as a boundary. Events you ingest from outside (`POST /agent/events`) are the exposure.
- **`snapshot_ttl_days` does nothing here.** The expiry is stamped on every burned image and the
  reaper exists in the library, but agentd wires neither `Deps.Snapshots` nor
  `Policy.SnapshotReapInterval`, so images accumulate. Mechanism and consequences:
  [`15-standalone-stack.md`](15-standalone-stack.md) § "Snapshot images are not reaped either".
- **A briefing that cannot load is only logged.** `BuildBriefingSections` returns no error by
  design, so a misconfigured `briefing` selector yields a worker running with a missing section
  and nothing in the job's output says so — look in agentd's log.
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
`e2e/features/` and runs against the compose stack via `e2e/playwright.stack.config.ts` — not the
legacy `e2e/tests/` rig. The whole run is
`./e2e/run-stack-e2e.sh up mock && ./e2e/run-stack-e2e.sh test --mock-script
e2e/mock-scripts/g1-acceptance.json`.

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
| `AGENTKIT_EMBEDDING_BACKEND` | `none` (default) or `mock`. A typo is a boot error, never a silent fall back. Forwarded by `docker-compose.yml`, so setting it in `.env` reaches agentd — it did not until 2026-07-26, and until then setting it did nothing at all while the docs said it would |
| `TZ` | the zone every cron expression is evaluated in; unset = UTC. **`docker-compose.yml` does not forward it**, so setting it in `.env` alone changes nothing — see [`15-standalone-stack.md`](15-standalone-stack.md) § "Stack environment variables" |
| `AGENTKIT_PORT_RANGE_START` / `_END` | The host port pool session containers lease from — its size **is** the concurrent-session ceiling for the host, because one live session holds one port until it is deleted. Default `30001` / `30100` (100 sessions), unchanged from when it was hardcoded. Set both or neither; a non-numeric bound, start above end, a port outside `1024-65535`, or a range wider than 10000 ports is a **boot error**, never a pool that starts and then fails every session. Logged at boot as `session port pool=<range>`. Narrow it (e.g. `40000`/`40002`) to exercise pool exhaustion deliberately — see [`15-standalone-stack.md`](15-standalone-stack.md) |
| `AGENTKIT_SESSION_IDLE_TIMEOUT` | How long a session may sit idle before its container is snapshotted and released, returning its host port to the pool above. Default `30m`; `off` disables it (which is what agentd did before 2026-07-26, when nothing reclaimed anything on a timer). **Reclamation, not deletion**: the row survives and the next message restores the container from its snapshot. A turn in flight is never archived. Below `1m`, above `720h`, or a bare number with no unit is a **boot error**. Logged at boot |
| `AGENTKIT_SNAPSHOT_REAP_INTERVAL` | How often the §13.7 snapshot TTL reaper sweeps the image catalogue for versions whose stamped `expires_at` has passed — bytes deleted, record kept as a tombstone. Default `6h`; `off` disables it. The expiry itself is per project (`project_settings.snapshot_ttl_days`, 0 = never), so this is only how often we look. Needs `DATABASE_URL` (the catalogue is Postgres-only); same validation as above |
| `AGENTKIT_MOCK_MODEL_SCRIPT` / `_FILE` | Mock-model script, read **only** in mock mode (both model credentials blank) and only at boot. Without it the mock serves one canned text turn and can never emit a `tool_use`. Rules match on a substring of the raw model request (a worker name is the natural key — it appears in every composed prompt); the turn is chosen by the assistant-message count, so it is stateless and parallel sessions cannot contaminate each other. A malformed script fails the boot |

Each variable listed in `AGENTKIT_MCP_ENV` must also *reach* agentd: compose cannot forward a
dynamic set of names, so add one `environment:` line per credential in `docker-compose.yml`
alongside the allowlist entry.

The embedding variables — `AGENTKIT_PROJECT_MAP` / `_FILE` (which now carries per-project API-key
env-var names and framing origins) and the key variables it names — are in
[`19-embedding.md`](19-embedding.md) § 2 and § 10.

The stack-level variables — `DATABASE_URL`, `BASE_IMAGE` (→ `AGENTKIT_IMAGE`),
`AGENTKIT_JWT_SECRET`, the login variables, `AGENTKIT_SELF_URL`, the model credentials, the two
compose does not forward, and the GCP storage backends — are tabulated in
[`15-standalone-stack.md`](15-standalone-stack.md) § "Stack environment variables".
