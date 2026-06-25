import { tool } from '@anthropic-ai/claude-agent-sdk';
import { z } from 'zod';
import type { ToolPlugin } from '../registry.js';

/**
 * ask_user — pose a structured question to the user with selectable options.
 * Generic builtin: every agent product needs this.
 * Returns a __ask_user marker that the PostToolUse hook intercepts and emits
 * as an 'ask_user' SSE event.
 */
export const askUserTool: ToolPlugin = {
  name: 'ask_user',
  sdkTool: tool(
    'ask_user',
    'Pose a structured question to the user with selectable options. After calling this, STOP and wait for the user to respond.',
    {
      question: z.string().min(1).describe('The question to ask'),
      options: z.array(z.object({
        label: z.string().min(1).describe('Short button label'),
        value: z.string().min(1).describe('Value sent back when selected'),
        description: z.string().optional().describe('Longer description below the label'),
        advance: z.boolean().optional().describe('If true, clicking triggers phase advancement instead of sending a message'),
      })).min(2).max(10).describe('Selectable options'),
      allow_freetext: z.boolean().optional().default(false).describe('Show free-text input alongside options'),
      context: z.string().optional().describe('Context shown above the options'),
    },
    async (args) => ({
      content: [{
        type: 'text' as const,
        text: JSON.stringify({
          __ask_user: true,
          question: args.question,
          options: args.options,
          allow_freetext: args.allow_freetext ?? false,
          context: args.context || '',
        }),
      }],
    })
  ),
  marker: {
    key: '__ask_user',
    event: 'ask_user',
    toEvent: (payload: Record<string, unknown>) => ({
      question: payload.question,
      options: payload.options,
      allowFreetext: payload.allow_freetext || false,
      context: payload.context || '',
    }),
    toModelText: (payload: Record<string, unknown>) =>
      JSON.stringify({
        __ask_user: true,
        question: payload.question,
        options: payload.options,
        allow_freetext: payload.allow_freetext ?? false,
        context: payload.context || '',
      }),
  },
};
