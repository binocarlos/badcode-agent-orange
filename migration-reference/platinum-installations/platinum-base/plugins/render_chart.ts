import { tool } from '@anthropic-ai/claude-agent-sdk';
import { z } from 'zod';
import path from 'node:path';
import type { ToolPlugin, MarkerSpec } from './types.js';
import { env, savePlatinumData } from './shared.js';

const renderChartMarker: MarkerSpec = {
  key: '__render_chart',
  event: 'chart_rendered',
  toEvent: (p) => ({
    platinumData: p.platinumData,
    chartType: p.chartType,
    title: p.title,
    customer: p.customer,
    job: p.job,
    spec: p.spec,
  }),
  toModelText: (p) =>
    `Rendered chart${p.title ? ` "${p.title}"` : ''} in the chat.`,
};

export const renderChartPlugin: ToolPlugin = {
  name: 'render_chart',
  marker: renderChartMarker,
  sdkTool: tool(
    'render_chart',
    'Run a cross-tabulation and render the results as a chart in the chat. Provide EITHER a TOC path (to chart an existing saved table) OR a spec JSON (for ad-hoc queries). If this tool returns an error, inform the user about the issue rather than retrying repeatedly.',
    {
      path: z.string().optional().describe('TOC path of an existing saved table. When provided, the saved spec is loaded automatically.'),
      spec: z.string().optional().describe('CarbonSpec JSON for ad-hoc queries. Only needed when path is not provided.'),
      chartType: z.enum(['bar', 'line', 'pie', 'stacked']).optional().describe('Chart type (default: bar)'),
      title: z.string().optional().describe('Optional title for the chart'),
    },
    async (args) => {
      try {
        let specObj: unknown;

        if (args.path) {
          try {
            const specResponse = await fetch(
              `${env.HOST_API_URL}/tables/spec?path=${encodeURIComponent(args.path as string)}`,
              { headers: { 'Authorization': `Bearer ${env.SESSION_TOKEN}` } }
            );
            if (!specResponse.ok) {
              const errorText = await specResponse.text();
              return {
                content: [{ type: 'text' as const, text: `Failed to load table spec for path "${args.path}": ${errorText}` }],
                isError: true,
              };
            }
            specObj = await specResponse.json();
          } catch (error) {
            return {
              content: [{ type: 'text' as const, text: `Failed to load table spec: ${error instanceof Error ? error.message : 'Unknown error'}` }],
              isError: true,
            };
          }
        } else if (args.spec) {
          try {
            specObj = JSON.parse(args.spec as string);
          } catch {
            return {
              content: [{ type: 'text' as const, text: `Invalid JSON in spec parameter: ${args.spec}` }],
              isError: true,
            };
          }
        } else {
          return {
            content: [{ type: 'text' as const, text: 'Either path (TOC table path) or spec (CarbonSpec JSON) is required.' }],
            isError: true,
          };
        }

        const [jsonResponse, markdownResponse] = await Promise.all([
          fetch(
            `${env.HOST_API_URL}/tables/run?format=json`,
            {
              method: 'POST',
              headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${env.SESSION_TOKEN}` },
              body: JSON.stringify(specObj),
            }
          ),
          fetch(
            `${env.HOST_API_URL}/tables/run?format=markdown`,
            {
              method: 'POST',
              headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${env.SESSION_TOKEN}` },
              body: JSON.stringify(specObj),
            }
          ),
        ]);

        if (!jsonResponse.ok) {
          const errorText = await jsonResponse.text();
          return {
            content: [{ type: 'text' as const, text: `Chart query failed (${jsonResponse.status}): ${errorText}` }],
            isError: true,
          };
        }

        // Read the verified scope from gateway response headers
        const verifiedCustomer = jsonResponse.headers.get('X-Agent-Customer') ?? '';
        const verifiedJob = jsonResponse.headers.get('X-Agent-Job') ?? '';

        const platinumData = await jsonResponse.json();
        const markdownTable = markdownResponse.ok ? await markdownResponse.text() : '(markdown unavailable)';

        const savedPath = await savePlatinumData(platinumData, specObj as Record<string, unknown>, 'chart');
        const chartDatasetName = savedPath ? path.basename(savedPath, '.json') : '';
        const saveNote = savedPath
          ? `\n\n---\nPlatinumData saved to: \`${savedPath}\`\nDataset name for data-loader: \`${chartDatasetName}\` (use \`listDatasets()\` to discover all datasets)`
          : '';

        const chartContent: { type: 'text'; text: string }[] = [{
          type: 'text' as const,
          text: JSON.stringify({
            __render_chart: true,
            platinumData,
            chartType: args.chartType || 'bar',
            title: args.title || '',
            markdown: markdownTable + saveNote,
            customer: verifiedCustomer,
            job: verifiedJob,
            spec: args.spec,
          }),
        }];

        if (savedPath) {
          const relativePath = savedPath.replace(/^\/workspace\//, '');
          chartContent.push({
            type: 'text' as const,
            text: JSON.stringify({
              __artifact_registered: true,
              file_path: relativePath,
              artifact_type: 'data',
              label: args.title ? `${args.title} (data)` : relativePath.split('/').pop() || '',
              description: 'Chart data (PlatinumData JSON)',
            }),
          });
        }

        return { content: chartContent };
      } catch (error) {
        return {
          content: [{ type: 'text' as const, text: `Failed to render chart: ${error instanceof Error ? error.message : 'Unknown error'}` }],
          isError: true,
        };
      }
    },
  ),
};

export default renderChartPlugin;
