// HarnessRegistry: maps harness names to their descriptors (credentials + factory).
// See agent-library/docs/12-harness.md.

import type { HarnessCredentialSpec, Harness } from './harness.js';

/** Describes one registered harness: its credentials and a per-session factory. */
export interface HarnessDescriptor {
  name: string;
  credentials: HarnessCredentialSpec;
  /** Produce a fresh, per-session Harness instance. */
  create(sessionId: string): Harness;
}

/**
 * Registry of available harness descriptors. The control server uses this to
 * validate a session's harness choice and to instantiate the harness at session start.
 */
export class HarnessRegistry {
  private readonly descriptors: Map<string, HarnessDescriptor> = new Map();

  register(d: HarnessDescriptor): void {
    this.descriptors.set(d.name, d);
  }

  has(name: string): boolean {
    return this.descriptors.has(name);
  }

  get(name: string): HarnessDescriptor | undefined {
    return this.descriptors.get(name);
  }

  names(): string[] {
    return Array.from(this.descriptors.keys());
  }
}

/** The default harness name — used when the session request omits harness. */
export const DEFAULT_HARNESS = 'claude-agent-sdk';

/**
 * Validate the credential spec for a descriptor against process.env.
 * Returns { ok: true } if all required env vars are set (and, when the spec
 * declares an anyOfEnv group, at least one of that group is set), or
 * { ok: false, missing: string[] } listing the absent vars. An unsatisfied
 * anyOfEnv group appears as one synthetic entry: "one of: A, B, C".
 */
export function checkCredentials(
  desc: HarnessDescriptor,
  env: NodeJS.ProcessEnv = process.env,
): { ok: true } | { ok: false; missing: string[] } {
  const missing = desc.credentials.requiredEnv.filter(k => !env[k]);
  const anyOf = desc.credentials.anyOfEnv ?? [];
  if (anyOf.length > 0 && !anyOf.some(k => env[k])) {
    missing.push(`one of: ${anyOf.join(', ')}`);
  }
  if (missing.length > 0) {
    return { ok: false, missing };
  }
  return { ok: true };
}
