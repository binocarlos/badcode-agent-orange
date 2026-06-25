// Artifact panel component — sidebar listing artifacts and todos.
// Copied + factored from frontend/src/components/agent/ArtifactPanel.tsx.
// Factoring changes:
//   - Removed ArtifactViewer dependency; replaced with onOpenViewer callback prop.
//   - Removed ArtifactPreviewDialog, ArtifactTreeView dependencies; inline tree.
//   - Removed @ aliases; all imports local.
//   - onPinToDashboard is optional prop (Platinum-specific callback).
//   - Removed the list/tree view toggle; the panel always renders the tree.
// See ../../docs/90-provenance-map.md.

import React, { useCallback, useMemo } from 'react'
import { Box, Typography, IconButton } from '@mui/material'
import {
  ArrowForward as ArrowIcon,
  PushPin as PinIcon,
  CheckCircle as CheckCircleIcon,
  RadioButtonUnchecked as UncheckedIcon,
  FiberManualRecord as DotIcon,
} from '@mui/icons-material'
import type { ArtifactInfo, TodoItem } from '../types.js'

interface ArtifactPanelProps {
  artifacts: ArtifactInfo[]
  todos?: TodoItem[]
  sessionId: string
  customer?: string
  /** Called when user clicks pin-to-dashboard (Platinum-specific). */
  onPinToDashboard?: (sessionId: string) => void
  /** Called when user clicks "View all artifacts". */
  onViewAll?: () => void
  /** Called when an artifact is clicked for preview. */
  onArtifactClick?: (artifact: ArtifactInfo) => void
  /** API base URL for download links. Default: empty string. */
  apiBaseUrl?: string
  /** Auth header for downloads. */
  authHeader?: string
}

const STATUS_DOT_COLORS: Record<string, string> = {
  live: '#16a34a',
  extracted: '#2563eb',
  lost: '#9ca3af',
}

function TodoStatusIcon({ status }: { status: TodoItem['status'] }) {
  if (status === 'completed') return <CheckCircleIcon sx={{ fontSize: 16, color: '#16a34a' }} />
  if (status === 'in_progress') return <DotIcon sx={{ fontSize: 16, color: '#2563eb', '@keyframes pulse': { '0%, 100%': { opacity: 1 }, '50%': { opacity: 0.4 } }, animation: 'pulse 1.5s ease-in-out infinite' }} />
  return <UncheckedIcon sx={{ fontSize: 16, color: '#9ca3af' }} />
}

// Simple flat list tree view (no external ArtifactTreeView dependency)
function ArtifactFlatTree({ artifacts, onSelect }: { artifacts: ArtifactInfo[]; onSelect: (a: ArtifactInfo) => void }) {
  // Group by directory
  const grouped = useMemo(() => {
    const map = new Map<string, ArtifactInfo[]>()
    for (const a of artifacts) {
      const parts = a.filePath.split('/')
      const dir = parts.length > 1 ? parts.slice(0, -1).join('/') : '/'
      const existing = map.get(dir) || []
      existing.push(a)
      map.set(dir, existing)
    }
    return map
  }, [artifacts])

  return (
    <Box>
      {Array.from(grouped.entries()).map(([dir, items]) => (
        <Box key={dir}>
          {dir !== '/' && (
            <Typography sx={{ fontSize: 11, color: '#9ca3af', px: 1, py: 0.25, fontFamily: 'monospace' }}>
              {dir}/
            </Typography>
          )}
          {items.map(a => (
            <Box
              key={a.filePath}
              data-testid="artifact-entry"
              data-lost={a.status === 'lost' ? 'true' : undefined}
              onClick={() => onSelect(a)}
              sx={{
                px: dir !== '/' ? 2 : 1,
                py: 0.5,
                cursor: 'pointer',
                display: 'flex',
                alignItems: 'center',
                gap: 0.5,
                opacity: a.status === 'lost' ? 0.6 : 1,
                '&:hover': { backgroundColor: 'action.hover' },
              }}
            >
              <Box sx={{ width: 6, height: 6, borderRadius: '50%', backgroundColor: STATUS_DOT_COLORS[a.status] || '#9ca3af', flexShrink: 0 }} />
              <Typography sx={{ fontSize: 12, color: '#374151', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', textDecoration: a.status === 'lost' ? 'line-through' : 'none' }}>
                {a.fileName}
              </Typography>
            </Box>
          ))}
        </Box>
      ))}
    </Box>
  )
}

export default function ArtifactPanel({
  artifacts,
  todos,
  sessionId,
  onPinToDashboard,
  onViewAll,
  onArtifactClick,
}: ArtifactPanelProps) {
  const handleArtifactClick = useCallback((artifact: ArtifactInfo) => {
    if (onArtifactClick) onArtifactClick(artifact)
  }, [onArtifactClick])

  const handleViewAll = useCallback(() => {
    if (onViewAll) onViewAll()
  }, [onViewAll])

  const hasTodos = todos && todos.length > 0
  if (artifacts.length === 0 && !hasTodos) return null
  const completedCount = hasTodos ? todos.filter(t => t.status === 'completed').length : 0

  return (
    <Box
      sx={{
        width: 320,
        borderLeft: '1px solid #e5e7eb',
        backgroundColor: '#f9fafb',
        display: 'flex',
        flexDirection: 'column',
        overflow: 'hidden',
      }}
    >
      {hasTodos && (
        <Box sx={{ p: '8px 12px', borderBottom: '1px solid #e5e7eb' }}>
          <Typography sx={{ fontWeight: 600, fontSize: 14, mb: 0.75 }}>
            Tasks ({completedCount}/{todos.length})
          </Typography>
          {todos.map((todo, idx) => (
            <Box key={idx} sx={{ display: 'flex', alignItems: 'flex-start', gap: 0.75, mb: 0.5 }}>
              <TodoStatusIcon status={todo.status} />
              <Typography sx={{
                fontSize: 12,
                color: todo.status === 'completed' ? '#9ca3af' : '#374151',
                textDecoration: todo.status === 'completed' ? 'line-through' : 'none',
                lineHeight: 1.4,
              }}>
                {todo.content}
              </Typography>
            </Box>
          ))}
        </Box>
      )}
      <Box sx={{ p: '8px 12px', borderBottom: '1px solid #e5e7eb', display: 'flex', alignItems: 'center', gap: 1 }}>
        <Typography sx={{ fontWeight: 600, fontSize: 14, flex: 1 }}>
          Artifacts ({artifacts.length})
        </Typography>
        {onPinToDashboard && (
          <IconButton
            size="small"
            onClick={() => onPinToDashboard(sessionId)}
            title="Pin to dashboard"
            aria-label="pin to dashboard"
            sx={{ color: '#6b7280' }}
          >
            <PinIcon sx={{ fontSize: 16 }} />
          </IconButton>
        )}
      </Box>
      <Box sx={{ flex: 1, overflow: 'auto', p: 0.5 }}>
        <ArtifactFlatTree artifacts={artifacts} onSelect={handleArtifactClick} />
      </Box>
      <Box
        onClick={handleViewAll}
        sx={{
          p: '8px 16px',
          borderTop: '1px solid #e5e7eb',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          cursor: 'pointer',
          '&:hover': { backgroundColor: '#f1f5f9' },
        }}
      >
        <Typography sx={{ fontSize: 12, color: '#2563eb', fontWeight: 500 }}>View all artifacts</Typography>
        <ArrowIcon sx={{ fontSize: 14, color: '#2563eb', ml: 0.5 }} />
      </Box>
    </Box>
  )
}
