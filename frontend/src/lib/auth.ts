const TOKEN_KEY = "spotiflac_token";
const USER_KEY  = "spotiflac_user";

export interface AuthUser {
  id: string;
  display_name: string;
  is_admin: boolean;
}

export function getToken(): string | null {
  return localStorage.getItem(TOKEN_KEY);
}

export function getUser(): AuthUser | null {
  const raw = localStorage.getItem(USER_KEY);
  if (!raw) return null;
  try { return JSON.parse(raw); } catch { return null; }
}

export function saveAuth(token: string, user: AuthUser) {
  localStorage.setItem(TOKEN_KEY, token);
  localStorage.setItem(USER_KEY, JSON.stringify(user));
}

export function clearAuth() {
  localStorage.removeItem(TOKEN_KEY);
  localStorage.removeItem(USER_KEY);
}

export function isAuthenticated(): boolean {
  return !!getToken();
}

export async function login(username: string, password: string): Promise<AuthUser> {
  const resp = await fetch("/api/v1/auth/login", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ username, password }),
  });
  const data = await resp.json();
  if (!resp.ok) throw new Error(data.error || "Login failed");
  saveAuth(data.token, data.user);
  return data.user;
}

export async function fetchMe(): Promise<AuthUser | null> {
  const token = getToken();
  if (!token) return null;
  try {
    const resp = await fetch("/api/v1/auth/me", {
      headers: { Authorization: `Bearer ${token}` },
    });
    if (!resp.ok) { clearAuth(); return null; }
    return await resp.json();
  } catch {
    return null;
  }
}

// Tente un login automatique si DISABLE_AUTH_ON_LAN=true et IP locale
export async function tryLocalAuth(): Promise<AuthUser | null> {
  try {
    const resp = await fetch("/auth/local", { method: "POST" });
    if (!resp.ok) return null;
    const data = await resp.json();
    if (!data.token) return null;
    saveAuth(data.token, data.user);
    return data.user;
  } catch {
    return null;
  }
}
