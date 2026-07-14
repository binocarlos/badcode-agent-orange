// Left sidebar: project switcher + "New session" + the library's
// ChatHistoryDrawer (session rows with the filter-by-user select).
import { useEffect, useMemo, useState } from "react";
import { Box, Button, MenuItem, Select, Typography } from "@mui/material";
import AddIcon from "@mui/icons-material/Add";
import { ChatHistoryDrawer, useAgentChat, useAgentSessions } from "@agentkit/chat-ui";
import { AuthState } from "./auth";

const NEW_PROJECT_SENTINEL = "__new-project__";

export default function Sidebar({
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
  const { sessions, refresh, select } = useAgentSessions();
  const { createSession, session, isCreating } = useAgentChat();
  const [userFilter, setUserFilter] = useState<string>("me");
  const [searchQuery, setSearchQuery] = useState("");

  useEffect(() => {
    void refresh({ userEmail: userFilter === "me" ? undefined : userFilter });
  }, [refresh, userFilter]);

  // Distinct creators among the loaded sessions feed the per-user filter options.
  const users = useMemo(
    () => Array.from(new Set(sessions.map((s) => s.user_email).filter(Boolean))),
    [sessions],
  );

  const newSession = async () => {
    const id = await createSession({ customer: project, workflow_id: "agent" });
    if (id) void refresh({ userEmail: userFilter === "me" ? undefined : userFilter });
  };

  return (
    <Box data-testid="session-sidebar" sx={{ display: "flex", flexDirection: "column", height: "100%" }}>
      <Box sx={{ p: 1.5, borderBottom: "1px solid rgba(0,0,0,0.06)", display: "flex", flexDirection: "column", gap: 1 }}>
        <Select
          native
          size="small"
          value={project}
          onChange={(e) => {
            if (e.target.value === NEW_PROJECT_SENTINEL) {
              const name = window.prompt("New project id (kebab-case, e.g. apples-oranges):");
              if (name?.trim()) void onCreateProject(name.trim()).catch((err) => window.alert(String(err)));
              return;
            }
            onSwitchProject(e.target.value);
          }}
          inputProps={{ "data-testid": "project-switcher" }}
          sx={{ fontSize: 13 }}
        >
          {auth.projects.map((p) => (
            <option key={p.id} value={p.id}>{p.id}</option>
          ))}
          {auth.wildcard && <option value={NEW_PROJECT_SENTINEL}>＋ New project…</option>}
        </Select>
        <Button
          variant="contained"
          size="small"
          startIcon={<AddIcon />}
          onClick={newSession}
          disabled={isCreating}
          data-testid="new-session"
          sx={{ textTransform: "none" }}
        >
          New session
        </Button>
        <Box sx={{ display: "flex", alignItems: "center", justifyContent: "space-between" }}>
          <Typography sx={{ fontSize: 11, color: "#6b7280", overflow: "hidden", textOverflow: "ellipsis" }}>
            {auth.email}
          </Typography>
          <Button size="small" onClick={onSignOut} sx={{ textTransform: "none", fontSize: 11, minWidth: 0 }}>
            Sign out
          </Button>
        </Box>
      </Box>
      <ChatHistoryDrawer
        open
        onClose={() => {}}
        sessions={sessions}
        activeSessionId={session?.id}
        onSelectSession={select}
        users={users}
        selectedUserEmail={userFilter}
        onUserFilterChange={setUserFilter}
        currentUserEmail={auth.email}
        searchQuery={searchQuery}
        onSearchChange={setSearchQuery}
      />
    </Box>
  );
}
