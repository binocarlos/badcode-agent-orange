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
