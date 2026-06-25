/**
 * kpi-cards.js — Row of stat/KPI cards.
 *
 * @param {HTMLElement} el - Container element
 * @param {object} config
 * @param {Array<{label: string, value: string|number, change?: string}>} config.cards
 */

export function kpiCards(el, { cards = [] } = {}) {
  el.innerHTML = ''
  if (!cards.length) return

  const row = document.createElement('div')
  row.className = 'kpi-row'

  cards.forEach(({ label, value, change }) => {
    const card = document.createElement('div')
    card.className = 'kpi'

    const valueEl = document.createElement('div')
    valueEl.className = 'kpi-value'
    valueEl.textContent = value
    card.appendChild(valueEl)

    const labelEl = document.createElement('div')
    labelEl.className = 'kpi-label'
    labelEl.textContent = label
    card.appendChild(labelEl)

    if (change) {
      const changeEl = document.createElement('div')
      changeEl.textContent = change
      const isPositive = change.trim().startsWith('+')
      const isNegative = change.trim().startsWith('-')
      changeEl.style.cssText = `
        font-size: 0.8125rem;
        font-weight: 600;
        margin-top: 4px;
        color: ${isPositive ? 'var(--success)' : isNegative ? 'var(--danger)' : 'var(--text-secondary)'};
      `
      card.appendChild(changeEl)
    }

    row.appendChild(card)
  })

  el.appendChild(row)
}
