# Workflows: what to build here, and what to build somewhere else

You have a job you want a team of agents to do. This page says how to express it
in Agent Orange — or, for a handful of jobs, that you should not.

It is the readable half of [`product/25-cooperative-patterns.md`](product/25-cooperative-patterns.md),
which judged 38 published cooperative workflow patterns against this codebase and
rejected 22 more. That document is the evidence and the arguing; this one is the
recommendation.

**Read the numbers as direction, not magnitude.** The research behind this page
audits its own sourcing in its §6, and several figures rest on a single preprint
or on a summary of a paper rather than the paper. More importantly: this project
has run **one** live experiment against a real model, and it ended in a ceiling
result — the task was too easy to tell two arrangements apart. Everything else is
mock-proven, which demonstrates that a message arrived where it was addressed and
never that an arrangement is better. Treat every recommendation below as a
well-supported starting point, not as a measured result.

---

## First: should this be an organisation at all?

Ask one good model to do one task and it will beat a committee of agents at it,
reliably and for a fraction of the cost. That finding is well replicated, and it
is the single most useful thing on this page.

So before anything else: **if you are trying to decompose one task, stop.** Use
subagents inside a single session — the harness gives a worker parallel tool use
and subagents for free, and they share one context, which is exactly what makes
them better than a fleet.

What a single session cannot do is **persist**. It cannot wake on Tuesday, notice
the newsletter never went out, remember what happened the last three times, and
decide the project needs a proofreader it does not yet have. That is the gap this
runtime fills, and the test for whether you belong here is not "is my task big?"
but "does my work outlive one conversation?".

---

## The shape almost everyone should start with

Two roles and one piece of wiring:

- an **architect** you talk to, which proposes a roster and then writes the
  configuration with the same tools a human would;
- an **archivist**, woken whenever work finishes, whose prompt decides what
  finished work *becomes* in memory.

Everything else — the specialists, their schedules, what they read and write — is
built by the architect after it has talked to you. Reach for a different shape
when you know why, and the sections below are where you find out whether you do.

**A worker earns its own row only if it differs in tools, in cadence, in trust, or
in memory view.** A worker that differs only in personality is the same worker
with a bigger brief. This is not a style preference: prompt-improvement loops
measure *better* on small rosters and *worse* on large ones, inverting somewhere
around eight workers, and role labels on their own measured slightly worse than
no labels at all.

---

## 1. Proving the org actually got better

**What you want:** evidence that a change — a rewritten prompt, a new worker —
improved something, rather than a feeling that it did.

**Build it as:** a scorer worker marked `frozen`, so nothing in the project can
edit the instrument; a control arm running the same inputs without the change; and
outcomes written to memory under labels the scorer never writes to. Compare arms
on a metric with a known answer, not on the org's own opinion of itself.

**Verdict: expressible, and the discipline matters more than the machinery.** Two
traps are worth stating. Self-scoring is worthless — in the published run that
matters, every model rated its own work above 0.7 while nearly half its outputs
were worse than random. And "the next job went better" cannot validate an evolved
evaluator: an always-pass detector keeps downstream scores looking fine. If you
change the yardstick, you need a set of cases that neither loop can reach.

**Honest limit:** memory is project-global, so "the actor cannot read the rubric"
is a sentence in a prompt, not a boundary. If the measurement has to be airtight,
keep the rubric and the held-out cases outside the project.

## 2. Deciding who gets woken

**What you want:** the right worker doing each piece of work.

**Build it as:** distinct event types classified at the ingress, by ordinary code,
before any model is involved. Route the residue — and only the residue — to a
triage worker. Prefer a schedule to a subscription: a worker woken by a clock is
easy to reason about, a chain of workers waking each other is not.

**Verdict: expressible.** Most routing is genuinely deterministic, and the cheapest
improvement available is to stop spending a container on decisions an `if` can
make. A worker can also name its successor at runtime by writing a subscription
before it finishes, which is the sanctioned way to do a model-chosen handoff.

**Spend on diversity, not headcount.** Two differently-briefed workers beat sixteen
identical ones in the published comparison. Running one worker at higher
concurrency buys you copies of one prompt; two workers with different briefings
and different reading lists buy genuinely different attempts.

## 3. What crosses between workers

**What you want:** worker B to act on what worker A produced.

**Build it as:** A writes what it learned to memory under agreed labels; B's
briefing selects those labels. That is the intended channel, and it is the one the
whole design is organised around.

**Verdict: expressible, with one sharp edge.** When B is woken by A finishing, the
event carries A's *entire transcript* as B's first message — uncapped and verbatim.
That is fine for an archivist, whose job is to read everything, and wrong for
almost everything else: it is the largest context cost and the largest injection
surface in the system. Until a projection knob exists, the discipline is: have B
rely on memory and treat the transcript as background, and keep chains short.

## 4. Memory as the coordination substrate

**What you want:** the org to accumulate what it learns and act on it later.

**Build it as:** labels chosen deliberately, written down in a seeded
`label-registry` memory that every worker is briefed on. Use `name=` for anything
with a current value, `since`/`until` for windows, and `latest_per` when you want
the current value of *every* member of a class at once.

**Verdict: expressible, and this is the part to spend design effort on.** The
memory schema is three things, and the middle one is easy to miss: the label
registry, **the architect's instructions to each role about what it reads and
writes**, and the archivist's prompt. Leg two lives inside the roles the architect
writes rather than in any editable field.

**Withdraw things that turn out wrong.** A memory labelled `retracts=<id>` removes
the original from briefings and searches without deleting history. Nothing else
does this — a superseded fact with no retraction keeps competing for attention
forever.

## 5. Not burning money

**What you want:** to not discover a two-worker loop after it has run for a week.

**Build it as:** short chains, schedules rather than subscription cascades, and a
worker on a cron that reads recent activity and pages a human when it looks wrong.

**Verdict: partial, and you should know exactly which part is missing.** A chain of
workers waking each other is capped at eight hops, so a subscription loop cannot
run forever. But a **schedule** resets that budget, no worker can see token spend
or cost, and nothing attributes cost to one originating event. The published
horror stories in this class are all unbounded recursion with no completion
rule — $47,000 over eleven days in one case — and the honest position is that this
runtime would not notice that either. Keep an eye on the bill.

## 6. Involving a human

**What you want:** sign-off before something goes out.

**Build it as:** `request_human_attention` in the worker's prompt, with a message
saying what is needed. The human gets a link, replies in the ordinary chat thread,
and the worker carries on. Granting autonomy later is deleting a sentence from a
prompt — no approval machinery ever existed to dismantle.

**Verdict: expressible, with two operational warts.** Always pass `expires_in`: a
request made without it is never marked answered, even after the human replies, and
the asks list accumulates forever. And a job parked waiting for a human stays
displayed as parked in the job history even after the conversation resumes; it
holds no capacity and blocks nothing, but it looks stalled when it is not.

**Close the loop.** After a human corrects a worker, that correction should become
a memory the worker's own briefing selects — otherwise the same question gets asked
every week. That single habit is the difference between an escalation and a
learning escalation.

---

## Use something else for these

Not every workflow belongs here, and pretending otherwise wastes your time.

**Decomposing a single task across agents.** Use subagents inside one session.
Committees lose to one good model on one task, and this runtime's whole value is
persistence you do not need for a task that finishes in one sitting.

**Collecting results from more than a hundred parallel branches.** There is no
join, no barrier and no quorum. The workaround is a collector worker polling
memory on a clock, and it is genuinely racy — it cannot distinguish "nobody
answered" from "nobody has run yet". Use a workflow engine with real fan-in.

**Anything needing authorization boundaries between workers.** There are none, by
design. Every worker holds every core tool, including the one that rewrites
another worker's prompt, and can read any other worker's configuration. The
project is the only boundary. If you need one worker that genuinely cannot affect
another, use two projects — or a different system.

**Multi-agent debate to improve accuracy.** Measured worse than the cheap
alternative: the same model, given the same revision budget and no peer text at
all, matched or beat ten-agent three-round debate on five of six comparisons at
roughly a third of the tokens. If you want reliability, run independent attempts
and compare them; do not let them talk.

**Searching automatically for the best org chart.** Six published generators all
lose to plain chain-of-thought with self-consistency at roughly ten times the
cost, and the systems they generate structurally collapse. Hand-designed
decomposition beat all of them. Design the roster yourself; there is nothing to
search for.

**Work that must not lose a step on failure.** A failed job is terminal and
carries only an error string — not its transcript, not the input that triggered
it. There is no retry. For work where a dropped step is unacceptable, put a
durable workflow engine in front and let it call this runtime, rather than
expecting the org to notice.

---

## Where to go next

- [`18-workers-memory-events.md`](18-workers-memory-events.md) — operating the
  product layer: workers, memory, triggers, the core tools, the config log.
- [`product/25-cooperative-patterns.md`](product/25-cooperative-patterns.md) — the
  research this page summarises, including the 22 rejections and the audit of its
  own sourcing.
- [`product/17-product-spec.md`](product/17-product-spec.md) — the binding
  principles and the explicit non-goals, which explain *why* several of the
  limitations above are deliberate rather than unfinished.
