# Work plan — console remaining work (the close-out tickets)

*Created 2026-08-06. The tail of the operator-console effort: docs
[`15`](./15-operator-console-design.md) (design, decisions K1–K6),
[`16`](./16-work-plan-operator-console.md) (build, 16/16 done) and
[`21`](./21-console-ux-review.md) (UX/motion review, W1–W6 done + live-pass findings fixed)
are ALL COMPLETE. This file holds only what their Discovered Issues Logs left open, written to be
executable by an agent with no other context than the repo. Read those three docs' DIL sections
before starting — they are binding scar tissue, not history.*

## EXECUTION RULES

1. **Ground yourself first**: this file, then the Standing traps in doc 16, then the Discovered
   Issues Logs of docs 16 and 21 (all three bind you), then the files an item names.
2. **This branch is shared with other live sessions.** Never `git add -A`, never `git commit
   --amend`, never `git stash`, never rebase. Stage explicit paths; if `git status` shows files
   you did not touch, leave them alone and say so in your report.
3. **Run each item's validation verbatim before ticking it.** Tick boxes in THIS file as you
   complete items (this plan is yours to update), and append surprises to the Discovered Issues
   Log at the bottom.
4. **Do not touch:** `.env` (real credentials — documenting it is R4's job, editing it is
   forbidden), `e2e/experiments/`, `sandbox/`, the shared test Postgres `ao-test-pg` on
   `localhost:5433` (use, never recreate).
5. **Search hygiene:** several `web/src` files contain non-UTF8 bytes and grep as binary —
   **always `grep -an`** in `web/src` (doc 21 DIL).
6. **The compose stack is a serial resource.** Check nothing else holds it before `docker compose
   up`; the e2e stack may hold port 8080 — the main stack runs fine on `WEB_PORT=8081`.
7. Design authority: doc 15 §3 (tokens, type rules) and doc 21 §4–§5 (motion rules) win over any
   judgement call here.

## Context an executor needs (one paragraph)

The console is a MUI-based component library (`web/`, npm, ~1220 vitest tests, no router, no new
runtime deps allowed, every export through `web/src/index.ts`) composed by a shell
(`examples/web/`, **yarn only**). Its design rests on four named colours (ember/steel/rose/fault,
resolved via `spine.tsx`'s exported `CONSOLE_TOKENS`/`consoleTokenColor` with per-mode fallbacks)
over paper/ink neutrals, in light AND dark themes (`examples/web/src/theme.ts`). The product-layer
pages follow this; the older chat-side components predate it. The fixture rig
`e2e/ux-stub/` (README inside) serves a populated fake org to the BUILT shell bundle and is how
visual claims get verified without the real stack; the real stack runs via
`WEB_PORT=8081 ANTHROPIC_API_KEY= CLAUDE_CODE_OAUTH_TOKEN= AGENTKIT_BLOB_BACKEND=fs
AGENTKIT_REGISTRY_BACKEND=blobarchive GOOGLE_APPLICATION_CREDENTIALS= docker compose up -d --build`
(every override is load-bearing — see R4).

*Validation shorthand used below:*
- **WEB** = `cd web && npm ci && npm run typecheck && npm test`
- **SHELL** = `cd examples/web && yarn && yarn typecheck && yarn build`
- **RIG** = rebuild the shell (`SHELL`), then from `e2e/`: start
  `node ux-stub/stub-server.mjs` and run `node ux-stub/shoot-app3.mjs`; read the screenshots
  with your own eyes (both themes matter). `SHOT_DIR` env sets the output dir.

---

## R1 — Chat-side dark-mode sweep *(the last visible defect; do this first)*

- [ ] The ~18 chat-era components carry hardcoded light-mode colours that break dark mode
  (filed by TH1, doc 16 DIL): `AgentChat`, `ChatHistoryDrawer`, `ChatInputToolbar`,
  `ToolCallGroup`, `ThinkingBlock`, `ScriptExecutionBlock`, `CodeCreatedBlock`, `AskUserCard`,
  `RecordingOverlay`, `InlineArtifactPreview`, and the Artifact family (`ArtifactCodePreview`,
  `ArtifactCsvPreview`, `ArtifactGrid`, `ArtifactLightbox`, `ArtifactPanel`,
  `ArtifactPreviewDialog`, `ArtifactTreeView`, `ArtifactViewer`). Worst offenders: literal
  `1px solid rgba(0,0,0,0.06)` hairlines (invisible on `#0E1114`), `ArtifactTreeView`'s
  `STATUS_DOT_COLORS` and `#dbeafe` selection fill, Tailwind-ish grey/blue hexes throughout.
  **Replace with theme tokens** (`divider`, `text.secondary`, `background.paper`,
  `action.hover/selected`, `theme.palette.mode`-aware pairs where a token has no equivalent).
  Do NOT introduce the console's ember/rose/fault into chat surfaces — chat is neutral territory;
  this is a de-hardcoding sweep, not a redesign. Prism code themes may legitimately stay
  light-styled if fenced in their own surface — judgement call, state it in the report.
  Sweep hygiene: `grep -anrE '#[0-9a-fA-F]{3,8}\b|rgba?\(' web/src/components/ | grep -v test`
  and triage every hit in the listed files (hits in product-layer files are likely fine — they
  resolve through token fallbacks; verify, don't assume).
  *Validation:* WEB, then RIG and actually look at `pop-desk-dark.png` plus a dark-mode Chat
  view screenshot (the walkthrough lands on chat in dev-mode; extend the shoot script locally
  if needed — do not commit shoot-script changes unless they generalise).

## R2 — e2e feature specs for the console *(the missing regression net)*

- [ ] Nothing in `e2e/features/` covers the new views. Add `console.stack.spec.ts` in the
  existing pattern (look at `product-ui.stack.spec.ts` and `topologies.stack.spec.ts` for the
  helpers, auth and run-scoped-project conventions; `e2e/run-stack-e2e.sh` runs the rig —
  **serial resource**, mock mode). Cover, minimally:
  (a) the eight nav views all render for a fresh project (Desk shows its first-run state);
  (b) apply `actor-critic@v1` via the UI flow → the Chart draws two plates and two wires, and
      the Desk's changes stack shows `seeded from actor-critic@v1` rationales;
  (c) post an event via the API helper → delivery goes `ok` → the chart's wire carries `↳ ×1`;
  (d) **drag-to-wire under a real pointer**: on the Chart, create a wire via the node menu
      (keyboard path is fine — it exists precisely for testability), fill the mandatory
      "why", assert the subscription exists with that rationale in the changelog;
  (e) freeze toggle: freeze a worker from the chart's node menu with a reason, assert
      `worker_freeze` + rationale in the config log, and that the plate shows frozen.
  Traps that WILL bite (all filed): assert on happens-after signals never sleeps; delete
  sessions in teardown (port pool); the stack serves a BUILT web image — rebuild before
  asserting UI changes; worker names must not be substrings of each other.
  *Validation:* `./e2e/run-stack-e2e.sh up mock` then
  `./e2e/run-stack-e2e.sh test mock -- e2e/features/console.stack.spec.ts`, green, plus the
  existing `product-ui` spec still green (shared stack state).

## R3 — Turn the live tail on *(the pill and pause toggle are inert)*

- [ ] W4 built staged arrivals, the "N new" pill and the "Pause live updates" toggle, but gated
  them behind an opt-in `refreshMs` that nothing passes (doc 21 DIL). In
  `examples/web/src/App.tsx`, pass `refreshMs={15000}` to `DeskPage` and the Events surface
  (check the exact prop plumbing in `useDesk`/`EventsPage` — W4's report says both accept it).
  Verify the pause toggle now renders and that a paused surface stops polling (network tab or
  a stub-server request log). Keep it to the shell — no library default changes.
  *Validation:* SHELL + WEB, then RIG with `SCENE=burst` (the stub reveals more deliveries per
  fetch): within ~30s of watching, the pill or new rows should appear without a reload.

## R4 — Document the stack's two boot traps *(docs only; `.env` itself is off-limits)*

- [ ] Found on the live pass (doc 21 DIL): (a) `.env` as committed sets
  `AGENTKIT_BLOB_BACKEND=gcs` with `GOOGLE_APPLICATION_CREDENTIALS=/gcp/key.json` — a
  *directory* — so agentd exits at boot; (b) `.env` holds a real `ANTHROPIC_API_KEY`, so a
  plain `docker compose up` runs a REAL billable agent, and mock mode needs BOTH credentials
  blanked. Add a clearly-titled subsection to `README-stack.md` (and a one-line pointer in
  `CLAUDE.md`'s "Run it" section) giving the exact known-good local/mock invocation from the
  Context paragraph above, and the boot-log line that confirms mock mode
  (`ANTHROPIC_API_KEY unset → MOCK model proxy`). Do not edit `.env`.
  *Validation:* the documented command works from a clean `docker compose down` (leave the
  stack down afterwards unless R2 needs it).

## R5 — Mechanical debt sweep *(small, self-contained)*

- [ ] **Token-table copies**: `ChangelogView.tsx` and `OrgChartPage.tsx` still carry hand-copied
  ember/fault token values predating `spine.tsx`'s exported `CONSOLE_TOKENS` (doc 21 DIL said
  "one authority plus two legacy copies"). Point both at the export; delete the copies.
- [ ] **Stale comment**: `web/src/index.ts`'s config-log export block still says the read route
  does not exist (third of three found; the other two were fixed in V1). Fix it.
- [ ] **Non-UTF8 bytes**: `useEvents.ts`, `desk.ts`, `DeskPage.tsx` grep as binary from stray
  non-UTF8 bytes (NOT the U+2212 minus characters, which are legitimate). Find and normalise
  the offending bytes so `grep -n` works again — byte-level diff before/after to prove nothing
  else changed. **Leave `WorkerEditor.tsx` alone** — its NUL bytes are live sentinel values.
  *Validation:* WEB after each; for the bytes item also
  `grep -c "" web/src/useEvents.ts` works and `git diff --stat` shows only those files.

## R6 — OPTIONAL (needs a decision or backend work; report, do not start without one)

- **`rate_limited` countdown**: needs a retry-after column on `event_deliveries` (backend).
  Filed; skip unless Kai asks.
- **Memory selector as chips-per-clause**: doc 15 promised a teaching selector bar; the shipped
  one is a plain text field. Moderate UI work; skip unless Kai asks.

## Discovered Issues Log

*(append here; the orchestrator files nothing for you)*
