/**
 * theme.js — Override CSS custom properties on :root.
 *
 * @param {object} overrides - Partial theme override
 * @param {string} [overrides.primary] - Maps to --accent
 * @param {string} [overrides.accent] - Maps to --accent (alias)
 * @param {string} [overrides.bg] - Maps to --bg-primary
 * @param {string} [overrides.text] - Maps to --text-primary
 * @param {string} [overrides.card] - Maps to --bg-card
 * @param {string} [overrides.border] - Maps to --border
 * @param {string} [overrides.success] - Maps to --success
 * @param {string} [overrides.warning] - Maps to --warning
 * @param {string} [overrides.danger] - Maps to --danger
 */

export function applyTheme({ primary, accent, bg, text, card, border, success, warning, danger } = {}) {
  const root = document.documentElement.style

  const accentColor = primary || accent
  if (accentColor) {
    root.setProperty('--accent', accentColor)
    root.setProperty('--accent-hover', adjustBrightness(accentColor, -20))
  }

  if (bg) root.setProperty('--bg-primary', bg)
  if (card) {
    root.setProperty('--bg-card', card)
    root.setProperty('--bg-card-hover', adjustBrightness(card, 15))
  }
  if (border) root.setProperty('--border', border)
  if (success) root.setProperty('--success', success)
  if (warning) root.setProperty('--warning', warning)
  if (danger) root.setProperty('--danger', danger)

  if (text) {
    root.setProperty('--text-primary', text)
    root.setProperty('--text-secondary', adjustAlpha(text, 0.6))
    root.setProperty('--text-muted', adjustAlpha(text, 0.4))
  }
}

/**
 * Adjust hex color brightness by a delta (-255 to +255).
 */
function adjustBrightness(hex, delta) {
  const rgb = hexToRgb(hex)
  if (!rgb) return hex
  const clamp = v => Math.max(0, Math.min(255, v + delta))
  return `rgb(${clamp(rgb.r)}, ${clamp(rgb.g)}, ${clamp(rgb.b)})`
}

/**
 * Create an rgba version of a hex color with given alpha.
 */
function adjustAlpha(hex, alpha) {
  const rgb = hexToRgb(hex)
  if (!rgb) return hex
  return `rgba(${rgb.r}, ${rgb.g}, ${rgb.b}, ${alpha})`
}

/**
 * Parse hex color to {r, g, b}.
 */
function hexToRgb(hex) {
  const match = hex.replace('#', '').match(/^([0-9a-f]{2})([0-9a-f]{2})([0-9a-f]{2})$/i)
  if (!match) return null
  return { r: parseInt(match[1], 16), g: parseInt(match[2], 16), b: parseInt(match[3], 16) }
}
