// Chat input toolbar component.
// Copied from frontend/src/components/agent/ChatInputToolbar.tsx.
// Generic — no Platinum-specific dependencies.
// See ../../docs/90-provenance-map.md.

import React, { useCallback, useRef, useState } from 'react'
import { Box, IconButton, Chip, CircularProgress, Tooltip, Typography, Menu, MenuItem } from '@mui/material'
import AttachFileIcon from '@mui/icons-material/AttachFile'
import MicIcon from '@mui/icons-material/Mic'
import ArrowDropDownIcon from '@mui/icons-material/ArrowDropDown'
import CloseIcon from '@mui/icons-material/Close'
import type { PendingAttachment } from '../hooks/useFileAttachments.js'
import RecordingOverlay from './RecordingOverlay.js'

const ACCEPTED_TYPES = '*/*'

interface ChatInputToolbarProps {
  onFilesSelected: (files: File[]) => void
  attachments: PendingAttachment[]
  onRemoveAttachment: (id: string) => void
  isRecording: boolean
  isTranscribing: boolean
  onToggleRecording: () => void
  onStopRecording: () => void
  onCancelRecording: () => void
  stream: MediaStream | null
  error?: string | null
  disabled: boolean
  compact?: boolean
  devices?: { deviceId: string; label: string }[]
  selectedDeviceId?: string | null
  onSelectDevice?: (deviceId: string | null) => void
}

export default function ChatInputToolbar({
  onFilesSelected,
  attachments,
  onRemoveAttachment,
  isRecording,
  isTranscribing,
  onToggleRecording,
  onStopRecording,
  onCancelRecording,
  stream,
  error,
  disabled,
  compact,
  devices,
  selectedDeviceId,
  onSelectDevice,
}: ChatInputToolbarProps) {
  const fileInputRef = useRef<HTMLInputElement>(null)
  const [micMenuAnchor, setMicMenuAnchor] = useState<HTMLElement | null>(null)

  const handleFileChange = useCallback((e: React.ChangeEvent<HTMLInputElement>) => {
    const files = e.target.files
    if (files && files.length > 0) {
      onFilesSelected(Array.from(files))
    }
    e.target.value = ''
  }, [onFilesSelected])

  const iconSize = compact ? 18 : 22
  const btnSize = compact ? 28 : 34
  const showDeviceDropdown = devices && devices.length > 1

  return (
    <Box sx={{ display: 'flex', flexDirection: 'column', gap: 0.5 }}>
      {/* Recording overlay — replaces buttons row when recording */}
      {isRecording ? (
        <RecordingOverlay
          stream={stream}
          isRecording={isRecording}
          onStop={onStopRecording}
          onCancel={onCancelRecording}
          compact={compact}
        />
      ) : (
      <>
      {/* Buttons row + inline attachment previews */}
      <Box sx={{ display: 'flex', alignItems: 'center', flexWrap: 'wrap', gap: 0.5 }}>
        {/* Hidden file input */}
        <input
          ref={fileInputRef}
          type="file"
          multiple
          accept={ACCEPTED_TYPES}
          onChange={handleFileChange}
          style={{ display: 'none' }}
        />

        {/* Paperclip button */}
        <Tooltip title="Attach files">
          <span>
            <IconButton
              size="small"
              disabled={disabled}
              onClick={() => fileInputRef.current?.click()}
              sx={{ width: btnSize, height: btnSize, color: 'info.main' }}
            >
              <AttachFileIcon sx={{ fontSize: iconSize }} />
            </IconButton>
          </span>
        </Tooltip>

        {/* Mic button */}
        <Tooltip title={isTranscribing ? 'Transcribing...' : 'Voice input'}>
          <span>
            <IconButton
              size="small"
              disabled={disabled || isTranscribing}
              onClick={onToggleRecording}
              sx={{
                width: btnSize,
                height: btnSize,
                color: 'info.main',
              }}
            >
              {isTranscribing ? (
                <CircularProgress size={iconSize - 4} />
              ) : (
                <MicIcon sx={{ fontSize: iconSize }} />
              )}
            </IconButton>
          </span>
        </Tooltip>

        {/* Mic device dropdown arrow */}
        {showDeviceDropdown && (
          <>
            <IconButton
              size="small"
              disabled={disabled || isRecording || isTranscribing}
              onClick={(e) => setMicMenuAnchor(e.currentTarget)}
              sx={{
                width: compact ? 20 : 24,
                height: btnSize,
                ml: -0.5,
                color: 'info.main',
              }}
            >
              <ArrowDropDownIcon sx={{ fontSize: compact ? 16 : 18 }} />
            </IconButton>
            <Menu
              anchorEl={micMenuAnchor}
              open={Boolean(micMenuAnchor)}
              onClose={() => setMicMenuAnchor(null)}
              slotProps={{ paper: { sx: { maxWidth: 320 } } }}
            >
              {devices.map(d => (
                <MenuItem
                  key={d.deviceId}
                  selected={d.deviceId === selectedDeviceId}
                  onClick={() => {
                    onSelectDevice?.(d.deviceId)
                    setMicMenuAnchor(null)
                  }}
                  sx={{ fontSize: compact ? 12 : 13 }}
                >
                  {d.label}
                </MenuItem>
              ))}
            </Menu>
          </>
        )}

        {/* Inline attachment previews */}
        {attachments.map(a => (
          <Box key={a.id} sx={{ position: 'relative', display: 'inline-flex' }}>
            {a.previewUrl ? (
              <Box sx={{ position: 'relative' }}>
                <Box
                  component="img"
                  src={a.previewUrl}
                  alt={a.file.name}
                  sx={{
                    width: compact ? 28 : 34,
                    height: compact ? 28 : 34,
                    borderRadius: '6px',
                    objectFit: 'cover',
                    border: '1px solid #e5e7eb',
                    opacity: a.uploadState === 'uploading' ? 0.5 : 1,
                  }}
                />
                {a.uploadState === 'uploading' && (
                  <CircularProgress
                    size={14}
                    sx={{ position: 'absolute', top: '50%', left: '50%', mt: '-7px', ml: '-7px' }}
                  />
                )}
                <IconButton
                  size="small"
                  onClick={() => onRemoveAttachment(a.id)}
                  sx={{
                    position: 'absolute',
                    top: -6,
                    right: -6,
                    width: 16,
                    height: 16,
                    backgroundColor: '#fff',
                    border: '1px solid #d1d5db',
                    '&:hover': { backgroundColor: '#f3f4f6' },
                  }}
                >
                  <CloseIcon sx={{ fontSize: 10 }} />
                </IconButton>
              </Box>
            ) : (
              <Chip
                label={a.file.name}
                size="small"
                onDelete={() => onRemoveAttachment(a.id)}
                color={a.uploadState === 'error' ? 'error' : 'default'}
                sx={{ fontSize: compact ? 11 : 12, maxWidth: 160 }}
              />
            )}
          </Box>
        ))}
      </Box>

      {/* Error display */}
      {error && (
        <Typography sx={{ fontSize: compact ? 11 : 12, color: '#ef4444', px: 0.5 }}>
          {error}
        </Typography>
      )}
      </>
      )}
    </Box>
  )
}
