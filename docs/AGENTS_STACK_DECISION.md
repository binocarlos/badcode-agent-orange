# Agent Orange — Execution-Environment & Stack Decision

**Date:** 2026-06-26
**Status:** Decision proposal. Follows on from `docs/AGENTS_RESEARCH.md` (the design seed) and
the live `MIGRATION.md` work (the agentkit→Agent-Orange hoist). Written after two adversarially-
verified deep-research passes (single-agent execution layer; autonomous orchestration layer).
**Audience:** us, deciding whether to keep copying Platinum's `agentkit` or to lean on
off-the-shelf systems.

> **TL;DR.** Stop hand-building the layer that is now a commodity, and concentrate our code on the
> layer that nobody sells. Concretely: **adopt an off-the-shelf single-agent runtime
> (OpenHands Software Agent SDK self-hosted, or a managed sandbox) for layer 1** instead of
> finishing the `agentkit` hoist, and **build thin on top** — the orchestrator + board + triggering
> + memory (layers 2–4). The Docker-image-as-environment format you care about is *native* to the
> off-the-shelf runtimes, and single-tenancy lets us shed exactly the heavy machinery (DinD,
> multi-tenant hardening, image GC, egress allowlists) that justified `agentkit`'s weight.

---

## 1. The decision being made

`docs/AGENTS_RESEARCH.md` already reached the right *shape* of conclusion ("scopes not personas";
rent the commodity layers; build only the differentiated coordination layer). But it was written
**before** a mature, self-hostable agent runtime existed off the shelf, so it hedged: "borrow
`agent-library` *code* for layer 1." Two things have changed the calculus:

1. **The single-agent runtime is now a commodity.** The OpenHands **Software Agent SDK** (paper
   `arXiv:2511.03690`, ~Nov 2025; MIT-licensed) ships exactly what `agentkit`'s layer-1 was being
   re-built to provide — Docker-image-defined environments, in-container bash/file/code execution,
   and **native pause/snapshot/resume** — as a maintained open-source project. Renting/borrowing it
   is now strictly cheaper than finishing the hoist.
2. **You re-stated the project's actual goal**, and it is *not* Platinum's goal. Platinum sells
   reproducible-compute-environments-for-research as a hardened, multi-tenant, image-burning SaaS.
   Agent Orange is an **experimental framework for connecting agentic workflows to a handful of the
   same org's own projects** (art collective, financial-research bot, holiday-bookings marketplace).
   Different shape ⇒ different build/buy line.

**So the decision is:** do we keep porting `agentkit`'s heavy container/registry/snapshot machinery
(the `phase4-gcp-seams` direction), or do we treat layer 1 as bought and redirect our effort up the
stack? This doc argues for the latter and lays out the evidence.

---

## 2. The single lever that changes everything: single-tenancy

You made the load-bearing point explicitly: **one trusted organization runs all projects on an
installation.** A project namespace is "as good as multi-tenant" *for our purposes* because we do
not need to defend a trust boundary between mutually-hostile workloads.

That single fact deletes most of `agentkit`'s reason to exist:

| `agentkit` machinery | Why it exists in Platinum | Needed for Agent Orange? |
|---|---|---|
| Docker-in-Docker, per-session container isolation | Untrusted, multi-tenant sessions | **No** — namespacing/container is plenty for one trusted org |
| Hardened isolation tests, egress allowlists | Defend tenant↔tenant + tenant↔host | **No** — same-org trust |
| Image registry push/pull + GC, blobarchive snapshot seams | Burn & redistribute customer images at SaaS scale | **Mostly no** — an off-the-shelf runtime's image + snapshot model covers it |
| Model proxy (container never holds the key) | Secret-isolation across tenants | **Optional** — nice hygiene, not a trust requirement |
| Fleet placement, idle-reaper, archive loops | SaaS density & cost control | **Later, if ever** |

Everything in that right-hand column is the "undifferentiated heavy lifting" `AGENTS_RESEARCH.md`
§3 already told us to shed. Single-tenancy is what makes shedding it *safe*.

---

## 3. Layer 1 — the single-agent execution environment (BUY / BORROW, don't finish building)

**Requirement:** a sandboxed Linux env where a harness (system prompt + event history + configured
tools/skills) runs bash, reads/writes files, writes & runs code, installs deps; environment defined
as a Docker image; session can be paused/snapshotted/resumed days later; self-hostable; single-tenant.

### 3.1 Recommended: OpenHands Software Agent SDK (self-hosted) — *high confidence, primary sources*

The only candidate that reached **high-confidence, primary-source** verification on every column we
care about:

- **What it is:** MIT-licensed open-source agent runtime; client-server, each agent instance in its
  own Docker container with a dedicated FS/env; in-container `ActionExecutor` runs shell, file ops,
  and Python/code "safely within the container." Official Docker images bundle the full agent-server
  stack. *(arXiv:2511.03690; docs.openhands.dev/.../runtime; .../docker-sandbox; GitHub LICENSE.)*
- **Environment = Docker image (exactly your format):** you give it a **custom base Docker image**;
  it builds a derived "OH runtime image" on top (adds the runtime client) and launches that as the
  session container. `DockerWorkspace` auto-pulls/builds the image, starts the agent-server, waits
  for readiness, cleans up. **This is the same image-layering idea as Agent Orange's
  `installations/` (sandbox → core → example → per-project) — already implemented for us.**
- **Native pause/snapshot/resume:** pausing persists state and emits a `PauseEvent`; conversations
  resume "from the same point later" by reloading `base_state.json` and **replaying a per-event JSON
  directory**, auto-detecting incomplete conversations. *(docs: convo-pause-and-resume;
  convo-persistence.)* — i.e. the thing `agentkit`'s blobarchive snapshot seam was being wired up
  to do, but turnkey.
- **Single-tenant fit:** isolation is Docker-container namespacing — *not* a hardened multi-tenant
  boundary. That's a **liability for a SaaS and a perfect fit for one trusted org.**

> **One open question to verify before committing (research flagged it):** does OpenHands' pause
> snapshot the **container filesystem** (installed deps + working-dir artifacts), or only the
> conversation event log + `base_state.json`? If only the latter, then "resume days later with my
> previously-installed packages intact" needs either (a) committing the container to an image on
> pause, or (b) a persistent volume. This is the one place your Platinum instinct — *snapshot every
> overlay, not just git-committed files* — may need a small custom seam on top of OpenHands. Worth a
> spike. (See §3.3 Morph for an alternative that snapshots full memory+FS by design.)

### 3.2 Alternative A — Claude Agent SDK as the harness + our own persistence — *high confidence*

- One session = one `claude` CLI subprocess owning shell/cwd/JSONL transcripts. *(platform.claude.com
  /docs/agent-sdk/hosting.)* This is the same SDK `agentkit`'s harness already wraps — so it's the
  "borrow the harness, keep our seam" path.
- **Caveat that matters:** state does **not** survive container restart/scale-down/move by default.
  Durable resume requires wiring a `SessionStore` adapter (S3/Redis/Postgres) for transcripts **plus
  separate** storage for `CLAUDE.md` memory and working-dir artifacts (SessionStore mirrors
  transcripts *only*). There's a documented "hybrid" pattern (hydrate-on-startup, spin-down-on-idle,
  resume-by-id) explicitly aimed at workloads like "deep research that pauses and resumes over hours."
- **Net:** this is essentially *what `agentkit` is* (harness + host-built persistence). Choosing it
  means we keep owning the persistence layer — i.e. keep the part we're trying to stop owning.
  Prefer §3.1 unless we want harness-level control OpenHands won't give.
- **Auth note (carried from `AGENTS_RESEARCH.md` §2.3, still true):** use an **Anthropic API key**,
  not a Max subscription, for headless/automation.

### 3.3 Alternative B — rent a managed sandbox (if we'd rather not self-host a runtime at all)

Coverage here was **uneven** — most provider specifics came from blog/secondary sources and did
*not* survive adversarial verification, so treat the following as **leads to confirm against primary
docs**, not settled facts:

- **Morph / MorphCloud (Infinibranch):** Firecracker microVMs; **snapshot/branch/restore entire VM
  state (memory + filesystem) in <250ms**, auto-suspend after idle. This is the closest match to
  your Platinum-style "snapshot every overlay" instinct — full-state, not just an event log. Managed.
  *(morphllm.com — blog/vendor; verify.)*
- **E2B:** Firecracker microVM sandboxes; **stateful across calls** within a session (variables/
  dataframes/models persist across turns) — *this one was confirmed*. Broader claims (exact isolation
  model, self-hostability via Apache-2.0 Terraform, ~1s full-state resume) were **unverified** here.
- **Daytona:** advertised as self-hostable, OCI containers, environment snapshots, ~$0.067/vCPU-hr —
  **unverified**, check primary docs.
- **Modal:** filesystem + memory snapshots, sub-second cold start, **not** self-hostable (managed only).
- **Fly Machines / Cloudflare Containers / Northflank / Coder:** not robustly characterized in this
  pass; `AGENTS_RESEARCH.md` §4 already sketches their tradeoffs (Fly = most "your own server"
  control + suspend/resume; CF = bursty/scale-to-zero, ephemeral FS; etc.).

**Recommendation within layer 1:** default to **OpenHands self-hosted** (§3.1) — it's the cheapest
way to get the Docker-image env format + native resume + self-host + single-tenant fit, and it maps
cleanly onto our existing `installations/` image layering. Keep **Morph** in your pocket as the
"buy" option if the full-memory-snapshot/branch semantics turn out to be a hard requirement and we'd
rather rent that than build it on OpenHands.

---

## 4. Layer 2/3 — the orchestration / management layer (BUILD THIN; steal patterns)

**This is the part with no off-the-shelf answer, and therefore the part worth owning.** The research
confirms: nobody sells the full loop you described (goal → human interview → spec → plan workstreams
→ assign workers with scoped prompts/tools/memory → drive a kanban board → react on cron + events →
escalate to human). Every candidate covers a *slice*:

| System | Covers | Gap vs. your vision | Self-host | Verdict |
|---|---|---|---|---|
| **Magentic-One / AutoGen** (MS) | Orchestrator does task decomposition + planning, directs workers, **Task Ledger + Progress Ledger** dual loop, re-plans on error. *Confirmed, primary.* | No long-running/continuous op, no dynamic role definition, **no board**, no cron/event triggers (docs explicitly silent). | Yes (OSS) | **Steal the pattern** (the two-ledger orchestrator) — not the runtime. |
| **Ruflo / Claude Flow** (`ruvnet/ruflo`) | Multi-agent meta-harness for Claude; swarm topologies; **long-running via auto-triggered workers + loop-workers timer (cron-ish)**; runtime agent spawn via `agent_spawn` MCP. *Confirmed, primary.* | Pre-defined specialist agents, not arbitrary runtime role creation; no board/ticket model; no spec-interview. | Yes | **Watch / harvest ideas.** Closest to "continuous + spawns workers." |
| **`saltbo/agent-kanban`** | **Closest board-driven thing that exists:** continuously-running daemon polls a Todo→In Progress→In Review→Done board, provisions worktrees, installs skills, **spawns a worker agent per task**; agents are first-class board members with identity/role/skills. *Confirmed, primary.* | Coding-agent focused; not goal→spec interview; not business-goal orchestration. | Yes | **Closest reference architecture** for the board + worker-per-ticket daemon. Study it hard. |
| **MindStudio "Agentic OS" Command Center** | Kanban board organized **around business goals** (queued / in-progress / completed / **needs-attention**) — needs-attention ≈ your human-escalation column. | Commercial SaaS; not the open primitive. | No (SaaS) | **UX reference** for goal-as-card + escalation lane. |
| **MetaGPT / ChatDev** | One-line goal → artifacts via **fixed** professional roles (PM/architect/engineer/…). *ChatDev confirmed one-shot, static roles.* | **Static, predefined waterfall pipelines** — explicitly *not* dynamic hiring or continuous op. | Yes | Evidence for what **not** to copy (persona zoo, fixed SOP). Reinforces "scopes not personas." |

**Implication:** build the orchestrator ourselves as a **thin** Magentic-One-shaped loop
(Task Ledger + Progress Ledger), use a **board** as the persisted state (à la `agent-kanban`), and
keep workers as **one archetype under many scopes** (per `AGENTS_RESEARCH.md` §6). The spec-by-
interview step is essentially the **`superpowers:brainstorming` pattern** run by the orchestrator —
we already have that muscle.

---

## 5. Layer 3 — triggering (cron + events)

`AGENTS_RESEARCH.md` §6.4 decided to **hand-roll** cron+events (stateless-between-triggers, re-derive
from ledgers). The research surfaces solid **self-hostable durable-execution** options if/when the
hand-rolled loop hurts (surviving restarts mid-run, multi-day sleeps, retries) — treat these as
*confirmed-by-reputation but rate-limited in our verify pass*, so sanity-check before adopting:

- **Trigger.dev** — open-source (Apache-2.0), **self-hostable**, cron + event triggers, no timeouts,
  retries, human-in-the-loop. Lightest "durable jobs in your own codebase" fit.
- **Inngest** — event-first durable workflows; cron; pause-mid-execution/wait-for-input/resume
  (maps to escalation). Managed, generous OSS dev experience.
- **Temporal** — heaviest, most guarantees, self-hostable (ex-Cadence). Overkill unless we need it.
- **Cloudflare Workflows** — durable step engine on Workers; couples us to CF.

**Recommendation:** start with the **hand-rolled** loop (§6.4 was right for Phase 0 simplicity); the
moment "survive a crash mid-task" or "sleep 3 days waiting on a human" becomes real pain, adopt
**Trigger.dev self-hosted** rather than expanding the hand-rolled loop. Don't build durable execution.

---

## 6. Layer 4 — memory (BUY a memory layer; don't reinvent pgvector retrieval)

`agentkit` has **no memory system** (it persists conversation events + workspace snapshots only).
Two strong off-the-shelf options (both confirmed primary):

- **Letta (formerly MemGPT)** — open-source, **self-hostable agent runtime** with OS-style **tiered
  memory** (Core / Recall / Archival); agents self-manage memory via tool calls. Best if we want the
  *runtime* to own memory.
- **mem0** — **framework-agnostic, pluggable memory layer** exposed as a service/API; integrates with
  LangGraph/CrewAI/AutoGen; multi-level (user/session/agent). Best if we want memory as a sidecar to
  whatever runtime we pick. Strong reported benchmarks (LoCoMo/LongMemEval).
- **Generative-Agents retrieval** (the algorithm, not a product): score =
  **recency (exp decay ≈0.995) + importance (LLM 1–10) + relevance (cosine)**, equal weights. This is
  the ~30-line retrieval function to drop on top of Postgres+pgvector if we'd rather own a tiny memory
  store than run Letta/mem0.

**Recommendation:** if we self-host OpenHands (§3.1), pair it with **mem0** as a pluggable memory
sidecar (cleanest separation), and keep the Generative-Agents scoring formula as the fallback
"build-it-ourselves-in-an-afternoon" option. Either way: **don't reimplement Platinum's embedding
+ hybrid-search stack.**

---

## 7. The recommended stack (one picture)

```
GOAL (vague, human-set)  ──interview──▶  SPEC          [build: orchestrator runs brainstorming pattern]
        │
        ▼
┌─────────────────────────────────────────────────────────────────────┐
│ ORCHESTRATOR (BUILD, thin)  — Magentic-One pattern                    │
│   Task Ledger + Progress Ledger; defines child SCOPES; re-plans       │
│   state persisted as a BOARD (todo/in-progress/review/done/needs-human)│  ← ref: saltbo/agent-kanban
│   escalate_to_human() = a board lane (ref: MindStudio "needs-attention")│
└───────┬──────────────────────────────────────────┬────────────────────┘
        │ spawn-with-scope (one archetype)          │ memory r/w
        ▼                                           ▼
┌──────────────────────────────┐          ┌──────────────────────────┐
│ LAYER 1 RUNTIME (BUY/BORROW) │          │ MEMORY (BUY)             │
│ OpenHands SDK, self-hosted   │          │ mem0 sidecar             │
│  • Docker-image env (= our   │          │  (or Letta runtime, or   │
│    installations/ layering)  │          │   pgvector + gen-agents  │
│  • native pause/resume       │          │   scoring)               │
│  • container-namespace iso    │          └──────────────────────────┘
│    (single-tenant = fine)    │
└──────────────────────────────┘
        ▲
        │ fires on
┌──────────────────────────────────────────────────────────────────────┐
│ TRIGGER (hand-roll first → Trigger.dev self-host when it hurts)        │
│   cron loop + event intake → "load board+memory → 1 orch exchange →    │
│   persist". Orchestrator stateless between triggers.                   │
└──────────────────────────────────────────────────────────────────────┘
```

**What we build:** the orchestrator + board + scope/spawn primitive + escalation lane (layers 2–3).
**What we buy/borrow:** runtime (OpenHands), memory (mem0), durable triggering (Trigger.dev, later).
**What we delete/abandon from the `agentkit` hoist:** DinD isolation, multi-tenant hardening, image
registry GC, blobarchive snapshot machinery, fleet placement — *unless* a concrete need resurrects a
specific piece.

---

## 8. What this means for the current branch / `MIGRATION.md`

The `phase4-gcp-seams` work (GCS BlobStore, ADC registry auth, backend selection) is **migration of
the very machinery this doc recommends we stop owning.** Before sinking more into it:

- **Pause** further `agentkit`-layer-1 porting (registry/blobarchive/DinD/GCP image plumbing).
- **Spike** OpenHands self-hosted against one real project to validate: (a) does its image model
  accept our `installations/` base images cleanly; (b) does pause/resume preserve installed deps +
  working-dir files (the §3.1 open question), or do we need a thin commit-on-pause seam.
- **Keep** the genuinely reusable, runtime-agnostic bits if any survive (e.g. event vocabulary in
  `go/events/` could still describe the orchestrator's stream; the host-store seams for Postgres).
- **Reframe** `MIGRATION.md`'s GCP goal: we still want GCP, but for **our orchestrator + board +
  memory + a place to run OpenHands**, not for an image-burning registry pipeline.

> This is a redirection, not a teardown. Nothing gets deleted until the OpenHands spike confirms it
> replaces the corresponding `agentkit` piece. But the default direction of new effort should flip
> from "finish porting the runtime" to "build the orchestrator on a bought runtime."

---

## 9. Open decisions for you (the genuinely human calls)

1. **Self-host vs. rent layer 1.** OpenHands self-hosted (recommended) vs. a managed sandbox
   (Morph for full-state snapshot; E2B/Daytona). Driven by: do we want to run our own box, and how
   strict is the "restore the exact filesystem overlay days later" requirement?
2. **How much of `agentkit` to keep.** Full pause of the hoist + adopt OpenHands, vs. a hybrid that
   keeps `agentkit`'s harness/event seams and only drops the registry/DinD parts.
3. **Snapshot fidelity.** Is "resume the conversation + re-pull the base image" enough, or do we
   genuinely need Platinum-grade "every overlay layer preserved"? This decides §3.1-vs-Morph and how
   much custom snapshot code we own.

---

## Appendix — confidence & provenance

- **High confidence (primary sources, 3-0 verified):** OpenHands runtime/MIT/Docker-image-env/
  pause-resume; Claude Agent SDK subprocess model + no-default-persistence; Magentic-One orchestrator
  + dual-ledger pattern (and its silence on board/cron/dynamic-roles); Ruflo continuous + `agent_spawn`;
  ChatDev one-shot/static-roles; `saltbo/agent-kanban` board + worker-per-task daemon; Letta tiered
  memory; mem0 pluggable memory; Generative-Agents retrieval formula.
- **Medium / single-source:** E2B stateful-across-calls (2-0); MindStudio Agentic OS board (blog).
- **Unverified this pass (rate-limited during adversarial verify — treat as leads, confirm before
  adopting):** Temporal / Inngest / Trigger.dev / Cloudflare Workflows specifics; Morph/MorphCloud
  <250ms branch/restore; Daytona self-host + pricing; E2B isolation model + self-hostability; Modal
  snapshots. *Absence of confirmation here ≠ false; it means our verifier hit API rate limits, not
  that the claim was refuted.*
- **Key primary sources:** arXiv:2511.03690 (OpenHands SDK); docs.openhands.dev (runtime,
  docker-sandbox, convo-pause-and-resume, convo-persistence); platform.claude.com/docs/agent-sdk/
  hosting; microsoft.com/research Magentic-One + microsoft.github.io/autogen; github.com/ruvnet/ruflo;
  github.com/saltbo/agent-kanban; github.com/letta-ai/letta; github.com/mem0ai/mem0;
  ar5iv arXiv:2304.03442 (Generative Agents). Full source list in the two research run outputs.
- **Prior internal docs:** `docs/AGENTS_RESEARCH.md` (design seed — scopes-not-personas, layered
  build/buy, reliability rules); `MIGRATION.md` (current agentkit hoist status).
