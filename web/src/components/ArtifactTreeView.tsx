// Tree/folder view of artifacts.
// Copied from frontend/src/components/agent/ArtifactTreeView.tsx.
// Import paths updated: artifactTree/types → local. Added @untitledui/file-icons dependency.
// See ../../docs/90-provenance-map.md.

import { useState } from 'react'
import { Box, Collapse, Typography } from '@mui/material'
import {
  Folder as FolderIcon,
  FolderOpen as FolderOpenIcon,
  ExpandMore as ExpandMoreIcon,
  ChevronRight as ChevronRightIcon,
} from '@mui/icons-material'
import { FileIcon } from '@untitledui/file-icons'
import type { ArtifactTreeNode } from '../artifactTree.js'
import type { ArtifactInfo } from '../types.js'

const STATUS_DOT_COLORS: Record<string, string> = {
  live: 'success.main',
  extracted: 'primary.main',
  lost: 'text.disabled',
  extraction_failed: 'error.main',
}

interface ArtifactTreeViewProps {
  root: ArtifactTreeNode
  selectedPath?: string
  onSelect: (artifact: ArtifactInfo) => void
}

export default function ArtifactTreeView({ root, selectedPath, onSelect }: ArtifactTreeViewProps) {
  return (
    <Box sx={{ py: 0.5 }}>
      {root.children.map(child => (
        <TreeNode
          key={child.path}
          node={child}
          depth={0}
          selectedPath={selectedPath}
          onSelect={onSelect}
        />
      ))}
    </Box>
  )
}

function TreeNode({
  node,
  depth,
  selectedPath,
  onSelect,
}: {
  node: ArtifactTreeNode
  depth: number
  selectedPath?: string
  onSelect: (artifact: ArtifactInfo) => void
}) {
  const [expanded, setExpanded] = useState(true)
  const indent = depth * 12

  if (node.isDirectory) {
    return (
      <Box>
        <Box
          onClick={() => setExpanded(!expanded)}
          sx={{
            display: 'flex',
            alignItems: 'center',
            gap: 0.5,
            pl: `${indent + 4}px`,
            pr: 1,
            py: '3px',
            cursor: 'pointer',
            borderRadius: '4px',
            '&:hover': { backgroundColor: 'action.hover' },
          }}
        >
          {expanded ? (
            <ExpandMoreIcon sx={{ fontSize: 16, color: 'text.secondary' }} />
          ) : (
            <ChevronRightIcon sx={{ fontSize: 16, color: 'text.secondary' }} />
          )}
          {expanded ? (
            <FolderOpenIcon sx={{ fontSize: 16, color: 'warning.main' }} />
          ) : (
            <FolderIcon sx={{ fontSize: 16, color: 'warning.main' }} />
          )}
          <Typography sx={{ fontSize: 13, fontWeight: 500, color: 'text.primary' }}>
            {node.name}
          </Typography>
        </Box>
        <Collapse in={expanded}>
          {node.children.map(child => (
            <TreeNode
              key={child.path}
              node={child}
              depth={depth + 1}
              selectedPath={selectedPath}
              onSelect={onSelect}
            />
          ))}
        </Collapse>
      </Box>
    )
  }

  // File node
  const artifact = node.artifact!
  const isSelected = selectedPath === node.path
  const isLost = artifact.status === 'lost'
  const ext = node.name.split('.').pop()?.toLowerCase() || ''

  return (
    <Box
      onClick={() => onSelect(artifact)}
      sx={{
        display: 'flex',
        alignItems: 'center',
        gap: 0.5,
        pl: `${indent + 16}px`,
        pr: 1,
        py: '3px',
        cursor: 'pointer',
        borderRadius: '4px',
        backgroundColor: isSelected ? 'action.selected' : undefined,
        opacity: isLost ? 0.5 : 1,
        '&:hover': { backgroundColor: isSelected ? 'action.selected' : 'action.hover' },
      }}
    >
      <Box
        sx={{
          width: 7,
          height: 7,
          borderRadius: '50%',
          backgroundColor: STATUS_DOT_COLORS[artifact.status] || 'text.disabled',
          flexShrink: 0,
        }}
      />
      <FileIcon type={ext || 'document'} size={15} />
      <Typography
        sx={{
          fontSize: 13,
          color: isSelected ? 'primary.main' : 'text.primary',
          fontWeight: isSelected ? 600 : 400,
          overflow: 'hidden',
          textOverflow: 'ellipsis',
          whiteSpace: 'nowrap',
          textDecoration: isLost ? 'line-through' : 'none',
        }}
      >
        {node.name}
      </Typography>
    </Box>
  )
}
