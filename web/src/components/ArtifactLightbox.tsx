// Full-screen image lightbox overlay.
// Copied from frontend/src/components/agent/ArtifactLightbox.tsx.
// No Platinum-specific dependencies.
// See ../../docs/90-provenance-map.md.

import React, { useCallback } from 'react'
import { Box, IconButton, Typography } from '@mui/material'
import { Close as CloseIcon } from '@mui/icons-material'

interface ArtifactLightboxProps {
  open: boolean
  imageUrl: string
  alt: string
  caption?: string
  onClose: () => void
}

export default function ArtifactLightbox({ open, imageUrl, alt, caption, onClose }: ArtifactLightboxProps) {
  const handleKeyDown = useCallback((e: KeyboardEvent) => {
    if (e.key === 'Escape') onClose()
  }, [onClose])

  // Attach/detach keydown listener based on open state
  React.useEffect(() => {
    if (!open) return
    document.addEventListener('keydown', handleKeyDown)
    return () => document.removeEventListener('keydown', handleKeyDown)
  }, [open, handleKeyDown])

  if (!open) return null

  return (
    <Box
      data-testid="lightbox-backdrop"
      onClick={onClose}
      sx={{
        position: 'fixed',
        top: 0,
        left: 0,
        right: 0,
        bottom: 0,
        backgroundColor: 'rgba(0, 0, 0, 0.85)',
        zIndex: 1300,
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        flexDirection: 'column',
      }}
    >
      <IconButton
        onClick={(e) => { e.stopPropagation(); onClose() }}
        aria-label="close"
        sx={{
          position: 'absolute',
          top: 16,
          right: 16,
          color: 'white',
        }}
      >
        <CloseIcon />
      </IconButton>

      <img
        src={imageUrl}
        alt={alt}
        onClick={(e) => e.stopPropagation()}
        style={{
          maxWidth: '90vw',
          maxHeight: '85vh',
          objectFit: 'contain',
          cursor: 'default',
        }}
      />

      {caption && (
        <Typography
          sx={{ color: 'white', mt: 2, fontSize: 14, textAlign: 'center' }}
          onClick={(e) => e.stopPropagation()}
        >
          {caption}
        </Typography>
      )}
    </Box>
  )
}
