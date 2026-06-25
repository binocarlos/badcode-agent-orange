// File attachment hook.
// Copied + factored from frontend/src/hooks/useFileAttachments.ts.
// Upload endpoint is parameterised via uploadEndpoint instead of a hard-coded
// Platinum route. See ../../docs/90-provenance-map.md.

import { useCallback, useRef, useState } from 'react'

export interface PendingAttachment {
  id: string
  file: File
  previewUrl?: string
  uploadState: 'pending' | 'uploading' | 'uploaded' | 'error'
  artifactId?: string
  error?: string
}

const MAX_FILE_SIZE = 100 * 1024 * 1024 // 100MB

export interface UseFileAttachmentsOptions {
  /** Endpoint to POST files to. Receives sessionId as parameter. */
  uploadEndpoint?: (sessionId: string) => string
  /** Auth header value (e.g. "Bearer <token>"). If provided, sent with upload. */
  authHeader?: string
}

export default function useFileAttachments(options: UseFileAttachmentsOptions = {}) {
  const {
    uploadEndpoint = (sessionId: string) => `/agent/session/${sessionId}/upload`,
    authHeader,
  } = options

  const [attachments, setAttachments] = useState<PendingAttachment[]>([])
  const idCounterRef = useRef(0)

  const addFiles = useCallback((files: FileList | File[]) => {
    const newAttachments: PendingAttachment[] = []
    for (const file of Array.from(files)) {
      if (file.size > MAX_FILE_SIZE) continue

      const id = `attachment-${++idCounterRef.current}`
      const previewUrl = file.type.startsWith('image/') ? URL.createObjectURL(file) : undefined
      newAttachments.push({ id, file, previewUrl, uploadState: 'pending' })
    }
    if (newAttachments.length > 0) {
      setAttachments(prev => [...prev, ...newAttachments])
    }
  }, [])

  const removeFile = useCallback((id: string) => {
    setAttachments(prev => {
      const item = prev.find(a => a.id === id)
      if (item?.previewUrl) URL.revokeObjectURL(item.previewUrl)
      return prev.filter(a => a.id !== id)
    })
  }, [])

  const uploadAll = useCallback(async (sessionId: string): Promise<string[]> => {
    const pending = attachments.filter(a => a.uploadState === 'pending')
    if (pending.length === 0) return attachments.filter(a => a.artifactId).map(a => a.artifactId!)

    // Mark all pending as uploading
    setAttachments(prev => prev.map(a =>
      a.uploadState === 'pending' ? { ...a, uploadState: 'uploading' as const } : a
    ))

    const artifactIds: string[] = []
    const headers: Record<string, string> = {}
    if (authHeader) headers['Authorization'] = authHeader

    for (const attachment of pending) {
      try {
        const formData = new FormData()
        formData.append('file', attachment.file, attachment.file.name)
        const resp = await fetch(uploadEndpoint(sessionId), {
          method: 'POST',
          body: formData,
          headers,
        })
        if (!resp.ok) throw new Error(`Upload failed: ${resp.status}`)
        const data = await resp.json() as { id: string }
        const artifactId = data.id
        artifactIds.push(artifactId)
        setAttachments(prev => prev.map(a =>
          a.id === attachment.id ? { ...a, uploadState: 'uploaded' as const, artifactId } : a
        ))
      } catch {
        setAttachments(prev => prev.map(a =>
          a.id === attachment.id ? { ...a, uploadState: 'error' as const, error: 'Upload failed' } : a
        ))
      }
    }

    // Also include previously uploaded IDs
    const existingIds = attachments.filter(a => a.artifactId).map(a => a.artifactId!)
    return [...existingIds, ...artifactIds]
  }, [attachments, uploadEndpoint, authHeader])

  const handlePaste = useCallback((e: ClipboardEvent) => {
    const items = e.clipboardData?.items
    if (!items) return
    const files: File[] = []
    for (const item of Array.from(items)) {
      if (item.kind === 'file' && item.type.startsWith('image/')) {
        const file = item.getAsFile()
        if (file) files.push(file)
      }
    }
    if (files.length > 0) {
      addFiles(files)
    }
  }, [addFiles])

  const clear = useCallback(() => {
    setAttachments(prev => {
      for (const a of prev) {
        if (a.previewUrl) URL.revokeObjectURL(a.previewUrl)
      }
      return []
    })
  }, [])

  return { attachments, addFiles, removeFile, uploadAll, handlePaste, clear }
}
