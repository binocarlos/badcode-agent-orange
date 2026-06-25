import { tool } from '@anthropic-ai/claude-agent-sdk';
import { z } from 'zod';
import { readFile } from 'node:fs/promises';
import { execSync } from 'node:child_process';
import type { ToolPlugin } from '../registry.js';

/**
 * screenshot_url — take a screenshot of a URL or local HTML file using headless Chromium.
 * Generic builtin: useful for any agent that builds web apps or visualizations.
 */
export const screenshotUrlTool: ToolPlugin = {
  name: 'screenshot_url',
  sdkTool: tool(
    'screenshot_url',
    'Take a screenshot of a URL or local HTML file using headless Chromium. Returns the screenshot as an image so you can visually inspect web apps, charts, or any HTML content you have built. Use this to catch visual bugs like "undefined" labels, layout issues, or styling problems before delivering to the user.',
    {
      url: z.string().min(1).describe('URL (http/https) or absolute path to a local HTML file (e.g. /workspace/dist/index.html)'),
      width: z.number().optional().default(1280).describe('Viewport width in pixels (default: 1280)'),
      height: z.number().optional().default(800).describe('Viewport height in pixels (default: 800)'),
      wait: z.number().optional().default(3000).describe('JS rendering budget in milliseconds (default: 3000)'),
    },
    async (args) => {
      try {
        const outputPath = `/tmp/screenshot_${Date.now()}.png`;
        const cmd = [
          'python3', '/workspace/lib/screenshot.py',
          args.url, outputPath,
          '--width', String(args.width || 1280),
          '--height', String(args.height || 800),
          '--wait', String(args.wait || 3000),
        ].join(' ');

        execSync(cmd, { timeout: 45000, encoding: 'utf-8', cwd: '/workspace' });

        const data = await readFile(outputPath);
        return {
          content: [
            { type: 'image' as const, data: data.toString('base64'), mimeType: 'image/png' },
            { type: 'text' as const, text: `Screenshot of ${args.url} (${(data.length / 1024).toFixed(0)} KB, ${args.width || 1280}x${args.height || 800})` },
          ],
        };
      } catch (error) {
        const execErr = error as { stderr?: string; message?: string };
        return {
          content: [{
            type: 'text' as const,
            text: `Failed to screenshot: ${execErr.stderr || execErr.message || 'Unknown error'}. Is Chromium installed in the sandbox?`,
          }],
          isError: true,
        };
      }
    }
  ),
  // No marker: screenshot_url returns image content directly, not a marker.
};
