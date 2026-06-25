// Bootstrap module: constructs the singleton HarnessRegistry and exports
// resolveHarness so routes can import from here without touching index.ts
// (importing index.ts from routes would cause a circular dependency because
// index.ts builds the Fastify server with heavy side effects).
//
// index.ts re-exports from this module so all existing importers keep working.

import { HarnessRegistry, DEFAULT_HARNESS, checkCredentials } from './registry.js';
import { claudeAgentSdkDescriptor } from './claude-agent-sdk.js';

// ---------------------------------------------------------------------------
// Singleton registry — registered once at module load.
// ---------------------------------------------------------------------------

export const harnessRegistry = new HarnessRegistry();
harnessRegistry.register(claudeAgentSdkDescriptor);

/**
 * Resolve a harness from the registry and validate its credentials.
 * Returns the descriptor on success, or an error descriptor for HTTP responses.
 *
 * Usage pattern (at session/query entry):
 *   const result = resolveHarness(req.harness);
 *   if ('errorCode' in result) { reply.status(result.status).send(result.body); return; }
 *   const harness = result.descriptor.create(sessionId);
 */
export function resolveHarness(
  harnessName: string | undefined,
  env: NodeJS.ProcessEnv = process.env,
):
  | { descriptor: ReturnType<typeof harnessRegistry.get> & object }
  | { errorCode: 'UNKNOWN_HARNESS' | 'HARNESS_CREDENTIALS_MISSING'; status: number; body: Record<string, unknown> } {
  const name = harnessName || DEFAULT_HARNESS;

  if (!harnessRegistry.has(name)) {
    return {
      errorCode: 'UNKNOWN_HARNESS',
      status: 400,
      body: { code: 'UNKNOWN_HARNESS', supported: harnessRegistry.names() },
    };
  }

  const desc = harnessRegistry.get(name)!;
  const credCheck = checkCredentials(desc, env);
  if (!credCheck.ok) {
    return {
      errorCode: 'HARNESS_CREDENTIALS_MISSING',
      status: 424,
      body: {
        code: 'HARNESS_CREDENTIALS_MISSING',
        message: desc.credentials.describe(),
        missing: credCheck.missing,
      },
    };
  }

  return { descriptor: desc };
}
