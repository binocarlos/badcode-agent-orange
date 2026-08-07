// The embed page (T12 of design/2026-08-06-embeddable-agent-orange.md).
//
//     GET /embed/session/<name>#token=<jwt>
//
// One session's chat and nothing else: no sidebar, no login gate, no project
// picker. It is loaded inside a THIRD PARTY's iframe, which drives every
// decision here:
//
//   - The credential arrives in the URL **fragment**, because a fragment is
//     never sent to a server: it is absent from access logs, from `Referer`,
//     and from anything a proxy records. It is read once, erased from the
//     address bar, and held in a closure — never localStorage, never
//     sessionStorage, never a query parameter, never a fetch URL.
//   - A failure renders a flat "session unavailable" card. It must NOT redirect
//     to the console login: inside someone else's page that login is both
//     useless (the parent owns the user's identity) and a phishing surface
//     (a password box in an iframe the host did not write).
//
// The token is an embed token: scoped to exactly one session (see
// go/httpapi/lifecycle.go's scopeAllows), minutes-long, minted server-to-server
// by the embedding app with its project API key.

import { useEffect, useMemo, useRef, useState } from "react";
import { ThemeProvider, CssBaseline, Box, Typography, useMediaQuery } from "@mui/material";
import { AgentChat, AgentChatProvider, useAgentChat } from "@agentkit/chat-ui";
import { darkTheme, lightTheme } from "./theme";

const API = import.meta.env.VITE_API ?? ""; // "" → same origin (nginx proxy)

/**
 * Read `#token=…` and immediately erase the fragment from the address bar.
 *
 * Call this ONCE, before React mounts (see embed-main.tsx): it is destructive,
 * and StrictMode double-invokes effects — a second read would find the
 * fragment already gone and overwrite the captured token with "".
 */
export function takeTokenFromHash(): string {
  if (typeof window === "undefined") return "";
  const raw = window.location.hash.replace(/^#/, "");
  if (!raw) return "";
  const token = new URLSearchParams(raw).get("token") ?? "";
  // Erase before the first paint, so the parent frame, an extension, or a
  // screenshot never sees the credential. replaceState (not pushState): the
  // tokened URL must not survive in history either.
  window.history.replaceState(null, "", window.location.pathname + window.location.search);
  return token;
}

/**
 * Session NAME from `/embed/session/<name>`. Only the segment after `session`
 * is matched, so a UI mounted under a sub-path still parses — the same
 * tolerance permalink.ts applies to `/p/<project>/s/<id>`.
 */
export function sessionNameFromPath(pathname?: string): string {
  const path = pathname ?? (typeof window === "undefined" ? "" : window.location.pathname);
  const segments = path.split("/").filter(Boolean);
  const idx = segments.lastIndexOf("session");
  const raw = idx === -1 ? undefined : segments[idx + 1];
  if (!raw) return "";
  try {
    return decodeURIComponent(raw);
  } catch {
    return raw;
  }
}

// name → id, resolved before the chat mounts. GET /agent/sessions/by-name/{name}
// answers a session-scoped identity for its OWN name and 404s every other one
// (go/httpapi/sessions_byname.go:131-139), so the embed token alone is enough —
// the page never needs to be told the uuid.
type Resolution =
  | { state: "resolving" }
  | { state: "ready"; sessionId: string }
  | { state: "unavailable"; detail: string };

export default function EmbedSession({ token }: { token: string }) {
  // Two theme objects, not one with `mode` flipped — same rule as the console
  // shell (App.tsx:56). An embedded page has no chrome of its own to ask, so it
  // follows the viewer's OS preference.
  const prefersDark = useMediaQuery("(prefers-color-scheme: dark)");
  const theme = prefersDark ? darkTheme : lightTheme;

  const name = useMemo(() => sessionNameFromPath(), []);
  const [resolution, setResolution] = useState<Resolution>({ state: "resolving" });

  useEffect(() => {
    if (!token) {
      setResolution({ state: "unavailable", detail: "This link is missing its access token." });
      return;
    }
    if (!name) {
      setResolution({ state: "unavailable", detail: "This link does not name a session." });
      return;
    }
    let cancelled = false;
    (async () => {
      try {
        const resp = await fetch(`${API}/agent/sessions/by-name/${encodeURIComponent(name)}`, {
          headers: { Authorization: `Bearer ${token}` },
        });
        if (cancelled) return;
        if (!resp.ok) {
          setResolution({ state: "unavailable", detail: detailForStatus(resp.status) });
          return;
        }
        const body = (await resp.json()) as { id?: string };
        if (cancelled) return;
        if (!body.id) {
          setResolution({ state: "unavailable", detail: "The server did not return a session." });
          return;
        }
        setResolution({ state: "ready", sessionId: body.id });
      } catch {
        if (!cancelled) {
          setResolution({ state: "unavailable", detail: "Could not reach the agent service." });
        }
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [token, name]);

  const chatConfig = useMemo(
    () => ({
      apiBaseUrl: API,
      // Raw token — the provider prepends "Bearer " itself. Closed over, so it
      // exists only in this module's memory for the life of the frame.
      getAuthToken: () => token,
      models: [{ id: "claude-opus-4-5", label: "Opus" }],
    }),
    [token],
  );

  if (resolution.state === "unavailable") {
    return (
      <ThemeProvider theme={theme}>
        <CssBaseline />
        <Unavailable detail={resolution.detail} />
      </ThemeProvider>
    );
  }

  if (resolution.state === "resolving") {
    // Deliberately blank: a spinner that flashes for 80ms inside someone
    // else's page reads as a broken widget.
    return (
      <ThemeProvider theme={theme}>
        <CssBaseline />
      </ThemeProvider>
    );
  }

  return (
    <ThemeProvider theme={theme}>
      <CssBaseline />
      <AgentChatProvider config={chatConfig}>
        <EmbeddedChat sessionId={resolution.sessionId} />
      </AgentChatProvider>
    </ThemeProvider>
  );
}

/**
 * The chat itself. Separate component because `useAgentChat` reads the context
 * — the hook has to run *inside* <AgentChatProvider>, not beside it.
 *
 * `useSessionPermalink` is deliberately NOT used: it would rewrite the address
 * bar to /p/<project>/s/<id>, which is the console's route, breaks the embed
 * path, and would leak the project id into the parent page's history.
 */
function EmbeddedChat({ sessionId }: { sessionId: string }) {
  const { resumeSession } = useAgentChat();

  // Guarded by a ref against StrictMode's double effect invocation: resuming
  // twice would refetch the whole transcript for nothing.
  const resumedRef = useRef<string | null>(null);
  useEffect(() => {
    if (resumedRef.current === sessionId) return;
    resumedRef.current = sessionId;
    void resumeSession(sessionId);
  }, [sessionId, resumeSession]);

  // AgentChat's root is `flex: 1; min-height: 0`, so it needs a flex parent
  // with a definite height — the iframe's viewport.
  return (
    <Box sx={{ display: "flex", height: "100vh", minHeight: 0 }}>
      <AgentChat />
    </Box>
  );
}

function Unavailable({ detail }: { detail: string }) {
  return (
    <Box
      data-testid="embed-unavailable"
      sx={{
        height: "100vh",
        display: "flex",
        flexDirection: "column",
        alignItems: "center",
        justifyContent: "center",
        gap: 1,
        p: 3,
        textAlign: "center",
      }}
    >
      <Typography variant="h6">Session unavailable</Typography>
      <Typography variant="body2" color="text.secondary">
        {detail}
      </Typography>
    </Box>
  );
}

// What the reader is told. 404 and 401 are the two ordinary outcomes and get
// plain-language text; nothing here reveals more than the API already told this
// token holder, and no status sends the reader to a login screen.
function detailForStatus(status: number): string {
  if (status === 401 || status === 403) return "This link has expired. Reopen the page that created it.";
  if (status === 404) return "This session no longer exists.";
  return `The agent service answered ${status}.`;
}
