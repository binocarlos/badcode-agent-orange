/**
 * footer.js — Methodology and source references.
 *
 * @param {HTMLElement} el - Container element
 * @param {object} config
 * @param {string} [config.methodology] - Methodology description
 * @param {string[]} [config.sources] - Source references
 */

export function footer(el, { methodology, sources = [] } = {}) {
  el.innerHTML = ''

  const section = document.createElement('footer')
  section.style.cssText = `
    margin-top: 48px;
    padding-top: 24px;
    border-top: 1px solid var(--border);
  `

  if (methodology) {
    const methTitle = document.createElement('h3')
    methTitle.textContent = 'Methodology'
    methTitle.style.cssText = `
      font-size: 0.75rem;
      font-weight: 600;
      color: var(--text-secondary);
      text-transform: uppercase;
      letter-spacing: 0.05em;
      margin-bottom: 8px;
    `
    section.appendChild(methTitle)

    const methText = document.createElement('p')
    methText.textContent = methodology
    methText.style.cssText = `
      font-size: 0.8125rem;
      line-height: 1.6;
      color: var(--text-muted);
      margin-bottom: 16px;
    `
    section.appendChild(methText)
  }

  if (sources.length) {
    const srcTitle = document.createElement('h3')
    srcTitle.textContent = 'Sources'
    srcTitle.style.cssText = `
      font-size: 0.75rem;
      font-weight: 600;
      color: var(--text-secondary);
      text-transform: uppercase;
      letter-spacing: 0.05em;
      margin-bottom: 8px;
    `
    section.appendChild(srcTitle)

    const list = document.createElement('ul')
    list.style.cssText = `
      list-style: none;
      padding: 0;
      margin: 0;
    `

    sources.forEach(src => {
      const li = document.createElement('li')
      li.textContent = src
      li.style.cssText = `
        font-size: 0.8125rem;
        color: var(--text-muted);
        padding: 2px 0;
        padding-left: 12px;
        position: relative;
      `
      // Bullet
      const bullet = document.createElement('span')
      bullet.style.cssText = `
        position: absolute;
        left: 0;
        color: var(--text-muted);
      `
      bullet.textContent = '\u2022'
      li.prepend(bullet)
      list.appendChild(li)
    })

    section.appendChild(list)
  }

  el.appendChild(section)
}
