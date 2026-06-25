import { tool } from '@anthropic-ai/claude-agent-sdk';
import { z } from 'zod';
import { randomUUID } from 'node:crypto';
import type { ToolPlugin, MarkerSpec } from './types.js';
import { env } from './shared.js';

const createDashboardMarker: MarkerSpec = {
  key: '__dashboard_created',
  event: 'dashboard_created',
  toEvent: (p) => ({
    dashboardId: p.dashboardId,
    customer: p.customer,
    config: p.config,
    viewUrl: p.viewUrl,
  }),
  toModelText: (p) =>
    `Created dashboard${p.name ? ` "${p.name}"` : ''}.`,
};

export const createDashboardPlugin: ToolPlugin = {
  name: 'create_dashboard',
  marker: createDashboardMarker,
  sdkTool: tool(
    'create_dashboard',
    `Create a shareable dashboard. Build a DashboardConfig with pages containing rows and cells.

For tables and charts, use widget_type "table_graph" with table_graph_data containing the CarbonSpec:
- Table: {"widget_type":"table_graph","table_graph_data":{"job":"jobName","view_mode":"table","title":"My Table","spec":{"top":"Age","side":"Gender","filter":"Region(1)","useFilter":true,"useWeight":false}}}
- Chart: {"widget_type":"table_graph","table_graph_data":{"job":"jobName","view_mode":"graph","chart_type":"bar","data_type":"colPct","title":"My Chart","spec":{"top":"Age","side":"Gender"}}}

For text headings, use widget_type "text" with text_data.content (supports markdown).
For images/webapps from artifacts, use widget_type "agent_artifacts" with agent_artifact_data (session_id auto-injected).

The spec uses the same CarbonSpec format as render_table/render_chart. Do NOT include customerName/jobName in the spec.
Multiple cells in one row appear side by side.`,
    {
      customer: z.string().min(1).describe('Customer name (Azure storage account)'),
      name: z.string().min(1).describe('Dashboard name'),
      description: z.string().optional().describe('Dashboard description'),
      config: z.string().min(1).describe('DashboardConfig JSON string with pages array. Each page has id, name, layout with rows/cells.'),
    },
    async (args) => {
      try {
        let configObj: Record<string, unknown>;
        try {
          configObj = JSON.parse(args.config as string);
        } catch {
          return {
            content: [{ type: 'text' as const, text: `Invalid JSON in config parameter: ${args.config}` }],
            isError: true,
          };
        }

        configObj.name = args.name;
        if (args.description) configObj.description = args.description;

        // Auto-inject session_id into all agent_artifact_data blocks and generate page IDs
        const pages = configObj.pages as Array<Record<string, unknown>> | undefined;
        if (pages) {
          for (const page of pages) {
            if (!page.id) page.id = randomUUID();
            const layout = page.layout as Record<string, unknown> | undefined;
            if (layout?.rows) {
              for (const row of layout.rows as Array<Record<string, unknown>>) {
                if (row.cells) {
                  for (const cell of row.cells as Array<Record<string, unknown>>) {
                    const artifactData = cell.agent_artifact_data as Record<string, unknown> | undefined;
                    if (artifactData) {
                      artifactData.session_id = env.SESSION_ID;
                    }
                  }
                }
              }
            }
          }
        }

        // Also inject into top-level layout if present
        const topLayout = configObj.layout as Record<string, unknown> | undefined;
        if (topLayout?.rows) {
          for (const row of topLayout.rows as Array<Record<string, unknown>>) {
            if (row.cells) {
              for (const cell of row.cells as Array<Record<string, unknown>>) {
                const artifactData = cell.agent_artifact_data as Record<string, unknown> | undefined;
                if (artifactData) {
                  artifactData.session_id = env.SESSION_ID;
                }
              }
            }
          }
        }

        const response = await fetch(
          `${env.HOST_API_URL}/dashboards/${encodeURIComponent(args.customer as string)}`,
          {
            method: 'POST',
            headers: {
              'Content-Type': 'application/json',
              'Authorization': `Bearer ${env.SESSION_TOKEN}`,
            },
            body: JSON.stringify({ config: configObj }),
          }
        );

        if (!response.ok) {
          const errorText = await response.text();
          return {
            content: [{ type: 'text' as const, text: `Dashboard creation failed (${response.status}): ${errorText}` }],
            isError: true,
          };
        }

        const created = await response.json() as Record<string, unknown>;
        const dashboardId = created.id as string;
        const viewUrl = `/dashboards/${encodeURIComponent(args.customer as string)}/${dashboardId}/view`;

        return {
          content: [{
            type: 'text' as const,
            text: JSON.stringify({
              __dashboard_created: true,
              dashboardId,
              customer: args.customer,
              name: args.name,
              config: configObj,
              viewUrl,
            }),
          }],
        };
      } catch (error) {
        return {
          content: [{ type: 'text' as const, text: `Failed to create dashboard: ${error instanceof Error ? error.message : 'Unknown error'}` }],
          isError: true,
        };
      }
    },
  ),
};

export default createDashboardPlugin;
