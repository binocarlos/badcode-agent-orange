// Pure event replay utilities.
// Copied verbatim (shape) from frontend/src/utils/replayEvents.ts.
// The single-reducer invariant: the same agentEventReducer processes
// live SSE events AND persisted/replayed events — there is never a
// second reconstruction path.
// See ../../docs/09-frontend-components.md.

import { agentEventReducer, initialAgentEventState, type AgentEventState } from './agentEventReducer.js'
import type { AgentSSEEvent, AgentMessage, PersistedAgentMessage } from './types.js'

/**
 * Replay compacted events through the reducer to produce display state.
 * This is the core of event-based session restoration — the same reducer
 * that processes live SSE events also processes stored events.
 *
 * Post-processing merges fragmented assistant messages that were split
 * during live streaming at arbitrary chunk boundaries. The reducer splits
 * at tool call boundaries (content_delta after tool_use_start), but on
 * restore the final state should show coherent text blocks.
 */
export function replayEvents(events: AgentSSEEvent[]): AgentEventState {
  let state = initialAgentEventState()
  for (const event of events) {
    state = agentEventReducer(state, event)
  }
  // Post-processing: merge fragmented assistant messages
  state = mergeAssistantFragments(state)
  // Post-processing: mark ask-user questions as answered
  state = markAnsweredQuestions(state)
  // Clear transient streaming state
  state.isStreaming = false
  state.activityStatus = null
  state.currentMessage = null
  state.toolInputBuffer = ''
  return state
}

/**
 * Merge consecutive assistant message fragments produced by the reducer's
 * continuation splitting logic. During live streaming, content_delta after
 * a tool_use_start creates a new "continuation" message. On restore, we
 * merge these back together while preserving text→tools→text interleaving:
 *
 * - Two consecutive assistant msgs with no tools on the first → merge content/thinking
 * - Assistant with tools followed by assistant with text → keep separate (interleaving)
 * - Artifact paths are accumulated during merging
 */
function mergeAssistantFragments(state: AgentEventState): AgentEventState {
  const messages = state.messages
  if (messages.length <= 1) return state

  const merged: typeof messages = []
  for (const msg of messages) {
    const prev = merged[merged.length - 1]
    if (msg.role === 'assistant' && prev?.role === 'assistant') {
      const hasNewText = !!msg.content?.trim()
      const prevHasTools = (prev.toolCalls?.length ?? 0) > 0

      if (hasNewText && prevHasTools) {
        // New text after tools = preserve as separate visual segment
        merged.push(msg)
      } else {
        // Fragment: merge thinking, content, tools, artifact paths
        const mergedMsg = { ...prev }
        if (msg.thinking) {
          mergedMsg.thinking = mergedMsg.thinking ? mergedMsg.thinking + msg.thinking : msg.thinking
        }
        if (msg.content) {
          mergedMsg.content = mergedMsg.content ? mergedMsg.content + msg.content : msg.content
        }
        if (msg.toolCalls?.length) {
          mergedMsg.toolCalls = [...(mergedMsg.toolCalls || []), ...msg.toolCalls]
        }
        if (msg.autoArtifactPaths?.length) {
          mergedMsg.autoArtifactPaths = [...(mergedMsg.autoArtifactPaths || []), ...msg.autoArtifactPaths]
        }
        merged[merged.length - 1] = mergedMsg
      }
    } else {
      merged.push(msg)
    }
  }

  return { ...state, messages: merged }
}

/**
 * Scan for ask_user questions that have a subsequent user message.
 * During live streaming, answers are tracked by the UI; on replay,
 * we detect them by sequence (question -> user message = answered).
 */
function markAnsweredQuestions(state: AgentEventState): AgentEventState {
  if (state.askedQuestions.size === 0) return state

  const updatedQuestions = new Map(state.askedQuestions)
  const messages = state.messages

  // For each question, find if there's a user message after it in the timeline
  for (const [toolCallId, question] of updatedQuestions) {
    if (question.answered) continue

    // Find the message containing this tool call
    let questionIndex = -1
    for (let i = 0; i < messages.length; i++) {
      const msg = messages[i]
      if (msg.toolCalls?.some(tc => tc.id === toolCallId)) {
        questionIndex = i
        break
      }
    }

    if (questionIndex === -1) continue

    // Look for a subsequent user message
    for (let j = questionIndex + 1; j < messages.length; j++) {
      if (messages[j].role === 'user') {
        updatedQuestions.set(toolCallId, {
          ...question,
          answered: true,
          selectedValue: messages[j].content,
        })
        break
      }
    }
  }

  return { ...state, askedQuestions: updatedQuestions }
}

/**
 * Convert persisted agent_messages rows to synthetic SSE events.
 * This is a FROZEN backward-compat layer for sessions that predate event storage.
 * New features only produce events — this function never needs updating.
 */
export function persistedToEvents(messages: PersistedAgentMessage[]): AgentSSEEvent[] {
  const sorted = [...messages].sort((a, b) => a.sequence_num - b.sequence_num)
  const events: AgentSSEEvent[] = []
  let messageStartOpen = false

  for (let i = 0; i < sorted.length; i++) {
    const msg = sorted[i]
    const timestamp = msg.created_at
      ? new Date(msg.created_at * 1000).toISOString()
      : new Date().toISOString()

    switch (msg.role) {
      case 'user': {
        if (messageStartOpen) {
          events.push({ type: 'message_end', data: {}, timestamp })
          messageStartOpen = false
        }
        events.push({
          type: 'user_message',
          data: { id: msg.id, content: msg.content },
          timestamp,
        })
        break
      }

      case 'assistant': {
        if (messageStartOpen) {
          events.push({ type: 'message_end', data: {}, timestamp })
          messageStartOpen = false
        }

        const messageId = `restored-${msg.id}`

        events.push({
          type: 'message_start',
          data: { role: 'assistant', messageId },
          timestamp,
        })
        messageStartOpen = true

        const thinking = msg.metadata?.thinking as string | undefined
        if (thinking) {
          events.push({
            type: 'thinking_delta',
            data: { delta: thinking },
            timestamp,
          })
        }

        if (msg.content) {
          events.push({
            type: 'content_delta',
            data: { delta: msg.content },
            timestamp,
          })
        }

        const nextMsg = sorted[i + 1]
        if (!nextMsg || (nextMsg.role !== 'tool_call' && nextMsg.role !== 'tool_result')) {
          events.push({ type: 'message_end', data: {}, timestamp })
          messageStartOpen = false
        }
        break
      }

      case 'tool_call': {
        const toolCallId = (msg.metadata?.tool_call_id as string) || msg.id
        events.push({
          type: 'tool_use_start',
          data: {
            toolCallId,
            toolName: msg.tool_name || '',
            input: msg.tool_input || {},
          },
          timestamp,
        })
        break
      }

      case 'tool_result': {
        if (msg.tool_name?.startsWith('__')) {
          if (msg.tool_name === '__session_info') {
            try {
              const parsed = JSON.parse(msg.content)
              events.push({ type: 'session_info', data: parsed, timestamp })
            } catch { /* ignore */ }
          } else if (msg.tool_name === '__subagent_event') {
            try {
              const parsed = JSON.parse(msg.content)
              events.push({ type: 'subagent_event', data: parsed, timestamp })
            } catch { /* ignore */ }
          } else if (msg.tool_name === '__table_rendered') {
            try {
              const parsed = JSON.parse(msg.content)
              events.push({ type: 'table_rendered', data: parsed, timestamp })
            } catch { /* ignore */ }
          } else if (msg.tool_name === '__chart_rendered') {
            try {
              const parsed = JSON.parse(msg.content)
              events.push({ type: 'chart_rendered', data: parsed, timestamp })
            } catch { /* ignore */ }
          } else if (msg.tool_name === '__artifact_registered') {
            try {
              const parsed = JSON.parse(msg.content)
              events.push({ type: 'artifact_registered', data: parsed, timestamp })
            } catch { /* ignore */ }
          }

          const nextMsg = sorted[i + 1]
          if (messageStartOpen && (!nextMsg || (nextMsg.role !== 'tool_call' && nextMsg.role !== 'tool_result'))) {
            events.push({ type: 'message_end', data: {}, timestamp })
            messageStartOpen = false
          }
          break
        }

        events.push({
          type: 'tool_use_end',
          data: {
            toolCallId: msg.tool_name || '',
            output: msg.content || '',
            isError: msg.metadata?.is_error || false,
          },
          timestamp,
        })

        const nextMsg = sorted[i + 1]
        if (messageStartOpen && (!nextMsg || (nextMsg.role !== 'tool_call' && nextMsg.role !== 'tool_result'))) {
          events.push({ type: 'message_end', data: {}, timestamp })
          messageStartOpen = false
        }
        break
      }
    }
  }

  if (messageStartOpen) {
    events.push({ type: 'message_end', data: {}, timestamp: new Date().toISOString() })
  }

  return events
}

/**
 * Restore session state from legacy persisted messages (agent_messages rows).
 * Converts them to synthetic events and replays through the reducer.
 * Used as a fallback when no event data exists (pre-event-storage sessions).
 */
export function replayFromPersistedMessages(messages: PersistedAgentMessage[]): AgentEventState {
  if (messages.length === 0) return initialAgentEventState()
  const events = persistedToEvents(messages)
  return replayEvents(events)
}
