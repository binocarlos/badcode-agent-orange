// Project picker: shown after login when the account maps to more than one
// project (or has a wildcard grant). A project is a pure namespace over
// sessions (the customer claim); wildcard users can mint a brand-new one here.
import { useState } from "react";
import { Alert, Box, Button, Paper, TextField, Typography } from "@mui/material";
import { AuthState } from "./auth";

export default function ProjectPicker({
  auth,
  onSelect,
  onCreate,
  onSignOut,
}: {
  auth: AuthState;
  onSelect: (projectID: string) => void;
  onCreate: (projectID: string) => Promise<void>;
  onSignOut: () => void;
}) {
  const [newProject, setNewProject] = useState("");
  const [error, setError] = useState<string | null>(null);

  const create = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    try {
      await onCreate(newProject.trim());
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not create project");
    }
  };

  return (
    <Box sx={{ display: "flex", alignItems: "center", justifyContent: "center", height: "100vh", bgcolor: "#f8fafc" }}>
      <Paper sx={{ p: 4, width: 360, display: "flex", flexDirection: "column", gap: 2 }} data-testid="project-picker">
        <Typography variant="h6" sx={{ fontWeight: 600 }}>Choose a project</Typography>
        <Typography variant="body2" color="text.secondary">{auth.email}</Typography>
        {error && <Alert severity="error">{error}</Alert>}
        <Box sx={{ display: "flex", flexDirection: "column", gap: 1 }}>
          {auth.projects.map((p) => (
            <Button
              key={p.id}
              variant="outlined"
              onClick={() => onSelect(p.id)}
              data-testid={`project-option-${p.id}`}
              sx={{ justifyContent: "flex-start", textTransform: "none" }}
            >
              {p.id}
            </Button>
          ))}
          {auth.projects.length === 0 && !auth.wildcard && (
            <Typography variant="body2" color="text.secondary">No projects for this account.</Typography>
          )}
        </Box>
        {auth.wildcard && (
          <Box component="form" onSubmit={create} sx={{ display: "flex", gap: 1 }}>
            <TextField
              size="small"
              fullWidth
              placeholder="new-project-name"
              value={newProject}
              onChange={(e) => setNewProject(e.target.value)}
              slotProps={{ htmlInput: { "data-testid": "new-project-input" } }}
            />
            <Button type="submit" variant="contained" disabled={!newProject.trim()} data-testid="new-project-create" sx={{ textTransform: "none" }}>
              Create
            </Button>
          </Box>
        )}
        <Button size="small" onClick={onSignOut} sx={{ alignSelf: "flex-start", textTransform: "none" }}>
          Sign out
        </Button>
      </Paper>
    </Box>
  );
}
