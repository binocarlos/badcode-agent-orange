/**
 * bar-chart.js — D3-based grouped/stacked bar chart.
 *
 * @param {HTMLElement} el - Container element
 * @param {object} config
 * @param {Array<{row: string, col: string, value: number}>} config.records
 * @param {string} [config.title]
 * @param {object} [config.palette] - Map of series name to hex color
 * @param {boolean} [config.horizontal] - Horizontal bars
 * @param {boolean} [config.stacked] - Stacked bars
 * @param {string} [config.metric] - Axis label (default "%")
 */
import * as d3 from 'd3'

const DEFAULT_PALETTE = ['#3b82f6', '#ef4444', '#22c55e', '#f59e0b', '#8b5cf6', '#ec4899', '#14b8a6', '#f97316']

function cssVar(name) {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
}

export function barChart(el, { records = [], title, palette = {}, horizontal = false, stacked = false, metric = '%' } = {}) {
  el.innerHTML = ''
  if (!records.length) {
    el.innerHTML = '<div style="padding:40px;text-align:center;color:var(--text-secondary)">No data available</div>'
    return
  }

  const categories = [...new Set(records.map(r => r.row))]
  const series = [...new Set(records.map(r => r.col))]
  const dataMap = new Map(records.map(r => [`${r.row}|${r.col}`, r.value]))

  const colorScale = d3.scaleOrdinal()
    .domain(series)
    .range(series.map((s, i) => palette[s] || DEFAULT_PALETTE[i % DEFAULT_PALETTE.length]))

  const margin = { top: title ? 36 : 16, right: 20, bottom: 40, left: 60 }
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

  // Tooltip
  const tooltip = d3.select(el).append('div').attr('class', 'tooltip').style('display', 'none')

  // Build scales
  const catScale = d3.scaleBand().domain(categories).range([0, horizontal ? inner.h : inner.w]).padding(0.2)
  const subScale = stacked ? null : d3.scaleBand().domain(series).range([0, catScale.bandwidth()]).padding(0.05)

  let maxVal
  if (stacked) {
    maxVal = d3.max(categories, cat => d3.sum(series, s => dataMap.get(`${cat}|${s}`) || 0))
  } else {
    maxVal = d3.max(records, r => r.value) || 0
  }
  const valScale = d3.scaleLinear().domain([0, maxVal * 1.1]).nice().range(horizontal ? [0, inner.w] : [inner.h, 0])

  // Axes
  if (horizontal) {
    g.append('g').attr('transform', `translate(0,${inner.h})`).call(d3.axisBottom(valScale).ticks(6))
    g.append('g').call(d3.axisLeft(catScale))
  } else {
    g.append('g').attr('transform', `translate(0,${inner.h})`).call(d3.axisBottom(catScale))
    g.append('g').call(d3.axisLeft(valScale).ticks(6))
  }

  // Bars
  categories.forEach(cat => {
    let cumulative = 0
    series.forEach(s => {
      const val = dataMap.get(`${cat}|${s}`) || 0
      let x, y, w, h
      if (stacked) {
        if (horizontal) {
          x = valScale(cumulative); y = catScale(cat)
          w = valScale(cumulative + val) - valScale(cumulative); h = catScale.bandwidth()
        } else {
          x = catScale(cat); y = valScale(cumulative + val)
          w = catScale.bandwidth(); h = valScale(cumulative) - valScale(cumulative + val)
        }
        cumulative += val
      } else {
        if (horizontal) {
          x = 0; y = catScale(cat) + subScale(s)
          w = valScale(val); h = subScale.bandwidth()
        } else {
          x = catScale(cat) + subScale(s); y = valScale(val)
          w = subScale.bandwidth(); h = inner.h - valScale(val)
        }
      }
      g.append('rect')
        .attr('x', x).attr('y', y).attr('width', Math.max(0, w)).attr('height', Math.max(0, h))
        .attr('fill', colorScale(s)).attr('rx', 2)
        .on('mouseenter', (event) => {
          tooltip.style('display', 'block')
            .html(`<strong>${cat}</strong><br/>${s}: ${val.toFixed(1)}${metric}`)
          const rect = el.getBoundingClientRect()
          tooltip.style('left', `${event.clientX - rect.left + 12}px`).style('top', `${event.clientY - rect.top - 28}px`)
        })
        .on('mouseleave', () => tooltip.style('display', 'none'))
    })
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
