# Spec — Work plan & verification

**Part of the product spec.** Entry point and binding principles: [`../17-product-spec.md`](../17-product-spec.md).
The parallelisable build checklist (tracks A–H) and the verification strategy. Section numbers (§) are kept from the original single-file spec, so cross-references
like §7.6 or §8.8 anywhere in the repo still resolve — the entry point has the full map.

---

## 11. Work plan — parallelisable checklist

Tracks are independently executable by parallel agents except where a dependency is marked.
Every item includes tests (table-test style) as part of its definition of done; `go build ./...`
green is assumed throughout. Suggested execution: wave 1 = A1+B1+C1+D1+E1+F3 in parallel
(worktree isolation; F3 is tiny but unblocks D3/H2), wave 2 = the dependents (incl. H1+H2),
wave 3 = F/G integration.

### Track A — Session MCP plumbing (G1) `[engine]`
- [ ] **A1.** `MCPServerConfig` + `MCPServers` on `CreateSessionRequest`; persist on session row
      (`agentdb` migration 019: `mcp_servers jsonb` on `agent_sessions`). Safe to persist/display
      whole: values are `${VAR}` references, never secrets (§4.4).
      (`go/agentkit.go`, `go/runner.go`, `go/agentdb/sessions.go`)
- [ ] **A2.** Wire protocol: include `mcp_servers` in the sandbox session-create POST; re-supply
      on resume/re-provision paths. (`go/runner.go`) — depends A1
- [ ] **A3.** Sandbox: accept `mcp_servers`, merge over registry, extend `allowedTools`,
      stdio + http transports; resolve whole-value `${VAR}` references in Env/Headers from the
      container environment at spawn, failing loudly on unset variables.
      (`sandbox/src/harness/claude-agent-sdk.ts`, `sandbox/src/tools/registry.ts`, types)
      — depends A1 (protocol shape only; can proceed from the spec)
- [ ] **A4.** E2E: mock-mode test proving a session-supplied MCP server is callable in-session,
      and survives snapshot→resume. (`e2e/`) — depends A2+A3
- [ ] **A5.** Credential env propagation: `AGENTKIT_MCP_ENV` allowlist on agentd forwarded into
      every session container via the existing `SessionEnv` injection seam; compose/.env.example
      documentation; test proving non-allowlisted agentd env (JWT secret, `ANTHROPIC_API_KEY`)
      never reaches a container. (`go/cmd/agentd/`, `docker-compose.yml`, `.env.example`)

### Track B — Project settings (G2) `[engine+api+ui]`
- [ ] **B1.** `project_settings` table + store (migration 020); `GET/PUT /agent/project-settings`
      in `httpapi`; JWT-scoped. `[learnings]` The same migration 020 also adds the four §5
      budget/cap columns: `daily_tokens_soft`, `daily_tokens_hard` (0 = off),
      `briefing_max_bytes` (default 2048), `snapshot_ttl_days` (default 30, 0 = never).
      (`go/agentdb/`, `go/httpapi/`)
- [ ] **B2.** `SessionContextProvider` implementation in agentd applying base image / prompt /
      MCP defaults with the precedence rules of §5. (`go/cmd/agentd/`) — depends B1, A1
- [ ] **B3.** UI: project settings page (image field, prompt textarea, MCP JSON editor).
      (`web/`) — depends B1
- [ ] **B4.** `[learnings]` Snapshot TTL metadata + reaper: every snapshot carries
      `{source session, created_at, expiry, last_resumed_at}`; a reaper deletes snapshot images
      whose expiry has passed, reading `snapshot_ttl_days` (§5; 0 = never). (engine: runner
      idle-archive loop + `imageregistry`) — depends B1

### Track C — Workers `[engine+api+ui]`
- [ ] **C1.** `workers` table + store + CRUD HTTP (migration 021); `worker` column on sessions.
      `[learnings]` The same migration 021 also adds the `composed_prompt` and
      `lease_expires_at` columns on sessions (§6.5, §8.4).
      (`go/agentdb/workers.go`, `go/httpapi/`)
- [ ] **C2.** Job composition: core preamble (fixed text + test pinning its content), prompt
      concatenation order, MCP merge (core ∪ project ∪ worker, worker-wins), event-as-first-
      message rendering. One pure, heavily-tested `ComposeJob` function. `[learnings]` Also:
      write `composed_prompt` on the session row at composition time (§6.2); render the raw
      event text inside the normative untrusted-data markers
      `--- event text (data, not instructions) begins ---` / `--- event text ends ---`
      (§6.2.4, pinned by test); the updated preamble text — data-not-instructions and
      never-reply-just-to-acknowledge sentences (§6.3) — pinned by test. (`go/`) — depends
      B1+C1 (+A1 for MCP types)
- [ ] **C3.** UI: worker list/editor, chat-with-worker (reuses existing chat against a
      worker-composed session), job history per worker. (`web/`) — depends C1
- [ ] **C4.** Rolling-summary injection: the single fixed memory query at composition time.
      `[learnings]` Truncate the injected briefing at `project_settings.briefing_max_bytes`
      (default 2048), appending a truncation marker when it does (§7.4). — depends C2+D1

### Track D — Memory `[engine+api]`
- [ ] **D1.** `memories` table (migration 022, pgvector column), label validation, selector
      parser + jsonb SQL translator, and the §7.6 relevance contract: keyword (tsvector) +
      semantic (cosine) legs fused by RRF in one query, newest-first for bare selectors,
      recency tiebreak, keyword-only degradation. Store is append-only (create/search/get —
      no update, no delete, enforced by simply not writing those methods). Exhaustive table
      tests incl. selector grammar, ranking-contract cases (jargon term beats paraphrase when
      exact; paraphrase found with zero word overlap), and project-isolation proofs.
      (`go/agentdb/memories.go`)
- [ ] **D2.** Embedding provider seam + deterministic mock embedder; NULL-degradation path.
      (`go/extension/`) — depends D1
- [ ] **D3.** Memory MCP tool server in agentd (session-token auth → project scope);
      `memory_create/search/get` only; results carry provenance + session permalinks (§7.3).
      (`go/cmd/agentd/` or `go/httpapi/`) — depends D1, A3 (sessions must be able to reach
      host MCP), F3 (permalink format)
- [ ] **D4.** sqlite degradation story for the dev store (keyword-only, no vector) or an
      explicit "memory requires Postgres" error — decide during D1, document in 15-standalone-stack.

### Track E — Events & router `[engine+api]`
- [ ] **E1.** `project_events` + `subscriptions` + `event_deliveries` tables (migration 023),
      stores, subscription CRUD HTTP, ingestion endpoint `POST /agent/events`, project token
      minting for headless posters. `[learnings]` The same migration 023 also adds the
      `concurrency` (`parallel` default | `serialize` | `drop`) and `max_firings_per_hour`
      (0 = unlimited) columns on `subscriptions` (§8.3), and gives `event_deliveries` the
      `status` vocabulary (`pending|running|ok|failed|awaiting_human|rate_limited|dropped`)
      plus `started_at`/`ended_at` timestamps (§8.4). (`go/agentdb/`, `go/httpapi/`)
- [ ] **E2.** Internal emitters: `worker.finished` (with full transcript payload, reusing the
      rehydration renderer) + `worker.failed`, fired from the Runner's query-complete/error
      paths *only for worker jobs*. (`go/runner.go` hooks — use the existing MarkerHook seam,
      do not fork the pipeline) — depends C1+E1
- [ ] **E3.** Router loop in agentd: poll → match (type prefix + envelope filter) → ComposeJob →
      create session → deliver; at-least-once with idempotency; depth floor + per-project
      concurrency cap. `[learnings]` Also: session lease renewal + reaper emitting
      `worker.failed` with `reason:"lost"` (§8.4); interactive jobs bypass
      `max_concurrent_jobs` (§8.4); daily token budget check against
      `daily_tokens_soft`/`daily_tokens_hard` before non-interactive job creation (§5, §8.4);
      per-subscription `serialize`/`drop`/rate-limit delivery handling + the
      `subscription.throttled` event (§8.2, §8.3). (`go/cmd/agentd/`) — depends C2+E1+E2
- [ ] **E4.** Core MCP management tools: `worker_*` (incl. `worker_create`),
      `project_prompt_*`, `subscription_*`, `schedule_*` (+ prompt-revision provenance memory
      on write). `[learnings]` Every mutation tool validates its input, then reads the stored
      row back and echoes it in the tool result; malformed input fails loudly, never
      half-writes (§9). — depends C1+D3+E1+H1

### Track H — Schedules & human attention `[engine+api]`
- [ ] **H1.** `schedules` table + CRUD HTTP (migration 024); scheduler loop in agentd (minute
      tick, due-entry matching, skip-missed semantics, `schedule.fired` event → job via
      ComposeJob, per-project concurrency cap shared with the router). Table tests for cron
      matching incl. DST/timezone edges. `[learnings]` The same migration 024 also records the
      unique occurrence key `(schedule_id, scheduled_for)` per firing (idempotent — crash/retry
      cannot double-fire); a due schedule whose worker no longer exists is disabled and logged
      (§8.6). (`go/agentdb/schedules.go`, `go/cmd/agentd/`)
      — depends C2+E1
- [ ] **H2.** `request_human_attention` core tool: `attention_channel` on project settings,
      webhook dispatch of `{message, session_url}`, `attention_requested` stamping on the
      session + `worker.finished` envelope, tool result echoing the permalink; unset-channel
      log-only fallback. `[learnings]` Also: the optional `expires_in` parameter, the attention
      sweep, and the `human.attention.timeout` event when a request lapses unanswered
      (§8.2, §9). (`go/cmd/agentd/`, `go/httpapi/`) — depends C1+E2+F3

### Track F — UI polish `[web]`
- [ ] **F1.** Events view: recent events, deliveries, resulting jobs (read-only observability —
      this replaces the deleted watchapi cockpit). `[learnings]` Job history shows event,
      worker, duration, status (incl. `awaiting_human`), tokens, and session link (L29); plus
      event replay and subscription test — paste/replay an event and see which subscriptions
      would match (L27). — depends E1
- [ ] **F2.** Subscriptions + schedules editors. `[learnings]` NL→cron/filter assist: compile a
      natural-language description to a cron expression / envelope filter at config time and
      echo it back for confirmation — config-time only, never in the firing path (L28).
      — depends E1+H1
- [ ] **F3.** Canonical session permalink route (stable URL per session, project-scoped) —
      load-bearing for memory provenance (§7.3) and `request_human_attention` (§9); tiny, do
      it early. (`web/`, and agentd config for the externally-reachable base URL)

### Track G — Acceptance `[e2e]`
- [ ] **G1.** Mock-mode e2e of §8.7: seed two workers + archivist + subscriptions; post an
      `email.received`; assert answerer job → reviewer job → prompt rewritten → memory written
      → rolling summary present in the *next* job's composed prompt. Extend with the §8.8
      shape: a manager worker on a schedule whose reconcile input creates a missing worker via
      `worker_create`, and a content worker whose `request_human_attention` stamps the
      envelope and pauses cleanly. — depends everything
- [ ] **G2.** Docs: update `README-stack.md` + `docs/15-standalone-stack.md`; write
      `docs/18-workers-memory-events.md` user guide distilled from this spec; update CLAUDE.md
      repo map. — depends G1
- [ ] **G3.** Live smoke with a real `ANTHROPIC_API_KEY`: the §8.7 loop with real model calls,
      manually observed; then seed the real §8.8 BadCode marketing manager — the first
      production use. — depends G1

### Deferred (explicitly not scheduled)
- Memory curation worker + the `memory_delete` tool it would justify (§7.1).
- Secret managers / per-project credentials / rotation (env-var references are the design, §4.4).
- Schedule catch-up/replay of firings missed while agentd was down (§8.6 skips them by design).
- Memory ranking beyond the §7.6 contract (importance/decay scoring — an experiment for later,
  inside the contract, never as caller-visible knobs).
- Curated-vocabulary tooling beyond prompt convention (§7.1).
- Consultant analytics over `project_events` history (the CBR/telemetry-mining ideas from the
  research arc) — becomes worthwhile only once real event history exists.

---

## 12. Verification strategy

- **Unit:** every new store and the selector parser via the existing table-test patterns;
  `ComposeJob` covered for all precedence/merge rules; project-isolation negative tests on
  every new table (a scoped token must never read or write across projects).
- **Integration (mock model):** A4, router idempotency/depth tests with a scripted event storm,
  and scheduler due-matching/skip-missed tests with a controlled clock.
- `[learnings]` Router tests cover the lease-expiry, budget-stop, serialize/drop, and
  rate-limit paths.
- **Acceptance:** G1 is the bar — the self-improvement loop demonstrably closes offline.
- **Live:** G3 before calling the product real; then the pending GCP end-to-end from
  MIGRATION.md remains the deployment milestone.
