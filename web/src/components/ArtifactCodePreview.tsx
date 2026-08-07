// Code preview component with syntax highlighting.
// Copied from frontend/src/components/agent/ArtifactCodePreview.tsx.
// Import paths updated: artifactFilters/prismLanguage → local.
// Dependency added: prism-react-renderer.
// See ../../docs/90-provenance-map.md.

import { Box, Chip, Typography, useTheme } from '@mui/material'
import { Highlight, themes } from 'prism-react-renderer'
import { getLanguageFromFilename } from '../artifactFilters.js'
import { getPrismLanguage } from '../prismLanguage.js'

interface ArtifactCodePreviewProps {
  code: string
  fileName: string
  previewLines?: number
  expanded?: boolean
}

export default function ArtifactCodePreview({
  code,
  fileName,
  previewLines = 5,
  expanded = false,
}: ArtifactCodePreviewProps) {
  const muiTheme = useTheme()
  const lines = code.split('\n')
  const totalLines = lines.length
  const language = getLanguageFromFilename(fileName)
  const prismLang = getPrismLanguage(fileName)
  const displayCode = expanded ? code : lines.slice(0, previewLines).join('\n')

  return (
    <Box sx={{ border: '1px solid', borderColor: 'divider', borderRadius: '6px', overflow: 'hidden', backgroundColor: 'action.hover' }}>
      <Box sx={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', px: 1.5, py: 0.5, borderBottom: '1px solid', borderColor: 'divider', backgroundColor: 'action.hover' }}>
        <Chip
          label={language}
          size="small"
          sx={{ height: 20, fontSize: 11, fontWeight: 500, backgroundColor: 'divider', color: 'text.secondary' }}
        />
        <Typography sx={{ fontSize: 11, color: 'text.disabled' }}>
          {totalLines} lines
        </Typography>
      </Box>
      <Highlight theme={muiTheme.palette.mode === 'dark' ? themes.vsDark : themes.vsLight} code={displayCode} language={prismLang}>
        {({ style, tokens, getLineProps, getTokenProps }) => (
          <pre
            style={{
              ...style,
              margin: 0,
              padding: '8px 12px',
              fontSize: 12,
              lineHeight: 1.5,
              fontFamily: '"Fira Code", "Consolas", monospace',
              overflow: 'hidden',
              whiteSpace: 'pre-wrap',
              wordBreak: 'break-all',
            }}
          >
            {tokens.map((line, i) => (
              <div key={i} {...getLineProps({ line })}>
                {line.map((token, key) => (
                  <span key={key} {...getTokenProps({ token })} />
                ))}
              </div>
            ))}
          </pre>
        )}
      </Highlight>
      {!expanded && totalLines > previewLines && (
        <Box sx={{ px: 1.5, py: 0.5, borderTop: '1px solid', borderColor: 'divider', textAlign: 'center' }}>
          <Typography sx={{ fontSize: 11, color: 'text.disabled' }}>
            +{totalLines - previewLines} more lines
          </Typography>
        </Box>
      )}
    </Box>
  )
}
