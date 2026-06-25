// Session-scoped routes: POST /sessions, DELETE /sessions/:id,
// POST /sessions/:id/query-stream, GET /sessions/:id/stream/:queryId,
// POST /sessions/:id/cancel, POST /sessions/:id/load-conversation,
// POST /sessions/:id/reset-conversation
//
// Every response sets the X-Session-Id header.
// See agent-library/docs/07-in-image-agent.md §HTTP contract.

import { FastifyInstance } from 'fastify';
import { v4 as uuidv4 } from 'uuid';
import {
  sessionManager,
  UnknownHarnessError,
  HarnessCredentialsMissingError,
} from '../services/session-manager.js';
import { streamService } from '../services/stream-service.js';
import { sessionContext } from '../session-context.js';
import { config } from '../config.js';
import { QueryRequestSchema, QueryRequest } from '../types/index.js';
import { toolRegistry } from '../tools/registry-impl.js';
import { BoundHarnessEmitter } from '../harness/harness.js';

export async function sessionRoutes(fastify: FastifyInstance): Promise<void> {
  // ---------------------------------------------------------------------------
  // POST /sessions
  // Create a session: boot the named harness + credential pre-check.
  // Idempotent if it already exists with the same harness.
  // ---------------------------------------------------------------------------
  fastify.post<{
    Body: { sessionId: string; harness?: string; model?: string; maxTurns?: number };
  }>('/sessions', async (request, reply) => {
    const { sessionId, harness, model, maxTurns } = request.body || {};

    if (!sessionId) {
      reply.header('X-Session-Id', '');
      reply.status(400);
      return { success: false, error: { code: 'MISSING_SESSION_ID', message: 'sessionId is required' } };
    }

    reply.header('X-Session-Id', sessionId);

    try {
      sessionManager.create(sessionId, { harness, model, maxTurns });
      return { success: true, data: { sessionId } };
    } catch (err) {
      if (err instanceof UnknownHarnessError) {
        reply.status(err.http.status);
        return { success: false, error: err.http.body };
      }
      if (err instanceof HarnessCredentialsMissingError) {
        reply.status(err.http.status);
        return { success: false, error: err.http.body };
      }
      throw err;
    }
  });

  // ---------------------------------------------------------------------------
  // DELETE /sessions/:sessionId
  // Tear down a session: abort turns, dispose harness, free maps.
  // ---------------------------------------------------------------------------
  fastify.delete<{
    Params: { sessionId: string };
  }>('/sessions/:sessionId', async (request, reply) => {
    const { sessionId } = request.params;
    reply.header('X-Session-Id', sessionId);

    sessionManager.destroy(sessionId);
    return { success: true, data: { sessionId } };
  });

  // ---------------------------------------------------------------------------
  // POST /sessions/:sessionId/query-stream
  // Submit a turn AND stream its SSE in one response (no race window).
  // ---------------------------------------------------------------------------
  fastify.post<{
    Params: { sessionId: string };
    Body: QueryRequest;
  }>('/sessions/:sessionId/query-stream', async (request, reply) => {
    const { sessionId } = request.params;
    reply.header('X-Session-Id', sessionId);

    let body: QueryRequest;
    try {
      body = QueryRequestSchema.parse(request.body);
    } catch (error) {
      if (!reply.raw.headersSent) {
        reply.status(400);
        return {
          success: false,
          error: {
            code: 'INVALID_REQUEST',
            message: error instanceof Error ? error.message : 'Invalid request',
          },
        };
      }
      return;
    }

    // Ensure session exists; create with defaults if not
    if (!sessionManager.has(sessionId)) {
      try {
        sessionManager.create(sessionId, { harness: body.harness });
      } catch (err) {
        if (err instanceof UnknownHarnessError || err instanceof HarnessCredentialsMissingError) {
          const e = err as UnknownHarnessError | HarnessCredentialsMissingError;
          reply.status(e.http.status);
          return { success: false, error: e.http.body };
        }
        throw err;
      }
    }

    const sess = sessionManager.get(sessionId);
    if (!sess) {
      reply.status(404);
      return { success: false, error: { code: 'SESSION_NOT_FOUND', message: `Session ${sessionId} not found` } };
    }

    const queryId = uuidv4();

    // One active turn per session: start the turn (supersedes any prior)
    const abortController = sessionManager.startTurn(sessionId, queryId);

    fastify.log.info({
      sessionId,
      queryId,
      promptLength: body.prompt.length,
      model: body.model,
    }, 'Starting session-scoped query-stream');

    // Hijack response to keep the SSE connection open
    reply.hijack();
    reply.raw.writeHead(200, {
      'Content-Type': 'text/event-stream',
      'Cache-Control': 'no-cache',
      'Connection': 'keep-alive',
      'X-Accel-Buffering': 'no',
      'X-Session-Id': sessionId,
    });

    // Register stream BEFORE firing the turn — no race window
    streamService.addStream(queryId, reply, sessionId);

    // Send connected event with queryId so the caller can extract it
    const connectEvent = `event: connected\ndata: ${JSON.stringify({
      queryId,
      sessionId,
      timestamp: new Date().toISOString(),
    })}\n\n`;
    reply.raw.write(connectEvent);

    // Heartbeat
    const heartbeat = setInterval(() => {
      try {
        streamService.sendEvent(queryId, 'heartbeat', { ts: new Date().toISOString() }, sessionId);
      } catch {
        clearInterval(heartbeat);
      }
    }, 15000);

    reply.raw.on('close', () => {
      fastify.log.info({ sessionId, queryId }, 'session query-stream response closed');
      clearInterval(heartbeat);
    });

    // Resolve tools for this turn
    const resolved = toolRegistry.resolve(body.tools);

    // Build the emitter bound to (sessionId, queryId)
    const emit = new BoundHarnessEmitter(streamService, sessionId, queryId);

    // Run the harness turn inside the AsyncLocalStorage context so outbound
    // fetch calls are stamped with the correct x-session-id header.
    sessionContext.run({ sessionId }, () => {
      sess.harness.runTurn(body, {
        sessionId,
        queryId,
        signal: abortController.signal,
        emit,
        resolved,
        config,
      }).catch((error) => {
        const errorMessage = error instanceof Error ? error.message : String(error);
        fastify.log.error({ sessionId, queryId, errorMessage }, 'Error processing session turn');
      }).finally(() => {
        sessionManager.endTurn(sessionId, queryId);
      });
    });
  });

  // ---------------------------------------------------------------------------
  // GET /sessions/:sessionId/stream/:queryId
  // Attach to a query's stream; replays the in-image buffer then live.
  // ---------------------------------------------------------------------------
  fastify.get<{
    Params: { sessionId: string; queryId: string };
  }>('/sessions/:sessionId/stream/:queryId', async (request, reply) => {
    const { sessionId, queryId } = request.params;
    reply.header('X-Session-Id', sessionId);

    fastify.log.info({ sessionId, queryId }, 'Session stream connection opened');

    reply.hijack();
    reply.raw.writeHead(200, {
      'Content-Type': 'text/event-stream',
      'Cache-Control': 'no-cache',
      'Connection': 'keep-alive',
      'X-Accel-Buffering': 'no',
      'X-Session-Id': sessionId,
    });

    streamService.addStream(queryId, reply, sessionId);

    const connectEvent = `event: connected\ndata: ${JSON.stringify({
      queryId,
      sessionId,
      timestamp: new Date().toISOString(),
    })}\n\n`;
    reply.raw.write(connectEvent);

    const heartbeat = setInterval(() => {
      try {
        streamService.sendEvent(queryId, 'heartbeat', { ts: new Date().toISOString() }, sessionId);
      } catch {
        clearInterval(heartbeat);
      }
    }, 15000);

    reply.raw.on('close', () => {
      fastify.log.info({ sessionId, queryId }, 'Session stream response closed');
      clearInterval(heartbeat);
    });
  });

  // ---------------------------------------------------------------------------
  // POST /sessions/:sessionId/cancel
  // Abort a turn (or all turns of the session).
  // ---------------------------------------------------------------------------
  fastify.post<{
    Params: { sessionId: string };
    Body: { queryId?: string };
  }>('/sessions/:sessionId/cancel', async (request, reply) => {
    const { sessionId } = request.params;
    const { queryId } = request.body || {};
    reply.header('X-Session-Id', sessionId);

    sessionManager.cancel(sessionId, queryId);
    return { success: true, data: { cancelled: true } };
  });

  // ---------------------------------------------------------------------------
  // POST /sessions/:sessionId/load-conversation
  // Load persisted conversation history. Called by host runner on resume.
  // ---------------------------------------------------------------------------
  fastify.post<{
    Params: { sessionId: string };
    Body: { messages: Array<{ role: 'user' | 'assistant'; content: string }> };
  }>('/sessions/:sessionId/load-conversation', async (request, reply) => {
    const { sessionId } = request.params;
    const { messages } = request.body || {};
    reply.header('X-Session-Id', sessionId);

    const sess = sessionManager.get(sessionId);
    if (!sess) {
      reply.status(404);
      return { success: false, error: { code: 'SESSION_NOT_FOUND', message: `Session ${sessionId} not found` } };
    }

    if (!Array.isArray(messages)) {
      reply.status(400);
      return { success: false, error: { code: 'INVALID_REQUEST', message: 'messages must be an array' } };
    }

    sess.harness.loadConversation(messages);
    return { success: true, data: { loaded: messages.length } };
  });

  // ---------------------------------------------------------------------------
  // POST /sessions/:sessionId/reset-conversation
  // Clear conversation history (phase transitions).
  // ---------------------------------------------------------------------------
  fastify.post<{
    Params: { sessionId: string };
  }>('/sessions/:sessionId/reset-conversation', async (request, reply) => {
    const { sessionId } = request.params;
    reply.header('X-Session-Id', sessionId);

    const sess = sessionManager.get(sessionId);
    if (!sess) {
      reply.status(404);
      return { success: false, error: { code: 'SESSION_NOT_FOUND', message: `Session ${sessionId} not found` } };
    }

    sess.harness.resetConversation();
    return { success: true, data: { reset: true } };
  });
}
