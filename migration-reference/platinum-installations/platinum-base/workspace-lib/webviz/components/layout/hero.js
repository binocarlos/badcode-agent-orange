/**
 * hero.js — Title banner section.
 *
 * @param {HTMLElement} el - Container element
 * @param {object} config
 * @param {string} config.title - Main heading
 * @param {string} [config.subtitle] - Subtitle text
 * @param {string} [config.date] - Date string
 * @param {string} [config.logo] - Logo image URL
 */

export function hero(el, { title, subtitle, date, logo } = {}) {
  el.innerHTML = ''

  const section = document.createElement('section')
  section.style.cssText = `
    padding: 32px 0 24px;
    border-bottom: 3px solid var(--accent);
    margin-bottom: 24px;
  `

  const container = document.createElement('div')
  container.style.cssText = `
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 24px;
  `

  const textBlock = document.createElement('div')

  if (title) {
    const h1 = document.createElement('h1')
    h1.textContent = title
    h1.style.cssText = `
      font-size: 1.75rem;
      font-weight: 700;
      color: var(--text-primary);
      margin-bottom: 4px;
      line-height: 1.2;
    `
    textBlock.appendChild(h1)
  }

  if (subtitle) {
    const sub = document.createElement('p')
    sub.textContent = subtitle
    sub.style.cssText = `
      font-size: 1rem;
      color: var(--text-secondary);
      margin-top: 4px;
    `
    textBlock.appendChild(sub)
  }

  if (date) {
    const dateEl = document.createElement('p')
    dateEl.textContent = date
    dateEl.style.cssText = `
      font-size: 0.8125rem;
      color: var(--text-muted);
      margin-top: 8px;
    `
    textBlock.appendChild(dateEl)
  }

  container.appendChild(textBlock)

  if (logo) {
    const img = document.createElement('img')
    img.src = logo
    img.alt = 'Logo'
    img.style.cssText = `
      max-height: 48px;
      width: auto;
      opacity: 0.9;
    `
    container.appendChild(img)
  }

  section.appendChild(container)
  el.appendChild(section)
}
