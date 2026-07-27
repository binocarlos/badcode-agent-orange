# 17 — Product specification: from atoms to the full system

**Status: BUILT** (2026-07-26). All 37 items in [`06-work-plan.md`](06-work-plan.md) are complete and
merged on the `product-layer` branch; the §8.7 acceptance loop closes offline and was observed
against the real Anthropic API. Read this as **shipped design, not a proposal** — where this document
and the code disagree, the code is the authority and the difference is a bug here. Amendments made
during the build are marked inline (§13.5, §15.6, §8.4) and one **open decision** is flagged in §6.2.
Drafted 2026-07-15 as a forward-looking spec, after the great simplification
(the `go/orchestrator/` + `cmd/oranged/` + fragments/board/tickets layer was deleted wholesale —
see git history ≤ `dc49595` for the old design), split into component files 2026-07-22. This
document is the **entry point**: the goal, the atoms, the binding principles, what exists, the
vocabulary, and the non-goals. The component designs live as sibling files in this folder (map below) and
keep their original section numbers, so a reference like §7.6 or §8.8 always means the same
thing. The numbered engine docs (`01`–`15`) describe the runtime as built; this spec describes
what we build **on top of it**, and nothing here may violate the engine rules in `CLAUDE.md`.

---

## 1. The declarative goal

**Agent Orange is a runtime for durable, container-backed agent sessions, organised into
projects, on which self-improving arrangements of workers can be composed with prompts alone.**

The system is defined by a deliberately small set of atoms. Everything else — workers,
consultants, archivists, email bots, review loops — is *data and wiring on top of the atoms*,
never new engine machinery.

### 1.1 The atoms

1. **Session** — run an agentic session in a container; snapshot it (filesystem included) and
   resume it later.
2. **Tools** — configure a session with MCP servers.
3. **Images** — named, versioned, labeled records of an environment (`name:version`), which agents
   create from *inside* a session with `image_create`; sessions launch from them
   (§13, [`08-images-and-skills.md`](08-images-and-skills.md)).
4. **Projects** — log in, switch projects; a project is a namespace for everything.
5. **Project defaults** — each project carries the defaults for new sessions: base image,
   system prompt, MCP configuration.

### 1.2 The three per-worker quantities

Once inside a project, every *opinion* a worker holds reduces to exactly three things:

| Quantity | Where opinion lives |
| --- | --- |
| **System prompt** | Plain big string. Composed project-level + worker-level. |
| **MCP tools** | Merged project-level ∪ worker-level. |
| **Memory** | A core substrate (labeled KV + search); *how* it is used lives entirely in prompts. |

A worker row additionally carries a little plumbing — an image pointer
(§13, [`08-images-and-skills.md`](08-images-and-skills.md)), `max_instances`, and `briefing`
label selectors (§7.4, [`03-memory.md`](03-memory.md)) — but none of it holds opinion; it is
wiring, and every behavioural question is still answered by one of the three quantities above.

### 1.3 Design principles (binding)

These principles resolve disputes during implementation. When a proposed feature conflicts with
one of them, the feature loses.

- **P1 — Mechanism, not policy.** Core code provides substrates (sessions, prompt storage,
  memory store, event delivery). All *opinion* — what to remember, how to label, when to react,
  what a workflow looks like — lives in worker system prompts. If you find yourself writing Go
  that encodes a behaviour a prompt could encode, stop.
- **P2 — Roles are not primitives.** A "consultant", "archivist", or "email bot" is a worker
  with a particular system prompt and event wiring. No role ever becomes a Go type, a table, or
  a file of its own.
- **P3 — No pipelines.** There is no workflow/DAG feature. Complex intra-job workflows
  ("do X, then three things in parallel, then finalise") are expressed in the worker's prompt;
  the Claude Agent SDK orchestrates them inside the session (subagents, parallel tool dispatch).
  Research note: this matches Anthropic's own guidance — prompt-driven orchestration *within* a
  session, deterministic code only for coordination *between* agents. Our "between" layer is the
  event router (§8, [`04-events-and-schedules.md`](04-events-and-schedules.md)), which is deliberately trivial: a subscriptions table, nothing more.
  Multi-step arrangements across workers are *emergent* from chained events, never declared.
- **P4 — Prompts are plain strings.** No fragments, no templates, no `{{placeholders}}`,
  no versioned prompt store. A consultant improves another worker by *rewriting its system
  prompt wholesale* through a tool. We trust the model to edit large prompts.
- **P5 — Project is the hard namespace.** Every table introduced by this spec carries a
  `project` column, always filtered, never crossable from inside a session. Inside a project
  there are no further hard boundaries — labels and prompts do the soft partitioning.
- **P6 — Non-interactive first.** The primary consumer of a worker session is an event or a
  schedule, not a human. Humans *may* chat with any worker interactively, but nothing in the
  design may depend on a human being present (`ask_user` must never be load-bearing for a
  background worker). The sanctioned way for a background worker to involve a human is the core
  `request_human_attention` tool (§9, [`05-management-tools.md`](05-management-tools.md)): notify a configured channel with a link to the session,
  end the turn, and carry on when the human's reply arrives as the next message in the thread.
  "Staged autonomy" — ask first, later just act — is then purely a prompt edit, never a feature.
- **P7 — Engine invariants hold.** The `go/` module imports nothing from host apps;
  `go build ./...` stays green; installation Dockerfiles never set CMD/ENTRYPOINT; heavy test
  coverage in the existing table-test style accompanies every change.
- **P8 — Append-only everywhere.** Nothing in Agent Orange is ever destructively updated;
  current state is always a *view* over an append-only history. Memories are immutable rows
  (§7.1), sessions are immutable transcripts, images and skills gain versions rather than
  losing them (§13–§14), and configuration itself — every worker hire, prompt rewrite,
  subscription, schedule and image adoption — is a fold over the `config_events` log
  (§15, [`09-config-log.md`](09-config-log.md)), of which the ordinary tables are projections.
  Undo is therefore a *forward* operation: "restore to last Tuesday" appends compensating
  events (git revert, never git reset), so the history of undoing things is itself history.

---

## 2. What already exists (layer 0 — the engine)

Built, tested, and kept. Reference only — no work items here beyond the two gaps flagged.

| Atom | Where | Status |
| --- | --- | --- |
| Session lifecycle | `go/agentkit.go`, `go/runner.go` (`Runner`: Create/Send/Stream/Stop) | done |
| Snapshot + resume | `runner.go` Snapshot/`restoreToWorker`/`rehydrateConversation`; `imageregistry` Persist/Materialize; idle archive loop | done |
| Container seam | `go/execenv/` (Docker, DinD, mock) | done |
| Image registry | `go/imageregistry/` (+ `auth`, `blobarchive`, `ociregistry`); `Build()` intentionally host-side | done |
| Custom images | `BuildSpec`/overlays; launch priority `Image` > `CustomImageID` > `Policy.BaseImage` (`runner.go:resolveLaunchImage`) | done |
| Event stream | `go/events/` canonical SSE vocabulary; single web reducer for live+replay | done |
| Persistence | `go/agentdb/` sessions/messages/query-events/artifacts/skills/customimages (Postgres + sqlite) | done |
| Login + projects | `cmd/agentd/{auth,googleauth}.go` — Google / password / dev-open; project = the `customer` JWT claim; `AGENTKIT_PROJECT_MAP` | done (Google login untested live) |
| In-image agent | `sandbox/` — control server + `@anthropic-ai/claude-agent-sdk` harness | done |
| Chat UI | `web/` — event-reducer chat, login, project switch | done |
| Standalone stack | `docker-compose.yml`: web + agentd + dind + postgres(pgvector) | done |

**Gap G1 — per-session MCP config is not plumbed.** `CreateSessionRequest` has no MCP surface;
the sandbox resolves tools exclusively from its in-image registry (builtins + `PRODUCT_PLUGINS_DIR`
plugins) in `sandbox/src/tools/registry.ts`. Fixed by §4 ([`01-session-config.md`](01-session-config.md)).

**Gap G2 — no per-project defaults.** Base image is the global `AGENTKIT_IMAGE`; system prompt is
per-session with a nil `SessionContextProvider` in agentd. Fixed by §5 ([`01-session-config.md`](01-session-config.md)).

---

## 3. Vocabulary

- **Project** — namespace; the `customer` claim; owns defaults, workers, memories, subscriptions.
- **Worker** — a named row in a project: `{name, system_prompt, mcp_config, enabled}`. Not a
  process. A worker "exists" whether or not anything is running.
- **Job** — one session run on behalf of a worker, triggered by an event or a human. A worker
  may have many jobs over time; each job is a fresh session — a fresh container from the image
  the worker points at, else the project default (§13) — unless resuming.
- **Event** — a named **text** payload delivered into a project: `{type, text}` and nothing
  else the sender controls (core stamps an envelope — §8.1). Either **external** (arrived via
  the ingestion API — an email, a webhook) or **internal** (emitted by the runtime — most
  importantly `worker.finished`). A worker without an input does nothing: the system prompt is
  the persona and capabilities; the event text is what actually tells it what to do, this run.
- **Schedule** — a row saying "at *these times*, start a job for *this worker* with *this input
  text*". The input text is the point: "10:00 → *write the morning tweet*", "17:00 → *write the
  evening tweet*" — same worker, different instruction per trigger (§8.6).
- **Subscription** — a row saying "when an event matching *this* arrives in *this project*,
  start a job for *this worker*".
- **Memory** — one row in the project's memory store: content + labels (+ optional embedding).
- **Image** — a named, versioned, labeled environment record (`name:version`), created from
  inside a session by `image_create`. A bare `name` resolves to the newest version at launch;
  `name:version` pins. Versions are append-only (§13).
- **Skill** — a project-scoped, labeled, append-only record pairing a Claude-Code-style skill
  markdown document with an `install_sh` that installs its software dependencies;
  `skill_install` applies both inside a running session (§14).
- **Config event** — one append-only row in `config_events` recording a management mutation:
  `{actor, action, payload, rationale, created_at}`. The log is authoritative; workers,
  subscriptions and schedules are projections of it (§15).

---

---

## The component specs (the § map)

| File | Sections | Covers |
| --- | --- | --- |
| [`00-overview.md`](00-overview.md) | — | **Start here for the quick shape**: one-page map of the system, the decided calls, the build waves, and where the research/plan trail lives. |
| [`01-session-config.md`](01-session-config.md) | §4–§5 | Session MCP plumbing (G1): Go surface, wire protocol, harness merge, `${VAR}` credential references, snapshot interaction. Project settings (G2): `project_settings` table, precedence, HTTP/UI. |
| [`02-workers.md`](02-workers.md) | §6 | Worker data model — including the `image` pointer (§13), `max_instances` and `briefing` selectors — deterministic job composition (pre-prompt manipulation), the core preamble, interactive chat, HTTP/UI. |
| [`03-memory.md`](03-memory.md) | §7 | Append-only immutable memory: labels + K8s selectors, the `memory_*` MCP tools with provenance, rolling-summary convention, embeddings, the §7.6 relevance contract (hybrid RRF), the build-on-Postgres decision. |
| [`04-events-and-schedules.md`](04-events-and-schedules.md) | §8 | Event shape (text + core envelope), the four internal events (`worker.finished`/`worker.failed`/`human.attention.timeout`/`subscription.throttled`), subscriptions, the router + loop floors, external ingestion, schedules (cron + input text), the acceptance scenario (§8.7) and the BadCode marketing-manager reference use case (§8.8). |
| [`05-management-tools.md`](05-management-tools.md) | §9 | Core management MCP tools (`worker_*`, `project_prompt_*`, prompt-revision memories) and `request_human_attention`. |
| [`06-work-plan.md`](06-work-plan.md) | §11–§12 | The parallelisable checklist (tracks A–H, waves) and the verification strategy. |
| [`07-reference-prompts.md`](07-reference-prompts.md) | — | Optional reference prompts — archivist, consultant, manager, failure notifier, chronicler; conventions, never mechanisms. |
| [`08-images-and-skills.md`](08-images-and-skills.md) | §13–§14 | Images as named/versioned/labeled append-only records (`image_create`/`image_list`, latest-vs-pinned resolution, the worker pointer, snapshot GC) and skills as markdown + `install_sh` (`skill_create`/`skill_list`/`skill_get`/`skill_install`, the curate-then-burn workflow). |
| [`09-config-log.md`](09-config-log.md) | §15 | The config log / time machine: the `config_events` table, which mutations log, dual-write with tables as projections, point-in-time replay, restore as compensating events, required `rationale`, the routable `config.changed` event, `config_history`. |

Sections §1–§3 and §10 live in this file.

---

## 10. Explicit non-goals (deleted concepts stay deleted)

- No pipelines / DAGs / workflow definitions (P3).
- No prompt fragments, templates, or composition beyond §6.2's concatenation (P4). This was
  re-examined on 2026-07-25 with named memories in hand and **rejected again**: in-prompt
  interpolation (`{{customer-greeting}}` resolving against a memory) makes a prompt unreadable
  without running the resolver and breaks wholesale-rewrite self-improvement. The sanctioned
  routes to the same effect are runtime lookup (`memory_current`, §7.3) and `briefing`
  label selectors injected at composition time (§7.4, §6.2).
- No ambient durable workshops. A rejected design (2026-07-25) gave each worker one long-lived
  container kept warm by a TTL "kernel", snapshotted on eviction, accumulating filesystem state
  as a side effect; it loses because ambient state means filesystem contention between
  concurrent jobs and unauditable drift. Environment continuity is instead **deliberate**:
  nothing persists unless an agent snapshots it with `image_create` and labels saying why
  (§13). Invisible warm-container reuse remains available as an engine-internal optimisation,
  never as a semantic feature.
- No roles/staff tables, per-worker model-tier routing, per-worker spend meters, tickets,
  kanban, approval gates, goal boxes, or manager tick loops. Each was built once and removed;
  the replacement for every one of them is "a worker with the right prompt". (The per-project daily
  token budget — two-tier, §5, enforced by the router in §8.4 — is not that meter re-grown: like
  the depth cap, it is resource physics, a project-wide floor, never per-worker accounting.)
- No runtime loop-safety governors beyond the §8.4 depth + concurrency floors and the §5 daily
  budget: no schedule-recursion guards, no per-job iteration caps, no stuck detector in v1
  (considered 2026-07-25, rejected — prompt vigilance plus root-only prompt editing covers the
  risk today; revisit with live evidence; see
  [`2026-07-22-landscape-learnings.md`](2026-07-22-landscape-learnings.md)).
- No per-worker visibility filtering of project MCP tools, and no roles/authorization inside a
  project — any worker may adjust anything; the project is the only boundary. Review topology is
  fully prompt-defined: core never protects one worker's prompt from another — even "never edit
  your reviewer" is only a suggested pattern ([`07-reference-prompts.md`](07-reference-prompts.md)).
- No approval queues, draft queues, or approval UI — `request_human_attention` + the ordinary
  chat thread is the entire human-review surface (§9); staged autonomy is a prompt pattern.
- No core auto-archiving of conversations.
- No memory mutation or deletion (append-only, immutable — §7.1); delete arrives only with a
  future curator worker, and update never does.
- No credential storage in the database — MCP config names environment variables (§4.4);
  no secret manager, no per-project credential separation, no rotation.
- No multi-tenant hardening beyond the existing project scoping (single-trusted-org posture).
