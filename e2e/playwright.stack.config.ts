import { defineConfig } from '@playwright/test'

// Config for the standalone-stack e2e: the full docker-compose stack (web on
// :8080) is stood up by run-stack-e2e.sh before playwright runs. Separate from
// playwright.config.ts, which drives the older Vite+mock-server harness with
// its own global setup/teardown.
//
// Two kinds of spec run here (see e2e/README.md):
//   stack.spec.ts        the browser journey — login → session → streamed reply
//   features/*.spec.ts   product-layer features through the HTTP API
const BASE_URL = process.env.STACK_BASE_URL || 'http://localhost:8080'

export default defineConfig({
  testDir: '.',
  // A run that leaks stack state fails, even if every assertion passed: setup
  // refuses to start on a host with no room left, teardown reports (and for
  // schedules, clears) whatever this run left behind. Both exist because
  // afterEach cannot help with the runs that die before afterEach — which are
  // exactly the runs that leak. See helpers/occupancy.ts.
  globalSetup: './stack-setup.ts',
  globalTeardown: './stack-teardown.ts',
  testMatch: ['stack.spec.ts', 'features/*.spec.ts'],
  timeout: 240_000,
  expect: { timeout: 30_000 },
  // Parallel across FILES, serial within one. `fullyParallel: false` is what
  // makes that distinction: with it, workers > 1 hands each worker a whole
  // spec file, and the `describe.configure({ mode: 'serial' })` blocks several
  // specs rely on keep working untouched. `fullyParallel: true` would break
  // them, so the two settings are a pair — do not raise one without the other
  // in mind.
  //
  // Why it is safe at this granularity: every test takes a fresh, run-scoped
  // project of its own (`newProjectClient`), so there is no shared product
  // state to race over. The shared resources are the host port pool (100, and
  // three workers touch a handful) and DinD.
  //
  // Why it is worth it: almost all of this suite's wall clock is WAITING — for
  // a container to boot, for a cron minute to arrive, for a snapshot to commit —
  // not computing. Serial execution left three of four cores idle.
  //
  // The port-pool spec is the one thing that must not share a host, since it
  // works by filling the pool. It already runs alone, in its own `--port-pool 3`
  // invocation, so this does not endanger it.
  fullyParallel: false,
  workers: Number(process.env.STACK_E2E_WORKERS) || 3,
  retries: 0,
  reporter: [['list'], ['html', { outputFolder: 'playwright-report-stack', open: 'never' }]],
  use: {
    baseURL: BASE_URL,
    screenshot: 'only-on-failure',
    trace: 'retain-on-failure',
  },
})
