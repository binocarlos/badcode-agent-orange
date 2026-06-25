/**
 * data-loader.js — runtime loader for PlatinumData JSON (NO build step).
 *
 * Unlike the Vite data-loader (which used import.meta.glob at build time), this
 * fetches the JSON that render_table saved to /workspace/data/ at runtime. It
 * works identically when previewed via screenshot_url and when served back to the
 * user in the webapp iframe, because the data directory is resolved relative to
 * this module.
 *
 * This module lives at  webapp/lib/data-loader.js , and the data lives at the
 * session root in  data/ , i.e. two levels up from lib/.
 *
 * Usage (note: these are async — await them):
 *   import { getRecords, getMatrix, getDatasetMeta } from './lib/data-loader.js'
 *   const records = await getRecords('awareness')   // [{row, col, value}], values 0-100
 *   const matrix  = await getMatrix('awareness')    // {rows, columns, values}
 *
 * Pass the dataset name = the `datasetName` you gave render_table (filename
 * without .json). You always know your dataset names because you created them.
 */

import {
  toRecords, toMatrix, getMeta, getBaseSizes, getColumnLabels, getRowLabels,
} from './platinum-data.js'

// Resolve <session root>/data/ relative to this module (webapp/lib/ -> ../../data/).
const DATA_DIR = new URL('../../data/', import.meta.url)

const _cache = new Map()

/**
 * Fetch and cache a raw PlatinumData object by dataset name.
 * @param {string} name - dataset name (filename without .json)
 * @returns {Promise<object>}
 */
export async function loadDataset(name) {
  if (_cache.has(name)) return _cache.get(name)
  const res = await fetch(new URL(`${encodeURIComponent(name)}.json`, DATA_DIR))
  if (!res.ok) throw new Error(`dataset "${name}" not found (HTTP ${res.status})`)
  const data = await res.json()
  _cache.set(name, data)
  return data
}

/** Flat records for D3/Plot. @returns {Promise<Array<{row,col,value}>>} */
export async function getRecords(name, metric = 'colpc') {
  return toRecords(await loadDataset(name), metric)
}

/** Matrix for Chart.js/heatmaps. @returns {Promise<{rows,columns,values}>} */
export async function getMatrix(name, metric = 'colpc') {
  return toMatrix(await loadDataset(name), metric)
}

/** Dataset metadata (top/side/filter/weight/name). @returns {Promise<object>} */
export async function getDatasetMeta(name) {
  return getMeta(await loadDataset(name))
}

/**
 * List available dataset names. Best-effort: reads an optional data/_manifest.json
 * (a JSON array of names). If absent, returns []. You normally pass explicit names
 * to getRecords/getMatrix instead of relying on discovery.
 * @returns {Promise<string[]>}
 */
export async function listDatasets() {
  try {
    const res = await fetch(new URL('_manifest.json', DATA_DIR))
    if (res.ok) return await res.json()
  } catch { /* no manifest — fall through */ }
  return []
}

// Low-level helpers re-exported for direct use on a raw PlatinumData object.
export { toRecords, toMatrix, getMeta, getBaseSizes, getColumnLabels, getRowLabels }
