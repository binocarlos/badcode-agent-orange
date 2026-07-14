// Login screen: renders whichever modes agentd advertises via /auth/config —
// a Google Sign-In button and/or the fixed email+password form (test mode).
import { useEffect, useRef, useState } from "react";
import { Alert, Box, Button, Divider, Paper, TextField, Typography } from "@mui/material";
import {
  AuthConfig,
  AuthState,
  loginWithGoogle,
  loginWithPassword,
  renderGoogleButton,
} from "./auth";

export default function LoginScreen({
  apiBase,
  config,
  onLogin,
}: {
  apiBase: string;
  config: AuthConfig;
  onLogin: (state: AuthState) => void;
}) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const googleRef = useRef<HTMLDivElement>(null);

  const showGoogle = config.modes.includes("google");
  const showPassword = config.modes.includes("password");

  useEffect(() => {
    if (!showGoogle || !googleRef.current) return;
    renderGoogleButton(googleRef.current, config.google_client_id, async (credential) => {
      setError(null);
      setBusy(true);
      try {
        onLogin(await loginWithGoogle(apiBase, credential));
      } catch (e) {
        setError(e instanceof Error ? e.message : "Sign-in failed");
      } finally {
        setBusy(false);
      }
    }).catch(() => setError("Could not load Google Sign-In"));
  }, [showGoogle, config.google_client_id, apiBase, onLogin]);

  const submitPassword = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setBusy(true);
    try {
      onLogin(await loginWithPassword(apiBase, email, password));
    } catch (err) {
      setError(err instanceof Error ? err.message : "Sign-in failed");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Box sx={{ display: "flex", alignItems: "center", justifyContent: "center", height: "100vh", bgcolor: "#f8fafc" }}>
      <Paper sx={{ p: 4, width: 360, display: "flex", flexDirection: "column", gap: 2 }} data-testid="login-screen">
        <Typography variant="h5" sx={{ fontWeight: 600 }}>Agent Orange</Typography>
        <Typography variant="body2" color="text.secondary">Sign in to your projects</Typography>
        {error && <Alert severity="error">{error}</Alert>}

        {showGoogle && <Box ref={googleRef} sx={{ display: "flex", justifyContent: "center" }} />}
        {showGoogle && showPassword && <Divider>or</Divider>}

        {showPassword && (
          <Box component="form" onSubmit={submitPassword} sx={{ display: "flex", flexDirection: "column", gap: 1.5 }}>
            <TextField
              label="Email"
              size="small"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              slotProps={{ htmlInput: { "data-testid": "login-email" } }}
            />
            <TextField
              label="Password"
              type="password"
              size="small"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              slotProps={{ htmlInput: { "data-testid": "login-password" } }}
            />
            <Button type="submit" variant="contained" disabled={busy || !email || !password} data-testid="login-submit">
              Sign in
            </Button>
          </Box>
        )}
      </Paper>
    </Box>
  );
}
