import { AsyncLocalStorage } from 'node:async_hooks';

/**
 * Ambient session context for the current async execution.
 *
 * Each turn is run inside `sessionContext.run({ sessionId }, () => ...)` so that
 * the patched `fetch` (in index.ts) and StreamService.key() can resolve the
 * current session without threading sessionId through every internal call.
 */
export const sessionContext = new AsyncLocalStorage<{ sessionId: string }>();
