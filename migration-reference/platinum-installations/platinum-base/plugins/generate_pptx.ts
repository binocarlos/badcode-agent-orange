import { tool } from '@anthropic-ai/claude-agent-sdk';
import { z } from 'zod';
import path from 'node:path';
import { execSync } from 'node:child_process';
import type { ToolPlugin } from './types.js';

export const generatePptxPlugin: ToolPlugin = {
  name: 'generate_pptx',
  sdkTool: tool(
    'generate_pptx',
    'Generate a branded PPTX report from a slide manifest and client template. Handles all chart formatting deterministically: reverse_order, legend hiding, number format, adaptive layout, insight positioning, empty data handling, gradient cycling. The agent builds the manifest (JSON array of slides with type, title, chart_type, data_file, insight, footer), then calls this tool. Do NOT write custom python-pptx scripts — use this tool instead.',
    {
      template: z.string().min(1).describe('Path to the client PPTX template (e.g. "/workspace/uploads/template.pptx")'),
      manifest: z.string().min(1).describe('Path to the slide manifest JSON file (e.g. "/workspace/slides/manifest.json")'),
      output: z.string().min(1).describe('Output PPTX path (e.g. "/workspace/report.pptx")'),
      data_dir: z.string().optional().describe('Directory containing data JSON files (default: same dir as manifest)'),
      colors: z.string().optional().describe('Comma-separated hex colors for chart series (e.g. "#99FFED,#89CDFF,#CA88FF")'),
      single_color: z.string().optional().describe('Hex color for single-series bar charts (default: first color)'),
      sidebar_width: z.number().optional().describe('Template sidebar width in inches (default: 1.5)'),
    },
    async (args) => {
      try {
        const cmd = [
          'python3', '/workspace/lib/pptx-tools/generate_pptx.py',
          '--template', args.template as string,
          '--manifest', args.manifest as string,
          '--output', args.output as string,
          '--validate', '--thumbnails', '-v',
        ];
        if (args.data_dir) cmd.push('--data-dir', args.data_dir as string);
        if (args.colors) cmd.push('--colors', args.colors as string);
        if (args.single_color) cmd.push('--single-color', args.single_color as string);
        if (args.sidebar_width) cmd.push('--sidebar-width', String(args.sidebar_width));

        const result = execSync(cmd.join(' '), {
          encoding: 'utf-8',
          timeout: 120000,
          cwd: '/workspace',
        });

        const relativePath = (args.output as string).replace(/^\/workspace\//, '');
        return {
          content: [
            { type: 'text' as const, text: result },
            {
              type: 'text' as const,
              text: JSON.stringify({
                __artifact_registered: true,
                file_path: relativePath,
                artifact_type: 'file',
                label: path.basename(args.output as string),
                description: 'Generated PPTX report',
              }),
            },
          ],
        };
      } catch (error) {
        const execErr = error as { stderr?: string; stdout?: string; message?: string };
        return {
          content: [{
            type: 'text' as const,
            text: `generate_pptx failed:\n${execErr.stdout || ''}\n${execErr.stderr || execErr.message || 'Unknown error'}`,
          }],
          isError: true,
        };
      }
    },
  ),
};

export default generatePptxPlugin;
