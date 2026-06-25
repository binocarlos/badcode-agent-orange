import Fastify from 'fastify';
import cors from '@fastify/cors';
import { config } from './config.js';
import { healthRoutes } from './routes/health.js';
import { workspaceRoutes } from './routes/workspace.js';
import { sessionRoutes } from './routes/sessions.js';
import { toolRegistry } from './tools/registry-impl.js';
import { loadProductPlugins } from './tools/load-plugins.js';

// ---------------------------------------------------------------------------
// Harness registry — constructed in bootstrap.ts to avoid circular imports.
// Re-exported here so any existing importers keep working.
// ---------------------------------------------------------------------------

export { harnessRegistry, resolveHarness } from './harness/bootstrap.js';

// ---------------------------------------------------------------------------
// AsyncLocalStorage for per-session outbound proxy header.
//
// Defined in session-context.ts (separate module to avoid circular imports).
// Re-exported here so existing importers keep working.
// ---------------------------------------------------------------------------

export { sessionContext } from './session-context.js';
import { sessionContext } from './session-context.js';

// Inject X-Session-Id header on outbound model API calls so the host's model
// proxy can route to per-session mock scripts without affecting other sessions.
if (config.ANTHROPIC_BASE_URL) {
  const _originalFetch = globalThis.fetch;
  globalThis.fetch = function patchedFetch(input: string | URL | Request, init?: RequestInit) {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
    if (url.startsWith(config.ANTHROPIC_BASE_URL)) {
      // Prefer the per-turn session ID from AsyncLocalStorage; fall back to process-level
      // config.SESSION_ID for back-compat with single-session deployments.
      const store = sessionContext.getStore();
      const sessionId = store?.sessionId || config.SESSION_ID || '';
      if (sessionId) {
        const headers = new Headers(init?.headers);
        if (!headers.has('x-session-id')) {
          headers.set('x-session-id', sessionId);
        }
        return _originalFetch(input, { ...init, headers });
      }
    }
    return _originalFetch(input, init);
  } as typeof globalThis.fetch;
}

// Global error handlers -- must be registered early to catch all errors
process.on('uncaughtException', (error, origin) => {
  console.error('=== UNCAUGHT EXCEPTION ===');
  console.error('Origin:', origin);
  console.error('Error:', error);
  console.error('Stack:', error.stack);
  console.error('===========================');
  process.exit(1);
});

process.on('unhandledRejection', (reason, promise) => {
  console.error('=== UNHANDLED REJECTION ===');
  console.error('Promise:', promise);
  console.error('Reason:', reason);
  if (reason instanceof Error) {
    console.error('Stack:', reason.stack);
  }
  console.error('============================');
  process.exit(1);
});

process.on('exit', (code) => {
  console.log(`=== PROCESS EXIT with code ${code} ===`);
});

async function main() {
  const fastify = Fastify({
    bodyLimit: 100 * 1024 * 1024, // 100MB for large PPTX/DOCX with base64 overhead
    logger: {
      level: config.LOG_LEVEL,
      transport: config.NODE_ENV === 'development'
        ? { target: 'pino-pretty', options: { colorize: true } }
        : undefined,
    },
  });

  // Register CORS -- the host runner is the only caller, but allow all
  // origins in dev for easier debugging
  await fastify.register(cors, {
    origin: true,
  });

  // Register routes
  await fastify.register(healthRoutes);
  await fastify.register(sessionRoutes);
  await fastify.register(workspaceRoutes);

  // Global error handler
  fastify.setErrorHandler((error, _request, reply) => {
    fastify.log.error(error);
    reply.status(500).send({
      success: false,
      error: {
        code: 'INTERNAL_ERROR',
        message: config.NODE_ENV === 'development' ? error.message : 'Internal server error',
      },
    });
  });

  // Load product tool plugins (if a host image populated PRODUCT_PLUGINS_DIR).
  if (config.PRODUCT_PLUGINS_DIR) {
    const n = await loadProductPlugins(config.PRODUCT_PLUGINS_DIR, toolRegistry);
    fastify.log.info(`Loaded ${n} product tool plugin(s) from ${config.PRODUCT_PLUGINS_DIR}`);
  }

  // Start server
  try {
    await fastify.listen({ port: config.PORT, host: config.HOST });
    fastify.log.info(`Agent sandbox listening on http://${config.HOST}:${config.PORT}`);
    fastify.log.info(`Session ID: ${config.SESSION_ID || '(multi-session mode)'}`);
    fastify.log.info(`ANTHROPIC_BASE_URL: ${config.ANTHROPIC_BASE_URL}`);
    fastify.log.info(`HOST_API_URL: ${config.HOST_API_URL}`);
    fastify.log.info(`Model: ${config.DEFAULT_MODEL}`);
  } catch (error) {
    fastify.log.error(error);
    process.exit(1);
  }

  // Graceful shutdown
  const signals: NodeJS.Signals[] = ['SIGINT', 'SIGTERM', 'SIGHUP'];
  for (const signal of signals) {
    process.on(signal, async () => {
      console.log(`=== RECEIVED SIGNAL: ${signal} ===`);
      fastify.log.info(`Received ${signal}, shutting down...`);
      await fastify.close();
      process.exit(0);
    });
  }
}

main();
