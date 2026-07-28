# Mock model scripts

Scripts for `AGENTKIT_MOCK_MODEL_SCRIPT` — how a test makes the **model** call a tool.

The stack's mock model otherwise serves a fixed canned reply and never emits a tool call, so
without one of these no mock-mode test can exercise a tool the way a job does. Load one per run:

```sh
./e2e/run-stack-e2e.sh test --mock-script e2e/mock-scripts/g1-acceptance.json
```

That restarts agentd with the script, runs the suite, and restores the plain model afterwards
however the run ends. It is deliberately not part of `up`: the script is agentd-wide and read once
at boot, so leaving one loaded would quietly change the model's behaviour for every later run and
for anyone else sharing the stack.

## The format

```jsonc
{"rules": [{"match": "<substring>", "absent": "<substring>", "turns": [ {"blocks": [ … ]} ]}]}
```

- **`match`** is a substring of the **raw model request**, and `absent` (optional) keeps a rule away
  from requests containing that substring. Both are match predicates only.
- **`turns`** is the sequencer, and the only one: the turn is chosen by the **assistant-message
  count** in the request. Turn 0 is the model's first reply; turn 1 is what it says once the tool
  result has come back. That makes it stateless — parallel sessions, retries and re-runs cannot
  contaminate each other.
- Matching nothing, or running past the end of the turns, yields the ordinary canned text. Never a
  stray or repeated tool call.
- Tools are namespaced by the server that serves them. agentd's core MCP server is called `core`,
  so its tools are addressed as `mcp__core__<tool>`.

**JSON only — no comments.** Unknown fields are a *boot failure*, deliberately: a rig that asked for
a script and silently didn't get one is worse than one that refuses to start. (This file exists
because that rule cost me a run: a `_comment` key stopped agentd dead.)

## Choosing a `match` key

Match on a marker planted in the **trigger text** — the schedule input, or the text of the event
that starts the job (it becomes the job's first user message) — or in the worker's **system
prompt**. The latter works because for a routed worker job the composed prompt is sent verbatim
every turn (the `fix-prompt` repair, 2026-07-26; `a worker's own prompt reaches the model` in
acceptance-loop.spec.ts is the guard). A prompt-planted marker is what the learning stories run
on: a `worker_prompt_write` that adds the marker flips which rule the actor's next job matches,
so the behaviour switch is itself proof of prompt delivery.

Beware the worker's *name* as a sole key: a name is a substring of every composed prompt that
mentions it, including a manager's prompt describing the workforce it should create, and of every
transcript that names it — a `worker.finished` event's text is the finishing job's whole
transcript, so a critic triggered by an actor receives the actor's name, output and event text in
its first message. Names still partition well if the rules are **ordered**: put the critic's rule
above the actor's (the critic's requests contain the actor's name and, after its tool call, the
marker — never the other way round), and keep names mutually non-substring
(`ls1-poet`/`ls1-reviewer`, not `poet`/`poet-critic`).

## What's here

| Script | Drives |
| --- | --- |
| `g1-acceptance.json` | G1 §8.8: the manager's reconcile hiring `tweet-author` via `worker_create`, and the content worker pausing for sign-off via `request_human_attention` |
| `learning-stories.json` | The learning stories (`features/learning-stories.stack.spec.ts`): one composite table, rules partitioned by worker name, before/after actor states split with `absent` on each story's marker |
| `topologies.json` | The topology seeds T4–T7 (`features/topologies.stack.spec.ts`): per-seed name prefixes (`tp4-`…`tp7-`), critics above actors, and the supervisor's specialists keyed on their unique identity phrase (`You are tp6-hand-N`) because dispatcher and specialist requests each contain the other's *name* |
| `calibration-smoke.json` | The calibration rig's mock smoke (`experiments/calibration/configs/smoke-4.ts`): two arms of `hypothesis-lab@v1`, prefixes `cala-`/`calb-`. Rule order is checker → critic → investigator. The checker's rules key on the harness's `[CAL-CHECK-<id>-YES\|NO]` check marker; the critic's on `You review cala-invest` (a phrase only its own prompt holds, and its body carries the investigator's whole transcript); the investigator's come in pairs per hypothesis, split with `absent: "[CAL-CONTROL-RULE]"` so the critic's rewrite arriving in the composed prompt is what flips the answer. Loaded by `experiments/calibration/run.sh run`, not by `run-stack-e2e.sh` |
| `triage-smoke.json` | The triage rig's mock smoke (`experiments/triage/configs/triage-smoke-6.ts`): two arms of `triage-lab@v1`, prefixes `tra-`/`trb-`. Rule order is auditor → critic → **queues** → dispatcher. The auditor's rules key on the harness's `[TRI-AUDIT-<ticket>-<ROUTE>]` marker and carry no arm tag, so nine rules serve both arms; the critic's on `You review tra-dispatch`; the three queues per arm on their identity phrase. The queues sit ABOVE the dispatcher — the reverse of `topologies.json`'s supervisor block — because a fan-out seed delivers the dispatcher's whole transcript (ticket marker included) to every queue, so a marker-keyed dispatcher rule placed higher would answer the queues' requests. The dispatcher's rules come in pairs per ticket, split with `absent: "[TRI-RULE-APPLIED]"`, and the ticket marker carries the ARM (`[TRI-A-T01]`) because a `ROUTE-TO` line names a worker and the two arms have different ones. Loaded by `experiments/triage/run.sh run`, not by `run-stack-e2e.sh`; `experiments/triage/script.test.ts` pins the ordering offline |
| `experiments-compare.json` | The comparison rig's demo (`experiments/configs/actor-critic-vs-sham-vs-solo.ts`): three arms on one task, prefixes `xpa-`/`xps-`/`xpz-`. Loaded by `experiments/run.sh compare`, not by `run-stack-e2e.sh`. Rule order is critic → prompt-state → actor: each critic's rule sits above its actor's (the critic's body carries the actor's transcript), and the two prompt-state rules (`XPA-HEADLINE-RULE`, the reordered sentence pair) sit between them — below the critic that writes the marker into its own tool call, above the actor whose composed prompt then carries it |
