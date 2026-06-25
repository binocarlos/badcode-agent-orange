import { readdir, stat } from 'node:fs/promises';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import type { ToolPlugin } from './registry.js';

interface Registrar {
  register(p: ToolPlugin): void;
}

function isToolPlugin(v: unknown): v is ToolPlugin {
  return (
    typeof v === 'object' &&
    v !== null &&
    typeof (v as { name?: unknown }).name === 'string' &&
    (v as { sdkTool?: unknown }).sdkTool != null
  );
}

function extractPlugins(mod: Record<string, unknown>): ToolPlugin[] {
  const out: ToolPlugin[] = [];
  const candidates = [mod.default, mod.plugin, mod.plugins, mod.toolPlugins];
  for (const c of candidates) {
    if (Array.isArray(c)) {
      for (const item of c) if (isToolPlugin(item)) out.push(item);
    } else if (isToolPlugin(c)) {
      out.push(c);
    }
  }
  return out;
}

/**
 * Scans `dir`, dynamically imports each module file, extracts ToolPlugins, and
 * registers them. Plugins are deduped by `name` within a single call, so a
 * directory containing both a barrel (`index.ts`) and the individual plugin
 * files will not register the same plugin twice. Returns the number of unique
 * plugins registered.
 */
export async function loadProductPlugins(
  dir: string,
  registry: Registrar,
): Promise<number> {
  if (!dir) return 0;
  let entries: string[];
  try {
    const s = await stat(dir);
    if (!s.isDirectory()) return 0;
    entries = await readdir(dir);
  } catch {
    return 0;
  }

  const moduleFiles = entries
    .filter((f) => /\.(mjs|js|ts)$/.test(f) && !/\.(test|spec)\./.test(f))
    .sort();

  let count = 0;
  const seen = new Set<string>();
  for (const file of moduleFiles) {
    const full = path.join(dir, file);
    try {
      const mod = (await import(pathToFileURL(full).href)) as Record<string, unknown>;
      for (const plugin of extractPlugins(mod)) {
        if (seen.has(plugin.name)) continue;
        seen.add(plugin.name);
        registry.register(plugin);
        count++;
      }
    } catch (err) {
      console.error(
        `[load-plugins] failed to load ${file}: ${err instanceof Error ? err.message : String(err)}`,
      );
    }
  }
  return count;
}
