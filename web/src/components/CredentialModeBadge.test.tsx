// @vitest-environment jsdom
// RD18: mock mode is invisible and it is the default. The badge is the one
// place the browser says which model answered.

import React from 'react'
import { render, screen } from '@testing-library/react'
import { createTheme, ThemeProvider } from '@mui/material'
import { describe, it, expect } from 'vitest'
import CredentialModeBadge, { isCredentialMode } from './CredentialModeBadge.js'

const renderBadge = (mode: string | null | undefined) =>
  render(
    <ThemeProvider theme={createTheme()}>
      <CredentialModeBadge mode={mode} />
    </ThemeProvider>,
  )

describe('CredentialModeBadge', () => {
  it('says MOCK MODEL when no model is being called', () => {
    const { container } = renderBadge('mock')
    expect(screen.getByText('MOCK MODEL')).toBeTruthy()
    const chip = container.querySelector('[data-credential-mode="mock"]')!
    expect(chip).toBeTruthy()
    // Loud: filled and painted, not a quiet outline the eye skips.
    expect(chip.className).toMatch(/filled/i)
    expect(getComputedStyle(chip).backgroundColor).not.toBe('')
  })

  it('names the real credential in each billed mode', () => {
    const key = renderBadge('api-key')
    expect(key.container.querySelector('[data-credential-mode="api-key"]')).toBeTruthy()
    expect(screen.getByText('API key')).toBeTruthy()
    key.unmount()

    const sub = renderBadge('subscription')
    expect(sub.container.querySelector('[data-credential-mode="subscription"]')).toBeTruthy()
    expect(screen.getByText('subscription')).toBeTruthy()
  })

  it('renders nothing rather than guessing when the mode is unknown or absent', () => {
    for (const mode of [undefined, null, '', 'sk-ant-something']) {
      const { container } = renderBadge(mode)
      expect(container.textContent).toBe('')
    }
  })

  it('isCredentialMode admits exactly the three server answers', () => {
    expect(['mock', 'api-key', 'subscription'].every(isCredentialMode)).toBe(true)
    expect(isCredentialMode('opus')).toBe(false)
    expect(isCredentialMode(undefined)).toBe(false)
  })
})
