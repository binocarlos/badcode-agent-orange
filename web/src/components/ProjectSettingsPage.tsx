// ProjectSettingsPage — the whole of `project_settings` on one screen
// (spec docs/product/01-session-config.md §5; work-plan B3).
//
// Base image, the project system prompt, the two JSON editors, and the five
// numeric budget/cap settings. Save is whole-object (the route has no patch
// semantics) and is blocked while any JSON editor is unparsable — the error
// appears under the offending box, not in a toast that has scrolled away.
//
// The numeric fields render their "0 means…" sentence live, from
// PROJECT_SETTING_NUMERICS, because the difference between "0 = off" and
// "0 = use the default" is the one thing about this screen a human can get
// expensively wrong.
//
// Router-free by construction: it renders where the host puts it and owns no
// URL. Mount it inside <AgentChatProvider> to inherit apiBaseUrl + auth, or
// pass them as props to use it standalone.

import {
  Alert,
  Box,
  Button,
  CircularProgress,
  Divider,
  FormHelperText,
  Stack,
  TextField,
  Typography,
} from '@mui/material'
import useProjectSettings, { type UseProjectSettingsOptions } from '../useProjectSettings.js'
import {
  describeNumericSetting,
  PROJECT_SETTING_NUMERICS,
  type NumericSettingSpec,
} from '../projectSettings.js'
import JsonObjectEditor from './JsonObjectEditor.js'

export interface ProjectSettingsPageProps extends UseProjectSettingsOptions {
  /** Heading text. Pass '' to render no heading (host supplies its own). */
  title?: string
}

export default function ProjectSettingsPage({
  title = 'Project settings',
  ...options
}: ProjectSettingsPageProps) {
  const s = useProjectSettings(options)

  if (s.loading) {
    return (
      <Box sx={{ p: 3, display: 'flex', justifyContent: 'center' }}>
        <CircularProgress size={24} aria-label="Loading project settings" />
      </Box>
    )
  }

  return (
    <Box sx={{ p: 3, maxWidth: 880 }}>
      {title !== '' && (
        <Typography variant="h6" sx={{ mb: 2 }}>
          {title}
        </Typography>
      )}

      {s.error !== null && (
        <Alert severity="error" sx={{ mb: 2 }}>
          {s.error}
        </Alert>
      )}

      <Stack spacing={3}>
        <Box>
          <TextField
            label="Base image"
            fullWidth
            value={s.draft.base_image}
            onChange={(e) => s.update({ base_image: e.target.value })}
            placeholder="(unset — the global default image)"
          />
          <FormHelperText>
            Default launch image for every session in this project. A worker&rsquo;s own image
            overrides it; leaving it empty falls back to the global default.
          </FormHelperText>
        </Box>

        <Box>
          <TextField
            label="Project system prompt"
            multiline
            minRows={8}
            maxRows={30}
            fullWidth
            value={s.draft.system_prompt}
            onChange={(e) => s.update({ system_prompt: e.target.value })}
          />
          <FormHelperText>
            Prepended to every worker&rsquo;s prompt, after the engine&rsquo;s core preamble
            and before the worker&rsquo;s own prompt.
          </FormHelperText>
        </Box>

        <JsonObjectEditor
          id="project-mcp-config"
          label="MCP servers (project-wide)"
          value={s.mcpText}
          onChange={s.setMcpText}
          error={s.mcpError}
          helperText={
            'map of name → server config, granted to every worker in the project. ' +
            'Secrets belong in ${VAR} references, never in this file.'
          }
        />

        <JsonObjectEditor
          id="project-attention-channel"
          label="Attention channel"
          value={s.attentionText}
          onChange={s.setAttentionText}
          error={s.attentionError}
          rows={4}
          helperText={
            'Where request_human_attention notifications go, e.g. ' +
            '{"kind":"webhook","url":"https://..."}. Unset: the tool still succeeds and only logs.'
          }
        />

        <Divider />

        <Box>
          <Typography variant="subtitle2" sx={{ mb: 0.5 }}>
            Budgets and caps
          </Typography>
          <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
            Both token budgets exempt interactive chat: a blown budget never locks you out of
            talking to your workers.
          </Typography>
          <Stack spacing={2.5}>
            {PROJECT_SETTING_NUMERICS.map((spec) => (
              <NumericSetting
                key={spec.key}
                spec={spec}
                value={s.draft[spec.key]}
                error={s.fieldErrors[spec.key] ?? null}
                onChange={(v) => s.update({ [spec.key]: v })}
              />
            ))}
          </Stack>
        </Box>

        <Box>
          <TextField
            label="Why?"
            fullWidth
            size="small"
            value={s.rationale}
            placeholder="raising the concurrency cap; the morning queue was backing up"
            onChange={(e) => s.setRationale(e.target.value)}
            inputProps={{ 'aria-label': 'Why?' }}
          />
          <FormHelperText>
            {s.rationale.trim() === ''
              ? 'Required. One line, stored with the change in the config log — the changelog reads it next to who made it.'
              : 'Stored with the change in the config log, and shown in the changelog next to who made it.'}
          </FormHelperText>
        </Box>

        <Stack direction="row" spacing={2} alignItems="center">
          <Button variant="contained" disabled={!s.canSave || !s.dirty} onClick={() => void s.save()}>
            {s.saving ? 'Saving…' : 'Save settings'}
          </Button>
          <Button disabled={s.saving || !s.dirty} onClick={() => void s.reload()}>
            Discard changes
          </Button>
          {!s.dirty && (
            <Typography variant="caption" color="text.secondary">
              No unsaved changes
            </Typography>
          )}
        </Stack>
      </Stack>
    </Box>
  )
}

/** One numeric setting: the input plus the sentence that applies to the value
 *  currently typed — so "0" always explains itself. */
function NumericSetting({
  spec,
  value,
  error,
  onChange,
}: {
  spec: NumericSettingSpec
  value: number
  error: string | null
  onChange: (value: number) => void
}) {
  return (
    <Box>
      <TextField
        label={spec.label}
        type="number"
        size="small"
        value={String(value)}
        error={error !== null}
        onChange={(e) => {
          // An emptied box means zero, not NaN: the human is mid-edit and the
          // draft must stay a number or every downstream check misreports.
          const raw = e.target.value.trim()
          onChange(raw === '' ? 0 : Number(raw))
        }}
        inputProps={{ min: 0, 'aria-label': spec.label }}
        InputProps={{
          endAdornment: (
            <Typography variant="caption" color="text.secondary" sx={{ ml: 1, whiteSpace: 'nowrap' }}>
              {spec.unit}
            </Typography>
          ),
        }}
        sx={{ width: 280 }}
      />
      <FormHelperText error={error !== null}>
        {error !== null ? error : describeNumericSetting(spec, value)}
      </FormHelperText>
    </Box>
  )
}
