# Spec — Workers

**Part of the product spec.** Entry point and binding principles: [`../17-product-spec.md`](../17-product-spec.md).
The worker data model and deterministic job composition (pre-prompt manipulation). Section numbers (§) are kept from the original single-file spec, so cross-references
like §7.6 or §8.8 anywhere in the repo still resolve — the entry point has the full map.

---

## 6. Workers

### 6.1 Data model

Table `workers`:

| column | type | meaning |
| --- | --- | --- |
| `project` | text | hard namespace (composite PK with `name`) |
| `name` | text | kebab-case identity, e.g. `email-answerer`, `email-review-consultant` |
| `description` | text | one-liner for the UI and for other workers' context |
| `system_prompt` | text | the worker-level prompt (plain string, may be very large) |
| `mcp_config` | jsonb | worker-level MCP servers, merged over project-level |
| `enabled` | boolean | disabled workers ignore subscriptions (manual chat still allowed) |
| `created_at` / `updated_at` | bigint | |

No model tier, no budget, no memory namespace, no skills column — resist re-growing the deleted
`board_staff` table. If a worker needs a different model, that is a session-create parameter a
subscription can carry (§8.3); if it needs special software, that is the project base image.

### 6.2 Job composition (pre-prompt manipulation)

When a job starts for worker W in project P, the effective session is composed deterministically:

1. **Image** = P.base_image (else global default).
2. **System prompt** = concatenation, in order, with clear separators:
   1. *Core preamble* (small, fixed, engine-owned — §6.3),
   2. P.system_prompt,
   3. W.system_prompt,
   4. *Memory briefing* — the worker's rolling summary, if one exists (§7.4).
3. **MCP servers** = core tools (§7) ∪ P.mcp_config ∪ W.mcp_config (worker wins name collisions
   with project; core tools are non-overridable).
4. **First user message** = the triggering event, rendered as: event type, envelope metadata,
   then the raw text (e.g. the full inbound email; a schedule's input instruction; or for
   `worker.finished`, the finished job's transcript — §8.2).

Composition is code (deterministic, testable); *content* of every part except the preamble is
data. This is the entire "pre-prompt manipulation" machinery — there is deliberately nothing
else.

### 6.3 Core preamble

A short fixed text (checked into `go/`, versioned with the engine) that tells every worker the
things that are true by construction:

> You are the worker "<name>" in project "<project>". You have a persistent memory store
> containing everything workers in this project have chosen to remember, searchable with the
> `memory` tools by label and by content — search it before making decisions that prior work
> might inform. You have tools to read and update worker and project system prompts. When your
> job is done, simply finish; your completion is itself an event other workers may react to.
> You may be running with no human present: never block waiting for user input unless the job
> came from an interactive chat. If you genuinely need a human, call `request_human_attention`
> with a message explaining what you need — a link to this conversation will reach them, and
> their reply will arrive as your next message.

Keep it under ~150 words. Everything project-specific belongs in P/W prompts, not here.

### 6.4 Interactive chat with a worker

The web UI lets a human open a chat with any worker: this is just a job whose triggering "event"
is the human's message and whose session stays interactive. Same composition path — no special
case beyond allowing `ask_user`.

### 6.5 HTTP + UI

- `GET/PUT/DELETE /agent/workers/{name}`, `GET /agent/workers` (project from JWT).
- UI: workers list per project; worker page with prompt editor, MCP JSON editor, enabled
  toggle, "chat with this worker" button, and the job history (sessions filtered by worker).
- Sessions gain a `worker` column (nullable — plain vanilla sessions remain possible) so
  history, events, and the UI can group jobs by worker.
