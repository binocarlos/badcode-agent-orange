// WorkerList — the project's workers, one row each (spec §6.5).
//
// Presentational: it takes the rows and reports clicks. The parent owns loading
// and selection, which is what lets WorkersPage keep selection in the URL while
// a host with its own router drives the same component from route params.

import React from 'react'
import {
  Box,
  Button,
  Chip,
  List,
  ListItem,
  ListItemButton,
  ListItemText,
  Stack,
  Tooltip,
  Typography,
} from '@mui/material'
import AddIcon from '@mui/icons-material/Add'
import LockIcon from '@mui/icons-material/Lock'
import { FROZEN_SENTENCE, type Worker } from '../workers.js'

export interface WorkerListProps {
  workers: Worker[]
  /** Name of the selected worker, or null. */
  selected: string | null
  onSelect: (name: string) => void
  /** Omitted ⇒ no "New worker" button (e.g. a read-only view). */
  onCreate?: () => void
  loading?: boolean
}

export default function WorkerList({
  workers,
  selected,
  onSelect,
  onCreate,
  loading = false,
}: WorkerListProps) {
  return (
    <Box>
      <Stack
        direction="row"
        alignItems="center"
        justifyContent="space-between"
        sx={{ px: 2, py: 1.5 }}
      >
        <Typography variant="subtitle2">Workers</Typography>
        {onCreate && (
          <Button size="small" startIcon={<AddIcon />} onClick={onCreate}>
            New worker
          </Button>
        )}
      </Stack>

      {workers.length === 0 ? (
        <Box sx={{ px: 2, pb: 2 }}>
          <Typography variant="body2" color="text.secondary">
            {loading ? 'Loading workers…' : 'No workers yet.'}
          </Typography>
        </Box>
      ) : (
        <List disablePadding>
          {workers.map((worker) => (
            <ListItem key={worker.name} disablePadding>
              <ListItemButton
                selected={worker.name === selected}
                onClick={() => onSelect(worker.name)}
              >
                <ListItemText
                  primary={
                    <Stack direction="row" spacing={1} alignItems="center">
                      <span>{worker.name}</span>
                      {!worker.enabled && <Chip size="small" label="disabled" />}
                      {worker.frozen && (
                        <Tooltip title={FROZEN_SENTENCE}>
                          <Chip
                            size="small"
                            color="info"
                            variant="outlined"
                            icon={<LockIcon />}
                            label="frozen"
                          />
                        </Tooltip>
                      )}
                    </Stack>
                  }
                  secondary={worker.description || undefined}
                  primaryTypographyProps={{ variant: 'body2', noWrap: true }}
                  secondaryTypographyProps={{ variant: 'caption', noWrap: true }}
                />
              </ListItemButton>
            </ListItem>
          ))}
        </List>
      )}
    </Box>
  )
}
