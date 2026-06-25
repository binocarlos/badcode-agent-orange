// Code-created block — shows syntax-highlighted file contents.
// Copied from frontend/src/components/agent/CodeCreatedBlock.tsx.
// Uses prism-react-renderer — falls back to plain pre if unavailable.
// Generic — no Platinum-specific dependencies.
// See ../../docs/90-provenance-map.md.

import React, { useState, useCallback } from 'react'
import { Box, Typography, Chip, IconButton } from '@mui/material'
import ContentCopyIcon from '@mui/icons-material/ContentCopy'
import CheckIcon from '@mui/icons-material/Check'

const DEFAULT_VISIBLE_LINES = 20

interface CodeCreatedBlockProps {
  filePath: string
  code: string
}

function getLanguageFromFilename(fileName: string): string {
  const ext = fileName.split('.').pop()?.toLowerCase() || ''
  const map: Record<string, string> = {
    ts: 'TypeScript', tsx: 'TypeScript', js: 'JavaScript', jsx: 'JavaScript',
    py: 'Python', rs: 'Rust', go: 'Go', java: 'Java', cs: 'C#',
    cpp: 'C++', c: 'C', rb: 'Ruby', php: 'PHP', swift: 'Swift',
    kt: 'Kotlin', scala: 'Scala', sh: 'Shell', bash: 'Shell',
    html: 'HTML', css: 'CSS', json: 'JSON', yaml: 'YAML', yml: 'YAML',
    md: 'Markdown', sql: 'SQL', r: 'R',
  }
  return map[ext] || ext.toUpperCase() || 'Text'
}

export default function CodeCreatedBlock({ filePath, code }: CodeCreatedBlockProps) {
  const [expanded, setExpanded] = useState(false)
  const [copied, setCopied] = useState(false)

  const fileName = filePath.split('/').pop() || filePath
  const language = getLanguageFromFilename(fileName)
  const lines = code.split('\n')
  const totalLines = lines.length
  const needsCollapse = totalLines > DEFAULT_VISIBLE_LINES
  const displayCode = expanded || !needsCollapse
    ? code
    : lines.slice(0, DEFAULT_VISIBLE_LINES).join('\n')

  const handleCopy = useCallback(() => {
    navigator.clipboard.writeText(code).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    })
  }, [code])

  return (
    <Box
      data-testid="code-created-block"
      sx={{
        border: '1px solid #e5e7eb',
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
        <Typography component="span" sx={{ fontSize: 14 }}>
          📄
        </Typography>
        <Typography
          component="span"
          sx={{ fontWeight: 600, fontSize: 13, flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}
        >
          {fileName}
        </Typography>
        <Chip
          label={language}
          size="small"
          data-testid="language-badge"
          sx={{
            height: 20,
            fontSize: 11,
            fontWeight: 500,
            backgroundColor: '#e2e8f0',
            color: '#475569',
          }}
        />
        <Typography sx={{ fontSize: 11, color: '#94a3b8' }}>
          {totalLines} lines
        </Typography>
        <IconButton
          size="small"
          onClick={handleCopy}
          title="Copy code"
          aria-label="copy code"
          sx={{ color: copied ? '#22c55e' : '#6b7280', ml: 0.5 }}
        >
          {copied ? <CheckIcon sx={{ fontSize: 16 }} /> : <ContentCopyIcon sx={{ fontSize: 16 }} />}
        </IconButton>
      </Box>

      <Box
        component="pre"
        sx={{
          m: 0,
          p: '8px 12px',
          fontSize: 12,
          lineHeight: 1.5,
          fontFamily: '"Fira Code", "Consolas", monospace',
          overflow: 'auto',
          maxHeight: expanded ? 'none' : 400,
          backgroundColor: '#fafafa',
          whiteSpace: 'pre',
        }}
      >
        {displayCode}
      </Box>

      {needsCollapse && (
        <Box
          onClick={() => setExpanded(prev => !prev)}
          sx={{
            px: 1.5,
            py: 0.5,
            borderTop: '1px solid #e5e7eb',
            textAlign: 'center',
            cursor: 'pointer',
            '&:hover': { backgroundColor: '#f9fafb' },
          }}
        >
          <Typography sx={{ fontSize: 12, color: '#2563eb', fontWeight: 500 }}>
            {expanded ? 'Show less' : `Show all ${totalLines} lines`}
          </Typography>
        </Box>
      )}
    </Box>
  )
}
