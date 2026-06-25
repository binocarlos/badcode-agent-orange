import { tool } from '@anthropic-ai/claude-agent-sdk';
import { z } from 'zod';
import type { ToolPlugin } from './types.js';
import { env } from './shared.js';

export const searchSkillsPlugin: ToolPlugin = {
  name: 'search_skills',
  sdkTool: tool(
    'search_skills',
    'List or search reusable skills installable into this session. Answers "list the skills" / "find a skill that does X". Returns name, description, visibility, and whether the skill needs a build step.',
    { query: z.string().optional().describe('Optional keyword to filter by name/description') },
    async (args) => {
      const q = (args.query as string | undefined)?.trim();
      const url = `${env.HOST_API_URL}/skills${q ? `?q=${encodeURIComponent(q)}` : ''}`;
      try {
        const resp = await fetch(url, { headers: { Authorization: `Bearer ${env.SESSION_TOKEN}` } });
        if (!resp.ok) return { content: [{ type: 'text' as const, text: `Failed to search skills (${resp.status}): ${await resp.text()}` }], isError: true };
        const body = await resp.json() as { skills: Array<{ name: string; description: string; visibility: string; requires_build: boolean }> };
        const lines = body.skills.length
          ? body.skills.map(s => `- ${s.name} (${s.visibility}${s.requires_build ? ', needs build' : ''}): ${s.description}`).join('\n')
          : '(no matching skills)';
        return { content: [{ type: 'text' as const, text: `Available skills:\n${lines}\n\nTo add one, call install_skill with its name.` }] };
      } catch (e) {
        return { content: [{ type: 'text' as const, text: `Failed to search skills: ${e instanceof Error ? e.message : 'unknown error'}` }], isError: true };
      }
    },
  ),
};

export default searchSkillsPlugin;
