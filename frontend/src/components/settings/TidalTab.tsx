import { useState, useEffect, useCallback, useRef } from "react";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Link, ExternalLink, RefreshCw } from "lucide-react";
import { toastWithSound as toast } from "@/lib/toast-with-sound";
import {
  GetTidalStatus,
  DisconnectTidal,
  StartTidalDeviceAuth,
  PollTidalDeviceAuth,
  type TidalStatus,
  type TidalDeviceAuth,
} from "@/lib/rpc";

// TidalTab — connect/disconnect a personal Tidal account via device auth.
// The component only mounts while its tab is active, so status loads on
// mount and any in-flight device-auth poll is cleared on unmount (leaving
// the tab), replacing the two activeTab-gated effects the parent used to hold.
export function TidalTab() {
  const [tidalStatus, setTidalStatus] = useState<TidalStatus | null>(null);
  const [tidalDeviceAuth, setTidalDeviceAuth] =
    useState<TidalDeviceAuth | null>(null);
  const [tidalPolling, setTidalPolling] = useState(false);
  const tidalPollRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const stopTidalPoll = useCallback(() => {
    if (tidalPollRef.current) {
      clearInterval(tidalPollRef.current);
      tidalPollRef.current = null;
    }
    setTidalPolling(false);
  }, []);

  const loadTidalStatus = useCallback(async () => {
    try {
      setTidalStatus(await GetTidalStatus());
    } catch {
      /* ignore */
    }
  }, []);

  useEffect(() => {
    // Loading Tidal auth status on mount (tab became active) is
    // external-system sync, not a derived render value; the cleanup clears
    // any in-flight device-auth poll when the tab is left (unmount).
    // eslint-disable-next-line react-hooks/set-state-in-effect
    loadTidalStatus();
    return () => stopTidalPoll();
  }, [loadTidalStatus, stopTidalPoll]);

  const handleTidalConnect = async () => {
    stopTidalPoll();
    setTidalDeviceAuth(null);
    try {
      const auth = await StartTidalDeviceAuth();
      setTidalDeviceAuth(auth);
      setTidalPolling(true);

      const interval = Math.max(auth.interval || 5, 5) * 1000;
      tidalPollRef.current = setInterval(async () => {
        try {
          const result = await PollTidalDeviceAuth(auth.device_code);
          if (result.status === "authorized") {
            stopTidalPoll();
            setTidalDeviceAuth(null);
            await loadTidalStatus();
            toast.success("Tidal account connected");
          } else if (
            result.status === "expired" ||
            result.status === "denied" ||
            result.status === "error"
          ) {
            stopTidalPoll();
            setTidalDeviceAuth(null);
            toast.error("Tidal connection failed", {
              description: result.error || result.status,
            });
          }
          // "pending" → continuer à poller
        } catch {
          // erreur réseau passagère → continuer
        }
      }, interval);
    } catch (err) {
      toast.error("Failed to start Tidal authentication", {
        description: err instanceof Error ? err.message : "Unknown error",
      });
    }
  };

  const handleTidalDisconnect = async () => {
    stopTidalPoll();
    setTidalDeviceAuth(null);
    try {
      await DisconnectTidal();
      setTidalStatus({ connected: false });
      toast.success("Tidal account disconnected");
    } catch (err) {
      toast.error("Failed to disconnect", {
        description: err instanceof Error ? err.message : "Unknown error",
      });
    }
  };

  return (
    <div className="space-y-6 max-w-2xl">
      <div>
        <h2 className="text-base font-semibold mb-1">Tidal Account</h2>
        <p className="text-sm text-muted-foreground mb-4">
          Connect your personal Tidal account for full-quality Tidal downloads.
          Without it, Tidal is handled by the download engine.
        </p>
      </div>

      {tidalStatus === null ? (
        <div className="text-sm text-muted-foreground">Loading...</div>
      ) : tidalStatus.connected ? (
        <div className="space-y-4">
          <div className="flex items-center gap-3 border rounded-lg px-4 py-3 bg-muted/20">
            <span className="h-2.5 w-2.5 rounded-full bg-green-500 shrink-0" />
            <div className="flex-1">
              <p className="text-sm font-medium">Connected</p>
              {tidalStatus.expires_at && (
                <p className="text-xs text-muted-foreground">
                  Expires{" "}
                  {new Date(
                    tidalStatus.expires_at * 1000,
                  ).toLocaleDateString()}
                </p>
              )}
            </div>
            <Button
              variant="outline"
              size="sm"
              onClick={handleTidalDisconnect}
              className="gap-1.5 text-destructive hover:text-destructive"
            >
              <Link className="h-4 w-4" />
              Disconnect
            </Button>
          </div>
        </div>
      ) : (
        <div className="space-y-4">
          <div className="flex items-center gap-3 border rounded-lg px-4 py-3 bg-muted/20">
            <span className="h-2.5 w-2.5 rounded-full bg-muted-foreground shrink-0" />
            <p className="text-sm flex-1">Not connected</p>
            <Button
              size="sm"
              onClick={handleTidalConnect}
              disabled={tidalPolling}
              className="gap-1.5"
            >
              <ExternalLink className="h-4 w-4" />
              {tidalPolling
                ? "Waiting for authorization..."
                : "Connect with Tidal"}
            </Button>
          </div>

          {tidalDeviceAuth && (
            <div className="space-y-3 border rounded-lg px-4 py-3 bg-muted/10">
              <div className="space-y-1.5">
                <Label className="text-sm font-medium">
                  Step 1 — Open the Tidal authorization page:
                </Label>
                <div className="flex items-center gap-2">
                  <a
                    href={tidalDeviceAuth.verification_uri_complete}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="flex-1"
                  >
                    <Button
                      variant="outline"
                      size="sm"
                      className="w-full gap-1.5"
                    >
                      <ExternalLink className="h-3.5 w-3.5" />
                      Open Tidal authorization page
                    </Button>
                  </a>
                </div>
                {tidalDeviceAuth.user_code && (
                  <p className="text-xs text-muted-foreground">
                    If asked for a code, enter:{" "}
                    <code className="font-mono font-bold text-foreground">
                      {tidalDeviceAuth.user_code}
                    </code>
                  </p>
                )}
              </div>
              <div className="flex items-center gap-2 text-sm text-muted-foreground">
                <RefreshCw className="h-3.5 w-3.5 animate-spin shrink-0" />
                <span>
                  Waiting for you to authorize… (checking every{" "}
                  {Math.max(tidalDeviceAuth.interval || 5, 5)}s)
                </span>
              </div>
              <Button
                variant="ghost"
                size="sm"
                onClick={() => {
                  stopTidalPoll();
                  setTidalDeviceAuth(null);
                }}
                className="text-xs h-7"
              >
                Cancel
              </Button>
            </div>
          )}
        </div>
      )}
      <Button
        variant="outline"
        size="sm"
        onClick={loadTidalStatus}
        className="gap-1.5"
      >
        <RefreshCw className="h-3.5 w-3.5" />
        Refresh Status
      </Button>
    </div>
  );
}
