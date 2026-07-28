// The spine — one vertical hairline with ticks, at the left edge of every
// reading surface (operator-console design §3.6).
//
// "Agent Orange is the tool where everything hangs off a rail, because
// everything is an append." The rail is 1px; the ticks are a CLOSED glyph set
// (§3.6, and the token table in the work plan):
//
//   ● filled disc  an agent did this            ember
//   ○ hollow disc  a human did this             ink
//   ◆ diamond      something is waiting for you  rose  (never an error colour)
//   ✕ cross        a failure                     fault
//   🔒 lock        a freeze refusal              steel
//
// Presentational only: no state, no fetch, no data shaping. What goes on the
// rail is decided by desk.ts and by the pages that mount these.
//
// Colour policy. The four named values live in the host's theme
// (`examples/web/src/theme.ts` adds `palette.ember|steel|rose|fault`), and this
// package must not depend on that augmentation — `web/` is a component library
// that any MUI host can mount. So each glyph asks the theme for its named
// entry and falls back to the design's own token for the current mode. A host
// with the console theme gets its exact values; a host without one still gets
// the right *meaning*, which is the part that must never be lost.

import type { ElementType, ReactNode } from 'react'
import { Box, type SxProps, type Theme } from '@mui/material'

/** The closed glyph set. Nothing else is ever drawn on the rail. */
export const SPINE_GLYPHS = ['agent', 'human', 'attention', 'failure', 'freeze'] as const
export type SpineGlyphName = (typeof SPINE_GLYPHS)[number]

/** One line, in the operator's words, for each glyph. */
export const SPINE_GLYPH_MEANINGS: Record<SpineGlyphName, string> = {
  agent: 'a worker did this',
  human: 'a person did this',
  attention: 'waiting for you',
  failure: 'a failure',
  freeze: 'a frozen worker refused a rewrite',
}

/** The design's §3.3 tokens, per mode — the fallback when a host theme has no
 *  named entries. The authority is the token table; this is a copy of it that
 *  cannot import from `examples/web`. */
const TOKENS = {
  light: { ember: '#B3541E', steel: '#2F6272', rose: '#A6376A', fault: '#8F2B2B', ink: '#12161A' },
  dark: { ember: '#E0873F', steel: '#6FA6B8', rose: '#DF7BA4', fault: '#D96C6C', ink: '#E8EAEC' },
} as const

type TokenName = keyof (typeof TOKENS)['light']

/** The named colour a glyph carries. */
const GLYPH_TOKEN: Record<SpineGlyphName, TokenName> = {
  agent: 'ember',
  human: 'ink',
  attention: 'rose',
  failure: 'fault',
  freeze: 'steel',
}

/**
 * A named console colour from the theme, falling back to the design token.
 *
 * The cast is the whole point: `palette.ember` exists only where a host has
 * declared it, so this reads the palette as the open record it is at runtime
 * rather than pretending the augmentation is always present.
 */
export function consoleColor(theme: Theme, name: string, fallback: string): string {
  const palette = theme.palette as unknown as Record<string, { main?: string } | undefined>
  const entry = palette[name]
  return typeof entry?.main === 'string' ? entry.main : fallback
}

/** The colour for one glyph under one theme. */
export function spineGlyphColor(theme: Theme, glyph: SpineGlyphName): string {
  const token = GLYPH_TOKEN[glyph]
  const fallback = TOKENS[theme.palette.mode === 'dark' ? 'dark' : 'light'][token]
  // `ink` is text, not a named palette entry — the theme already has it.
  if (token === 'ink') return theme.palette.text.primary || fallback
  return consoleColor(theme, token, fallback)
}

/** Width of the gutter the rail runs down, in px. The glyph is centred in it. */
export const SPINE_GUTTER = 28

/** Diameter of a glyph's box, in px. */
export const SPINE_GLYPH_SIZE = 12

export interface SpineGlyphProps {
  glyph: SpineGlyphName
  /** Accessible label; defaults to the glyph's meaning. Pass '' to hide it. */
  label?: string
  /** Box size in px. Default SPINE_GLYPH_SIZE. */
  size?: number
  sx?: SxProps<Theme>
}

/**
 * One tick. Drawn as SVG rather than a character so the shapes are the closed
 * set and not whatever a font decides ◆ looks like.
 */
export function SpineGlyph({ glyph, label, size = SPINE_GLYPH_SIZE, sx }: SpineGlyphProps) {
  const title = label === undefined ? SPINE_GLYPH_MEANINGS[glyph] : label
  return (
    <Box
      component="svg"
      viewBox="0 0 12 12"
      role={title === '' ? 'presentation' : 'img'}
      aria-label={title === '' ? undefined : title}
      data-glyph={glyph}
      sx={[
        {
          width: size,
          height: size,
          display: 'block',
          flex: '0 0 auto',
          color: (theme: Theme) => spineGlyphColor(theme, glyph),
        },
        ...(Array.isArray(sx) ? sx : [sx]),
      ]}
    >
      {title !== '' && <title>{title}</title>}
      {glyph === 'agent' && <circle cx="6" cy="6" r="4" fill="currentColor" />}
      {glyph === 'human' && (
        <circle cx="6" cy="6" r="3.5" fill="none" stroke="currentColor" strokeWidth="1.4" />
      )}
      {glyph === 'attention' && <path d="M6 1.5 10.5 6 6 10.5 1.5 6Z" fill="currentColor" />}
      {glyph === 'failure' && (
        <path
          d="M2.5 2.5 9.5 9.5M9.5 2.5 2.5 9.5"
          stroke="currentColor"
          strokeWidth="1.6"
          strokeLinecap="round"
        />
      )}
      {glyph === 'freeze' && (
        <>
          <path
            d="M4 5.2V3.9a2 2 0 0 1 4 0v1.3"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.3"
          />
          <rect x="2.8" y="5.2" width="6.4" height="5" rx="0.8" fill="currentColor" />
        </>
      )}
    </Box>
  )
}

export interface SpineRailProps {
  children?: ReactNode
  /** Hide the hairline (a nested rail, or a single row shown out of context). */
  hideRail?: boolean
  component?: ElementType
  sx?: SxProps<Theme>
}

/**
 * The rail itself: one continuous 1px hairline down the gutter, with the rows
 * hung off it. Continuity is the point — distance down the page is elapsed
 * time — so the line is drawn once by the container and masked by the glyphs,
 * never segment by segment.
 */
export function SpineRail({ children, hideRail = false, component = 'div', sx }: SpineRailProps) {
  return (
    <Box
      component={component}
      sx={[
        {
          position: 'relative',
          pl: `${SPINE_GUTTER}px`,
          m: 0,
          listStyle: 'none',
          '&::before': hideRail
            ? undefined
            : {
                content: '""',
                position: 'absolute',
                left: `${SPINE_GUTTER / 2}px`,
                top: 0,
                bottom: 0,
                width: '1px',
                bgcolor: 'divider',
              },
        },
        ...(Array.isArray(sx) ? sx : [sx]),
      ]}
    >
      {children}
    </Box>
  )
}

export interface SpineRowProps {
  glyph: SpineGlyphName
  /** Accessible label on the glyph; defaults to the glyph's meaning. */
  glyphLabel?: string
  children?: ReactNode
  component?: ElementType
  sx?: SxProps<Theme>
}

/**
 * One record on the rail: a glyph in the gutter and content beside it.
 *
 * The glyph sits on `background.paper` — that opaque square is what masks the
 * hairline running behind it, which is why the rail can be one uninterrupted
 * line and the ticks still read as ticks.
 */
export function SpineRow({ glyph, glyphLabel, children, component = 'div', sx }: SpineRowProps) {
  return (
    <Box
      component={component}
      sx={[{ position: 'relative', pb: 2 }, ...(Array.isArray(sx) ? sx : [sx])]}
    >
      <Box
        aria-hidden={false}
        sx={{
          position: 'absolute',
          left: `-${SPINE_GUTTER}px`,
          top: 0,
          width: `${SPINE_GUTTER}px`,
          display: 'flex',
          justifyContent: 'center',
          // Opaque, so the rail passes behind rather than through.
          bgcolor: 'background.paper',
          py: '3px',
        }}
      >
        <SpineGlyph glyph={glyph} label={glyphLabel} />
      </Box>
      {children}
    </Box>
  )
}

export interface SpineGapProps {
  /** What the gap was, already phrased — e.g. `4h 20m of nothing`. */
  label: string
}

/**
 * A quiet stretch, marked rather than compressed away (§3.6): "a quiet night
 * reads as a quiet night". Purely a caption on the rail; the caller decides
 * when a gap is long enough to be worth saying.
 */
export function SpineGap({ label }: SpineGapProps) {
  return (
    <Box
      sx={{
        position: 'relative',
        pb: 2,
        color: 'text.disabled',
        fontSize: 12,
      }}
    >
      <Box
        sx={{
          position: 'absolute',
          left: `-${SPINE_GUTTER}px`,
          top: 0,
          width: `${SPINE_GUTTER}px`,
          display: 'flex',
          justifyContent: 'center',
          bgcolor: 'background.paper',
          py: '3px',
        }}
      >
        <Box sx={{ width: SPINE_GLYPH_SIZE, height: SPINE_GLYPH_SIZE }} />
      </Box>
      {label}
    </Box>
  )
}
