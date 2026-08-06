// CSV preview component — renders a compact table from CSV text.
// Copied from frontend/src/components/agent/ArtifactCsvPreview.tsx.
// Import paths updated: artifactFilters → local.
// See ../../docs/90-provenance-map.md.

import { Box, Typography } from '@mui/material'
import { parseCSVPreview } from '../artifactFilters.js'

interface ArtifactCsvPreviewProps {
  csvContent: string
  maxRows?: number
}

export default function ArtifactCsvPreview({ csvContent, maxRows = 3 }: ArtifactCsvPreviewProps) {
  const { columns, rows, totalRows } = parseCSVPreview(csvContent, maxRows)

  if (columns.length === 0) return null

  return (
    <Box sx={{ border: '1px solid', borderColor: 'divider', borderRadius: '6px', overflow: 'hidden' }}>
      <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, px: 1.5, py: 0.5, borderBottom: '1px solid', borderColor: 'divider', backgroundColor: 'action.hover' }}>
        <Typography sx={{ fontSize: 11, color: 'text.secondary', fontWeight: 500 }}>
          {totalRows} rows
        </Typography>
        <Typography sx={{ fontSize: 11, color: 'text.disabled' }}>
          {columns.length} columns
        </Typography>
      </Box>
      <Box sx={{ overflow: 'auto' }}>
        <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 12 }}>
          <thead>
            <tr>
              {columns.map((col, i) => (
                <th
                  key={i}
                  style={{
                    textAlign: 'left',
                    padding: '4px 8px',
                    borderBottom: '1px solid', borderColor: 'divider',
                    backgroundColor: 'action.hover',
                    fontWeight: 600,
                    fontSize: 11,
                    color: 'text.primary',
                    whiteSpace: 'nowrap',
                  }}
                >
                  {col}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {rows.map((row, ri) => (
              <tr key={ri}>
                {row.map((cell, ci) => (
                  <td
                    key={ci}
                    style={{
                      padding: '3px 8px',
                      borderBottom: '1px solid', borderColor: 'divider',
                      fontSize: 11,
                      color: 'text.secondary',
                      whiteSpace: 'nowrap',
                    }}
                  >
                    {cell}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </Box>
      {totalRows > maxRows && (
        <Box sx={{ px: 1.5, py: 0.5, borderTop: '1px solid', borderColor: 'divider', textAlign: 'center' }}>
          <Typography sx={{ fontSize: 11, color: 'text.disabled' }}>
            +{totalRows - maxRows} more rows
          </Typography>
        </Box>
      )}
    </Box>
  )
}
