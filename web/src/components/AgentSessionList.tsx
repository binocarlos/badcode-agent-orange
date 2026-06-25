// AgentSessionList — lists sessions from the nearest AgentChatProvider.
//
// Calls refresh() on mount via a render-phase ref-guard (per CLAUDE.md rule 5:
// useEffect is only for cleanup/lifecycle — one-shot init uses a ref-guard).
//
// Uses MUI List + semantic theme tokens. Delete button uses info (blue) theme.

import React, { useRef } from 'react'
import {
  List,
  ListItem,
  ListItemButton,
  ListItemText,
  IconButton,
  Tooltip,
  Typography,
  Box,
} from '@mui/material'
import DeleteIcon from '@mui/icons-material/Delete'
import { useAgentSessions } from '../AgentChatProvider.js'

export default function AgentSessionList() {
  const { sessions, refresh, select, delete: deleteSession } = useAgentSessions()

  // Render-phase ref-guard: call refresh() once on first render.
  // NOT useEffect — per CLAUDE.md rule 5. No cleanup needed; no lifecycle trigger.
  const didRefresh = useRef(false)
  if (!didRefresh.current) {
    didRefresh.current = true
    refresh()
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

  return (
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
                  deleteSession(session.id)
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
  )
}
