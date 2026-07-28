// BriefingPreview — what core would paste into this worker's prompt right now
// (design §8's last bullet; engine: go/compose.go BuildBriefingSections).
//
// A briefing that fails to load is only logged today, so an operator who wires
// a selector has no way to find out that it matches nothing until a job reads
// oddly. This makes it visible WITHOUT changing the engine: the same selectors,
// in the same order, each resolved to its newest match through the B2 read
// route, capped at the same `briefing_max_bytes` with the truncation marker
// shown where it would fall.
//
// It is a preview, not the thing: core reads the whole memory (`NewestMemory`)
// while the browse route returns a 500-character snippet, so a long section is
// short here. That is said on the row rather than hidden.

import { Box, Paper, Stack, Typography } from '@mui/material'
import type { ConfigApiOptions } from '../configApi.js'
import useMemories from '../useMemories.js'
import useProjectSettings from '../useProjectSettings.js'
import {
  briefingSlots,
  capBriefingContent,
  formatMemoryTimestamp,
  MEMORY_SNIPPET_CHARS,
  SNIPPET_CAVEAT,
  type BriefingSlot,
} from '../memories.js'
import { DEFAULT_BRIEFING_MAX_BYTES } from '../projectSettings.js'
import type { Worker } from '../workers.js'

export interface BriefingPreviewProps extends ConfigApiOptions {
  /** The worker whose briefing is previewed. */
  worker: Pick<Worker, 'name' | 'briefing'>
}

const MONO = { fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace' }

export default function BriefingPreview({ worker, ...apiOptions }: BriefingPreviewProps) {
  const slots = briefingSlots(worker)
  // 0 is "unset ⇒ the server's default" for this column, the B1 convention.
  const { draft } = useProjectSettings(apiOptions)
  const maxBytes =
    draft.briefing_max_bytes > 0 ? draft.briefing_max_bytes : DEFAULT_BRIEFING_MAX_BYTES

  if (slots.length === 0) return null

  return (
    <Box sx={{ mt: 2 }} data-testid="briefing-preview">
      <Typography variant="subtitle2">Briefing preview</Typography>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
        The sections core would inject into this worker&apos;s next job, newest match per selector,
        each capped at {maxBytes} bytes.
      </Typography>
      <Stack spacing={1}>
        {slots.map((slot) => (
          <BriefingSlotPreview
            key={slot.selector}
            slot={slot}
            maxBytes={maxBytes}
            {...apiOptions}
          />
        ))}
      </Stack>
    </Box>
  )
}

/** One section. Its own component because each selector is its own query, and
 *  a hook cannot run in a loop. */
function BriefingSlotPreview({
  slot,
  maxBytes,
  ...apiOptions
}: { slot: BriefingSlot; maxBytes: number } & ConfigApiOptions) {
  // limit 1: core injects the newest match and nothing else, ever.
  const { memories, loading, selectorError, error, available } = useMemories({
    ...apiOptions,
    selector: slot.selector,
    limit: 1,
  })
  const row = memories[0] ?? null
  const capped = row ? capBriefingContent(row.snippet, maxBytes) : null

  return (
    <Paper variant="outlined" sx={{ p: 1.5 }}>
      <Typography variant="caption" sx={{ ...MONO, display: 'block' }}>
        {slot.selector}
        {slot.builtin ? ' (built in)' : ''}
      </Typography>
      {loading && (
        <Typography variant="body2" color="text.secondary">
          Loading…
        </Typography>
      )}
      {!loading && !available && (
        <Typography variant="body2" color="text.secondary">
          Memory is not available on this host, so no briefing is injected at all.
        </Typography>
      )}
      {!loading && available && selectorError !== null && (
        <Typography variant="body2" color="error">
          {selectorError} — core logs this and skips the section; the job still runs.
        </Typography>
      )}
      {!loading && available && error !== null && selectorError === null && (
        <Typography variant="body2" color="error">
          {error}
        </Typography>
      )}
      {!loading && available && selectorError === null && error === null && row === null && (
        <Typography variant="body2" color="text.secondary">
          Nothing matches — this section is not injected at all, and the job runs without it.
        </Typography>
      )}
      {capped !== null && row !== null && (
        <>
          <Typography variant="body2" sx={{ whiteSpace: 'pre-wrap', mt: 0.5 }}>
            {`--- ${slot.heading} ---\n${capped.text}`}
          </Typography>
          <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mt: 0.5 }}>
            {capped.bytes} bytes
            {capped.truncated ? ' — cut at the cap, marker above' : ''}
            {row.created_at > 0 ? ` · ${formatMemoryTimestamp(row.created_at)}` : ''}
          </Typography>
          {row.snippet.length >= MEMORY_SNIPPET_CHARS && (
            <Typography variant="caption" color="text.secondary" sx={{ display: 'block' }}>
              {SNIPPET_CAVEAT}
            </Typography>
          )}
        </>
      )}
    </Paper>
  )
}
