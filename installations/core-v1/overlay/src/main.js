import * as d3 from 'd3'
import './style.css'
import { listDatasets, getAllDatasets, getRecords, getDatasetMeta } from './lib/data-loader.js'

const datasets = getAllDatasets()
const datasetNames = listDatasets()

const app = d3.select('#app')
app.html('')

if (datasetNames.length === 0) {
  app.append('div').attr('class', 'empty-state')
    .html('<h2>No datasets loaded</h2><p>Use <code>render_table</code> to tabulate data, then rebuild.</p>')
} else {
  app.append('h1').text('Dashboard')
  app.append('p').attr('class', 'subtitle')
    .text(`${datasetNames.length} dataset${datasetNames.length !== 1 ? 's' : ''} loaded: ${datasetNames.join(', ')}`)
  // Your visualization code here — replace this template
}
