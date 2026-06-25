import { writeFile, mkdir } from 'node:fs/promises';
import path from 'node:path';

export const env = {
  HOST_API_URL: (process.env.HOST_API_URL ?? 'http://localhost:80/api/v1') + '/agent-gw',
  SESSION_TOKEN: process.env.SESSION_TOKEN ?? '',
  SESSION_ID: process.env.SESSION_ID ?? '',
};

const DATA_DIR = '/workspace/data';

/**
 * Generate a filesystem-safe slug from a table spec for saving JSON.
 */
export function generateTableSlug(spec: Record<string, unknown>, type: 'table' | 'chart' = 'table'): string {
  const sanitize = (s: string) =>
    s.replace(/[^a-zA-Z0-9_-]/g, '_').replace(/_+/g, '_').replace(/^_|_$/g, '').slice(0, 30);
  const top = sanitize(String(spec.top || 'unknown').split(/[;*(]/)[0]);
  const side = sanitize(String(spec.side || 'unknown').split(/[;*(]/)[0]);
  return `${type}_${top}_x_${side}_${Date.now()}`;
}

/**
 * Save PlatinumData JSON to /workspace/data/ for Python processing.
 * Non-fatal: returns the saved path or null on failure.
 */
export async function savePlatinumData(platinumData: unknown, spec: Record<string, unknown>, type: 'table' | 'chart' = 'table', customName?: string): Promise<string | null> {
  try {
    await mkdir(DATA_DIR, { recursive: true });
    const slug = customName
      ? customName.replace(/[^a-zA-Z0-9_-]/g, '_').replace(/_+/g, '_').replace(/^_|_$/g, '').slice(0, 60)
      : generateTableSlug(spec, type);
    const filePath = path.join(DATA_DIR, `${slug}.json`);
    await writeFile(filePath, JSON.stringify(platinumData, null, 2));
    return filePath;
  } catch {
    return null;
  }
}


/**
 * Infer artifact type from file extension for auto-registration.
 */
export function inferArtifactType(ext: string, filePath?: string): string {
  if (['.png', '.jpg', '.jpeg', '.gif', '.webp', '.svg'].includes(ext)) return 'image';
  if (['.html', '.htm'].includes(ext)) {
    // Only treat HTML in dist/ as webapp; source HTML (e.g. /workspace/index.html)
    // has unresolved ES module imports and renders as broken unstyled text.
    if (filePath && /\bdist\//.test(filePath)) return 'webapp';
    return 'code';
  }
  if (['.csv', '.json', '.tsv'].includes(ext)) return 'data';
  if (['.js', '.ts', '.py', '.r', '.sql', '.sh', '.css'].includes(ext)) return 'code';
  return 'file';
}

/**
 * Truncate a full markdown table for batch mode.
 * Keeps title, metadata lines, header + separator + first N data rows,
 * and strips the analysis notes footer.
 */
export function truncateMarkdown(markdown: string, maxDataRows = 5): string {
  const lines = markdown.split('\n');
  const kept: string[] = [];
  let dataRowCount = 0;
  let inTable = false;
  let totalDataRows = 0;

  // First pass: count total data rows
  for (const line of lines) {
    if (line.startsWith('|') && !line.match(/^\|[\s-:|]+\|$/)) {
      // Not a separator row
      totalDataRows++;
    }
  }
  // Subtract 1 for the header row
  totalDataRows = Math.max(0, totalDataRows - 1);

  for (const line of lines) {
    // Keep title and metadata lines
    if (line.startsWith('#') || line.startsWith('**')) {
      kept.push(line);
      continue;
    }
    // Stop at analysis notes footer
    if (line.startsWith('---') && inTable) break;
    if (line.startsWith('> **') || line.startsWith('> _')) break;

    if (line.startsWith('|')) {
      inTable = true;
      // Separator row (e.g. |---|---|)
      if (line.match(/^\|[\s-:|]+\|$/)) {
        kept.push(line);
        continue;
      }
      // Header row (first table row before separator)
      if (dataRowCount === 0 && !kept.some(l => l.startsWith('|') && !l.match(/^\|[\s-:|]+\|$/))) {
        kept.push(line);
        continue;
      }
      // Data rows
      dataRowCount++;
      if (dataRowCount <= maxDataRows) {
        kept.push(line);
      }
    } else if (!inTable) {
      kept.push(line);
    }
  }

  const truncated = totalDataRows - maxDataRows;
  if (truncated > 0) {
    kept.push(`\n_(${truncated} more rows — full table rendered in chat)_`);
  }

  return kept.join('\n');
}

/**
 * Run tasks with a concurrency limit using Promise.allSettled semantics.
 */
export async function withConcurrencyLimit<T>(tasks: (() => Promise<T>)[], limit: number): Promise<PromiseSettledResult<T>[]> {
  const results: PromiseSettledResult<T>[] = [];
  const executing = new Set<Promise<void>>();

  for (const task of tasks) {
    const p = (async () => {
      try {
        const value = await task();
        results.push({ status: 'fulfilled', value });
      } catch (reason) {
        results.push({ status: 'rejected', reason });
      }
    })();
    executing.add(p);
    p.then(() => executing.delete(p));
    if (executing.size >= limit) {
      await Promise.race(executing);
    }
  }
  await Promise.all(executing);
  return results;
}

/**
 * Core helper: fetch a table spec (by path or inline spec) and run it.
 * Returns the content array for a single __render_table marker.
 * customer/job are derived from the JWT by the /agent-gw gateway and returned
 * in X-Agent-Customer/X-Agent-Job response headers.
 */
export async function fetchAndRunTable(args: {
  path?: string;
  spec?: string;
  title?: string;
  batch?: boolean;
  datasetName?: string;
}): Promise<{ type: 'text'; text: string }[]> {
  let specObj: unknown;

  // If path is provided, load the saved spec from the TOC
  if (args.path) {
    const specResponse = await fetch(
      `${env.HOST_API_URL}/tables/spec?path=${encodeURIComponent(args.path)}`,
      {
        headers: { 'Authorization': `Bearer ${env.SESSION_TOKEN}` },
      }
    );
    if (!specResponse.ok) {
      const errorText = await specResponse.text();
      throw new Error(`Failed to load table spec for path "${args.path}": ${errorText}`);
    }
    specObj = await specResponse.json();
  } else if (args.spec) {
    try {
      specObj = JSON.parse(args.spec);
    } catch {
      throw new Error(`Invalid JSON in spec parameter: ${args.spec}`);
    }
  } else {
    throw new Error('Either path (TOC table path) or spec (CarbonSpec JSON) is required.');
  }

  // Fetch both JSON (for rich rendering) and markdown (for LLM context) in parallel
  const [jsonResponse, markdownResponse] = await Promise.all([
    fetch(
      `${env.HOST_API_URL}/tables/run?format=json&frequency=false&column_percent=true`,
      {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${env.SESSION_TOKEN}`,
        },
        body: JSON.stringify(specObj),
      }
    ),
    fetch(
      `${env.HOST_API_URL}/tables/run?format=markdown`,
      {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${env.SESSION_TOKEN}`,
        },
        body: JSON.stringify(specObj),
      }
    ),
  ]);

  if (!jsonResponse.ok) {
    const errorText = await jsonResponse.text();
    throw new Error(`Table query failed (${jsonResponse.status}): ${errorText}`);
  }

  // Read the verified scope from gateway response headers
  const verifiedCustomer = jsonResponse.headers.get('X-Agent-Customer') ?? '';
  const verifiedJob = jsonResponse.headers.get('X-Agent-Job') ?? '';

  const platinumData = await jsonResponse.json();
  const markdownTable = markdownResponse.ok ? await markdownResponse.text() : '(markdown unavailable)';

  // Save PlatinumData JSON to disk for Python processing
  const savedPath = await savePlatinumData(platinumData, specObj as Record<string, unknown>, 'table', args.datasetName);
  const datasetName = savedPath ? path.basename(savedPath, '.json') : '';
  const saveNote = savedPath
    ? `\n\n---\nPlatinumData saved to: \`${savedPath}\`\nDataset name for data-loader: \`${datasetName}\` (use \`listDatasets()\` to discover all datasets)`
    : '';

  if (args.batch) {
    // Batch mode: reference dataPath instead of inline platinumData, truncate markdown
    const truncatedMarkdown = truncateMarkdown(markdownTable);
    const marker: Record<string, unknown> = {
      __render_table: true,
      title: args.title || '',
      markdown: truncatedMarkdown + saveNote,
      customer: verifiedCustomer,
      job: verifiedJob,
      spec: args.spec,
    };
    if (savedPath) {
      marker.dataPath = savedPath;
    } else {
      // Fallback: inline platinumData if disk save failed
      marker.platinumData = platinumData;
    }
    const content: { type: 'text'; text: string }[] = [{
      type: 'text' as const,
      text: JSON.stringify(marker),
    }];
    // Register artifact in batch mode too
    if (savedPath) {
      const relativePath = savedPath.replace(/^\/workspace\//, '');
      content.push({
        type: 'text' as const,
        text: JSON.stringify({
          __artifact_registered: true,
          file_path: relativePath,
          artifact_type: 'data',
          label: args.title ? `${args.title} (data)` : relativePath.split('/').pop() || '',
          description: 'Table data (PlatinumData JSON)',
        }),
      });
    }
    return content;
  }

  // Single mode: full platinumData inline, full markdown
  const content: { type: 'text'; text: string }[] = [{
    type: 'text' as const,
    text: JSON.stringify({
      __render_table: true,
      platinumData,
      title: args.title || '',
      markdown: markdownTable + saveNote,
      customer: verifiedCustomer,
      job: verifiedJob,
      spec: args.spec,
    }),
  }];

  // Register the saved JSON file as a downloadable artifact
  if (savedPath) {
    const relativePath = savedPath.replace(/^\/workspace\//, '');
    content.push({
      type: 'text' as const,
      text: JSON.stringify({
        __artifact_registered: true,
        file_path: relativePath,
        artifact_type: 'data',
        label: args.title ? `${args.title} (data)` : relativePath.split('/').pop() || '',
        description: 'Table data (PlatinumData JSON)',
      }),
    });
  }

  return content;
}
