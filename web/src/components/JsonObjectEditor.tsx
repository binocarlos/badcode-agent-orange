// JsonObjectEditor — a monospaced textarea for a JSON *object* field, with the
// parse error shown inline under the box.
//
// Shared by the project-settings page and the worker editor because both edit
// an `mcp_config` map with identical rules. The parsing itself lives in
// projectSettings.ts (pure, tested); this component only renders text, an error
// and a "format" affordance.
//
// Deliberately not a code editor: no CodeMirror, no Monaco, no syntax
// highlighting. The spec's words for this screen are "nothing clever", and a
// 200 KB editor dependency inside a component library a host embeds is the
// opposite of that.

import React from 'react'
import { Box, Button, FormHelperText, Stack, TextField, Typography } from '@mui/material'
import { formatJsonObject, parseJsonObject } from '../projectSettings.js'

export interface JsonObjectEditorProps {
  label: string
  /** Current editor text (the source of truth while editing). */
  value: string
  onChange: (text: string) => void
  /** Parse error to display, or null when the text is a valid JSON object. */
  error: string | null
  /** One-line explanation shown under the box when there is no error. */
  helperText?: string
  rows?: number
  disabled?: boolean
  /** Stable id, so the label/helper wiring and tests have something to grab. */
  id?: string
}

export default function JsonObjectEditor({
  label,
  value,
  onChange,
  error,
  helperText,
  rows = 10,
  disabled = false,
  id,
}: JsonObjectEditorProps) {
  const canFormat = !disabled && parseJsonObject(value).ok

  return (
    <Box>
      <Stack direction="row" alignItems="center" justifyContent="space-between" sx={{ mb: 0.5 }}>
        <Typography variant="subtitle2" color="text.primary">
          {label}
        </Typography>
        <Button
          size="small"
          disabled={!canFormat}
          onClick={() => {
            const parsed = parseJsonObject(value)
            if (parsed.ok) onChange(formatJsonObject(parsed.value))
          }}
        >
          Format
        </Button>
      </Stack>
      <TextField
        id={id}
        inputProps={{ 'aria-label': label, spellCheck: false }}
        multiline
        minRows={rows}
        maxRows={rows * 3}
        fullWidth
        value={value}
        disabled={disabled}
        error={error !== null}
        onChange={(e) => onChange(e.target.value)}
        sx={{ '& textarea': { fontFamily: 'monospace', fontSize: '0.8125rem' } }}
      />
      <FormHelperText error={error !== null}>
        {error !== null ? `Invalid JSON: ${error}` : helperText}
      </FormHelperText>
    </Box>
  )
}
