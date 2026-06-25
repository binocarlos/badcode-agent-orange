import { FastifyInstance } from 'fastify';
import { config } from '../config.js';

export async function healthRoutes(fastify: FastifyInstance): Promise<void> {
  /**
   * GET /health
   * Basic health check. Returns session identity for the host runner
   * to verify the sandbox is running and belongs to the expected session.
   */
  fastify.get('/health', async () => {
    return {
      status: 'ok',
      sessionId: config.SESSION_ID,
      timestamp: new Date().toISOString(),
      service: 'agent-sandbox',
    };
  });
}
