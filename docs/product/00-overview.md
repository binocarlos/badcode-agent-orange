# Spec — Overview: the shape of what we're building

**Start here.** This page is the quick map: the shape, the status, and where every piece of
detail lives. Everything about this plan — spec, tickets, research trail, execution record —
lives in this one folder, `docs/product/`:

| File | What |
| --- | --- |
| [`17-product-spec.md`](17-product-spec.md) | The entry point — goal, atoms, binding principles P1–P7, vocabulary, non-goals (§10). |
| `01`–`07` (component specs) | The designs, §4–§9 (table below), plus **the engineering tickets** ([`06-work-plan.md`](06-work-plan.md)). |
| [`2026-07-22-landscape-learnings.md`](2026-07-22-landscape-learnings.md) | The research trail: the landscape verdict (nobody has built this shape) and the 33 mechanisms mined from other projects, with final dispositions. |
| [`2026-07-25-fold-landscape-learnings.md`](2026-07-25-fold-landscape-learnings.md) | The executed record of how those learnings were interviewed, decided, and folded into this spec. |

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
  │  WORKERS = rows of {name, system_prompt, mcp_config, enabled}   (§6)   │
  │      │                                                                 │
  │      │  a trigger arrives…                                             │
  │      │    events (external POST / worker.finished / …)  (§8.1–8.3)     │
  │      │    schedules (cron + instruction text)           (§8.6)         │
  │      │    a human opens a chat                          (§6.4)         │
  │      ▼                                                                 │
  │  ROUTER matches subscriptions → ComposeJob → JOB                (§8.4) │
  │      │        preamble + project prompt + worker prompt +              │
  │      │        memory briefing; event text as first message     (§6.2)  │
  │      ▼                                                                 │
  │  SESSION in its own container (snapshot/resume, engine layer 0)        │
  │      │   tools: worker's MCP servers  +  core tools:                   │
  │      │     memory_* (§7) · worker_*/schedule_*/subscription_* (§9)     │
  │      │     request_human_attention → webhook + ordinary chat (§9)      │
  │      ▼                                                                 │
  │  finishes → worker.finished event (full transcript)  ──► other workers │
  │             wake: reviewers rewrite prompts, archivists file           │
  │             MEMORY (append-only, labeled, hybrid search)   (§7)        │
  └────────────────────────────────────────────────────────────────────────┘
```

The loop that defines "done" (§8.7): emails flow through an answerer; a reviewer reads the
transcripts and *rewrites the answerer's system prompt* through a tool; the next email is
answered better — behaviour changed by a worker editing a worker, no human, no deploy.

The first real use (§8.8): the BadCode marketing manager — one seeded worker whose prompt
describes the whole workforce, reconciling it into existence on a schedule.

## What's decided (the load-bearing calls)

- **Mechanism, not policy** — core ships substrates; all opinion lives in prompts (P1–P7 in
  [`17-product-spec.md`](17-product-spec.md)). No DAGs, no roles-as-code, no approval queues.
- **Loop safety is minimal by choice** — depth ≤ 8, per-project concurrency, per-project daily
  token budget. No stuck detectors or recursion guards in v1: prompt vigilance + root-only
  prompt editing (decided 2026-07-25; §10 records the reasoning and the revisit condition).
- **Review topology is fully fluid** — core never protects one worker's prompt from another;
  patterns live in [`07-reference-prompts.md`](07-reference-prompts.md) as suggestions only.
- **Not reinventing the wheel** — the landscape research proved no existing project covers this
  combination; 18 mechanisms were adopted from the closest ones (see the research trail above).

## The component specs (§ detail)

| Spec | § | Covers |
| --- | --- | --- |
| [`01-session-config.md`](01-session-config.md) | §4–5 | Per-session MCP plumbing, `${VAR}` credentials, project settings + budgets |
| [`02-workers.md`](02-workers.md) | §6 | Worker rows, deterministic job composition, the core preamble |
| [`03-memory.md`](03-memory.md) | §7 | Append-only labeled memory, K8s selectors, hybrid RRF search, rolling summaries |
| [`04-events-and-schedules.md`](04-events-and-schedules.md) | §8 | Events, subscriptions, router, schedules, loop floors, the worked examples |
| [`05-management-tools.md`](05-management-tools.md) | §9 | `worker_*`/`schedule_*`/`subscription_*` tools, `request_human_attention` |
| [`07-reference-prompts.md`](07-reference-prompts.md) | — | Optional archivist / consultant / manager / failure-notifier prompts |

## The build (tickets live in [`06-work-plan.md`](06-work-plan.md))

Engine layer 0 (sessions, snapshots, containers, events, persistence, UI, stack) is **already
built** — see §2 of the entry point. What remains is the product layer, in parallelisable
tracks; items tagged `[learnings]` came from the research fold-in:

| Wave | Tracks | Delivers |
| --- | --- | --- |
| 1 | A1 · B1 · C1 · D1 · E1 · F3 | Foundations: MCP types on sessions, settings/workers/memories/events tables, session permalinks |
| 2 | A2–A5 · B2–B4 · C2–C4 · D2–D3 · E2–E4 · H1–H2 | The machinery: composition, router, scheduler, emitters, core tools, budgets, lease reaper |
| 3 | F1–F2 · G1–G3 | Observability UI, then acceptance: the §8.7 loop closes offline (G1), docs (G2), live BadCode manager (G3) |

**G1 is the bar**: the self-improvement loop demonstrably closes with the mock model. G3 is the
first production use.
