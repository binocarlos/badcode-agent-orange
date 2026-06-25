// Full artifact viewer with file tree, content preview, download.
// Copied from frontend/src/components/agent/ArtifactViewer.tsx.
// Factoring changes:
//   - Removed useAccount hook; replaced with `customer` prop.
//   - Removed API_BASE_URL import; replaced with `apiBaseUrl` prop (default '/api/v1').
//   - Removed axios dependency; replaced axios.defaults auth header read with `authHeader` prop.
//   - Removed PublishWebappDialog (Platinum-specific); replaced with `onPublishWebapp` callback prop.
//   - Removed InlinePlatinumTable (Platinum-specific); replaced with `renderPlatinumData` render prop.
//   - Import paths updated: types/artifactFilters/artifactTree/prismLanguage → local.
// See ../../docs/90-provenance-map.md.

import React, { useState, useCallback, useMemo, useRef, ReactNode } from 'react'
import {
  Box,
  Button,
  Typography,
  IconButton,
  CircularProgress,
  Chip,
  Table,
  TableHead,
  TableBody,
  TableRow,
  TableCell,
  TableSortLabel,
  Dialog,
  Tooltip,
} from '@mui/material'
import {
  Close as CloseIcon,
  Download as DownloadIcon,
  Share as ShareIcon,
  WarningAmber as WarningAmberIcon,
  ErrorOutline as ErrorOutlineIcon,
} from '@mui/icons-material'
import { FileIcon } from '@untitledui/file-icons'
import { Highlight, themes } from 'prism-react-renderer'
import type { ArtifactInfo } from '../types.js'
import type { PlatinumArtifactData } from '../platinumArtifact.js'
import { tryParsePlatinumJson } from '../platinumArtifact.js'
import { buildArtifactTree } from '../artifactTree.js'
import { getLanguageFromFilename, parseCSVPreview, webappEntryRelPath } from '../artifactFilters.js'
import { getPrismLanguage } from '../prismLanguage.js'
import AgentMarkdown from './AgentMarkdown.js'
import ArtifactTreeView from './ArtifactTreeView.js'
import ArtifactLightbox from './ArtifactLightbox.js'

type ContentState =
  | { status: 'idle' }
  | { status: 'loading' }
  | { status: 'loaded'; text?: string; blobUrl?: string }
  | { status: 'error'; message: string }

// PlatinumArtifactData and tryParsePlatinumJson now live in ../platinumArtifact.js
// so the inline ArtifactPreviewDialog can apply the exact same rules.
export type { PlatinumArtifactData } from '../platinumArtifact.js'

interface ArtifactViewerProps {
  artifacts: ArtifactInfo[]
  sessionId: string
  initialArtifact?: ArtifactInfo | null
  /** When true, renders inline (no Dialog wrapper). When false/undefined, renders as fullscreen Dialog. */
  inline?: boolean
  onClose?: () => void
  /** API base URL for fetching/downloading artifacts. Default: '/api/v1'. */
  apiBaseUrl?: string
  /** Authorization header value (e.g. 'Bearer <token>'). */
  authHeader?: string
  /** Customer name — used to gate the publish (share) button visibility. */
  customer?: string
  /**
   * Called when user clicks the publish/share button on a webapp artifact.
   * Platinum passes this to open PublishWebappDialog.
   */
  onPublishWebapp?: (sessionId: string, artifact: ArtifactInfo) => void
  /**
   * Render Platinum table/chart JSON artifacts inline.
   * If not provided, JSON artifacts that match the Platinum pattern fall back to generic text.
   */
  renderPlatinumData?: (data: PlatinumArtifactData) => ReactNode
}

const STATUS_DOT_COLORS: Record<string, string> = {
  live: '#16a34a',
  extracted: '#2563eb',
  lost: '#9ca3af',
  extraction_failed: '#ef4444',
}

function formatFileSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

// Known binary formats that can't be previewed as text
const BINARY_EXTENSIONS = new Set([
  // Office documents
  '.pptx', '.ppt', '.docx', '.doc', '.xlsx', '.xls', '.odt', '.ods', '.odp',
  // Archives
  '.zip', '.tar', '.gz', '.bz2', '.rar', '.7z',
  // Media
  '.mp3', '.mp4', '.avi', '.mov', '.wav', '.flac', '.ogg', '.webm', '.mkv',
  // Fonts
  '.woff', '.woff2', '.ttf', '.otf', '.eot',
  // Executables/binaries
  '.exe', '.dll', '.so', '.dylib', '.bin',
  // Data
  '.sqlite', '.db', '.parquet',
])

export default function ArtifactViewer({
  artifacts,
  sessionId,
  initialArtifact,
  inline,
  onClose,
  apiBaseUrl = '/api/v1',
  authHeader,
  customer,
  onPublishWebapp,
  renderPlatinumData,
}: ArtifactViewerProps) {
  const [selected, setSelected] = useState<ArtifactInfo | null>(initialArtifact || null)
  const [content, setContent] = useState<ContentState>({ status: 'idle' })
  const [lightboxUrl, setLightboxUrl] = useState<string | null>(null)

  const tree = useMemo(() => buildArtifactTree(artifacts), [artifacts])

  const fetchContent = useCallback(async (art: ArtifactInfo) => {
    if (art.status === 'lost') {
      setContent({ status: 'error', message: 'This artifact is no longer available (session workspace was cleaned up).' })
      return
    }
    if (art.status === 'extraction_failed') {
      setContent({ status: 'error', message: 'This artifact failed to save and may not be available for download.' })
      return
    }

    // Webapp artifacts are rendered via iframe src — no content fetch needed
    if (art.artifactType === 'webapp') {
      setContent({ status: 'loaded' })
      return
    }

    // Binary files that can't be previewed — skip fetch, show download prompt
    const ext = '.' + art.fileName.split('.').pop()?.toLowerCase()
    if (ext && BINARY_EXTENSIONS.has(ext)) {
      setContent({ status: 'loaded' })
      return
    }

    // If the artifact already has an inline data URL (e.g. from view_image base64), use it directly
    if (art.downloadUrl?.startsWith('data:')) {
      setContent({ status: 'loaded', blobUrl: art.downloadUrl })
      return
    }

    setContent({ status: 'loading' })
    const headers: Record<string, string> = {}
    if (authHeader) headers['Authorization'] = authHeader

    // Determine URL: artifacts with a DB id use the download endpoint
    const filePath = art.filePath.replace(/^\//, '')
    const url = art.id
      ? `${apiBaseUrl}/agent/artifacts/${art.id}/download`
      : `${apiBaseUrl}/agent/session/${sessionId}/workspace/files/${filePath}`

    try {
      const isImage = art.artifactType === 'image' || art.artifactType === 'chart'
      const isPdf = art.fileName.endsWith('.pdf')

      if (isImage || isPdf) {
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
      setContent({ status: 'error', message: `Failed to load: ${err instanceof Error ? err.message : 'Unknown error'}` })
    }
  }, [sessionId, apiBaseUrl, authHeader])

  const initialFetchRef = useRef(false)
  if (initialArtifact && !initialFetchRef.current) {
    initialFetchRef.current = true
    fetchContent(initialArtifact)
  }

  const handleSelect = useCallback((artifact: ArtifactInfo) => {
    // Revoke previous blob URL
    if (content.status === 'loaded' && content.blobUrl) {
      URL.revokeObjectURL(content.blobUrl)
    }
    setSelected(artifact)
    fetchContent(artifact)
  }, [fetchContent, content])

  const handleDownload = useCallback(() => {
    if (!selected) return
    const downloadUrl = selected.id
      ? `${apiBaseUrl}/agent/artifacts/${selected.id}/download`
      : `${apiBaseUrl}/agent/session/${sessionId}/workspace/files/${selected.filePath.replace(/^\//, '')}`

    const headers: Record<string, string> = {}
    if (authHeader) headers['Authorization'] = authHeader
    fetch(downloadUrl, { headers })
      .then(res => res.blob())
      .then(blob => {
        const url = URL.createObjectURL(blob)
        const a = document.createElement('a')
        a.href = url
        a.download = selected.fileName
        a.click()
        URL.revokeObjectURL(url)
      })
      .catch(err => console.error('Download failed:', err))
  }, [selected, sessionId, apiBaseUrl, authHeader])

  // Derive selected path for tree highlighting
  const selectedPath = useMemo(() => {
    if (!selected) return undefined
    return selected.filePath.replace(/^\/?(workspace\/)?/, '').replace(/^\//, '')
  }, [selected])

  const viewerContent = (
    <Box sx={{ display: 'flex', height: '100%', minHeight: 0, overflow: 'hidden' }}>
      {/* Left pane: File tree */}
      <Box
        sx={{
          width: 280,
          minWidth: 280,
          borderRight: '1px solid #e5e7eb',
          backgroundColor: '#fafafa',
          display: 'flex',
          flexDirection: 'column',
          overflow: 'hidden',
        }}
      >
        <Box sx={{ p: '10px 12px', borderBottom: '1px solid #e5e7eb' }}>
          <Typography sx={{ fontSize: 13, fontWeight: 600, color: '#374151' }}>
            Files ({artifacts.length})
          </Typography>
        </Box>
        <Box sx={{ flex: 1, overflow: 'auto', px: 0.5 }}>
          <ArtifactTreeView root={tree} selectedPath={selectedPath} onSelect={handleSelect} />
        </Box>
      </Box>

      {/* Right pane: Content viewer */}
      <Box sx={{ flex: 1, display: 'flex', flexDirection: 'column', minWidth: 0, overflow: 'hidden' }}>
        {/* Header bar */}
        {selected && (
          <Box
            sx={{
              display: 'flex',
              alignItems: 'center',
              gap: 1,
              px: 2,
              py: 1,
              borderBottom: '1px solid #e5e7eb',
              backgroundColor: 'white',
              minHeight: 44,
            }}
          >
            <Box
              sx={{
                width: 8,
                height: 8,
                borderRadius: '50%',
                backgroundColor: STATUS_DOT_COLORS[selected.status] || '#9ca3af',
                flexShrink: 0,
              }}
            />
            <Typography
              sx={{
                fontSize: 14,
                fontWeight: 600,
                flex: 1,
                overflow: 'hidden',
                textOverflow: 'ellipsis',
                whiteSpace: 'nowrap',
              }}
            >
              {selected.fileName}
            </Typography>
            {selected.status === 'lost' && (
              <Tooltip title="This artifact is no longer available -- the workspace was destroyed before it could be saved">
                <WarningAmberIcon
                  data-testid="status-warning-lost"
                  sx={{ fontSize: 16, color: 'warning.main', flexShrink: 0 }}
                />
              </Tooltip>
            )}
            {selected.status === 'extraction_failed' && (
              <Tooltip title="This artifact failed to save -- it may not be downloadable">
                <ErrorOutlineIcon
                  data-testid="status-error-extraction-failed"
                  sx={{ fontSize: 16, color: 'error.main', flexShrink: 0 }}
                />
              </Tooltip>
            )}
            <Chip
              label={selected.artifactType}
              size="small"
              sx={{ height: 20, fontSize: 11, fontWeight: 500, backgroundColor: '#e2e8f0', color: '#475569' }}
            />
            {selected.artifactType === 'code' && (
              <Chip
                label={getLanguageFromFilename(selected.fileName)}
                size="small"
                sx={{ height: 20, fontSize: 11, fontWeight: 500, backgroundColor: '#dbeafe', color: '#1d4ed8' }}
              />
            )}
            {selected.artifactType === 'webapp' && customer && onPublishWebapp && (
              <Tooltip title="Publish as dashboard">
                <IconButton
                  size="small"
                  onClick={() => onPublishWebapp(sessionId, selected)}
                  disabled={selected.status === 'lost' || selected.status === 'extraction_failed'}
                  sx={{ color: '#2563eb' }}
                >
                  <ShareIcon sx={{ fontSize: 20 }} />
                </IconButton>
              </Tooltip>
            )}
            <IconButton
              size="small"
              onClick={handleDownload}
              disabled={selected.status === 'lost' || selected.status === 'extraction_failed'}
              title="Download"
              sx={{ color: '#2563eb' }}
            >
              <DownloadIcon sx={{ fontSize: 20 }} />
            </IconButton>
            {onClose && (
              <IconButton size="small" onClick={onClose} title="Close">
                <CloseIcon sx={{ fontSize: 20 }} />
              </IconButton>
            )}
          </Box>
        )}

        {/* Content area */}
        <Box sx={{ flex: 1, overflow: 'auto', backgroundColor: '#fafafa' }}>
          {!selected && (
            <Box sx={{ display: 'flex', justifyContent: 'center', alignItems: 'center', height: '100%' }}>
              <Typography sx={{ fontSize: 14, color: '#9ca3af' }}>
                Select a file to preview
              </Typography>
            </Box>
          )}

          {selected && content.status === 'loading' && (
            <Box sx={{ display: 'flex', justifyContent: 'center', alignItems: 'center', py: 8 }}>
              <CircularProgress size={32} />
            </Box>
          )}

          {selected && content.status === 'error' && (
            <Box sx={{ p: 3, textAlign: 'center' }}>
              <Typography sx={{ color: '#ef4444', fontSize: 14 }}>{content.message}</Typography>
            </Box>
          )}

          {selected && content.status === 'loaded' && (
            <ContentRenderer
              artifact={selected}
              content={content}
              sessionId={sessionId}
              apiBaseUrl={apiBaseUrl}
              authHeader={authHeader}
              onImageClick={setLightboxUrl}
              onDownload={handleDownload}
              renderPlatinumData={renderPlatinumData}
            />
          )}
        </Box>
      </Box>

      {/* Lightbox for full-screen image view */}
      <ArtifactLightbox
        open={!!lightboxUrl}
        imageUrl={lightboxUrl || ''}
        alt={selected?.label || ''}
        caption={selected?.label}
        onClose={() => setLightboxUrl(null)}
      />
    </Box>
  )

  if (inline) {
    return (
      <Box sx={{ height: '100%', display: 'flex', flexDirection: 'column' }}>
        {viewerContent}
      </Box>
    )
  }

  return (
    <Dialog
      open
      onClose={onClose}
      fullScreen
      PaperProps={{ sx: { backgroundColor: '#fafafa' } }}
    >
      {viewerContent}
    </Dialog>
  )
}

// ——— Content renderers ———

function ContentRenderer({
  artifact,
  content,
  sessionId,
  apiBaseUrl = '/api/v1',
  authHeader,
  onImageClick,
  onDownload,
  renderPlatinumData,
}: {
  artifact: ArtifactInfo
  content: { text?: string; blobUrl?: string }
  sessionId: string
  apiBaseUrl?: string
  authHeader?: string
  onImageClick: (url: string) => void
  onDownload: () => void
  renderPlatinumData?: (data: PlatinumArtifactData) => ReactNode
}) {
  const isHtml = artifact.fileName.endsWith('.html') || artifact.fileName.endsWith('.htm')
  const isMarkdown = artifact.fileName.endsWith('.md')
  const isPdf = artifact.fileName.endsWith('.pdf')
  const isImage = artifact.artifactType === 'image' || artifact.artifactType === 'chart'

  // Webapp: iframe with src URL (enables relative imports for JS/CSS)
  if (artifact.artifactType === 'webapp') {
    return <WebappRenderer artifact={artifact} sessionId={sessionId} apiBaseUrl={apiBaseUrl} authHeader={authHeader} />
  }

  // HTML: sandboxed iframe
  if (isHtml && content.text) {
    return <HtmlRenderer html={content.text} />
  }

  // PDF: embedded viewer
  if (isPdf && content.blobUrl) {
    return (
      <Box sx={{ height: '100%', minHeight: 500 }}>
        <iframe
          src={content.blobUrl}
          style={{ width: '100%', height: '100%', border: 'none' }}
          title={artifact.fileName}
        />
      </Box>
    )
  }

  // Image: full display with click-to-lightbox
  if (isImage && content.blobUrl) {
    return (
      <Box
        sx={{
          display: 'flex',
          justifyContent: 'center',
          alignItems: 'flex-start',
          p: 2,
          minHeight: 200,
        }}
      >
        <Box
          component="img"
          src={content.blobUrl}
          alt={artifact.label}
          onClick={() => onImageClick(content.blobUrl!)}
          sx={{
            maxWidth: '100%',
            maxHeight: '80vh',
            objectFit: 'contain',
            cursor: 'zoom-in',
            borderRadius: '4px',
          }}
        />
      </Box>
    )
  }

  // Platinum table/chart JSON: render via renderPlatinumData prop if provided
  if (renderPlatinumData && content.text && artifact.fileName.endsWith('.json')) {
    const parsed = tryParsePlatinumJson(content.text, artifact.fileName)
    if (parsed) {
      return <>{renderPlatinumData(parsed)}</>
    }
  }

  // CSV: sortable table
  if (artifact.artifactType === 'csv' && content.text) {
    return <CsvRenderer text={content.text} />
  }

  // Code: syntax highlighting
  if (artifact.artifactType === 'code' && content.text !== undefined) {
    return <CodeRenderer text={content.text} fileName={artifact.fileName} />
  }

  // Markdown
  if ((artifact.artifactType === 'report' && isMarkdown) && content.text !== undefined) {
    return (
      <Box sx={{ p: 3, backgroundColor: 'white' }}>
        <AgentMarkdown content={content.text} />
      </Box>
    )
  }

  // Generic text fallback
  if (content.text !== undefined) {
    return (
      <pre
        style={{
          margin: 0,
          padding: '16px 20px',
          fontSize: 13,
          lineHeight: 1.6,
          fontFamily: '"Fira Code", "Consolas", monospace',
          whiteSpace: 'pre-wrap',
          wordBreak: 'break-all',
          overflow: 'auto',
        }}
      >
        {content.text}
      </pre>
    )
  }

  // Binary fallback — rich card with file icon, metadata, and download button
  const ext = artifact.fileName.split('.').pop()?.toLowerCase() || ''
  return (
    <Box sx={{ display: 'flex', justifyContent: 'center', alignItems: 'center', height: '100%', minHeight: 300, p: 4 }}>
      <Box sx={{ textAlign: 'center', maxWidth: 320 }}>
        <Box sx={{ mx: 'auto', mb: 2.5 }}>
          <FileIcon
            type={ext || (artifact.mimeType ?? 'document')}
            size={80}
          />
        </Box>

        <Typography sx={{ fontSize: 16, fontWeight: 600, color: '#1f2937', mb: 0.5, wordBreak: 'break-word' }}>
          {artifact.fileName}
        </Typography>

        {(artifact.fileSize != null || artifact.description) && (
          <Typography sx={{ fontSize: 13, color: '#6b7280', mb: 0.5 }}>
            {[
              artifact.fileSize != null ? formatFileSize(artifact.fileSize) : null,
              artifact.description,
            ].filter(Boolean).join(' — ')}
          </Typography>
        )}

        <Typography sx={{ fontSize: 12, color: '#9ca3af', mb: 3, textTransform: 'uppercase', letterSpacing: 0.5 }}>
          {ext ? `.${ext} file` : 'Binary file'} — no preview available
        </Typography>

        <Button
          variant="contained"
          color="info"
          startIcon={<DownloadIcon />}
          onClick={onDownload}
          disabled={artifact.status === 'lost' || artifact.status === 'extraction_failed'}
          sx={{ textTransform: 'none', fontWeight: 600, px: 3, py: 1 }}
        >
          Download File
        </Button>
      </Box>
    </Box>
  )
}

function WebappRenderer({
  artifact,
  sessionId,
  apiBaseUrl = '/api/v1',
  authHeader,
}: {
  artifact: ArtifactInfo
  sessionId: string
  apiBaseUrl?: string
  authHeader?: string
}) {
  const iframeSrc = useMemo(() => {
    const filePath = webappEntryRelPath(artifact)
    const token = authHeader?.replace(/^Bearer\s+/, '')
    // Embed JWT in the URL path so relative sub-resources (JS/CSS) automatically carry the token
    if (token) {
      return `${apiBaseUrl}/webapp/${sessionId}/${token}/${filePath}`
    }
    return `${apiBaseUrl}/agent/session/${sessionId}/workspace/files/${filePath}`
  }, [artifact, sessionId, apiBaseUrl, authHeader])

  return (
    <Box sx={{ height: '100%', minHeight: 500 }}>
      <iframe
        src={iframeSrc}
        sandbox="allow-scripts allow-same-origin"
        style={{ width: '100%', height: '100%', border: 'none', backgroundColor: 'white' }}
        title={artifact.label || 'Web Visualization'}
      />
    </Box>
  )
}

function HtmlRenderer({ html }: { html: string }) {
  return (
    <Box sx={{ height: '100%', minHeight: 500 }}>
      <iframe
        srcDoc={html}
        sandbox="allow-scripts"
        style={{ width: '100%', height: '100%', border: 'none', backgroundColor: 'white' }}
        title="HTML Preview"
      />
    </Box>
  )
}

function CsvRenderer({ text }: { text: string }) {
  const csv = parseCSVPreview(text, 10000) // show all rows
  const [sortCol, setSortCol] = useState<number | null>(null)
  const [sortDir, setSortDir] = useState<'asc' | 'desc'>('asc')

  const handleSort = (colIndex: number) => {
    if (sortCol === colIndex) {
      setSortDir(prev => (prev === 'asc' ? 'desc' : 'asc'))
    } else {
      setSortCol(colIndex)
      setSortDir('asc')
    }
  }

  const sortedRows = useMemo(() => {
    if (sortCol === null) return csv.rows
    const col = sortCol
    const dir = sortDir === 'asc' ? 1 : -1
    return [...csv.rows].sort((a, b) => {
      const va = a[col] || ''
      const vb = b[col] || ''
      // Try numeric comparison
      const na = parseFloat(va)
      const nb = parseFloat(vb)
      if (!isNaN(na) && !isNaN(nb)) return (na - nb) * dir
      return va.localeCompare(vb) * dir
    })
  }, [csv.rows, sortCol, sortDir])

  return (
    <Box sx={{ overflow: 'auto', backgroundColor: 'white' }}>
      <Table size="small" stickyHeader>
        <TableHead>
          <TableRow>
            {csv.columns.map((col, i) => (
              <TableCell key={i} sx={{ fontWeight: 600, fontSize: 12, whiteSpace: 'nowrap' }}>
                <TableSortLabel
                  active={sortCol === i}
                  direction={sortCol === i ? sortDir : 'asc'}
                  onClick={() => handleSort(i)}
                >
                  {col}
                </TableSortLabel>
              </TableCell>
            ))}
          </TableRow>
        </TableHead>
        <TableBody>
          {sortedRows.map((row, ri) => (
            <TableRow key={ri} hover>
              {row.map((cell, ci) => (
                <TableCell key={ci} sx={{ fontSize: 12, whiteSpace: 'nowrap' }}>
                  {cell}
                </TableCell>
              ))}
            </TableRow>
          ))}
        </TableBody>
      </Table>
      <Typography sx={{ fontSize: 12, color: '#6b7280', textAlign: 'center', py: 1 }}>
        {csv.totalRows} row{csv.totalRows !== 1 ? 's' : ''}
      </Typography>
    </Box>
  )
}

function CodeRenderer({ text, fileName }: { text: string; fileName: string }) {
  return (
    <Highlight
      theme={themes.vsLight}
      code={text}
      language={getPrismLanguage(fileName)}
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
            <div key={i} {...getLineProps({ line })} style={{ display: 'flex' }}>
              <span
                style={{
                  display: 'inline-block',
                  width: '3em',
                  textAlign: 'right',
                  paddingRight: '1em',
                  color: '#9ca3af',
                  userSelect: 'none',
                  flexShrink: 0,
                }}
              >
                {i + 1}
              </span>
              <span>
                {line.map((token, key) => (
                  <span key={key} {...getTokenProps({ token })} />
                ))}
              </span>
            </div>
          ))}
        </pre>
      )}
    </Highlight>
  )
}
