import { tool } from '@anthropic-ai/claude-agent-sdk';
import { z } from 'zod';
import type { ToolPlugin, MarkerSpec } from './types.js';
import { fetchAndRunTable, withConcurrencyLimit } from './shared.js';

const renderTableMarker: MarkerSpec = {
  key: '__render_table',
  event: 'table_rendered',
  toEvent: (p) => ({
    platinumData: p.platinumData,
    title: p.title,
    customer: p.customer,
    job: p.job,
    spec: p.spec,
    datasetName: p.datasetName,
  }),
  toModelText: (p) =>
    `Rendered table${p.title ? ` "${p.title}"` : ''} in the chat for the user.`,
};

export const renderTablePlugin: ToolPlugin = {
  name: 'render_table',
  marker: renderTableMarker,
  sdkTool: tool(
    'render_table',
    'Run a cross-tabulation and render the results as a rich interactive table in the chat. Provide EITHER a TOC path (to run an existing saved table) OR a spec JSON (to run an ad-hoc query). Using path is preferred. If this tool returns an error, inform the user rather than retrying repeatedly.',
    {
      path: z.string().optional().describe('TOC path of an existing saved table.'),
      spec: z.string().optional().describe('CarbonSpec JSON for ad-hoc queries.'),
      title: z.string().optional().describe('Optional title for the table'),
      datasetName: z.string().optional().describe('Short name for the saved dataset file in /workspace/data/.'),
    },
    async (args) => {
      try {
        const content = await fetchAndRunTable({
          path: args.path as string | undefined,
          spec: args.spec as string | undefined,
          title: args.title as string | undefined,
          datasetName: args.datasetName as string | undefined,
        });
        return { content };
      } catch (error) {
        return {
          content: [{ type: 'text' as const, text: `Failed to render table: ${error instanceof Error ? error.message : 'Unknown error'}` }],
          isError: true,
        };
      }
    },
  ),
};

export const renderTablesPlugin: ToolPlugin = {
  name: 'render_tables',
  marker: renderTableMarker,
  sdkTool: tool(
    'render_tables',
    'Run multiple cross-tabulations in parallel and render each as a rich interactive table in the chat. Results are returned individually — failed tables include an error message without blocking others.',
    {
      specs: z.array(z.object({
        path: z.string().optional(),
        spec: z.string().optional(),
        title: z.string().optional(),
        datasetName: z.string().optional(),
      })).min(1).max(20).describe('Array of table specs to run in parallel'),
    },
    async (args) => {
      try {
        const specs = args.specs as Array<{ path?: string; spec?: string; title?: string; datasetName?: string }>;
        const useBatch = specs.length >= 5;
        const tasks = specs.map((s) => () =>
          fetchAndRunTable({ ...s, batch: useBatch })
        );
        const results = await withConcurrencyLimit(tasks, 5);
        const content = results.flatMap((r, i) =>
          r.status === 'fulfilled'
            ? r.value
            : [{ type: 'text' as const, text: `Failed to render table "${specs[i].title || specs[i].path || `spec ${i + 1}`}": ${String(r.reason)}` }],
        );
        return { content };
      } catch (error) {
        return {
          content: [{ type: 'text' as const, text: `Failed to render tables: ${error instanceof Error ? error.message : 'Unknown error'}` }],
          isError: true,
        };
      }
    },
  ),
};

export default [renderTablePlugin, renderTablesPlugin];
