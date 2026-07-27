# Composition playbook — what we know, and the plan to act on it

*Written 2026-07-27. Status: **distillation + action plan.***
*This is the entry point for the self-improvement workstream. It distils
[`docs/AGENTS_RESEARCH.md`](../AGENTS_RESEARCH.md) (measurement),
[`10-topology-library.md`](./10-topology-library.md) (org charts) and
[`11-learning-stories.md`](./11-learning-stories.md) (deterministic proof) into principles and an
ordered plan. Those three carry the evidence; this one carries the decisions.*

---

## 1. The realisation this document exists to hold

Most of what this research produced is not engine code. It is **knowledge about how to compose an
Agent Orange system so that it can learn** — which workers exist, what tools each holds, what is
frozen, what subscribes to what, what gets measured and by whom. The engine already supports all of
it; the value is in the arrangement.

That makes the arrangement itself the product surface to iterate on. Composition knowledge is
stored as **topologies** (seedable, versioned org charts — doc 10), proven by **learning stories**
(deterministic e2e tests — doc 11), and compared by the **measurement harness** (AGENTS_RESEARCH).
The loop for improving Agent Orange compositions is itself: propose a topology → run it against a
task → measure → keep what wins. We are applying the product's own thesis to the product.

## 2. The composition principles

Each principle: what it says, why we believe it, and where it is actioned.

**C1 — Structure beats model quality as the first lever.**
~79% of multi-agent failures are organisational (specification 42%, coordination 37%), and
role/verification fixes gained +9.4%/+15.6% with the model held constant (MAST). But imposed
structure is not monotonically good: above a capability threshold, fixed-order-with-autonomous-roles
beat both centralised and free-form coordination. So the org chart is a variable to search, not a
constant to enshrine. → *Actioned by the topology library (doc 10) and comparison rig (P5).*

**C2 — The measuring instrument must be causally isolated from the loop it measures.**
Self-graded loops optimise persuasiveness, not quality (judge 72→94% while truth stayed flat at
~20%). A weak-but-frozen metric slows learning; a capturable metric silently corrupts it.
→ *Actioned by frozen workers (P0) and the frozen-scorer harness (topology 12).*

**C3 — Prefer facts to judges; calibrate judges on facts first.**
You cannot reward-hack ground truth. Run the rig on a domain with held-out answers (the hypothesis
lab, with planted nulls and confound traps) before trusting it on unverifiable domains like poetry.
→ *Actioned by P3, deliberately ahead of any creative-domain experiment.*

**C4 — When a judge is unavoidable: de-anchored, blind, ranked, anchored, and not the model
under test.** The five-point grading protocol in AGENTS_RESEARCH §7. Absolute scores drift;
comparison within one context is the reliable operation; anchor items make runs comparable;
self-preference is documented. → *Actioned by the Tier B harness (P7).*

**C5 — Prove transmission deterministically before measuring discovery expensively.**
The scripted mock is prompt-conditioned, so "the critic's rewrite changed the next job's behaviour"
is assertable offline, byte-for-byte, free. A behavioural switch also proves *delivery* (the prompt
reached the model), which a database read-back never can. → *Actioned by the learning stories (P.5,
first thing built).*

**C6 — Every trigger flows through the event spine, so time is simulatable.**
Already true: a cron firing is `CreateProjectEvent` + the shared dispatch gate; the tick only
decides *when*. Tests emit events instead of waiting for clocks. **Standing rule: no future trigger
type may fire through a private path** — the moment one does, its consumers stop being testable
offline. → *Actioned as a review rule, and exploited by every story in doc 11.*

**C7 — Controls or it isn't knowledge.**
Solo, solo+memory, sham-critic arms; multiple seeds; variance reported. An improvement claim that
hasn't beaten the sham critic is motion, not learning. MR-3 (no ghost learning) is the cheapest
false-green detector we have. → *Actioned by topology family A and the MR relations in doc 11.*

**C8 — Refusals are signals.**
A worker attempting to edit its frozen scorer is the reward-hacking hypothesis observed in the
wild. Count refused mutations against frozen workers and surface them. → *Actioned in P0.*

## 3. The plan

Ordered by dependency and by cost-of-being-wrong; each phase produces something usable on its own.
Owner of the tick-boxes: this file.

- [ ] **P.5 — Learning stories 1–6, 8** (doc 11). Offline, zero new schema, zero tokens. Gates
      merges from day one. *The only phase with no prerequisites.*
- [ ] **P0 — Frozen workers** (doc 10 §3): `Worker.Frozen` (no gorm default tag), MCP-boundary
      refusal in `mcp_management.go`, JWT-path freeze/unfreeze, config-logged, UI lock badge,
      refused-attempt counter. Then story 7.
- [ ] **P1 — Topology as data** (doc 10 §2): questions → rendered bundle → preview diff → applied
      as ordinary config mutations, named and versioned like skills.
- [ ] **P2 — Seed four topologies**: Solo, Actor–Critic, Supervisor, Frozen-scorer harness.
- [ ] **P3 — Hypothesis lab + calibration** (AGENTS_RESEARCH §6): synthetic datasets with held-out
      truth, trap taxonomy (planted nulls, confounds, underpowered samples). First real-model
      measurement; proves the instrument can see improvement we know is real.
- [ ] **P4 — Remaining seed topologies** (doc 10 §4): controls 2–3, work topologies 6–11.
- [ ] **P5 — Comparison rig**: one task × N topologies × multiple seeds, ranked with variance.
- [ ] **P6 — Onboarding flow**: empty project → pick topology → answer questions → preview → apply.
- [ ] **P7 — Tier B graded harness** (AGENTS_RESEARCH §7): same stories, real model, second-model
      grader, anchor items, nightly/on-demand curve — never a CI gate.

Cross-cutting cautions, all learned the hard way in this repo: assert on happens-after signals,
never sleeps; multi-round suites must release sessions or they drain the port pool; subscription-
billed unattended runs are a terms question before they are a technical one (AGENTS_RESEARCH §1).

## 4. What "done" looks like

Not a winning org chart. Done is: **composition knowledge lives in versioned topologies; every
claim about a topology is either gated by a deterministic story or measured by the frozen
instrument; and re-ranking the library against a new task or a new model is cheap.** At that point
the question Kai asked — *what is the most effective arrangement?* — stops being a debate and
becomes a query.
