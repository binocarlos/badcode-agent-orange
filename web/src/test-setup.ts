import { expect, afterEach } from 'vitest'
import * as matchers from '@testing-library/jest-dom/matchers'
expect.extend(matchers)

// Unmount React trees between tests so repeated render() calls in the same file
// don't leak DOM (otherwise findByText sees duplicates). Only meaningful in the
// jsdom environment — guarded so the node-env .ts tests skip it.
if (typeof document !== 'undefined') {
  afterEach(async () => {
    const { cleanup } = await import('@testing-library/react')
    cleanup()
  })
}
