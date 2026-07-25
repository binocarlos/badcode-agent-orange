// skills.ts — the in-session route behind the host's `skill_install` tool
// (spec docs/product/08-images-and-skills.md §14.2).
//
// agentd owns the skills catalogue and the tenancy boundary; this route owns
// the container. It takes a skill that agentd has already resolved — the
// markdown and the optional install script — writes the document where the
// harness will find it, runs the script, and reports both outcomes.
//
// It deliberately does NOT look a skill up: there is no database client in the
// image and there must not be one, because "which project's skills may this
// session read?" is exactly the question a session cannot be trusted to answer
// for itself (see mcpserver.go's header on the same point).
//
// A failed install is a 200 carrying `ok: false` and the whole story, not a
// 500: it is an answer about the container, and the caller must be able to read
// the script's output. Only a malformed REQUEST is a 4xx.

import { FastifyInstance } from 'fastify';
import {
  installSkill,
  InvalidSkillInstallError,
  type SkillInstallRequest,
} from '../tools/skill-install.js';

export async function skillRoutes(fastify: FastifyInstance): Promise<void> {
  fastify.post<{ Body: SkillInstallRequest }>('/skills/install', async (request, reply) => {
    const body = request.body;
    if (!body || typeof body !== 'object') {
      return reply.status(400).send({
        ok: false,
        name: '',
        path: '',
        bytes_written: 0,
        script: { ran: false, exit_code: 0, stdout: '', stderr: '', timed_out: false },
        error: 'a JSON body with {name, markdown, install_sh?} is required',
      });
    }

    try {
      const result = await installSkill(body);
      if (!result.ok) {
        fastify.log.warn(
          { skill: result.name, exitCode: result.script.exit_code, error: result.error },
          'skill install failed',
        );
      }
      return reply.send(result);
    } catch (err) {
      if (err instanceof InvalidSkillInstallError) {
        return reply.status(400).send({
          ok: false,
          name: typeof body.name === 'string' ? body.name : '',
          path: '',
          bytes_written: 0,
          script: { ran: false, exit_code: 0, stdout: '', stderr: '', timed_out: false },
          error: err.message,
        });
      }
      throw err;
    }
  });
}
