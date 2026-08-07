// AutomationPage — the two ways a worker is woken, on one screen: event
// subscriptions (§8.3) and schedules (§8.6). Work-plan item F2.
//
// One page rather than two because the question a human arrives with is "what
// makes this worker run?", and the answer is always one of these two. The tabs
// are the two mechanisms; the list column is whichever mechanism is selected.
//
// Router-free, F3's way: the tab and the selected row are query parameters
// written through the History API, so a host with its own router passes
// `selected` + `onSelect` and this component never touches the URL.

import { useCallback, useEffect, useState } from 'react'
import { Alert, Box, Button, Chip, List, ListItem, ListItemButton, ListItemText, Stack, Tab, Tabs, Typography } from '@mui/material'
import AddIcon from '@mui/icons-material/Add'
import type { ConfigApiOptions } from '../configApi.js'
import useSubscriptions from '../useSubscriptions.js'
import useSchedules from '../useSchedules.js'
import useEventsOverview from '../useEvents.js'
import {
  buildScheduleSearch,
  describeCron,
  newScheduleDraft,
  scheduleFromSearch,
  type ScheduleDraft,
} from '../schedules.js'
import {
  buildSubscriptionSearch,
  describeSubscriptionTarget,
  newSubscriptionDraft,
  subscriptionFromSearch,
  type SubscriptionDraft,
} from '../subscriptions.js'
import SubscriptionEditor from './SubscriptionEditor.js'
import ScheduleEditor from './ScheduleEditor.js'

/** Sentinel for "the create form is open". Not a legal uuid, so it can never
 *  collide with a real selection. */
const NEW_ROW = '#new'

export type AutomationTab = 'subscriptions' | 'schedules'

export interface AutomationPageProps extends ConfigApiOptions {
  /** Project id — scopes drafts created here. */
  projectId: string
  /** Which tab to show. Controlled when passed with onTabChange. */
  tab?: AutomationTab
  onTabChange?: (tab: AutomationTab) => void
  /** Controlled selection (the row id, or the NEW sentinel). */
  selected?: string | null
  onSelect?: (id: string | null) => void
  /** Write the selection into the URL. Ignored when `selected` is controlled. */
  syncUrl?: boolean
  /** Known worker names for the pickers. */
  workerOptions?: string[]
  /**
   * Load recent events for the subscription match preview. On by default; pass
   * false in a host that does not serve `/agent/events` yet — the editor simply
   * shows no preview rather than an error.
   */
  showMatchPreview?: boolean
}

export default function AutomationPage({
  projectId,
  tab: controlledTab,
  onTabChange,
  selected: controlledSelected,
  onSelect,
  syncUrl = true,
  workerOptions,
  showMatchPreview = true,
  ...apiOptions
}: AutomationPageProps) {
  const subs = useSubscriptions(apiOptions)
  const scheds = useSchedules(apiOptions)
  // Read-only, and only for the dry-run preview: it never posts. Its failure is
  // deliberately ignored — a host without an events route still gets a working
  // editor, just without the "would this have matched?" line. (The request is
  // made either way: a hook cannot be called conditionally, and skipping it
  // would mean two components instead of one honest prop.)
  const events = useEventsOverview({ ...apiOptions, limit: 50 })

  const controlledTabbed = controlledTab !== undefined
  const controlled = controlledSelected !== undefined
  const hasWindow = typeof window !== 'undefined'
  const urlEnabled = !controlled && syncUrl && hasWindow

  const [internalTab, setInternalTab] = useState<AutomationTab>(() =>
    urlEnabled && scheduleFromSearch(window.location.search) !== null ? 'schedules' : 'subscriptions',
  )
  const [internalSelected, setInternalSelected] = useState<string | null>(() => {
    if (!urlEnabled) return null
    return (
      subscriptionFromSearch(window.location.search) ?? scheduleFromSearch(window.location.search)
    )
  })
  const [saving, setSaving] = useState(false)

  const tab = controlledTabbed ? controlledTab! : internalTab
  const selected = controlled ? controlledSelected! : internalSelected

  const writeUrl = useCallback(
    (nextTab: AutomationTab, id: string | null) => {
      if (!urlEnabled) return
      // Only one of the two parameters is ever set: the other is cleared, so a
      // shared link never opens two selections at once.
      const cleared =
        nextTab === 'schedules'
          ? buildSubscriptionSearch(window.location.search, null)
          : buildScheduleSearch(window.location.search, null)
      const search =
        nextTab === 'schedules'
          ? buildScheduleSearch(cleared, id)
          : buildSubscriptionSearch(cleared, id)
      window.history.pushState(null, '', window.location.pathname + search + window.location.hash)
    },
    [urlEnabled],
  )

  const select = useCallback(
    (id: string | null) => {
      if (!controlled) setInternalSelected(id)
      writeUrl(tab, id)
      onSelect?.(id)
    },
    [controlled, onSelect, tab, writeUrl],
  )

  const changeTab = useCallback(
    (next: AutomationTab) => {
      if (!controlledTabbed) setInternalTab(next)
      if (!controlled) setInternalSelected(null)
      writeUrl(next, null)
      onTabChange?.(next)
      onSelect?.(null)
    },
    [controlled, controlledTabbed, onSelect, onTabChange, writeUrl],
  )

  // Back/forward moves the selection when we own the URL. A browser event with
  // a matching unsubscribe is exactly what useEffect is for.
  useEffect(() => {
    if (!urlEnabled) return
    const onPop = () => {
      const sub = subscriptionFromSearch(window.location.search)
      const sch = scheduleFromSearch(window.location.search)
      setInternalTab(sch !== null ? 'schedules' : 'subscriptions')
      setInternalSelected(sub ?? sch)
    }
    window.addEventListener('popstate', onPop)
    return () => window.removeEventListener('popstate', onPop)
  }, [urlEnabled])

  const isNew = selected === NEW_ROW
  const currentSub = isNew ? null : (subs.subscriptions.find((s) => s.id === selected) ?? null)
  const currentSched = isNew ? null : (scheds.schedules.find((s) => s.id === selected) ?? null)
  const loading = tab === 'subscriptions' ? subs.loading : scheds.loading
  const error = tab === 'subscriptions' ? subs.error : scheds.error

  const saveSubscription = useCallback(
    async (draft: SubscriptionDraft, rationale: string) => {
      setSaving(true)
      const stored = await subs.save(draft, rationale)
      setSaving(false)
      if (stored) select(stored.id)
    },
    [select, subs],
  )

  const saveSchedule = useCallback(
    async (draft: ScheduleDraft, rationale: string) => {
      setSaving(true)
      const stored = await scheds.save(draft, rationale)
      setSaving(false)
      if (stored) select(stored.id)
    },
    [scheds, select],
  )

  const removeRow = useCallback(
    async (id: string, rationale: string) => {
      setSaving(true)
      const ok =
        tab === 'subscriptions' ? await subs.remove(id, rationale) : await scheds.remove(id, rationale)
      setSaving(false)
      if (ok) select(null)
    },
    [scheds, select, subs, tab],
  )

  return (
    <Stack direction="row" sx={{ height: '100%', minHeight: 0 }}>
      <Box sx={{ width: 300, flexShrink: 0, borderRight: 1, borderColor: 'divider', overflowY: 'auto' }}>
        <Tabs
          value={tab}
          onChange={(_e, v: AutomationTab) => changeTab(v)}
          variant="fullWidth"
          sx={{ borderBottom: 1, borderColor: 'divider' }}
        >
          <Tab value="subscriptions" label="Subscriptions" />
          <Tab value="schedules" label="Schedules" />
        </Tabs>

        <Stack direction="row" alignItems="center" justifyContent="space-between" sx={{ px: 2, py: 1.5 }}>
          <Typography variant="subtitle2">
            {tab === 'subscriptions' ? 'On an event' : 'On a clock'}
          </Typography>
          <Button size="small" startIcon={<AddIcon />} onClick={() => select(NEW_ROW)}>
            {tab === 'subscriptions' ? 'New subscription' : 'New schedule'}
          </Button>
        </Stack>

        {tab === 'subscriptions' ? (
          subs.subscriptions.length === 0 ? (
            <EmptyList text={loading ? 'Loading subscriptions…' : 'No subscriptions yet.'} />
          ) : (
            <List disablePadding>
              {subs.subscriptions.map((s) => (
                <ListItem key={s.id} disablePadding>
                  <ListItemButton selected={s.id === selected} onClick={() => select(s.id)}>
                    <ListItemText
                      primary={
                        <Stack direction="row" spacing={1} alignItems="center">
                          <span>{s.event_type}</span>
                          {!s.enabled && <Chip size="small" label="disabled" />}
                        </Stack>
                      }
                      secondary={describeSubscriptionTarget(s)}
                      primaryTypographyProps={{ variant: 'body2', noWrap: true }}
                      secondaryTypographyProps={{ variant: 'caption', noWrap: true }}
                    />
                  </ListItemButton>
                </ListItem>
              ))}
            </List>
          )
        ) : scheds.schedules.length === 0 ? (
          <EmptyList text={loading ? 'Loading schedules…' : 'No schedules yet.'} />
        ) : (
          <List disablePadding>
            {scheds.schedules.map((s) => (
              <ListItem key={s.id} disablePadding>
                <ListItemButton selected={s.id === selected} onClick={() => select(s.id)}>
                  <ListItemText
                    primary={
                      <Stack direction="row" spacing={1} alignItems="center">
                        <span>{s.worker}</span>
                        {!s.enabled && <Chip size="small" label="disabled" />}
                      </Stack>
                    }
                    secondary={describeCron(s.cron) ?? s.cron}
                    primaryTypographyProps={{ variant: 'body2', noWrap: true }}
                    secondaryTypographyProps={{ variant: 'caption', noWrap: true }}
                  />
                </ListItemButton>
              </ListItem>
            ))}
          </List>
        )}
      </Box>

      <Box sx={{ flex: 1, minWidth: 0, overflowY: 'auto' }}>
        {!isNew && selected === null ? (
          <Box sx={{ p: 3 }}>
            <Typography variant="body2" color="text.secondary">
              {tab === 'subscriptions'
                ? 'Select a subscription, or create one. A subscription says: when an event of this type arrives, start a job for this worker.'
                : 'Select a schedule, or create one. A schedule says: at these times, tell this worker to do this.'}
            </Typography>
          </Box>
        ) : tab === 'subscriptions' ? (
          !isNew && currentSub === null ? (
            <MissingRow kind="subscription" />
          ) : (
            <SubscriptionEditor
              key={selected ?? 'new'}
              isNew={isNew}
              subscription={isNew ? newSubscriptionDraft(projectId) : currentSub}
              onSave={saveSubscription}
              onDelete={isNew ? undefined : removeRow}
              error={error}
              saving={saving}
              workerOptions={workerOptions}
              recentEvents={showMatchPreview ? events.events : []}
            />
          )
        ) : !isNew && currentSched === null ? (
          <MissingRow kind="schedule" />
        ) : (
          <ScheduleEditor
            key={selected ?? 'new'}
            isNew={isNew}
            schedule={isNew ? newScheduleDraft(projectId) : currentSched}
            onSave={saveSchedule}
            onDelete={isNew ? undefined : removeRow}
            error={error}
            saving={saving}
            workerOptions={workerOptions}
          />
        )}
      </Box>
    </Stack>
  )
}

function EmptyList({ text }: { text: string }) {
  return (
    <Box sx={{ px: 2, pb: 2 }}>
      <Typography variant="body2" color="text.secondary">
        {text}
      </Typography>
    </Box>
  )
}

function MissingRow({ kind }: { kind: string }) {
  return (
    <Alert severity="warning" sx={{ m: 2 }}>
      That {kind} is not in the list — it may have been deleted in another tab.
    </Alert>
  )
}
