import { useCallback, useEffect, useMemo, useState } from "react";
import { ThemeProvider, createTheme, CssBaseline, Box } from "@mui/material";
import { AgentChatProvider, AgentChat, AgentSessionList } from "@agentkit/chat-ui";
import { AuthConfig, AuthState, clearAuthState, fetchAuthConfig, loadAuthState, mintProjectToken, saveAuthState } from "./auth";
import LoginScreen from "./LoginScreen";
import ProjectPicker from "./ProjectPicker";
import Sidebar from "./Sidebar";

const theme = createTheme();
const API = import.meta.env.VITE_API ?? ""; // "" → same origin (nginx proxy)

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
        <Box sx={{ display: "flex", height: "100vh" }}>
          <Box sx={{ width: 280, borderRight: 1, borderColor: "divider" }}>
            <Sidebar auth={auth} project={project} onSwitchProject={selectProject} onCreateProject={createProject} onSignOut={signOut} />
          </Box>
          <Box sx={{ flex: 1 }}><AgentChat /></Box>
        </Box>
      </AgentChatProvider>
    </ThemeProvider>
  );
}

// Dev mode keeps the original minimal list.
function DevSessionList() {
  return <AgentSessionList />;
}
