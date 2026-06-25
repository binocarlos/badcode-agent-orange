/**
 * section-nav.js — Sticky navigation bar with scroll tracking.
 *
 * @param {HTMLElement} el - Container element
 * @param {object} config
 * @param {Array<{id: string, label: string}>} config.sections
 */

export function sectionNav(el, { sections = [] } = {}) {
  el.innerHTML = ''
  if (!sections.length) return

  const nav = document.createElement('nav')
  nav.style.cssText = `
    position: sticky;
    top: 0;
    z-index: 50;
    background: var(--bg-primary);
    border-bottom: 1px solid var(--border);
    padding: 0 0 0 0;
    margin-bottom: 24px;
    display: flex;
    gap: 4px;
    overflow-x: auto;
    -webkit-overflow-scrolling: touch;
  `

  const links = []

  sections.forEach(({ id, label }) => {
    const link = document.createElement('a')
    link.href = `#${id}`
    link.textContent = label
    link.dataset.sectionId = id
    link.style.cssText = `
      padding: 12px 16px;
      font-size: 0.8125rem;
      font-weight: 500;
      color: var(--text-secondary);
      text-decoration: none;
      white-space: nowrap;
      border-bottom: 2px solid transparent;
      transition: color 0.2s, border-color 0.2s;
      cursor: pointer;
    `

    link.addEventListener('mouseenter', () => {
      if (!link.classList.contains('active')) {
        link.style.color = 'var(--text-primary)'
      }
    })
    link.addEventListener('mouseleave', () => {
      if (!link.classList.contains('active')) {
        link.style.color = 'var(--text-secondary)'
      }
    })

    link.addEventListener('click', (e) => {
      e.preventDefault()
      const target = document.getElementById(id)
      if (target) {
        target.scrollIntoView({ behavior: 'smooth', block: 'start' })
      }
    })

    links.push(link)
    nav.appendChild(link)
  })

  el.appendChild(nav)

  // Highlight current section with IntersectionObserver
  function setActive(activeId) {
    links.forEach(link => {
      const isActive = link.dataset.sectionId === activeId
      link.classList.toggle('active', isActive)
      link.style.color = isActive ? 'var(--accent)' : 'var(--text-secondary)'
      link.style.borderBottomColor = isActive ? 'var(--accent)' : 'transparent'
    })
  }

  const observer = new IntersectionObserver(
    (entries) => {
      for (const entry of entries) {
        if (entry.isIntersecting) {
          setActive(entry.target.id)
          break
        }
      }
    },
    { rootMargin: '-20% 0px -70% 0px', threshold: 0 }
  )

  // Observe after a tick so DOM sections exist
  requestAnimationFrame(() => {
    sections.forEach(({ id }) => {
      const target = document.getElementById(id)
      if (target) observer.observe(target)
    })
  })

  // Set first section active by default
  if (sections.length) setActive(sections[0].id)
}
