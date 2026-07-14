// Auth plumbing for the standalone web app: reads agentd's runtime /auth/config,
// exchanges a Google credential or the fixed test password for per-project JWTs,
// and persists the signed-in state in localStorage.

export interface AuthConfig {
  modes: Array<"google" | "password" | "dev">;
  google_client_id: string;
}

export interface ProjectToken {
  id: string;
  token: string;
}

export interface AuthState {
  email: string;
  projects: ProjectToken[];
  selectedProject: string | null;
  /** Wildcard grant: user may mint tokens for new project IDs. */
  wildcard?: boolean;
  loginToken?: string;
}

const STORAGE_KEY = "agent-orange-auth";

export async function fetchAuthConfig(apiBase: string): Promise<AuthConfig> {
  const r = await fetch(`${apiBase}/auth/config`);
  if (!r.ok) throw new Error(`auth config: HTTP ${r.status}`);
  return (await r.json()) as AuthConfig;
}

export async function loginWithGoogle(apiBase: string, credential: string): Promise<AuthState> {
  return login(`${apiBase}/auth/google`, { credential });
}

export async function loginWithPassword(apiBase: string, email: string, password: string): Promise<AuthState> {
  return login(`${apiBase}/auth/password`, { email, password });
}

async function login(url: string, body: unknown): Promise<AuthState> {
  const r = await fetch(url, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!r.ok) throw new Error(r.status === 403 ? "No projects for this account" : "Sign-in failed");
  const data = (await r.json()) as {
    email: string;
    projects: ProjectToken[];
    wildcard?: boolean;
    login_token?: string;
  };
  const state: AuthState = {
    email: data.email,
    projects: data.projects ?? [],
    // Wildcard users always see the picker (they may want a new project).
    selectedProject: !data.wildcard && data.projects.length === 1 ? data.projects[0].id : null,
    wildcard: data.wildcard,
    loginToken: data.login_token,
  };
  saveAuthState(state);
  return state;
}

/** Exchange a wildcard login token for a token scoped to (possibly new) project. */
export async function mintProjectToken(apiBase: string, loginToken: string, project: string): Promise<ProjectToken> {
  const r = await fetch(`${apiBase}/auth/project-token`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ token: loginToken, project }),
  });
  if (!r.ok) throw new Error(await r.text());
  return (await r.json()) as ProjectToken;
}

/** Decode a JWT's exp claim locally (no verification — expiry hygiene only). */
function tokenExpired(token: string): boolean {
  try {
    const payload = JSON.parse(atob(token.split(".")[1])) as { exp?: number };
    return !payload.exp || payload.exp * 1000 < Date.now() + 60_000;
  } catch {
    return true;
  }
}

export function loadAuthState(): AuthState | null {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return null;
    const state = JSON.parse(raw) as AuthState;
    if (!state.email || !Array.isArray(state.projects) || state.projects.length === 0) return null;
    if (state.projects.some((p) => tokenExpired(p.token))) {
      clearAuthState();
      return null;
    }
    return state;
  } catch {
    return null;
  }
}

export function saveAuthState(state: AuthState): void {
  localStorage.setItem(STORAGE_KEY, JSON.stringify(state));
}

export function clearAuthState(): void {
  localStorage.removeItem(STORAGE_KEY);
}

// ── Google Identity Services ────────────────────────────────────────────────

declare global {
  interface Window {
    google?: {
      accounts: {
        id: {
          initialize(config: { client_id: string; callback: (resp: { credential: string }) => void }): void;
          renderButton(el: HTMLElement, options: Record<string, unknown>): void;
        };
      };
    };
  }
}

let gisLoading: Promise<void> | null = null;

export function loadGis(): Promise<void> {
  if (window.google?.accounts?.id) return Promise.resolve();
  if (!gisLoading) {
    gisLoading = new Promise((resolve, reject) => {
      const script = document.createElement("script");
      script.src = "https://accounts.google.com/gsi/client";
      script.async = true;
      script.onload = () => resolve();
      script.onerror = () => reject(new Error("failed to load Google Identity Services"));
      document.head.appendChild(script);
    });
  }
  return gisLoading;
}

export async function renderGoogleButton(
  el: HTMLElement,
  clientID: string,
  onCredential: (credential: string) => void,
): Promise<void> {
  await loadGis();
  window.google!.accounts.id.initialize({ client_id: clientID, callback: (resp) => onCredential(resp.credential) });
  window.google!.accounts.id.renderButton(el, { theme: "outline", size: "large", width: 280 });
}
