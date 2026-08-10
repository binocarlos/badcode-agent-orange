<img src="docs/assets/agent-orange.svg" alt="Agent Orange" width="100%">

**Give a project a goal. It works out what team it needs, asks you to confirm, builds that team, and
remembers everything it learns.**

Agent Orange runs small teams of long-lived AI agents. Each one is a container, a prompt and a set
of tools. They are woken by a clock or by something happening, they do a job, and they coordinate
through **one shared, labelled memory** — not by messaging each other. Over time the team edits
itself: new roles get created, prompts get rewritten, and every change is recorded with a reason.

---

## Why this exists

Ask a single good model to do one task and it will beat a committee of agents at it — reliably,
and for a tenth of the cost. That result is now well replicated, and if you are trying to decompose
*one task*, you should not be here: use subagents inside a single session and stop.

The thing a single session cannot do is **persist**. It cannot wake up on Tuesday, notice that the
newsletter did not go out, remember what happened the last three times, decide the project needs a
proofreader it does not yet have, and create one.

That is the gap Agent Orange fills. It is not a framework for splitting a task across agents. It is
a runtime for an **organisation that outlives any single conversation** — and, as far as we can
tell from the literature, almost nobody is building for that case. Every published topology system
compiles a fresh graph per task and throws it away.

So the design is deliberately, aggressively small:

|  | |
|---|---|
| **Nodes** | workers — a prompt, an image, some tools |
| **Clock** | schedules — cron, the normal way a worker wakes |
| **Shared state** | labelled append-only memory, with hybrid keyword + vector search. Its schema is three things: a seeded label registry, the architect's instructions to each role about what it reads and writes, and the archivist's prompt |
| **Wiring** | event subscriptions — used sparingly, often exactly once |
| **Change** | workers create and rewrite workers; every mutation is logged with a rationale |

There is no message bus between agents, no handoff protocol, no orchestrator DSL. A worker that
wants to tell another worker something writes it down where that worker will read it.

---

## How it works

The recommended starting shape is two roles and one piece of wiring. Everything else in your
project gets built by the first role, after it has talked to you.

```
        you ──────────────────────────────────────────────┐
         │  "run marketing for an art collective"          │  you talk to any
         ▼                                                 │  worker, any time
   ┌───────────┐   proposes a roster and a label schema,   │
   │ architect │   waits for you to say yes, THEN writes   │
   └─────┬─────┘   the configuration                       │
         │ worker_create · schedule_create · skill_create  │
         ▼                                                 │
   ┌──────────┐   ┌──────────┐   ┌──────────┐              │
   │  poster  │   │ curator  │   │ analyst  │  ← woken by  │
   └────┬─────┘   └────┬─────┘   └────┬─────┘    the clock │
        │              │              │                    │
        └──────────────┼──────────────┘                    │
                       ▼                                   │
            ╔═════════════════════╗                        │
            ║    shared memory    ║  labelled, append-only │
            ║  kind=fact  name=…  ║  searchable, retractable
            ╚═════════════════════╝                        │
                       ▲                                   │
                       │ writes what each finished piece of work was worth
              ┌────────────────┐                           │
              │   archivist    │ ◀─────────────────────────┘
              └────────────────┘   the ONE subscription:
               frozen — it decides   worker.finished — a chat
               what finished work    that went quiet, or a
               BECOMES               job that completed
```

**The architect** is a worker you chat with. You give it a goal; it proposes which roles should
exist, what wakes each one, what tools they need and what labels they will read and write. When you
agree, it writes that configuration using the same tools a human would. It builds nothing new —
those tools already exist — so its entire contribution is a prompt that makes it *propose before it
acts*.

**The archivist** is woken every time work in the project finishes — a conversation that goes quiet
or a dispatched job that completes — and turns it into memories. **Its prompt decides what finished
work becomes.** If you decide every conversation should yield an extract of emotional temperature
under `kind=emotion`, you write that sentence into the archivist and the whole project starts
recording emotion. It is frozen, so no other worker can quietly change what the project is allowed
to remember.

That is why there is only one subscription. Without an archivist you would have to tell *every*
worker to manage memory. With one, memory accumulates as a side effect of working — and you own the
policy in a single editable place.

**The memory schema is not one prompt but three legs**, and it is worth knowing which is which
before you go looking for a place to define it:

1. **the label registry** — a seeded memory (`name=label-registry`) briefed to both workers: the
   shared vocabulary everything else refers to;
2. **the architect's prompt** — which labels each role it creates *reads*, and which it *writes*.
   "You are a news journalist; before you do anything, read from this memory and write to that one"
   is an instruction the architect gives when it designs a role;
3. **the archivist's prompt** — what a finished piece of work becomes.

Leg 2 is the one that is easy to miss, because nothing names it: it lives inside the roles the
architect writes, not in any single editable field.

```sh
# it ships as a seed, so this shape is one command
architect-archivist@v1   →  2 workers, 1 subscription, 1 seeded label registry
```

---

## The topologies it enables

The org chart is a cheap, swappable variable here — a topology renders to ordinary configuration
writes, so seeding one is inspectable and revertible. **15 seeds** ship in [`go/topology/`](go/topology/).
The four worth knowing:

```
  ARCHITECT + ARCHIVIST            SUPERVISOR (fan-out)
  the KISS default                 one dispatcher, many specialists

   architect ──▶ builds             ┌──▶ specialist A
        │                           │
   [ shared memory ]  ◀── dispatcher├──▶ specialist B
        ▲                           │
   archivist ──┘                    └──▶ specialist C
                                    good for triage; the shape
   two roles, one edge              most people reach for first


  ASSEMBLY LINE                     ACTOR + CRITIC
  fixed order, autonomous roles     the self-improvement loop

   draft ─▶ edit ─▶ fact-check       ┌──────────┐  writes the
      each stage wakes the next      │  actor   │  next prompt
      won a real bake-off against    └────┬─────┘       ▲
      both centralised and free-form      │ finishes    │
      coordination                        ▼             │
                                     ┌──────────┐───────┘
                                     │  critic  │ (actor may be frozen
                                     └──────────┘  to stop it editing
                                                   its own yardstick)
```

Also seeded: blackboard, temporal hierarchy, escalation, solo and solo+memory (as controls),
sham-critic (as a placebo), and three experiment rigs. The full catalogue with the evidence for and
against each is [`docs/product/10-topology-library.md`](docs/product/10-topology-library.md).

Two more ship with a **health warning**, because our own research measured them worse than the cheap
alternative: **debate** (isolated self-correction with the same revision budget matched or beat
ten-agent debate on five of six comparisons, at a third of the tokens) and **self-organizing**
(self-organising teams consistently fail to match their best individual member, and it worsens as
the team grows). They stay available because a negative result is worth keeping visible — not
because they are recommended.

**What the research says about choosing between them** — 38 patterns from the wider world, judged
against this codebase, is [`docs/product/25-cooperative-patterns.md`](docs/product/25-cooperative-patterns.md).
The short version:

- **Automated org-chart search is a trap.** Six published generators all lose to plain
  chain-of-thought with self-consistency at ~10× the cost.
- **Role labels buy nothing; output diversity buys everything.** Expert personas scored *below* a
  plain all-assistant setup, and two differently-configured agents beat sixteen identical ones. The
  published version of that result varies the model backbone, which is an explicit non-goal here —
  so the diversity available to you is prompt, briefing and image, and that is where to spend.
- **Small rosters.** Prompt-rewriting inside a multi-agent system measures +2.4 points at 2 agents
  and −2.1 at 10. A worker earns its own row only if it differs in tools, cadence, trust, or memory
  view — never merely in personality.
- **Failures are organisational, not model failures.** 42% specification, 37% inter-agent
  misalignment, 21% weak verification, across 1,600 annotated traces.

Read those figures as *direction*, not magnitude: §6 of that document audits its own sourcing and
names the numbers that rest on a single preprint or a paper-summary site.

---

## Quickstart

One command brings up the API, chat UI and container runtime (Docker required):

```sh
cp .env.example .env          # optionally set ANTHROPIC_API_KEY=sk-ant-...
docker compose up --build     # then open http://localhost:8080
```

With `ANTHROPIC_API_KEY` you get a real agent; without one, a deterministic mock model replies so
everything works offline. **The product layer needs Postgres** — compose always sets `DATABASE_URL`;
on the sqlite fallback the router never routes and schedules never fire, silently. Details, and the
two traps waiting in a *real* `.env`, are in [`README-stack.md`](README-stack.md).

This is the standalone demo, not how you embed the engine as a library — for that see
[`docs/14-host-adapters.md`](docs/14-host-adapters.md), or
[`docs/19-embedding.md`](docs/19-embedding.md) if another application will host it.

---

## What's in the box

| Component | Path | What it is |
|---|---|---|
| **Orchestration core** | [`go/`](go/) | The engine — Go module `github.com/binocarlos/badcode-agent-orange`. Session lifecycle, container control, image registry, persistence, the event pipeline, and the whole product layer. The library a host app embeds. Entry type: `Runner` in `go/agentkit.go`. |
| **In-image agent** | [`sandbox/`](sandbox/) | TypeScript. The HTTP/SSE control server that runs *inside* each session container, wrapping `@anthropic-ai/claude-agent-sdk`. Because the harness is the Claude Agent SDK, a worker gets subagents and parallel tool use *inside one job* for free — which is exactly where multi-agent decomposition belongs. |
| **UI library** | [`web/`](web/) | React components: chat (one reducer renders live and replayed sessions identically) plus the product-layer pages. No router; the shell is `examples/web/`. |

Two seams make it portable: `ExecutionEnvironment` (how a container runs — Docker, Docker-in-Docker)
and `ImageRegistry` (images in, snapshots out). Swap an implementation to go from a laptop to a
fleet; the core does not change.

**Sessions are durable.** A session is snapshot-able — commit a running container to an image and
resume it later — and one idle for 30 minutes is archived automatically: snapshotted, its container
and host port released, restored on the next message. Reclamation, not deletion.

---

## What we actually know

Nobody knows the best arrangement of agents for a given job — the literature reports *reversals*,
where the winner flips with the task and with model strength. So this repo treats the org chart as
an empirical question and ships the apparatus for asking it: seedable topologies, scenario
generators with known answers, and a rig that runs one task through N org charts under equal token
budgets.

The discipline matters more than the apparatus:

- **The scorer is a fact, not a judge.** Scenarios carry held-out ground truth, so a flat result
  means "no improvement", never "blind instrument". You cannot reward-hack an answer key.
- **Controls, or it isn't knowledge.** Every experiment runs a no-learning arm and a *placebo* arm —
  a critic whose rewrites only shuffle instruction order. In our own rig the placebo ties the real
  critic exactly on rewrite count.
- **The instrument is frozen.** A worker can be marked frozen; the tool boundary refuses writes to
  it and records the attempt as an event. A loop cannot edit its own yardstick.
- **Mock first, spend later.** The model proxy is scriptable, so "the rewrite reached the next job"
  is asserted offline, byte-for-byte, free.

**One live run so far, and it is a negative result.** On 2026-07-28 a methodology critic was tested
against an investigator on hypotheses with known answers. The investigator was right 11 times out of
11; the critic correctly declined to change anything each round. At that model strength the task was
too easy to discriminate between org charts — and the harness then crashed at hypothesis 9 of 30,
losing the artifacts. Both findings are written up unedited in
[`docs/product/runs/`](docs/product/runs/). We publish it because an instrument that says "nothing
to see here" when there is nothing to see is the thing you want to find out about early.

**We are not claiming any topology beats any other.** The tournament has not been run.

---

## Status

Private use for now — the source is bayesprice-owned (forked from an in-house Go runtime,
"agentkit"); a public release needs licensing resolved first.

The product layer is built and the acceptance loop closes: one worker rewrites another worker's
prompt with a rationale, and the next job runs under it —
`e2e/features/acceptance-loop.spec.ts` offline, and observed against the real Anthropic API on
2026-07-26, including the prompt-injection boundary holding when event text ordered the model to
ignore its prompt.

All three packages build and test. The gates are `go build ./... && go vet ./... && go test ./...`
in `go/` (Go floor **1.25**) and each JS package's `typecheck` + `test`. CI runs all three and
enforces the liftability invariant — the `go/` module imports nothing from any host app. Memory
tests need a live Postgres (`AGENTKIT_TEST_POSTGRES_URL`); a green run without one does not cover
the pgvector paths.

GCP is wired in: a GCS blob store, ADC registry auth, config-driven backend selection. Snapshot
push+pull was verified against the live project on 2026-06-25 — see
[`MIGRATION.md`](MIGRATION.md) §4a/§4b for the evidence *and its limits*.

---

## Documentation

**Start here, in order:** this file → [`README-stack.md`](README-stack.md) (run it) →
[`docs/workflows.md`](docs/workflows.md) (**is your workflow a fit? — including when it is not**) →
[`docs/01-architecture.md`](docs/01-architecture.md) (the runtime) →
[`docs/product/00-overview.md`](docs/product/00-overview.md) (the product layer's map) →
[`docs/18-workers-memory-events.md`](docs/18-workers-memory-events.md) (operating it).

If you are an **agent** working in this repo, read [`CLAUDE.md`](CLAUDE.md) first instead.

| Doc | Topic |
|---|---|
| [docs/18-workers-memory-events.md](docs/18-workers-memory-events.md) | **Operating the product layer** — workers, memory, events, schedules, the core tools |
| [docs/workflows.md](docs/workflows.md) | **Workflows** — how to express yours here, and the six where the answer is to use something else |
| [docs/19-embedding.md](docs/19-embedding.md) | Embedding Orange in another application — credentials, named sessions, embed tokens, and a hazard list |
| [docs/product/17-product-spec.md](docs/product/17-product-spec.md) | The authoritative spec: goal, atoms, binding principles P1–P8, non-goals |
| [docs/product/10-topology-library.md](docs/product/10-topology-library.md) | The 15 seeded org charts and the evidence behind each |
| [docs/product/25-cooperative-patterns.md](docs/product/25-cooperative-patterns.md) | 38 cooperative patterns from the wider world, judged against this code |
| [docs/product/26-work-plan-cooperative-tests.md](docs/product/26-work-plan-cooperative-tests.md) | The unbuilt test plan and engine gap list that follows from it |
| [docs/product/06-work-plan.md](docs/product/06-work-plan.md) | The build checklist, and a **Discovered Issues Log** — the most reliable record of design-vs-code drift |
| [docs/01-architecture.md](docs/01-architecture.md) | The layered architecture; how Go owns orchestration |
| [docs/02-execution-environment.md](docs/02-execution-environment.md) | The container seam; Docker/DinD; capability axis and trust gate |
| [docs/03-image-registry.md](docs/03-image-registry.md) | Image build/save/load/push/pull; snapshot-as-image |
| [docs/05-event-streaming.md](docs/05-event-streaming.md) | Event vocabulary, compaction, persistence, the single reducer |
| [docs/06-artifacts.md](docs/06-artifacts.md) | `ArtifactStore`; snapshot vs artifacts |
| [docs/07-in-image-agent.md](docs/07-in-image-agent.md) | The control-server contract; the harness seam |
| [docs/13-fleet-placement.md](docs/13-fleet-placement.md) | Fleet and placement — scaling across a worker pool |
| [docs/14-host-adapters.md](docs/14-host-adapters.md) | Every seam a host app implements |
| [docs/15-standalone-stack.md](docs/15-standalone-stack.md) | The standalone stack; library vs standalone |
| [docs/product/20-operations-doctrine.md](docs/product/20-operations-doctrine.md) | The operator's manual and the common-sense block in every worker's prompt. Nothing is law until a measured A/B says so |
| [installations/README.md](installations/README.md) | Installation images: the derived-image tree, the overlay model, `imagetree` |
| [MIGRATION.md](MIGRATION.md) | Standalone-ification, registry-agnostic build, and the GCP roadmap |

The numbered engine docs have deliberate gaps (no 04, 08–12, 16; `17` is the product spec, under
`docs/product/`).
