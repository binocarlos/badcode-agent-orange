# Fold the design-walkthrough amendments into the product spec — Design & Implementation Plan

> **EXECUTION RULES (for agents):** Work ONE ticket at a time, in order unless dependencies say
> otherwise. Only the orchestrator changes ticket Status; workers may only append to Notes and
> the Discovered Issues Log. A ticket's checkbox is checked only after its Validation commands
> have been re-run by the orchestrator and pass. Do not expand scope; log surprises in the
> Discovered Issues Log instead.

Status: done (executed 2026-07-25; approved same day by Kai — decisions made in the
design-walkthrough conversation; Kai pre-authorized execution)
Relates: `17-product-spec.md` + `01`–`07` (the spec being edited),
`2026-07-25-fold-landscape-learnings.md` (the previous fold — one of its additions is
partially reverted here, see D4).

## Context — the walkthrough decision record (authoritative, in detail)

On 2026-07-25 Kai walked the whole product design chapter by chapter and made the following
decisions. This section is the complete record; tickets implement it. **This is a docs-only
plan** — no code. All edits land in `docs/product/`; § numbers of existing sections never
change; new content gets NEW section numbers (§13–§15) in NEW files.

### Amendment 1 — Environments: named images, skills, worker instances

**Rejected on the way here (record in §10):** a "durable workshop" design — one long-lived
container per worker, kept warm by a TTL scheduler ("kernel"), snapshotted on eviction, with
ambient filesystem accumulation. Rejected because ambient state creates filesystem contention
between concurrent jobs and unauditable drift. The adopted design makes environment continuity
**deliberate**: nothing persists unless an agent explicitly snapshots it, with labels saying why.

**D1 — Images become named, versioned, labeled, append-only records** (the same grammar as
memories):
- Identity: `name:version`. `image_create(name, labels)` — callable from inside any session —
  snapshots the session's **current environment** as a new version under `name` (monotonic
  integer versions). Versions are never overwritten or deleted by tools (append-only; the
  existing `snapshot_ttl_days` reaper from the previous fold still governs storage GC).
- Resolution: a reference `name` (bare) resolves to the **latest** version at launch time;
  `name:version` pins. This lets prompts and workers reference stable names while curation
  publishes improving versions; pinning is available when stability matters.
- Records carry: name, version, labels (flat map, same limits as memory labels), provenance
  (`created_by_worker`, `created_by_session`), `created_at`.
- Tools: `image_create(name, labels)` → `{name, version}`; `image_list(label_selector?)` →
  `[{name, version, labels, created_by_worker, created_by_session, created_at}]` (newest
  first; selector optional).
- Engine mapping (cite, don't respec): `Snapshot()`/`Persist()` exist; `agentdb` already has a
  `customimages` table; the launch priority chain `Image > CustomImageID > Policy.BaseImage`
  (`runner.go:resolveLaunchImage`) gains the worker pointer at the front.

**D2 — Workers get an explicit image pointer.** New worker column `image` (text, optional):
`name` (floating → latest) or `name:version` (pinned). Composition step 1 (§6.2) becomes:
**worker.image (resolved) > project base_image > global default**. Image adoption is a
visible act (a config event per Amendment 2), never an automatic side effect of snapshotting.

**D2b — A tool must exist to make that visible act (and to edit the other new worker
config).** Add **`worker_update(name, fields, rationale?)`** to the §9 surface: partial
update of the non-prompt worker fields (`description`, `image`, `max_instances`, `briefing`,
`enabled`) — the prompt stays exclusively behind `worker_prompt_write` (its revision-memory
semantics are special). `worker_create` gains the same optional fields
(`image?`, `max_instances?`, `briefing?`). Both are config-evented mutations (D5).

**D3 — Skills: portable capability = knowledge + its install.** A skill is a project-scoped,
labeled, append-only record: `{name, labels, markdown, install_sh, provenance, created_at}`.
The markdown is a Claude-Code-style skill document; `install_sh` installs its software
dependencies. Tools: `skill_create(name, labels, markdown, install_sh?)`,
`skill_list(label_selector?)`, `skill_get(name)`, and **`skill_install(name)`** — inside a
session, writes the markdown into the harness's skills directory and runs `install_sh`.
Two sanctioned workflows, both on this one primitive: (a) install skills live in a session as
needed; (b) start from a vanilla image, `skill_install` a set, then `image_create` to burn a
curated environment. "Hoisting" — a worker promoting something it learned into a project skill
— is just `skill_create` with good labels. Engine mapping: `agentdb` already has a `skills`
table. Prompt-driven loading (P1): *which* skills a worker installs is its prompt's business;
there is no `skills` column on workers.

**D4 — Worker parallelism is worker config.** New worker column `max_instances` (int, default
**1**): the maximum number of simultaneously active jobs (harness instances) for this worker.
Enforced **uniformly at dispatch** by both router and scheduler: any delivery — event-matched
or schedule-fired — for a worker at capacity queues as `pending` and dispatches FIFO when an
instance frees (§8.6 skip-missed semantics apply only to firings missed while agentd was
*down*, unchanged). Within one instance, a single Claude thread is sequential by nature.
**This supersedes the per-subscription `concurrency` column added in the previous fold
(2026-07-25): that column is REMOVED from §8.3** (nothing is built yet; worker-level
serialization is the blunter, better tool; `max_firings_per_hour` stays). With `drop` mode
gone, the `dropped` delivery status has no producer: **remove `dropped` from the
`event_deliveries` status vocabulary** everywhere it appears (§8.4 and work-plan E1). Record
the removal + reasoning where §8.3 defines subscriptions. The §12 verification line
mentioning serialize/drop is updated to cover `max_instances` gating instead.

### Amendment 2 — The time machine: event-sourced configuration

**Unifying principle (new P8, added to §1.3):** *Nothing in Agent Orange is ever destructively
updated. Current state is always a view over an append-only history.* Memories (append-only by
construction), sessions (immutable transcripts), images (`name:version`), skills (versioned by
append), and — closing the loop — **configuration** itself.

- **D5 — `config_events` log.** Every management mutation — `worker_create` /
  worker update/enable/disable, `worker_prompt_write`, `project_prompt_write`, project-settings
  PUT, subscription CRUD, schedule CRUD, `image_create`, worker image-pointer change,
  `skill_create` — appends a structured record: `{id, project, actor (worker/session or
  human), action, payload (full new state, jsonb), rationale, created_at}`. Written **in the
  same transaction** as the projection-table update. The log is authoritative; the ordinary
  tables (workers, subscriptions, schedules, …) are projections/caches of the fold.
- **D6 — Point-in-time replay.** The org's configuration at any historical instant T is
  reconstructible by folding `config_events` from t₀ to T. **Restore is a forward operation**:
  "restore to T" appends compensating events (git revert, never git reset) — history is never
  truncated, including the history of undoing things.
- **D7 — `rationale` is required on prompt rewrites.** `worker_prompt_write(name,
  system_prompt, rationale)` and `project_prompt_write(system_prompt, rationale)` — the
  commit-message-style *why*, stored in the config event and echoed in the auto
  `kind=prompt-revision` memory. Other mutation tools accept an optional `rationale`.
- **D8 — Routable `config.changed` event.** Alongside the log row, core emits a normal
  project event `{type: "config.changed", text: human-readable description including the
  rationale}` through the router (envelope from the acting session — source `worker` when a
  worker did it; `external` for human/API edits). Workers can subscribe: org-awareness is
  wiring, not a feature. (§8.2's internal-event list grows by one; this plan is the required
  design conversation.)
- **D9 — `config_history(label/range query)` core read tool** so workers (chroniclers,
  consultants) can query the log — e.g. "the last 10 prompt rewrites for worker X, with
  rationales".
- **D10 — Changelog UI** (Track F): chronological config events — prompt diffs, rationales,
  workers hired, schedules changed, images published — each deep-linking to the acting
  session. **D11 — Chronicler reference prompt** (spec 07): a worker subscribed to
  `config.changed` (or on a weekly schedule) that narrates what the organization decided and
  why, and delivers the digest (e.g. via `request_human_attention` or a `name=org-digest`
  memory).

### Amendment 3 — Memory ergonomics: named memories, briefing selectors

- **D12 — `name=` label convention, singleton semantics.** "The current value of
  `customer-greeting`" = newest memory matching `name=customer-greeting`. Append-only + newest
  first IS a KV store (LSM/compacted-topic/git-ref precedent); same data serves the archive
  lens (full history of the value). Document in the memory spec + the label-registry
  convention.
- **D13 — `memory_current(name)` core tool.** Sugar for `memory_search("name=<name>",
  limit=1)` with `memory_get` semantics (full content). Makes the KV read one obvious word in
  a prompt.
- **D14 — Briefing selectors.** New worker column `briefing` (list of label selectors,
  optional). At composition, the newest match of **each** selector is injected as its own
  headed section (each byte-capped like the rolling summary). The existing rolling-summary
  injection becomes the built-in default selector (`kind=rolling-summary, worker=<name>`) —
  behaviour preserved when `briefing` is unset. Editable via worker tools (and therefore
  config-evented). Prompts refer to briefing content in plain English.
- **D15 — In-prompt interpolation REJECTED, P4 stands.** No mustache/`{{placeholders}}` in
  prompts, reaffirmed with the named-memory use case considered: templates would make prompts
  unreadable without resolution and break wholesale-rewrite self-improvement. Record in §10.
  The sanctioned routes are runtime lookup (`memory_current`) and briefing selectors.

### Also decided
- **D16 — Work plan becomes an executor-ready backlog** for parallel Opus coding agents:
  execution-rules preamble, per-item validation commands, Model hints, checkbox discipline
  (see T10).

## Architecture

New content gets new § numbers in new files (existing §s untouched): **§13 Images** and
**§14 Skills** in new `08-images-and-skills.md`; **§15 The config log** in new
`09-config-log.md`. Principle P8 lands in `17-product-spec.md` §1.3. All shared names below
must be used verbatim everywhere:

- Worker columns (new): `image` text (empty | `name` | `name:version`), `max_instances` int
  default 1, `briefing` jsonb (list of label-selector strings, default null).
- Subscriptions: REMOVE `concurrency`; KEEP `max_firings_per_hour`.
- Tables (new): `config_events {id, project, actor_worker, actor_session, action, payload,
  rationale, created_at}`; images/skills reuse existing `customimages`/`skills` agentdb tables
  (extended with labels/provenance as needed — work-plan item, not respecced here).
- Tools (new): `image_create(name, labels)`, `image_list(label_selector?)`, `skill_create(name,
  labels, markdown, install_sh?)`, `skill_list(label_selector?)`, `skill_get(name)`,
  `skill_install(name)`, `memory_current(name)`, `config_history(query)`,
  `worker_update(name, fields, rationale?)` (fields ⊆ {description, image, max_instances,
  briefing, enabled}).
- Tool signatures (changed): `worker_prompt_write(name, system_prompt, rationale)`,
  `project_prompt_write(system_prompt, rationale)`, `worker_create(name, description,
  system_prompt, mcp_config?, image?, max_instances?, briefing?)`.
- Status vocabulary (changed): `event_deliveries.status` loses `dropped` → 
  `pending|running|ok|failed|awaiting_human|rate_limited`.
- Migration numbers (minted here): **025** = images/skills store changes (Track I1),
  **026** = `config_events` (Track J1). Existing numbers 019–024 unchanged.
- Event (new): `config.changed` (routable, text payload; envelope from the acting session).
- Composition step 1: `worker.image` (resolved) > `project_settings.base_image` > global.
- Composition step 2.4 becomes: briefing sections — default rolling-summary selector plus
  `worker.briefing` selectors, each newest-match, each byte-capped.

Rejected alternatives that matter (record in §10 via T1): the durable-workshop/TTL-kernel
design (ambient state, contention, drift); in-prompt interpolation (P4); per-subscription
`concurrency` (superseded by `max_instances`).

## File Structure

| Action | File | Purpose |
| --- | --- | --- |
| Modify | `docs/product/17-product-spec.md` | P8; atoms/vocabulary updates; §10 additions; § map rows for 08/09 (T1) |
| Modify | `docs/product/01-session-config.md` | image-resolution precedence note in §5 (T2) |
| Modify | `docs/product/02-workers.md` | worker columns; composition steps 1 & 2.4; preamble sentence (T3) |
| Modify | `docs/product/03-memory.md` | `name=` convention; `memory_current`; briefing-selectors cross-ref (T4) |
| Create | `docs/product/08-images-and-skills.md` | §13 Images, §14 Skills (T5) |
| Create | `docs/product/09-config-log.md` | §15 The config log / time machine (T6) |
| Modify | `docs/product/04-events-and-schedules.md` | remove `concurrency`; `max_instances` routing; `config.changed` (T7) |
| Modify | `docs/product/05-management-tools.md` | new tools; rationale params; mutation-eventing statement (T8) |
| Modify | `docs/product/07-reference-prompts.md` | chronicler prompt; named-memory + curation patterns (T9) |
| Modify | `docs/product/06-work-plan.md` | new tracks I/J; item updates; executor-ready upgrade (T10) |
| Modify | `docs/product/00-overview.md` | four substrates; updated diagram/tables (T11) |

## Interfaces

The shared names in **Architecture** above, used identically across all tickets. T12 verifies.

## Out of Scope

- Any code/migrations/tests (docs only; implementation follows the work plan).
- Renumbering or editing the meaning of any existing § section.
- The durable-workshop/TTL design in any form; warm-container reuse as a semantic feature
  (an *invisible* warm-reuse optimization may be noted as engine-internal future work only).
- Re-adding approval machinery, per-subscription concurrency, or prompt templates.
- Changing engine docs `../01`–`../15`.

## Tickets

### T1: 17-product-spec — P8, atoms, vocabulary, §10, § map   [Status: pending | Model: opus]
- **Scope:** (a) §1.3: add **P8 — Append-only everywhere**: nothing is destructively updated;
  current state is a view over append-only history (memories, sessions, images, skills,
  config); restores are forward operations. (b) §1.1 atom 3 reword: images are *named,
  versioned, labeled* records agents can create from inside sessions (pointer to §13).
  (c) §3 vocabulary: update **Job** (image comes from the worker's pointer, else project
  default); add **Image** (`name:version`, §13), **Skill** (markdown + install, §14),
  **Config event** (§15). (d) §10 new bullets: no ambient durable workshops (the rejected
  TTL-kernel design, one line of reasoning); no in-prompt interpolation even for named
  memories (P4 reaffirmed; sanctioned routes = `memory_current` + briefing selectors).
  (e) § map: add rows for `08-images-and-skills.md` (§13–§14) and `09-config-log.md` (§15);
  update the 02-workers row's Covers cell to mention image pointer/max_instances/briefing;
  update the 07 row's Covers cell role list to include the chronicler.
  (f) §1.2 ("exactly three things"): amend honestly — the three quantities remain where all
  *opinion* lives (prompt, tools, memory); a worker row now also carries small plumbing
  config (image pointer §13, `max_instances`, `briefing` selectors §7.4) which holds no
  opinion, only wiring.
- **Files:** `docs/product/17-product-spec.md`.
- **Acceptance criteria:** P8 present in §1.3; four vocabulary entries touched/added; both
  §10 bullets name their rejected design; both § map rows link correctly; §1.2 no longer
  claims the row holds exactly three fields while still locating all opinion in the three.
- **TDD:** no
- **Validation:** `grep -n 'P8' docs/product/17-product-spec.md` → ≥1; `grep -c '08-images-and-skills\|09-config-log' docs/product/17-product-spec.md` → ≥2; `grep -n 'interpolation' docs/product/17-product-spec.md` → ≥1.
- **Depends on:** —
- [x] done
- Notes: 2026-07-25 executed; orchestrator re-ran validations (1, 5, 1; no headings removed). §1.2 reframed as "every *opinion* reduces to three things" + plumbing note. Links to 08/09 dangling until T5/T6 (expected).

### T2: 01-session-config — image precedence   [Status: pending | Model: sonnet]
- **Scope:** (a) In §5's agentd-wiring bullet (precedence rules), update image precedence to
  `worker.image (resolved per §13: bare name → latest, name:version → pinned) >
  project_settings.base_image > global Policy.BaseImage`, cross-referencing §13 and noting the
  engine seam (`runner.go:resolveLaunchImage`). (b) Update the `briefing_max_bytes` table
  row's meaning: byte cap **per injected briefing section** (the rolling summary and each
  `briefing` selector's section — §7.4). Touch nothing else.
- **Files:** `docs/product/01-session-config.md`.
- **Acceptance criteria:** precedence chain updated exactly once with §13 cross-ref;
  `briefing_max_bytes` meaning says per-section.
- **TDD:** no
- **Validation:** `grep -n 'worker.image\|§13' docs/product/01-session-config.md` → ≥2.
- **Depends on:** —
- [x] done
- Notes: 2026-07-25 executed; orchestrator re-ran validations (2 hits; briefing_max_bytes row says per-section).

### T3: 02-workers — columns, composition, preamble   [Status: pending | Model: sonnet]
- **Scope:** (a) §6.1 table: add `image` (text: '' | `name` | `name:version` — §13),
  `max_instances` (int, default 1 — the max simultaneously active jobs for this worker;
  enforced uniformly at dispatch by router *and* scheduler, §8.4), `briefing` (jsonb list of
  label selectors — §7.4). Amend the "No model tier, no budget…" paragraph so it doesn't
  contradict the new columns — including, explicitly, its "if it needs special software, that
  is the project base image" sentence, which becomes: special software = install it and
  snapshot a named image (§13), or bake it into the project base image.
  (b) §6.2 step 1: image = worker pointer resolved, else project, else global. §6.2 step 2.4:
  briefing sections — the default rolling-summary selector plus each `briefing` selector,
  newest match each, injected as its own headed section, each byte-capped
  (`briefing_max_bytes`). (c) §6.3 preamble: add one sentence telling workers about their
  environment powers, verbatim: *"You can save your current environment as a named image with
  `image_create`, install project skills with `skill_install`, and read the current value of a
  named memory with `memory_current`."* — and bump the word budget "~200 words" → "~250
  words". (d) §6.5: worker editor UI bullet gains image picker + max_instances + briefing
  list.
- **Files:** `docs/product/02-workers.md`.
- **Acceptance criteria:** three new columns with the Architecture defaults; both composition
  steps updated; preamble sentence verbatim; no contradiction left in §6.1 prose.
- **TDD:** no
- **Validation:** `grep -c 'max_instances\|briefing' docs/product/02-workers.md` → ≥4; `grep -n 'image_create' docs/product/02-workers.md` → ≥1 (preamble).
- **Depends on:** —
- [x] done
- Notes: 2026-07-25 executed; orchestrator re-ran validations (7 and 1). Preamble sentence verbatim; special-software sentence amended.

### T4: 03-memory — named memories, memory_current, briefing cross-ref   [Status: pending | Model: sonnet]
- **Scope:** (a) In §7.1 or §7.2 (wherever labels are specified — keep it one place): document
  the `name=` convention: singleton semantics, current value = newest match; append-only +
  newest-first is the KV store (one line of the LSM/git-ref analogy); the archive lens is the
  same selector without limit. (b) §7.3: add `memory_current(name)` to the tool surface —
  sugar for `memory_search("name=<name>", limit=1)` with full-content return; the tool list
  sentence ("create, search, get") becomes "create, search, get, current". (c) §7.4: note the
  generalization — rolling summary is the *default* briefing selector; workers may add more
  via their `briefing` column (§6.2); each section independently byte-capped — and REWORD the
  two sentences claiming "core runs one fixed query" / "the *only* memory read core ever
  performs": the only memory reads core performs are the briefing lookups — one fixed
  newest-match query per selector, nothing else, ever. (d) One line:
  in-prompt interpolation of memories is rejected (P4, §10).
- **Files:** `docs/product/03-memory.md`.
- **Acceptance criteria:** convention documented once; tool added; §7.4 generalization
  consistent with T3's §6.2 wording; rejection line present.
- **TDD:** no
- **Validation:** `grep -c 'memory_current' docs/product/03-memory.md` → ≥2; `grep -n 'name=' docs/product/03-memory.md` → ≥2.
- **Depends on:** —
- [x] done
- Notes: 2026-07-25 executed; orchestrator re-ran validations (2 and 5). Both "only memory read" sentences reworded to per-selector briefing lookups.

### T5: NEW 08-images-and-skills.md — §13, §14   [Status: pending | Model: opus]
- **Scope:** Create the file, header style matching siblings (entry-point link; note it
  carries NEW § numbers 13–14). **§13 Images**: the D1/D2 design in full — record shape,
  `name:version` semantics, latest-vs-pinned resolution, append-only versions, provenance,
  `image_create`/`image_list` tool contracts, worker pointer + composition precedence,
  engine mapping (customimages table, Snapshot/Persist, resolveLaunchImage), interaction with
  `snapshot_ttl_days` GC, the two curation workflows, and a short "why deliberate snapshots
  beat ambient workshops" paragraph (the rejected design, cross-ref §10). **§14 Skills**: the
  D3 design in full — record shape (markdown + `install_sh`), tool contracts incl.
  `skill_install` mechanics (markdown → harness skills dir; run install script in-session),
  hoisting, the vanilla-image+skills→burn workflow, engine mapping (skills table), and the
  explicit P1 note that skill *selection* is prompt policy (no skills column on workers).
- **Files:** create `docs/product/08-images-and-skills.md`.
- **Acceptance criteria:** every D1–D3 element present; tool signatures match Architecture
  verbatim; no invented features beyond the Context record.
- **TDD:** no
- **Validation:** `test -f docs/product/08-images-and-skills.md`; `grep -c 'image_create\|skill_install' docs/product/08-images-and-skills.md` → ≥4; `grep -c '^## 13\|^## 14' docs/product/08-images-and-skills.md` → 2.
- **Depends on:** —
- [x] done
- Notes: 2026-07-25 executed (231 lines, §13.1–13.9 + §14.1–14.6); orchestrator re-ran validations (OK, 12, 2 with anchored grep — original pattern substring-matched subsections). skill_install correctly appends no config event (session mutation, not config).

### T6: NEW 09-config-log.md — §15   [Status: pending | Model: opus]
- **Scope:** Create the file (sibling header style; NEW § number 15). **§15 The config log**:
  the D5–D9 design in full — P8 restated as the motivation; the `config_events` record shape;
  the mutation list that MUST log (enumerate all from D5); dual-write-in-transaction with
  tables as projections; point-in-time replay semantics; restore-as-forward-operation
  (compensating events, git-revert-never-git-reset, one worked example: restoring a worker's
  prompt to revision N); the required-`rationale` rule for prompt writes / optional elsewhere;
  the routable `config.changed` event (text shape, envelope semantics, subscribe-ability);
  the `config_history` read tool contract; and a pointer to the changelog UI (Track F) and
  chronicler reference prompt (07).
- **Files:** create `docs/product/09-config-log.md`.
- **Acceptance criteria:** all D5–D9 elements present; the mutation enumeration matches D5
  exactly; restore semantics unambiguous.
- **TDD:** no
- **Validation:** `test -f docs/product/09-config-log.md`; `grep -c 'config_events\|config_history\|config.changed' docs/product/09-config-log.md` → ≥5; `grep -n 'compensating' docs/product/09-config-log.md` → ≥1.
- **Depends on:** —
- [x] done
- Notes: 2026-07-25 executed (§15.1–15.11 incl. restore worked example, all 11 D5 mutations enumerated); orchestrator re-ran validations (OK, 17, 1).

### T7: 04-events-and-schedules — subscriptions, router, config.changed   [Status: pending | Model: sonnet]
- **Scope:** (a) §8.3: REMOVE the `concurrency` table row; add one line where it was: removed
  2026-07-25, superseded by worker-level `max_instances` (§6.1) — deliveries for a worker at
  capacity queue as `pending`. Keep `max_firings_per_hour` untouched. (b) §8.4: add a numbered
  router point: per-worker instance gating (dispatch only while active jobs for the worker <
  `max_instances`, default 1; excess deliveries stay `pending`, dispatched FIFO as instances
  free; applies uniformly to router and scheduler dispatch — schedule firings queue like any
  delivery, and §8.6 skip-missed remains only about firings missed while agentd was down).
  Also in §8.4 point 2: remove `dropped` from the `event_deliveries` status vocabulary (its
  only producer was the removed `drop` mode) → `pending|running|ok|failed|awaiting_human|
  rate_limited`. (c) §8.2: add `config.changed` to the internal-events list (text = human-readable
  description incl. rationale; envelope from the acting session — source `worker` for
  worker-initiated, `external` for human/API; cross-ref §15), updating the intro count
  (four → five) with a note that this plan was the design conversation. Do not touch
  §8.7/§8.8.
- **Files:** `docs/product/04-events-and-schedules.md`.
- **Acceptance criteria:** `concurrency` row gone with the superseded note; router gating
  consistent with T3's `max_instances` wording; event list says five; `dropped` absent from
  the status vocabulary.
- **TDD:** no
- **Validation:** `grep -c 'max_instances' docs/product/04-events-and-schedules.md` → ≥2; `grep -n 'config.changed' docs/product/04-events-and-schedules.md` → ≥1; `grep -n '| \`concurrency\` |' docs/product/04-events-and-schedules.md` → 0 hits; `grep -n 'dropped' docs/product/04-events-and-schedules.md` → 0 hits.
- **Depends on:** —
- [x] done
- Notes: 2026-07-25 executed; orchestrator re-ran validations (2, 2, 0, 0). §8.7/§8.8 md5-identical to HEAD; five internal events; gating uniform router+scheduler.

### T8: 05-management-tools — new tools, rationale, mutation eventing   [Status: pending | Model: sonnet]
- **Scope:** (a) Update the §9 tool list: `worker_prompt_write(name, system_prompt,
  rationale)` / `project_prompt_write(system_prompt, rationale)` (rationale required; stored
  in the config event and echoed in the prompt-revision memory); extend `worker_create` with
  optional `image?`, `max_instances?`, `briefing?`; add **`worker_update(name, fields,
  rationale?)`** (partial update of `description`/`image`/`max_instances`/`briefing`/`enabled`
  — the prompt stays exclusively behind `worker_prompt_write`); add `memory_current(name)`
  (§7.3), `image_create`/`image_list` (§13), `skill_create`/`skill_list`/`skill_get`/
  `skill_install` (§14), `config_history` (§15). (b) Extend the read-back-validation
  paragraph's enumeration to cover the new mutation tools (`worker_update`, `image_create`,
  `skill_create`). (c) Add one paragraph after it: **every mutation tool appends a
  `config_events` record in-transaction and emits a routable `config.changed` event (§15)**;
  rationale optional on non-prompt mutations. Keep the "No approval gate" paragraph verbatim.
- **Files:** `docs/product/05-management-tools.md`.
- **Acceptance criteria:** all new tools listed with § cross-refs incl. `worker_update`; both
  prompt-write signatures carry `rationale`; read-back enumeration extended; eventing
  paragraph present; "No approval gate" untouched.
- **TDD:** no
- **Validation:** `grep -c 'rationale' docs/product/05-management-tools.md` → ≥3; `grep -c 'skill_install\|image_create\|config_history\|memory_current\|worker_update' docs/product/05-management-tools.md` → ≥5; `grep -c 'No approval gate' docs/product/05-management-tools.md` → 1.
- **Depends on:** —
- [x] done
- Notes: 2026-07-25 executed; orchestrator re-ran validations (6, 7, 1). worker_update added; read-back enumeration extended; "No approval gate" byte-identical.

### T9: 07-reference-prompts — chronicler + curation patterns   [Status: pending | Model: opus]
- **Scope:** Add (keeping the file's optional-patterns framing): (a) **Chronicler** reference
  prompt: subscribed to `config.changed` and/or weekly schedule; queries `config_history`;
  narrates what the org decided and why (prompt rewrites with rationales, hires, schedule
  changes); delivers via `request_human_attention` or a `name=org-digest` memory; silence
  rules consistent with the failure-notifier's. (b) A **named-memory pattern** paragraph in
  the archivist section: `name=` singletons via `memory_current`, updating by appending.
  (c) An **environment-curation pattern** in the manager section: vanilla image →
  `skill_install` set → `image_create(name, labels)` → `worker_update` workers to `name`
  (floating) or pin `name:version`; record the why in the image labels and a memory. Update
  existing prompt bodies only where a new tool name makes an existing instruction simpler
  (e.g. archivist rolling-summary write may mention the briefing generalization). (d) Update
  the file's header/scope line so the role list includes the chronicler.
- **Files:** `docs/product/07-reference-prompts.md`.
- **Acceptance criteria:** chronicler is copy-paste-able and cites §15 tools; both patterns
  present; framing ("optional, never enforced") intact; header role list updated.
- **TDD:** no
- **Validation:** `grep -c 'Chronicler\|chronicler' docs/product/07-reference-prompts.md` → ≥2; `grep -c 'memory_current\|image_create' docs/product/07-reference-prompts.md` → ≥3.
- **Depends on:** —
- [x] done
- Notes: 2026-07-25 executed (chronicler prompt added as role 5; named-memory + environment-curation patterns; header role list updated); orchestrator re-ran validations (8, 5, no headings removed). Cross-refs to 08/09 resolve (T5/T6 landed first).

### T10: 06-work-plan — new tracks + executor-ready upgrade   [Status: pending | Model: opus]
- **Scope:** (a) New **Track I — Images & skills** `[engine+api]` `[walkthrough]`: I1 labels +
  name:version on `customimages` + store methods + resolution (latest/pinned) — **migration
  025**; I2
  `image_create`/`image_list` MCP tools (in-session snapshot path via Snapshot/Persist); I3
  `skills` store (markdown + install_sh + labels) + `skill_*` tools + `skill_install`
  in-session mechanics (harness skills dir + script execution); I4 worker `image` pointer in
  composition/resolveLaunchImage + e2e (curate → burn → relaunch-from-name). (b) New
  **Track J — Config log** `[engine+api]` `[walkthrough]`: J1 `config_events` table (**migration
  026**) + dual-write seam used by every mutation path + `rationale` params; J2 replay/fold +
  restore-as-forward compensating events; J3 `config.changed` emission + `config_history` +
  `worker_update` tools; J4 changelog UI (may fold into Track F). (c) Amend existing items
  `[walkthrough]`-tagged: C1 worker columns
  (`image`, `max_instances`, `briefing` — same migration 021); C2 composition step 1 + briefing
  sections; C4 becomes multi-selector briefing injection; E1 REMOVE the subscription
  `concurrency` column from its migration line (keep `max_firings_per_hour`) and remove
  `dropped` from its status vocabulary; E3 replace serialize/drop handling with per-worker
  `max_instances` gating (uniform for router and scheduler); E4/D3 add the new tools;
  F1 add changelog view (or reference J4); §12's "serialize/drop" verification line becomes
  "max_instances gating". Also update the wave guidance so I1 and J1 join an early wave.
  (d) **Executor-ready upgrade (D16)**: add at the
  top of §11 an EXECUTION RULES block for parallel coding agents (work one item at a time;
  respect `— depends`; check a box only when the item's validation passes; items without
  explicit validation inherit `go build ./... && go vet ./... && go test ./...`; default
  Model: opus; log surprises in a Discovered Issues Log section added at the file's end);
  ensure every item (old and new) names its files; add per-item `Validation:` lines where
  missing (concrete `go test ./agentdb/... -run TestX -count=1`-style commands are ideal;
  the inherited default is acceptable for wiring items). Do not renumber existing items.
- **Files:** `docs/product/06-work-plan.md`.
- **Acceptance criteria:** Tracks I and J exist with dependencies; all listed amendments
  applied; EXECUTION RULES + Discovered Issues Log present; no existing item renumbered;
  wave guidance updated to include I1/J1 in an early wave.
- **TDD:** no (the *items* mandate tests; this ticket edits docs)
- **Validation:** `grep -c '\[walkthrough\]' docs/product/06-work-plan.md` → ≥10; `grep -c 'Track I\|Track J' docs/product/06-work-plan.md` → ≥2; `grep -c 'EXECUTION RULES' docs/product/06-work-plan.md` → 1; `grep -n 'concurrency' docs/product/06-work-plan.md` → **per-subscription** concurrency only in removal-note context; per-**project** concurrency-cap mentions (E3, H1) remain untouched; `grep -n 'serialize\|dropped' docs/product/06-work-plan.md` → 0 hits outside removal notes.
- **Depends on:** T3, T5, T6, T7, T8
- [x] done
- Notes: 2026-07-25 executed; orchestrator re-ran validations (25 tags, tracks I/J present, EXECUTION RULES ×1, serialize/dropped only in E1 removal note, no headings removed, no renumbering, all 37 items carry exactly one Validation line). Judgment call accepted: worker_update built in E4, config-wired in J3.

### T11: 00-overview refresh   [Status: pending | Model: opus]
- **Scope:** Update the overview to the post-walkthrough design: (a) file table gains 08/09;
  (b) the shape diagram: worker box gains `image` pointer + `max_instances`; the memory line
  becomes the four substrates (memories · images · skills · config log) with "append-only,
  labeled, provenance" as the shared banner; add the config-log/time-machine line; (c) "What's
  decided" gains P8 and the walkthrough decisions (deliberate environments over ambient
  workshops; named memories; rationale + changelog); (d) build-waves table mentions tracks
  I/J; (e) the "one folder" table row count stays accurate; (f) anywhere the page lists the
  reference-prompt roles ("archivist, consultant, manager, failure notifier"), add the
  chronicler.
- **Files:** `docs/product/00-overview.md`.
- **Acceptance criteria:** diagram + tables reflect every amendment; links to 08/09 resolve;
  page still reads in one sitting.
- **TDD:** no
- **Validation:** `grep -c '08-images-and-skills\|09-config-log' docs/product/00-overview.md` → ≥2; `grep -n 'P8\|time machine\|config log' docs/product/00-overview.md` → ≥2.
- **Depends on:** T1, T5, T6
- [x] done
- Notes: 2026-07-25 executed; orchestrator re-ran validations (4, 7; all 18 links resolve; no headings removed). Diagram shows four substrates + time machine; page held to ~120 lines. Executor also added the walkthrough-plan row to the folder table (accuracy judgment call, accepted).

### T12: End-to-end verification   [Status: done | Model: sonnet]
- **Scope:** Cross-file consistency: (1) every D1–D16 element appears in ≥1 spec file and
  (engine-relevant ones) ≥1 work-plan item; (2) shared names identical everywhere
  (`image_create`, `image_list`, `skill_create`, `skill_list`, `skill_get`, `skill_install`,
  `memory_current`, `config_history`, `config_events`, `config.changed`, `max_instances`,
  `briefing`, `rationale`, `name:version`); (3) the subscription `concurrency` column exists
  NOWHERE as a live feature (only in removal notes); (4) all relative links in changed files
  resolve; (5) no pre-existing § heading changed (git diff shows additions/in-place edits
  only); (6) CLAUDE.md's docs row still accurate (`00`–`09` range — update it if the range
  changed); (7) append one line to the research note's "Decisions (2026-07-25)" block:
  L9 (per-subscription concurrency) and the `drop`/`dropped` semantics were superseded the
  same day by worker-level `max_instances` (walkthrough amendment D4; plan:
  `2026-07-25-fold-walkthrough-amendments.md`). Fix trivial inconsistencies; log anything
  structural.
- **Files:** all changed files (verify; minor fixes), `CLAUDE.md` (range digit only, if
  needed), `docs/product/2026-07-22-landscape-learnings.md` (the one superseded line).
- **Acceptance criteria:** all seven checks pass.
- **TDD:** no
- **Validation:** `for n in image_create skill_install memory_current config_history config_events max_instances briefing rationale worker_update; do echo "== $n"; grep -rln "$n" docs/product/*.md; done` — each in ≥2 files; link-resolution loop over `docs/product/*.md`; `git diff HEAD -- docs/product/0*.md docs/product/17-product-spec.md | grep -E '^-#{2,3} '` → empty (spec files only — plan/research files excluded because their ticket-status headings legitimately change).
- **Depends on:** T1–T11
- [x] done
- Notes: 2026-07-25 run by orchestrator. All seven checks pass: shared names in 6–10 files each; `label_selector?` naming normalized (05 + plan Architecture); per-subscription concurrency only in E1's removal note; all links resolve; no spec § heading changed; CLAUDE.md bumped to 00–09; L9-superseded line appended to the research note's decisions block.

## Discovered Issues Log

- (T5) Param-name drift: Architecture/T8 wrote `image_list(selector?)` while D1/D3 and the
  memory tools use `label_selector?`. Canonical: **`label_selector?`** — T12 must normalize
  `05-management-tools.md` line ~36 to match.
- (T5) Open reconciliation deferred to Track I: tool-level append-only images vs. the
  `snapshot_ttl_days` reaper deleting underlying bytes (exempt referenced versions / tombstone
  reaped ones). §13.7 states both facts; the resolution is an implementation decision for I1.
