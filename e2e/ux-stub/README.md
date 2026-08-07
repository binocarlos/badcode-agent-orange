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

## Motion review (added for doc 21 Wave B)

Still screenshots cannot judge animation — a screenshot of a moving dot is one
frame, usually an empty one. Two additions close that gap:

- `scene-burst.mjs` + `SCENE=burst` on the stub — deliveries and events GROW
  across successive fetches (8→9→10→12→14), so the browser sees *arrivals*, not
  a still life. Motion caused by arrival is doc 21 §4.1 rule 1; the growth steps
  deliberately cross `MAX_CONCURRENT_PULSES` (3) so the "past three, show a
  static ×n count" rule is exercised for real, not only in unit tests.
- `capture-motion.mjs` — records video AND samples a film strip (N frames at a
  fixed interval) of the chart and the Desk, then does the whole run a second
  time under `reducedMotion: 'reduce'`. Compare the two: **a motion feature
  whose reduced pass loses information has failed the review's own rule** (§5,
  "nothing encoded only in motion").

```sh
cd examples/web && yarn build            # the stub serves the BUILT bundle
cd ../../e2e && SCENE=burst node ux-stub/stub-server.mjs &
SHOT_DIR=/tmp/ao-ux-shots node ux-stub/capture-motion.mjs
```

**The cheap objective check.** Frame hashes tell you whether anything moved at
all, without opening a file:

```sh
md5sum /tmp/ao-ux-shots/motion/strip-chart-motion-0*.png | awk '{print $1}' | sort -u | wc -l
```

`1` means every frame is identical — nothing animates. Measured **1 on the
pre-Wave-B build (2026-07-28)**: that is the baseline. After the motion work
lands this must be >1 for the animated pass, and the reduced pass should return
toward 1 while the static equivalents (chevrons, counts, flash) stay legible in
any single frame.
