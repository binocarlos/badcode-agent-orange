# Autonomous Agent Organization — Research & Design Seed

**Date:** 2026-06-20
**Status:** Research seed for a NEW repository (not Platinum work). Portable by design.
**Purpose:** Capture everything discovered in the design thread that produced it, so a
fresh repo can be populated from this single file. Self-contained: assumes no Platinum
context beyond the clearly-labelled "what we already have" notes.

---

## 0. What this is

A foundational design + research document for a **standalone runtime for long-running,
goal-directed AI agents** — to be used for personal projects (first target: marketing /
growing engagement for a creative/art project; later: a trading bot). It is the output
of a build-vs-buy investigation that started as "should I hoist Platinum's Dockerized
agent infra into a library?" and ended as "this is a different project; here is what it
should be."

**The one-line thesis the whole design rests on:**

> Don't build a company of collaborating AI *personas*. Build **one agent archetype,
> recursively invoked under many scopes**. The unit of difference is not *who the agent
> is* but *what each invocation gets*: which slice of context, which tools, which model.
> **Scopes, not personas.**

---

## 1. The vision

Set an agent a **high-level, open-ended goal** ("maximize engagement for this creative
project") plus a **budget** and a **set of tools**, and have it run **continuously over
time** — planning, decomposing work, doing it, remembering what it did, and pulling a
human in when it needs a resource, a credential, money, or a decision.

Decompose the vision into capabilities:

- A **persistent, goal-directed agent** that operates over weeks/months, not one request.
- **Dual triggering**: scheduled work (cron — "3am, review yesterday's metrics") *and*
  reactive work (an event interrupts — "an email/comment arrived, respond").
- **Persistent memory** of everything it has done and learned.
- The ability to **break work into smaller chunks** and delegate them.
- The ability to **ask a human** for things it can't do itself.
- A **containerized Linux environment** the agent can operate (install software, clone
  repos, run code) to actually get work done.

---

## 2. Executive summary (the conclusions, up front)

1. **Don't re-build commodity infra.** The need decomposes into ~4 layers; only one of
   them (the coordination/agent logic) is differentiated. Rent the rest.
2. **Keep the single-agent runtime; it's the reliable unit.** A containerized agent
   running the **Claude Agent SDK** is the proven atom. Everything else orchestrates it.
3. **Use an API key, not a Claude Max subscription.** As of Feb 2026 Anthropic bans
   subscription OAuth for the Agent SDK / headless automation. Budget pay-per-token.
4. **The multi-agent "org of collaborating staff" is the most failure-prone design that
   exists today** (verified research, §5). Reliability comes from a *single coherent
   intelligence recursively scoped*, not a team of peers negotiating.
5. **Steal, don't invent, the solved parts**: the supervisor/orchestrator loop
   (Magentic-One), and long-lived memory (MemGPT tiers + Generative-Agents retrieval).
6. **Our genuinely novel + risky surface** is three things, to be de-risked carefully and
   built *last*: dynamic agent **spawning/"hiring"**, **dual cron+event triggering on a
   long-running agent**, and **human-in-the-loop escalation as a first-class primitive**.
7. **Build the novel parts in the simplest setting first** (one agent, no spawning),
   prove reliability, then add coordination, then parallelism, then dynamic hiring.

---

## 3. Build vs. Buy — the layered decomposition

The need splits into four layers. Map each to build/buy:

| Layer | What it does | Verdict |
|---|---|---|
| **1. Single-agent runtime ("the exchange")** | Given accumulated state + a trigger, run one agent turn-loop to completion. Container the agent operates. | **Keep/own** — the Claude Agent SDK in a container. The reliable unit. |
| **2. Coordination / orchestration** | Plan toward the goal, decompose into chunks, delegate, track progress, re-plan on failure, hold memory. | **Build (thin)** — this is the differentiation. Steal the *pattern*. |
| **3. Triggering / scheduling** | Fire the agent on cron + on async events. | **Self-build (hardcode)** — decided in this thread; see §6.4 + caveat. |
| **4. Storage / state** | Persist memory, ledgers, artifacts. | **Buy/standard** — Postgres + object storage. |

**Why not just hoist Platinum's Docker orchestration?** Because it is tuned for the
wrong *shape*: interactive, multi-tenant, image-burning SaaS sessions. These new
projects are headless, single-tenant, cron/event-triggered background agents. Reusing
Platinum's heavy machinery would force us to *also* build cron + memory + spawning
(which it lacks — see §8) while inheriting maintenance tax (DinD networking, image GC,
egress allowlists, isolation tests) that is exactly the undifferentiated heavy lifting a
provider/standalone design should shed. We may still **reuse agent-library *code*** for
layer 1 (see §8) — but as a borrowed foundation in a new repo, not as a host.

---

## 4. The infrastructure tool landscape (what each tool actually does)

Reference for picking layer components. (Sandbox host is only needed if/when we want a
managed persistent Linux box instead of running our own container.)

### Layer 1 candidates — the sandbox / Linux box the agent operates
- **Daytona** — dev-environment-as-a-service repositioned for AI agents. API/SDK spins
  up a full isolated Linux workspace. **Persistent by default** (like a Codespace);
  state survives between runs. Best fit for "install software, clone repos, continue
  where I left off." Fast cold start.
- **E2B** — sandboxes purpose-built for agents to run code, Firecracker microVMs (strong
  isolation). **Ephemeral** (~1h hobby / 24h pro, then reset). Best for transactional
  "spin up, run, grab result, tear down."
- **Fly.io (Fly Machines)** — general cloud infra: fast-booting Firecracker microVMs,
  run any Docker image, **persistent volumes**, suspend/resume. Most "your own server"
  control; more plumbing to own.
- **Cloudflare Containers/Sandbox** — scale-to-zero, ephemeral filesystem (state goes in
  Durable Objects / R2 *alongside*, not in the container). Max ~4 vCPU / 12 GiB. Billed
  per 10ms with idle `sleepAfter`. Great for **bursty** cron work, poor for always-on /
  persistent-box / "commit-my-work" needs.

### Layer 3 candidates — durable cron + event triggering
*(Decided in this thread: self-build / hardcode instead — see §6.4. Listed for context
and as a fallback if hand-rolled triggering proves painful.)*
- **Inngest** — event-first durable workflows, fully managed; functions triggered by
  events or cron; durable steps (retries, sleeps for days, fan-out, concurrency caps).
- **Trigger.dev** — durable background jobs/tasks defined in your codebase; cron + event
  triggers, no timeouts, retries, observability; **open-source + self-hostable**.
- **Temporal** — heavyweight, battle-tested durable execution; most guarantees, most ops.
- **Cloudflare Workflows + Durable Objects + Cron Triggers** — one-vendor integrated
  stack; cheapest for bursty work; couples you to CF.

### Layer 4 — storage
- **R2** (Cloudflare) — S3-compatible object storage, **no egress fees**. For files,
  artifacts, snapshots, memory blobs. Swappable (S3 API).
- **Postgres (+ pgvector)** — structured state: ledgers, memory stream, embeddings,
  agent/run registry.

---

## 5. Multi-agent research findings (verified, 2025–2026)

These were gathered via a fan-out research pass and **adversarially verified** (each
claim voted by 3 independent checkers; only claims that survived are below). Sources at
the end of this section.

### Feasibility is sobering
- **Berkeley MAST study** (1,600+ annotated traces across 7 frameworks): failure rates
  **41%–86.7%**; ChatDev correctness as low as **25%**; gains over a *single* agent
  "often minimal." Failures split into three structural categories:
  **specification/design (~42%)**, **inter-agent coordination breakdown (~37%)**, and
  **task verification (~21%)**. **Simple fixes (better role prompts, tweaks) are
  insufficient** (+15.6% for ChatDev, still unreliable). [1]
- **Cognition, "Don't Build Multi-Agents":** running agents collaboratively on
  interdependent *write* work is fragile — context can't be shared thoroughly, decisions
  get dispersed, miscommunications **compound** rather than cancel. Crucially their
  failure example was **two copies of the same agent** — so shared *identity* does not
  rescue parallel branches. [2]

### But the danger is scoped — not a universal law
- The fragility is specific to **interdependent write tasks run in parallel**.
  **Read-heavy / breadth-first parallel** decomposition is the recognized exception
  (Anthropic's multi-agent research system). **Depth-first sequential** work is safe.

### Two tempting claims were REFUTED by verification (do not rely on them)
- ❌ "A better base model won't fix multi-agent failures." → **Model quality DOES matter.**
- ❌ "Memory is purely a retrieval problem, not storage." → **Storage architecture also matters.**

### Strong, steal-able prior art for the *parts*
- **Coordination → Magentic-One / Semantic Kernel "Magentic":** a lead/orchestrator
  agent plans, maintains shared context, tracks progress via an outer-loop **Task Ledger**
  + inner-loop **Progress Ledger**, delegates to specialists, and **re-plans to recover
  from errors**. Explicitly built for goals "where the solution path is not known in
  advance." This is *the* coordination-layer blueprint. [3][4]
- **Persona/role + org structure → MetaGPT, ChatDev:** assign fixed professional roles
  (CEO/CTO/PM/engineer/designer/reviewer); take a one-line goal to a chain of artifacts.
  **But** both use *fixed, predefined pipelines* ("Code = SOP(Team)"; chain-shaped
  waterfall) — **not** dynamic hiring. [5][6]
- **Long-lived memory → MemGPT/Letta + Stanford Generative Agents:**
  - MemGPT/Letta: OS-style **tiered memory** — small in-context "core" memory the agent
    self-manages vs. external conversational/archival/file tiers = unlimited memory
    within a fixed window. [7]
  - Generative Agents: a **memory stream** (timestamped natural-language records) with a
    directly implementable **retrieval score = weighted sum of recency (exp decay
    ≈0.995) + importance (LLM-scored 1–10) + relevance (cosine similarity)**, equal
    weights by default. [8]

### What is genuinely novel / under-served (our risk concentrates here)
1. **Dynamic spawning / "hiring."** Every surveyed framework uses a **static roster**;
   managers *select/route* among predefined agents — **none create new workers at
   runtime.**
2. **Dual cron + event triggering on a long-running agent.** An infra/durable-execution
   concern; canonical frameworks are request/response or single-task pipelines.
3. **Human-in-the-loop escalation as a first-class org primitive** (request credentials,
   approve spend, make a decision) — not evidenced in surveyed frameworks.

**Sources:**
[1] MAST — https://arxiv.org/abs/2503.13657 ·
[2] Cognition — https://cognition.ai/blog/dont-build-multi-agents ·
[3] Magentic-One — https://arxiv.org/abs/2411.04468 ·
[4] Semantic Kernel Magentic — https://learn.microsoft.com/en-us/semantic-kernel/frameworks/agent/agent-orchestration/magentic ·
[5] MetaGPT — https://github.com/FoundationAgents/MetaGPT ·
[6] ChatDev — https://github.com/OpenBMB/ChatDev ·
[7] Letta/MemGPT — https://www.letta.com/blog/benchmarking-ai-agent-memory/ ·
[8] Stanford Generative Agents — https://arxiv.org/abs/2304.03442

---

## 6. The architecture: "Scopes, not personas"

### 6.1 The core reasoning (why scopes, not personas)

- **Collapsing the persona zoo is correct.** Reliability comes from one coherent thread
  of context (per Cognition). A single agent identity that recursively decomposes work —
  children inheriting the parent's reasoning — is the cleanest implementation of "share
  full agent traces."
- **But you cannot merge all context into one "all-knowing being."** Context windows are
  finite. The moment you spawn a worker you are *forced* to choose what slice it sees —
  that choice **is** the namespacing problem. It's essential complexity, not incidental.
- **The benefit and the cost are the same coin.** A leaf only *saves* anything (context,
  money, focus) by being **less than the root**: less context, cheaper model, fewer
  tools. If a leaf were truly all-knowing it would just be the root again — no saving.
  So you cannot keep the savings and delete the scoping.
- **Therefore: collapse *identity*, keep *scope*.** One archetype, invoked under many
  scopes. The leaf is the same being, given less.
- **Three things "persona" was secretly doing that we still need** (so don't naively give
  every spawn the full identity):
  1. **Tool scoping = blast radius** (never hand spend/irreversible tools to a leaf).
  2. **Prompt focus = spec quality** (MAST's #1 failure is over-broad specs; a narrowed
     objective is a reliability technique, not a personality).
  3. **Memory continuity by function** (memory must still be scoped by domain on
     retrieval — namespacing you can't escape; solved via the shared store below).

### 6.2 The four layers

```
┌──────────────────────────────────────────────────────────────────────┐
│ 4. TRIGGER / DURABILITY  (SELF-BUILD — hardcoded cron + event loop)    │
│    • cron fires the orchestrator on a schedule                          │
│    • async events (webhook/email/queue) interrupt it                    │
│    • each trigger = load persisted state → run one orchestrator exchange│
│      → persist. (Caveat on durability: §6.4.)                           │
└───────────────┬────────────────────────────────────────────────────────┘
                ▼
┌──────────────────────────────────────────────────────────────────────┐
│ 3. ORCHESTRATOR  (BUILD, pattern = Magentic-One)                        │
│    • holds the GOAL; owns Task Ledger (plan) + Progress Ledger (state)  │
│    • decides next chunk; defines child SCOPES; re-plans on failure      │
│    • SHARED MEMORY store with scoped retrieval (BUILD)                   │
│    • escalate_to_human(...) tool (BUILD)                                 │
└───────────────┬────────────────────────────────────────────────────────┘
                │ spawn-with-scope
                ▼
┌──────────────────────────────────────────────────────────────────────┐
│ 2. EXCHANGE / SPAWN PRIMITIVE  (BUILD thin wrapper over layer 1)        │
│    • create a child invocation of the SAME archetype under a Scope      │
│    • returns { result, full_trace, memory_writes[] } to parent          │
└───────────────┬────────────────────────────────────────────────────────┘
                ▼
┌──────────────────────────────────────────────────────────────────────┐
│ 1. SINGLE-AGENT RUNTIME  (KEEP/OWN: Claude Agent SDK in a container)   │
│    • given state + trigger msg, run one turn-loop to completion         │
│    • API key (NOT Max). Candidate foundation: agent-library code (§8).  │
└──────────────────────────────────────────────────────────────────────┘
```

### 6.3 The Scope contract (layer 2)

```
Scope {
  objective:  string         // narrowed task ("draft 3 launch posts about X")
  context:    ContextSlice    // parent-trace excerpt + memory retrieved for THIS objective
  tools:      ToolPolicy      // allowlist; NEVER spend/irreversible at leaves
  model:      ModelTier       // full | mid | cheap — downgrade toward leaves
  budget:     Budget          // token ceiling, spend ceiling, max depth
  return:     ResultSchema     // structured result the parent expects
}
// spawn returns { result, full_trace, memory_writes[] }
```

### 6.4 Orchestrator, memory, triggering, escalation

- **Orchestrator (Magentic-One pattern):** Task Ledger (plan/what's known) + Progress
  Ledger (done/in-flight/blocked/needs-human). The decision is "what's the next scope to
  spawn," depth-first by default. On failure, **re-plan** rather than retry-in-place.
- **Shared memory (ONE store, scoped retrieval):** append-only memory stream
  (`{timestamp, importance, embedding, source_scope}`); per-spawn retrieval by
  recency·importance·relevance; tiered working memory (MemGPT-style) for the long-running
  orchestrator so it never blows its window. Postgres + pgvector is the natural home.
- **Triggering — SELF-BUILT (decision):** cron + events will be **hardcoded**, not a
  bought durable engine. Keep it simple: a scheduler loop + an event intake that both
  resolve to "load state → one orchestrator exchange → persist."
  - **Honest caveat (the trade-off being accepted):** a hand-rolled loop gives up what
    durable-execution engines provide for free — surviving process restarts mid-run,
    multi-day sleeps, and automatic retries. **Mitigation:** make the orchestrator
    **stateless between triggers** and **fully re-derive from persisted ledgers + memory
    on each fire.** Never hold long-lived in-process state across a sleep. If/when long
    human-wait windows or mid-task crash-recovery become painful, that's the signal to
    revisit Trigger.dev/Temporal (§4).
- **Human-in-the-loop escalation (first-class):**
  `escalate_to_human({request, options?, blocking?}) → HumanResponse`. Pauses the branch,
  notifies out-of-band (email/Slack), resumes on response. **Mandatory** for credentials,
  spending money, irreversible/external actions, or out-of-mandate decisions. With
  hardcoded triggering, "blocking" escalation = persist a `needs-human` ledger entry and
  end the exchange; resume on the human's reply event.

---

## 7. Reliability rules (baked into the design)

1. **One archetype, many scopes** — no persona zoo.
2. **Depth-first by default; parallelize only independent / read-only subtasks** (the
   Anthropic-blessed exception). Never parallel interdependent writes.
3. **Share full traces** down to children and up to the parent — not just messages.
4. **Verify every delegated write** (parent or an independent verify scope) before it
   enters the progress ledger. ~21% of failures are verification failures.
5. **Tool scope = blast radius.** Spend/irreversible/credential tools never reach leaves;
   they bubble up to escalation.
6. **Bounded autonomy** — every branch carries token + spend + depth budgets. Caps
   runaway decomposition and runaway "hiring" structurally.
7. **Model downgrade only at mechanically-verifiable leaves**, paired with a verify step.

---

## 8. Relationship to `agent-library` (candidate foundation for layer 1)

`agent-library` is the existing (Platinum-internal) reusable runtime. An infrastructure
map of it (what it provides vs. what it lacks) — relevant to deciding what *code* to
borrow into the new repo for layer 1:

**Provides (reusable for layer 1):**
- **Pluggable harness seam**; currently one impl wrapping the `@anthropic-ai/claude-agent-sdk`.
- **ExecutionEnvironment** adapters (Docker / DinD / Kubernetes) with **snapshot/restore**
  and commit-to-image ("burn").
- **Model proxy** (injects the real API key; container never holds it; per-session header
  routing).
- **Orchestration core** (Runner), **fleet placement**, **event pipeline**, and a
  host-implemented **SessionStore** seam (Platinum backs it with Postgres).

**Does NOT provide (we must build — and these are exactly our novel primitives):**
- **No cron/scheduler** — only internal idle-reaper + archive loops. (Our layer 3.)
- **No memory system** — persists conversation events + workspace snapshots, but no
  searchable persona/long-term memory. (Our layer 3 memory.)
- **No sub-agent spawning** — there's a `SubagentEvent` hook for the UI, but no primitive
  to spawn/route/aggregate child agents. (Our layer 2/3 spawn-with-scope + orchestrator.)
- **No egress control** — left to Docker/K8s network policy or a sidecar.

**Implication:** agent-library is a strong candidate for **layer 1** (the containerized
Claude-Agent-SDK runtime + snapshot/restore + model proxy) but contributes **nothing** to
layers 2–4 — which is the whole point of the new project. Borrow the runtime; build the
coordination/memory/triggering/escalation ourselves.

---

## 9. Roadmap (novel/risky parts LAST, on a proven base)

First use case (decided): **art-project marketing** — promote/grow engagement for the
creative project. Chosen as a lower-stakes first target (content/posting/outreach), with
a gentler escalation surface than the trading bot.

- **Phase 0 — One agent, long-running.** Single archetype, **dual-triggered** (hardcoded
  cron + event), **shared memory**, `escalate_to_human`. **No spawning.** Proves the
  least-prior-art primitives in the simplest form. If this isn't reliable, nothing above
  it will be.
- **Phase 1 — Orchestrator + depth-first decomposition.** Add Task/Progress ledgers +
  spawn-with-scope, **sequential only**, same-archetype workers with scoped
  context/tools/model, **verify step on every delegated write.**
- **Phase 2 — Bounded parallel fan-out.** Parallelize **independent / read-only**
  subtasks only (research, gather, summarize). Never interdependent writes.
- **Phase 3 — Dynamic spawning ("hiring").** Orchestrator defines *new* worker scopes at
  runtime from scope templates — the genuinely novel primitive — behind budget caps,
  depth limits, and mandatory verification. **Last**, because it's the largest unbounded-
  failure surface, stacked on a layer that is fragile even when static.

---

## 10. Decisions made in this thread + open questions

**Decided:**
- New **standalone repo**, not a hoist into/of Platinum. (May reuse agent-library *code*
  for layer 1 — §8.)
- **First use case: art-project marketing.**
- **Triggering: self-built / hardcoded** cron + events (not Inngest/Temporal/etc.) — with
  the statelessness mitigation in §6.4.
- **Auth: Anthropic API key**, not a Max subscription.
- **Architecture: scopes-not-personas**, orchestrator-over-homogeneous-workers, shared
  memory, depth-first-default + verify, staged roadmap.

**Open (resolve before/while building):**
1. **Layer 1 host** — run our own container (borrowing agent-library code) vs. a managed
   persistent-workspace provider (Daytona / Fly). Driven by whether the marketing agent
   needs a persistent install-stuff box or just bursty runs.
2. **Memory store** — fresh Postgres+pgvector for the new repo (the steal-able embedding
   + hybrid-search patterns exist in Platinum and can be reimplemented cleanly).
3. **The art-marketing tool surface** — which concrete tools/MCP servers (social posting,
   image generation, analytics, email) and which of them require escalation.
4. **Model tiers per depth** — which Claude models at root vs. leaves; confirm cost
   envelope for a continuously-running agent.

---

## Appendix — full source list

- Magentic-One — https://arxiv.org/abs/2411.04468
- Semantic Kernel "Magentic" orchestration — https://learn.microsoft.com/en-us/semantic-kernel/frameworks/agent/agent-orchestration/magentic
- MAST (multi-agent failure taxonomy) — https://arxiv.org/abs/2503.13657
- Cognition, "Don't Build Multi-Agents" — https://cognition.ai/blog/dont-build-multi-agents
- MetaGPT — https://github.com/FoundationAgents/MetaGPT
- ChatDev — https://github.com/OpenBMB/ChatDev
- Letta/MemGPT memory benchmarking — https://www.letta.com/blog/benchmarking-ai-agent-memory/
- Stanford Generative Agents — https://arxiv.org/abs/2304.03442
- Cloudflare Containers pricing/limits — https://developers.cloudflare.com/containers/pricing/
- Anthropic subscription-auth restriction (context) — https://code.claude.com/docs/en/authentication
