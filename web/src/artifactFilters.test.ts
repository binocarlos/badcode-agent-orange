import { describe, it, expect } from 'vitest'
import type { ArtifactInfo } from './types.js'
import {
  filterArtifactsByType,
  filterArtifactsByStatus,
  filterArtifactsBySearch,
  getLanguageFromFilename,
  parseCSVPreview,
  ArtifactTypeFilter,
  ArtifactStatusFilter,
} from './artifactFilters.js'

function makeArtifact(overrides: Partial<ArtifactInfo> = {}): ArtifactInfo {
  return {
    filePath: '/workspace/output.txt',
    fileName: 'output.txt',
    label: 'Output',
    artifactType: 'file',
    source: 'registered',
    status: 'live',
    ...overrides,
  }
}

// ==========================================
// Type Filtering
// ==========================================
describe('filterArtifactsByType', () => {
  const artifacts: ArtifactInfo[] = [
    makeArtifact({ fileName: 'chart.png', artifactType: 'image' }),
    makeArtifact({ fileName: 'script.py', artifactType: 'code' }),
    makeArtifact({ fileName: 'data.csv', artifactType: 'csv' }),
    makeArtifact({ fileName: 'bar.png', artifactType: 'chart' }),
    makeArtifact({ fileName: 'report.pdf', artifactType: 'report' }),
    makeArtifact({ fileName: 'misc.txt', artifactType: 'file' }),
  ]

  it('returns all artifacts when filter is "all"', () => {
    expect(filterArtifactsByType(artifacts, 'all')).toHaveLength(6)
  })

  it('filters images (includes image and chart types)', () => {
    const result = filterArtifactsByType(artifacts, 'images')
    expect(result).toHaveLength(2)
    expect(result.every(a => a.artifactType === 'image' || a.artifactType === 'chart')).toBe(true)
  })

  it('filters code artifacts', () => {
    const result = filterArtifactsByType(artifacts, 'code')
    expect(result).toHaveLength(1)
    expect(result[0].artifactType).toBe('code')
  })

  it('filters data artifacts (csv)', () => {
    const result = filterArtifactsByType(artifacts, 'data')
    expect(result).toHaveLength(1)
    expect(result[0].artifactType).toBe('csv')
  })

  it('filters report artifacts', () => {
    const result = filterArtifactsByType(artifacts, 'reports')
    expect(result).toHaveLength(1)
    expect(result[0].artifactType).toBe('report')
  })
})

// ==========================================
// Status Filtering
// ==========================================
describe('filterArtifactsByStatus', () => {
  const artifacts: ArtifactInfo[] = [
    makeArtifact({ status: 'live' }),
    makeArtifact({ status: 'extracted' }),
    makeArtifact({ status: 'lost' }),
  ]

  it('returns all when filter is "all"', () => {
    expect(filterArtifactsByStatus(artifacts, 'all')).toHaveLength(3)
  })

  it('filters available (live + extracted)', () => {
    const result = filterArtifactsByStatus(artifacts, 'available')
    expect(result).toHaveLength(2)
    expect(result.every(a => a.status !== 'lost')).toBe(true)
  })

  it('filters lost only', () => {
    const result = filterArtifactsByStatus(artifacts, 'lost')
    expect(result).toHaveLength(1)
    expect(result[0].status).toBe('lost')
  })
})

// ==========================================
// Search Filtering
// ==========================================
describe('filterArtifactsBySearch', () => {
  const artifacts: ArtifactInfo[] = [
    makeArtifact({ fileName: 'demographics.csv', label: 'Demographics Data' }),
    makeArtifact({ fileName: 'chart.png', label: 'Age Distribution' }),
    makeArtifact({ fileName: 'script.py', label: 'Analysis Script' }),
  ]

  it('returns all when search is empty', () => {
    expect(filterArtifactsBySearch(artifacts, '')).toHaveLength(3)
  })

  it('matches file name case-insensitively', () => {
    const result = filterArtifactsBySearch(artifacts, 'CHART')
    expect(result).toHaveLength(1)
    expect(result[0].fileName).toBe('chart.png')
  })

  it('matches label', () => {
    const result = filterArtifactsBySearch(artifacts, 'distribution')
    expect(result).toHaveLength(1)
    expect(result[0].label).toBe('Age Distribution')
  })

  it('returns empty array for no matches', () => {
    expect(filterArtifactsBySearch(artifacts, 'nonexistent')).toHaveLength(0)
  })
})

// ==========================================
// Language Detection
// ==========================================
describe('getLanguageFromFilename', () => {
  it('detects Python', () => {
    expect(getLanguageFromFilename('script.py')).toBe('Python')
  })

  it('detects JavaScript', () => {
    expect(getLanguageFromFilename('app.js')).toBe('JavaScript')
  })

  it('detects TypeScript', () => {
    expect(getLanguageFromFilename('index.ts')).toBe('TypeScript')
  })

  it('detects SQL', () => {
    expect(getLanguageFromFilename('query.sql')).toBe('SQL')
  })

  it('returns "Code" for unknown extension', () => {
    expect(getLanguageFromFilename('data.xyz')).toBe('Code')
  })

  it('handles .tsx files', () => {
    expect(getLanguageFromFilename('Component.tsx')).toBe('TypeScript')
  })
})

// ==========================================
// CSV Preview Parsing
// ==========================================
describe('parseCSVPreview', () => {
  it('parses basic CSV into columns and rows', () => {
    const csv = 'Name,Age,City\nAlice,30,London\nBob,25,Paris\nCarol,35,Berlin\nDave,28,Rome'
    const result = parseCSVPreview(csv, 3)
    expect(result.columns).toEqual(['Name', 'Age', 'City'])
    expect(result.rows).toHaveLength(3)
    expect(result.rows[0]).toEqual(['Alice', '30', 'London'])
    expect(result.totalRows).toBe(4)
  })

  it('handles CSV with fewer rows than limit', () => {
    const csv = 'A,B\n1,2'
    const result = parseCSVPreview(csv, 3)
    expect(result.rows).toHaveLength(1)
    expect(result.totalRows).toBe(1)
  })

  it('handles empty CSV', () => {
    const result = parseCSVPreview('', 3)
    expect(result.columns).toEqual([])
    expect(result.rows).toHaveLength(0)
    expect(result.totalRows).toBe(0)
  })

  it('handles quoted fields with commas', () => {
    const csv = 'Name,Description\n"Smith, John","A person"\nBob,Simple'
    const result = parseCSVPreview(csv, 3)
    expect(result.columns).toEqual(['Name', 'Description'])
    expect(result.rows[0][0]).toBe('Smith, John')
  })
})
