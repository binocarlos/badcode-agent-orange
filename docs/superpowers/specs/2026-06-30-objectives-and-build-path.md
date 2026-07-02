# Objectives & Build Path — Decision Record

**Date:** 2026-06-30
**Status:** Established in a red-team / intent-interview session. This is the *why* and the
*what-for* that should gate every architecture decision in `ARCHITECTURE.md`. The *how-it-runs*
mechanics live in the sibling `2026-06-30-execution-coordination-model.md`.
**Supersedes, for prioritisation purposes,** the docs' framing that the non-code oracle is the
make-or-break risk (see §"The reframe" below).

---

## Objectives (confirmed)

1. **North star — credibility through demonstrated technical prowess.** A *genuinely novel,
   genuinely working* agent-org platform. The platform existing and being coherent IS the payoff.
2. **Genuinely useful + novel, not just satire.** The novelty is what earns the credibility;
   satire alone fails the objective. Art is *inherent* (it runs art-type orgs), not the
   load-bearing claim.
3. **Character:** a satirical engine guidable toward humour. Fallibility is on-brand ("Bad
   Decisions"). Explicitly **not** a ruthless KPI optimiser.
4. **Real deployments that must actually run:** BadCode marketing dept; **AgentWolf** (financial
   research); BadCode political-party satire.
5. **Content/education is a first-class output.** Building *and* running the system teaches people
   how agentic systems work; the system is itself a content engine.
6. **Success signal = taste/curation** ("good note / bad note"), fine-tuning not gradient descent.
   No rigorous oracle required.
7. **Winning demo = legible narrative** of self-improvement via the learning loop — real,
   traceable, story-shaped ("over time it learned to stop being dumb and got clever"). **Not**
   statistical proof; no spreadsheet metrics.
8. **Smallest winning version = the full self-improving loop** (manager routing events by learned
   past decisions + guidance refined over time). The novel core *is* the MVP.
9. **Scope = the whole stack, genuinely working** — both because the deployments need it and
   because building it all is content.
10. **Build capacity = author + Claude agents, plan-driven** (superpowers-style: write a strong
    plan, then execute it slice by slice).

## The reframe (what changed vs. the docs)

The docs call the **non-code oracle / credit-assignment** the make-or-break risk, five times. Under
these objectives it is **demoted**: you do not optimise against an oracle, you **curate taste and
tell a story**. Consequences:
- The measurement apparatus (proxy/judge §11; GrowthBook canary, SHA-pinned A/B branches §6C;
  contextual-bandit CBR §13) **over-serves** and should be cut or deferred.
- The versioned-board / commit-the-why / pinning / telemetry spine **survives, for a different
  reason**: not canary measurement or safety theatre, but **"show your work" — legibility and
  content**. That is a *stronger* justification for this goal than the docs give.

## Where the risk actually moved (Phase-3 findings, severity-tiered)

**Critical**
- **C1 — Build strategy is the live threat, not the architecture.** "Plan the whole stack and glue
  it together at the end", hardest-slice-first, solo+AI, is the highest-probability path to a
  half-built sprawl — which *defeats objective #1* (a half-working version confers *negative*
  credibility). Contradicts §12 (build novel/risky parts last, on a proven base) and the
  superpowers thin-slice methodology. **Resolution: Option B + one thin end-to-end slice first
  (below).**
- **C2 — No defined floor for irreversible real-world acts.** All three deployments are
  publish-acts under the author's own brand, by a system branded on bad decisions. One
  defamatory/offensive/account-bannable post negates the project's purpose. The resource floor
  caps *cost*; nothing caps *publishing*. **Resolution: a human-approval gate on irreversible
  acts, built in from line one** (an `escalate_to_human`-style hold on publish, not prose in a
  prompt).

**Important**
- **I1 — Change ≠ improvement.** With a taste-only signal and a system editing its own prompts,
  drift is narratable as improvement via the builder's confirmation bias — and the technical
  audience will smell a cherry-picked anecdote. The narrative bar is "real, causally traceable,
  non-cherry-picked", which is higher than "no spreadsheet". The diagnosis step is where this
  lives (see learning-loop §9 of the execution addendum).
- **I2 — Adopted enterprise machinery is build-weight without novelty.** Bandit-CBR → plain
  prompt-stuffed CBR (retrieve N similar past cases into the prompt). OPA/Conftest, promptfoo,
  GrowthBook → defer. Temporal → decide by need; Postgres + cron + a `thread.finished` driver may
  suffice for the first slice.
- **I3 — Foundational dependencies still open (§15).** Cannot parallel-build sections whose
  interfaces don't exist yet (memory store, bus, what-survives-agentkit). Interfaces get *proven
  by running code*, not guessed up front.

**Minor:** two-manager reconciliation (§6 vs §6B/§6D) thin but harmless; `board_subscriptions`
needs a correlation-key column (execution addendum §3); project naming (Bad Decisions / Agent
Orange / BadCode / AgentWolf) should settle before it leaks into schema/namespaces.

## The one thing to validate before any code

**Not the oracle.** Validate: *can a **minimal** end-to-end loop — one deployment (BadCode
marketing), human-seeded guidance, human-feedback learning, **no autonomous Consultant, no adopted
enterprise tooling** — produce ONE real, causally-traceable, non-cherry-picked story of handling
getting better?* If a thin slice can't yield the narrative, the full stack won't either. Prove the
*demo is reachable* before committing to the architecture meant to reach it.

## Recommended build path — Option B, thin slice first

**Option B = novel primitives + human-curated learning.** Build the genuinely-novel core (the
agentic-OS primitive set, `spawn`/`pipeline`/`broadcast`, recursive scopes, the versioned board);
the "learning" is the **human-feedback→`write_fragment` loop** (execution addendum §9). The
autonomous Consultant is the *same loop with the trigger/input swapped* — shipped later, treated as
a research bet, not a refactor.

**Slice 0 of Option B — one end-to-end loop that proves the narrative is reachable:**
1. Human seeds fragments (role/routing/specialist guidance) + gives one vague goal (BadCode
   marketing).
2. Manager turns goal → spec → a few tickets → spawns worker(s) fire-and-forget (no questions, v1).
3. Worker produces a publishable artifact and **stops at the human-approval floor** before any
   irreversible publish (C2).
4. Human gives a **targeted note** ((target_ref, note) → `human-feedback` event); the subscriber
   `write_fragment`s a delta edit (I1/coherence guards: delta-only, length cap).
5. Next cycle visibly reflects the note. **Capture the before/after as the narrative artifact.**

Everything in this slice is shared machinery with the full design; nothing is throwaway. The
Consultant, adopted substrate, and the other deployments fan out *after* this loop runs and the
seams are proven.
