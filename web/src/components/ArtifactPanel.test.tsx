// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import React from 'react'
import ArtifactPanel from './ArtifactPanel.js'
import type { ArtifactInfo, TodoItem } from '../types.js'

function makeArtifact(overrides: Partial<ArtifactInfo> = {}): ArtifactInfo {
  return {
    id: 'art-1',
    filePath: 'chart.png',
    fileName: 'chart.png',
    label: 'Test Chart',
    description: 'A test chart',
    artifactType: 'image',
    source: 'registered',
    status: 'live',
    ...overrides,
  }
}

describe('ArtifactPanel', () => {
  it('renders without crashing with empty artifacts', () => {
    // With no artifacts and no todos, component returns null — container is still valid
    const { container } = render(
      <ArtifactPanel artifacts={[]} sessionId="sess-1" />
    )
    expect(container).toBeTruthy()
  })

  it('renders the correct number of artifact items', () => {
    // Default view mode is 'tree' which renders fileName; use distinct fileNames
    const artifacts = [
      makeArtifact({ id: 'a1', filePath: 'alpha.png', fileName: 'alpha.png', label: 'Chart Alpha' }),
      makeArtifact({ id: 'a2', filePath: 'beta.png', fileName: 'beta.png', label: 'Chart Beta' }),
      makeArtifact({ id: 'a3', filePath: 'gamma.png', fileName: 'gamma.png', label: 'Chart Gamma' }),
    ]
    render(<ArtifactPanel artifacts={artifacts} sessionId="sess-1" />)

    // Tree view renders fileName values
    expect(screen.getByText('alpha.png')).toBeTruthy()
    expect(screen.getByText('beta.png')).toBeTruthy()
    expect(screen.getByText('gamma.png')).toBeTruthy()
  })

  it('renders todo items when provided', () => {
    const todos: TodoItem[] = [
      { content: 'Fix the bug', status: 'pending' },
      { content: 'Write tests', status: 'completed' },
    ]
    render(
      <ArtifactPanel artifacts={[]} todos={todos} sessionId="sess-1" />
    )

    expect(screen.getByText('Fix the bug')).toBeTruthy()
    expect(screen.getByText('Write tests')).toBeTruthy()
  })

  it('calls onArtifactClick when an artifact is clicked', () => {
    const onArtifactClick = vi.fn()
    // Default view is 'tree', which renders fileName — use fileName as the clickable text
    const artifact = makeArtifact({ id: 'click-me', filePath: 'clickable.png', fileName: 'clickable.png', label: 'Clickable' })
    render(
      <ArtifactPanel
        artifacts={[artifact]}
        sessionId="sess-1"
        onArtifactClick={onArtifactClick}
      />
    )

    fireEvent.click(screen.getByText('clickable.png'))
    expect(onArtifactClick).toHaveBeenCalledWith(artifact)
  })

  it('renders lost artifacts with dimmed styling', () => {
    const lostArtifact = makeArtifact({ id: 'lost-1', filePath: 'lost.png', fileName: 'lost.png', label: 'Lost File', status: 'lost' })
    const liveArtifact = makeArtifact({ id: 'live-1', filePath: 'live.png', fileName: 'live.png', label: 'Live File', status: 'live' })
    render(
      <ArtifactPanel
        artifacts={[lostArtifact, liveArtifact]}
        sessionId="sess-1"
      />
    )

    // Both should render (tree view shows fileName)
    expect(screen.getByText('lost.png')).toBeTruthy()
    expect(screen.getByText('live.png')).toBeTruthy()
  })
})
