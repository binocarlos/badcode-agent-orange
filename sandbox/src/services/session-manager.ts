// SessionManager: holds N concurrent sessions keyed by session ID.
// Each session has its own harness instance + conversation + per-turn AbortController.
// One active turn per session; cross-session turns run fully in parallel.
// See agent-library/docs/07-in-image-agent.md (multi-session correctness section).

import type { Harness } from '../harness/harness.js';
import { resolveHarness } from '../harness/bootstrap.js';
import { streamService } from './stream-service.js';

// ---------------------------------------------------------------------------
// Typed errors that the control server converts to HTTP responses
// ---------------------------------------------------------------------------

export interface HarnessErrorBody {
  status: number;
  body: Record<string, unknown>;
}

export class UnknownHarnessError extends Error {
  readonly errorCode = 'UNKNOWN_HARNESS' as const;
  constructor(public readonly http: HarnessErrorBody) {
    super(`Unknown harness: ${JSON.stringify(http.body)}`);
  }
}

export class HarnessCredentialsMissingError extends Error {
  readonly errorCode = 'HARNESS_CREDENTIALS_MISSING' as const;
  constructor(public readonly http: HarnessErrorBody) {
    super(`Harness credentials missing: ${JSON.stringify(http.body)}`);
  }
}

// ---------------------------------------------------------------------------
// Session record
// ---------------------------------------------------------------------------

interface TurnRecord {
  abort: AbortController;
}

interface SessionRecord {
  harness: Harness;
  harnessName: string;
  turns: Map<string, TurnRecord>;
  createdAt: number;
  lastActivity: number;
}

// ---------------------------------------------------------------------------
// SessionManager
// ---------------------------------------------------------------------------

export interface CreateSessionOptions {
  /** Harness name; defaults to 'claude-agent-sdk'. */
  harness?: string;
  model?: string;
  maxTurns?: number;
}

export class SessionManager {
  private readonly sessions = new Map<string, SessionRecord>();

  /**
   * Create a session. Idempotent if the session already exists with the same harness.
   * Throws UnknownHarnessError or HarnessCredentialsMissingError on harness validation failure.
   */
  create(sessionId: string, opts: CreateSessionOptions = {}): void {
    const harnessName = opts.harness || 'claude-agent-sdk';

    // Idempotent: if session already exists with the same harness, do nothing
    const existing = this.sessions.get(sessionId);
    if (existing) {
      if (existing.harnessName === harnessName) {
        return; // already created with matching harness — no-op
      }
      // Different harness requested — treat as error (session already exists with different harness)
      // Per spec: "Idempotent if the session already exists with the same harness"
      // A different harness means it's a new session config — destroy and recreate
      this.destroy(sessionId);
    }

    // Validate harness via the AG-2 resolveHarness seam
    const result = resolveHarness(harnessName);
    if ('errorCode' in result) {
      if (result.errorCode === 'UNKNOWN_HARNESS') {
        throw new UnknownHarnessError({ status: result.status, body: result.body });
      } else {
        throw new HarnessCredentialsMissingError({ status: result.status, body: result.body });
      }
    }

    const harness = result.descriptor.create(sessionId);
    this.sessions.set(sessionId, {
      harness,
      harnessName,
      turns: new Map(),
      createdAt: Date.now(),
      lastActivity: Date.now(),
    });
  }

  get(sessionId: string): SessionRecord | undefined {
    return this.sessions.get(sessionId);
  }

  has(sessionId: string): boolean {
    return this.sessions.has(sessionId);
  }

  /**
   * Destroy a session: abort all its turns, dispose the harness, free stream buffers.
   */
  destroy(sessionId: string): void {
    const sess = this.sessions.get(sessionId);
    if (!sess) return;

    // Abort all pending turns
    for (const [, turn] of sess.turns) {
      turn.abort.abort();
    }
    sess.turns.clear();

    // Gracefully dispose the harness (if it supports it)
    if (sess.harness.dispose) {
      sess.harness.dispose().catch(() => { /* ignore dispose errors */ });
    }

    // Free all stream buffers for this session
    streamService.closeSession(sessionId);

    this.sessions.delete(sessionId);
  }

  /**
   * Register a new turn for a session. If a prior turn is active, it is superseded
   * (aborted) before the new one is registered. Returns the new AbortController.
   *
   * ONE ACTIVE TURN PER SESSION — a new turn supersedes/aborts the prior turn.
   */
  startTurn(sessionId: string, queryId: string): AbortController {
    const sess = this.sessions.get(sessionId);
    if (!sess) {
      throw new Error(`Session ${sessionId} does not exist`);
    }

    // Supersede any active turns for this session
    for (const [existingQueryId, turn] of sess.turns) {
      if (existingQueryId !== queryId) {
        turn.abort.abort();
        sess.turns.delete(existingQueryId);
      }
    }

    const abort = new AbortController();
    sess.turns.set(queryId, { abort });
    sess.lastActivity = Date.now();
    return abort;
  }

  /**
   * Remove a turn from the session's turn map (called when the turn completes).
   */
  endTurn(sessionId: string, queryId: string): void {
    const sess = this.sessions.get(sessionId);
    if (!sess) return;
    sess.turns.delete(queryId);
    sess.lastActivity = Date.now();
  }

  /**
   * Abort one turn (by queryId) or all turns of the session (if queryId omitted).
   */
  cancel(sessionId: string, queryId?: string): void {
    const sess = this.sessions.get(sessionId);
    if (!sess) return;

    if (queryId) {
      const turn = sess.turns.get(queryId);
      if (turn) {
        turn.abort.abort();
        sess.turns.delete(queryId);
      }
    } else {
      for (const [, turn] of sess.turns) {
        turn.abort.abort();
      }
      sess.turns.clear();
    }
    sess.lastActivity = Date.now();
  }
}

// Singleton
export const sessionManager = new SessionManager();
