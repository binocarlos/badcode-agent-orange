#!/usr/bin/env node
/**
 * inspect-data.js — Debug helper to inspect PlatinumData JSON files from the command line.
 *
 * Usage:
 *   node /workspace/lib/viz/inspect-data.js                    # list all datasets
 *   node /workspace/lib/viz/inspect-data.js awareness          # inspect a specific dataset
 *   node /workspace/lib/viz/inspect-data.js awareness matrix   # show matrix format
 *   node /workspace/lib/viz/inspect-data.js awareness records  # show records format
 *
 * This works in Node.js (unlike the browser data-loader.js, which uses fetch).
 * Uses the same toRecords/toMatrix functions that the data-loader uses internally,
 * so the output exactly matches what your webapp code will see.
 */

import { readFileSync, readdirSync } from 'node:fs'
import { join, basename } from 'node:path'
import { toRecords, toMatrix, getMeta, getBaseSizes, getColumnLabels, getRowLabels } from './platinum-data.js'

const DATA_DIR = '/workspace/data'

function listDatasets() {
  try {
    return readdirSync(DATA_DIR)
      .filter(f => f.endsWith('.json'))
      .map(f => f.replace('.json', ''))
  } catch {
    return []
  }
}

function loadDataset(name) {
  const filePath = join(DATA_DIR, `${name}.json`)
  return JSON.parse(readFileSync(filePath, 'utf8'))
}

const args = process.argv.slice(2)

if (args.length === 0) {
  // List all datasets
  const names = listDatasets()
  if (names.length === 0) {
    console.log('No datasets found in /workspace/data/')
    console.log('Run render_table first to generate data files.')
  } else {
    console.log(`Datasets (${names.length}):`)
    for (const name of names) {
      const data = loadDataset(name)
      const meta = getMeta(data)
      const cols = getColumnLabels(data)
      const rows = getRowLabels(data)
      console.log(`  ${name}: ${rows.length} rows x ${cols.length} columns (top: ${meta.top || '?'}, side: ${meta.side || '?'})`)
    }
  }
  process.exit(0)
}

const name = args[0]
const mode = args[1] || 'summary'
const data = loadDataset(name)
const meta = getMeta(data)

if (mode === 'summary' || mode === 'info') {
  console.log(`Dataset: ${name}`)
  console.log(`  Top (columns): ${getColumnLabels(data).join(', ')}`)
  console.log(`  Side (rows):   ${getRowLabels(data).join(', ')}`)
  console.log(`  Meta:          top=${meta.top} side=${meta.side} filter=${meta.filter} weight=${meta.weight}`)
  const bases = getBaseSizes(data)
  if (bases.length > 0) {
    console.log(`  Base sizes:    ${bases.map(b => `${b.column}=${b.base}`).join(', ')}`)
  }
  // Show first few records as a preview
  const records = toRecords(data)
  console.log(`  Records:       ${records.length} total`)
  console.log(`  Preview (first 5):`)
  for (const r of records.slice(0, 5)) {
    console.log(`    ${r.row} | ${r.col} | ${r.value.toFixed(1)}%`)
  }
} else if (mode === 'matrix') {
  const { rows, columns, values } = toMatrix(data)
  console.log(`Matrix: ${rows.length} rows x ${columns.length} columns`)
  console.log(`Columns: ${JSON.stringify(columns)}`)
  console.log(`Rows: ${JSON.stringify(rows)}`)
  console.log(`Values:`)
  for (let i = 0; i < rows.length; i++) {
    console.log(`  ${rows[i]}: ${values[i].map(v => v.toFixed(1)).join(', ')}`)
  }
} else if (mode === 'records') {
  const records = toRecords(data)
  console.log(JSON.stringify(records, null, 2))
} else {
  console.log(`Unknown mode: ${mode}. Use: summary, matrix, records`)
}
