import { createTheme, type Theme } from "@mui/material";

/**
 * The operator console theme (design 15-operator-console-design.md §3).
 *
 * Two facts drive everything here:
 *
 *  - **Authorship is a colour** (§3.2). `ember` means an agent did this,
 *    `steel` is instrument, `rose` is "it wants you", `fault` is failure.
 *    They are *named* palette entries, not semantic ones: MUI's
 *    success/warning/error stay exactly as MUI ships them, because
 *    `awaiting_human` is a pause and must never render as an error.
 *  - **Identifiers are mono, content is prose** (§3.4). `typography.fontFamily`
 *    is the Instrument Sans stack; the mono stack is a theme extension
 *    (`theme.monoFontFamily`) so components can reach it from `sx`.
 *
 * The fonts themselves are bundled by `fonts.css` in this package only — every
 * stack ends in a system fallback so `web/` never depends on them.
 */

/** The six named values of §3.3, per mode. */
export interface ConsolePalette {
  paper: string;
  ink: string;
  ember: string;
  steel: string;
  rose: string;
  fault: string;
}

export const consoleLight: ConsolePalette = {
  paper: "#FBFAF9",
  ink: "#12161A",
  ember: "#B3541E",
  steel: "#2F6272",
  rose: "#A6376A",
  fault: "#8F2B2B",
};

export const consoleDark: ConsolePalette = {
  paper: "#0E1114",
  ink: "#E8EAEC",
  ember: "#E0873F",
  steel: "#6FA6B8",
  rose: "#DF7BA4",
  fault: "#D96C6C",
};

/** A named colour, shaped like a MUI palette colour so `sx` paths resolve. */
export interface NamedColor {
  main: string;
}

declare module "@mui/material/styles" {
  interface Palette {
    ember: NamedColor;
    steel: NamedColor;
    rose: NamedColor;
    fault: NamedColor;
  }
  interface PaletteOptions {
    ember?: NamedColor;
    steel?: NamedColor;
    rose?: NamedColor;
    fault?: NamedColor;
  }
  interface Theme {
    /** Identifiers are set in this; content uses `typography.fontFamily`. */
    monoFontFamily: string;
  }
  interface ThemeOptions {
    monoFontFamily?: string;
  }
}

const sansStack = `"Instrument Sans", system-ui, -apple-system, "Segoe UI", sans-serif`;
const monoStack = `"IBM Plex Mono", ui-monospace, SFMono-Regular, Menlo, monospace`;

/** Greys derive from ink at 8/14/40/64% — there is no separate grey ramp (§3.3). */
function alpha(hex: string, pct: number): string {
  const n = parseInt(hex.slice(1), 16);
  return `rgba(${(n >> 16) & 255}, ${(n >> 8) & 255}, ${n & 255}, ${pct})`;
}

function buildTheme(mode: "light" | "dark", c: ConsolePalette): Theme {
  return createTheme({
    monoFontFamily: monoStack,
    palette: {
      mode,
      // Instrument, not Material blue. success/warning/error are left alone.
      primary: { main: c.steel },
      secondary: { main: c.ember },
      background: { default: c.paper, paper: c.paper },
      text: { primary: c.ink, secondary: alpha(c.ink, 0.64), disabled: alpha(c.ink, 0.4) },
      divider: alpha(c.ink, 0.14),
      ember: { main: c.ember },
      steel: { main: c.steel },
      rose: { main: c.rose },
      fault: { main: c.fault },
    },
    shape: { borderRadius: 2 },
    typography: {
      fontFamily: sansStack,
      fontSize: 14,
      // Display: tight tracking, never shouty.
      h4: { fontSize: "2rem", fontWeight: 600, letterSpacing: "-0.02em" },
      h5: { fontSize: "1.5rem", fontWeight: 600, letterSpacing: "-0.02em" },
      h6: { fontSize: "1.125rem", fontWeight: 600, letterSpacing: "-0.01em" },
      body1: { fontSize: "0.9375rem", lineHeight: 1.55 },
      body2: { fontSize: "0.875rem", lineHeight: 1.55 },
      button: { textTransform: "none", fontWeight: 500 },
    },
    components: {
      // Quiet by default: hairlines do the separating, not shadows.
      MuiPaper: {
        defaultProps: { elevation: 0 },
        styleOverrides: { root: { backgroundImage: "none" } },
      },
      MuiChip: {
        // Chips carry statuses and refs — identifiers, so mono and square.
        styleOverrides: {
          root: { borderRadius: 2 },
          label: { fontFamily: monoStack, fontSize: "0.75rem" },
        },
      },
    },
  });
}

export const lightTheme = buildTheme("light", consoleLight);
export const darkTheme = buildTheme("dark", consoleDark);
