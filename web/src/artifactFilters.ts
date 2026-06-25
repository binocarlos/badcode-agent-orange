import type { ArtifactInfo } from './types.js'

/**
 * Workspace-relative path the webapp iframe should load.
 *
 * Webapp artifacts are captured as a directory (the build folder, e.g. "dist"),
 * so `filePath` is the directory, not the entry file. The iframe must point at
 * the actual `index.html` inside it — and the URL must end at that file — so the
 * browser resolves the page's relative asset URLs (./assets/x.js) within the
 * webapp directory rather than dropping it. Legacy single-file webapp artifacts
 * (filePath already points at index.html) are returned unchanged.
 */
export function webappEntryRelPath(artifact: ArtifactInfo): string {
  const filePath = artifact.filePath.replace(/^\//, '')
  if (artifact.isDir) {
    return filePath.replace(/\/$/, '') + '/index.html'
  }
  return filePath
}

export type ArtifactTypeFilter = 'all' | 'images' | 'code' | 'data' | 'reports'
export type ArtifactStatusFilter = 'all' | 'available' | 'lost'

export function filterArtifactsByType(artifacts: ArtifactInfo[], filter: ArtifactTypeFilter): ArtifactInfo[] {
  if (filter === 'all') return artifacts
  switch (filter) {
    case 'images':
      return artifacts.filter(a => a.artifactType === 'image' || a.artifactType === 'chart')
    case 'code':
      return artifacts.filter(a => a.artifactType === 'code')
    case 'data':
      return artifacts.filter(a => a.artifactType === 'csv')
    case 'reports':
      return artifacts.filter(a => a.artifactType === 'report')
    default:
      return artifacts
  }
}

export function filterArtifactsByStatus(artifacts: ArtifactInfo[], filter: ArtifactStatusFilter): ArtifactInfo[] {
  if (filter === 'all') return artifacts
  if (filter === 'available') return artifacts.filter(a => a.status !== 'lost')
  if (filter === 'lost') return artifacts.filter(a => a.status === 'lost')
  return artifacts
}

export function filterArtifactsBySearch(artifacts: ArtifactInfo[], search: string): ArtifactInfo[] {
  if (!search.trim()) return artifacts
  const term = search.toLowerCase()
  return artifacts.filter(a =>
    a.fileName.toLowerCase().includes(term) ||
    a.label.toLowerCase().includes(term)
  )
}

const LANGUAGE_MAP: Record<string, string> = {
  py: 'Python',
  js: 'JavaScript',
  jsx: 'JavaScript',
  ts: 'TypeScript',
  tsx: 'TypeScript',
  sql: 'SQL',
  sh: 'Shell',
  bash: 'Shell',
  r: 'R',
  rb: 'Ruby',
  go: 'Go',
  rs: 'Rust',
  java: 'Java',
  cs: 'C#',
  css: 'CSS',
  html: 'HTML',
  json: 'JSON',
  yaml: 'YAML',
  yml: 'YAML',
  xml: 'XML',
  md: 'Markdown',
}

export function getLanguageFromFilename(fileName: string): string {
  const ext = fileName.split('.').pop()?.toLowerCase() || ''
  return LANGUAGE_MAP[ext] || 'Code'
}

export interface CSVPreviewResult {
  columns: string[]
  rows: string[][]
  totalRows: number
}

export function parseCSVPreview(content: string, maxRows: number): CSVPreviewResult {
  if (!content.trim()) {
    return { columns: [], rows: [], totalRows: 0 }
  }

  const lines = parseCSVLines(content)
  if (lines.length === 0) {
    return { columns: [], rows: [], totalRows: 0 }
  }

  const columns = lines[0]
  const dataLines = lines.slice(1)
  const rows = dataLines.slice(0, maxRows)

  return {
    columns,
    rows,
    totalRows: dataLines.length,
  }
}

function parseCSVLines(content: string): string[][] {
  const results: string[][] = []
  let current = ''
  let inQuotes = false
  let row: string[] = []

  for (let i = 0; i < content.length; i++) {
    const ch = content[i]

    if (inQuotes) {
      if (ch === '"') {
        if (i + 1 < content.length && content[i + 1] === '"') {
          current += '"'
          i++ // skip escaped quote
        } else {
          inQuotes = false
        }
      } else {
        current += ch
      }
    } else {
      if (ch === '"') {
        inQuotes = true
      } else if (ch === ',') {
        row.push(current)
        current = ''
      } else if (ch === '\n') {
        row.push(current)
        current = ''
        if (row.length > 0) results.push(row)
        row = []
      } else if (ch === '\r') {
        // skip
      } else {
        current += ch
      }
    }
  }

  // push last field/row
  row.push(current)
  if (row.some(cell => cell.length > 0)) {
    results.push(row)
  }

  return results
}
