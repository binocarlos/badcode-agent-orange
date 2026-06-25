// Grid view of artifacts with type filters and search.
// Copied from frontend/src/components/agent/ArtifactGrid.tsx.
// Import paths updated: types/artifactFilters → local.
// See ../../docs/90-provenance-map.md.

import React, { useState } from 'react'
import { Box, Button, Checkbox, Chip, TextField, Typography } from '@mui/material'
import {
  Download as DownloadIcon,
  InsertDriveFile as FileIcon,
  Image as ImageIcon,
  Code as CodeIcon,
  TableChart as CsvIcon,
  BarChart as ChartIcon,
  Description as ReportIcon,
} from '@mui/icons-material'
import type { ArtifactInfo } from '../types.js'
import {
  ArtifactTypeFilter,
  filterArtifactsByType,
  filterArtifactsBySearch,
} from '../artifactFilters.js'

interface ArtifactGridProps {
  artifacts: ArtifactInfo[]
  sessionId: string
  onSelect: (artifactId: string) => void
  selectable?: boolean
}

const TYPE_FILTERS: { label: string; value: ArtifactTypeFilter }[] = [
  { label: 'All', value: 'all' },
  { label: 'Images', value: 'images' },
  { label: 'Code', value: 'code' },
  { label: 'Data', value: 'data' },
  { label: 'Reports', value: 'reports' },
]

const ARTIFACT_ICONS: Record<string, React.ElementType> = {
  file: FileIcon,
  image: ImageIcon,
  code: CodeIcon,
  csv: CsvIcon,
  chart: ChartIcon,
  report: ReportIcon,
}

export default function ArtifactGrid({ artifacts, sessionId: _sessionId, onSelect, selectable }: ArtifactGridProps) {
  const [typeFilter, setTypeFilter] = useState<ArtifactTypeFilter>('all')
  const [search, setSearch] = useState('')
  const [selected, setSelected] = useState<Set<string>>(new Set())

  const filtered = filterArtifactsBySearch(
    filterArtifactsByType(artifacts, typeFilter),
    search
  )

  const toggleSelect = (id: string) => {
    setSelected(prev => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  return (
    <Box>
      {/* Toolbar */}
      <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mb: 2, flexWrap: 'wrap' }}>
        {TYPE_FILTERS.map(f => (
          <Button
            key={f.value}
            size="small"
            variant={typeFilter === f.value ? 'contained' : 'outlined'}
            color="info"
            onClick={() => setTypeFilter(f.value)}
            sx={{ textTransform: 'none', minWidth: 'auto', fontSize: 12 }}
          >
            {f.label}
          </Button>
        ))}
        <TextField
          size="small"
          placeholder="Search artifacts..."
          value={search}
          onChange={e => setSearch(e.target.value)}
          sx={{ ml: 'auto', minWidth: 180, '& .MuiInputBase-input': { fontSize: 13, py: '6px' } }}
        />
        <Button
          size="small"
          variant="outlined"
          color="info"
          startIcon={<DownloadIcon sx={{ fontSize: 16 }} />}
          sx={{ textTransform: 'none', fontSize: 12 }}
        >
          Download All
        </Button>
      </Box>

      {/* Grid */}
      {filtered.length === 0 ? (
        <Box sx={{ textAlign: 'center', py: 6, color: '#9ca3af' }}>
          <Typography sx={{ fontSize: 14 }}>No artifacts found</Typography>
        </Box>
      ) : (
        <Box
          sx={{
            display: 'grid',
            gridTemplateColumns: 'repeat(auto-fill, minmax(220px, 1fr))',
            gap: 2,
          }}
        >
          {filtered.map(artifact => {
            const Icon = ARTIFACT_ICONS[artifact.artifactType] || FileIcon
            const id = artifact.id || artifact.filePath

            return (
              <Box
                key={id}
                onClick={() => onSelect(artifact.id || artifact.filePath)}
                sx={{
                  border: '1px solid #e5e7eb',
                  borderRadius: '8px',
                  p: 1.5,
                  cursor: 'pointer',
                  backgroundColor: 'white',
                  '&:hover': { borderColor: '#3b82f6', backgroundColor: '#f8fafc' },
                  position: 'relative',
                }}
              >
                {selectable && (
                  <Checkbox
                    size="small"
                    checked={selected.has(id)}
                    onChange={(e) => {
                      e.stopPropagation()
                      toggleSelect(id)
                    }}
                    onClick={e => e.stopPropagation()}
                    sx={{ position: 'absolute', top: 4, right: 4, p: 0.5 }}
                  />
                )}
                <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mb: 1 }}>
                  <Icon sx={{ fontSize: 20, color: '#6b7280' }} />
                  <Typography sx={{ fontSize: 13, fontWeight: 600, flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                    {artifact.label}
                  </Typography>
                </Box>
                <Box sx={{ display: 'flex', alignItems: 'center', gap: 0.5 }}>
                  <Chip
                    label={artifact.artifactType}
                    size="small"
                    sx={{ height: 18, fontSize: 10, fontWeight: 500 }}
                  />
                  {artifact.fileSize && (
                    <Typography sx={{ fontSize: 11, color: '#9ca3af' }}>
                      {(artifact.fileSize / 1024).toFixed(1)} KB
                    </Typography>
                  )}
                </Box>
              </Box>
            )
          })}
        </Box>
      )}
    </Box>
  )
}
