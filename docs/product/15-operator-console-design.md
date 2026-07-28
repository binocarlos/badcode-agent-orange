# 15 — The operator's console: a front-end design proposal

*Written 2026-07-28. Status: **proposal. Nothing here is built.***
*File numbers in this folder are not § numbers (09-config-log.md holds §15). This document owns no
spec section; it proposes what the browser shows and asks for the few backend seams it needs.*

Read after [`17-product-spec.md`](./17-product-spec.md) (atoms, P1–P8),
[`../18-workers-memory-events.md`](../18-workers-memory-events.md) (the operator's seat) and
[`12-composition-playbook.md`](./12-composition-playbook.md) (why the arrangement is the product).

---

## 0. What this is

A design direction for the Agent Orange front end, covering five questions: the first-run journey,
the operator's daily loop, org-chart visualisation and editing, how learning is surfaced, and what
prior art to take from. It ends with the backend seams the design needs and a proposed build order.

Nothing here is a work plan yet. The house rule is design doc → approval → work-plan items, and
this is the first half.

---

## 1. The finding that reframes the brief

Two things turned up while reading the code, and both change what the highest-value work is.

**1.1 — Most of the product layer's UI is built and unreachable.** `web/` exports `EventsPage`
(events · jobs · replay · changelog), `AutomationPage` (subscriptions · schedules),
`ChangelogView`, `EventReplayPanel` and `EventJobHistory`. The shell that the stack actually
serves — `examples/web/src/App.tsx` — mounts three views: **Chat, Workers, Settings**. Nothing
else. `GET /agent/config-events` *is* mounted (`go/httpapi/config_events.go`; the header comment
in `web/src/configLog.ts` saying it is not is stale), so the changelog would work today if
anything rendered it.

So in the running stack there is currently no way to see an event, a job, a delivery, a rationale
or a prompt diff, and no way to edit a subscription or a schedule outside `curl`. The single
cheapest improvement available is four lines in `ViewNav` — before any new component exists.

**1.2 — The UI is a configuration editor, and the product is a system that changes itself.**
Every screen we have is a list plus a form over a row: workers, subscriptions, schedules,
settings. That is the right shape for *authoring*. But the thesis of the product is that a worker
rewrites another worker's prompt with a rationale and the next job runs improved — and the
operator's question is never "what is in this row". It is:

> *What shape is my organisation, what did it do overnight, what did it change about itself, and
> why?*

None of those four are answerable on any screen today. The design below is mostly about
**reading**, not editing.

---

## 2. The thesis: space and time

The data model gives us exactly two structures, and they should give us exactly two views.

| | The structure | The view | The question it answers |
| --- | --- | --- | --- |
| **Space** | workers (nodes), subscriptions (edges), schedules (clocks) | **the org chart** | what shape is this, and what fires when X arrives? |
| **Time** | `config_events`, `events`, `event_deliveries`, `attention_requests` — all append-only | **the spine** | what happened, who decided it, and why? |

Everything else is a lens onto one of those two. The Desk is the spine filtered to *today*; worker
lineage is the spine filtered to *one worker's prompt*; the changelog is the spine unfiltered. One
visual device, three placements — which is also how P8 gets taught: you never see "current state",
you see a fold over history, and the rail is always in the corner of your eye reminding you of it.

---

## 3. Visual direction

### 3.1 Where we're starting from

`examples/web/src/App.tsx` line 17: `const theme = createTheme();` — the MUI default, unmodified.
Roboto, `#1976d2` blue, 4px radii. It is the most templated look available in React, and it says
nothing about a product whose subject is an organisation that keeps its own books.

`web/` is a component library with MUI as a *peer* dependency, and every component already styles
through theme tokens (`divider`, `text.secondary`, `success.light`). So the identity can live
almost entirely in a theme object in the shell, with components staying theme-driven. That is the
constraint to design inside, and it is a comfortable one.

### 3.2 The organising idea: authorship is a colour

The one fact this product exists to make legible is **who decided this — a person or a worker**.
The config log already distinguishes them for free (a human edit logs with an empty
`actor_worker`; a worker's rewrite names it — S7's discovery). So make that the palette's job:

- **Agent-authored change is marked. Human-authored change is unmarked.**

That single rule does more work than any decoration: scanning a changelog, the warm marks *are*
the self-improvement loop, and their absence is you. It also means colour is never spent on
prettiness, which keeps the surface calm enough for the one thing that must shout — a worker
asking you a question.

### 3.3 Palette — six named values

| Token | Light | Dark | Means |
| --- | --- | --- | --- |
| `paper` | `#FBFAF9` | `#0E1114` | ground |
| `ink` | `#12161A` | `#E8EAEC` | text, hairlines |
| `ember` | `#B3541E` | `#E0873F` | **an agent did this** — rewrites, agent-authored config, agent marks on the spine |
| `steel` | `#2F6272` | `#6FA6B8` | **instrument** — frozen workers, measurement, the bench |
| `rose` | `#A6376A` | `#DF7BA4` | **it wants you** — `awaiting_human`, open attention requests |
| `fault` | `#8F2B2B` | `#D96C6C` | failed deliveries, disabled-by-failure schedules |

Notes on the choices. `ember` is a burnt ochre, not a safety orange — the product is called Agent
Orange and refusing the hue entirely would be coy, but near-black-plus-one-acid-accent is the
default look of every agent tool shipped this year, so the warm hue is demoted to a *marking* and
the cold `steel` carries equal weight. `rose` exists because "a worker is waiting for you" is not
an error and must never be red: `awaiting_human` is a pause, and the current
`deliveryStatusSeverity` maps it to MUI `warning`, which puts it in the same visual bucket as a
rate limit. It is not the same thing.

Greys derive from `ink` at 8/14/40/64% — no separate grey ramp.

### 3.4 Typography — identifiers are mono, content is prose

§7.1 already says the sentence this design borrows: *"Labels are identifiers; content is
content."* Type it that way, everywhere, without exception:

- **Identifiers** — worker names, event types, cron expressions, label selectors, delivery
  statuses, `name:version` image refs, payload keys, diffs → **IBM Plex Mono**, 12–13px, slight
  negative tracking.
- **Content** — descriptions, rationales, prompts, briefings, event text, empty states, everything
  a human or a model *wrote* → **Instrument Sans**, 14–15px, 1.55 leading.
- **Display** — screen titles, worker name plates → Instrument Sans, 24–32px, tight tracking.

Both are OFL/free and self-hostable in `examples/web` (two woff2 files, ~90KB total), with
`system-ui` / `ui-monospace` fallbacks so the library never depends on them.

The rule is load-bearing rather than decorative: when a rationale is prose and the prompt it
rewrote is mono, a changelog entry reads as *a commit message above a diff* without any chrome
saying so. And the moment someone types free text into a label field, the type tells them it does
not belong there before the API does.

### 3.5 Layout

```
┌────────────┬──────────────────────────────────────────────────────────┐
│ project ▾  │  DESK ·  CHART ·  WORKERS ·  EVENTS ·  CHANGELOG ·  SET  │
│            ├──────────────────────────────────────────────────────────┤
│ ▸ Desk   3 │ │                                                        │
│ ▸ Chart    │ │  ← the spine: one hairline rail, ticks are records     │
│ ▸ Workers 6│ │                                                        │
│ ▸ Events   │ ●  09:14  ember mark = a worker did it                   │
│ ▸ Changelog│ │                                                        │
│ ▸ Settings │ ○  09:02  hollow = a human did it                        │
│            │ │                                                        │
│ sessions…  │ ◆  08:40  rose = something is waiting for you            │
└────────────┴──────────────────────────────────────────────────────────┘
```

Left rail stays as today (280px, project switcher, session list) and gains counts — the badge on
Desk is the number of things asking for you, and it is the only number in the chrome.

### 3.6 The signature: the spine

One vertical hairline with ticks, at the left edge of every reading surface. Tick glyphs are a
closed set — filled (agent), hollow (human), diamond (attention), cross (failure) — and the rail
is continuous across a session's worth of scrolling so that distance down the page is genuinely
elapsed time (with a gap marker when >4h passes, so a quiet night reads as a quiet night rather
than compressing away).

It is one device because it is one table shape, and it is the thing the product should be
recognised by: *Agent Orange is the tool where everything hangs off a rail, because everything is
an append.*

### 3.7 The deliberate risk: the chart is a schematic, not a flowchart

Every agent-orchestration canvas on the market draws rounded cards with drop shadows on a dotted
grid (n8n, LangGraph Studio, every Flow clone). We draw a **patchbay**: orthogonal hairline
routes, right-angled joints, event types set in mono *riding the wire* rather than in a pill,
name plates with a 1px rule instead of a card, frozen workers double-ruled in `steel`, schedules
as small 24-hour dials docked to the left of a node with the firing hours ticked.

It is a riskier look — it will read as cold to some people — and it is the right risk, because
this product's own documents call the frozen scorer an *instrument* and the whole self-improvement
workstream is a measurement rig. If the chart looks like a bench panel, the frozen worker looks
like what it is.

Restraint clause: the schematic is where all the boldness goes. Everything around it — forms,
lists, editors — stays quiet MUI with the new type and hairlines and no accents at all.

---

## 4. Storyboard A — empty project → working fleet → what happened overnight

Six screens. The current flow covers screens 2–4 in list form; the design keeps their logic and
changes what you look at.

### A1 · The empty project

Today: a small outlined panel, *"This project has no workers yet"*, two buttons.

Proposed: **the empty chart is the hero.** The canvas is drawn, empty, with one ghost node and one
ghost wire — so the first thing you learn is what this system is made of, before you have made
anything.

```
        ┌ ─ ─ ─ ─ ─ ─ ─┐          Workers are woken by events and clocks.
   ? ─ ▸│   a worker   │─ ▸ ?     Nothing here yet.
        └ ─ ─ ─ ─ ─ ─ ─┘
                                  ┌──────────────────────────────────┐
                                  │ Start from an org chart          │
                                  │ Hire one worker                  │
                                  └──────────────────────────────────┘
```

Copy: "Start from an org chart" rather than "Start from a topology" on the first screen — the word
*topology* is correct and stays everywhere else, but it is not the word someone arriving at an
empty project has yet.

### A2 · Pick a shape

Today: a list of names and descriptions. Proposed: **each of the 13 seeds renders as its own
miniature schematic**, drawn by the same layout function as the real chart, so you choose a shape
by looking at shapes.

```
┌──────────────────┐ ┌──────────────────┐ ┌──────────────────┐
│  ┌─┐             │ │  ┌─┐    ┌─┐      │ │      ┌─┐         │
│  └─┘  ⏱          │ │  └┬┘ ─▸ └─┘      │ │   ┌──┴──┐        │
│                  │ │   ▲──────┘       │ │  ┌─┐ ┌─┐ ┌─┐     │
│  solo@v1         │ │  actor-critic@v1 │ │  supervisor@v1   │
│  one worker, one │ │  a critic rewrites│ │ one dispatcher, │
│  clock — the     │ │  the actor's      │ │ N specialists   │
│  control arm     │ │  prompt           │ │                 │
│  ⓘ control       │ │                   │ │                 │
└──────────────────┘ └──────────────────┘ └──────────────────┘
```

Families are labelled, because the library's own point is that three of them are **controls** and
two are **instruments** — a person picking `sham-critic@v1` without being told it is a placebo has
been failed by the screen. Small caps eyebrow per card: `CONTROL` / `WORK` / `INSTRUMENT`.

### A3 · Answer 3–5 questions

Keep the existing form. Add one thing: **the miniature redraws live as you type names**, so the
answers visibly land on the chart. The preview is not a separate step you take on faith; it starts
the moment you start typing.

### A4 · Preview the diff

The existing preview data (`new_workers`, `colliding_workers`, `new_subscriptions`,
`new_schedules`, `settings_fields`, `memory_seeds`, `missing_images/skills`, `applicable`) is
exactly right and should not change. What changes is that it is shown **twice**: as the chart,
with new nodes/wires drawn in ink and collisions struck through in `fault`; and as the existing
exact list beneath it. Two representations of one fact — the picture for comprehension, the list
for verification. Apply stays disabled on `applicable: false`, and the reasons stay where the
disabled button is.

### A5 · Applied — and the honest next line

The current success screen names what was created. It should also say the thing that is true and
currently unsaid:

> **Six rows applied, recorded as one entry in the changelog.**
> Nothing will run until something wakes them. `daily-brief` next fires at 09:00 tomorrow.
> ▸ *Wake it now* — send a test event

That last affordance is a real gap, not a nicety: after applying a topology whose only clock is
cron, an operator has no way to make anything happen (the e2e suite hit this exact wall — T4–T7's
discovery that a schedule-only seed "cannot be driven on demand" and needed a poke subscription).
`POST /agent/events` already exists; the replay panel already composes a legal draft event. One
button.

### A6 · The morning after

Land on **the Desk** (§5). The first-run journey ends where the daily loop begins, which is the
point of a first run.

---

## 5. Storyboard B — the daily loop

### 5.1 What the operator actually needs

Three questions, in this order, every morning: *does anything want me? what changed? what broke?*
So the Desk is three stacks in that order, and it is **read-only** — every item's action is to
open the thing it names.

This does not re-grow the approval queue §9 deletes. §9's rule is that the chat thread is the
review surface and that no approval *state machine* exists. The Desk stores nothing, decides
nothing, and has no states of its own: it is a query over `event_deliveries`, `attention_requests`,
`config_events` and `project_events`. Clicking an ask opens the session thread, exactly as
clicking the permalink in a webhook does today.

### 5.2 The Desk

```
Desk                                              Thursday 28 July, 09:14
──────────────────────────────────────────────────────────────────────────

ASKS  2                                          nobody has answered these
◆  email-answerer  ·  awaiting_human  ·  2h 40m
   "Reply drafted for the Ridley invoice query, but the amount doesn't match
    our records. Send as-is, or hold?"                        ▸ open thread
◆  marketing-manager  ·  awaiting_human  ·  19h        expires in 5h
   "Which of these three headlines?"                          ▸ open thread

CHANGES  4                                              since you last looked
●  email-reviewer  rewrote  email-answerer            03:12   +4 −1 lines
   "answers kept omitting the ticket reference, so the rule is now first"
●  email-reviewer  rewrote  email-answerer            05:40   +1 −1 lines
   "narrowing yesterday's rule: reference only when one exists"
○  you  retuned  schedule daily-brief                 08:02   (no reason given)
●  archivist  published  toolbox:4                    04:15

TROUBLE  3
✕  3 deliveries failed  ·  worker  invoice-parser     since 02:00
   No reason is recorded on a delivery. ▸ open agentd log · ▸ open last job
✕  schedule  nightly-sweep  disabled after 5 failed starts
   last reason: image "toolbox:9" names no image in the catalogue
🔒 fee-scorer refused 2 rewrites from  tuner                   ▸ why this matters
```

Notes on each stack:

**Asks** is `awaiting_human` deliveries joined with open `attention_requests` — which is where the
*message text* lives, and the only reason this stack needs a backend seam (§10, B2). Without it
the stack still renders, just without the sentence the worker actually wrote, which is most of the
value. The age is load-bearing: `awaiting_human` never stamps `ended_at`, so the clock genuinely
keeps running, and the design shows it running rather than hiding a null.

The known wart — a delivery parked at `awaiting_human` never leaves that status even after the
human replies — is handled here honestly and cheaply: an ask whose session has a human message
newer than the request renders as **answered** and drops out of the stack, computed at read time
from data we have (the store already has `CountUserMessagesSince` for exactly this). We do not fix
the row; we stop showing a stale row as if it were live. If that turns out to matter more, it is a
resume hook on the message path, which is a backend decision, not a UI one.

**Changes** is the changelog windowed to "since you last looked" (a `localStorage` high-water mark
on `created_at`, cleared per project — no server state). Agent-authored entries carry the ember
mark; yours are hollow. `(no reason given)` on your own schedule edit is not a scold — it is the
screen telling the truth about §8's patchy rationales, and it is the argument for B3.

**Trouble** collects the four failure shapes the docs warn about, each with the sentence the docs
already wrote: a failed delivery has no reason column, so the item says so and points at the log
rather than pretending; a schedule disabled by the five-strike rule shows
`last_provision_error`, because that field exists precisely so a human can recover; a rate-limited
subscription names the cap it hit.

**Freeze refusals get their own line** because C8 says refusals are signals — *"an agent trying to
edit the thing that scores it is the reward-hacking hypothesis in its most literal form"*. They
are `worker.freeze_refused` events, already emitted, already listable. The counter belongs on the
worker too (§7.1). `▸ why this matters` opens a two-sentence explainer, once.

### 5.3 The rest of the loop

Approve → open the thread and type. Unfreeze → the worker's own page, with the sentence *"Frozen —
cannot be changed by other workers"* becoming *"Unfreezing lets any worker in this project rewrite
this prompt, including the ones it scores"* at the moment of the click. D4 says a plain JWT is
enough ceremony; the sentence is not ceremony, it is the fact.

---

## 6. The org chart (Q3)

### 6.1 What it draws

Nodes are workers. Edges are subscriptions. Clocks are schedules. Entry pips at the top are
external event types seen in the last N events. Nothing invented.

```
   email.received                      ⏱ 0 9 · · · 1 7 · ·
        │                                   │
        ▼                                   ▼
  ┌─────────────────┐              ┌─────────────────┐
  │ email-answerer  │              │ social-writer   │
  │ answers inbound │              │ posts twice a d.│
  │ ● running   1/1 │              │   idle      0/2 │
  └────────┬────────┘              └─────────────────┘
           │ worker.finished  {worker: email-answerer}
           ▼
  ┌─────────────────┐   writes ▸   ╔═════════════════╗
  │ email-reviewer  │ ─ ─ ─ ─ ─ ─ ▸║ fee-scorer   🔒 ║
  │ 47 rewrites     │   refused 2  ╚═════════════════╝
  └─────────────────┘
```

Node plate: name (mono, large), description (prose, one line), and a state line —
`● running n/max` · `idle` · `disabled` · `frozen`. Frozen is a double rule in `steel` plus the
lock; it should look like a sealed instrument, not a disabled row.

### 6.2 Layout

A **pure function**, `layoutOrgChart(workers, subscriptions, schedules) → {nodes, edges, ranks}`,
in the style of every other pure module in `web/src` (`events.ts`, `configLog.ts`, `topologies.ts`)
and unit-tested the same way. Layered/Sugiyama-lite: rank by longest path from an entry pip, order
within rank to minimise crossings, orthogonal routing, deterministic tie-breaks by name so the
chart never reshuffles between renders.

**No graph library.** The largest thing we draw is a 13-seed topology; `reactflow`+`dagre` is
~120KB and a peer-dependency negotiation across two packages (`web/` npm, `examples/web/` yarn)
for a layout we can write in ~150 lines and actually test. Hand-rolled SVG also buys the schematic
look, which a library's node/edge primitives would fight.

### 6.3 The chart has no state of its own

**Node positions are derived and never persisted.** If we stored coordinates we would have
invented a piece of project state that is not in the config log, and P8 would stop being true of
the screen you are looking at. Pan and zoom live in the URL fragment at most.

This is also the answer to "can the operator tidy the diagram" — no, and that is a feature: two
people looking at the same project see the same chart, and a chart that changed shape means the
organisation changed shape.

### 6.4 Direct manipulation — yes, with a reason attached

**Does drag-an-edge fit the append-only model?** Yes, cleanly, because there is nothing to
mutate: creating a subscription is an append (`subscription_create` + its config event), and
deleting one appends too, carrying the final state. The append-only model is not hostile to direct
manipulation; it is hostile to *silent* manipulation.

So the gesture is: **drag from A to B → a proposal, not a mutation.**

```
        ┌─────────────────┐
        │ email-answerer  │
        └────────┬────────┘
                 ┆  dragging…
                 ▼
        ┌─────────────────────────────────────────────┐
        │ When  email-answerer  finishes, wake         │
        │       email-reviewer                         │
        │                                              │
        │ event_type  worker.finished                  │
        │ filter      {"worker":"email-answerer"}      │
        │                                              │
        │ Why are you wiring this?                     │
        │ ┌──────────────────────────────────────────┐ │
        │ │ review pass on every answered email      │ │
        │ └──────────────────────────────────────────┘ │
        │                        Cancel   Wire it up   │
        └─────────────────────────────────────────────┘
```

Three rules hold it to the model:

1. **Every canvas write goes through the same route the form uses.** No canvas-only endpoint, no
   batching, no optimistic local graph. The chart is a second front end onto `useSubscriptions`.
2. **The reason field is mandatory on the canvas** even though the HTTP route currently drops it
   (B3). Making the gesture cheap and the reason free is how you get a changelog full of
   `(no reason given)`; the drag is already the cheap part.
3. **Cutting an edge is never called undo.** The wording is *"Stop waking email-reviewer when
   email-answerer finishes"* and the result is a new record. P8's "undo is a forward operation"
   should be audible in the button, not just true in the database.

What direct manipulation must **not** grow into: dragging nodes into a shape, a palette of node
types, or anything that implies a canvas is the source of truth. The chart edits two things —
subscriptions and enabled/frozen — and hands everything else to the worker page.

### 6.5 "What fires when this event arrives?" — the propagation view

This is the question the known-gaps list says nothing answers, and we can answer it today with a
pure function we already have. `matchSubscriptions(event, subscriptions)` is a faithful dry-run
copy of the router's rules. Chain it:

- Pick or paste an event type → hop 1 lights the wires and nodes that match.
- Each woken worker will emit `worker.finished` when it ends, so hop 2 is
  `matchSubscriptions({type:'worker.finished', envelope:{worker:X,…}})` — and so on.
- Draw it against a **depth ruler** down the side, 0…8, with hop 8 against a stop line, because
  the router refuses depth > 8 and that floor should be visible rather than folklore.

```
depth ┌──────────────────────────────────────────────
  0   │  email.received ─▸ email-answerer
  1   │      worker.finished ─▸ email-reviewer
  2   │          worker.finished ─▸ archivist
  3   │              (nothing subscribes)
      ┆
  8   ├━━━━━━━━━━━━━━━━━━━━━━━━━ the router refuses deeper
```

Honesty rail: the preview models **only** the two austere §8.3 predicates, exactly as
`matchSubscriptions`'s comment says it must. Rate limits, `max_instances` gating and budget stops
depend on live counters and are deliberately not modelled — the panel says so once, in a line, and
never guesses.

A loop shows up here as a cycle that hits the stop line, which is the cheapest runaway-detector we
can offer without building the governor §10 explicitly rejects.

### 6.6 The conventions overlay — the honest dashed line

Several real behaviours live only in prompts and are invisible to the graph: the `ROUTE-TO: <name>`
relay the supervisor seed uses (because workers cannot emit typed events), memory-label handoffs
in `blackboard@v1`, and temporal-hierarchy's review channel, which is memory by necessity.

Proposal: an opt-in **Conventions** toggle that scans worker prompts for other workers' names and
for `ROUTE-TO:` lines and draws them as **dashed grey edges**, labelled *"convention — written in
a prompt, not enforced by the engine"*, with the matched line quoted in the tooltip.

It is a heuristic and it must announce itself as one. It is worth building because the gap between
the org chart people *think* they have and the one the router will actually execute is where
MAST's 42%-specification failures live, and this makes that gap a picture.

---

## 7. Surfacing learning (Q4)

### 7.1 Worker lineage — the spine, filtered to one prompt

The most interesting data in the product is nearly free: every `worker_prompt_write` carries the
full new prompt and a mandatory rationale, and `buildChangelog` already diffs consecutive events
of the same key. What is missing is *placement* — it is buried in a global changelog rather than
sitting on the worker it describes.

A fourth tab on the worker page — **Configuration · Jobs · Lineage · Chat**:

```
 fee-scorer                                        🔒 frozen · 0 rewrites
 email-answerer                                        47 rewrites · 3 today
 ────────────────────────────────────────────────────────────────────────────
 │
 ●  05:40  email-reviewer                                           v12
 │  "narrowing yesterday's rule: reference only when one exists"
 │  ┌──────────────────────────────────────────────────────────────┐
 │  │  Always quote the ticket reference in the first line.        │
 │  │ +Quote the ticket reference in the first line when one       │
 │  │ +exists; otherwise open with the customer's name.            │
 │  └──────────────────────────────────────────────────────────────┘
 │  ▸ the job that decided it        ▸ what ran before / after
 │
 ●  03:12  email-reviewer                                           v11
 │  "answers kept omitting the ticket reference, so the rule is now first"
 │  +4 −1                                                    ▸ show diff
 │
 ○  yesterday 14:02  you                                            v10
 │  (no reason given — edited over HTTP)
 │
 ⌂  12 July  seeded from actor-critic@v1                             v1
```

Two behaviours make it more than a list:

- **Fold to a version.** Click v11 and the Configuration tab shows the prompt *as it was*, banner:
  *"Viewing v11 as of 03:12 · this is history, not the live prompt"*. Restoring is an ordinary
  forward write with a pre-filled rationale naming the config event — never a "revert" button
  (§15: restore is a forward operation, and S8 proved the fold reproduces what the job actually
  ran with, via `composed_prompt`).
- **A rewrite by a worker links to the job that decided it.** The link already exists in every
  record (`actor_session`); it just needs to be one click from the prompt it changed.

### 7.2 Before / after — the product's thesis on one screen

The acceptance loop's whole claim is "the composed prompt changed the model's behaviour". Show it:

```
┌────────────────────────┬──────────────────────┬────────────────────────┐
│ BEFORE   job 08:15     │  THE REWRITE  03:12  │ AFTER    job 09:40     │
│ ────────────────────── │ ──────────────────── │ ────────────────────── │
│ Hi Jane,               │ email-reviewer said: │ Ticket #4471           │
│ Thanks for getting in  │ "answers kept        │ Hi Jane,               │
│ touch about the...     │  omitting the ticket │ Thanks for getting in  │
│                        │  reference, so the   │ touch about the...     │
│                        │  rule is now first"  │                        │
│                        │                      │                        │
│                        │  +4 −1  ▸ diff       │                        │
└────────────────────────┴──────────────────────┴────────────────────────┘
     composed_prompt v11        config event         composed_prompt v12
```

Every column is data we hold: the two neighbouring jobs for that worker, and the config event
between them. It is the one screen to put in front of someone who asks what Agent Orange is.

Caveat to render, not hide: **tool calls are absent from `worker.finished` transcripts**, so a
before/after column shows what the worker *said*, never what it *did*.

### 7.3 The bench — experiment reports

The comparison rig (C1) already emits `report.json` + `report.md` with a ranked arm table, mean
±spread, property predicates and the recorded prompt rewrites. Render it, and inherit its
editorial discipline exactly:

- The **Tier A banner is not dismissable**: *"mock mode proves the machinery transmits a
  difference, never that the system discovered one."* It is the first thing on the screen.
- **`prompt_writes` is never the headline number.** C1's own demo report is the proof: the sham
  critic and the genuine critic tie at 2 ±0, and only the property predicates separate them
  (headline-rule 0.5 vs 0). A UI that ranks org charts by rewrite count ranks the placebo
  first-equal. Show churn in a muted column, always beside an outcome column, and label it
  *churn*.
- **Distinct rewrites are deduped and counted separately** — `SetWorkerPrompt` has no no-op
  short-circuit, so an identical re-fire is logged, and "47 rewrites" may be "9 distinct".
- Spread is rendered, never averaged away; in mock every spread must be 0 and a non-zero one is an
  alarm, not a data point.

Where it gets its JSON: for now, **drop a report file on the page**. The rig lives in `e2e/` and
runs outside the product; a viewer that takes a file needs zero backend and works the day it
ships. A `GET /agent/experiments` can come later if reports ever want a home.

### 7.4 What learning looks like at a glance

One sparkline per worker on the workers list — rewrites over the last 30 days, ember — plus
`n rewrites · m distinct`. Not a score. We have no honest quality number for a worker (that is the
entire point of the frozen-scorer work), and inventing one on a list page would be the exact
failure C2 warns about.

---

## 8. The memory browser (blocked, and worth asking for)

Memory is one of the three per-worker quantities and has **no HTTP surface at all** — R1's
discovery: *"There is no memories HTTP API — nothing under `/agent/*` lists memories."* So the
browser cannot be built, and that is a backend item, not a design one.

What it should be when the route exists, so the ask is concrete:

- A **selector bar**, not a search box — the query language is Kubernetes selectors and the UI
  should teach them: `kind=rolling-summary, worker=email-answerer` with chips per clause, an
  invalid clause named the way the parser names it, and the reminder that there is no OR (run two
  searches).
- **Newest-first without a query, RRF-ranked with one** — and a line saying the semantic leg is
  off unless embeddings are configured, because §7.6 has no distance threshold and a low-scoring
  result means "nothing good", not "no match". A results list that hides that is lying.
- **`name=` rendered as the KV convention it is**: current value large, superseded values folded
  beneath it, because "updating" is appending.
- **Provenance on every row** — which worker, which session, one click to the thread.
- **A briefing preview on the worker page**: given this worker's `briefing` selectors, here are the
  sections that would be injected *right now*, at their real byte cap, with the truncation marker
  where it would fall. A briefing that fails to load is only logged today; this makes it visible
  without changing the engine.

Minimum viable route: `GET /agent/memories?selector=&query=&limit=`, returning what
`memory_search` returns.

---

## 9. Prior art (Q5)

**Take: Temporal's timeline discipline.** Temporal's Web UI groups related events into single
spans and offers Compact / Timeline / Full History as three views over one history, coloured by
outcome. The lesson for us is the grouping: a delivery's `pending → running → ok` is one span, not
three rows, and the spine should span it rather than tick it. Also worth stealing: the honest
rendering of *duration*, including unfinished ones.

**Take: LangGraph Studio's "the graph is the debugger".** Studio renders the graph live and lets
you step, inspect state at each node, and replay from any point. We can offer a real subset —
propagation preview before the fact, and highlight-the-path after it — without building a
step-debugger, because our "between" layer is deliberately trivial (P3: a subscriptions table,
nothing more). What we must *not* import is Studio's premise that the graph is the program. Ours
is emergent from chained events; if the chart ever becomes authoritative we have grown the DAG
feature P3 forbids.

**Take: LangSmith's prompt-commit model** — linear versions, diff against the previous commit,
compare experiments side by side. Our config log is already this, with one advantage worth
leaning on in the design: LangSmith's commits have commit messages by convention; ours have
rationales *by construction*, required on prompt writes. The lineage view should look like a
commit history because it is one.

**Avoid: n8n's canvas as the whole product.** The three-panel canvas is a fine authoring tool and
its own users report opening it and being confused for ten minutes with nothing explaining what
they are looking at. Our chart must be a reading surface first (it answers "what is this?" the
moment it loads) and an editing surface second. Concretely: no node palette, no drag-in-a-new-node,
no canvas-first onboarding — the topology picker is the authoring path, and the canvas is where
you understand what it made.

**Avoid: the generic enterprise audit log.** The whole genre is a filterable table of
who/what/when, which is technically complete and unreadable. Ours differs on one axis the genre
does not have: **why**. The rationale is a first-class field, and the design treats every entry as
a commit — headline, message, diff — rather than a row with an expandable JSON blob. Keep the raw
payload behind "Show full state" exactly as `ChangelogView` already does.

**Avoid: dashboards of aggregate agent metrics.** Every observability tool wants to show tokens,
latency and success rate as big numbers. We have honest small numbers (delivery outcomes, refusals,
rewrites) and no honest quality number at all; a KPI row here would be the "capturable metric"
C2 warns silently corrupts the loop.

Sources: [LangGraph Studio](https://www.langchain.com/blog/langgraph-studio-the-first-agent-ide) ·
[Temporal Timeline View](https://temporal.io/blog/lets-visualize-a-workflow) ·
[Temporal Web UI](https://docs.temporal.io/web-ui) ·
[LangSmith prompt management](https://docs.langchain.com/langsmith/manage-prompts) ·
[n8n canvas](https://docs.n8n.io/courses/level-one/chapter-1/) ·
[activity-feed pattern](https://uxpatterns.dev/patterns/social/activity-feed) ·
[audit-trail pattern](https://aiuxplayground.com/pattern/audit-trail/)

---

## 10. What this needs from the backend

Ranked by value per line of Go. None of them are large; all of them are read paths except B3.

| | Ask | Unblocks | Size |
| --- | --- | --- | --- |
| **B1** | `GET /agent/attention-requests?state=open` | the Desk's Asks stack with the *message the worker wrote*. `Store.ListOpenAttentionRequests` already exists | a handler |
| **B2** | `GET /agent/memories?selector=&query=&limit=` | the whole memory browser (§8) and the briefing preview | small |
| **B3** | thread `rationale` through the worker / subscription / project-settings routes (schedules already do) | every human edit stops logging `(no reason given)`; makes canvas edits and form edits equally legible | small, touches several handlers |
| **B4** | `GET /agent/images`, `GET /agent/skills` | the worker editor's image field is validated free text today; a typo is caught at launch | small |
| **B5** | `?worker=` on `GET /agent/sessions` | per-worker job history currently filters one 200-row page client-side and says so | small |

B3 is the one with a design opinion attached: if it does not land, the canvas's mandatory "why"
field is theatre, and the Changes stack keeps showing your own edits as reasonless. It is also the
difference between a changelog that is worth reading and one that is merely complete.

---

## 11. Editorial rules

The copy carries as much of this design as the layout does. Six rules:

1. **Name things as the operator controls them, in their own vocabulary.** "When email-answerer
   finishes, wake email-reviewer" — not "create subscription on `worker.finished` with envelope
   filter". The exact fields are shown beneath, in mono, because they are also true.
2. **Never call a forward write an undo.** "Restore this version" writes a new one and says so.
3. **Say what is not modelled.** Every dry run, preview and inferred edge carries one line naming
   its limits. The docs are unusually honest about this system's edges; the UI should inherit that
   voice rather than smoothing it.
4. **An empty screen is an invitation, and a broken one is a diagnosis.** "No workers yet" is a
   shrug; "Nothing will run until something wakes them — `daily-brief` next fires at 09:00" is a
   next step. A failed delivery says *no reason is recorded on a delivery row* rather than showing
   a blank cell.
5. **Churn is labelled churn.** Rewrite counts never appear without an outcome column beside them.
6. **The word for the thing stays the same everywhere.** *Worker*, *job*, *event*, *rewrite*,
   *frozen*, *topology* — the vocabulary in §3 of the spec is the vocabulary in the UI, including
   in button labels.

---

## 12. Proposed build order (sketch, for the follow-up work plan)

Not a work plan — the shape one would take, ordered by value and by what unblocks what.

- **W0 · Mount what exists.** `ViewNav` gains Events and Automation; changelog reachable. Four
  lines of `examples/web`, and the largest single jump in visible surface in this document.
- **W1 · The theme.** Palette, the two bundled typefaces (K5), the authorship rule, hairlines.
  Shell-side; the library follows for free because it already styles through tokens.
- **W2 · The spine + the Desk.** Pure `buildDesk(deliveries, events, configEvents, attention)` in
  `web/src`, `DeskPage` component, landing view (K1) with a first-run empty state. Needs B1 to be
  complete; renders without it.
- **W3 · Worker lineage + fold-to-version.** Almost entirely `buildChangelog` re-placed.
- **W4 · `layoutOrgChart` + the chart, read-only.** In `web/` (K6). Pure layout module first,
  unit-tested; then the SVG. Propagation preview, the depth ruler and the conventions overlay
  (K4, opt-in) land here.
- **W5 · Direct manipulation.** Drag-to-wire and freeze toggles (K3), with the mandatory reason.
  Follows B3, which is now a committed engine task (K2), not a gate.
- **W6 · Before/after and the bench viewer.**
- **W7 · The memory browser.** Gated on B2.

Every step is a pure module plus a component, tested in the existing vitest style, which is how
the ~580 tests in `web/` were written and how these should be.

---

## 13. Decisions (K1–K6) — DECIDED by Kai, 2026-07-28

- **K1: the Desk is the landing view.** Ahead of Chat. It is the strongest statement that this is
  a workforce and not a chat app. Consequence the design owes back: the day-one Desk is empty, so
  its empty state is a *first-run* state — it must point at the topology flow, not shrug.
- **K2: B3 ships — human edits carry a reason.** `rationale` threads through the worker,
  subscription and project-settings HTTP routes (schedules already do). Every UI edit asks for one
  line. This is what makes the canvas's mandatory "why" real rather than theatre, and it retires
  `(no reason given)` on the Changes stack.
- **K3: direct manipulation is wire + freeze.** Drag A→B creates a subscription; nodes toggle
  enabled and frozen. Schedules stay on the row — the clocks render on the chart but do not edit
  there. Everything else goes to the worker page.
- **K4: the conventions overlay ships, opt-in and labelled.** Off by default. Each inferred edge
  quotes the prompt line it was read from and carries *"convention — written in a prompt, not
  enforced by the engine"*. A heuristic that announces itself.
- **K5: both typefaces are bundled.** Instrument Sans + IBM Plex Mono as two woff2 files in
  `examples/web` (~90KB, offline-safe), with `system-ui` / `ui-monospace` fallbacks so `web/`
  never depends on them.
- **K6: the chart is built in `web/` from the start.** Pure `layoutOrgChart` module plus a
  component, tested in the existing vitest style — the library rule holds, no prototype phase in
  the shell.

With K2 decided, **W5 (direct manipulation) is no longer gated on an undecided backend item** —
B3 becomes a Wave-1 engine task rather than a maybe.
