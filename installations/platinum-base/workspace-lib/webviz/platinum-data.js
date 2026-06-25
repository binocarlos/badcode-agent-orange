/**
 * platinum-data.js — Helper for processing PlatinumData JSON in web visualizations.
 *
 * PlatinumData is the structured output from cross-tabulation queries.
 * This module converts it to flat records or matrix format for charting libraries.
 *
 * Usage:
 *   import rawData from '../data/my-table.json'
 *   import { toRecords, toMatrix, getMeta } from './lib/platinum-data.js'
 *
 *   const records = toRecords(rawData)            // [{row, col, value}, ...]
 *   const matrix = toMatrix(rawData, 'freq')      // {rows, columns, values}
 *   const meta = getMeta(rawData)                  // {top, side, filter, ...}
 */

const SKIP_TYPES = new Set(['Base', 'Spacer', 'Empty'])

/**
 * Sanitize a label, returning fallback if null/undefined/empty/"undefined"/"null".
 * @param {*} label
 * @param {string} fallback
 * @returns {string}
 */
function sanitizeLabel(label, fallback) {
  if (!label || typeof label !== 'string') return fallback
  const trimmed = label.trim()
  if (!trimmed || trimmed === 'undefined' || trimmed === 'null') return fallback
  return trimmed
}

/**
 * Build filtered top labels and mask from PlatinumData.
 * @param {object} data - PlatinumData object
 * @returns {{ mask: boolean[], labels: string[] }}
 */
function topInfo(data) {
  const vecs = data?.top?.vecs || []
  const mask = vecs.map(v => !SKIP_TYPES.has(v.type))
  const labels = vecs.filter((_, i) => mask[i]).map((v, i) => sanitizeLabel(v.label, `Column ${i + 1}`))
  return { mask, labels }
}

/**
 * Convert PlatinumData to flat records for D3/Plot.
 *
 * @param {object} data - PlatinumData object (parsed JSON)
 * @param {string} [metric='colpc'] - 'colpc', 'rowpc', or 'freq'
 * @param {boolean} [includeBase=false] - include Base/Spacer rows
 * @returns {Array<{row: string, col: string, value: number}>}
 */
export function toRecords(data, metric = 'colpc', includeBase = false) {
  const { mask, labels: colLabels } = topInfo(data)
  const sideVecs = data?.side?.vecs || []
  const rows = data?.cells?.rows || []
  const records = []
  const needsScale = metric === 'colpc' || metric === 'rowpc'

  for (let i = 0; i < sideVecs.length; i++) {
    const sv = sideVecs[i]
    if (!includeBase && SKIP_TYPES.has(sv.type)) continue
    const rowLabel = sanitizeLabel(sv.label, `Row ${i + 1}`)
    const cells = (i < rows.length ? rows[i].cell : []) || []
    let colIdx = 0
    for (let j = 0; j < mask.length; j++) {
      if (!mask[j]) continue
      const cell = j < cells.length ? cells[j] : {}
      const raw = cell[metric] || 0
      records.push({ row: rowLabel, col: colLabels[colIdx], value: needsScale ? raw * 100 : raw })
      colIdx++
    }
  }
  return records
}

/**
 * Convert PlatinumData to a matrix for Chart.js / heatmaps.
 *
 * @param {object} data - PlatinumData object
 * @param {string} [metric='colpc'] - 'colpc', 'rowpc', or 'freq'
 * @param {boolean} [includeBase=false] - include Base/Spacer rows
 * @returns {{ rows: string[], columns: string[], values: number[][] }}
 */
export function toMatrix(data, metric = 'colpc', includeBase = false) {
  const { mask, labels: columns } = topInfo(data)
  const sideVecs = data?.side?.vecs || []
  const cellRows = data?.cells?.rows || []
  const rowLabels = []
  const values = []
  const needsScale = metric === 'colpc' || metric === 'rowpc'

  for (let i = 0; i < sideVecs.length; i++) {
    const sv = sideVecs[i]
    if (!includeBase && SKIP_TYPES.has(sv.type)) continue
    rowLabels.push(sanitizeLabel(sv.label, `Row ${i + 1}`))
    const cells = (i < cellRows.length ? cellRows[i].cell : []) || []
    const rowData = []
    for (let j = 0; j < mask.length; j++) {
      if (!mask[j]) continue
      const cell = j < cells.length ? cells[j] : {}
      const raw = cell[metric] || 0
      rowData.push(needsScale ? raw * 100 : raw)
    }
    values.push(rowData)
  }
  return { rows: rowLabels, columns, values }
}

/**
 * Extract base sizes (unweighted counts) per column.
 *
 * @param {object} data - PlatinumData object
 * @returns {Array<{column: string, base: number}>}
 */
export function getBaseSizes(data) {
  const { mask, labels } = topInfo(data)
  const sideVecs = data?.side?.vecs || []
  const cellRows = data?.cells?.rows || []

  for (let i = 0; i < sideVecs.length; i++) {
    if (sideVecs[i].type === 'Base' && i < cellRows.length) {
      const cells = cellRows[i].cell || []
      const result = []
      let colIdx = 0
      for (let j = 0; j < mask.length; j++) {
        if (!mask[j]) continue
        const cell = j < cells.length ? cells[j] : {}
        result.push({ column: labels[colIdx], base: cell.freq || 0 })
        colIdx++
      }
      return result
    }
  }
  return []
}

/**
 * Extract table metadata.
 *
 * @param {object} data - PlatinumData object
 * @returns {{ top: string, side: string, filter: string, weight: string, name: string }}
 */
export function getMeta(data) {
  const meta = data?.meta || {}
  return {
    top: meta.top || '',
    side: meta.side || '',
    filter: meta.filter || '',
    weight: meta.weight || '',
    name: data?.name || '',
  }
}

/**
 * Get column labels (filtered, no Base/Spacer).
 * @param {object} data - PlatinumData object
 * @returns {string[]}
 */
export function getColumnLabels(data) {
  return topInfo(data).labels
}

/**
 * Get row labels (filtered, no Base/Spacer).
 * @param {object} data - PlatinumData object
 * @returns {string[]}
 */
export function getRowLabels(data) {
  const sideVecs = data?.side?.vecs || []
  return sideVecs
    .filter(v => !SKIP_TYPES.has(v.type))
    .map((v, i) => sanitizeLabel(v.label, `Row ${i + 1}`))
}
