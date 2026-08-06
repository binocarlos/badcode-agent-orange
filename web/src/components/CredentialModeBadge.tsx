// CredentialModeBadge — which model actually answered (doc 22, RD18).
//
// Both credential lines ship blank in `.env.example`, so a user following the
// README exactly gets the deterministic offline mock. agentd says so on stdout;
// the browser was told nothing, and a scheduled job in mock mode writes
// plausible canned output into Desk, Events and Jobs with no marker anywhere.
// That is a credibility failure rather than a UX one: the reader may conclude
// the product works when no model was ever called.
//
// So the mock badge is loud and permanent — filled, `fault`-painted, always on
// screen — and the two real modes get a quiet outlined chip that names what is
// being billed. An unknown or missing mode renders nothing rather than
// guessing: silence is the one honest answer to "we do not know".

import { Chip, Tooltip, type Theme } from '@mui/material'
import { consoleTokenColor } from '../spine.js'

/** The three answers `GET /auth/config` can give for `credential_mode`. */
export const CREDENTIAL_MODES = ['mock', 'api-key', 'subscription'] as const
export type CredentialMode = (typeof CREDENTIAL_MODES)[number]

export function isCredentialMode(v: unknown): v is CredentialMode {
  return typeof v === 'string' && (CREDENTIAL_MODES as readonly string[]).includes(v)
}

/** Label and explanation per mode. The mock's wording says the consequence. */
const PAINT: Record<CredentialMode, { label: string; title: string; loud: boolean }> = {
  mock: {
    label: 'MOCK MODEL',
    title:
      'No model is being called. agentd booted with neither ANTHROPIC_API_KEY nor ' +
      'CLAUDE_CODE_OAUTH_TOKEN, so every reply — including scheduled worker jobs — is ' +
      'canned offline output. Nothing you read here came from a model.',
    loud: true,
  },
  'api-key': {
    label: 'API key',
    title: 'Real model, billed to ANTHROPIC_API_KEY through agentd’s model proxy.',
    loud: false,
  },
  subscription: {
    label: 'subscription',
    title:
      'Real model, billed to the Claude Code subscription (CLAUDE_CODE_OAUTH_TOKEN); ' +
      'sessions call api.anthropic.com directly.',
    loud: false,
  },
}

export interface CredentialModeBadgeProps {
  /** `credential_mode` from `GET /auth/config`. Unknown ⇒ renders nothing. */
  mode: string | null | undefined
}

export default function CredentialModeBadge({ mode }: CredentialModeBadgeProps) {
  if (!isCredentialMode(mode)) return null
  const paint = PAINT[mode]
  return (
    <Tooltip title={paint.title}>
      <Chip
        size="small"
        label={paint.label}
        data-credential-mode={mode}
        variant={paint.loud ? 'filled' : 'outlined'}
        sx={
          paint.loud
            ? {
                fontWeight: 700,
                letterSpacing: '0.04em',
                bgcolor: (theme: Theme) => consoleTokenColor(theme, 'fault'),
                color: (theme: Theme) =>
                  theme.palette.getContrastText(consoleTokenColor(theme, 'fault')),
              }
            : { color: 'text.secondary', borderColor: 'divider' }
        }
      />
    </Tooltip>
  )
}
