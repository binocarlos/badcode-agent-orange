import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ThemeProvider, CssBaseline, Box, Button, useMediaQuery } from "@mui/material";
import {
  AgentChatProvider,
  AgentChat,
  AgentSessionList,
  AutomationPage,
  CredentialModeBadge,
  DeskPage,
  EventsPage,
  MemoryBrowserPage,
  OrgChartPage,
  ProjectSettingsPage,
  WorkersPage,
  projectIdFromLocation,
  useAsksCount,
  useSessionPermalink,
  useWorkers,
} from "@agentkit/chat-ui";
import { AuthConfig, AuthState, clearAuthState, fetchAuthConfig, loadAuthState, mintProjectToken, saveAuthState } from "./auth";
import LoginScreen from "./LoginScreen";
import ProjectPicker from "./ProjectPicker";
import Sidebar from "./Sidebar";
import { darkTheme, lightTheme } from "./theme";

const API = import.meta.env.VITE_API ?? ""; // "" → same origin (nginx proxy)

// What a project view can show. Deliberately a state machine and not a router:
// the library must not impose react-router on hosts, and the permalink hook
// already owns the one URL that matters (the session).
// Desk is first because it is the landing view (design decision K1): the
// question "does anything want me?" is the one an operator arrives with.
// Chart sits between Desk and Workers: it is the same fleet, seen as a shape
// rather than as a list.
type View = "desk" | "chart" | "chat" | "workers" | "memory" | "events" | "automation" | "settings";

// How often the two live surfaces re-fetch. The library defaults `refreshMs` to
// 0 — no timer — because a component library that starts polling the moment it
// mounts is deciding something that belongs to the host. Turning it on is
// therefore a shell decision, and this is the shell.
//
// The staged-arrival machinery behind it (the "N new" pill, and the "Pause live
// updates" toggle WCAG 2.2.2 requires wherever content moves on its own) only
// renders while something is actually polling, so before this constant existed
// both were unreachable.
const LIVE_REFRESH_MS = 15_000;

// App state machine: loading → dev (legacy /dev/token, straight to chat)
//                            → login → project picker → chat (per-project JWT)
export default function App() {
  const [authConfig, setAuthConfig] = useState<AuthConfig | null>(null);
  const [auth, setAuth] = useState<AuthState | null>(() => loadAuthState());
  const [devToken, setDevToken] = useState<string | null>(null);

  // Two theme objects, not one with `mode` flipped: the palette differs by
  // value, not by inversion (design §3.3).
  const prefersDark = useMediaQuery("(prefers-color-scheme: dark)");
  const theme = prefersDark ? darkTheme : lightTheme;

  useEffect(() => {
    fetchAuthConfig(API)
      .then(setAuthConfig)
      .catch(() => setAuthConfig({ modes: ["dev"], google_client_id: "" }));
  }, []);

  const devMode = authConfig?.modes.includes("dev") ?? false;
  useEffect(() => {
    if (!devMode) return;
    fetch(`${API}/dev/token`)
      .then((r) => r.json())
      .then((j) => setDevToken(j.token))
      .catch(() => setDevToken("")); // dev-open fallback
  }, [devMode]);

  const handleLogin = useCallback((state: AuthState) => setAuth(state), []);

  const selectProject = useCallback((projectID: string) => {
    setAuth((prev) => {
      if (!prev) return prev;
      const next = { ...prev, selectedProject: projectID };
      saveAuthState(next);
      return next;
    });
  }, []);

  const signOut = useCallback(() => {
    clearAuthState();
    setAuth(null);
  }, []);

  // Wildcard users can mint a token for a brand-new project id — this is how
  // a project is "created" (it has no row anywhere; the first session in it
  // makes it real).
  const createProject = useCallback(async (projectID: string) => {
    const loginToken = auth?.loginToken;
    if (!loginToken) throw new Error("no wildcard login token");
    const minted = await mintProjectToken(API, loginToken, projectID);
    setAuth((prev) => {
      if (!prev) return prev;
      const projects = prev.projects.some((p) => p.id === minted.id)
        ? prev.projects.map((p) => (p.id === minted.id ? minted : p))
        : [...prev.projects, minted];
      const next = { ...prev, projects, selectedProject: minted.id };
      saveAuthState(next);
      return next;
    });
  }, [auth?.loginToken]);

  // A pasted permalink names its project (/p/<project>/s/<session>), so honour
  // it once we hold a token for that project — otherwise the link would dump
  // the reader in the project picker, or worse, in whichever project they last
  // used, and the session would never resume.
  //
  // Once only, guarded by a ref: after this, switching project is the human's
  // decision and the URL must not drag them back.
  const permalinkProjectApplied = useRef(false);
  useEffect(() => {
    if (permalinkProjectApplied.current || !auth) return;
    const wanted = projectIdFromLocation();
    if (!wanted) return;
    permalinkProjectApplied.current = true;
    // No token for that project means the reader was never authorised for it —
    // leave them where they are rather than failing a fetch later.
    if (!auth.projects.some((p) => p.id === wanted)) return;
    if (auth.selectedProject !== wanted) selectProject(wanted);
  }, [auth, selectProject]);

  const project = auth?.selectedProject ?? null;
  const projectToken = auth?.projects.find((p) => p.id === project)?.token ?? null;

  // Which model actually answers (RD18). agentd computes it from its own
  // credentials and reports it on /auth/config; the shell never guesses.
  const credentialMode = authConfig?.credential_mode ?? null;

  const chatConfig = useMemo(() => {
    const token = devMode ? devToken : projectToken;
    return {
      apiBaseUrl: API,
      // Raw token — the chat-ui hook/provider prepend "Bearer " themselves.
      getAuthToken: () => token ?? "",
      // The id is what the server is asked for; the LABEL must not claim Opus
      // answered when the offline mock did.
      models: [
        {
          id: "claude-opus-4-5",
          label: credentialMode === "mock" ? "Opus (mock — no model called)" : "Opus",
        },
      ],
    };
  }, [devMode, devToken, projectToken, credentialMode]);

  if (authConfig === null) return null; // waiting for /auth/config

  // ── Dev mode: legacy zero-login demo ──────────────────────────────────────
  if (devMode) {
    if (devToken === null) return null; // waiting for /dev/token
    return (
      <ThemeProvider theme={theme}>
        <CssBaseline />
        <AgentChatProvider config={chatConfig}>
          <Box sx={{ display: "flex", height: "100vh" }}>
            <Box sx={{ width: 280, borderRight: 1, borderColor: "divider" }}>
              <Box sx={{ px: 1.5, py: 1 }}>
                <CredentialModeBadge mode={credentialMode} />
              </Box>
              <DevSessionList />
            </Box>
            <Box sx={{ flex: 1 }}><AgentChat /></Box>
          </Box>
        </AgentChatProvider>
      </ThemeProvider>
    );
  }

  // ── Login modes ────────────────────────────────────────────────────────────
  if (!auth) {
    return (
      <ThemeProvider theme={theme}>
        <CssBaseline />
        <LoginScreen apiBase={API} config={authConfig} onLogin={handleLogin} />
      </ThemeProvider>
    );
  }

  if (!project || !projectToken) {
    return (
      <ThemeProvider theme={theme}>
        <CssBaseline />
        <ProjectPicker auth={auth} onSelect={selectProject} onCreate={createProject} onSignOut={signOut} />
      </ThemeProvider>
    );
  }

  return (
    <ThemeProvider theme={theme}>
      <CssBaseline />
      {/* Keyed by project: switching remounts the provider with the new token. */}
      <AgentChatProvider key={project} config={chatConfig}>
        <ProjectWorkspace
          auth={auth}
          credentialMode={credentialMode}
          project={project}
          onSwitchProject={selectProject}
          onCreateProject={createProject}
          onSignOut={signOut}
        />
      </AgentChatProvider>
    </ThemeProvider>
  );
}

/**
 * The signed-in, project-scoped workspace: desk, chart, chat, workers, events,
 * automation and project settings behind one switch, with the session permalink
 * bound to the URL.
 *
 * It is a separate component because `useSessionPermalink` reads the chat
 * context — the hook has to run *inside* <AgentChatProvider>, not beside it.
 */
function ProjectWorkspace({
  auth,
  credentialMode,
  project,
  onSwitchProject,
  onCreateProject,
  onSignOut,
}: {
  auth: AuthState;
  /** "mock" | "api-key" | "subscription" from /auth/config; null when unknown. */
  credentialMode: string | null;
  project: string;
  onSwitchProject: (projectID: string) => void;
  onCreateProject: (projectID: string) => Promise<void>;
  onSignOut: () => void;
}) {
  const [view, setView] = useState<View>("desk");

  // Worker names for the subscription/schedule pickers. Fetched here because
  // AutomationPage takes them as a prop — a library page never fetches another
  // page's collection for itself.
  const { workers } = useWorkers();
  const workerOptions = useMemo(() => workers.map((w) => w.name), [workers]);

  // The only number in the chrome (design §3.5): how many things are asking for
  // you — through useAsksCount, which applies the very join the Asks stack
  // applies (doc 21, X7). The badge used to count open attention requests, a
  // superset: it read 2 above a stack of 1.
  //
  // While the Desk is open it already holds both lists, so it reports its own
  // count up and this hook stands down (W4 collapsing X7's duplicate fetch).
  const onDesk = view === "desk";
  const [deskAsks, setDeskAsks] = useState(0);
  const { count: fetchedAsks } = useAsksCount({ enabled: !onDesk });
  const openAsks = onDesk ? deskAsks : fetchedAsks;

  // URL ⇄ active session, both directions: a pasted /p/<project>/s/<session>
  // resumes that session, and whatever session is open is already permalinked.
  const { openSession, routeSessionId } = useSessionPermalink({ projectId: project });

  // Whenever the routed session CHANGES, show it. Same reasoning as
  // `showSession` below, applied to the two paths that do not go through it: a
  // pasted permalink (URL → state) and "New session" in the sidebar (state →
  // URL). Both used to resume a session behind the Desk — the landing view
  // since K1 — so the user clicked a link, the session really did resume, and
  // the screen showed the Desk's "no workers yet" panel. Nothing said so.
  //
  // Render-phase and keyed on the id, not an effect: an effect would paint the
  // wrong view first. Switching only on a *change* leaves the human free to
  // walk to Workers or Settings with a session open.
  const shownSession = useRef<string | null>(null);
  if (routeSessionId !== null && shownSession.current !== routeSessionId) {
    shownSession.current = routeSessionId;
    setView("chat");
  }

  // A job row in the workers view is a link to the session that ran it — open
  // it *and* show it, since resuming a session behind a hidden tab would look
  // like nothing happened.
  const showSession = useCallback(
    (sessionId: string) => {
      openSession(sessionId);
      setView("chat");
    },
    [openSession],
  );

  return (
    <Box sx={{ display: "flex", height: "100vh" }}>
      <Box sx={{ width: 280, borderRight: 1, borderColor: "divider", display: "flex", flexDirection: "column", minHeight: 0 }}>
        {/* Which model answers, on every view, permanently (RD18). In mock mode
            — the default — everything below this line is canned output. */}
        <Box sx={{ px: 1.5, pt: 1.5 }}>
          <CredentialModeBadge mode={credentialMode} />
        </Box>
        <ViewNav view={view} onChange={setView} asks={openAsks} />
        {/* The sidebar stays mounted in every view: it carries the project
            switcher and the session list, which are how you leave a view. */}
        <Box sx={{ flex: 1, minHeight: 0, display: "flex", flexDirection: "column" }}>
          <Sidebar auth={auth} project={project} onSwitchProject={onSwitchProject} onCreateProject={onCreateProject} onSignOut={onSignOut} />
        </Box>
      </Box>

      <Box sx={{ flex: 1, minWidth: 0, overflowY: "auto" }}>
        {view === "desk" && (
          <DeskPage
            projectId={project}
            refreshMs={LIVE_REFRESH_MS}
            onOpenSession={showSession}
            onAsksCount={setDeskAsks}
            onStartFromTopology={() => setView("workers")}
            onOpenChat={() => setView("chat")}
          />
        )}
        {/* Schedules are not edited on the canvas (K3): a clock is a deep link
            to the row on Automation. */}
        {view === "chart" && (
          <OrgChartPage projectId={project} onOpenAutomation={() => setView("automation")} />
        )}
        {view === "chat" && <AgentChat />}
        {view === "workers" && <WorkersPage projectId={project} onOpenSession={showSession} />}
        {/* No fetchConfigEvents: GET /agent/config-events is mounted, so the
            changelog tab reads the route directly. */}
        {view === "memory" && <MemoryBrowserPage onOpenSession={showSession} />}
        {view === "events" && (
          <EventsPage projectId={project} refreshMs={LIVE_REFRESH_MS} onOpenSession={showSession} />
        )}
        {view === "automation" && <AutomationPage projectId={project} workerOptions={workerOptions} />}
        {view === "settings" && <ProjectSettingsPage />}
      </Box>
    </Box>
  );
}

/** The view switch. Desk first, and it carries the one badge in the chrome. */
function ViewNav({ view, onChange, asks }: { view: View; onChange: (v: View) => void; asks: number }) {
  const item = (key: View, label: string, badge = 0) => (
    <Button
      key={key}
      size="small"
      variant={view === key ? "contained" : "text"}
      onClick={() => onChange(key)}
      data-testid={`nav-${key}`}
      sx={{ textTransform: "none", flexGrow: 1, minWidth: 0 }}
    >
      {badge > 0 ? `${label} ${badge}` : label}
    </Button>
  );
  // Seven views no longer fit one 280px row (the Wave-4 screenshot pass caught
  // the labels colliding), so the nav wraps: reading order keeps Desk first.
  return (
    <Box
      sx={{ p: 1, borderBottom: 1, borderColor: "divider", display: "flex", flexWrap: "wrap", gap: 0.5 }}
    >
      {item("desk", "Desk", asks)}
      {item("chart", "Chart")}
      {item("chat", "Chat")}
      {item("workers", "Workers")}
      {item("memory", "Memory")}
      {item("events", "Events")}
      {item("automation", "Automation")}
      {item("settings", "Settings")}
    </Box>
  );
}

// Dev mode keeps the original minimal list.
function DevSessionList() {
  return <AgentSessionList />;
}
