// NlAssistField — "describe it in words → see what that compiles to → apply".
// Work-plan item F2, learning L28.
//
// The whole point is the middle step. The compiler proposes; a human reads the
// proposal in plain English and clicks Apply; only then does anything reach the
// draft, and what reaches the draft is the compiled artifact — a cron
// expression, a filter object — which is what gets saved and what the engine
// executes. Nothing in this component runs when a schedule fires.
//
// So: no auto-apply on blur, no "did you mean" that silently rewrites a field,
// and a refusal is shown as a refusal rather than as a best guess. A wrong cron
// that parses is the expensive failure, because it runs wrong every day until
// somebody notices.

import React, { useState } from 'react'
import { Alert, Box, Button, Stack, TextField, Typography } from '@mui/material'
import type { CompileResult } from '../nlAssist.js'

export interface NlAssistFieldProps<T> {
  /** Field label, also the input's aria-label. */
  label: string
  placeholder: string
  /** Sentence under the input explaining what the assist can understand. */
  helperText: string
  /** The compiler. Sync or async — a host may inject a model (see nlAssist.ts). */
  compile: (text: string) => CompileResult<T> | Promise<CompileResult<T>>
  /** Called only when the human accepts the proposal. */
  onApply: (value: T) => void
  /** Button label for accepting. Default "Apply". */
  applyLabel?: string
  /** Extra detail about a successful proposal, rendered under the sentence. */
  renderProposal?: (value: T) => React.ReactNode
}

export default function NlAssistField<T>({
  label,
  placeholder,
  helperText,
  compile,
  onApply,
  applyLabel = 'Apply',
  renderProposal,
}: NlAssistFieldProps<T>) {
  const [text, setText] = useState('')
  const [result, setResult] = useState<CompileResult<T> | null>(null)
  const [busy, setBusy] = useState(false)

  const preview = async () => {
    setBusy(true)
    try {
      setResult(await compile(text))
    } catch (err) {
      setResult({ ok: false, error: err instanceof Error ? err.message : 'could not compile that' })
    } finally {
      setBusy(false)
    }
  }

  return (
    <Box>
      <Stack direction="row" spacing={1} alignItems="flex-start">
        <TextField
          label={label}
          fullWidth
          size="small"
          value={text}
          placeholder={placeholder}
          onChange={(e) => {
            setText(e.target.value)
            // A stale proposal next to edited text is how the wrong thing gets
            // applied: the preview belongs to the words that produced it.
            setResult(null)
          }}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault()
              void preview()
            }
          }}
          inputProps={{ 'aria-label': label }}
        />
        <Button
          onClick={() => void preview()}
          disabled={busy || text.trim() === ''}
          sx={{ mt: 0.5, flexShrink: 0 }}
        >
          {busy ? 'Working…' : 'Preview'}
        </Button>
      </Stack>

      <Typography variant="caption" color="text.secondary" component="div" sx={{ mt: 0.5 }}>
        {helperText}
      </Typography>

      {result !== null && !result.ok && (
        <Alert severity="warning" sx={{ mt: 1 }}>
          {result.error}
        </Alert>
      )}

      {result !== null && result.ok && (
        <Alert
          severity="info"
          sx={{ mt: 1 }}
          action={
            <Button
              size="small"
              onClick={() => {
                onApply(result.value)
                setResult(null)
                setText('')
              }}
            >
              {applyLabel}
            </Button>
          }
        >
          <Stack spacing={0.5}>
            <span>{result.explanation}</span>
            {renderProposal?.(result.value)}
            <Typography variant="caption" color="text.secondary">
              Nothing is saved until you apply this and save the form.
            </Typography>
          </Stack>
        </Alert>
      )}
    </Box>
  )
}
