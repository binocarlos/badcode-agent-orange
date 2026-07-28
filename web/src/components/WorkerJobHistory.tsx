// WorkerJobHistory — the sessions one worker has run (spec §6.5, "job history").
//
// Rows link to the canonical session permalink (F3) so a job is shareable, and
// clicking one resumes it through the ordinary chat path — replaying a job and
// watching one live are the same reducer, so there is nothing extra here.
//
// Honesty about the data: the list comes from `GET /agent/sessions?worker=`,
// filtered in the database, so it is this worker's whole history — up to the
// page size. When that page is full the older jobs are missing, and this
// component says so rather than showing a short list that looks authoritative.

import {
  Alert,
  Box,
  Chip,
  Link,
  List,
  ListItem,
  ListItemButton,
  ListItemText,
  Stack,
  Typography,
} from '@mui/material'
import { useWorkerJobs, type UseWorkerJobsOptions } from '../useWorkers.js'
import { buildSessionPath } from '../permalink.js'

export interface WorkerJobHistoryProps extends UseWorkerJobsOptions {
  /** Worker whose jobs to show. */
  workerName: string
  /** Project id, used to build session permalinks. */
  projectId: string
  /** Called when a job row is clicked — typically useSessionPermalink().openSession. */
  onOpenSession?: (sessionId: string) => void
}

export default function WorkerJobHistory({
  workerName,
  projectId,
  onOpenSession,
  ...options
}: WorkerJobHistoryProps) {
  const { jobs, loading, error, truncated } = useWorkerJobs(workerName, options)

  return (
    <Box sx={{ p: 3 }}>
      <Typography variant="subtitle2" sx={{ mb: 1.5 }}>
        Job history
      </Typography>

      {error !== null && (
        <Alert severity="error" sx={{ mb: 2 }}>
          {error}
        </Alert>
      )}

      {truncated && (
        <Alert severity="info" sx={{ mb: 2 }}>
          Showing this worker's most recent jobs only — older jobs are not listed.
        </Alert>
      )}

      {jobs.length === 0 ? (
        <Typography variant="body2" color="text.secondary">
          {loading ? 'Loading job history…' : 'No jobs yet for this worker.'}
        </Typography>
      ) : (
        <List disablePadding>
          {jobs.map((job) => {
            const href = buildSessionPath(projectId, job.id)
            return (
              <ListItem key={job.id} disablePadding>
                <ListItemButton
                  component={onOpenSession ? 'div' : Link}
                  {...(onOpenSession ? { onClick: () => onOpenSession(job.id) } : { href })}
                >
                  <ListItemText
                    primary={
                      <Stack direction="row" spacing={1} alignItems="center">
                        <span>{job.title || job.id}</span>
                        {job.status && <Chip size="small" label={job.status} />}
                      </Stack>
                    }
                    secondary={formatTimestamp(job.created_at)}
                    primaryTypographyProps={{ variant: 'body2', noWrap: true }}
                    secondaryTypographyProps={{ variant: 'caption' }}
                  />
                </ListItemButton>
              </ListItem>
            )
          })}
        </List>
      )}
    </Box>
  )
}

/** Unix seconds → a local, human-readable stamp. 0/absent renders as ''. */
function formatTimestamp(seconds: number | undefined): string {
  if (!seconds) return ''
  return new Date(seconds * 1000).toLocaleString()
}
