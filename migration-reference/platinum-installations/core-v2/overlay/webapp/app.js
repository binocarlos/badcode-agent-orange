// app.js — your dashboard code. No build step: this module runs directly in the
// browser. Libraries (d3, chart.js, three, @observablehq/plot) resolve via the
// import map in index.html, so import them by bare name:
//
//   import * as d3 from 'd3'
//   import Chart from 'chart.js/auto'
//   import * as Plot from '@observablehq/plot'
//
// Data is loaded at runtime (no bundling) with the async data-loader, which reads
// the PlatinumData JSON that render_table saved to /workspace/data/:
//
//   import { getRecords, getMatrix, getDatasetMeta } from './lib/data-loader.js'
//   const records = await getRecords('awareness')   // [{row, col, value}], values 0-100
//
// Reusable, theme-aware components are available too:
//
//   import { barChart, kpiCards, chartSection } from './lib/components/index.js'

import { getRecords, getDatasetMeta } from './lib/data-loader.js'

const app = document.getElementById('app')

async function main() {
  // Example (uncomment and adapt to your datasets):
  // const records = await getRecords('awareness')
  // const { barChart } = await import('./lib/components/index.js')
  // const section = document.createElement('div')
  // app.appendChild(section)
  // barChart(section, { records, title: 'Awareness' })

  app.innerHTML = '<h1 style="padding:24px">Dashboard</h1>'
}

main().catch(err => {
  app.innerHTML = `<pre style="padding:24px;color:#ef4444">${err.message}</pre>`
})
