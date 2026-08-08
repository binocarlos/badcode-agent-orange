# Spec — Overview: the shape of what we built

> **Status (2026-07-26): built.** All 37 work-plan items are complete and merged on the
> `product-layer` branch. The §8.7 acceptance loop — a worker rewriting *another worker's* system
> prompt, with a rationale, and the next job running with the improved prompt — closes offline in
> the e2e suite, and was observed against the real Anthropic API. So read this as **shipped design,
> not a proposal**: where the two ever disagree, the code is the authority and the difference is a
> bug in this document.
>
> Two things a reader should know before trusting any section: the amendments made *during* the
> build are marked inline (§13.5, §15.6, §8.4, and an **open decision** in §6.2 about the
> untrusted-data markers), and [`06-work-plan.md`](06-work-plan.md)'s **Discovered Issues Log** —
> ~100 entries — is the honest record of what surprised us, including what the evidence does *not*
> prove.

**Start here.** This page is the quick map: the shape, the status, and where every piece of
detail lives. Everything about this plan — spec, tickets, research trail, execution record —
lives in this one folder, `docs/product/`:

| File | What |
| --- | --- |
| [`17-product-spec.md`](17-product-spec.md) | The entry point — goal, atoms, binding principles P1–P8, vocabulary, non-goals (§10). |
| `01`–`09` (component specs) | The designs, §4–§9 and §13–§15 (table below), plus **the engineering tickets** ([`06-work-plan.md`](06-work-plan.md)). |
| [`2026-07-22-landscape-learnings.md`](2026-07-22-landscape-learnings.md) | The research trail: the landscape verdict (nobody has built this shape) and the 33 mechanisms mined from other projects, with final dispositions. |
| [`2026-07-25-fold-landscape-learnings.md`](2026-07-25-fold-landscape-learnings.md) | The executed record of how those learnings were interviewed, decided, and folded into this spec. |
| [`2026-07-25-fold-walkthrough-amendments.md`](2026-07-25-fold-walkthrough-amendments.md) | The design-walkthrough amendments (images, skills, the config log, named memories) and their execution record. |
| [`25-cooperative-patterns.md`](25-cooperative-patterns.md) | 38 cooperative workflow patterns drawn from what people outside this project actually run, each judged against the code (expressible / partial / blocked) by an adversarial fit pass. Read §5 first. |
| [`26-work-plan-cooperative-tests.md`](26-work-plan-cooperative-tests.md) | The executable half of `25`: 24 test tickets, 20 engine gaps, 6 pieces of test infrastructure. Three gaps now closed; G1/G2 withdrawn. |
| [`27-simplification-inventory.md`](27-simplification-inventory.md) | **The KISS decision.** What it makes dead (less than you would think), what stays and why, and the correction that there is no "event system" to delete — the spine is what the simple design runs on. |

The engine the product layer builds on (architecture, containers, images, events, harness,
stack) is documented in `../01`–`../15` — reference, not work.

## The one-sentence goal

**A runtime for durable, container-backed agent sessions, organised into projects, on which
self-improving arrangements of workers can be composed with prompts alone.**

## The shape

```
                         PROJECT  (the only hard namespace)
  ┌────────────────────────────────────────────────────────────────────────┐
  │  defaults: base image · project prompt · MCP config · budgets   (§5)   │
  │                                                                        │
  │  WORKERS = rows of {name, system_prompt, mcp_config, enabled,          │
  │                     image, max_instances, briefing}             (§6)   │
  │      │                                                                 │
  │      │  a trigger arrives…                                             │
  │      │    events (external POST / worker.finished / config.changed)    │
  │      │                                                  (§8.1–8.3)     │
  │      │    schedules (cron + instruction text)           (§8.6)         │
  │      │    a human opens a chat                          (§6.4)         │
  │      ▼                                                                 │
  │  ROUTER matches subscriptions → ComposeJob → JOB                (§8.4) │
  │      │    preamble + project prompt + worker prompt + briefing         │
  │      │    sections; event text as first message               (§6.2)   │
  │      │    image: worker pointer > project > global           (§13)     │
  │      │    dispatch gated by max_instances; excess queues FIFO (§8.4)   │
  │      ▼                                                                 │
  │  SESSION in its own container (snapshot/resume, engine layer 0)        │
  │      │   tools: worker's MCP servers  +  core tools:                   │
  │      │     memory_* (§7) · image_*/skill_* (§13–14)                    │
  │      │     worker_*/schedule_*/subscription_* · config_history (§9,15) │
  │      │     request_human_attention → webhook + ordinary chat (§9)      │
  │      ▼                                                                 │
  │  finishes → worker.finished event (full transcript)  ──► other workers │
  │             wake: reviewers rewrite prompts, archivists file           │
  │                                                                        │
  │  FOUR SUBSTRATES — append-only, labeled, provenance-carrying (P8):     │
  │    MEMORIES (§7) · IMAGES (§13) · SKILLS (§14) · CONFIG LOG (§15)      │
  │  time machine: every management mutation appends a config event and    │
  │    emits config.changed; tables are projections; restore is a forward  │
  │    operation (compensating events), so history is never truncated      │
  └────────────────────────────────────────────────────────────────────────┘
```

The loop that defines "done" (§8.7): emails flow through an answerer; a reviewer reads the
transcripts and *rewrites the answerer's system prompt* through a tool; the next email is
answered better — behaviour changed by a worker editing a worker, no human, no deploy.

The first real use (§8.8): the BadCode marketing manager — one seeded worker whose prompt
describes the whole workforce, reconciling it into existence on a schedule.

## What's decided (the load-bearing calls)

- **Mechanism, not policy** — core ships substrates; all opinion lives in prompts (P1–P8 in
  [`17-product-spec.md`](17-product-spec.md)). No DAGs, no roles-as-code, no approval queues.
- **Append-only everywhere (P8)** — memories, sessions, images, skills *and configuration* are
  histories; current state is a view over them. Undo appends compensating events (git revert,
  never git reset) — [`09-config-log.md`](09-config-log.md).
- **Environments are deliberate, not ambient** — a session's filesystem survives only if an agent
  calls `image_create`; images are `name:version` records a worker points at, and capability travels
  as skills (markdown + `install_sh`). Long-lived "workshop" containers accumulating state were
  rejected (§10, [`08-images-and-skills.md`](08-images-and-skills.md)).
- **Named memories, no templates** — `name=<x>` labels give singleton/KV semantics over the
  append-only log (`memory_current`), and workers pull extra context via `briefing` selectors.
  In-prompt `{{interpolation}}` stays rejected (P4).
- **Every change carries a why** — prompt rewrites require a `rationale`; it lands in the config
  event, the prompt-revision memory, and the changelog UI, and a chronicler worker can narrate it.
- **Loop safety is minimal by choice** — depth ≤ 8, per-project concurrency, per-project daily
  token budget, plus per-worker `max_instances`. No stuck detectors or recursion guards in v1:
  prompt vigilance + root-only prompt editing (§10 records the reasoning and the revisit condition).
- **Review topology is fully fluid** — core never protects one worker's prompt from another;
  patterns live in [`07-reference-prompts.md`](07-reference-prompts.md) as suggestions only.
- **Not reinventing the wheel** — no existing project covers this combination; 18 mechanisms were
  adopted from the closest ones (research trail above).

## The component specs (§ detail)

| Spec | § | Covers |
| --- | --- | --- |
| [`01-session-config.md`](01-session-config.md) | §4–5 | Per-session MCP plumbing, `${VAR}` credentials, project settings + budgets |
| [`02-workers.md`](02-workers.md) | §6 | Worker rows (incl. `image` pointer, `max_instances`, `briefing`), deterministic job composition, the core preamble |
| [`03-memory.md`](03-memory.md) | §7 | Append-only labeled memory, K8s selectors, hybrid RRF search, `name=` singletons + `memory_current`, briefing sections |
| [`04-events-and-schedules.md`](04-events-and-schedules.md) | §8 | Events (incl. `config.changed`), subscriptions, router + instance gating, schedules, loop floors, the worked examples |
| [`05-management-tools.md`](05-management-tools.md) | §9 | `worker_*`/`schedule_*`/`subscription_*` tools, `request_human_attention` |
| [`08-images-and-skills.md`](08-images-and-skills.md) | §13–14 | Named/versioned/labeled images (`image_create`/`image_list`, latest-vs-pinned), skills as markdown + install (`skill_install`), curate-then-burn |
| [`09-config-log.md`](09-config-log.md) | §15 | The config log / time machine: `config_events`, tables as projections, replay, restore-as-forward, `config_history` |
| [`07-reference-prompts.md`](07-reference-prompts.md) | — | Optional archivist / consultant / manager / failure-notifier / chronicler prompts |

## The build (tickets live in [`06-work-plan.md`](06-work-plan.md))

Engine layer 0 (sessions, snapshots, containers, events, persistence, UI, stack) is **already
built** — see §2 of the entry point. What remains is the product layer, in parallelisable
tracks; items tagged `[learnings]` came from the research fold-in, `[walkthrough]` from the
design walkthrough:

| Wave | Tracks | Delivers |
| --- | --- | --- |
| 1 | A1 · B1 · C1 · D1 · E1 · F3 · I1 · J1 | Foundations: MCP types on sessions, settings/workers/memories/events tables, image+skill store, `config_events`, session permalinks |
| 2 | A2–A5 · B2–B4 · C2–C4 · D2–D3 · E2–E4 · H1–H2 · I2–I4 · J2–J3 | The machinery: composition, router, scheduler, emitters, core tools (incl. `image_*`/`skill_*`/`config_history`), budgets, lease reaper |
| 3 | F1–F2 · J4 · G1–G3 | Observability UI incl. the changelog, then acceptance: the §8.7 loop closes offline (G1), docs (G2), live BadCode manager (G3) |

**G1 is the bar**: the self-improvement loop demonstrably closes with the mock model. G3 is the
first production use.
