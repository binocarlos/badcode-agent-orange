# Learning stories — deterministic offline proof that the loop learns

*Written 2026-07-27. Status corrected 2026-07-29: **BUILT and green.** The stories run offline
against the scripted mock in `e2e/features/learning-stories.stack.spec.ts`. The plan below stands as
written — only the "nothing is built" claim was stale.*
*Companions: [`docs/AGENTS_RESEARCH.md`](../AGENTS_RESEARCH.md) (measuring real improvement),
[`10-topology-library.md`](./10-topology-library.md) (org-chart options).*

---

## 1. The idea, and its scientific name

We want cheap tests that observe *that learning happened*, without building an evaluation apparatus
and without spending a token. The technique has a name: **[metamorphic testing](https://en.wikipedia.org/wiki/Metamorphic_testing)**.

Metamorphic testing exists for exactly our situation — the **oracle problem**, where you cannot say
whether a single output is correct. Instead of judging one run, you assert a **metamorphic relation**:
a necessary property of the relationship *between* multiple runs. It is the standard answer for
testing systems that have no ground truth, and it is what "did it improve?" reduces to.

Our relation:

> **MR-1 (improvement is transmitted).** Given the same task input, after the critic has rewritten
> the actor's prompt, the actor's output satisfies a property it did not satisfy before — and the
> config log contains that rewrite with its rationale.

That is fully assertable offline, deterministically, with no judge.

## 2. Why our mock model makes this work

`go/modelproxy/script.go` selects a rule by **substring match against the raw request body** — and
the request body contains the composed system prompt. So the mock is not a fixed responder. It is a
**prompt-conditioned deterministic model**: its behaviour is a function of the prompt it is given.

That is precisely the property a self-improvement test needs. Round 0's prompt lacks a marker and a
"naive" rule serves a bad answer. The critic rewrites the prompt to include the marker. Round 1's
composed prompt now contains it, a different rule matches, and the answer is good. **Nothing in the
test simulates learning — the loop really carries it**, through real workers, real events, real
`worker_prompt_write`, real composition.

**The property that makes this rigorous.** A recurring lesson in this repo is that reading back a
value the system just wrote proves *storage*, not *delivery*. Here we get delivery for free: because
the mock matches on the request body, **a behavioural switch is itself proof the composed prompt
reached the model.** It cannot be faked by a database write. Assert both anyway — the stored prompt
(storage) and the changed behaviour (delivery) — but the second is the load-bearing one.

### What these tests do and do not prove

- **They prove transmission.** The machinery of self-improvement works end to end: a critic's edit
  reaches the next job's behaviour and is recorded.
- **They do not prove discovery.** The improvement is one *we* wrote into the script. Whether a real
  model would find it is the question `AGENTS_RESEARCH.md` exists to answer.

State this in the file header of the suite, the way the live-smoke caveat is stated elsewhere.
A green learning-story suite must never be quoted as evidence that the system self-improves.

## 3. Writing the scripts: the footgun

The match is against the **whole raw body**, which replays the entire conversation — including prior
assistant turns. So a marker string emitted in a tool call *leaks into every later request of that
session* and can match a rule meant for someone else.

Concretely: a critic whose `worker_prompt_write` input contains `"ALWAYS begin with a title"` will,
on its next turn, send a request containing that string — and match the "improved poet" rule.

Rules to follow:

1. **Partition by worker first.** Order rules so each worker's block is selected by its own name,
   which appears in every composed prompt. Distinct, non-substring names (`poet`, `poem-reviewer` —
   not `poet` and `poet-critic`).
2. **Within a worker, split before/after with `absent`.** The naive rule carries `absent: "<marker>"`;
   the improved rule carries `match: "<marker>"`.
3. **Markers should be conspicuous and unlikely.** `ALWAYS begin with a title line` beats `title`.
4. **Never rely on rule order for sequencing.** Sequencing is the turn list, always — `absent` is a
   match predicate, not a second sequencer (the file comment says so; believe it).

Sketch for story 1:

```json
{"rules":[
  {"match":"poem-reviewer","turns":[
    {"blocks":[{"type":"tool_use","name":"mcp__core__worker_prompt_write",
      "input":{"worker":"poet",
               "system_prompt":"Write short poems. ALWAYS begin with a title line.",
               "rationale":"the last poem had no title"}}]},
    {"blocks":[{"type":"text","text":"Gave the poet a title rule."}]}]},
  {"match":"ALWAYS begin with a title line","turns":[
    {"blocks":[{"type":"text","text":"Title: Rain\nSoft rain on slate."}]}]},
  {"match":"poet","turns":[
    {"blocks":[{"type":"text","text":"Soft rain on slate."}]}]}
]}
```

Three rules, one relation, no judge: round 0 has no `Title:`, round 1 does.

## 4. The stories

Deliberately trivial. The point is to observe the loop, not to create difficulty.

| # | Story | The improvement | Assertion (all string-level) |
| --- | --- | --- | --- |
| 1 | **The missing title** | Poet omits a title; reviewer adds a title rule. | `Title:` absent in round 0, present in round 1. The canonical story. |
| 2 | **The forgotten sign-off** | Answerer omits a signature; reviewer adds one. | Sign-off substring appears only after the rewrite. |
| 3 | **The missing units** | Worker reports `20`; reviewer requires units. | `°C` appears only after. |
| 4 | **The unasked question** | Actor guesses at an ambiguous request; reviewer adds "ask before assuming". | Round 1 calls `request_human_attention` and the delivery parks at `awaiting_human`. Exercises a *non-text* observable, and covers [MAST](https://arxiv.org/html/2503.13657v2) FM-2.2. |
| 5 | **The planted null** | Investigator confirms a false hypothesis; reviewer adds "report no effect when p > 0.05". | Round 1 says no significant effect. The failure that matters most in the hypothesis lab. |
| 6 | **No regression** | Two rewrites in sequence. | Properties from story 1 *still hold* after the second rewrite. Catches fix-A-break-B; this is MR-2 below. |
| 7 | **The frozen scorer** | Reviewer attempts to rewrite a frozen worker. | Refused; scorer's prompt byte-identical; refusal recorded. Gates the P0 primitive. |
| 8 | **The lineage** | Three rewrites. | Config log holds exactly three, in order, each with a rationale; folding to round *k* reproduces round *k*'s prompt. Proves P8 for the loop. |

Two further relations worth naming:

> **MR-2 (improvement is monotone).** A property established by rewrite *n* still holds after
> rewrite *n+1*, unless the log records a deliberate reversal.

> **MR-3 (no ghost learning).** With the critic disabled, the actor's behaviour on the same input is
> byte-identical across rounds. The control — it catches a test that "passes" because of ambient
> nondeterminism rather than the loop.

MR-3 is the cheapest and the one most likely to catch a false green.

## 5. Where they live

`e2e/features/`, beside `acceptance-loop.spec.ts`, run against the compose stack with both model
credentials blank so the scripted mock serves. One scenario per story, no branching, no shared
mutable project between stories. These are golden-path regression tests — the shape the industry
calls a **replay regression suite**: small, fast, offline, and run on every change.

Target: the whole suite green in a couple of minutes with zero token spend, so it can gate merges.

## 6. Sequencing

**Build these first — before the frozen-worker primitive and before the topology library.** They are
the cheapest thing in any of these three documents, they need no new schema (stories 1–6 and 8 run
against today's code), and they establish the regression floor that everything afterwards is built
on. Story 7 lands with P0.

Revised order across the three docs:

1. **Learning stories 1–6, 8** — offline, no new code, no tokens. *This document.*
2. **P0 frozen workers** + story 7.
3. **P1 topology as data**, P2 seed four.
4. **P3 hypothesis lab and calibration** — the first real-model measurement.
5. P4–P6.

---

## Sources

- [Metamorphic testing](https://en.wikipedia.org/wiki/Metamorphic_testing) — the oracle problem and metamorphic relations
- [Metamorphic Testing of Large Language Models for NLP](https://valerio-terragni.github.io/assets/pdf/cho-icsme-2025.pdf)
- [Test machine learning the right way: Metamorphic relations](https://www.lakera.ai/blog/metamorphic-relations-guide)
- [Get Experience from Practice: LLM Agents with Record & Replay](https://arxiv.org/html/2505.17716v1)
- [Why Do Multi-Agent LLM Systems Fail? (MAST)](https://arxiv.org/html/2503.13657v2)
