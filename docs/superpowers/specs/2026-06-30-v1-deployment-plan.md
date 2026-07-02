# v1 — Development & Deployment Plan (BadCode marketing, single box we watch)

**Date:** 2026-06-30
**Status:** Plan. Builds on Slice 0 (`go/orchestrator`, the learning loop) toward a deployed,
watchable, post-approving first version. Reading order: `AGENTS_DESIGN.md` →
`2026-06-30-objectives-and-build-path.md` → `2026-06-30-execution-coordination-model.md` → this.

## v1 definition (confirmed with the author)

- **Shape:** a **single box we watch** — single-tenant, one project, human-in-the-loop.
- **First project:** **BadCode marketing.** One real **social channel** (TBD — see Open Decisions).
- **Success:** it drafts marketing content, we approve/reject and leave notes, and over a few weeks
  the guidance visibly improves from those notes — the legible learning narrative, on real stakes.
- **Execution environment:** the **existing agentkit Docker-in-Docker setup on the author's GKE
  cluster** (known-good — not rebuilt here). Abstracted behind the §7 `WorkerRuntime` seam.

## Architectural stance: seam-first, infra off the critical path

Develop and test the app layer against **trivial seam impls**; swap in the production impls only at
the edges. **Infra is not on the critical path** — GKE/DinD reappears only at the final deploy step,
reusing what already works.

| Seam | Dev/test impl | Production (v1) impl |
|---|---|---|
| `Model` (Slice 0) | `ScriptedModel` (deterministic) | **Anthropic API** (API key — subscription OAuth is not permitted for automation; verified 2026-06). Per-scope model tiers. |
| `BoardStore` (Slice 0) | `MemBoard` (in-memory fold) | **Postgres** (reuse `go/agentdb` gorm + the §0 collapse: `fragments` + revisions) |
| `WorkerRuntime` (§7) | in-process session (no container) | **existing agentkit DinD on GKE** |
| `Connector` (new, thin) | fake connector (records "would post") | **one social channel** (behind the approval gate) |
| `MemoryStore` (§8) | — | **deferred post-v1** (v1 learning = human-feedback→fragment; needs no memory store) |

## v1 build slices (each ships something testable)

- **Slice A — Postgres `BoardStore`.** Implement the `agentdb.BoardStore` interface on Postgres:
  the §0 collapse (drop `board_staff`/`board_pipelines`/`board_event_types`; generalise
  `board_prompt_fragments` → `fragments`; add `tickets`). Fold/append/pin parity with `MemBoard`
  (reuse its tests against the new impl). **No new external deps; buildable now.**
- **Slice B — real `Model`.** Anthropic API impl behind the `Model` seam; per-scope **model tiers**
  (Haiku for summary/leaf, Opus for manager/reasoning); a **hard monthly spend ceiling** enforced in
  mechanism (the cost floor, sibling to the depth/spawn floors). Needs `ANTHROPIC_API_KEY`.
- **Slice C — the manager loop, for real.** `human-goal` → manager scope composes a marketing plan
  from seed fragments → files **tickets** → a worker scope drafts a post. Runs against the real
  model; still no publishing. Reuses Slice-0 compose/runner/telemetry.
- **Slice D — `Connector` seam + one channel + the publish-approval floor (C2).** Worker drafts →
  files a **pending-post ticket** (Needs-Human lane) → **human approves** → *only then* the connector
  publishes. Nothing reaches the channel without an explicit click. Fake connector in tests; the real
  channel impl is the only network-touching piece.
- **Slice E — the watch/approve/note surface.** The human-in-the-loop UI (reuse `web/` or a minimal
  new surface): pending approvals, an **approve/reject** action, a **targeted note** box
  (`(target_ref, note)` → `human-feedback` → `write_fragment` delta), and the **board-revision
  timeline + telemetry** render (the "show your work" content artifact).
- **Slice F — deploy.** Package the orchestrator; drop onto the **existing GKE/DinD** as the
  `WorkerRuntime`; wire secrets (API key + social token via Secret Manager); run BadCode marketing
  live, behind approval.

## Human-in-the-loop & the content surface

One surface serves three roles at once: **control panel** (approve/reject posts), **teacher's desk**
(leave notes that edit guidance), and **content artifact** (the legible board-revision/decision story
you publish as "watch it learn"). This is not an afterthought — for the credibility+content
objective it *is* a primary deliverable (Phase-3 finding: v1 under-serves legibility unless it's
built in).

## Cost governance (the deployment-specific floor)

API spend is the new runaway surface once workers spawn recursively. Governors, all in mechanism:
per-scope **model tiers**; **depth / fan-out / tree-global** caps (execution addendum §7); and a
**hard monthly spend ceiling** that stops dispatch when hit. The spend ceiling is load-bearing —
an autonomous fleet with a billing key and no ceiling is a financial version of the C2 grenade.

## Deferred post-v1 (all behind seams, no rework)

Autonomous Consultant · memory store + summary bots · pipelines · event bus · multi-channel ·
multi-project · snapshot-resume for stateful workers.

## Open decisions

1. **Which social channel?** Shapes the one `Connector` impl. Lowest build friction + on-brand:
   **Bluesky or Mastodon** (open APIs). **X** is gated/expensive; **Instagram/TikTok/LinkedIn** are
   painful to post to programmatically. **← needs the author.**
2. **Web surface:** reuse the existing `web/` React app, or a minimal purpose-built approval surface?
3. **Tickets schema:** finalise the v1 `tickets` columns (status lane, objective, acceptance,
   pending-artifact ref) with Slice A.

## Deployment-specific risks

- **The approval gate is the only thing between the model and BadCode's public reputation.** It must
  be un-bypassable in mechanism (a worker cannot publish directly; only an approved ticket triggers
  the connector). Single most important safety property of v1.
- **API spend runaway** → the monthly ceiling (above) is mandatory before any live run.
- **Secret handling** — API key + social token in Secret Manager, never in the board/fragments
  (which are content, versioned, and Consultant-editable later).
- **Prompt-injection via the channel** (replies/mentions the manager might ingest later) — out of
  scope for v1 (draft-only + approval), but flag before any read-from-channel feature lands.
