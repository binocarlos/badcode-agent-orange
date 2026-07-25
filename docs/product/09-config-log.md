# Spec — The config log (the time machine)

**Part of the product spec.** Entry point and binding principles: [`17-product-spec.md`](17-product-spec.md).
Event-sourced configuration: every management mutation appends a record, the ordinary tables are
projections of that log, and "restore" is a forward operation. This file carries a **new** section
number — **§15**, added by the 2026-07-25 design walkthrough. Existing section numbers (§) are kept from the original single-file spec, so cross-references
like §7.6 or §8.8 anywhere in the repo still resolve — the entry point has the full map.

---

## 15. The config log

### 15.1 Why — P8, applied to the last mutable thing

**P8 (§1.3): nothing in Agent Orange is ever destructively updated; current state is always a view
over an append-only history.** Most substrates already obeyed it before it was named: memories are
append-only and immutable by construction (§7.1), session transcripts are immutable records of what
happened, images are `name:version` records that are never overwritten
(§13, [`08-images-and-skills.md`](08-images-and-skills.md)), skills are versioned by append (§14).

Configuration was the exception — and configuration is precisely what this system exists to let
workers rewrite. A project whose workers hire other workers, retune schedules, and rewrite each
other's system prompts (§8.7 is the definition of done for the whole spec) generates a *history of
decisions*, and until now that history was being overwritten in place by every `UPDATE`. The
automatic `kind=prompt-revision` memory (§9) was a partial patch — provenance for prompts only, and
only as a side effect. The config log generalises it: **every management mutation appends a
structured record, and the workers/subscriptions/schedules/settings tables become caches of the
fold.**

What this buys, concretely:

- *"Why is the email answerer curt now?"* — the prompt rewrite that did it, its rationale, and a
  link to the session where a worker decided it.
- *"What did the org look like on Tuesday?"* — fold the log to Tuesday (§15.6).
- *"Put the tweet author's prompt back to the version that worked"* — append the old text again,
  keeping the failed experiment in the record (§15.7).
- *"Tell me what we changed this week"* — a worker subscribed to `config.changed`, or querying
  `config_history` on a schedule (§15.8, §15.9).

The log is the organisation's changelog, and it is written by construction rather than by
discipline.

### 15.2 The record

Table `config_events` (migration **026**; work-plan Track J, §11):

| column | type | meaning |
| --- | --- | --- |
| `id` | uuid PK | stable identity — quoted in restore rationales and deep links |
| `project` | text, indexed | hard namespace (P5) — every query filters on it, in code |
| `actor_worker` | text | the worker that made the change; empty for human/API edits |
| `actor_session` | text | the session it was made from; empty for human/API edits — the deep link |
| `action` | text | the mutation performed, from the fixed vocabulary in §15.3 |
| `payload` | jsonb | **the full new state** of the mutated record, not a diff |
| `rationale` | text | commit-message-style *why*; required on prompt writes (§15.5), optional elsewhere |
| `created_at` | bigint | |

Two shape decisions worth stating because they are load-bearing:

**Payload is full state, never a diff.** Folding a log of full states is last-writer-wins per key —
a `map[key]payload` loop with no merge algebra, no rebase semantics, no way for a corrupted
intermediate record to poison everything after it. Prompts are large strings and full copies are
cheap next to the transcripts we already store. Diffs are a *read-time* concern: the changelog UI
(§15.10) computes them between consecutive events for the same key, which is also the only place a
diff is ever wanted.

**Actor is (worker, session), not "user".** Most mutations in a healthy project are made by workers,
and the interesting question is always *which run decided this* — so provenance points at a session
permalink, exactly as memory search results do (§7.3). Human edits through the UI or HTTP API leave
both actor columns empty; the acting human's identity is the login audit's business, not the config
log's (single-trusted-org posture, §10).

### 15.3 What must be logged

Every management mutation appends a record. The vocabulary is closed — this is the complete list:

| `action` | Written by | `payload` (full new state) |
| --- | --- | --- |
| `worker_create` | `worker_create` tool, `PUT /agent/workers/{name}` (new), UI | the whole new worker row |
| `worker_update` | `worker_update` tool, worker PUT, UI editor | the whole worker row after the partial update — including a change of the **image pointer** (§13), which is therefore always a visible act, never a silent side effect of snapshotting |
| `worker_enable` / `worker_disable` | the same paths, `enabled` toggled | the whole worker row |
| `worker_delete` | `DELETE /agent/workers/{name}`, UI | the whole worker row as it last stood (rule 2 — deletes append too) |
| `worker_prompt_write` | `worker_prompt_write` tool, UI prompt editor | the whole worker row (the new `system_prompt` included in full) |
| `project_prompt_write` | `project_prompt_write` tool, UI | the new project system prompt |
| `project_settings_put` | `PUT /agent/project-settings`, UI (§5) | the whole settings row after the write |
| `subscription_create` / `subscription_update` / `subscription_delete` | `subscription_*` tools, `/agent/subscriptions` CRUD, UI (§8.3) | the whole subscription row; for delete, the row as it last stood |
| `schedule_create` / `schedule_update` / `schedule_delete` | `schedule_*` tools, `/agent/schedules` CRUD, UI (§8.6) | the whole schedule row; for delete, the row as it last stood |
| `image_create` | `image_create` tool (§13) | `{name, version, labels, provenance}` of the new image version |
| `skill_create` | `skill_create` tool (§14) | the whole skill record (name, labels, markdown, `install_sh`, provenance) |

Three rules keep this list honest:

1. **The write path logs, not the caller.** MCP tool, HTTP endpoint, and UI all funnel through the
   same store seam (§15.4); there is no path that mutates configuration without a record, and a test
   enumerates the mutation methods to prove it.
2. **Deletes are appends too.** A delete writes an event carrying the final state; the projection
   row disappears, the record does not. This is what makes "restore a deleted schedule" a lookup
   rather than an archaeology project (§15.7).
3. **Only configuration lives here.** Memories are already their own append-only log (§7.1); events
   and deliveries are already their own (`project_events`, `event_deliveries` — §8.4); sessions and
   messages are already immutable. The config log covers the org's *settings*, and duplicating the
   other substrates into it would be noise.

### 15.4 Dual write, in one transaction

The log row and the projection-table update are written **in the same database transaction**. Either
both land or neither does — there is no window in which the workers table says one thing and the log
says another, and no reconciliation job to write.

The log is **authoritative**; `workers`, `subscriptions`, `schedules`, `project_settings`,
`customimages`, and `skills` are projections — caches of the fold, kept because reading the current
value on every job composition (§6.2) must be one indexed lookup, not a replay. Nothing in the
runtime reads the log on the hot path; the log is for history, replay, and audit.

Two consequences of "in the same transaction":

- **Rationale and validation come first.** The existing read-back-validation rule (§9) is unchanged:
  a mutation validates its input, writes both rows, then reads the projection back and echoes it in
  the tool result. Malformed input fails before either write.
- **The `config.changed` event is emitted after commit,** never inside the transaction — a routed
  event must not exist for a change that rolled back. Emission is at-least-once with an idempotency
  guard on the config event id, matching the router's delivery semantics (§8.4), so a crash between
  commit and emit is repaired by a retry rather than by a lost event.

### 15.5 Rationale

**Required** on the two prompt writes: `worker_prompt_write(name, system_prompt, rationale)` and
`project_prompt_write(system_prompt, rationale)` (§9). **Optional** on every other mutation tool
(`worker_create`, `worker_update`, `subscription_*`, `schedule_*`, `image_create`, `skill_create`),
which take `rationale?`.

Prompt rewrites are the self-improvement loop, and the *why* is the one thing that is not
recoverable from the text: a diff shows that "always acknowledge the customer's frustration" was
added; only the rationale says a reviewer found a hundred curt threads and decided it. So the
rationale is stored in the config event **and** echoed in the automatic `kind=prompt-revision`
memory (§9), where it becomes searchable evidence alongside the superseded prompt.

Core validates that the string is non-empty and nothing else — judging whether a rationale is *good*
is exactly the kind of opinion that belongs in a reviewing worker's prompt (P1), not in a
validator.

### 15.6 Point-in-time replay

The project's configuration at any historical instant T is reconstructible by folding `config_events`
from t₀ to T: iterate in `created_at`/`id` order, keep the newest payload per
`(entity kind, entity key)` — worker name, subscription id, schedule id, image `name:version`, skill
name, or the singleton project-settings/project-prompt keys — and treat delete actions as tombstones
that remove the key. The result is exactly the projection tables as they stood at T.

Being honest about the boundary: this replays **configuration**, not the world. Memories written
after T are still there, image blobs referenced by a fold may have been reaped by
`snapshot_ttl_days` (§5, §13), and nothing outside the system (sent emails, published tweets) rewinds.
The time machine answers *"what was this organisation instructed to be, and who decided that"* — for
*"what prompt actually produced this transcript"*, the session row's `composed_prompt` (§6.2)
already pins the answer without any replay at all.

### 15.7 Restore is a forward operation

**Git revert, never git reset.** History is never truncated, rewritten, or compacted — including the
history of undoing things. "Restore the configuration to T" means: fold to T (§15.6), diff against
current, and **append compensating mutations** that carry current state back to the historical one —
ordinary `worker_prompt_write` / `worker_update` / `schedule_create` calls, each with a rationale
naming what is being restored and why.

This is not a purity exercise. A worker studying the org's history must be able to see that a change
was tried, regretted, and reverted — that pattern *is* the evidence a second-layer consultant reads
(§9). A destructive restore would erase precisely the most instructive part of the record and leave
a log implying the failed experiment never happened.

#### Worked example — restoring a worker's prompt to revision N

The marketing manager (§8.8) notices `email-answerer` has grown florid since a rewrite last week and
wants the prompt it had on 2026-07-18.

1. **Find the revision.** The manager calls
   `config_history({entity: "worker:email-answerer", action: "worker_prompt_write", limit: 10})` and
   gets the last ten prompt rewrites, newest first — each with its rationale, its actor worker and
   session permalink, and the full prompt text in `payload` (§15.9).
2. **Pick the target.** Event `ce_41`, 2026-07-18, written by `email-reviewer` from session `s-991`,
   rationale *"shorten replies; three customers called the answers walls of text"*. Its payload
   holds the whole `system_prompt` as it stood after that write.
3. **Append the restore.** The manager calls
   `worker_prompt_write("email-answerer", <ce_41.payload.system_prompt>, rationale: "restore to
   ce_41 (2026-07-18): the 2026-07-22 rewrite regressed tone — reverting while we work out which
   part of it was responsible")`.
4. **What lands, in one transaction:** a **new** config event `ce_57` (`action:
   worker_prompt_write`, `payload` = the full worker row with the restored prompt, actor =
   `marketing-manager` / its session) plus the updated `workers` row. After commit: the automatic
   `kind=prompt-revision` memory holding the *superseded* (florid) prompt and the restore rationale
   (§9), and a routable `config.changed` event (§15.8).
5. **What does not happen:** `ce_42`…`ce_56` are untouched. The regression, the rationale that
   justified it, the complaint that followed, and the revert all remain in the log. Folding to any T
   still reproduces exactly what was live then — including the week the org was wrong.
6. **Timing, as always:** composition happens once at job start (§6.2), so the restore addresses the
   *next* job; a job already running with the florid prompt finishes with it.

The same shape covers the other entities: restoring a deleted schedule is `schedule_create` with the
payload from its `schedule_delete` event; restoring a worker's environment is
`worker_update(name, {image: "research-tools:7"}, rationale: …)` (§13); un-hiring is
`worker_update(name, {enabled: false}, rationale: …)`.

**Bulk restore** ("put the whole project back to Tuesday") is the same operation applied to every
entity that differs, appended as a run of ordinary mutation events. Core ships **no** atomic
`restore_project` verb in v1: a worker with `config_history`, the fold semantics above, and the
existing mutation tools can do it, and inventing a restore engine would be exactly the kind of
policy P1 keeps out of core. If atomicity is ever genuinely needed, it is a transaction wrapped
around existing writes — not a new concept.

### 15.8 The routable `config.changed` event

Alongside the log row, core emits an ordinary project event through the router (§8.4) — the fifth
internal event (§8.2):

```json
{
  "type": "config.changed",
  "text": "marketing-manager rewrote the system prompt of worker 'tweet-author'.\nRationale: the Thursday thread experiment underperformed; going back to single posts.",
  "envelope": { "source": "worker", "worker": "marketing-manager", "session_id": "s-1043", "depth": 2 }
}
```

- **Text** is a human-readable description of the change **including the rationale** — the same
  raw-text payload discipline as every other event (§8.1): a worker's prompt decides how to read it,
  and no structured schema is invented for the sender to depend on. The config event id is quoted in
  the text so a reader can fetch the full payload with `config_history`.
- **Envelope comes from the acting session:** `source: "worker"` with `worker` and `session_id` set
  when a worker made the change (and `depth` = the acting job's depth + 1, so the §8.4 depth floor
  binds normally); `source: "external"`, `depth: 0`, no worker, for human/UI/API edits.
- **Subscribing is ordinary wiring.** A subscription on `config.changed` — optionally filtered on
  envelope fields, e.g. `{"worker": "marketing-manager"}` — wakes any worker on org changes: the
  chronicler that narrates them (§15.10), a reviewer that second-guesses prompt rewrites, a
  notifier. Org-awareness is wiring, not a feature (P2), which is why there is one event type rather
  than a family: finer discrimination belongs in the reacting worker's prompt, consistent with §8.3.
- **Loop hygiene.** A worker that reacts to `config.changed` by changing config emits another one;
  the depth cap (8, §8.4) is the hard floor, and a filter or a "if this was your own change, finish
  immediately" line in the prompt is the ordinary remedy — same posture as every other reactive
  worker.

### 15.9 `config_history` — the read tool

Core MCP, granted to every session alongside the other management tools (§9):

- `config_history(query)` →
  `[{id, action, actor_worker, actor_session, session_url, rationale, payload, created_at}]`,
  **newest first**.
  - `query` is a filter object, all fields optional: `entity` (`worker:email-answerer`,
    `schedule:<id>`, `project-settings`, …), `action` (one of §15.3's vocabulary, or a trailing-`*`
    prefix like `worker_*`), `actor_worker`, `since` / `until` (the range half of "label/range
    query"), and `limit`.
  - Filtering is equality plus the time range — deliberately the same austerity as subscription
    filters (§8.3) and label selectors (§7.2). Anything smarter is the calling worker's prompt
    reading the results.
  - `session_url` is the permalink to the acting session, exactly as memory search results carry it
    (§7.3): the answer to "who decided this" is always one click from the conversation where it was
    decided.
- **Read-only.** No tool writes `config_events` directly — records appear only as the shadow of a
  real mutation (§15.4). Project scope comes from the session token, so a session physically cannot
  read another project's history (P5).

The canonical uses are exactly the ones the walkthrough motivated: *"the last ten prompt rewrites for
worker X, with their rationales"* (the restore flow of §15.7), and *"everything that changed this
week"* (the chronicler, below).

### 15.10 Where it surfaces

- **Changelog UI** (work-plan Track F / item J4, §11): the config log rendered chronologically —
  prompt rewrites with diffs computed between consecutive events for the same key, rationales shown
  as the commit messages they are, workers hired and disabled, schedules retuned, images published
  — every entry deep-linking to the acting session so a human lands in the conversation that
  decided it.
- **Chronicler reference prompt** ([`07-reference-prompts.md`](07-reference-prompts.md)): a worker
  subscribed to `config.changed` (or run on a weekly schedule) that queries `config_history` and
  narrates what the organisation decided and why, delivering the digest via
  `request_human_attention` (§9) or a `name=org-digest` memory (§7.1). Optional, like every
  reference prompt — the log exists whether or not anyone reads it.

### 15.11 This is not a versioned prompt store (P4 holds)

P4 forbids prompt fragments, templates, and a versioned prompt store; the config log is none of
those. Writes remain **wholesale string replacement of the one live prompt** (§9): there is no
branch, no draft, no staging, no "activate version 3" API, and nothing in the composition path (§6.2)
consults the log. What the log adds is *provenance* — the same argument the `kind=prompt-revision`
memory already made, generalised to every configuration row and made transactional instead of
incidental. A history you can read is not a versioning feature; a history you can *deploy from*
would be, and that is deliberately absent.
