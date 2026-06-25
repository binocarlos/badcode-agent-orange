/**
 * line-chart.js — D3-based multi-series line chart.
 *
 * @param {HTMLElement} el - Container element
 * @param {object} config
 * @param {Array<{row: string, col: string, value: number}>} config.records
 * @param {string} [config.title]
 * @param {object} [config.palette] - Map of series name to hex color
 * @param {boolean} [config.area] - Fill area under lines
 */
import * as d3 from 'd3'

const DEFAULT_PALETTE = ['#3b82f6', '#ef4444', '#22c55e', '#f59e0b', '#8b5cf6', '#ec4899', '#14b8a6', '#f97316']

function cssVar(name) {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
}

export function lineChart(el, { records = [], title, palette = {}, area = false } = {}) {
  el.innerHTML = ''
  if (!records.length) {
    el.innerHTML = '<div style="padding:40px;text-align:center;color:var(--text-secondary)">No data available</div>'
    return
  }

  const xLabels = [...new Set(records.map(r => r.row))]
  const series = [...new Set(records.map(r => r.col))]
  const dataMap = new Map(records.map(r => [`${r.row}|${r.col}`, r.value]))

  const colorScale = d3.scaleOrdinal()
    .domain(series)
    .range(series.map((s, i) => palette[s] || DEFAULT_PALETTE[i % DEFAULT_PALETTE.length]))

  const margin = { top: title ? 36 : 16, right: 20, bottom: 40, left: 50 }
  const width = 600
  const height = 360
  const inner = { w: width - margin.left - margin.right, h: height - margin.top - margin.bottom }

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

  const x = d3.scalePoint().domain(xLabels).range([0, inner.w]).padding(0.5)
  const maxVal = d3.max(records, r => r.value) || 0
  const y = d3.scaleLinear().domain([0, maxVal * 1.1]).nice().range([inner.h, 0])

  g.append('g').attr('transform', `translate(0,${inner.h})`).call(d3.axisBottom(x))
  g.append('g').call(d3.axisLeft(y).ticks(6))

  // Grid lines
  g.append('g').attr('class', 'grid')
    .selectAll('line').data(y.ticks(6)).join('line')
    .attr('x1', 0).attr('x2', inner.w).attr('y1', d => y(d)).attr('y2', d => y(d))
    .attr('stroke', cssVar('--border')).attr('stroke-dasharray', '3,3').attr('opacity', 0.5)

  const tooltip = d3.select(el).append('div').attr('class', 'tooltip').style('display', 'none')

  const line = d3.line().x(d => x(d.row)).y(d => y(d.value)).curve(d3.curveMonotoneX)

  series.forEach(s => {
    const pts = xLabels.map(row => ({ row, value: dataMap.get(`${row}|${s}`) || 0 }))
    const color = colorScale(s)

    if (area) {
      const areaGen = d3.area().x(d => x(d.row)).y0(inner.h).y1(d => y(d.value)).curve(d3.curveMonotoneX)
      g.append('path').datum(pts).attr('d', areaGen).attr('fill', color).attr('opacity', 0.15)
    }

    g.append('path').datum(pts).attr('d', line)
      .attr('fill', 'none').attr('stroke', color).attr('stroke-width', 2.5)

    // Data points
    g.selectAll(null).data(pts).join('circle')
      .attr('cx', d => x(d.row)).attr('cy', d => y(d.value)).attr('r', 4)
      .attr('fill', color).attr('stroke', cssVar('--bg-card')).attr('stroke-width', 2)
      .on('mouseenter', (event, d) => {
        tooltip.style('display', 'block')
          .html(`<strong>${d.row}</strong><br/>${s}: ${d.value.toFixed(1)}`)
        const rect = el.getBoundingClientRect()
        tooltip.style('left', `${event.clientX - rect.left + 12}px`).style('top', `${event.clientY - rect.top - 28}px`)
      })
      .on('mouseleave', () => tooltip.style('display', 'none'))
  })

  // Legend
  if (series.length > 1) {
    const legend = svg.append('g').attr('transform', `translate(${margin.left}, ${height - 8})`)
    series.forEach((s, i) => {
      const lg = legend.append('g').attr('transform', `translate(${i * 100}, 0)`)
      lg.append('rect').attr('width', 10).attr('height', 10).attr('rx', 2).attr('fill', colorScale(s))
      lg.append('text').attr('x', 14).attr('y', 9).text(s).style('font-size', '11px').attr('fill', cssVar('--text-secondary'))
    })
  }
}
