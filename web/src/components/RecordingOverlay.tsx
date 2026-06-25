// Recording overlay component shown during voice input.
// Copied from frontend/src/components/agent/RecordingOverlay.tsx.
// Generic — no Platinum-specific dependencies.
// See ../../docs/90-provenance-map.md.

import React, { useCallback, useRef, useState } from 'react'
import { Box, Button, Typography } from '@mui/material'
import StopIcon from '@mui/icons-material/Stop'
import CloseIcon from '@mui/icons-material/Close'

interface RecordingOverlayProps {
  stream: MediaStream | null
  isRecording: boolean
  onStop: () => void
  onCancel: () => void
  compact?: boolean
}

export default function RecordingOverlay({ stream, isRecording, onStop, onCancel, compact }: RecordingOverlayProps) {
  const canvasRef = useRef<HTMLCanvasElement>(null)
  const audioCtxRef = useRef<AudioContext | null>(null)
  const analyserRef = useRef<AnalyserNode | null>(null)
  const sourceRef = useRef<MediaStreamAudioSourceNode | null>(null)
  const animFrameRef = useRef<number>(0)
  const timerFrameRef = useRef<number>(0)
  const startTimeRef = useRef<number>(0)
  const [elapsed, setElapsed] = useState(0)
  const initializedRef = useRef(false)

  const cleanup = useCallback(() => {
    if (animFrameRef.current) cancelAnimationFrame(animFrameRef.current)
    if (timerFrameRef.current) cancelAnimationFrame(timerFrameRef.current)
    animFrameRef.current = 0
    timerFrameRef.current = 0
    sourceRef.current?.disconnect()
    sourceRef.current = null
    audioCtxRef.current?.close()
    audioCtxRef.current = null
    analyserRef.current = null
    initializedRef.current = false
    setElapsed(0)
  }, [])

  const handleStop = useCallback(() => {
    cleanup()
    onStop()
  }, [cleanup, onStop])

  const handleCancel = useCallback(() => {
    cleanup()
    onCancel()
  }, [cleanup, onCancel])

  // Initialize audio analyser and start animation when stream is available
  if (stream && isRecording && !initializedRef.current) {
    initializedRef.current = true
    startTimeRef.current = Date.now()

    const audioCtx = new AudioContext()
    audioCtxRef.current = audioCtx
    if (audioCtx.state === 'suspended') {
      audioCtx.resume()
    }
    const analyser = audioCtx.createAnalyser()
    analyser.fftSize = 128
    analyser.smoothingTimeConstant = 0.6
    analyserRef.current = analyser
    const source = audioCtx.createMediaStreamSource(stream)
    source.connect(analyser)
    sourceRef.current = source

    const dataArray = new Uint8Array(analyser.frequencyBinCount)

    const draw = () => {
      const canvas = canvasRef.current
      if (!canvas) {
        animFrameRef.current = requestAnimationFrame(draw)
        return
      }
      const ctx = canvas.getContext('2d')
      if (!ctx) return

      analyser.getByteFrequencyData(dataArray)
      const w = canvas.width
      const h = canvas.height
      ctx.clearRect(0, 0, w, h)

      const barCount = dataArray.length
      const gap = 2
      const barWidth = Math.max(2, (w - gap * (barCount - 1)) / barCount)

      for (let i = 0; i < barCount; i++) {
        const val = dataArray[i] / 255
        const barHeight = Math.max(2, val * h * 0.9)
        const x = i * (barWidth + gap)
        const y = (h - barHeight) / 2

        ctx.fillStyle = `rgba(59, 130, 246, ${0.4 + val * 0.6})`
        ctx.beginPath()
        ctx.roundRect(x, y, barWidth, barHeight, 1)
        ctx.fill()
      }

      animFrameRef.current = requestAnimationFrame(draw)
    }
    draw()

    const tick = () => {
      setElapsed(Math.floor((Date.now() - startTimeRef.current) / 1000))
      timerFrameRef.current = requestAnimationFrame(tick)
    }
    tick()
  }

  if (!isRecording) return null

  const height = compact ? 48 : 56
  const minutes = Math.floor(elapsed / 60)
  const seconds = elapsed % 60
  const timeStr = `${minutes}:${seconds.toString().padStart(2, '0')}`

  return (
    <Box
      sx={{
        display: 'flex',
        alignItems: 'center',
        gap: compact ? 1 : 1.5,
        px: compact ? 1.5 : 2,
        py: 0.75,
        height,
        backgroundColor: 'rgba(239, 68, 68, 0.06)',
        borderRadius: '10px',
        border: '1px solid rgba(239, 68, 68, 0.2)',
      }}
    >
      <Box sx={{ display: 'flex', alignItems: 'center', gap: 0.75, flexShrink: 0 }}>
        <Box
          sx={{
            width: 8,
            height: 8,
            borderRadius: '50%',
            backgroundColor: '#ef4444',
            animation: 'recDotPulse 1s ease-in-out infinite',
            '@keyframes recDotPulse': {
              '0%, 100%': { opacity: 1 },
              '50%': { opacity: 0.3 },
            },
          }}
        />
        <Typography sx={{ fontSize: compact ? 12 : 13, fontWeight: 600, color: '#ef4444', whiteSpace: 'nowrap' }}>
          Recording
        </Typography>
        <Typography sx={{ fontSize: compact ? 11 : 12, color: '#9ca3af', fontVariantNumeric: 'tabular-nums', whiteSpace: 'nowrap' }}>
          {timeStr}
        </Typography>
      </Box>

      <Box sx={{ flex: 1, minWidth: 0, height: '100%', display: 'flex', alignItems: 'center' }}>
        <canvas
          ref={canvasRef}
          width={300}
          height={compact ? 30 : 36}
          style={{ width: '100%', height: compact ? 30 : 36 }}
        />
      </Box>

      <Box sx={{ display: 'flex', gap: 0.5, flexShrink: 0 }}>
        <Button
          variant="contained"
          color="info"
          size="small"
          onClick={handleStop}
          startIcon={<StopIcon sx={{ fontSize: compact ? 14 : 16 }} />}
          sx={{
            borderRadius: '8px',
            textTransform: 'none',
            fontSize: compact ? 11 : 12,
            minWidth: 0,
            px: compact ? 1 : 1.5,
            py: 0.25,
          }}
        >
          Done
        </Button>
        <Button
          variant="outlined"
          color="inherit"
          size="small"
          onClick={handleCancel}
          sx={{
            borderRadius: '8px',
            textTransform: 'none',
            fontSize: compact ? 11 : 12,
            minWidth: 0,
            px: compact ? 0.75 : 1,
            py: 0.25,
            color: '#6b7280',
            borderColor: '#d1d5db',
          }}
        >
          <CloseIcon sx={{ fontSize: compact ? 14 : 16 }} />
        </Button>
      </Box>
    </Box>
  )
}
