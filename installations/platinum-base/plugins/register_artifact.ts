import { tool } from '@anthropic-ai/claude-agent-sdk';
import { z } from 'zod';
import type { ToolPlugin } from './types.js';

export const registerArtifactPlugin: ToolPlugin = {
  name: 'register_artifact',
  sdkTool: tool(
    'register_artifact',
    'Register an important output file as a downloadable artifact. Use this to explicitly register files (e.g. built webapp dist/index.html) with a specific artifact_type. The auto-classifier handles most files, but for webapp artifacts you MUST use this tool because HTML is auto-classified as "code" not "webapp". Set publish_to_files=true to also publish the artifact to the user\'s Files area.',
    {
      file_path: z.string().min(1).describe('Path relative to /workspace/ (e.g. "my-viz/dist/index.html")'),
      artifact_type: z.enum(['webapp', 'file', 'code', 'image', 'data']).describe('Artifact type. Use "webapp" for built web applications with sub-resources.'),
      label: z.string().optional().describe('Human-readable label for the artifact'),
      description: z.string().optional().describe('Brief description of the artifact'),
      publish_to_files: z.boolean().optional().describe('If true, artifact will be published to the user\'s Files area when extracted'),
    },
    async (args) => ({
      content: [{
        type: 'text' as const,
        text: JSON.stringify({
          __artifact_registered: true,
          file_path: args.file_path,
          artifact_type: args.artifact_type,
          label: args.label || '',
          description: args.description || '',
          publish_to_files: args.publish_to_files || false,
        }),
      }],
    }),
  ),
};

export default registerArtifactPlugin;
