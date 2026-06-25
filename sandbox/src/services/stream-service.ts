import { FastifyReply } from 'fastify';
import { SSEEvent, SSEEventType } from '../types/index.js';
import { sessionContext } from '../session-context.js';

/**
 * Manages Server-Sent Events (SSE) streams for agent queries.
 * Each query can have multiple listeners (though typically one).
 * Buffers events when no streams are connected to prevent event loss.
 *
 * Streams and buffers are keyed by the COMPOSITE key `${sessionId}:${queryId}`
 * so that queries from different sessions can't collide. `closeSession` cascades
 * all buffers belonging to a session.
 *
 * Back-compat: all public methods that previously took only `queryId` now also
 * accept a `sessionId` parameter (defaulting to '' for legacy callers).
 */
export class StreamService {
  private streams: Map<string, Set<FastifyReply>> = new Map();
  private eventBuffer: Map<string, SSEEvent[]> = new Map();
  private readonly MAX_BUFFER_SIZE = 2000;

  /** Buffers for coalescing tool_input_delta events (150ms debounce) */
  private inputDeltaBuffers: Map<string, { buffer: string; timer: ReturnType<typeof setTimeout> | null }> = new Map();

  // ---------------------------------------------------------------------------
  // Key helpers
  // ---------------------------------------------------------------------------

  /**
   * Build the composite stream key from a sessionId and queryId.
   * When sessionId is empty, falls back to the ambient AsyncLocalStorage session
   * (set by sessionContext.run() in flat back-compat routes). When neither is
   * present (legacy single-session callers with no header/env), uses queryId alone
   * to keep back-compat with the existing key format.
   */
  private key(sessionId: string, queryId: string): string {
    const resolved = sessionId || sessionContext.getStore()?.sessionId || '';
    return resolved ? `${resolved}:${queryId}` : queryId;
  }

  // ---------------------------------------------------------------------------
  // Stream registration
  // ---------------------------------------------------------------------------

  /**
   * Register a new SSE stream for a query.
   * Replays any buffered events to the new stream.
   */
  addStream(queryId: string, reply: FastifyReply, sessionId = ''): void {
    const k = this.key(sessionId, queryId);
    const connectTime = Date.now();
    console.log(`[SSE-DEBUG][StreamService] addStream called for key=${k} at ${connectTime}`);

    if (!this.streams.has(k)) {
      this.streams.set(k, new Set());
    }
    this.streams.get(k)!.add(reply);

    // Replay any buffered events to the newly connected stream
    const buffered = this.eventBuffer.get(k);
    if (buffered && buffered.length > 0) {
      console.log(`[SSE-DEBUG][StreamService] REPLAYING ${buffered.length} buffered events for key=${k}`);
      for (let i = 0; i < buffered.length; i++) {
        const event = buffered[i];
        console.log(`[SSE-DEBUG][StreamService] REPLAY event ${i + 1}/${buffered.length}: type=${event.type} ts=${event.timestamp}`);
        const eventString = `event: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`;
        try {
          reply.raw.write(eventString);
        } catch {
          // Ignore write errors during replay
        }
      }
      console.log(`[SSE-DEBUG][StreamService] REPLAY complete for key=${k}`);
      // Clear buffer after replay
      this.eventBuffer.delete(k);
    } else {
      console.log(`[SSE-DEBUG][StreamService] No buffered events for key=${k} — stream connected before any events`);
    }

    // Clean up when connection closes
    reply.raw.on('close', () => {
      this.removeStream(queryId, reply, sessionId);
    });
  }

  /**
   * Remove a stream when connection closes.
   */
  removeStream(queryId: string, reply: FastifyReply, sessionId = ''): void {
    const k = this.key(sessionId, queryId);
    const queryStreams = this.streams.get(k);
    if (queryStreams) {
      queryStreams.delete(reply);
      if (queryStreams.size === 0) {
        this.streams.delete(k);
      }
    }
  }

  /**
   * Check if a query has active streams.
   */
  hasActiveStreams(queryId: string, sessionId = ''): boolean {
    const k = this.key(sessionId, queryId);
    const queryStreams = this.streams.get(k);
    return queryStreams !== undefined && queryStreams.size > 0;
  }

  // ---------------------------------------------------------------------------
  // Buffer
  // ---------------------------------------------------------------------------

  /**
   * Buffer an event for later replay when no streams are connected.
   */
  private bufferEvent(k: string, event: SSEEvent): void {
    if (!this.eventBuffer.has(k)) {
      this.eventBuffer.set(k, []);
    }
    const buffer = this.eventBuffer.get(k)!;
    buffer.push(event);
    // Keep buffer size limited
    if (buffer.length > this.MAX_BUFFER_SIZE) {
      buffer.shift();
    }
  }

  // ---------------------------------------------------------------------------
  // Delta coalescing
  // ---------------------------------------------------------------------------

  /**
   * Flush a buffered tool_input_delta as a single coalesced event.
   */
  private flushInputDelta(k: string): void {
    const entry = this.inputDeltaBuffers.get(k);
    if (!entry || !entry.buffer) return;
    if (entry.timer) {
      clearTimeout(entry.timer);
      entry.timer = null;
    }
    const coalesced = entry.buffer;
    entry.buffer = '';
    this.sendEventDirect(k, 'tool_input_delta', { partialJson: coalesced });
  }

  // ---------------------------------------------------------------------------
  // Core send
  // ---------------------------------------------------------------------------

  /** Send using composite key (session-scoped). */
  sendEventByKey(k: string, type: SSEEventType | string, data: unknown): void {
    // Coalesce tool_input_delta events
    if (type === 'tool_input_delta') {
      const partialJson = (data as Record<string, unknown>).partialJson as string || '';
      let entry = this.inputDeltaBuffers.get(k);
      if (!entry) {
        entry = { buffer: '', timer: null };
        this.inputDeltaBuffers.set(k, entry);
      }
      entry.buffer += partialJson;
      if (!entry.timer) {
        entry.timer = setTimeout(() => {
          entry!.timer = null;
          this.flushInputDelta(k);
        }, 150);
      }
      return;
    }

    // Flush pending input deltas on boundary events
    if (type === 'tool_use_start' || type === 'tool_use_end' || type === 'message_end') {
      this.flushInputDelta(k);
    }

    this.sendEventDirect(k, type, data);
  }

  sendEvent(queryId: string, type: SSEEventType | string, data: unknown, sessionId = ''): void {
    this.sendEventByKey(this.key(sessionId, queryId), type, data);
  }

  private sendEventDirect(k: string, type: SSEEventType | string, data: unknown): void {
    const event: SSEEvent = {
      type,
      data,
      timestamp: new Date().toISOString(),
    };

    const queryStreams = this.streams.get(k);

    // If no streams connected, buffer the event for later replay
    if (!queryStreams || queryStreams.size === 0) {
      const bufferSize = (this.eventBuffer.get(k)?.length || 0) + 1;
      console.log(`[SSE-DEBUG][StreamService] BUFFERING event type=${type} for key=${k} (buffer will be ${bufferSize}) at ${Date.now()}`);
      this.bufferEvent(k, event);
      return;
    }

    console.log(`[SSE-DEBUG][StreamService] SENDING event type=${type} to ${queryStreams.size} stream(s) for key=${k} at ${Date.now()}`);
    const eventString = `event: ${type}\ndata: ${JSON.stringify(event)}\n\n`;

    for (const reply of queryStreams) {
      try {
        reply.raw.write(eventString);
      } catch {
        // Connection might be closed, remove it using the full key
        queryStreams.delete(reply);
        if (queryStreams.size === 0) {
          this.streams.delete(k);
        }
      }
    }
  }

  // ---------------------------------------------------------------------------
  // Typed send helpers (all take queryId + optional sessionId for back-compat)
  // ---------------------------------------------------------------------------

  sendContentDelta(queryId: string, messageId: string, delta: string, sessionId = ''): void {
    this.sendEvent(queryId, 'content_delta', { messageId, delta }, sessionId);
  }

  sendThinkingDelta(queryId: string, messageId: string, delta: string, sessionId = ''): void {
    this.sendEvent(queryId, 'thinking_delta', { messageId, delta }, sessionId);
  }

  sendToolUseStart(
    queryId: string,
    toolCallId: string,
    toolName: string,
    input: Record<string, unknown>,
    sessionId = '',
  ): void {
    this.sendEvent(queryId, 'tool_use_start', { toolCallId, toolName, input }, sessionId);
  }

  sendToolUseEnd(queryId: string, toolCallId: string, output: string, isError?: boolean, sessionId = ''): void {
    this.sendEvent(queryId, 'tool_use_end', { toolCallId, output, isError: isError || false }, sessionId);
  }

  endQuery(
    queryId: string,
    status: string,
    result?: string,
    totalCostUsd?: number,
    usage?: { inputTokens: number; outputTokens: number },
    model?: string,
    sessionId = '',
  ): void {
    const k = this.key(sessionId, queryId);
    this.flushInputDelta(k);
    this.inputDeltaBuffers.delete(k);

    this.sendEventByKey(k, 'query_complete', {
      queryId,
      status,
      result,
      totalCostUsd,
      usage,
      model,
    });

    // Clear any buffered events for this query
    this.eventBuffer.delete(k);
  }

  closeQuery(queryId: string, sessionId = ''): void {
    const k = this.key(sessionId, queryId);
    this.flushInputDelta(k);
    this.inputDeltaBuffers.delete(k);

    const queryStreams = this.streams.get(k);
    if (queryStreams) {
      for (const reply of queryStreams) {
        try {
          reply.raw.end();
        } catch {
          // Ignore errors when closing
        }
      }
      this.streams.delete(k);
    }
    this.eventBuffer.delete(k);
  }

  /**
   * Evict all buffers, streams, and coalescing state for a session.
   * Called by SessionManager.destroy(sessionId).
   */
  closeSession(sessionId: string): void {
    const prefix = `${sessionId}:`;
    for (const k of Array.from(this.streams.keys())) {
      if (k.startsWith(prefix)) {
        const queryStreams = this.streams.get(k);
        if (queryStreams) {
          for (const reply of queryStreams) {
            try { reply.raw.end(); } catch { /* ignore */ }
          }
        }
        this.streams.delete(k);
      }
    }
    for (const k of Array.from(this.eventBuffer.keys())) {
      if (k.startsWith(prefix)) {
        this.eventBuffer.delete(k);
      }
    }
    for (const k of Array.from(this.inputDeltaBuffers.keys())) {
      if (k.startsWith(prefix)) {
        const entry = this.inputDeltaBuffers.get(k);
        if (entry?.timer) clearTimeout(entry.timer);
        this.inputDeltaBuffers.delete(k);
      }
    }
  }

  sendError(queryId: string, code: string, message: string, sessionId = ''): void {
    this.sendEvent(queryId, 'error', { code, message }, sessionId);
  }

  sendToolProgress(
    queryId: string,
    toolUseId: string,
    toolName: string,
    elapsedSeconds: number,
    parentToolUseId: string | null,
    sessionId = '',
  ): void {
    this.sendEvent(queryId, 'tool_progress', {
      toolUseId,
      toolName,
      elapsedSeconds,
      parentToolUseId,
    }, sessionId);
  }

  sendSystemStatus(
    queryId: string,
    status: 'init' | 'compacting' | 'ready' | 'auth',
    details?: Record<string, unknown>,
    sessionId = '',
  ): void {
    this.sendEvent(queryId, 'system_status', { status, details }, sessionId);
  }

  sendSessionInfo(
    queryId: string,
    info: {
      tools: string[];
      model: string;
      mcpServers: { name: string; status: string }[];
    },
    sessionId = '',
  ): void {
    this.sendEvent(queryId, 'session_info', info, sessionId);
  }

  sendHookEvent(
    queryId: string,
    hookType: 'pre_tool' | 'post_tool' | 'tool_failure' | 'notification' | 'hook_response',
    payload: Record<string, unknown>,
    sessionId = '',
  ): void {
    this.sendEvent(queryId, 'hook_event', { hookType, payload }, sessionId);
  }

  sendActivityUpdate(
    queryId: string,
    phase: string,
    label: string,
    details?: Record<string, unknown>,
    sessionId = '',
  ): void {
    this.sendEvent(queryId, 'activity_update', { phase, label, ...details }, sessionId);
  }

  sendToolInputDelta(queryId: string, partialJson: string, sessionId = ''): void {
    this.sendEvent(queryId, 'tool_input_delta', { partialJson }, sessionId);
  }

  sendSubagentEvent(
    queryId: string,
    event: 'start' | 'stop',
    agentId: string,
    agentType?: string,
    result?: string,
    sessionId = '',
  ): void {
    this.sendEvent(queryId, 'subagent_event', {
      event,
      agentId,
      agentType,
      result,
    }, sessionId);
  }
}

// Singleton instance
export const streamService = new StreamService();
