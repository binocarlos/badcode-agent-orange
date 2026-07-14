import React, { useRef } from 'react'
import {
  Box,
  Button,
  Chip,
  Drawer,
  IconButton,
  InputAdornment,
  List,
  ListItemButton,
  ListItemText,
  MenuItem,
  Select,
  TextField,
  Tooltip,
  Typography,
} from '@mui/material'
import AppsIcon from '@mui/icons-material/Apps'
import ChevronLeftIcon from '@mui/icons-material/ChevronLeft'
import AttachFileIcon from '@mui/icons-material/AttachFile'
import SearchIcon from '@mui/icons-material/Search'
import WarningAmberIcon from '@mui/icons-material/WarningAmber'
import ErrorOutlineIcon from '@mui/icons-material/ErrorOutline'
import { AgentMessageSearchResult, AgentSessionListItem } from '../types.js'

const DRAWER_WIDTH = 280
const STORAGE_KEY = 'agent-drawer-open'

function getRelativeTime(unixSeconds: number): string {
  const now = Date.now() / 1000
  const diff = now - unixSeconds
  if (diff < 60) return 'Just now'
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`
  if (diff < 172800) return 'Yesterday'
  return `${Math.floor(diff / 86400)}d ago`
}

function getSessionTitle(session: AgentSessionListItem): string {
  if (session.title) return session.title
  return 'Untitled'
}

function getContainerStateLabel(state: string | undefined): string {
  switch (state) {
    case 'running': return 'Running'
    case 'starting': return 'Starting'
    case 'stopped': return 'Stopping'
    case 'snapshotting': return 'Snapshotting'
    case 'snapshotted': return 'Saved'
    case 'destroyed': return 'Archived'
    default: return state || 'Unknown'
  }
}

function getPersistenceIndicator(snapshotState: string | undefined): { icon: React.ReactNode; tooltip: string } | null {
  switch (snapshotState) {
    case 'persistence_failed':
      return {
        icon: <ErrorOutlineIcon sx={{ fontSize: 14, color: '#dc2626' }} />,
        tooltip: 'Session could not be saved',
      }
    case 'extraction_failed':
      return {
        icon: <WarningAmberIcon sx={{ fontSize: 14, color: '#d97706' }} />,
        tooltip: 'Some artifacts could not be saved',
      }
    case 'failed':
      return {
        icon: <ErrorOutlineIcon sx={{ fontSize: 14, color: '#dc2626' }} />,
        tooltip: 'Archive failed',
      }
    default:
      return null
  }
}

function getStatusColor(state: string | undefined): { bg: string; fg: string } {
  switch (state) {
    case 'running':
      // Inverted (filled) lozenge so a live container stands out in the list —
      // matches the toolbar's Running pill.
      return { bg: '#16a34a', fg: '#ffffff' }
    case 'starting':
    case 'snapshotting':
    case 'snapshotted':
      return { bg: '#dbeafe', fg: '#2563eb' }
    case 'error':
      return { bg: '#fef2f2', fg: '#dc2626' }
    case 'published':
      return { bg: '#f0e6ff', fg: '#7c3aed' }
    default:
      return { bg: '#f3f4f6', fg: '#6b7280' }
  }
}

interface ChatHistoryDrawerProps {
  open: boolean
  onClose: () => void
  sessions: AgentSessionListItem[]
  activeSessionId?: string
  onSelectSession: (sessionId: string) => void
  onBrowseApps?: () => void
  onLoadMore?: () => void
  hasMore?: boolean
  users?: string[]
  selectedUserEmail?: string
  onUserFilterChange?: (userEmail: string) => void
  searchQuery?: string
  onSearchChange?: (query: string) => void
  searchResults?: AgentMessageSearchResult[]
  onSearchSubmit?: (query: string) => void
  isSearching?: boolean
  currentUserEmail?: string
}

export default function ChatHistoryDrawer({
  open,
  onClose,
  sessions,
  activeSessionId,
  onSelectSession,
  onLoadMore,
  hasMore,
  users,
  selectedUserEmail = 'me',
  onUserFilterChange,
  searchQuery = '',
  onSearchChange,
  searchResults,
  onSearchSubmit,
  isSearching,
  currentUserEmail,
  onBrowseApps,
}: ChatHistoryDrawerProps) {
  const searchTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  const handleSearchKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter' && onSearchSubmit && searchQuery) {
      if (searchTimerRef.current) clearTimeout(searchTimerRef.current)
      onSearchSubmit(searchQuery)
    }
  }

  const handleSearchInput = (value: string) => {
    if (onSearchChange) onSearchChange(value)
    if (onSearchSubmit) {
      if (searchTimerRef.current) clearTimeout(searchTimerRef.current)
      if (value.length >= 2) {
        searchTimerRef.current = setTimeout(() => onSearchSubmit(value), 300)
      }
    }
  }

  const showUserEmail = selectedUserEmail === '*' || (selectedUserEmail !== 'me' && selectedUserEmail !== currentUserEmail)

  return (
    <Drawer
      variant="persistent"
      anchor="left"
      open={open}
      sx={{
        width: open ? DRAWER_WIDTH : 0,
        flexShrink: 0,
        '& .MuiDrawer-paper': {
          width: DRAWER_WIDTH,
          position: 'relative',
          borderRight: '1px solid rgba(0,0,0,0.06)',
          backgroundColor: 'background.paper',
        },
      }}
    >
      <Box sx={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', p: '8px 12px', borderBottom: '1px solid rgba(0,0,0,0.06)' }}>
        <Typography sx={{ fontSize: 13, fontWeight: 600, color: '#374151' }}>
          Chat History
        </Typography>
        <Box sx={{ display: 'flex', gap: 0.5 }}>
          {/*{onBrowseApps && (
            <Tooltip title="Browse Apps">
              <IconButton size="small" onClick={onBrowseApps}>
                <AppsIcon fontSize="small" />
              </IconButton>
            </Tooltip>
          )}*/}
          <Tooltip title="Close drawer">
            <IconButton size="small" onClick={onClose}>
              <ChevronLeftIcon fontSize="small" />
            </IconButton>
          </Tooltip>
        </Box>
      </Box>

      {/* User filter and search controls */}
      <Box sx={{ px: 1.5, py: 1, display: 'flex', flexDirection: 'column', gap: 0.75, borderBottom: '1px solid rgba(0,0,0,0.06)' }}>
        {onUserFilterChange && (
          <Select
            value={selectedUserEmail}
            onChange={(e) => onUserFilterChange(e.target.value)}
            size="small"
            sx={{ fontSize: 12, height: 30, '& .MuiSelect-select': { py: 0.5 } }}
            fullWidth
          >
            <MenuItem value="me" sx={{ fontSize: 12 }}>My sessions</MenuItem>
            <MenuItem value="*" sx={{ fontSize: 12 }}>All users</MenuItem>
            {users?.filter(u => u !== currentUserEmail).map(email => (
              <MenuItem key={email} value={email} sx={{ fontSize: 12 }}>{email}</MenuItem>
            ))}
          </Select>
        )}
        {onSearchChange && (
          <TextField
            value={searchQuery}
            onChange={(e) => handleSearchInput(e.target.value)}
            onKeyDown={handleSearchKeyDown}
            placeholder="Search messages..."
            size="small"
            fullWidth
            slotProps={{
              input: {
                startAdornment: (
                  <InputAdornment position="start">
                    <SearchIcon sx={{ fontSize: 16, color: '#9ca3af' }} />
                  </InputAdornment>
                ),
                sx: { fontSize: 12, height: 30 },
              },
            }}
          />
        )}
      </Box>

      <List dense sx={{ overflow: 'auto', flex: 1, p: 0 }}>
        {/* Search results mode */}
        {searchResults && searchResults.length > 0 ? (
          <>
            <Box sx={{ px: 1.5, py: 0.75, backgroundColor: '#f0f9ff', borderBottom: '1px solid rgba(0,0,0,0.06)' }}>
              <Typography sx={{ fontSize: 11, color: '#2563eb', fontWeight: 500 }}>
                {searchResults.length} search result{searchResults.length !== 1 ? 's' : ''}
              </Typography>
            </Box>
            {searchResults.map((r, i) => (
              <ListItemButton
                key={`${r.session_id}-${i}`}
                onClick={() => onSelectSession(r.session_id)}
                sx={{ py: 1, px: 1.5, borderBottom: '1px solid rgba(0,0,0,0.06)' }}
              >
                <ListItemText
                  primary={
                    <Typography sx={{ fontSize: 13, fontWeight: 500, color: '#1f2937', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                      {r.session_title || 'Untitled'}
                    </Typography>
                  }
                  secondaryTypographyProps={{ component: 'div' }}
                  secondary={
                    <Box sx={{ display: 'flex', flexDirection: 'column', gap: 0.25, mt: 0.25 }}>
                      <Typography component="span" sx={{ fontSize: 11, color: '#6b7280', overflow: 'hidden', textOverflow: 'ellipsis', display: '-webkit-box', WebkitLineClamp: 2, WebkitBoxOrient: 'vertical' }}>
                        {r.content}
                      </Typography>
                      <Box sx={{ display: 'flex', alignItems: 'center', gap: 0.5 }}>
                        {showUserEmail && r.user_email && (
                          <Typography component="span" sx={{ fontSize: 10, color: '#9ca3af' }}>{r.user_email}</Typography>
                        )}
                        <Typography component="span" sx={{ fontSize: 10, color: '#9ca3af', ml: 'auto' }}>{getRelativeTime(r.created_at)}</Typography>
                      </Box>
                    </Box>
                  }
                />
              </ListItemButton>
            ))}
          </>
        ) : searchResults && searchResults.length === 0 && searchQuery ? (
          <Box sx={{ p: 2, textAlign: 'center' }}>
            <Typography sx={{ fontSize: 13, color: '#9ca3af' }}>
              {isSearching ? 'Searching...' : 'No results found'}
            </Typography>
          </Box>
        ) : (
          /* Normal session list mode */
          <>
            {sessions.length === 0 && (
              <Box sx={{ p: 2, textAlign: 'center' }}>
                <Typography sx={{ fontSize: 13, color: '#9ca3af' }}>
                  No sessions yet
                </Typography>
              </Box>
            )}
            {sessions.map(s => {
              const isActive = s.id === activeSessionId
              const isPublished = s.status === 'published'
              const statusColor = isPublished ? getStatusColor('published') : getStatusColor(s.container_state)
              return (
                <ListItemButton
                  key={s.id}
                  data-testid="session-row"
                  selected={isActive}
                  onClick={() => onSelectSession(s.id)}
                  sx={{
                    py: 1,
                    px: 1.5,
                    borderBottom: '1px solid rgba(0,0,0,0.06)',
                    backgroundColor: isActive ? '#eff6ff' : undefined,
                    '&:hover': {
                      backgroundColor: isActive ? '#dbeafe' : '#f9fafb',
                    },
                  }}
                >
                  <ListItemText
                    primary={
                      <Typography
                        sx={{
                          fontSize: 13,
                          fontWeight: isActive ? 600 : 500,
                          color: '#1f2937',
                          overflow: 'hidden',
                          textOverflow: 'ellipsis',
                          whiteSpace: 'nowrap',
                        }}
                      >
                        {getSessionTitle(s)}
                      </Typography>
                    }
                    secondaryTypographyProps={{ component: 'div' }}
                    secondary={
                      <Box sx={{ display: 'flex', flexDirection: 'column', gap: 0.25, mt: 0.25 }}>
                        <Box sx={{ display: 'flex', alignItems: 'center', gap: 0.5 }}>
                          <Chip
                            label={isPublished ? 'Published' : getContainerStateLabel(s.container_state)}
                            size="small"
                            sx={{
                              fontSize: 10,
                              height: 18,
                              fontWeight: s.container_state === 'running' && !isPublished ? 600 : undefined,
                              backgroundColor: statusColor.bg,
                              color: statusColor.fg,
                              '& .MuiChip-label': { color: statusColor.fg },
                            }}
                          />
                          {getPersistenceIndicator(s.snapshot_state) && (
                            <Tooltip title={getPersistenceIndicator(s.snapshot_state)!.tooltip} arrow>
                              <Box sx={{ display: 'flex', alignItems: 'center' }}>
                                {getPersistenceIndicator(s.snapshot_state)!.icon}
                              </Box>
                            </Tooltip>
                          )}
                          {s.snapshot_progress && s.snapshot_progress.phase !== 'done' && s.snapshot_progress.phase !== 'failed' && (
                            <Chip size="small" variant="outlined" color="info"
                              label={s.snapshot_progress.bytesTotal > 0
                                ? `${Math.round((s.snapshot_progress.bytesDone / s.snapshot_progress.bytesTotal) * 100)}%`
                                : (s.snapshot_progress.op === 'create' ? 'Starting…'
                                : s.snapshot_progress.op === 'restore' ? 'Restoring…' : 'Snapshotting…')} />
                          )}
                          {s.artifact_count > 0 && (
                            <Box sx={{ display: 'flex', alignItems: 'center', gap: 0.25 }}>
                              <AttachFileIcon sx={{ fontSize: 12, color: '#6b7280' }} />
                              <Typography component="span" sx={{ fontSize: 10, color: '#6b7280' }}>
                                {s.artifact_count}
                              </Typography>
                            </Box>
                          )}
                          {s.workflow_id && (
                            <Typography component="span" sx={{ fontSize: 11, color: '#6b7280', fontStyle: 'italic' }}>
                              {s.workflow_id}
                            </Typography>
                          )}
                          <Typography component="span" sx={{ fontSize: 11, color: '#9ca3af', ml: 'auto' }}>
                            {getRelativeTime(s.updated_at)}
                          </Typography>
                        </Box>
                        {showUserEmail && s.user_email && s.user_email !== currentUserEmail && (
                          <Typography component="span" sx={{ fontSize: 11, color: '#6366f1', display: 'block' }}>
                            {s.user_email}
                          </Typography>
                        )}
                        {s.job && (
                          <Typography component="span" sx={{ fontSize: 11, color: '#9ca3af', display: 'block' }}>
                            {s.job}
                          </Typography>
                        )}
                      </Box>
                    }
                  />
                </ListItemButton>
              )
            })}

            {hasMore && (
              <Box sx={{ display: 'flex', justifyContent: 'center', py: 1 }}>
                <Button
                  size="small"
                  color="info"
                  onClick={onLoadMore}
                  sx={{ textTransform: 'none', fontSize: 12 }}
                >
                  Load more
                </Button>
              </Box>
            )}
          </>
        )}
      </List>
    </Drawer>
  )
}

export { DRAWER_WIDTH, STORAGE_KEY }
