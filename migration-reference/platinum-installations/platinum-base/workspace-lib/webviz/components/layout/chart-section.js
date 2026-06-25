/**
 * chart-section.js — Card with chart + narrative side by side.
 *
 * @param {HTMLElement} el - Container element
 * @param {object} config
 * @param {function} config.chart - Render function: (containerEl) => void
 * @param {string} [config.title] - Section title
 * @param {string} [config.narrative] - Narrative text
 */

export function chartSection(el, { chart, title, narrative } = {}) {
  el.innerHTML = ''

  const card = document.createElement('div')
  card.className = 'card'

  if (title) {
    const titleEl = document.createElement('div')
    titleEl.className = 'card-title'
    titleEl.textContent = title
    card.appendChild(titleEl)
  }

  const layout = document.createElement('div')
  layout.style.cssText = `
    display: flex;
    gap: 24px;
    align-items: flex-start;
  `

  // Chart container (60%)
  const chartContainer = document.createElement('div')
  chartContainer.style.cssText = `
    flex: 0 0 60%;
    min-width: 0;
    position: relative;
  `

  if (typeof chart === 'function') {
    chart(chartContainer)
  }

  layout.appendChild(chartContainer)

  // Narrative (40%)
  if (narrative) {
    const narrativeEl = document.createElement('div')
    narrativeEl.style.cssText = `
      flex: 1;
      font-size: 0.875rem;
      line-height: 1.6;
      color: var(--text-secondary);
      padding-top: 8px;
    `
    narrativeEl.textContent = narrative
    layout.appendChild(narrativeEl)
  }

  card.appendChild(layout)
  el.appendChild(card)

  // Responsive: stack on mobile
  const mq = window.matchMedia('(max-width: 768px)')
  function applyLayout(e) {
    if (e.matches) {
      layout.style.flexDirection = 'column'
      chartContainer.style.flex = '1 1 auto'
    } else {
      layout.style.flexDirection = 'row'
      chartContainer.style.flex = '0 0 60%'
    }
  }
  applyLayout(mq)
  mq.addEventListener('change', applyLayout)
}
