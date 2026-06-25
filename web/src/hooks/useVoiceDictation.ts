// Voice dictation hook.
// Copied + factored from frontend/src/hooks/useVoiceDictation.ts.
// Transcription endpoint is parameterised via options instead of a hard-coded
// Platinum route. See ../../docs/90-provenance-map.md.

import { useCallback, useRef, useState } from 'react'

interface AudioDevice {
  deviceId: string
  label: string
}

export interface UseVoiceDictationOptions {
  onTranscription: (text: string) => void
  /** Endpoint to POST audio for transcription. Default: /transcribe */
  transcribeEndpoint?: string
  /** Auth header value (e.g. "Bearer <token>"). If provided, sent with POST. */
  authHeader?: string
}

function loadPreferredDevice(): string | null {
  try {
    return localStorage.getItem('preferred-mic-device-id')
  } catch {
    return null
  }
}

function savePreferredDevice(deviceId: string | null) {
  try {
    if (deviceId) {
      localStorage.setItem('preferred-mic-device-id', deviceId)
    } else {
      localStorage.removeItem('preferred-mic-device-id')
    }
  } catch {
    // localStorage unavailable
  }
}

export default function useVoiceDictation({ onTranscription, transcribeEndpoint = '/transcribe', authHeader }: UseVoiceDictationOptions) {
  const [isRecording, setIsRecording] = useState(false)
  const [isTranscribing, setIsTranscribing] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [stream, setStream] = useState<MediaStream | null>(null)
  const [devices, setDevices] = useState<AudioDevice[]>([])
  const [selectedDeviceId, setSelectedDeviceId] = useState<string | null>(loadPreferredDevice)
  const mediaRecorderRef = useRef<MediaRecorder | null>(null)
  const chunksRef = useRef<Blob[]>([])
  const audioCtxRef = useRef<AudioContext | null>(null)

  const refreshDevices = useCallback(async () => {
    try {
      const allDevices = await navigator.mediaDevices.enumerateDevices()
      const audioInputs = allDevices
        .filter(d => d.kind === 'audioinput' && d.deviceId)
        .map(d => ({ deviceId: d.deviceId, label: d.label || `Microphone (${d.deviceId.slice(0, 8)})` }))
      setDevices(audioInputs)
    } catch {
      // enumerateDevices not available
    }
  }, [])

  const selectDevice = useCallback((deviceId: string | null) => {
    setSelectedDeviceId(deviceId)
    savePreferredDevice(deviceId)
  }, [])

  const startRecording = useCallback(async () => {
    setError(null)

    const baseConstraints: MediaTrackConstraints = {
      autoGainControl: true,
      noiseSuppression: true,
      echoCancellation: false,
    }
    const audioConstraint: MediaTrackConstraints = selectedDeviceId
      ? { ...baseConstraints, deviceId: { exact: selectedDeviceId } }
      : baseConstraints

    let mediaStream: MediaStream
    try {
      mediaStream = await navigator.mediaDevices.getUserMedia({ audio: audioConstraint })
    } catch (err) {
      // If the stored device is gone, fall back to default
      if (selectedDeviceId && (err as DOMException).name === 'OverconstrainedError') {
        savePreferredDevice(null)
        setSelectedDeviceId(null)
        try {
          mediaStream = await navigator.mediaDevices.getUserMedia({ audio: true })
        } catch (fallbackErr) {
          if ((fallbackErr as Error).name === 'NotAllowedError') {
            setError('Microphone permission denied')
          } else {
            setError('Failed to start recording')
          }
          return
        }
      } else if ((err as Error).name === 'NotAllowedError') {
        setError('Microphone permission denied')
        return
      } else {
        setError('Failed to start recording')
        return
      }
    }

    // Refresh device list after successful getUserMedia (labels now available)
    refreshDevices()

    setStream(mediaStream)

    // Boost audio volume via Web Audio API gain node
    const audioCtx = new AudioContext()
    audioCtxRef.current = audioCtx
    const source = audioCtx.createMediaStreamSource(mediaStream)
    const gainNode = audioCtx.createGain()
    gainNode.gain.value = 2.5
    const destination = audioCtx.createMediaStreamDestination()
    source.connect(gainNode)
    gainNode.connect(destination)

    const recorder = new MediaRecorder(destination.stream, { mimeType: 'audio/webm;codecs=opus' })
    mediaRecorderRef.current = recorder
    chunksRef.current = []

    recorder.ondataavailable = (e) => {
      if (e.data.size > 0) chunksRef.current.push(e.data)
    }

    recorder.onstop = async () => {
      // Clean up stream and audio context
      mediaStream.getTracks().forEach(t => t.stop())
      setStream(null)
      audioCtxRef.current?.close()
      audioCtxRef.current = null

      if (chunksRef.current.length === 0) return

      const blob = new Blob(chunksRef.current, { type: 'audio/webm' })
      setIsTranscribing(true)
      try {
        const formData = new FormData()
        formData.append('audio', blob, 'recording.webm')
        const headers: Record<string, string> = {}
        if (authHeader) headers['Authorization'] = authHeader
        const resp = await fetch(transcribeEndpoint, {
          method: 'POST',
          body: formData,
          headers,
        })
        if (!resp.ok) {
          const errorData = await resp.json().catch(() => ({})) as { error?: string }
          setError(errorData.error || 'Transcription failed')
          return
        }
        const data = await resp.json() as { text: string }
        const trimmed = data.text.trim()
        if (trimmed) onTranscription(trimmed)
      } catch {
        setError('Transcription failed')
      } finally {
        setIsTranscribing(false)
      }
    }

    recorder.start(250) // collect in 250ms chunks
    setIsRecording(true)
  }, [onTranscription, selectedDeviceId, refreshDevices, transcribeEndpoint, authHeader])

  const stopRecording = useCallback(() => {
    if (mediaRecorderRef.current?.state === 'recording') {
      mediaRecorderRef.current.stop()
    }
    setIsRecording(false)
  }, [])

  const cancelRecording = useCallback(() => {
    chunksRef.current = []
    if (mediaRecorderRef.current?.state === 'recording') {
      mediaRecorderRef.current.stop()
    }
    // Clean up the stream and audio context
    setStream(current => {
      current?.getTracks().forEach(t => t.stop())
      return null
    })
    audioCtxRef.current?.close()
    audioCtxRef.current = null
    setIsRecording(false)
  }, [])

  return {
    isRecording, isTranscribing, error, stream,
    startRecording, stopRecording, cancelRecording,
    devices, selectedDeviceId, selectDevice,
  }
}
