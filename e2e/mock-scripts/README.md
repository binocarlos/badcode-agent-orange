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
