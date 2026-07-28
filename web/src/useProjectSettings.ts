// useProjectSettings — load/edit/save GET+PUT /agent/project-settings.
//
// The JSON fields are held as *text* alongside the parsed settings object, and
// the text is the source of truth while editing: re-serialising on every
// keystroke would fight the human's cursor and reformat half-typed JSON. The
// text is parsed on change (so the error appears as you type) and again at save
// (so nothing unparsed can ever be PUT).
//
// PUT is whole-object — there are no patch semantics on this route — so `save`
// sends the entire settings object every time, which is also why an unparsable
// MCP editor must block the save rather than send the last-good value: sending
// stale JSON silently under a visible error is the one behaviour that would
// lose a human's work.

import { useCallback, useMemo, useRef, useState } from 'react'
import { useConfigApi, type ConfigApiOptions } from './configApi.js'
import {
  coerceProjectSettings,
  defaultProjectSettings,
  formatJsonObject,
  parseJsonObject,
  projectSettingsBody,
  PROJECT_SETTINGS_ENDPOINT,
  validateProjectSettings,
  type FieldErrors,
  type ProjectSettings,
} from './projectSettings.js'

export interface UseProjectSettingsOptions extends ConfigApiOptions {
  /** Override the endpoint path (default `/agent/project-settings`). */
  endpoint?: string
  /** Called after a successful save, with the row the server echoed back. */
  onSaved?: (settings: ProjectSettings) => void
}

export interface ProjectSettingsApi {
  /** The settings currently in the form. */
  draft: ProjectSettings
  /** Merge a partial change into the draft. */
  update: (patch: Partial<ProjectSettings>) => void
  /** Raw text of the mcp_config editor, and its parse error (null when valid). */
  mcpText: string
  setMcpText: (text: string) => void
  mcpError: string | null
  /** Raw text of the attention_channel editor, and its parse error. */
  attentionText: string
  setAttentionText: (text: string) => void
  attentionError: string | null
  /** Per-field validation problems for the numeric settings. */
  fieldErrors: FieldErrors
  /** The operator's one-line reason for this change (design B3 / K2). Required
   *  non-empty before `canSave` goes true — a settings edit carries a reason. */
  rationale: string
  setRationale: (text: string) => void
  loading: boolean
  saving: boolean
  /** Load or save failure, as the server phrased it. */
  error: string | null
  /** True once anything has been edited since the last load/save. */
  dirty: boolean
  /** False while a JSON editor is unparsable, a numeric field is invalid, or
   *  the rationale is empty. */
  canSave: boolean
  reload: () => Promise<void>
  save: () => Promise<void>
}

export default function useProjectSettings(
  options: UseProjectSettingsOptions = {},
): ProjectSettingsApi {
  const { endpoint = PROJECT_SETTINGS_ENDPOINT, onSaved } = options
  const { request } = useConfigApi(options)

  const [draft, setDraft] = useState<ProjectSettings>(() => defaultProjectSettings())
  const [mcpText, setMcpTextState] = useState('{}')
  const [attentionText, setAttentionTextState] = useState('{}')
  const [mcpError, setMcpError] = useState<string | null>(null)
  const [attentionError, setAttentionError] = useState<string | null>(null)
  const [rationale, setRationale] = useState('')
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [dirty, setDirty] = useState(false)

  const adopt = useCallback((raw: unknown) => {
    const settings = coerceProjectSettings(raw)
    setDraft(settings)
    setMcpTextState(formatJsonObject(settings.mcp_config))
    setAttentionTextState(formatJsonObject(settings.attention_channel))
    setMcpError(null)
    setAttentionError(null)
    // A saved reason belongs to the change that carried it: the next edit
    // writes its own, rather than inheriting the last one silently.
    setRationale('')
    setDirty(false)
  }, [])

  const reload = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      adopt(await request<unknown>(endpoint))
    } catch (err) {
      setError(err instanceof Error ? err.message : 'failed to load project settings')
    } finally {
      setLoading(false)
    }
  }, [adopt, endpoint, request])

  // Render-phase ref-guard rather than useEffect (the convention this package
  // already follows in AgentSessionList). It also protects against the common
  // host mistake of inlining `getAuthToken` in the config object: that changes
  // `request`'s identity every render, which a `[request]` effect would turn
  // into an unbounded GET loop.
  const didLoad = useRef(false)
  if (!didLoad.current) {
    didLoad.current = true
    void reload()
  }

  const update = useCallback((patch: Partial<ProjectSettings>) => {
    setDraft((prev) => ({ ...prev, ...patch }))
    setDirty(true)
  }, [])

  const setMcpText = useCallback((text: string) => {
    setMcpTextState(text)
    setDirty(true)
    const parsed = parseJsonObject(text)
    setMcpError(parsed.ok ? null : parsed.error)
  }, [])

  const setAttentionText = useCallback((text: string) => {
    setAttentionTextState(text)
    setDirty(true)
    const parsed = parseJsonObject(text)
    setAttentionError(parsed.ok ? null : parsed.error)
  }, [])

  const fieldErrors = useMemo(() => validateProjectSettings(draft), [draft])

  const canSave =
    !loading &&
    !saving &&
    mcpError === null &&
    attentionError === null &&
    rationale.trim() !== '' &&
    Object.keys(fieldErrors).length === 0

  const save = useCallback(async () => {
    // Re-parse rather than trusting the memoised error: this is the last gate
    // before the network, and it must read the text that is actually on screen.
    const mcp = parseJsonObject(mcpText)
    const attention = parseJsonObject(attentionText)
    if (!mcp.ok) {
      setMcpError(mcp.error)
      return
    }
    if (!attention.ok) {
      setAttentionError(attention.error)
      return
    }
    if (Object.keys(validateProjectSettings(draft)).length > 0) return
    // The reason is required (K2), and this is the last gate before the network.
    if (rationale.trim() === '') return

    setSaving(true)
    setError(null)
    try {
      const body = projectSettingsBody(
        {
          ...draft,
          mcp_config: mcp.value,
          attention_channel: attention.value,
        },
        rationale,
      )
      const saved = await request<unknown>(endpoint, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      })
      adopt(saved)
      onSaved?.(coerceProjectSettings(saved))
    } catch (err) {
      setError(err instanceof Error ? err.message : 'failed to save project settings')
    } finally {
      setSaving(false)
    }
  }, [adopt, attentionText, draft, endpoint, mcpText, onSaved, rationale, request])

  return {
    draft,
    update,
    mcpText,
    setMcpText,
    mcpError,
    attentionText,
    setAttentionText,
    attentionError,
    fieldErrors,
    rationale,
    setRationale,
    loading,
    saving,
    error,
    dirty,
    canSave,
    reload,
    save,
  }
}
