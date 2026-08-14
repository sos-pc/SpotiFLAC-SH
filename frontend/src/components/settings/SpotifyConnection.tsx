import { useCallback, useEffect, useState } from "react";
import type { Dispatch, SetStateAction } from "react";
import { Copy, ExternalLink, Loader2, Unlink } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { toastWithSound as toast } from "@/lib/toast-with-sound";
import type { Settings as SettingsType } from "@/lib/settings";
import {
  DisconnectSpotify,
  GetSpotifyConnection,
  StartSpotifyConnection,
  type SpotifyConnection as Connection,
} from "@/lib/rpc";
import { InstanceScoped } from "./InstanceScoped";

// Connecting a Spotify account, in two halves that belong to different people.
//
// The operator registers ONE application for the whole deployment and pastes
// its client id here. Everyone then connects their own account against it.
// Both halves live in one place because a user who cannot connect needs to see
// why in the same glance — "your administrator has not set this up" is an
// answer, a dead button is not.
//
// There is no client secret field, and that is not an omission. The flow is
// Authorization Code + PKCE, which needs none, so the one credential this
// feature could have leaked is never asked for, never transmitted and never
// stored.

// Spotify applications stay in development mode unless they pass a review this
// project would not: the developer policy forbids using the API in connection
// with downloading. So the cap is real and permanent, and worth stating before
// someone invites a thirtieth person.
const DEV_MODE_USER_CAP = 25;

export function SpotifyConnection({
  isAdmin,
  tempSettings,
  setTempSettings,
}: {
  isAdmin: boolean;
  tempSettings: SettingsType;
  setTempSettings: Dispatch<SetStateAction<SettingsType>>;
}) {
  const [conn, setConn] = useState<Connection | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      setConn(await GetSpotifyConnection());
    } catch (e) {
      toast.error(`Could not read the Spotify connection: ${e}`);
    }
  }, []);

  useEffect(() => {
    // Reading the connection on mount is external-system sync, not a
    // derived render value — the same shape as ApisTab's status load.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void load();
  }, [load]);

  // The callback redirects back with ?spotify=…, because it is a top-level
  // navigation and has nowhere else to report to. Reading it here is what turns
  // that redirect into a message rather than a silently changed page.
  useEffect(() => {
    const outcome = new URLSearchParams(window.location.search).get("spotify");
    if (!outcome) return;
    // Same reason: this reacts to what the browser came back with, which
    // is external state, not something derived from a render.
    if (outcome === "connected") {
      toast.success("Spotify account connected");
      // eslint-disable-next-line react-hooks/set-state-in-effect
      void load();
    } else if (outcome === "declined") {
      toast.info("Connection cancelled on Spotify");
    } else if (outcome === "failed") {
      toast.error("Spotify refused the connection — check the redirect URI");
    }
    const url = new URL(window.location.href);
    url.searchParams.delete("spotify");
    window.history.replaceState({}, "", url);
  }, [load]);

  const connect = useCallback(async () => {
    setBusy(true);
    try {
      // A full navigation, not a popup: Spotify's consent screen refuses to be
      // framed, and a popup is what a browser blocks when it did not come
      // straight from a click it recognises.
      window.location.href = await StartSpotifyConnection();
    } catch (e) {
      toast.error(`Could not start the connection: ${e}`);
      setBusy(false);
    }
  }, []);

  const disconnect = useCallback(async () => {
    setBusy(true);
    try {
      await DisconnectSpotify();
      toast.success("Disconnected");
      await load();
    } catch (e) {
      toast.error(`Could not disconnect: ${e}`);
    } finally {
      setBusy(false);
    }
  }, [load]);

  const copyRedirect = useCallback(async () => {
    if (!conn) return;
    try {
      await navigator.clipboard.writeText(conn.redirect_uri);
      toast.success("Redirect URI copied");
    } catch {
      toast.error("Could not copy — select it and copy by hand");
    }
  }, [conn]);

  if (!conn) {
    return (
      <p className="flex items-center gap-2 text-sm text-muted-foreground">
        <Loader2 className="h-4 w-4 animate-spin" />
        Reading the Spotify connection…
      </p>
    );
  }

  return (
    <div className="space-y-4">
      <div>
        <h2 className="text-base font-semibold mb-1">Your Spotify account</h2>
        <p className="text-sm text-muted-foreground">
          Connecting it lets the playlist picker offer your own playlists,
          including private and collaborative ones, with their track counts.
          Public profiles work without it.
        </p>
      </div>

      {/* The operator's half. Wrapped rather than hidden for a non-admin: they
          need to see that the setup exists and who owns it. */}
      <InstanceScoped canEdit={isAdmin} what="The Spotify application">
        <div className="space-y-3 rounded-lg border p-3">
          <div className="space-y-1.5">
            <Label htmlFor="spotify-client-id">Client ID</Label>
            <Input
              id="spotify-client-id"
              value={tempSettings.spotifyClientId}
              onChange={(e) =>
                setTempSettings((prev) => ({
                  ...prev,
                  spotifyClientId: e.target.value,
                }))
              }
              placeholder="From your app on developer.spotify.com"
            />
            <p className="text-xs text-muted-foreground">
              Not a secret: it appears in every authorization URL. There is no
              client-secret field because the flow does not use one.
            </p>
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="spotify-redirect">Redirect URI to register</Label>
            <div className="flex gap-2">
              <Input
                id="spotify-redirect"
                value={conn.redirect_uri}
                readOnly
                className="font-mono text-xs"
              />
              <Button
                type="button"
                variant="outline"
                size="icon"
                onClick={copyRedirect}
                title="Copy the redirect URI"
                className="shrink-0"
              >
                <Copy className="h-4 w-4" />
              </Button>
            </div>
            <p className="text-xs text-muted-foreground">
              Paste this into the app's settings on Spotify, exactly. A
              mismatch is refused by Spotify with no explanation reaching this
              side. Up to {DEV_MODE_USER_CAP} accounts can be connected, and
              each has to be added there by the email of its Spotify account.
            </p>
          </div>
        </div>
      </InstanceScoped>

      {/* Everyone's half. */}
      {!conn.configured ? (
        <p className="rounded-md border border-dashed p-3 text-sm text-muted-foreground">
          No Spotify application is configured for this instance yet, so no
          account can be connected.{" "}
          {isAdmin
            ? "Fill in the client id above and save."
            : "Your administrator has to set one up."}
        </p>
      ) : conn.connected ? (
        <div className="flex items-center justify-between gap-3 rounded-md border p-3">
          <div className="min-w-0">
            <p className="text-sm">
              Connected as{" "}
              <span className="font-medium">
                {conn.display_name || conn.spotify_id}
              </span>
            </p>
            {conn.connected_at && (
              // Refresh tokens can carry a finite lifetime — 180 days on this
              // deployment — and the only symptom of expiry is an empty
              // playlist list. Showing the date is what lets someone recognise
              // that before wondering where their playlists went.
              <p className="text-xs text-muted-foreground">
                since {new Date(conn.connected_at).toLocaleDateString()} —
                reconnect if your playlists stop appearing
              </p>
            )}
          </div>
          <Button
            variant="outline"
            size="sm"
            onClick={disconnect}
            disabled={busy}
            className="gap-1.5 shrink-0"
          >
            <Unlink className="h-3.5 w-3.5" />
            Disconnect
          </Button>
        </div>
      ) : (
        <div className="flex items-center justify-between gap-3 rounded-md border p-3">
          <p className="text-sm text-muted-foreground">
            Not connected. You will be sent to Spotify to approve read access to
            your playlists, and nothing else.
          </p>
          <Button onClick={connect} disabled={busy} className="gap-1.5 shrink-0">
            <ExternalLink className="h-3.5 w-3.5" />
            Connect
          </Button>
        </div>
      )}
    </div>
  );
}
