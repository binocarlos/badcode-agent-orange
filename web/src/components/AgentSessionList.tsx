// AgentSessionList — lists sessions from the nearest AgentChatProvider.
//
// Calls refresh() on mount via a render-phase ref-guard (per CLAUDE.md rule 5:
// useEffect is only for cleanup/lifecycle — one-shot init uses a ref-guard).
//
// Uses MUI List + semantic theme tokens.
//
// Deleting ASKS FIRST, and the question names what goes (doc 22 RD5). The
// button used to be a bare icon wired straight to the DELETE: one click, from a
// list, destroyed the entire conversation — every message the user and the model
// had exchanged and the index to the session's artifacts — with no undo. The
// dialog is the guard; the server's soft delete (migration 041) is the safety
// net behind it, and neither replaces the other.

import { useRef, useState } from 'react'
import {
  Alert,
  List,
  ListItem,
  ListItemButton,
  ListItemText,
  IconButton,
  Tooltip,
  Typography,
  Box,
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
} from '@mui/material'
import DeleteIcon from '@mui/icons-material/Delete'
import { useAgentSessions } from '../AgentChatProvider.js'
import type { AgentSessionListItem } from '../types.js'

export default function AgentSessionList() {
  const { sessions, refresh, select, delete: deleteSession } = useAgentSessions()

  // Render-phase ref-guard: call refresh() once on first render.
  // NOT useEffect — per CLAUDE.md rule 5. No cleanup needed; no lifecycle trigger.
  const didRefresh = useRef(false)
  if (!didRefresh.current) {
    didRefresh.current = true
    refresh()
  }

  // The session awaiting confirmation. null = no dialog open. Holding the whole
  // row (not just the id) is what lets the question name the thing being lost.
  const [pending, setPending] = useState<AgentSessionListItem | null>(null)
  const [deleting, setDeleting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const confirmDelete = async () => {
    if (pending === null) return
    setDeleting(true)
    setError(null)
    try {
      await deleteSession(pending.id)
      setPending(null)
    } catch (err) {
      // The server's own text. A delete that failed must say so here rather
      // than close the dialog and let the row reappear on the next refresh —
      // that is RD5's second defect (a failed delete reporting success) in the
      // browser instead of the handler.
      setError(err instanceof Error ? err.message : 'failed to delete the session')
    } finally {
      setDeleting(false)
    }
  }

  if (sessions.length === 0) {
    return (
      <Box sx={{ p: 2 }}>
        <Typography variant="body2" color="text.secondary">
          No sessions yet.
        </Typography>
      </Box>
    )
  }

  const pendingTitle = pending === null ? '' : pending.title || 'Untitled'

  return (
    <>
      <List disablePadding>
        {sessions.map((session) => (
          <ListItem
            key={session.id}
            disablePadding
            secondaryAction={
              <Tooltip title="Delete session">
                <IconButton
                  edge="end"
                  size="small"
                  color="info"
                  onClick={(e) => {
                    e.stopPropagation()
                    setError(null)
                    setPending(session)
                  }}
                  aria-label={`Delete ${session.title ?? session.id}`}
                >
                  <DeleteIcon fontSize="small" />
                </IconButton>
              </Tooltip>
            }
          >
            <ListItemButton onClick={() => select(session.id)}>
              <ListItemText
                primary={session.title || 'Untitled'}
                primaryTypographyProps={{
                  variant: 'body2',
                  noWrap: true,
                  sx: { color: 'text.primary' },
                }}
              />
            </ListItemButton>
          </ListItem>
        ))}
      </List>

      <Dialog
        open={pending !== null}
        onClose={() => (deleting ? undefined : setPending(null))}
        aria-labelledby="delete-session-title"
      >
        <DialogTitle id="delete-session-title">Delete “{pendingTitle}”?</DialogTitle>
        <DialogContent>
          <DialogContentText component="div">
            <p>
              This deletes the <strong>whole conversation</strong>: every message you and the
              model exchanged in this session, and the index to
              {' '}
              {pending !== null && pending.artifact_count > 0
                ? `its ${pending.artifact_count} file${pending.artifact_count === 1 ? '' : 's'}`
                : 'any files it produced'}
              . Its container is destroyed and the session stops accepting messages.
            </p>
            <p>
              <strong>You cannot undo this from here.</strong> Nothing else in the app can bring
              the session back or export it first — so if the transcript matters, copy what you
              need before deleting.
            </p>
          </DialogContentText>
          {error !== null && (
            <Alert severity="error" sx={{ mt: 1 }}>
              {error}
            </Alert>
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setPending(null)} disabled={deleting}>
            Keep it
          </Button>
          <Button
            variant="contained"
            color="error"
            onClick={() => void confirmDelete()}
            disabled={deleting}
          >
            {deleting ? 'Deleting…' : 'Delete the conversation'}
          </Button>
        </DialogActions>
      </Dialog>
    </>
  )
}
