import { useState } from "react";
import { login } from "@/lib/auth";
import { Button } from "@/components/ui/button";
import { Music2, Loader2 } from "lucide-react";

interface LoginPageProps {
  onLogin: () => void;
}

export function LoginPage({ onLogin }: LoginPageProps) {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [rateLimited, setRateLimited] = useState(false);
  const [loading, setLoading] = useState(false);

  const handleLogin = async () => {
    if (!username || !password) {
      setError("Please enter your Jellyfin credentials");
      return;
    }
    setLoading(true);
    setError("");
    setRateLimited(false);
    try {
      await login(username, password);
      onLogin();
    } catch (err: any) {
      if (err?.rateLimited) {
        setRateLimited(true);
      } else {
        setError(err.message || "Login failed");
      }
    } finally {
      setLoading(false);
    }
  };

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "Enter") handleLogin();
  };

  return (
    <div className="min-h-screen flex items-center justify-center bg-background">
      <div className="w-full max-w-sm space-y-6 p-8 border rounded-xl bg-card shadow-lg">
        <div className="flex flex-col items-center gap-2">
          <div className="h-12 w-12 rounded-full bg-primary/10 flex items-center justify-center">
            <Music2 className="h-6 w-6 text-primary" />
          </div>
          <h1 className="text-2xl font-bold">SpotiFLAC</h1>
          <p className="text-sm text-muted-foreground text-center">
            Sign in with your Jellyfin account
          </p>
        </div>

        <div className="space-y-3">
          <div className="space-y-1">
            <label className="text-sm font-medium">Username</label>
            <input
              className="w-full px-3 py-2 text-sm border rounded-md bg-background focus:outline-none focus:ring-2 focus:ring-primary"
              placeholder="Jellyfin username"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              onKeyDown={handleKeyDown}
              autoFocus
              autoComplete="username"
            />
          </div>
          <div className="space-y-1">
            <label className="text-sm font-medium">Password</label>
            <input
              type="password"
              className="w-full px-3 py-2 text-sm border rounded-md bg-background focus:outline-none focus:ring-2 focus:ring-primary"
              placeholder="Jellyfin password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              onKeyDown={handleKeyDown}
              autoComplete="current-password"
            />
          </div>

          {rateLimited && (
            <div className="rounded-md border border-yellow-500/40 bg-yellow-500/10 px-3 py-2 text-center">
              <p className="text-sm font-medium text-yellow-600 dark:text-yellow-400">Too many login attempts</p>
              <p className="text-xs text-yellow-600/80 dark:text-yellow-400/80 mt-0.5">Please wait 5 minutes before trying again.</p>
            </div>
          )}
          {error && (
            <p className="text-sm text-red-500 text-center">{error}</p>
          )}

          <Button className="w-full" onClick={handleLogin} disabled={loading}>
            {loading ? (
              <><Loader2 className="h-4 w-4 mr-2 animate-spin" />Signing in...</>
            ) : (
              "Sign in"
            )}
          </Button>
        </div>

        <p className="text-xs text-center text-muted-foreground">
          Access restricted to Jellyfin users
        </p>
      </div>
    </div>
  );
}
