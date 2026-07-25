import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ThemeProvider, createTheme, CssBaseline, Box, Button, Stack } from "@mui/material";
import {
  AgentChatProvider,
  AgentChat,
  AgentSessionList,
  ProjectSettingsPage,
  WorkersPage,
  projectIdFromLocation,
  useSessionPermalink,
} from "@agentkit/chat-ui";
import { AuthConfig, AuthState, clearAuthState, fetchAuthConfig, loadAuthState, mintProjectToken, saveAuthState } from "./auth";
import LoginScreen from "./LoginScreen";
import ProjectPicker from "./ProjectPicker";
import Sidebar from "./Sidebar";

const theme = createTheme();
const API = import.meta.env.VITE_API ?? ""; // "" → same origin (nginx proxy)

// The three things a project view can show. Deliberately a state machine and
// not a router: the library must not impose react-router on hosts, and the
// permalink hook already owns the one URL that matters (the session).
type View = "chat" | "workers" | "settings";

// App state machine: loading → dev (legacy /dev/token, straight to chat)
//                            → login → project picker → chat (per-project JWT)
export default function App() {
  const [authConfig, setAuthConfig] = useState<AuthConfig | null>(null);
  const [auth, setAuth] = useState<AuthState | null>(() => loadAuthState());
  const [devToken, setDevToken] = useState<string | null>(null);

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

  const chatConfig = useMemo(() => {
    const token = devMode ? devToken : projectToken;
    return {
      apiBaseUrl: API,
      // Raw token — the chat-ui hook/provider prepend "Bearer " themselves.
      getAuthToken: () => token ?? "",
      models: [{ id: "claude-opus-4-5", label: "Opus" }],
    };
  }, [devMode, devToken, projectToken]);

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
 * The signed-in, project-scoped workspace: chat, workers and project settings
 * behind a three-way switch, with the session permalink bound to the URL.
 *
 * It is a separate component because `useSessionPermalink` reads the chat
 * context — the hook has to run *inside* <AgentChatProvider>, not beside it.
 */
function ProjectWorkspace({
  auth,
  project,
  onSwitchProject,
  onCreateProject,
  onSignOut,
}: {
  auth: AuthState;
  project: string;
  onSwitchProject: (projectID: string) => void;
  onCreateProject: (projectID: string) => Promise<void>;
  onSignOut: () => void;
}) {
  const [view, setView] = useState<View>("chat");

  // URL ⇄ active session, both directions: a pasted /p/<project>/s/<session>
  // resumes that session, and whatever session is open is already permalinked.
  const { openSession } = useSessionPermalink({ projectId: project });

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
        <ViewNav view={view} onChange={setView} />
        {/* The sidebar stays mounted in every view: it carries the project
            switcher and the session list, which are how you leave a view. */}
        <Box sx={{ flex: 1, minHeight: 0, display: "flex", flexDirection: "column" }}>
          <Sidebar auth={auth} project={project} onSwitchProject={onSwitchProject} onCreateProject={onCreateProject} onSignOut={onSignOut} />
        </Box>
      </Box>

      <Box sx={{ flex: 1, minWidth: 0, overflowY: "auto" }}>
        {view === "chat" && <AgentChat />}
        {view === "workers" && <WorkersPage projectId={project} onOpenSession={showSession} />}
        {view === "settings" && <ProjectSettingsPage />}
      </Box>
    </Box>
  );
}

/** The three-way view switch. */
function ViewNav({ view, onChange }: { view: View; onChange: (v: View) => void }) {
  const item = (key: View, label: string) => (
    <Button
      key={key}
      size="small"
      variant={view === key ? "contained" : "text"}
      onClick={() => onChange(key)}
      data-testid={`nav-${key}`}
      sx={{ textTransform: "none", flex: 1, minWidth: 0 }}
    >
      {label}
    </Button>
  );
  return (
    <Stack direction="row" spacing={0.5} sx={{ p: 1, borderBottom: "1px solid rgba(0,0,0,0.06)" }}>
      {item("chat", "Chat")}
      {item("workers", "Workers")}
      {item("settings", "Settings")}
    </Stack>
  );
}

// Dev mode keeps the original minimal list.
function DevSessionList() {
  return <AgentSessionList />;
}
