// Preview dialog for a single artifact — code, image, CSV, markdown.
// Copied from frontend/src/components/agent/ArtifactPreviewDialog.tsx.
// Factoring changes:
//   - Removed axios dependency; replaced axios.defaults auth header read with `authHeader` prop.
//   - Removed API_BASE_URL import; replaced with `apiBaseUrl` prop (default '/api/v1').
//   - Import paths updated: types/artifactFilters/prismLanguage → local.
// See ../../docs/90-provenance-map.md.

import React, { useState, useEffect, useCallback, type ReactNode } from 'react'
import {
  Dialog,
  DialogTitle,
  DialogContent,
  Box,
  Typography,
  IconButton,
  CircularProgress,
  Table,
  TableHead,
  TableBody,
  TableRow,
  TableCell,
  Chip,
} from '@mui/material'
import {
  Close as CloseIcon,
  Download as DownloadIcon,
} from '@mui/icons-material'
import { Highlight, themes } from 'prism-react-renderer'
import { ArtifactInfo } from '../types.js'
import type { PlatinumArtifactData } from '../platinumArtifact.js'
import { tryParsePlatinumJson } from '../platinumArtifact.js'
import { getLanguageFromFilename, parseCSVPreview } from '../artifactFilters.js'
import { getPrismLanguage } from '../prismLanguage.js'
import AgentMarkdown from './AgentMarkdown.js'

interface ArtifactPreviewDialogProps {
  artifact: ArtifactInfo | null
  sessionId: string
  onClose: () => void
  /** API base URL for fetching artifact content. Default: '/api/v1'. */
  apiBaseUrl?: string
  /** Authorization header value (e.g. 'Bearer <token>'). */
  authHeader?: string
  /**
   * Render Platinum table/chart JSON artifacts inline (same seam as ArtifactViewer).
   * If not provided, JSON artifacts that match the Platinum pattern fall back to
   * pretty-printed text.
   */
  renderPlatinumData?: (data: PlatinumArtifactData) => ReactNode
}

type ContentState =
  | { status: 'idle' }
  | { status: 'loading' }
  | { status: 'loaded'; text?: string; blobUrl?: string; iframeUrl?: string }
  | { status: 'error'; message: string }

const officeExtensions = ['.pptx', '.xlsx', '.docx']
const isOfficeFile = (fileName: string) => officeExtensions.some(ext => fileName.toLowerCase().endsWith(ext))

// Pretty-print JSON content for the raw-text preview. Falls back to the original
// text if it isn't valid JSON (or the filename isn't .json), so non-JSON files
// still render verbatim.
function formatMaybeJson(text: string, fileName: string): string {
  if (!fileName.toLowerCase().endsWith('.json')) return text
  try {
    return JSON.stringify(JSON.parse(text), null, 2)
  } catch {
    return text
  }
}

export default function ArtifactPreviewDialog({
  artifact,
  sessionId,
  onClose,
  apiBaseUrl = '/api/v1',
  authHeader,
  renderPlatinumData,
}: ArtifactPreviewDialogProps) {
  const [content, setContent] = useState<ContentState>({ status: 'idle' })

  const fetchContent = useCallback(async (art: ArtifactInfo) => {
    if (art.status === 'lost') {
      setContent({ status: 'error', message: 'This artifact is no longer available (session workspace was cleaned up).' })
      return
    }

    // Inline artifacts (e.g. screenshot_url results) carry their bytes as a data:
    // URL — there is no backing file to fetch. Render it directly; fetching would
    // hit a non-existent workspace path and 404.
    if (art.downloadUrl?.startsWith('data:')) {
      setContent({ status: 'loaded', blobUrl: art.downloadUrl })
      return
    }

    setContent({ status: 'loading' })
    const filePath = art.filePath.replace(/^\//, '')
    const url = art.id
      ? `${apiBaseUrl}/agent/artifacts/${art.id}/download`
      : `${apiBaseUrl}/agent/session/${sessionId}/workspace/files/${filePath}`
    const headers: Record<string, string> = {}
    if (authHeader) headers['Authorization'] = authHeader

    try {
      const isImage = art.artifactType === 'image' || art.artifactType === 'chart'

      // Office files: use Microsoft Office Online Viewer via backend SAS URL
      if (art.id && isOfficeFile(art.fileName)) {
        const res = await fetch(`${apiBaseUrl}/agent/artifacts/${art.id}/preview-url`, { headers })
        if (res.ok) {
          const data = await res.json()
          setContent({ status: 'loaded', iframeUrl: data.previewUrl })
          return
        }
        // Fall through to download if preview URL fails
      }

      if (isImage) {
        const res = await fetch(url, { headers })
        if (!res.ok) throw new Error(`HTTP ${res.status}`)
        const blob = await res.blob()
        setContent({ status: 'loaded', blobUrl: URL.createObjectURL(blob) })
      } else {
        const res = await fetch(url, { headers })
        if (!res.ok) throw new Error(`HTTP ${res.status}`)
        const text = await res.text()
        setContent({ status: 'loaded', text })
      }
    } catch (err) {
      setContent({ status: 'error', message: `Failed to load artifact: ${err instanceof Error ? err.message : 'Unknown error'}` })
    }
  }, [sessionId, apiBaseUrl, authHeader])

  useEffect(() => {
    if (artifact) {
      fetchContent(artifact)
    } else {
      setContent({ status: 'idle' })
    }
    return () => {
      // Revoke blob URL on cleanup
      if (content.status === 'loaded' && content.blobUrl) {
        URL.revokeObjectURL(content.blobUrl)
      }
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [artifact, fetchContent])

  const handleDownload = useCallback(() => {
    if (!artifact) return
    const downloadUrl = artifact.id
      ? `${apiBaseUrl}/agent/artifacts/${artifact.id}/download`
      : `${apiBaseUrl}/agent/session/${sessionId}/workspace/files/${artifact.filePath.replace(/^\//, '')}`

    const headers: Record<string, string> = {}
    if (authHeader) headers['Authorization'] = authHeader
    fetch(downloadUrl, { headers })
      .then(res => res.blob())
      .then(blob => {
        const url = URL.createObjectURL(blob)
        const a = document.createElement('a')
        a.href = url
        a.download = artifact.fileName
        a.click()
        URL.revokeObjectURL(url)
      })
      .catch(err => console.error('Download failed:', err))
  }, [artifact, sessionId, apiBaseUrl, authHeader])

  if (!artifact) return null

  const isMarkdown = artifact.fileName.endsWith('.md')

  return (
    <Dialog
      open={!!artifact}
      onClose={onClose}
      maxWidth="md"
      fullWidth
      PaperProps={{ sx: { maxHeight: '80vh' } }}
    >
      <DialogTitle sx={{ display: 'flex', alignItems: 'center', gap: 1, py: 1.5, pr: 1 }}>
        <Typography sx={{ fontWeight: 600, fontSize: 15, flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
          {artifact.fileName}
        </Typography>
        {artifact.artifactType === 'code' && (
          <Chip
            label={getLanguageFromFilename(artifact.fileName)}
            size="small"
            sx={{ height: 20, fontSize: 11, fontWeight: 500, backgroundColor: '#e2e8f0', color: '#475569' }}
          />
        )}
        <IconButton
          size="small"
          onClick={handleDownload}
          disabled={artifact.status === 'lost'}
          title="Download"
          aria-label="download artifact"
          sx={{ color: '#2563eb' }}
        >
          <DownloadIcon sx={{ fontSize: 20 }} />
        </IconButton>
        <IconButton size="small" onClick={onClose} aria-label="close preview">
          <CloseIcon sx={{ fontSize: 20 }} />
        </IconButton>
      </DialogTitle>

      <DialogContent dividers sx={{ p: 0 }}>
        {content.status === 'loading' && (
          <Box sx={{ display: 'flex', justifyContent: 'center', alignItems: 'center', py: 6 }}>
            <CircularProgress size={32} />
          </Box>
        )}

        {content.status === 'error' && (
          <Box sx={{ p: 3, textAlign: 'center' }}>
            <Typography sx={{ color: '#ef4444', fontSize: 14 }}>{content.message}</Typography>
          </Box>
        )}

        {content.status === 'loaded' && content.blobUrl && (
          <Box sx={{ display: 'flex', justifyContent: 'center', p: 2, backgroundColor: '#f9fafb' }}>
            <Box
              component="img"
              src={content.blobUrl}
              alt={artifact.label}
              sx={{ maxWidth: '100%', maxHeight: '70vh', objectFit: 'contain' }}
            />
          </Box>
        )}

        {content.status === 'loaded' && content.iframeUrl && (
          <Box sx={{ width: '100%', height: '70vh' }}>
            <iframe
              src={content.iframeUrl}
              style={{ width: '100%', height: '100%', border: 'none' }}
              title={artifact.fileName}
            />
          </Box>
        )}

        {content.status === 'loaded' && content.text !== undefined && (() => {
          // Platinum table/chart JSON: render the rich component (same rules as
          // ArtifactViewer) instead of raw JSON, when a renderer is provided.
          if (renderPlatinumData && artifact.fileName.endsWith('.json')) {
            const parsed = tryParsePlatinumJson(content.text, artifact.fileName)
            if (parsed) return <>{renderPlatinumData(parsed)}</>
          }
          return (
          <>
            {/* Code artifacts: syntax highlighting */}
            {artifact.artifactType === 'code' && (
              <Highlight
                theme={themes.vsLight}
                code={content.text}
                language={getPrismLanguage(artifact.fileName)}
              >
                {({ style, tokens, getLineProps, getTokenProps }) => (
                  <pre
                    style={{
                      ...style,
                      margin: 0,
                      padding: '12px 16px',
                      fontSize: 13,
                      lineHeight: 1.5,
                      fontFamily: '"Fira Code", "Consolas", monospace',
                      overflow: 'auto',
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
            )}

            {/* CSV artifacts: table */}
            {artifact.artifactType === 'csv' && (() => {
              const csv = parseCSVPreview(content.text!, 100)
              return (
                <Box sx={{ overflow: 'auto' }}>
                  <Table size="small" stickyHeader>
                    <TableHead>
                      <TableRow>
                        {csv.columns.map((col, i) => (
                          <TableCell key={i} sx={{ fontWeight: 600, fontSize: 12, whiteSpace: 'nowrap' }}>
                            {col}
                          </TableCell>
                        ))}
                      </TableRow>
                    </TableHead>
                    <TableBody>
                      {csv.rows.map((row, ri) => (
                        <TableRow key={ri}>
                          {row.map((cell, ci) => (
                            <TableCell key={ci} sx={{ fontSize: 12, whiteSpace: 'nowrap' }}>
                              {cell}
                            </TableCell>
                          ))}
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                  {csv.totalRows > 100 && (
                    <Typography sx={{ fontSize: 12, color: '#6b7280', textAlign: 'center', py: 1 }}>
                      Showing 100 of {csv.totalRows} rows
                    </Typography>
                  )}
                </Box>
              )
            })()}

            {/* Report/markdown files */}
            {artifact.artifactType === 'report' && isMarkdown && (
              <Box sx={{ p: 2 }}>
                <AgentMarkdown content={content.text!} />
              </Box>
            )}

            {/* Report (non-markdown), data (chart/table JSON) and generic file fallback.
                'data' artifacts (PlatinumData chart/table JSON) were fetched but never
                rendered before this branch existed — the dialog appeared empty even
                though Download worked. JSON is pretty-printed for readability. */}
            {((artifact.artifactType === 'report' && !isMarkdown) || artifact.artifactType === 'file' || artifact.artifactType === 'data') && (
              <pre
                style={{
                  margin: 0,
                  padding: '12px 16px',
                  fontSize: 13,
                  lineHeight: 1.5,
                  fontFamily: '"Fira Code", "Consolas", monospace',
                  whiteSpace: 'pre-wrap',
                  wordBreak: 'break-all',
                  overflow: 'auto',
                }}
              >
                {formatMaybeJson(content.text, artifact.fileName)}
              </pre>
            )}
          </>
          )
        })()}
      </DialogContent>
    </Dialog>
  )
}
