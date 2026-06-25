// Tool call group component — renders a group of tool calls in the chat.
// Copied from frontend/src/components/agent/ToolCallGroup.tsx.
// Generic — no Platinum-specific dependencies.
// See ../../docs/90-provenance-map.md.

import React, { useState, useRef, useEffect } from 'react'
import { Box, Typography, Collapse, Button } from '@mui/material'
import type { ToolCallInfo } from '../types.js'
import { getToolDisplayName, getToolCategory, getToolIcon, getToolSummary, parseScriptExecution, stripMcpPrefix } from '../tool-formatters.js'
import CodeCreatedBlock from './CodeCreatedBlock.js'
import ScriptExecutionBlock from './ScriptExecutionBlock.js'

const OUTPUT_PREVIEW_LIMIT = 2000
const MAX_IMAGE_BASE64_LENGTH = 7_000_000 // ~5MB binary → ~7MB base64

/** Try to extract base64 image data from tool output JSON. */
export function tryParseImageOutput(output: string): { base64: string; mimeType: string } | null {
  if (output.length > MAX_IMAGE_BASE64_LENGTH) return null
  try {
    const parsed = JSON.parse(output)
    // Direct format: {"type":"image","file":{"base64":"..."}}
    if (parsed?.type === 'image' && parsed?.file?.base64) {
      return { base64: parsed.file.base64, mimeType: parsed.file.mimeType || 'image/png' }
    }
    // Direct Anthropic format: {"type":"image","source":{"data":"...","media_type":"image/png"}}
    if (parsed?.type === 'image' && parsed?.source?.data) {
      return { base64: parsed.source.data, mimeType: parsed.source.media_type || 'image/png' }
    }
    // Array format: [{"type":"image",...}]
    if (Array.isArray(parsed)) {
      const img = parsed.find((item: Record<string, unknown>) => item?.type === 'image' && ((item?.file as Record<string, unknown>)?.base64 || item?.data || (item?.source as Record<string, unknown>)?.data))
      if (img) {
        if ((img.source as Record<string, unknown>)?.data) return { base64: (img.source as Record<string, string>).data, mimeType: (img.source as Record<string, string>).media_type || 'image/png' }
        if (img.data) return { base64: img.data as string, mimeType: (img.mimeType as string) || 'image/png' }
        return { base64: (img.file as Record<string, string>).base64, mimeType: (img.file as Record<string, string>).mimeType || 'image/png' }
      }
    }
    // Content array format: {"content":[{"type":"image",...}]}
    if (Array.isArray(parsed?.content)) {
      const img = parsed.content.find((item: Record<string, unknown>) => item?.type === 'image' && ((item?.file as Record<string, unknown>)?.base64 || item?.data || (item?.source as Record<string, unknown>)?.data))
      if (img) {
        if ((img.source as Record<string, unknown>)?.data) return { base64: (img.source as Record<string, string>).data, mimeType: (img.source as Record<string, string>).media_type || 'image/png' }
        if (img.data) return { base64: img.data as string, mimeType: (img.mimeType as string) || 'image/png' }
        return { base64: (img.file as Record<string, string>).base64, mimeType: (img.file as Record<string, string>).mimeType || 'image/png' }
      }
    }
    return null
  } catch {
    return null
  }
}

const IMAGE_EXTENSIONS = /\.(png|jpg|jpeg|gif|webp|bmp|ico|tiff?)$/i

export function isScreenshotToolCall(tc: ToolCallInfo): boolean {
  const bareName = stripMcpPrefix(tc.name)
  return bareName === 'screenshot_url'
}

export function isImageReadToolCall(tc: ToolCallInfo): boolean {
  const bareName = stripMcpPrefix(tc.name)
  if (bareName !== 'Read') return false
  const filePath = tc.input?.file_path as string | undefined
  return !!filePath && IMAGE_EXTENSIONS.test(filePath)
}

/** Returns true for any tool known to produce image output (Read on image paths, screenshot_url, view_image). */
export function isImageToolCall(tc: ToolCallInfo): boolean {
  const bareName = stripMcpPrefix(tc.name)
  if (bareName === 'screenshot_url' || bareName === 'view_image') return true
  if (bareName === 'Read') {
    const filePath = tc.input?.file_path as string | undefined
    return !!filePath && IMAGE_EXTENSIONS.test(filePath)
  }
  return false
}

interface ToolCallGroupProps {
  toolCalls: ToolCallInfo[]
}

/** Renders just the input/output detail for a single tool (no wrapping card). */
function ToolDetail({ toolCall }: { toolCall: ToolCallInfo }) {
  const [showFullOutput, setShowFullOutput] = useState(false)
  const [showHooks, setShowHooks] = useState(false)

  const isLargeOutput = (toolCall.output?.length || 0) > OUTPUT_PREVIEW_LIMIT
  const displayOutput = toolCall.output
    ? (isLargeOutput && !showFullOutput
        ? toolCall.output.slice(0, OUTPUT_PREVIEW_LIMIT) + '...'
        : toolCall.output)
    : undefined

  const imageData = toolCall.output ? tryParseImageOutput(toolCall.output) : null

  const parsedOutput = (() => {
    if (!toolCall.output) return null
    try { return JSON.parse(toolCall.output) } catch { return null }
  })()

  const renderValue = (value: unknown): React.ReactNode => {
    if (value === null || value === undefined) return <span style={{ color: '#9ca3af' }}>null</span>
    if (typeof value === 'boolean') return <span style={{ color: value ? '#16a34a' : '#dc2626' }}>{String(value)}</span>
    if (typeof value === 'number') return <span style={{ fontWeight: 500 }}>{value.toLocaleString()}</span>
    if (typeof value === 'string') {
      if (value.length > 200) return <span style={{ color: '#6b7280' }}>{value.slice(0, 200)}...</span>
      return <span>{value}</span>
    }
    return <span style={{ color: '#6b7280' }}>{JSON.stringify(value)}</span>
  }

  const renderKeyValue = (obj: Record<string, unknown>) => (
    <Box sx={{ display: 'flex', flexDirection: 'column', gap: 0.5 }}>
      {Object.entries(obj).map(([key, value]) => (
        <Box key={key} sx={{ display: 'flex', alignItems: 'baseline', gap: 1 }}>
          <Typography sx={{ fontSize: '0.7rem', color: 'text.secondary', fontWeight: 500, minWidth: 60, flexShrink: 0 }}>
            {key}
          </Typography>
          <Typography sx={{ fontSize: '0.75rem', color: 'text.primary', wordBreak: 'break-word' }}>
            {renderValue(value)}
          </Typography>
        </Box>
      ))}
    </Box>
  )

  return (
    <Box sx={{ px: 3, pb: 1.5, pt: 1, borderTop: '1px solid', borderColor: 'divider' }}>
      {toolCall.input && Object.keys(toolCall.input).length > 0 && (
        <Box sx={{ mb: 1.5 }}>
          <Typography sx={{ fontSize: '0.65rem', color: 'text.secondary', textTransform: 'uppercase', letterSpacing: '0.05em', mb: 0.5 }}>Input</Typography>
          {renderKeyValue(toolCall.input)}
        </Box>
      )}

      {toolCall.output && (
        <Box>
          {imageData && (
            <Box sx={{ mb: 1 }}>
              <img
                src={`data:${imageData.mimeType};base64,${imageData.base64}`}
                style={{ maxWidth: '100%', maxHeight: 400, objectFit: 'contain', border: '1px solid rgba(0,0,0,0.06)' }}
              />
            </Box>
          )}
          {parsedOutput && typeof parsedOutput === 'object' && !Array.isArray(parsedOutput) ? (
            <Box sx={{ borderTop: '1px solid', borderColor: 'divider', pt: 1 }}>
              <Typography sx={{ fontSize: '0.65rem', color: 'text.secondary', textTransform: 'uppercase', letterSpacing: '0.05em', mb: 0.5 }}>Output</Typography>
              {renderKeyValue(parsedOutput as Record<string, unknown>)}
            </Box>
          ) : (
            <>
              <Box
                component="pre"
                sx={{
                  m: 0,
                  whiteSpace: 'pre-wrap',
                  wordBreak: 'break-all',
                  fontSize: '0.75rem',
                  maxHeight: showFullOutput ? 'none' : 300,
                  overflow: 'auto',
                  color: toolCall.status === 'error' ? 'text.secondary' : 'text.primary',
                }}
              >
                {displayOutput}
              </Box>
              {isLargeOutput && (
                <Button
                  size="small"
                  onClick={(e) => { e.stopPropagation(); setShowFullOutput(prev => !prev) }}
                  sx={{ fontSize: '0.7rem', textTransform: 'none', mt: 0.5, p: 0, color: 'text.secondary' }}
                >
                  {showFullOutput ? 'Show less' : 'Show more'}
                </Button>
              )}
            </>
          )}
        </Box>
      )}

      {toolCall.hookEvents && toolCall.hookEvents.length > 0 && (
        <Box sx={{ mt: 1 }}>
          <Typography
            onClick={(e) => { e.stopPropagation(); setShowHooks(prev => !prev) }}
            sx={{
              fontWeight: 600, fontSize: 11, color: '#9ca3af', mb: 0.5,
              cursor: 'pointer', userSelect: 'none',
              '&:hover': { color: '#6b7280' },
            }}
          >
            PROCESSING ({toolCall.hookEvents.length}) {showHooks ? '▾' : '▸'}
          </Typography>
          <Collapse in={showHooks} unmountOnExit>
            <Box sx={{ pl: 1, borderLeft: '2px solid #e5e7eb' }}>
              {toolCall.hookEvents.map((hook, i) => {
                const time = new Date(hook.timestamp).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
                const stdout = hook.payload?.stdout as string | undefined
                const stderr = hook.payload?.stderr as string | undefined
                return (
                  <Box key={i} sx={{ mb: 0.5 }}>
                    <Typography sx={{ fontSize: 11, color: '#9ca3af', fontFamily: 'monospace' }}>
                      [{hook.hookType}] {time}
                    </Typography>
                    {(stdout || stderr) && (
                      <Box
                        component="pre"
                        sx={{
                          m: 0, mt: 0.25, fontSize: 11, color: '#6b7280',
                          fontFamily: 'monospace', whiteSpace: 'pre-wrap',
                          maxHeight: 100, overflow: 'auto',
                        }}
                      >
                        {stdout || stderr}
                      </Box>
                    )}
                  </Box>
                )
              })}
            </Box>
          </Collapse>
        </Box>
      )}
    </Box>
  )
}

// Spinner using CSS animation (avoids loaderGif dependency)
function Spinner({ size = 14 }: { size?: number }) {
  return (
    <Box
      component="span"
      sx={{
        display: 'inline-block',
        width: size,
        height: size,
        border: '2px solid #e5e7eb',
        borderTopColor: '#3b82f6',
        borderRadius: '50%',
        animation: 'toolSpin 0.7s linear infinite',
        '@keyframes toolSpin': { '100%': { transform: 'rotate(360deg)' } },
        verticalAlign: 'middle',
      }}
    />
  )
}

export default function ToolCallGroup({ toolCalls }: ToolCallGroupProps) {
  const [expanded, setExpanded] = useState(false)
  const [expandedToolId, setExpandedToolId] = useState<string | null>(null)
  const [singleDetailOpen, setSingleDetailOpen] = useState(false)
  const userToggled = useRef(false)
  const prevHadRunning = useRef(false)

  const runningCount = toolCalls.filter(tc => tc.status === 'running').length
  const errorCount = toolCalls.filter(tc => tc.status === 'error').length
  const completeCount = toolCalls.filter(tc => tc.status === 'complete').length
  const hasError = errorCount > 0

  const currentlyRunning = runningCount > 0

  useEffect(() => {
    if (userToggled.current) return
    if (currentlyRunning) {
      setExpanded(true)
    } else if (prevHadRunning.current && !hasError) {
      setExpanded(false)
    }
    prevHadRunning.current = currentlyRunning
  }, [currentlyRunning, hasError])

  const handleHeaderClick = () => {
    userToggled.current = true
    setExpanded(prev => !prev)
  }

  const handleToolRowClick = (toolId: string) => {
    setExpandedToolId(prev => prev === toolId ? null : toolId)
  }

  // Single tool call: render flat row instead of collapsible group
  if (toolCalls.length === 1) {
    const tc = toolCalls[0]
    const bareName = stripMcpPrefix(tc.name)
    const isCodeCreate = bareName === 'text_editor'
      && tc.input.command === 'create'
      && typeof tc.input.file_text === 'string'
      && typeof tc.input.file_path === 'string'

    if (isCodeCreate) {
      return (
        <Box sx={{ mt: 1 }}>
          <CodeCreatedBlock
            filePath={tc.input.file_path as string}
            code={tc.input.file_text as string}
          />
        </Box>
      )
    }

    const scriptMatch = parseScriptExecution(tc.name, tc.input)
    if (scriptMatch) {
      return (
        <Box sx={{ mt: 1 }}>
          <ScriptExecutionBlock
            scriptFile={scriptMatch.scriptFile}
            language={scriptMatch.language}
            fullCommand={scriptMatch.fullCommand}
            output={tc.output}
            status={tc.status}
            elapsedSeconds={tc.elapsedSeconds}
          />
        </Box>
      )
    }

    const displayName = getToolDisplayName(tc.name, tc.input)
    const category = getToolCategory(tc.name, tc.input)
    const categoryIcon = getToolIcon(category)
    const summary = getToolSummary(tc.name, tc.input)

    const statusIcon: React.ReactNode = tc.status === 'running' ? <Spinner /> : tc.status === 'error' ? '✗' : '✓'
    const statusColor = tc.status === 'running' ? '#3b82f6' : tc.status === 'error' ? '#9ca3af' : '#22c55e'

    return (
      <Box
        sx={{
          border: '1px solid',
          borderColor: 'divider',
          borderRadius: 0,
          mt: 1,
          overflow: 'hidden',
          fontSize: '0.8125rem',
        }}
      >
        <Box
          onClick={() => setSingleDetailOpen(prev => !prev)}
          sx={{
            p: '8px 12px',
            cursor: 'pointer',
            display: 'flex',
            alignItems: 'center',
            gap: 1,
            userSelect: 'none',
            '&:hover': { backgroundColor: 'action.hover' },
          }}
        >
          <Typography component="span" sx={{ fontSize: 13, width: 20, textAlign: 'center', flexShrink: 0 }}>
            {categoryIcon}
          </Typography>
          <Typography component="span" sx={{ fontWeight: 500, fontSize: 13, whiteSpace: 'nowrap', flexShrink: 0 }}>
            {displayName}
          </Typography>
          <Typography component="span" sx={{ color: statusColor, fontWeight: 600, fontSize: 13, flexShrink: 0 }}>
            {statusIcon}
          </Typography>
          {tc.elapsedSeconds !== undefined && (
            <Typography component="span" sx={{ color: '#9ca3af', fontSize: 12, flexShrink: 0 }}>
              {tc.elapsedSeconds.toFixed(1)}s
            </Typography>
          )}
          {summary && (
            <Typography
              component="span"
              sx={{
                color: '#6b7280',
                fontSize: 12,
                ml: 'auto',
                whiteSpace: 'nowrap',
                overflow: 'hidden',
                textOverflow: 'ellipsis',
                minWidth: 0,
                flexShrink: 1,
              }}
            >
              {summary}
            </Typography>
          )}
        </Box>
        <Collapse in={singleDetailOpen} unmountOnExit>
          <ToolDetail toolCall={tc} />
        </Collapse>
      </Box>
    )
  }

  // Summary text for the header
  const headerText = currentlyRunning
    ? `Running tools... (${completeCount} done, ${runningCount} running)`
    : hasError
      ? `${toolCalls.length} steps (${errorCount} failed)`
      : `${toolCalls.length} steps completed`

  const headerIcon: React.ReactNode = currentlyRunning ? <Spinner /> : hasError ? null : '✓'
  const headerColor = currentlyRunning ? '#3b82f6' : hasError ? '#9ca3af' : '#22c55e'

  return (
    <Box
      sx={{
        border: '1px solid',
        borderColor: 'divider',
        borderRadius: 0,
        mt: 1,
        overflow: 'hidden',
        fontSize: 13,
      }}
    >
      <Box
        onClick={handleHeaderClick}
        sx={{
          p: '8px 12px',
          backgroundColor: '#f9fafb',
          cursor: 'pointer',
          display: 'flex',
          alignItems: 'center',
          gap: 1,
          userSelect: 'none',
        }}
      >
        <Typography component="span" sx={{ color: headerColor, fontWeight: 600, fontSize: 14 }}>
          {headerIcon}
        </Typography>
        <Typography component="span" sx={{ fontWeight: 600, fontSize: 13, color: '#374151' }}>
          {headerText}
        </Typography>
        <Typography component="span" sx={{ color: '#9ca3af', fontSize: 13, ml: 'auto' }}>
          {expanded ? '▾' : '▸'}
        </Typography>
      </Box>

      <Collapse in={expanded} unmountOnExit>
        <Box sx={{ borderTop: '1px solid', borderColor: 'divider' }}>
          {toolCalls.map(tc => {
            const bareName = stripMcpPrefix(tc.name)
            const isCodeCreate = bareName === 'text_editor'
              && tc.input.command === 'create'
              && typeof tc.input.file_text === 'string'
              && typeof tc.input.file_path === 'string'

            if (isCodeCreate) {
              return (
                <Box key={tc.id} sx={{ px: 1.5, py: 0.5 }}>
                  <CodeCreatedBlock
                    filePath={tc.input.file_path as string}
                    code={tc.input.file_text as string}
                  />
                </Box>
              )
            }

            const scriptMatch = parseScriptExecution(tc.name, tc.input)
            if (scriptMatch) {
              return (
                <Box key={tc.id} sx={{ px: 1.5, py: 0.5 }}>
                  <ScriptExecutionBlock
                    scriptFile={scriptMatch.scriptFile}
                    language={scriptMatch.language}
                    fullCommand={scriptMatch.fullCommand}
                    output={tc.output}
                    status={tc.status}
                    elapsedSeconds={tc.elapsedSeconds}
                  />
                </Box>
              )
            }

            const displayName = getToolDisplayName(tc.name, tc.input)
            const category = getToolCategory(tc.name, tc.input)
            const categoryIcon = getToolIcon(category)
            const summary = getToolSummary(tc.name, tc.input)
            const isExpanded = expandedToolId === tc.id

            const statusIcon: React.ReactNode = tc.status === 'running' ? <Spinner /> : tc.status === 'error' ? '✗' : '✓'
            const statusColor = tc.status === 'running' ? '#3b82f6' : tc.status === 'error' ? '#9ca3af' : '#22c55e'

            return (
              <Box key={tc.id}>
                <Box
                  onClick={() => handleToolRowClick(tc.id)}
                  sx={{
                    p: '6px 12px',
                    cursor: 'pointer',
                    display: 'flex',
                    alignItems: 'center',
                    gap: 1,
                    '&:hover': { backgroundColor: 'action.hover' },
                    userSelect: 'none',
                  }}
                >
                  <Typography component="span" sx={{ fontSize: 13, width: 20, textAlign: 'center', flexShrink: 0 }}>
                    {categoryIcon}
                  </Typography>
                  <Typography component="span" sx={{ fontWeight: 500, fontSize: 13, whiteSpace: 'nowrap', flexShrink: 0 }}>
                    {displayName}
                  </Typography>
                  <Typography component="span" sx={{ color: statusColor, fontWeight: 600, fontSize: 13, flexShrink: 0 }}>
                    {statusIcon}
                  </Typography>
                  {tc.elapsedSeconds !== undefined && (
                    <Typography component="span" sx={{ color: '#9ca3af', fontSize: 12, flexShrink: 0 }}>
                      {tc.elapsedSeconds.toFixed(1)}s
                    </Typography>
                  )}
                  {summary && (
                    <Typography
                      component="span"
                      sx={{
                        color: '#6b7280',
                        fontSize: 12,
                        ml: 'auto',
                        whiteSpace: 'nowrap',
                        overflow: 'hidden',
                        textOverflow: 'ellipsis',
                        minWidth: 0,
                        flexShrink: 1,
                      }}
                    >
                      {summary}
                    </Typography>
                  )}
                </Box>

                <Collapse in={isExpanded} unmountOnExit>
                  <ToolDetail toolCall={tc} />
                </Collapse>
              </Box>
            )
          })}
        </Box>
      </Collapse>
    </Box>
  )
}
