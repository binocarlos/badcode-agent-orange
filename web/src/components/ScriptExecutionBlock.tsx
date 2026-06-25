// Script execution block — terminal-style display of script runs.
// Copied from frontend/src/components/agent/ScriptExecutionBlock.tsx.
// Generic — no Platinum-specific dependencies.
// See ../../docs/90-provenance-map.md.

import React, { useState } from 'react'
import { Box, Typography, Chip } from '@mui/material'

const DEFAULT_VISIBLE_LINES = 20

interface ScriptExecutionBlockProps {
  scriptFile: string
  language: string
  fullCommand: string
  output?: string
  status: 'running' | 'complete' | 'error'
  elapsedSeconds?: number
}

export default function ScriptExecutionBlock({
  scriptFile,
  language,
  fullCommand,
  output,
  status,
  elapsedSeconds,
}: ScriptExecutionBlockProps) {
  const [expanded, setExpanded] = useState(false)

  const outputLines = output ? output.split('\n') : []
  const totalLines = outputLines.length
  const needsCollapse = totalLines > DEFAULT_VISIBLE_LINES
  const displayOutput = expanded || !needsCollapse
    ? output
    : outputLines.slice(0, DEFAULT_VISIBLE_LINES).join('\n')

  const statusIcon = status === 'running' ? '⏳' : status === 'error' ? '✗' : '✓'
  const statusColor = status === 'running' ? '#3b82f6' : status === 'error' ? '#9ca3af' : '#22c55e'
  const borderColor = '#e5e7eb'

  return (
    <Box
      data-testid="script-execution-block"
      sx={{
        border: `1px solid ${borderColor}`,
        borderRadius: '8px',
        overflow: 'hidden',
        my: 1,
      }}
    >
      <Box
        sx={{
          display: 'flex',
          alignItems: 'center',
          gap: 1,
          px: 1.5,
          py: 0.75,
          backgroundColor: '#f1f5f9',
          borderBottom: '1px solid #e5e7eb',
        }}
      >
        <Typography component="span" sx={{ fontSize: 14, color: statusColor }}>
          {statusIcon}
        </Typography>
        <Typography
          component="span"
          sx={{ fontWeight: 600, fontSize: 13, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}
        >
          {scriptFile}
        </Typography>
        <Chip
          label={language}
          size="small"
          sx={{
            height: 20,
            fontSize: 11,
            fontWeight: 500,
            backgroundColor: '#e2e8f0',
            color: '#475569',
          }}
        />
        {elapsedSeconds !== undefined && (
          <Typography sx={{ fontSize: 11, color: '#94a3b8', ml: 'auto' }}>
            {elapsedSeconds.toFixed(1)}s
          </Typography>
        )}
      </Box>

      <Box
        sx={{
          backgroundColor: '#1e1e1e',
          color: '#d4d4d4',
          fontFamily: '"Fira Code", "Consolas", monospace',
          fontSize: 12,
          lineHeight: 1.5,
          p: '8px 12px',
          overflow: 'auto',
          maxHeight: expanded ? 'none' : 400,
        }}
      >
        <Box component="pre" sx={{ m: 0, color: '#6a9955', whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>
          $ {fullCommand}
        </Box>

        {displayOutput && (
          <Box
            component="pre"
            sx={{
              m: 0,
              mt: 0.5,
              whiteSpace: 'pre-wrap',
              wordBreak: 'break-all',
              color: '#d4d4d4',
            }}
          >
            {displayOutput}
          </Box>
        )}

        {status === 'running' && !output && (
          <Typography sx={{ color: '#6a9955', fontSize: 12, mt: 0.5, fontFamily: 'inherit' }}>
            Running...
          </Typography>
        )}
      </Box>

      {needsCollapse && (
        <Box
          onClick={() => setExpanded(prev => !prev)}
          sx={{
            px: 1.5,
            py: 0.5,
            backgroundColor: '#1e1e1e',
            borderTop: '1px solid #333',
            textAlign: 'center',
            cursor: 'pointer',
            '&:hover': { backgroundColor: '#2a2a2a' },
          }}
        >
          <Typography sx={{ fontSize: 12, color: '#569cd6', fontWeight: 500 }}>
            {expanded ? 'Show less' : `Show all ${totalLines} lines`}
          </Typography>
        </Box>
      )}
    </Box>
  )
}
