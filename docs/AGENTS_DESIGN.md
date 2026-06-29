# Agent Orange — Design Index

**This is the map — start here.** Agent Orange is an opinionated, single-tenant **manager-as-HR layer
(in Go)** that runs an autonomous agent organization for general-purpose (non-code) business goals,
over a **pluggable worker runtime**. A human sets a vague goal; a continuously-running manager
interviews it into a spec, decomposes it onto a kanban **board**, composes & launches scoped worker
sessions, reviews their work, learns from it, and escalates when needed. The board (the org's operating
policy) is versioned **GitOps-style**, and a **Consultant** continuously proposes improvements through
a review gate.

> **Authoritative design → [`ARCHITECTURE.md`](ARCHITECTURE.md).** That is the single build-against
> document. Everything else below is the decision record, the research evidence, or history.

## The converged conclusion (one paragraph)
Five exhaustive research passes (~1,100 systems) reached the same verdict from every angle: **a small,
sharp, genuinely novel core to BUILD** — the manager-as-HR, the CBR routing layer, the Consultant, the
board-as-GitOps governance, and above all a non-code outcome signal — **sitting on an almost-entirely
ADOPTABLE substrate** (worker runtime, memory store, skill loaders, event bus, policy/eval/canary
tooling). The single load-bearing risk, confirmed five times independently: **there is no automatic
oracle for non-code goals, so the human-quality-score / credit-assignment layer is where the project
lives or dies.**

## Read in this order
| # | Doc | What it is |
|---|---|---|
| 1 | **[ARCHITECTURE.md](ARCHITECTURE.md)** | **The authoritative current design** — runtime seam, board, routing manager, memory/HR, self-improvement, board-as-GitOps, staged roadmap, risks. Build against this. |
| 2 | [AGENTS_STACK_DECISION.md](AGENTS_STACK_DECISION.md) | The build-vs-buy decision for the worker-runtime layer (stop porting agentkit's heavy impl; adopt behind a seam, agentkit-first then OpenHands). |
| 3 | [AGENTS_RESEARCH.md](AGENTS_RESEARCH.md) | The original design seed — vision, "scopes not personas", layered build/buy, reliability rules. Foundational background. |

## Research evidence (the landscape surveys)
Each is an exhaustive discovery pass (search → fetch → completeness-critic → synthesis). Reference when choosing a component; not needed to follow the design.
| Doc | Covers | Systems |
|---|---|---|
| [AGENTS_ORCHESTRATION_LANDSCAPE.md](AGENTS_ORCHESTRATION_LANDSCAPE.md) | orchestration / manager-over-workers / board-driven agents | 220 |
| [AGENTS_MEMORY_HR_LANDSCAPE.md](AGENTS_MEMORY_HR_LANDSCAPE.md) | agent memory, self-organizing labels, skill & prompt management | 227 |
| [AGENTS_SELF_IMPROVING_LANDSCAPE.md](AGENTS_SELF_IMPROVING_LANDSCAPE.md) | CBR-for-agents, event-driven architecture, self-evolving orchestration, org learning | 248 |
| [AGENTS_POLICY_AS_CODE_LANDSCAPE.md](AGENTS_POLICY_AS_CODE_LANDSCAPE.md) | policy-as-code, GitOps-for-agents, versioned config/governance, canary | 212 |

## Superseded / historical (kept for provenance, not for building)
- [conversation-handoff.md](conversation-handoff.md) — a **pre-pivot** handoff (when the plan was to
  finish the agentkit hoist + GCP Phase 4). Its "what's next" is **outdated** by the current direction;
  kept for provenance only.

## Legacy engine docs (the agentkit runtime)
`00-vision.md` … `16-derived-images.md`, `90-provenance-map.md`, `91-migration-plan.md` describe the
**agentkit** engine Agent Orange was forked from. We **keep its interface/seam shape** (it becomes the
`WorkerRuntime`) but are **pivoting away from its heavy implementation** (DinD, image registry,
blobarchive). See `ARCHITECTURE.md` §16 and the repo's `CLAUDE.md` / `MIGRATION.md`. Treat these as
runtime reference, not agent-org design.

## What's next
The architecture is complete end-to-end. The next build-shaping step is the **architecture-board data
model** — the versioned git schema for staff templates, event subscriptions, pipelines, and the event
taxonomy (the foundation everything reads from and the Consultant edits via PRs).
