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
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [['list'], ['html', { outputFolder: 'playwright-report-stack', open: 'never' }]],
  use: {
    baseURL: BASE_URL,
    screenshot: 'only-on-failure',
    trace: 'retain-on-failure',
  },
})
