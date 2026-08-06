# Topology library — research and plan

*Written 2026-07-27. Status corrected 2026-07-29: **BUILT.** 14 seeds ship in `go/topology/`, with
the registry, renderer, preview/apply and the UI onboarding flow; frozen workers are enforced at the
MCP seam. The design below stands as written — only the "nothing is built" claim was stale.*
*Companion to [`docs/AGENTS_RESEARCH.md`](../AGENTS_RESEARCH.md), which covers how to measure whether
any of this works. Read that first — this document assumes its vocabulary (frozen scorer, held-out
set, judge–truth divergence).*

---

## 1. The premise

> Nobody quite knows what the most efficient org chart is for any given process, and it is going to
> be different for each process. We cannot fix on one.

The literature supports this more strongly than I expected, and from two directions.

**Structure dominates model quality.** [MAST](https://arxiv.org/html/2503.13657v2) annotated 1600+
failure traces across 7 multi-agent frameworks and found failures cluster as **42% specification
issues, 37% inter-agent misalignment, 21% weak verification** — that is, overwhelmingly
*organisational*, not model-capability, failures. Their interventions bear this out: **+9.4% from
improved role definition alone**, and **+15.6% from adding verification at the task-objective level
rather than only at the output level** — same underlying model in both cases. Their conclusion is
that better models are "insufficient alone to guarantee reliable MAS performance."

**But designed structure is not automatically the answer.** The honest counter-evidence is
[Drop the Hierarchy and Roles](https://arxiv.org/html/2603.28990), 25,000+ task runs, which found a
hybrid *Sequential* protocol — fixed agent ordering, but autonomous role selection — beat
centralised coordination by **+14%** at 16 agents and fully-autonomous coordination by **+44%** at 8
agents. Critically, they also found a **capability threshold**: below it the effect reverses and
rigid structure wins (GLM-5 scored −9.6% under free-form vs fixed-role operation). So the right
amount of imposed structure depends on the model, not just the task.

Taken together these justify the library rather than undermining it. **The goal is not to ship the
correct org chart. It is to make org chart a cheap, comparable, swappable variable** — because the
evidence says the answer varies by task *and* by model, and will keep moving as models change.

**This is also the seed population for automated search.** The state of the art in topology
optimisation — [ADAS](https://arxiv.org/html/2606.27492v1) (meta-agent programs new agents,
maintaining *an archive of discovered designs*), [AFlow](https://arxiv.org/pdf/2502.04180) (MCTS over
typed operator graphs), [GPTSwarm](https://www.researchgate.net/publication/388686619) (optimises edge
probabilities), MaAS (probabilistic supernet) — all search *from* a starting population. A curated
library is not a rival to that; it is the archive those methods would begin from, and the baseline
they would have to beat.

---

## 2. What a topology is, concretely

Agent Orange already has everything a topology needs to be expressed in. A topology is not a new
storage type — it is **a parameterised generator over the existing configuration surface**:

| Primitive | Where | What a topology sets |
| --- | --- | --- |
| `Worker` | `agentdb/workers.go` | `Name`, `Description`, `SystemPrompt`, `MCPConfig` (which tools this role holds), `Image`, `Briefing` (label selectors — what memory it sees), `MaxInstances`, `Enabled` |
| `Subscription` | `agentdb/events.go` | `EventType` + `Filter` → `Worker`, `MaxFiringsPerHour`. **This is the wiring — the edges of the org chart.** |
| `Schedule` | `agentdb/schedules.go` | `Cron` + `Input` → `Worker`. The heartbeat. |
| `ProjectSettings` | `agentdb/project_settings.go` | `SystemPrompt` (org-wide preamble), `MaxConcurrentJobs`, `DailyTokensSoft`/`Hard`, `AttentionChannel`, `BriefingMaxBytes` |
| `CustomImage`, `Skill` | `agentdb/customimages.go`, `types.go` | The tools and environments roles get |
| Memory labels | `agentdb/memories.go` | The shared-state namespace: who writes what labels, who selects them |

The org chart's **nodes are workers**, its **edges are subscriptions**, its **clock is schedules**,
and its **shared state is labelled memory**. Nothing needs inventing.

Two design commitments follow:

1. **A topology renders to ordinary config mutations.** It writes through the same endpoints a human
   would, so `WithConfigEvent` records every row and the changelog reads "seeded from
   `hypothesis-lab@v1`". P8 holds; instantiation is inspectable, foldable and revertible for free.
2. **A topology is versioned and named like an image or a skill.** Reuse the pattern rather than
   inventing a third naming scheme.

The user-facing shape Kai described — *a few questions whose answers feed a prompt that emits a
configuration* — maps onto this as: `questions → answers → rendered config bundle → preview diff →
apply as one config-event batch`. The preview is not optional; seeding a project must never be a
black box.

---

## 3. Prerequisite primitive: frozen workers

[`AGENTS_RESEARCH.md` §4](../AGENTS_RESEARCH.md) argues that a measuring instrument must be causally
isolated from the loop it measures, and that in *our* architecture `worker_prompt_write` makes any
in-project scorer a legal target. Kai's proposal — a lock on the prompt, surfaced in the UI — is the
right primitive, and the architecture already has the seam to enforce it.

**The seam already exists.** The core MCP server (`cmd/agentd/mcpserver.go`) sits *outside* the JWT
middleware and is authenticated by session token; humans and host apps come through the JWT-guarded
HTTP API. So "workers may not change this; people may" is a single check at one boundary, not a new
permission system.

Sketch:

- `Worker.Frozen bool` — **no gorm `default` tag**, per the standing footgun (a declared default
  makes GORM omit the zero value, so `frozen: false` would silently persist as true).
- `mcp_management.go` refuses `worker_prompt_write`, `worker_update` and `worker_delete` against a
  frozen worker, with an error that explains why rather than just denying.
- The JWT/HTTP path allows freeze and unfreeze; both are config-logged.
- UI: a lock badge and the plain sentence *"Frozen — cannot be changed by other workers."*

**One unplanned benefit worth designing for.** A refused freeze attempt is itself a research signal:
an agent trying to edit the thing that scores it is the reward-hacking hypothesis of
`AGENTS_RESEARCH.md` §2 in its most literal possible form. **Count and surface those attempts** —
they are close to free to collect and would be among the most interesting numbers the whole
experiment produces.

---

## 4. Proposed seed library

Thirteen entries in three families. Families A and C are not optional garnish — without controls and
a measurement rig, a topology library produces opinions rather than findings.

### Family A — Controls

You cannot claim a topology helped without these. All three are cheap.

| # | Topology | Shape | Exists to answer |
| --- | --- | --- | --- |
| 1 | **Solo** | One worker, one schedule. | Does *any* multi-agent structure beat one good agent? Coordination overhead is real and single agents are strong. |
| 2 | **Solo + memory** | One worker, writes and selects labelled memory. | Does improvement come from the second agent, or just from persistence? |
| 3 | **Sham critic** | Actor + critic that rewrites prompts *arbitrarily*. | Does the real critic beat prompt churn? If not, there is no self-improvement — only motion. |

### Family B — Work topologies

| # | Topology | Shape in our primitives | Evidence / caveat |
| --- | --- | --- | --- |
| 4 | **Actor–Critic pair** | Two workers; critic subscribes to actor's completion, holds `worker_prompt_write`. | Literally §8.7. The minimal improvement loop; the thing already proven to close. |
| 5 | **Supervisor / star** | Dispatcher worker fans out by emitting typed events; specialists subscribe. | ~70% of production deployments. Simple, and the default people reach for. |
| 6 | **Assembly line** | Chain: each worker's completion event is the next one's subscription. | The Sequential protocol that beat both centralised and autonomous coordination — *fixed order, autonomous roles*. |
| 7 | **Debate committee** | N workers subscribe to the same event independently; an aggregator judges. | Real gains on reasoning and factual tasks — but collapses into rubber-stamping with weak critics, and is prone to groupthink. Independence must be enforced, not hoped for. |
| 8 | **Blackboard** | No addressing at all: workers subscribe to event types and post to labelled memory. | **Arguably our native topology** — the event spine plus append-only labelled memory *is* a blackboard. Likely the cheapest to seed and the most idiomatic. |
| 9 | **Self-organizing pool** | Workers hold `worker_create`; roles emerge at runtime. | Possible today. Beat designed structures above the capability threshold, lost below it — so this entry is a genuine experiment, not a safe default. |
| 10 | **Temporal hierarchy** | Strategist on a slow cron; operators on fast events; strategist rewrites operator prompts. | The temporal-hierarchy axis of the [HMAS taxonomy](https://arxiv.org/html/2508.12683) — separates long-horizon from tactical. |
| 11 | **Escalation** | Worker + `request_human_attention` + attention channel. | The practical shape for real work (§8.8's marketing manager), and it exercises the `awaiting_human` path. |

### Family C — Experiment topologies

These are how the others get judged.

| # | Topology | Shape | Notes |
| --- | --- | --- | --- |
| 12 | **Frozen-scorer harness** | Loop under test + frozen scorer worker + held-out briefs held outside the project. | `AGENTS_RESEARCH.md` §5, made instantiable. De-anchored pairwise scoring, reference-anchored Elo. |
| 13 | **Hypothesis lab** | Generator → investigator → frozen fact-checker → critic that rewrites methodology prompts. | Kai's red-jumpers/trains design. Synthetic datasets with known truth, including **planted nulls** and **confound traps**. Roles mirror the proposer–critic–ranker shape of Co-Scientist systems. |

**Entry 13 is the one I would build first after the plumbing**, for the reason in
`AGENTS_RESEARCH.md` §6: its scorer is a *fact*, not a model, so it calibrates the instrument before
we trust the instrument on anything unverifiable.

---

## 5. Why the library and the harness need each other

A topology library without measurement is a folder of opinions. A harness without a library has
nothing to compare.

Together they give the experiment its actual shape: **hold the task and the frozen scorer constant,
vary the topology, rank the results.** That is the question Kai posed — *how should Agent Orange
entities be arranged so the self-learning loop is most effective?* — turned into something with an
answer. And per §1 the answer will be per-task and per-model, so the deliverable is a ranking
procedure that stays cheap to re-run, not a winning org chart to enshrine.

---

## 6. Work plan

- [ ] **P0 — Frozen workers.** `Worker.Frozen`, MCP-boundary refusal, JWT-path freeze/unfreeze, config-logged, UI lock badge, refused-attempt counter. Test: a worker holding `worker_prompt_write` cannot modify a frozen worker; a human can.
- [ ] **P1 — Topology as data.** Schema for a topology (questions, defaults, rendered bundle), renderer, **preview diff**, apply-as-one-config-event-batch. Named and versioned like skills.
- [ ] **P2 — Seed four.** Solo (1), Actor–Critic (4), Supervisor (5), Frozen-scorer harness (12). Enough to run the first comparison.
- [ ] **P3 — Hypothesis lab (13) + calibration.** Synthetic dataset generator, trap taxonomy (planted nulls, confounds, underpowered samples), held-out truth. Prove the harness detects improvement we *know* is real.
- [ ] **P4 — Remaining seeds.** 2, 3, 6–11.
- [ ] **P5 — Comparison rig.** One task, N topologies, ranked with variance and multiple seeds.
- [ ] **P6 — Onboarding flow.** Empty project → choose topology → answer questions → preview → apply.

Offline mock-model coverage throughout: `AGENTKIT_MOCK_MODEL_SCRIPT` can drive scripted tool calls,
so topology rendering and the harness mechanics get deterministic tests before spending a token.

---

## 7. Open questions for Kai

1. **Authorable or built-in?** Should topologies be user-created and shareable like skills and
   images, or a curated built-in set at first? (Built-in first is cheaper and defers the sharing and
   trust questions.)
2. **Owned or referenced assets?** Does a topology carry its own images and skills, or only
   reference existing ones by name?
3. **How much autonomy for entry 9?** Self-organizing needs `worker_create` in agent hands. That is
   the entry most likely to produce a surprise in either direction, and the one where a runaway costs
   real money — `MaxConcurrentJobs` and the daily token caps are the existing brakes.
4. **Does unfreezing require anything beyond a human JWT?** A second confirmation, or an audit note?

---

## Sources

- [Why Do Multi-Agent LLM Systems Fail? (MAST)](https://arxiv.org/html/2503.13657v2)
- [Drop the Hierarchy and Roles: How Self-Organizing LLM Agents Outperform Designed Structures](https://arxiv.org/html/2603.28990)
- [A Taxonomy of Hierarchical Multi-Agent Systems](https://arxiv.org/html/2508.12683)
- [Multi-agent Architecture Search via Agentic Supernet (MaAS)](https://arxiv.org/pdf/2502.04180)
- [Automated Design of Agentic Systems](https://arxiv.org/html/2606.27492v1)
- [Multi-Agent Design: Optimizing Agents with Better Prompts and Topologies](https://www.researchgate.net/publication/388686619)
- [Accelerating scientific discovery with Co-Scientist](https://arxiv.org/pdf/2502.18864)
- [LLM-based Multi-Agent Blackboard System for Information Discovery](https://arxiv.org/html/2510.01285v1)
- [The Organizational Behavior of Agentic AI](https://arxiv.org/html/2606.30986v1)
- [Autonomous Topology Mutation: Safe Runtime Restructuring](https://arxiv.org/html/2607.20488)
- [From Static Templates to Dynamic Runtime Graphs: A Survey of Workflow Optimization for LLM Agents](https://arxiv.org/pdf/2603.22386)
