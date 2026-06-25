// Collapsible thinking/reasoning block shown above assistant message content.
// Extracted from AgentChat.tsx (was inline there) to enable reuse.
// Generic — no Platinum-specific dependencies.
// See ../../docs/09-frontend-components.md.

import React, { useState, useRef } from 'react'
import { Box, Typography, Collapse } from '@mui/material'

interface ThinkingBlockProps {
  content: string
  isActivelyThinking?: boolean
}

/** Collapsible thinking/reasoning block shown above assistant message content */
export default function ThinkingBlock({ content, isActivelyThinking }: ThinkingBlockProps) {
  const [expanded, setExpanded] = useState(false)
  const contentRef = useRef<HTMLDivElement>(null)

  // Default to collapsed once thinking finishes — user can expand manually
  const isOpen = isActivelyThinking || expanded

  // Single-line preview: strip newlines and truncate
  const preview = content.replace(/\n/g, ' ').trim()
  const previewTruncated = preview.length > 100 ? preview.slice(0, 100) + '...' : preview

  // Auto-scroll to bottom while actively thinking
  if (isActivelyThinking && contentRef.current) {
    const el = contentRef.current
    const isNearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 80
    if (isNearBottom) {
      el.scrollTop = el.scrollHeight
    }
  }

  return (
    <Box
      sx={{
        maxWidth: '80%',
        mb: 0.5,
        borderRadius: 0,
        border: '1px solid',
        borderColor: 'divider',
        borderLeft: '3px solid',
        borderLeftColor: 'rgba(0, 178, 255, 0.3)',
        overflow: 'hidden',
      }}
    >
      <Box
        onClick={() => !isActivelyThinking && setExpanded(!expanded)}
        sx={{
          px: '10px',
          py: '5px',
          display: 'flex',
          alignItems: 'center',
          gap: 0,
          cursor: !isActivelyThinking ? 'pointer' : 'default',
          userSelect: 'none',
          '&:hover': !isActivelyThinking ? { backgroundColor: '#f5f5f5' } : {},
        }}
      >
        {isActivelyThinking && (
          <Box
            component="span"
            sx={{
              display: 'inline-block',
              width: 14,
              height: 14,
              flexShrink: 0,
              marginRight: '6px',
              border: '2px solid rgba(0,178,255,0.5)',
              borderTopColor: '#00B2FF',
              borderRadius: '50%',
              animation: 'spin 0.8s linear infinite',
              '@keyframes spin': { '100%': { transform: 'rotate(360deg)' } },
            }}
          />
        )}
        <Typography variant="caption" sx={{ color: '#00B2FF', fontWeight: 600, fontSize: 11, flexShrink: 0 }}>
          Reasoning
        </Typography>
        {!isOpen && (
          <Typography
            variant="caption"
            sx={{
              color: '#bbb',
              fontSize: 11,
              mx: '6px',
              overflow: 'hidden',
              textOverflow: 'ellipsis',
              whiteSpace: 'nowrap',
              minWidth: 0,
            }}
          >
            | {previewTruncated}
          </Typography>
        )}
        {!isActivelyThinking && (
          <Typography variant="caption" sx={{ color: '#bbb', fontSize: 13, ml: 'auto', flexShrink: 0, pl: '8px' }}>
            {expanded ? '−' : '+'}
          </Typography>
        )}
      </Box>
      <Collapse in={isOpen}>
        <Box
          ref={contentRef}
          sx={{
            px: '10px',
            pb: '8px',
            fontSize: 12,
            color: '#9e9e9e',
            lineHeight: 1.5,
            whiteSpace: 'pre-wrap',
            ...(isActivelyThinking ? { maxHeight: 200, overflowY: 'auto' } : {}),
          }}
        >
          {content}
        </Box>
      </Collapse>
    </Box>
  )
}
