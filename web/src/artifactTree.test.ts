import { describe, it, expect } from 'vitest'
import { buildArtifactTree, ArtifactTreeNode } from './artifactTree.js'
import type { ArtifactInfo } from './types.js'

function makeArtifact(filePath: string, overrides?: Partial<ArtifactInfo>): ArtifactInfo {
  const fileName = filePath.split('/').pop() || filePath
  return {
    filePath,
    fileName,
    label: fileName,
    artifactType: 'file',
    source: 'auto',
    status: 'live',
    ...overrides,
  }
}

function getFileNames(node: ArtifactTreeNode): string[] {
  const names: string[] = []
  for (const child of node.children) {
    if (child.isDirectory) {
      names.push(...getFileNames(child))
    } else {
      names.push(child.name)
    }
  }
  return names
}

describe('buildArtifactTree', () => {
  it('returns empty root for no artifacts', () => {
    const root = buildArtifactTree([])
    expect(root.isDirectory).toBe(true)
    expect(root.children).toHaveLength(0)
  })

  it('handles single file at root level', () => {
    const root = buildArtifactTree([makeArtifact('/workspace/report.csv')])
    expect(root.children).toHaveLength(1)
    expect(root.children[0].name).toBe('report.csv')
    expect(root.children[0].isDirectory).toBe(false)
    expect(root.children[0].artifact).toBeDefined()
  })

  it('strips /workspace/ prefix', () => {
    const root = buildArtifactTree([
      makeArtifact('/workspace/output/data.csv'),
    ])
    expect(root.children).toHaveLength(1)
    expect(root.children[0].name).toBe('output')
    expect(root.children[0].isDirectory).toBe(true)
    expect(root.children[0].children[0].name).toBe('data.csv')
  })

  it('strips workspace/ without leading slash', () => {
    const root = buildArtifactTree([makeArtifact('workspace/file.txt')])
    expect(root.children).toHaveLength(1)
    expect(root.children[0].name).toBe('file.txt')
  })

  it('groups files in nested directories', () => {
    const root = buildArtifactTree([
      makeArtifact('/workspace/output/charts/bar.png'),
      makeArtifact('/workspace/output/charts/line.png'),
      makeArtifact('/workspace/output/data.csv'),
    ])

    // output dir
    expect(root.children).toHaveLength(1)
    const output = root.children[0]
    expect(output.name).toBe('output')
    expect(output.isDirectory).toBe(true)

    // Should have charts/ dir and data.csv, directories first
    expect(output.children).toHaveLength(2)
    expect(output.children[0].name).toBe('charts')
    expect(output.children[0].isDirectory).toBe(true)
    expect(output.children[1].name).toBe('data.csv')
    expect(output.children[1].isDirectory).toBe(false)
  })

  it('collapses single-child directory chains', () => {
    const root = buildArtifactTree([
      makeArtifact('/workspace/a/b/c/file.txt'),
    ])
    // a/b/c should collapse into a single node "a/b/c"
    expect(root.children).toHaveLength(1)
    expect(root.children[0].name).toBe('a/b/c')
    expect(root.children[0].isDirectory).toBe(true)
    expect(root.children[0].children).toHaveLength(1)
    expect(root.children[0].children[0].name).toBe('file.txt')
  })

  it('does not collapse directories with multiple children', () => {
    const root = buildArtifactTree([
      makeArtifact('/workspace/src/a.ts'),
      makeArtifact('/workspace/src/b.ts'),
    ])
    expect(root.children).toHaveLength(1)
    expect(root.children[0].name).toBe('src')
    expect(root.children[0].children).toHaveLength(2)
  })

  it('sorts directories before files, both alphabetically', () => {
    const root = buildArtifactTree([
      makeArtifact('/workspace/zebra.txt'),
      makeArtifact('/workspace/beta/file.txt'),
      makeArtifact('/workspace/alpha.txt'),
      makeArtifact('/workspace/alpha/file.txt'),
    ])

    const names = root.children.map(c => c.name)
    // directories first (alpha, beta), then files (alpha.txt, zebra.txt)
    expect(names).toEqual(['alpha', 'beta', 'alpha.txt', 'zebra.txt'])
  })

  it('preserves artifact reference on file nodes', () => {
    const artifact = makeArtifact('/workspace/code.py', { artifactType: 'code', status: 'extracted' })
    const root = buildArtifactTree([artifact])
    expect(root.children[0].artifact).toBe(artifact)
  })

  it('handles paths without workspace prefix', () => {
    const root = buildArtifactTree([
      makeArtifact('/output/result.json'),
    ])
    expect(root.children).toHaveLength(1)
    const names = getFileNames(root)
    expect(names).toContain('result.json')
  })

  it('handles mixed prefix styles', () => {
    const root = buildArtifactTree([
      makeArtifact('/workspace/a.txt'),
      makeArtifact('workspace/b.txt'),
      makeArtifact('c.txt'),
    ])
    const names = getFileNames(root)
    expect(names).toEqual(expect.arrayContaining(['a.txt', 'b.txt', 'c.txt']))
    expect(names).toHaveLength(3)
  })
})
