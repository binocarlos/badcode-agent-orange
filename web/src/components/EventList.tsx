// EventList — the project's recent events, newest first (§8.1).
//
// A presentational list: it receives events and reports selection. The fetch
// lives in useEventsOverview, one level up, because the jobs table and the
// replay panel read the same page of data and must agree on it.

import {
  Box,
  Chip,
  List,
  ListItem,
  ListItemButton,
  ListItemText,
  Stack,
  Typography,
} from '@mui/material'
import { formatTimestamp, type ProjectEvent } from '../events.js'

export interface EventListProps {
  events: ProjectEvent[]
  selected: string | null
  loading?: boolean
  onSelect: (id: string | null) => void
}

export default function EventList({ events, selected, loading, onSelect }: EventListProps) {
  return (
    <Box>
      <Box sx={{ px: 2, py: 1.5 }}>
        <Typography variant="subtitle2">Events</Typography>
      </Box>
      {events.length === 0 ? (
        <Typography variant="body2" color="text.secondary" sx={{ px: 2, pb: 2 }}>
          {loading ? 'Loading events…' : 'No events yet.'}
        </Typography>
      ) : (
        <List disablePadding aria-label="Events">
          {events.map((event) => (
            <ListItem key={event.id} disablePadding>
              <ListItemButton
                selected={event.id === selected}
                onClick={() => onSelect(event.id === selected ? null : event.id)}
              >
                <ListItemText
                  primary={
                    <Stack direction="row" spacing={0.75} alignItems="center">
                      <Box component="span" sx={{ fontFamily: 'monospace' }}>
                        {event.type}
                      </Box>
                      {event.envelope.source !== '' && (
                        <Chip size="small" variant="outlined" label={event.envelope.source} />
                      )}
                      {event.envelope.attention_requested && (
                        <Chip size="small" color="warning" label="attention" />
                      )}
                    </Stack>
                  }
                  secondary={
                    [
                      formatTimestamp(event.occurred_at),
                      `depth ${event.envelope.depth}`,
                      event.envelope.worker ? `from ${event.envelope.worker}` : '',
                      event.delivered ? '' : 'undelivered',
                    ]
                      .filter(Boolean)
                      .join(' · ') || undefined
                  }
                  primaryTypographyProps={{ variant: 'body2', component: 'div', noWrap: true }}
                  secondaryTypographyProps={{ variant: 'caption' }}
                />
              </ListItemButton>
            </ListItem>
          ))}
        </List>
      )}
    </Box>
  )
}
