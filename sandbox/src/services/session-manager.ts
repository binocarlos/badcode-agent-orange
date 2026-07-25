// SessionManager: holds N concurrent sessions keyed by session ID.
// Each session has its own harness instance + conversation + per-turn AbortController.
// One active turn per session; cross-session turns run fully in parallel.
// See agent-library/docs/07-in-image-agent.md (multi-session correctness section).

import type { Harness } from '../harness/harness.js';
import { resolveHarness } from '../harness/bootstrap.js';
import { streamService } from './stream-service.js';
import { validateSessionMCPServers } from '../tools/registry.js';
import type { SessionMCPServers } from '../tools/registry.js';

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
  /**
   * Session MCP server config, as supplied by the Runner on session create
   * (docs/product/01-session-config.md §4). Stored UNRESOLVED — `${VAR}`
   * references are resolved by the harness at MCP-process spawn time.
   * Safe to hold and display: values name env vars, never secrets (§4.4).
   */
  mcpServers: SessionMCPServers;
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
  /**
   * MCP servers for this session (wire field `mcp_servers`). Merged over the
   * in-image tool registry for every turn; validated here so a bad config is a
   * session-create failure rather than a mid-turn surprise.
   */
  mcpServers?: SessionMCPServers;
}

/** Thrown when session-supplied MCP config fails validation on session create. */
export class InvalidMCPServersError extends Error {
  readonly errorCode = 'INVALID_MCP_SERVERS' as const;
  constructor(public readonly http: HarnessErrorBody) {
    super(`Invalid mcp_servers: ${JSON.stringify(http.body)}`);
  }
}

export class SessionManager {
  private readonly sessions = new Map<string, SessionRecord>();

  /**
   * Create a session. Idempotent if the session already exists with the same harness.
   * Throws UnknownHarnessError or HarnessCredentialsMissingError on harness validation failure.
   */
  create(sessionId: string, opts: CreateSessionOptions = {}): void {
    const harnessName = opts.harness || 'claude-agent-sdk';

    // Validate MCP config before touching session state: a bad config must fail
    // the create call, never silently reach a turn.
    const mcpServers = opts.mcpServers ?? {};
    try {
      validateSessionMCPServers(mcpServers);
    } catch (err) {
      throw new InvalidMCPServersError({
        status: 400,
        body: {
          code: 'INVALID_MCP_SERVERS',
          message: err instanceof Error ? err.message : String(err),
        },
      });
    }

    // Idempotent: if session already exists with the same harness, do nothing
    const existing = this.sessions.get(sessionId);
    if (existing) {
      if (existing.harnessName === harnessName) {
        // Re-supply MCP config on resume / re-provision (§4.5): the config is
        // session config, not filesystem state, so the Runner sends it again.
        existing.mcpServers = mcpServers;
        return; // already created with matching harness — no-op otherwise
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
      mcpServers,
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
