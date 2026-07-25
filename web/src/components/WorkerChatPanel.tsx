// WorkerChatPanel — "chat with this worker" (spec §6.4).
//
// Interactive chat with a worker is *not* a second kind of chat: it is a job
// whose triggering event is the human's message and whose session stays
// interactive. So this component starts a session tagged with the worker and
// then renders the ordinary <AgentChat/> against the ordinary provider context.
// There is deliberately no second reducer, no second transport, no second
// message list — if this file ever grows one, the invariant has been broken.
//
// The prompt is composed **server-side** from the core preamble + project prompt
// + worker prompt. This component never sends the worker's prompt itself: a
// browser that could name a worker *and* supply its beliefs would make the
// worker catalogue advisory. Servers that predate job composition ignore the
// `worker` field and give back a plain session — the degradation is a session
// without the worker's prompt, never a session with a forged one.

import React, { useCallback, useState } from 'react'
import { Alert, Box, Button, CircularProgress, Stack, Typography } from '@mui/material'
import ChatIcon from '@mui/icons-material/Chat'
import { useAgentChat } from '../AgentChatProvider.js'
import AgentChat from './AgentChat.js'
import type { Worker } from '../workers.js'

export interface WorkerChatPanelProps {
  /** The worker to talk to. */
  worker: Worker
  /** Project id — the `customer` on the created session. */
  projectId: string
  /** Optional job label carried onto the session row. */
  job?: string
  /** Called with the new session id once the chat has started. */
  onSessionStarted?: (sessionId: string) => void
}

export default function WorkerChatPanel({
  worker,
  projectId,
  job,
  onSessionStarted,
}: WorkerChatPanelProps) {
  const { createSession, isCreating, session } = useAgentChat()
  const [startedId, setStartedId] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  const start = useCallback(async () => {
    setError(null)
    const id = await createSession({ customer: projectId, job, worker: worker.name })
    if (id === null) {
      setError(`Could not start a chat with ${worker.name}.`)
      return
    }
    setStartedId(id)
    onSessionStarted?.(id)
  }, [createSession, job, onSessionStarted, projectId, worker.name])

  // Show the chat once this panel has started one, or when the provider already
  // has the session we started (e.g. the human opened a past job from history).
  const showChat = startedId !== null && session !== null

  if (!showChat) {
    return (
      <Box sx={{ p: 3 }}>
        <Stack spacing={2} alignItems="flex-start">
          <Typography variant="body2" color="text.secondary">
            Start an interactive job with <strong>{worker.name}</strong>. Its prompt, tools and
            briefing are composed by the server exactly as they would be for an automated job.
          </Typography>
          {error !== null && <Alert severity="error">{error}</Alert>}
          <Button
            variant="contained"
            startIcon={isCreating ? <CircularProgress size={16} /> : <ChatIcon />}
            disabled={isCreating}
            onClick={() => void start()}
          >
            {isCreating ? 'Starting…' : `Chat with ${worker.name}`}
          </Button>
        </Stack>
      </Box>
    )
  }

  return (
    <Box sx={{ height: '100%', display: 'flex', flexDirection: 'column' }}>
      <AgentChat />
    </Box>
  )
}
