import { tool } from '@anthropic-ai/claude-agent-sdk';
import { z } from 'zod';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import type { ToolPlugin } from '../registry.js';

/**
 * view_image — feed a sandbox image to Claude's vision.
 * Generic builtin: useful for any agent that works with images in the workspace.
 */
export const viewImageTool: ToolPlugin = {
  name: 'view_image',
  sdkTool: tool(
    'view_image',
    'View an image file visually. Returns the image content so you can see and analyze it. Use for inspecting charts, rendered slides, screenshots, or any image in the workspace.',
    {
      file_path: z.string().min(1).describe('Absolute path to image file (PNG, JPG, GIF, WebP)'),
    },
    async (args) => {
      try {
        const data = await readFile(args.file_path);
        const ext = path.extname(args.file_path).toLowerCase();
        const mimeMap: Record<string, string> = {
          '.png': 'image/png',
          '.jpg': 'image/jpeg',
          '.jpeg': 'image/jpeg',
          '.gif': 'image/gif',
          '.webp': 'image/webp',
        };
        const mimeType = mimeMap[ext];
        if (!mimeType) {
          return {
            content: [{ type: 'text' as const, text: `Unsupported image format: ${ext}. Supported: PNG, JPG, GIF, WebP.` }],
            isError: true,
          };
        }
        return {
          content: [
            { type: 'image' as const, data: data.toString('base64'), mimeType },
            { type: 'text' as const, text: `Viewing: ${args.file_path} (${(data.length / 1024).toFixed(0)} KB)` },
          ],
        };
      } catch (error) {
        return {
          content: [{
            type: 'text' as const,
            text: `Failed to read image: ${error instanceof Error ? error.message : 'Unknown error'}`,
          }],
          isError: true,
        };
      }
    }
  ),
  // No marker: view_image returns an image content block directly, not a marker.
};
