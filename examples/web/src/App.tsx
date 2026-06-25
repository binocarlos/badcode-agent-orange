import { useEffect, useState } from "react";
import { ThemeProvider, createTheme, CssBaseline, Box } from "@mui/material";
import { AgentChatProvider, AgentSessionList, AgentChat } from "@agentkit/chat-ui";

const theme = createTheme();
const API = import.meta.env.VITE_API ?? ""; // "" → same origin (nginx proxy)

export default function App() {
  const [token, setToken] = useState<string | null>(null);
  useEffect(() => {
    fetch(`${API}/dev/token`)
      .then((r) => r.json())
      .then((j) => setToken(j.token))
      .catch(() => setToken("")); // dev-open fallback
  }, []);

  if (token === null) return null; // brief: waiting for token

  return (
    <ThemeProvider theme={theme}>
      <CssBaseline />
      <AgentChatProvider config={{
        apiBaseUrl: API,
        getAuthToken: () => `Bearer ${token}`,
        models: [{ id: "claude-opus-4-5", label: "Opus" }],
      }}>
        <Box sx={{ display: "flex", height: "100vh" }}>
          <Box sx={{ width: 280, borderRight: 1, borderColor: "divider" }}><AgentSessionList /></Box>
          <Box sx={{ flex: 1 }}><AgentChat /></Box>
        </Box>
      </AgentChatProvider>
    </ThemeProvider>
  );
}
