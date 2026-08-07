// WorkerPromptVersion — the Configuration tab, folded to a past version
// (design §7.1: "click v11 and the Configuration tab shows the prompt as it
// was").
//
// Read-only by construction: it renders text and offers one action, and that
// action is a *forward* write. "Restore this version" pre-fills the ordinary
// editor with the old text and a rationale naming the config event; saving it
// appends a new prompt write like any other. There is no revert path, because
// the log has no undo and pretending otherwise would be a lie about history.

import { useEffect, useState } from 'react'
import { Alert, Box, Button, Paper, Stack, Typography } from '@mui/material'
import { formatConfigTimestamp } from '../configLog.js'
import { usePrefersReducedMotion } from '../useReducedMotion.js'
import type { LineageVersion } from './WorkerLineage.js'

export interface WorkerPromptVersionProps {
  workerName: string
  version: LineageVersion
  /** Pre-fill the editor with this prompt and a rationale naming the event. */
  onRestore: () => void
  /** Leave history and go back to the live prompt. */
  onClose: () => void
}

/** The pre-filled reason for a restore — names the config event it came from. */
export function restoreRationale(version: LineageVersion): string {
  return `Restoring v${version.version} of the prompt (config event ${version.eventId}).`
}

export default function WorkerPromptVersion({
  workerName,
  version,
  onRestore,
  onClose,
}: WorkerPromptVersionProps) {
  // Folding to a version used to teleport: the live prompt was replaced with a
  // different prompt in the same box, and the operator lost their place. The
  // history slides out from under the banner instead — 150ms, the shortest
  // duration that reads as "this came from up there" (§5 / doc 21 §3).
  const reduced = usePrefersReducedMotion()
  const [entered, setEntered] = useState(false)
  useEffect(() => {
    if (reduced || typeof requestAnimationFrame !== 'function') {
      setEntered(true)
      return
    }
    const id = requestAnimationFrame(() => setEntered(true))
    return () => cancelAnimationFrame(id)
  }, [reduced])

  return (
    <Box sx={{ p: 3, maxWidth: 880 }}>
      <Alert severity="info" sx={{ mb: 2 }} data-testid="version-banner">
        Viewing v{version.version} as of {formatConfigTimestamp(version.at)} — this is history, not
        the live prompt
      </Alert>

      <Box
        data-testid="version-fold"
        data-entered={entered ? 'true' : 'false'}
        sx={{
          overflow: 'hidden',
          opacity: entered ? 1 : 0,
          transform: entered ? 'translateY(0)' : 'translateY(-8px)',
          transition: reduced ? 'none' : 'opacity 150ms ease-out, transform 150ms ease-out',
        }}
      >
      <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mb: 1 }}>
        The system prompt of <code>{workerName}</code>, as this version wrote it.
      </Typography>
      <Paper
        variant="outlined"
        sx={{
          p: 1.5,
          maxHeight: 480,
          overflow: 'auto',
          whiteSpace: 'pre-wrap',
          fontFamily: 'monospace',
          fontSize: 13,
        }}
      >
        {version.prompt}
      </Paper>

      <Stack direction="row" spacing={1} sx={{ mt: 2 }}>
        <Button size="small" variant="contained" onClick={onRestore}>
          Restore this version
        </Button>
        <Button size="small" onClick={onClose}>
          Back to the live prompt
        </Button>
      </Stack>
      <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mt: 1 }}>
        Restoring writes this text forward as a new version, with a reason naming the change it
        came from. Nothing is erased.
      </Typography>
      </Box>
    </Box>
  )
}
