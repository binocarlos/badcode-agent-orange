/**
 * pie-chart.js — D3-based pie/doughnut chart.
 *
 * @param {HTMLElement} el - Container element
 * @param {object} config
 * @param {Array<{row: string, col: string, value: number}>} config.records
 * @param {string} [config.title]
 * @param {object} [config.palette] - Map of label to hex color
 * @param {boolean} [config.donut] - Doughnut variant with inner radius
 */
import * as d3 from 'd3'

const DEFAULT_PALETTE = ['#3b82f6', '#ef4444', '#22c55e', '#f59e0b', '#8b5cf6', '#ec4899', '#14b8a6', '#f97316']

function cssVar(name) {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
}

export function pieChart(el, { records = [], title, palette = {}, donut = false } = {}) {
  el.innerHTML = ''
  if (!records.length) {
    el.innerHTML = '<div style="padding:40px;text-align:center;color:var(--text-secondary)">No data available</div>'
    return
  }

  // Aggregate by row: sum values across columns for each row
  const agg = new Map()
  for (const r of records) {
    agg.set(r.row, (agg.get(r.row) || 0) + r.value)
  }
  const data = [...agg.entries()].map(([label, value]) => ({ label, value }))

  const colorScale = d3.scaleOrdinal()
    .domain(data.map(d => d.label))
    .range(data.map((d, i) => palette[d.label] || DEFAULT_PALETTE[i % DEFAULT_PALETTE.length]))

  const width = 400
  const height = title ? 400 : 360
  const radius = Math.min(width, height - (title ? 60 : 20)) / 2 - 20
  const innerRadius = donut ? radius * 0.55 : 0

  const svg = d3.select(el).append('svg')
    .attr('viewBox', `0 0 ${width} ${height}`)
    .attr('preserveAspectRatio', 'xMidYMid meet')
    .style('width', '100%')

  if (title) {
    svg.append('text').attr('x', width / 2).attr('y', 22)
      .attr('text-anchor', 'middle').attr('fill', cssVar('--text-primary'))
      .style('font-size', '14px').style('font-weight', '600').text(title)
  }

  const centerY = (title ? 60 : 20) + radius
  const g = svg.append('g').attr('transform', `translate(${width / 2},${centerY})`)

  const pie = d3.pie().value(d => d.value).sort(null)
  const arc = d3.arc().innerRadius(innerRadius).outerRadius(radius)
  const arcHover = d3.arc().innerRadius(innerRadius).outerRadius(radius + 6)
  const labelArc = d3.arc().innerRadius(radius * 0.72).outerRadius(radius * 0.72)

  const tooltip = d3.select(el).append('div').attr('class', 'tooltip').style('display', 'none')
  const total = d3.sum(data, d => d.value)

  const slices = g.selectAll('path').data(pie(data)).join('path')
    .attr('d', arc)
    .attr('fill', d => colorScale(d.data.label))
    .attr('stroke', cssVar('--bg-card'))
    .attr('stroke-width', 2)
    .on('mouseenter', function (event, d) {
      d3.select(this).transition().duration(150).attr('d', arcHover)
      const pct = ((d.data.value / total) * 100).toFixed(1)
      tooltip.style('display', 'block')
        .html(`<strong>${d.data.label}</strong><br/>${d.data.value.toFixed(1)} (${pct}%)`)
      const rect = el.getBoundingClientRect()
      tooltip.style('left', `${event.clientX - rect.left + 12}px`).style('top', `${event.clientY - rect.top - 28}px`)
    })
    .on('mouseleave', function () {
      d3.select(this).transition().duration(150).attr('d', arc)
      tooltip.style('display', 'none')
    })

  // Labels on slices with enough space
  g.selectAll('text').data(pie(data)).join('text')
    .attr('transform', d => `translate(${labelArc.centroid(d)})`)
    .attr('text-anchor', 'middle')
    .attr('fill', cssVar('--text-primary'))
    .style('font-size', '11px')
    .style('font-weight', '500')
    .text(d => {
      const angle = d.endAngle - d.startAngle
      return angle > 0.3 ? d.data.label : ''
    })

  // Legend below chart
  const legendY = centerY + radius + 20
  const legend = svg.append('g').attr('transform', `translate(${width / 2 - (data.length * 50)},${legendY})`)
  data.forEach((d, i) => {
    const lg = legend.append('g').attr('transform', `translate(${i * 100}, 0)`)
    lg.append('rect').attr('width', 10).attr('height', 10).attr('rx', 2).attr('fill', colorScale(d.label))
    lg.append('text').attr('x', 14).attr('y', 9).text(d.label).style('font-size', '11px').attr('fill', cssVar('--text-secondary'))
  })
}
