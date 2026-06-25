/**
 * heatmap.js — D3-based color-coded matrix heatmap.
 *
 * @param {HTMLElement} el - Container element
 * @param {object} config
 * @param {{ rows: string[], columns: string[], values: number[][] }} config.matrix
 * @param {string} [config.title]
 * @param {string[]} [config.colorScale] - [lowColor, highColor] pair
 */
import * as d3 from 'd3'

function cssVar(name) {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
}

export function heatmap(el, { matrix, title, colorScale: colorRange } = {}) {
  el.innerHTML = ''
  if (!matrix || !matrix.rows.length || !matrix.columns.length) {
    el.innerHTML = '<div style="padding:40px;text-align:center;color:var(--text-secondary)">No data available</div>'
    return
  }

  const { rows, columns, values } = matrix

  // Compute label widths for margin
  const maxRowLabel = Math.max(...rows.map(r => r.length), 4)
  const maxColLabel = Math.max(...columns.map(c => c.length), 4)

  const margin = { top: (title ? 36 : 16) + maxColLabel * 5, right: 20, bottom: 20, left: Math.min(maxRowLabel * 7, 140) }
  const cellSize = Math.max(32, Math.min(60, 500 / Math.max(rows.length, columns.length)))
  const width = margin.left + columns.length * cellSize + margin.right
  const height = margin.top + rows.length * cellSize + margin.bottom

  const svg = d3.select(el).append('svg')
    .attr('viewBox', `0 0 ${width} ${height}`)
    .attr('preserveAspectRatio', 'xMidYMid meet')
    .style('width', '100%')

  if (title) {
    svg.append('text').attr('x', width / 2).attr('y', 20)
      .attr('text-anchor', 'middle').attr('fill', cssVar('--text-primary'))
      .style('font-size', '14px').style('font-weight', '600').text(title)
  }

  const g = svg.append('g').attr('transform', `translate(${margin.left},${margin.top})`)

  // Flatten values for domain
  const allVals = values.flat().filter(v => v != null)
  const lowColor = colorRange?.[0] || '#1e3a5f'
  const highColor = colorRange?.[1] || '#3b82f6'
  const color = d3.scaleSequential()
    .domain([d3.min(allVals) || 0, d3.max(allVals) || 1])
    .interpolator(d3.interpolate(lowColor, highColor))

  const tooltip = d3.select(el).append('div').attr('class', 'tooltip').style('display', 'none')

  // Cells
  rows.forEach((row, ri) => {
    columns.forEach((col, ci) => {
      const val = values[ri]?.[ci] ?? 0
      const x = ci * cellSize
      const y = ri * cellSize

      g.append('rect')
        .attr('x', x).attr('y', y)
        .attr('width', cellSize - 2).attr('height', cellSize - 2)
        .attr('rx', 3)
        .attr('fill', color(val))
        .on('mouseenter', (event) => {
          tooltip.style('display', 'block')
            .html(`<strong>${row}</strong> / ${col}<br/>${val.toFixed(1)}`)
          const rect = el.getBoundingClientRect()
          tooltip.style('left', `${event.clientX - rect.left + 12}px`).style('top', `${event.clientY - rect.top - 28}px`)
        })
        .on('mouseleave', () => tooltip.style('display', 'none'))

      // Cell value label
      if (cellSize >= 36) {
        const textColor = val > (d3.max(allVals) + d3.min(allVals)) / 2 ? '#ffffff' : cssVar('--text-secondary')
        g.append('text')
          .attr('x', x + (cellSize - 2) / 2).attr('y', y + (cellSize - 2) / 2)
          .attr('text-anchor', 'middle').attr('dominant-baseline', 'central')
          .attr('fill', textColor).style('font-size', '11px')
          .text(val.toFixed(1))
      }
    })
  })

  // Row labels
  rows.forEach((row, ri) => {
    g.append('text')
      .attr('x', -8).attr('y', ri * cellSize + (cellSize - 2) / 2)
      .attr('text-anchor', 'end').attr('dominant-baseline', 'central')
      .attr('fill', cssVar('--text-secondary')).style('font-size', '11px')
      .text(row)
  })

  // Column labels (rotated)
  columns.forEach((col, ci) => {
    g.append('text')
      .attr('x', 0).attr('y', 0)
      .attr('transform', `translate(${ci * cellSize + (cellSize - 2) / 2},-8) rotate(-45)`)
      .attr('text-anchor', 'start').attr('dominant-baseline', 'central')
      .attr('fill', cssVar('--text-secondary')).style('font-size', '11px')
      .text(col)
  })
}
