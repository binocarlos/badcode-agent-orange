// MemoryBrowserPage — the memory surface of design §8, over the B2 read route.
//
// A SELECTOR BAR, not a search box: the query language is Kubernetes label
// selectors and this screen's job is to teach them. Clauses become chips, an
// invalid clause is named the way the engine's parser names it, and the "there
// is no OR" rule is written down rather than discovered.
//
// Everything here is read-only, because memory is append-only (§7.1). The
// `name=` convention is folded — current value first, superseded values beneath
// — for the same reason: appending IS updating, and a flat list would show five
// values where the project has one.
//
// Router-free like its siblings: no URL is written, because the selector is not
// a location. A host that wants it in the address bar owns `selector`/`query`
// through the hook.

import { useMemo, useState } from 'react'
import {
  Alert,
  Box,
  Button,
  Chip,
  Divider,
  Link,
  Paper,
  Stack,
  TextField,
  Typography,
} from '@mui/material'
import useMemories, { type UseMemoriesOptions } from '../useMemories.js'
import {
  buildMemorySelector,
  foldNamedMemories,
  formatMemoryTimestamp,
  NO_OR_NOTE,
  parseMemorySelector,
  RRF_NOTE,
  SEMANTIC_OFF_NOTE,
  semanticLegLooksOff,
  type MemoryRow,
} from '../memories.js'

export interface MemoryBrowserPageProps extends UseMemoriesOptions {
  /** Open a session thread — typically useSessionPermalink().openSession. */
  onOpenSession?: (sessionId: string) => void
  /** Heading. Pass '' for none. */
  title?: string
}

/** Identifiers are mono, content is prose (§3.4). */
const MONO = { fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace' }

export default function MemoryBrowserPage({
  onOpenSession,
  title = 'Memory',
  ...options
}: MemoryBrowserPageProps) {
  const { memories, selector, query, search, loading, selectorError, error, available } =
    useMemories(options)

  // The bar is a draft until Search: fetching per keystroke would run a
  // half-typed selector against the route and paint an error for every prefix.
  const [selectorDraft, setSelectorDraft] = useState(selector)
  const [queryDraft, setQueryDraft] = useState(query)

  const parsed = useMemo(() => parseMemorySelector(selectorDraft), [selectorDraft])
  const folded = useMemo(() => foldNamedMemories(memories), [memories])
  const semanticOff = useMemo(() => semanticLegLooksOff(memories, query), [memories, query])

  const runSearch = () => void search(selectorDraft.trim(), queryDraft.trim())

  const dropClause = (index: number) => {
    const next = buildMemorySelector(parsed.requirements.filter((_r, i) => i !== index))
    setSelectorDraft(next)
    void search(next, queryDraft.trim())
  }

  return (
    <Box sx={{ p: 3, maxWidth: 900 }}>
      {title !== '' && (
        <Typography variant="h6" sx={{ mb: 2 }}>
          {title}
        </Typography>
      )}

      <Stack direction="row" spacing={1} sx={{ mb: 1 }} alignItems="flex-start">
        <TextField
          size="small"
          fullWidth
          label="Selector"
          placeholder="kind=rolling-summary, worker=email-answerer"
          value={selectorDraft}
          onChange={(e) => setSelectorDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') runSearch()
          }}
          inputProps={{ style: MONO, 'data-testid': 'memory-selector' }}
          error={parsed.error !== null}
          helperText={parsed.error ?? NO_OR_NOTE}
        />
        <TextField
          size="small"
          label="Text"
          placeholder="what was said"
          value={queryDraft}
          onChange={(e) => setQueryDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') runSearch()
          }}
          inputProps={{ 'data-testid': 'memory-query' }}
          sx={{ width: 240 }}
        />
        <Button variant="contained" size="small" sx={{ mt: 0.5 }} onClick={runSearch}>
          Search
        </Button>
      </Stack>

      {parsed.requirements.length > 0 && (
        <Stack
          direction="row"
          spacing={0.5}
          sx={{ mb: 1, flexWrap: 'wrap', gap: 0.5 }}
          data-testid="selector-chips"
        >
          {parsed.requirements.map((req, i) => (
            <Chip
              key={`${req.key}-${req.op}-${i}`}
              size="small"
              label={buildMemorySelector([req])}
              onDelete={() => dropClause(i)}
              sx={MONO}
            />
          ))}
        </Stack>
      )}

      {selectorError !== null && (
        <Alert severity="warning" sx={{ mb: 2 }} data-testid="memory-selector-error">
          {selectorError}
        </Alert>
      )}

      {!available && (
        <Alert severity="info" sx={{ mb: 2 }}>
          Memory is not available on this host — the browse route is not mounted, or the project
          runs without Postgres. Workers can still write memories only where the store exists.
        </Alert>
      )}
      {available && error !== null && (
        <Alert severity="error" sx={{ mb: 2 }}>
          {error}
        </Alert>
      )}

      {query !== '' && (
        <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
          {RRF_NOTE}
        </Typography>
      )}
      {query !== '' && semanticOff && (
        <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
          {SEMANTIC_OFF_NOTE}
        </Typography>
      )}

      {loading && (
        <Typography variant="body2" color="text.secondary">
          Loading…
        </Typography>
      )}

      {!loading && available && memories.length === 0 && (
        <Typography variant="body2" color="text.secondary">
          {selector === '' && query === ''
            ? 'Nothing has been remembered in this project yet. Memories are written by workers through their tools — there is nothing to add here.'
            : 'No memory matches. There is no OR in a selector: try one clause at a time.'}
        </Typography>
      )}

      {folded.named.map((group) => (
        <Paper key={group.name} variant="outlined" sx={{ p: 2, mb: 1 }}>
          <Typography variant="subtitle2" sx={MONO}>
            name={group.name}
          </Typography>
          <MemoryBody row={group.current} onOpenSession={onOpenSession} />
          {group.superseded.length > 0 && (
            <Box sx={{ mt: 1 }}>
              <Divider sx={{ mb: 1 }} />
              <Typography variant="caption" color="text.secondary">
                {group.superseded.length} superseded{' '}
                {group.superseded.length === 1 ? 'value' : 'values'} — appending is how this value
                was updated; nothing was deleted.
              </Typography>
              {group.superseded.map((row) => (
                <Box key={row.id} sx={{ opacity: 0.6, mt: 1 }}>
                  <MemoryBody row={row} onOpenSession={onOpenSession} />
                </Box>
              ))}
            </Box>
          )}
        </Paper>
      ))}

      {folded.rest.map((row) => (
        <Paper key={row.id} variant="outlined" sx={{ p: 2, mb: 1 }} data-testid="memory-row">
          <Stack direction="row" spacing={0.5} sx={{ flexWrap: 'wrap', gap: 0.5, mb: 1 }}>
            {Object.entries(row.labels)
              .sort(([a], [b]) => a.localeCompare(b))
              .map(([k, v]) => (
                <Chip key={k} size="small" variant="outlined" label={`${k}=${v}`} sx={MONO} />
              ))}
          </Stack>
          <MemoryBody row={row} onOpenSession={onOpenSession} />
        </Paper>
      ))}
    </Box>
  )
}

/** One memory's content and its provenance: which worker wrote it, when, and
 *  one click to the thread it was written in (design §8). */
function MemoryBody({
  row,
  onOpenSession,
}: {
  row: MemoryRow
  onOpenSession?: (sessionId: string) => void
}) {
  return (
    <>
      <Typography variant="body2" sx={{ whiteSpace: 'pre-wrap' }}>
        {row.snippet}
      </Typography>
      <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mt: 0.5 }}>
        {row.created_by_worker !== '' ? (
          <Box component="span" sx={MONO}>
            {row.created_by_worker}
          </Box>
        ) : (
          'unattributed'
        )}
        {row.created_at > 0 ? ` · ${formatMemoryTimestamp(row.created_at)}` : ''}
        {row.created_by_session !== '' && onOpenSession ? (
          <>
            {' · '}
            <Link
              component="button"
              variant="caption"
              onClick={() => onOpenSession(row.created_by_session)}
            >
              open the session
            </Link>
          </>
        ) : null}
      </Typography>
    </>
  )
}
