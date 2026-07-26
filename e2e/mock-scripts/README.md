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
that starts the job — because that becomes the job's first user message.

**Not the system prompt.** A marker placed in a worker's system prompt does not match, even though
the composed prompt provably contains it and the dispatcher hands it to the session as
`SystemPrompt`. The same marker in event text matches every time, and a direct POST to the proxy
carrying it in `system` also matches — so the matcher handles `system` fine, and something between
composition and the model request is dropping it. That is logged as a `(G1)` finding; until it is
resolved, put markers where a job's *input* goes.

Not the worker's name either: a name is a substring of every composed prompt that mentions it,
including a manager's prompt describing the workforce it should create, so it would fire in the
wrong job.

## What's here

| Script | Drives |
| --- | --- |
| `g1-acceptance.json` | G1 §8.8: the manager's reconcile hiring `tweet-author` via `worker_create`, and the content worker pausing for sign-off via `request_human_attention` |
