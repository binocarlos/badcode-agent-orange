# ux-stub — populated-fixture walkthrough rig

The fixture system behind the doc-21 UX review screenshots (docs/product/ux-review/).
Photographing empty states hides most rendering defects; this serves the built shell against a
deterministic, populated org so every surface renders with real-looking data.

- `fixtures.mjs` — the "BadCode email desk, mid-morning" scene: 6 workers (incl. a frozen scorer
  and a broken invoice-parser), 5 subscriptions, 3 schedules (one five-strike-disabled), events,
  deliveries in every status, config events with real rationales + full prompts (diffs compute),
  2 open attention requests, memories. Fixed NOW so screenshots reproduce. Wire shapes mirror
  the web/src coercers — including the seconds (agent_*) vs milliseconds (config_events,
  memories) split; keep that discipline when adding rows.
- `stub-server.mjs` — serves `examples/web/dist` + answers every `/agent/*` read route from the
  fixtures, port 5181. Build first: `cd examples/web && yarn build`.
- `shoot-app3.mjs` — Playwright walkthrough: seeds a fake-JWT auth state, visits every view,
  clicks tabs/toggles, both themes. Run from `e2e/` (playwright is resolved from its
  node_modules): `node ../e2e/ux-stub/shoot-app3.mjs` or with cwd e2e:
  `node ux-stub/shoot-app3.mjs`. Screenshots land in `$SHOT_DIR` (default /tmp/ao-ux-shots).

Deliberately not wired into CI or playwright.config — it is a design-review instrument, not a
test suite. The doc-16/21 orchestrator re-runs it between waves to verify defects look fixed,
not just test-green.
