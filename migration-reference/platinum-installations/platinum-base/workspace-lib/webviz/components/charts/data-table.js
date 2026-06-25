/**
 * data-table.js — Styled HTML table matching the dark theme.
 *
 * @param {HTMLElement} el - Container element
 * @param {object} config
 * @param {Array<{row: string, col: string, value: number}>} config.records
 * @param {string} [config.title]
 * @param {boolean} [config.highlightMax] - Highlight highest value per row
 */

export function dataTable(el, { records = [], title, highlightMax = false } = {}) {
  el.innerHTML = ''
  if (!records.length) {
    el.innerHTML = '<div style="padding:40px;text-align:center;color:var(--text-secondary)">No data available</div>'
    return
  }

  // Pivot records into rows x columns
  const rowNames = [...new Set(records.map(r => r.row))]
  const colNames = [...new Set(records.map(r => r.col))]
  const dataMap = new Map(records.map(r => [`${r.row}|${r.col}`, r.value]))

  const wrapper = document.createElement('div')
  wrapper.style.cssText = 'overflow-x:auto;'

  if (title) {
    const titleEl = document.createElement('div')
    titleEl.className = 'card-title'
    titleEl.textContent = title
    wrapper.appendChild(titleEl)
  }

  const table = document.createElement('table')
  table.style.cssText = `
    width:100%; border-collapse:collapse; font-size:0.8125rem;
    color:var(--text-primary); background:var(--bg-card);
    border:1px solid var(--border); border-radius:var(--radius);
  `

  // Header
  const thead = document.createElement('thead')
  const headerRow = document.createElement('tr')
  const cornerTh = document.createElement('th')
  cornerTh.style.cssText = cellStyle(true, false)
  headerRow.appendChild(cornerTh)

  colNames.forEach(col => {
    const th = document.createElement('th')
    th.textContent = col
    th.style.cssText = cellStyle(true, false)
    headerRow.appendChild(th)
  })
  thead.appendChild(headerRow)
  table.appendChild(thead)

  // Body
  const tbody = document.createElement('tbody')
  rowNames.forEach(row => {
    const tr = document.createElement('tr')

    // Find max in this row for highlight
    let maxVal = -Infinity
    if (highlightMax) {
      colNames.forEach(col => {
        const v = dataMap.get(`${row}|${col}`) ?? -Infinity
        if (v > maxVal) maxVal = v
      })
    }

    const rowTh = document.createElement('td')
    rowTh.textContent = row
    rowTh.style.cssText = cellStyle(false, true)
    tr.appendChild(rowTh)

    colNames.forEach(col => {
      const td = document.createElement('td')
      const val = dataMap.get(`${row}|${col}`)
      td.textContent = val != null ? val.toFixed(1) : '-'
      const isMax = highlightMax && val === maxVal && val !== -Infinity
      td.style.cssText = cellStyle(false, false, isMax)
      tr.appendChild(td)
    })

    // Hover effect
    tr.addEventListener('mouseenter', () => { tr.style.background = 'var(--bg-card-hover)' })
    tr.addEventListener('mouseleave', () => { tr.style.background = '' })

    tbody.appendChild(tr)
  })
  table.appendChild(tbody)

  wrapper.appendChild(table)
  el.appendChild(wrapper)
}

function cellStyle(isHeader, isRowLabel, isHighlight = false) {
  const base = `
    padding:8px 12px;
    border-bottom:1px solid var(--border);
    text-align:${isRowLabel ? 'left' : 'right'};
    white-space:nowrap;
  `
  if (isHeader) {
    return base + `
      font-weight:600;
      color:var(--text-secondary);
      text-transform:uppercase;
      letter-spacing:0.05em;
      font-size:0.75rem;
      background:var(--bg-primary);
      position:sticky; top:0;
    `
  }
  if (isRowLabel) {
    return base + 'font-weight:500; color:var(--text-primary);'
  }
  if (isHighlight) {
    return base + 'color:var(--accent); font-weight:600;'
  }
  return base + 'color:var(--text-primary);'
}
