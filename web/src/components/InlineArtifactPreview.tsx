// Inline artifact preview component.
// Copied + factored from frontend/src/components/agent/InlineArtifactPreview.tsx.
// Factoring changes:
//   - Removed PublishWebappDialog (Platinum-specific); replaced with optional onPublishWebapp prop.
//   - Removed useAccount (AccountContext); customer passed as optional prop.
//   - Removed @/ imports; API base URL passed as prop.
//   - Removed isImageArtifact/getArtifactImageUrl utils; inlined generically.
//   - Removed @untitledui/file-icons; replaced with emoji fallback.
// See ../../docs/90-provenance-map.md.

import React, { useState, useRef, useCallback } from 'react'
import { Box, Typography, IconButton } from '@mui/material'
import {
  Download as DownloadIcon,
  Fullscreen as FullscreenIcon,
  Share as ShareIcon,
} from '@mui/icons-material'
import type { ArtifactInfo } from '../types.js'
import { webappEntryRelPath } from '../artifactFilters.js'
import AgentMarkdown from './AgentMarkdown.js'

interface InlineArtifactPreviewProps {
  artifact: ArtifactInfo
  sessionId: string
  onOpenPreview: (artifact: ArtifactInfo) => void
  /** When provided, skip HTTP fetch and use this data URL directly. */
  dataUrl?: string
  /** API base URL. Default: empty string (same origin). */
  apiBaseUrl?: string
  /** Auth header value. */
  authHeader?: string
  /** Optional callback for publish/share action on webapp artifacts. */
  onPublishWebapp?: (sessionId: string, artifact: ArtifactInfo) => void
}

const MAX_CONTENT_SIZE = 50_000

const IMAGE_EXTENSIONS = new Set(['.png', '.jpg', '.jpeg', '.svg', '.gif', '.webp'])

function isImageArtifact(artifact: ArtifactInfo): boolean {
  if (artifact.artifactType === 'image' || artifact.artifactType === 'chart') return true
  if (artifact.artifactType === 'file') {
    const ext = '.' + artifact.fileName.split('.').pop()?.toLowerCase()
    return IMAGE_EXTENSIONS.has(ext)
  }
  return false
}

function getArtifactImageUrl(artifact: ArtifactInfo, sessionId: string, baseUrl: string): string | null {
  if (artifact.status === 'lost') return null
  if (artifact.id) return `${baseUrl}/agent/artifacts/${artifact.id}/download`
  const filePath = artifact.filePath.replace(/^\//, '')
  return `${baseUrl}/agent/session/${sessionId}/workspace/files/${filePath}`
}

export default function InlineArtifactPreview({
  artifact,
  sessionId,
  onOpenPreview,
  dataUrl,
  apiBaseUrl = '',
  authHeader,
  onPublishWebapp,
}: InlineArtifactPreviewProps) {
  const [content, setContent] = useState<{ text?: string; error?: string } | null>(null)
  // Tracks the artifact identity we last fetched for, so we re-fetch when the
  // artifact becomes loadable. A freshly-registered artifact starts out 'live'
  // with no DB id and its blob may not be written yet, so the first attempt can
  // 404; we retry once it gains an id / transitions to 'extracted'. Without this
  // the transient 404 would stick as a permanent error card even though clicking
  // the file (which re-fetches) works.
  const fetchedKeyRef = useRef<string | null>(null)

  function makeHeaders(): Record<string, string> {
    const h: Record<string, string> = {}
    if (authHeader) h['Authorization'] = authHeader
    return h
  }

  const needsFetch = (artifact.artifactType === 'code' || artifact.artifactType === 'csv' || artifact.artifactType === 'report')
    && artifact.status !== 'lost'

  const fetchKey = `${artifact.id ?? ''}:${artifact.status}`
  if (needsFetch && fetchedKeyRef.current !== fetchKey) {
    fetchedKeyRef.current = fetchKey
    const isExtracted = artifact.status === 'extracted'
    const url = artifact.id
      ? `${apiBaseUrl}/agent/artifacts/${artifact.id}/download`
      : `${apiBaseUrl}/agent/session/${sessionId}/workspace/files/${artifact.filePath.replace(/^\//, '')}`
    fetch(url, { headers: makeHeaders() })
      .then(res => {
        if (!res.ok) throw new Error(`HTTP ${res.status}`)
        return res.text()
      })
      .then(text => setContent({ text: text.slice(0, MAX_CONTENT_SIZE) }))
      .catch(err => {
        // While the artifact is still being extracted a 404 is expected — keep
        // showing the loading state and let the next update (id/status change)
        // retry, rather than flashing a permanent error.
        if (!isExtracted) {
          setContent(null)
          return
        }
        setContent({ error: err instanceof Error ? err.message : 'Failed to load' })
      })
  }

  const handleDownload = useCallback((e: React.MouseEvent) => {
    e.stopPropagation()
    const downloadUrl = artifact.id
      ? `${apiBaseUrl}/agent/artifacts/${artifact.id}/download`
      : `${apiBaseUrl}/agent/session/${sessionId}/workspace/files/${artifact.filePath.replace(/^\//, '')}`
    fetch(downloadUrl, { headers: makeHeaders() })
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

  const handleClick = useCallback(() => {
    onOpenPreview(artifact)
  }, [artifact, onOpenPreview])

  if (artifact.status === 'lost') {
    return (
      <Box sx={{ border: '1px solid #e5e7eb', borderRadius: '8px', p: 1.5, mt: 1, backgroundColor: '#f9fafb' }}>
        <Typography sx={{ fontSize: 13, color: '#6b7280' }}>
          {artifact.label} — no longer available
        </Typography>
      </Box>
    )
  }

  if (isImageArtifact(artifact)) {
    return (
      <ImagePreview
        artifact={artifact}
        sessionId={sessionId}
        onClick={handleClick}
        onDownload={handleDownload}
        dataUrl={dataUrl}
        apiBaseUrl={apiBaseUrl}
        authHeader={authHeader}
      />
    )
  }

  if (artifact.artifactType === 'code') {
    return (
      <Box onClick={handleClick} sx={{ mt: 1, cursor: 'pointer', '&:hover': { opacity: 0.85 } }}>
        {content?.text ? (
          <CodePreview code={content.text} fileName={artifact.fileName} />
        ) : content?.error ? (
          <ErrorCard message={content.error} />
        ) : (
          <LoadingCard />
        )}
        <CaptionBar artifact={artifact} onDownload={handleDownload} />
      </Box>
    )
  }

  if (artifact.artifactType === 'csv') {
    return (
      <Box onClick={handleClick} sx={{ mt: 1, cursor: 'pointer', '&:hover': { opacity: 0.85 } }}>
        {content?.text ? (
          <CsvPreview csv={content.text} />
        ) : content?.error ? (
          <ErrorCard message={content.error} />
        ) : (
          <LoadingCard />
        )}
        <CaptionBar artifact={artifact} onDownload={handleDownload} />
      </Box>
    )
  }

  if (artifact.artifactType === 'webapp') {
    const isReady = artifact.status === 'extracted'
    const filePath = webappEntryRelPath(artifact)
    const token = authHeader?.replace('Bearer ', '')
    const iframeSrc = token
      ? `${apiBaseUrl}/webapp/${sessionId}/${token}/${filePath}`
      : `${apiBaseUrl}/agent/session/${sessionId}/workspace/files/${filePath}`

    return (
      <Box sx={{
        mt: 1,
        border: '1.5px solid #3b82f6',
        borderRadius: '8px',
        overflow: 'hidden',
        boxShadow: '0 1px 6px rgba(59, 130, 246, 0.15)',
      }}>
        {isReady ? (
          <WebappIframe src={iframeSrc} title={artifact.label || 'Interactive Visualization'} />
        ) : (
          <Box sx={{ display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', height: 300, gap: 1.5, backgroundColor: '#f8fafc' }}>
            <Box sx={{ width: 28, height: 28, border: '3px solid #3b82f6', borderTopColor: 'transparent', borderRadius: '50%', animation: 'spin 0.8s linear infinite', '@keyframes spin': { '100%': { transform: 'rotate(360deg)' } } }} />
            <Typography sx={{ color: '#6b7280', fontSize: 13 }}>Uploading to cloud storage...</Typography>
          </Box>
        )}
        <WebappCaptionBar
          artifact={artifact}
          onDownload={handleDownload}
          onFullscreen={handleClick}
          onPublish={onPublishWebapp ? () => onPublishWebapp(sessionId, artifact) : undefined}
        />
      </Box>
    )
  }

  if (artifact.artifactType === 'report') {
    return (
      <Box onClick={handleClick} sx={{ mt: 1, border: '1px solid #e5e7eb', borderRadius: '8px', overflow: 'hidden', cursor: 'pointer', '&:hover': { borderColor: '#93c5fd' } }}>
        {content?.text ? (
          <Box sx={{ p: 1.5, maxHeight: 150, overflow: 'hidden' }}>
            <AgentMarkdown content={content.text.slice(0, 500)} />
          </Box>
        ) : content?.error ? (
          <ErrorCard message={content.error} />
        ) : (
          <LoadingCard />
        )}
        <CaptionBar artifact={artifact} onDownload={handleDownload} />
      </Box>
    )
  }

  // Generic file artifact
  return (
    <Box onClick={handleClick} sx={{ mt: 1, border: '1px solid #e5e7eb', borderRadius: '8px', overflow: 'hidden', backgroundColor: '#f9fafb', cursor: 'pointer', '&:hover': { borderColor: '#93c5fd' } }}>
      <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, p: 1.5 }}>
        <Typography sx={{ fontSize: 20 }}>📄</Typography>
        <Typography sx={{ fontSize: 13, color: '#374151', fontWeight: 500 }}>{artifact.fileName}</Typography>
      </Box>
      <CaptionBar artifact={artifact} onDownload={handleDownload} />
    </Box>
  )
}

function ImagePreview({ artifact, sessionId, onClick, onDownload, dataUrl, apiBaseUrl = '', authHeader }: { artifact: ArtifactInfo; sessionId: string; onClick: () => void; onDownload: (e: React.MouseEvent) => void; dataUrl?: string; apiBaseUrl?: string; authHeader?: string }) {
  const [blobUrl, setBlobUrl] = useState<string | null>(dataUrl || null)
  const fetchStartedRef = useRef(!!dataUrl)

  if (!fetchStartedRef.current) {
    fetchStartedRef.current = true
    const rawUrl = getArtifactImageUrl(artifact, sessionId, apiBaseUrl)
    if (rawUrl) {
      const h: Record<string, string> = {}
      if (authHeader) h['Authorization'] = authHeader
      fetch(rawUrl, { headers: h })
        .then(res => {
          if (!res.ok) throw new Error(`HTTP ${res.status}`)
          return res.blob()
        })
        .then(blob => setBlobUrl(URL.createObjectURL(blob)))
        .catch(err => console.error('Image load failed:', err))
    }
  }

  return (
    <Box onClick={onClick} sx={{ display: 'inline-block', border: '1px solid #e5e7eb', borderRadius: '8px', mt: 1, overflow: 'hidden', backgroundColor: '#f9fafb', cursor: 'pointer', maxWidth: '75%', '&:hover': { borderColor: '#93c5fd' } }}>
      {blobUrl ? (
        <img src={blobUrl} alt={artifact.label} style={{ display: 'block', maxWidth: '100%', maxHeight: 200, objectFit: 'contain' }} />
      ) : (
        <Box sx={{ p: 2, textAlign: 'center' }}>
          <Typography sx={{ fontSize: 12, color: '#94a3b8' }}>Loading image...</Typography>
        </Box>
      )}
      <CaptionBar artifact={artifact} onDownload={onDownload} />
    </Box>
  )
}

function CaptionBar({ artifact, onDownload }: { artifact: ArtifactInfo; onDownload: (e: React.MouseEvent) => void }) {
  return (
    <Box sx={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', p: '6px 10px', borderTop: '1px solid #e5e7eb', backgroundColor: '#f9fafb' }}>
      <Box sx={{ minWidth: 0, flex: 1 }}>
        <Typography sx={{ fontSize: 13, fontWeight: 600, color: '#374151', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
          {artifact.label}
        </Typography>
        {artifact.description && (
          <Typography sx={{ fontSize: 12, color: '#6b7280', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
            {artifact.description}
          </Typography>
        )}
      </Box>
      <IconButton size="small" onClick={onDownload} aria-label="download" sx={{ color: '#2563eb', ml: 0.5 }}>
        <DownloadIcon sx={{ fontSize: 18 }} />
      </IconButton>
    </Box>
  )
}

function WebappIframe({ src, title }: { src: string; title: string }) {
  const [loading, setLoading] = useState(true)
  return (
    <Box sx={{ position: 'relative', backgroundColor: '#0f172a' }}>
      {loading && (
        <Box sx={{ position: 'absolute', inset: 0, display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 1 }}>
          <Typography sx={{ fontSize: 13, color: '#94a3b8' }}>Loading visualization...</Typography>
        </Box>
      )}
      <iframe
        src={src}
        sandbox="allow-scripts allow-same-origin"
        onLoad={() => setLoading(false)}
        style={{ width: '100%', height: 800, border: 'none', display: 'block', opacity: loading ? 0 : 1, transition: 'opacity 0.2s' }}
        title={title}
      />
    </Box>
  )
}

function WebappCaptionBar({ artifact, onDownload, onFullscreen, onPublish }: { artifact: ArtifactInfo; onDownload: (e: React.MouseEvent) => void; onFullscreen: () => void; onPublish?: () => void }) {
  return (
    <Box sx={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', p: '6px 10px', borderTop: '1px solid #e5e7eb', backgroundColor: '#f9fafb' }}>
      <Box sx={{ minWidth: 0, flex: 1 }}>
        <Typography sx={{ fontSize: 13, fontWeight: 600, color: '#374151', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
          {artifact.label}
        </Typography>
        {artifact.description && (
          <Typography sx={{ fontSize: 12, color: '#6b7280', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
            {artifact.description}
          </Typography>
        )}
      </Box>
      <Box sx={{ display: 'flex', gap: 0.25 }}>
        {onPublish && (
          <IconButton size="small" onClick={onPublish} aria-label="publish" sx={{ color: '#2563eb' }}>
            <ShareIcon sx={{ fontSize: 18 }} />
          </IconButton>
        )}
        <IconButton size="small" onClick={onFullscreen} aria-label="fullscreen" sx={{ color: '#2563eb' }}>
          <FullscreenIcon sx={{ fontSize: 18 }} />
        </IconButton>
        <IconButton size="small" onClick={onDownload} aria-label="download" sx={{ color: '#2563eb' }}>
          <DownloadIcon sx={{ fontSize: 18 }} />
        </IconButton>
      </Box>
    </Box>
  )
}

function CodePreview({ code, fileName }: { code: string; fileName: string }) {
  const lines = code.split('\n').slice(0, 5).join('\n')
  return (
    <Box sx={{ border: '1px solid #e5e7eb', borderRadius: '6px', overflow: 'hidden' }}>
      <Box sx={{ px: 1.5, py: 0.75, backgroundColor: '#f1f5f9', borderBottom: '1px solid #e5e7eb', display: 'flex', gap: 1, alignItems: 'center' }}>
        <Typography sx={{ fontSize: 11, fontWeight: 600, color: '#374151' }}>{fileName}</Typography>
      </Box>
      <Box component="pre" sx={{ m: 0, p: '8px 12px', fontSize: 11, color: '#374151', fontFamily: 'monospace', overflow: 'hidden', maxHeight: 90, whiteSpace: 'pre' }}>
        {lines}
      </Box>
    </Box>
  )
}

function CsvPreview({ csv }: { csv: string }) {
  const rows = csv.trim().split('\n').slice(0, 4)
  return (
    <Box sx={{ border: '1px solid #e5e7eb', borderRadius: '6px', overflow: 'auto', maxHeight: 90 }}>
      <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 11 }}>
        <tbody>
          {rows.map((row, i) => (
            <tr key={i}>
              {row.split(',').map((cell, j) => (
                <td key={j} style={{ padding: '2px 8px', border: '1px solid #e5e7eb', color: i === 0 ? '#374151' : '#6b7280', fontWeight: i === 0 ? 600 : 400 }}>
                  {cell.trim()}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </Box>
  )
}

function LoadingCard() {
  return (
    <Box sx={{ p: 1.5, border: '1px solid #e5e7eb', borderRadius: '6px', backgroundColor: '#f8fafc' }}>
      <Typography sx={{ fontSize: 12, color: '#94a3b8' }}>Loading preview...</Typography>
    </Box>
  )
}

function ErrorCard({ message }: { message: string }) {
  return (
    <Box sx={{ p: 1.5, border: '1px solid #e5e7eb', borderRadius: '6px', backgroundColor: '#fef2f2' }}>
      <Typography sx={{ fontSize: 12, color: '#ef4444' }}>Failed to load: {message}</Typography>
    </Box>
  )
}
