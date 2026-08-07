# Spec — Management tools & human attention

**Part of the product spec.** Entry point and binding principles: [`17-product-spec.md`](17-product-spec.md).
The core MCP tools every session gets: prompt management and request_human_attention. Section numbers (§) are kept from the original single-file spec, so cross-references
like §7.6 or §8.8 anywhere in the repo still resolve — the entry point has the full map.

---

## 9. Management tools (core MCP, granted to every session)

Alongside `memory_*` (§7.3), `subscription_list/create/delete`, and
`schedule_list/create/update/delete` (§8.6):

- `worker_list()` → `[{name, description, enabled}]`
- `worker_create(name, description, system_prompt, mcp_config?, image?, max_instances?,
  briefing?)` — how a manager hires (§8.8). The optional fields set the worker's image
  pointer (§13), instance cap (§6.1), and briefing selectors (§7.4).
- `worker_update(name, fields, rationale?)` — partial update of the non-prompt worker
  fields (`fields` ⊆ {`description`, `image`, `max_instances`, `briefing`, `enabled`}).
  The prompt stays exclusively behind `worker_prompt_write` — its revision-memory semantics
  are special. Adopting an image is done here: pointing a worker at a `name` (floating) or
  `name:version` (pinned) is a visible, config-evented act, never an automatic side effect
  of snapshotting.
- `worker_prompt_read(name)` / `worker_prompt_write(name, system_prompt, rationale)` —
  wholesale string replace (P4). `rationale` is required: the commit-message-style *why*,
  stored in the config event (§15) and echoed in the automatic `kind=prompt-revision` memory
  that writing also stores, containing the previous prompt (provenance + manual rollback;
  NOT a versioning feature — it's just a memory).
  This is a load-bearing choice: because every prompt update lands in memory, the *pattern* of
  prompt updates is itself searchable evidence. A second-layer consultant can study how a
  first-layer consultant has been revising prompts and steer it — by updating the consultant's
  prompt. Self-improvement recurses through the same two tools with nothing added to core.
- `project_prompt_read()` / `project_prompt_write(system_prompt, rationale)` — same, project
  level (`rationale` required here too).
- `memory_current(name)` — the current value of a named memory (§7.3).
- `image_create(name, labels)` / `image_list(label_selector?)` — snapshot the session's current
  environment as a new version of a named image; list images by label selector (§13).
- `skill_create(name, labels, markdown, install_sh?)` / `skill_list(label_selector?)` /
  `skill_get(name)` / `skill_install(name)` — project skills: create, list, read, and
  install into the current session (§14).
- `config_history(query)` — query the config log by label/range (§15).

Every mutation tool (`worker_create`, `worker_update`, `worker_prompt_write`,
`project_prompt_write`, `subscription_*`, `schedule_*`, `image_create`, `skill_create`)
validates its input before writing — non-empty prompt,
parseable cron, known worker name — then reads the stored row back and echoes it in the tool
result, so the caller sees exactly what persisted. Malformed input fails loudly with an error;
nothing is ever half-written (L13).

Every mutation tool also appends a `config_events` record in the same transaction as the
projection-table write and emits a routable `config.changed` event (§15). `rationale` is
required on the two prompt-write tools and optional on every other mutation.

And the human-in-the-loop primitive:

- `request_human_attention(message, expires_in?)` — the worker is saying "a human needs to look at this
  thread". Mechanics (deliberately almost nothing): agentd posts `{message, session_url}` to
  the project's `attention_channel` (§5) and stamps the session/`worker.finished` envelope
  with `attention_requested`; the tool result echoes the permalink; the worker then simply
  ends its turn. The session pauses the way every session pauses (idle → archived; snapshot
  and resume already exist). The human clicks through to the **ordinary chat UI**, reads the
  thread, and whatever they type is the next message — "post it" grants permission; "change
  the tone" starts a live interaction loop. There is no approval state machine, no draft
  queue, no pending-items UI: the *thread itself* is the review surface, and staged autonomy
  (§8.8.3) is one sentence in a prompt. `expires_in` is optional; when set, a request that is
  still unanswered past expiry causes core to emit a `human.attention.timeout` event (§8.2),
  so the *worker's prompt* decides the fallback on its next run — staged autonomy remains a
  prompt pattern, and no approval machinery grows (L30).

No approval gate in core. If a project wants review-before-apply beyond that, it is a worker
arrangement (a proposer that writes `kind=prompt-proposal` memories and a gatekeeper worker
that applies them). We deleted the approval engine deliberately; do not re-grow it.
