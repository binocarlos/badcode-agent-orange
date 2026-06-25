/**
 * data-loader.js — Auto-discovers all JSON files in /workspace/data/ at build time.
 *
 * Uses Vite's import.meta.glob to bundle every *.json file from the data directory.
 * No manual imports needed — just tabulate data with render_table and it's available here.
 *
 * Usage:
 *   import { listDatasets, getAllDatasets, getRecords, getMeta } from './lib/data-loader.js'
 *
 *   const names = listDatasets()           // ['age_x_gender', 'brand_awareness', ...]
 *   const all = getAllDatasets()            // { age_x_gender: {...}, brand_awareness: {...} }
 *   const records = getRecords('age_x_gender')  // [{row, col, value}, ...]
 */

import { toRecords, toMatrix, getMeta, getBaseSizes, getColumnLabels, getRowLabels } from './platinum-data.js'

// Eagerly import all JSON files from the data directory at build time
const dataModules = import.meta.glob('../../data/*.json', { eager: true })

const datasets = {}
for (const [path, module] of Object.entries(dataModules)) {
  const filename = path.split('/').pop().replace('.json', '')
  datasets[filename] = module.default
}

/**
 * List all available dataset names.
 * @returns {string[]}
 */
export function listDatasets() {
  return Object.keys(datasets)
}

/**
 * Get a single dataset by name (filename without .json extension).
 * @param {string} name
 * @returns {object|undefined} PlatinumData object
 */
export function getDataset(name) {
  return datasets[name]
}

/**
 * Get all datasets as { name: platinumDataObj }.
 * @returns {object}
 */
export function getAllDatasets() {
  return { ...datasets }
}

/**
 * Get flat records for a dataset (for D3, Plot).
 * @param {string} name - Dataset name
 * @param {string} [metric='colpc'] - 'colpc', 'rowpc', or 'freq'
 * @returns {Array<{row: string, col: string, value: number}>}
 */
export function getRecords(name, metric = 'colpc') {
  const data = datasets[name]
  return data ? toRecords(data, metric) : []
}

/**
 * Get matrix format for a dataset (for Chart.js, heatmaps).
 * @param {string} name - Dataset name
 * @param {string} [metric='colpc'] - 'colpc', 'rowpc', or 'freq'
 * @returns {{ rows: string[], columns: string[], values: number[][] }}
 */
export function getMatrix(name, metric = 'colpc') {
  const data = datasets[name]
  return data ? toMatrix(data, metric) : { rows: [], columns: [], values: [] }
}

/**
 * Get metadata for a dataset.
 * @param {string} name - Dataset name
 * @returns {{ top: string, side: string, filter: string, weight: string, name: string }}
 */
export function getDatasetMeta(name) {
  const data = datasets[name]
  return data ? getMeta(data) : { top: '', side: '', filter: '', weight: '', name: '' }
}

// Re-export low-level helpers for direct use
export { toRecords, toMatrix, getMeta, getBaseSizes, getColumnLabels, getRowLabels }
