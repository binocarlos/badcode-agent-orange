/**
 * radar-chart.js — Chart.js radar/spider chart.
 *
 * @param {HTMLElement} el - Container element
 * @param {object} config
 * @param {{ rows: string[], columns: string[], values: number[][] }} config.matrix
 * @param {string} [config.title]
 * @param {object} [config.palette] - Map of series name to hex color
 */
import { Chart, RadarController, RadialLinearScale, PointElement, LineElement, Filler, Tooltip, Legend } from 'chart.js'

Chart.register(RadarController, RadialLinearScale, PointElement, LineElement, Filler, Tooltip, Legend)

const DEFAULT_PALETTE = ['#3b82f6', '#ef4444', '#22c55e', '#f59e0b', '#8b5cf6', '#ec4899', '#14b8a6', '#f97316']

function cssVar(name) {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
}

export function radarChart(el, { matrix, title, palette = {} } = {}) {
  el.innerHTML = ''
  if (!matrix || !matrix.rows.length || !matrix.columns.length) {
    el.innerHTML = '<div style="padding:40px;text-align:center;color:var(--text-secondary)">No data available</div>'
    return
  }

  const { rows, columns, values } = matrix

  // rows = axes (labels around the radar), columns = series
  const datasets = columns.map((col, ci) => {
    const color = palette[col] || DEFAULT_PALETTE[ci % DEFAULT_PALETTE.length]
    return {
      label: col,
      data: rows.map((_, ri) => values[ri]?.[ci] ?? 0),
      borderColor: color,
      backgroundColor: color + '33',
      borderWidth: 2,
      pointRadius: 4,
      pointBackgroundColor: color,
      pointBorderColor: cssVar('--bg-card'),
      pointBorderWidth: 2,
      fill: true,
    }
  })

  const canvas = document.createElement('canvas')
  el.appendChild(canvas)

  const textSecondary = cssVar('--text-secondary')
  const borderColor = cssVar('--border')

  new Chart(canvas, {
    type: 'radar',
    data: { labels: rows, datasets },
    options: {
      responsive: true,
      maintainAspectRatio: true,
      plugins: {
        title: title ? {
          display: true,
          text: title,
          color: cssVar('--text-primary'),
          font: { size: 14, weight: '600' },
          padding: { bottom: 12 },
        } : { display: false },
        legend: {
          display: columns.length > 1,
          position: 'bottom',
          labels: { color: textSecondary, usePointStyle: true, pointStyle: 'rectRounded', padding: 16 },
        },
        tooltip: {
          backgroundColor: cssVar('--bg-card'),
          titleColor: cssVar('--text-primary'),
          bodyColor: cssVar('--text-primary'),
          borderColor: borderColor,
          borderWidth: 1,
          cornerRadius: 6,
          padding: 10,
        },
      },
      scales: {
        r: {
          angleLines: { color: borderColor },
          grid: { color: borderColor },
          pointLabels: { color: textSecondary, font: { size: 12 } },
          ticks: {
            color: textSecondary,
            backdropColor: 'transparent',
            font: { size: 10 },
          },
          beginAtZero: true,
        },
      },
    },
  })
}
