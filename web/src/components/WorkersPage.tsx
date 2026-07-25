// WorkersPage — the whole worker surface of spec §6.5 on one screen: the list,
// the editor, the job history, and the chat, with the selected worker in the
// address bar.
//
// Router-free, F3's way: selection is one query parameter written through the
// History API, so a host that already has a router can pass `selected` +
// `onSelect` and this component never touches the URL. Nothing here imports a
// router, and nothing here assumes one exists.

import React, { useCallback, useEffect, useState } from 'react'
import { Alert, Box, Divider, Stack, Tab, Tabs, Typography } from '@mui/material'
import useWorkers from '../useWorkers.js'
import type { ConfigApiOptions } from '../configApi.js'
import { buildWorkerSearch, newWorkerDraft, workerFromSearch, type WorkerDraft } from '../workers.js'
import WorkerList from './WorkerList.js'
import WorkerEditor from './WorkerEditor.js'
import WorkerJobHistory from './WorkerJobHistory.js'
import WorkerChatPanel from './WorkerChatPanel.js'

/** Sentinel for "the create-a-worker form is open". Not a legal worker name
 *  (names are kebab-case), so it can never collide with a real selection. */
const NEW_WORKER = '#new'

export interface WorkersPageProps extends ConfigApiOptions {
  /** Project id — scopes permalinks and the chat's session. */
  projectId: string
  /**
   * Controlled selection. Pass it (with onSelect) when the host owns routing;
   * doing so also disables this component's own URL writing.
   */
  selected?: string | null
  onSelect?: (name: string | null) => void
  /** Write `?worker=` into the URL. Ignored when `selected` is controlled. */
  syncUrl?: boolean
  /** Known image names for the editor's image picker. */
  imageOptions?: string[]
  /** The project's base image, named in the picker's helper text. */
  projectBaseImage?: string
  /** Called when a job row is clicked — typically useSessionPermalink().openSession. */
  onOpenSession?: (sessionId: string) => void
  /** Render the "Chat" tab. Requires an <AgentChatProvider> ancestor. */
  enableChat?: boolean
}

type TabKey = 'config' | 'jobs' | 'chat'

export default function WorkersPage({
  projectId,
  selected: controlledSelected,
  onSelect,
  syncUrl = true,
  imageOptions,
  projectBaseImage,
  onOpenSession,
  enableChat = true,
  ...apiOptions
}: WorkersPageProps) {
  const { workers, loading, error, save, remove } = useWorkers(apiOptions)

  const controlled = controlledSelected !== undefined
  const hasWindow = typeof window !== 'undefined'
  const urlEnabled = !controlled && syncUrl && hasWindow

  const [internalSelected, setInternalSelected] = useState<string | null>(() =>
    urlEnabled ? workerFromSearch(window.location.search) : null,
  )
  const [tab, setTab] = useState<TabKey>('config')
  const [saving, setSaving] = useState(false)

  const selected = controlled ? controlledSelected! : internalSelected

  const select = useCallback(
    (name: string | null) => {
      if (!controlled) setInternalSelected(name)
      if (urlEnabled) {
        const search = buildWorkerSearch(window.location.search, name)
        window.history.pushState(null, '', window.location.pathname + search + window.location.hash)
      }
      onSelect?.(name)
    },
    [controlled, onSelect, urlEnabled],
  )

  // Back/forward moves the selection when we own the URL. Subscribing to a
  // browser event with a matching unsubscribe is exactly what useEffect is for
  // (unlike one-shot init, which this package does with a render-phase ref-guard).
  useEffect(() => {
    if (!urlEnabled) return
    const onPop = () => setInternalSelected(workerFromSearch(window.location.search))
    window.addEventListener('popstate', onPop)
    return () => window.removeEventListener('popstate', onPop)
  }, [urlEnabled])

  const isNew = selected === NEW_WORKER
  const current = isNew ? null : (workers.find((w) => w.name === selected) ?? null)

  const handleSave = useCallback(
    async (draft: WorkerDraft) => {
      setSaving(true)
      const stored = await save(draft)
      setSaving(false)
      if (stored) select(stored.name)
    },
    [save, select],
  )

  const handleDelete = useCallback(
    async (name: string) => {
      setSaving(true)
      const ok = await remove(name)
      setSaving(false)
      if (ok) select(null)
    },
    [remove, select],
  )

  return (
    <Stack direction="row" sx={{ height: '100%', minHeight: 0 }}>
      <Box sx={{ width: 280, flexShrink: 0, borderRight: 1, borderColor: 'divider', overflowY: 'auto' }}>
        <WorkerList
          workers={workers}
          selected={selected}
          loading={loading}
          onSelect={select}
          onCreate={() => {
            select(NEW_WORKER)
            setTab('config')
          }}
        />
      </Box>

      <Box sx={{ flex: 1, minWidth: 0, overflowY: 'auto' }}>
        {error !== null && (
          <Alert severity="error" sx={{ m: 2 }}>
            {error}
          </Alert>
        )}

        {!isNew && current === null ? (
          <Box sx={{ p: 3 }}>
            <Typography variant="body2" color="text.secondary">
              Select a worker, or create one.
            </Typography>
          </Box>
        ) : isNew ? (
          <WorkerEditor
            isNew
            worker={newWorkerDraft(projectId)}
            onSave={handleSave}
            saving={saving}
            imageOptions={imageOptions}
            projectBaseImage={projectBaseImage}
          />
        ) : (
          <>
            <Tabs value={tab} onChange={(_e, v: TabKey) => setTab(v)} sx={{ px: 2 }}>
              <Tab value="config" label="Configuration" />
              <Tab value="jobs" label="Jobs" />
              {enableChat && <Tab value="chat" label="Chat" />}
            </Tabs>
            <Divider />
            {tab === 'config' && (
              <WorkerEditor
                worker={current}
                onSave={handleSave}
                onDelete={handleDelete}
                saving={saving}
                imageOptions={imageOptions}
                projectBaseImage={projectBaseImage}
              />
            )}
            {tab === 'jobs' && current && (
              <WorkerJobHistory
                workerName={current.name}
                projectId={projectId}
                onOpenSession={onOpenSession}
                {...apiOptions}
              />
            )}
            {tab === 'chat' && enableChat && current && (
              <WorkerChatPanel worker={current} projectId={projectId} />
            )}
          </>
        )}
      </Box>
    </Stack>
  )
}
