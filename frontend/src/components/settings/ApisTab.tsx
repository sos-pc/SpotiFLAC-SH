import { useState, useEffect, useCallback } from "react";
import { SpotifyConnection } from "./SpotifyConnection";
import type { Dispatch, SetStateAction } from "react";
import type { Settings as SettingsType } from "@/lib/settings";
import { Button } from "@/components/ui/button";
import { RefreshCw } from "lucide-react";
import { toastWithSound as toast } from "@/lib/toast-with-sound";
import { GetAPIStatuses, type ServiceStatus } from "@/lib/rpc";


// ApisTab — external service status board.
//
// It also carried a Proxy Configuration panel for the Tidal community proxies
// until 2026-07-28. That list could not produce a download (previews only
// without a personal token) and was removed server-side, so the panel would
// have been a form writing into nothing.
export function ApisTab({
  isAdmin,
  tempSettings,
  setTempSettings,
}: {
  isAdmin: boolean;
  tempSettings: SettingsType;
  setTempSettings: Dispatch<SetStateAction<SettingsType>>;
}) {
  const [apiStatuses, setApiStatuses] = useState<ServiceStatus[] | null>(null);
  const [apisLoading, setApisLoading] = useState(false);

  const loadApiStatuses = useCallback(async () => {
    setApisLoading(true);
    try {
      setApiStatuses(await GetAPIStatuses());
    } catch (err) {
      toast.error("Status check failed", {
        description: err instanceof Error ? err.message : "Unknown error",
      });
    } finally {
      setApisLoading(false);
    }
  }, []);

  useEffect(() => {
    // Loading API status on mount (tab became active) is external-system sync,
    // not a derived render value.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    loadApiStatuses();
  }, [loadApiStatuses]);

  return (
    <div className="space-y-6 max-w-2xl">
      <SpotifyConnection
        isAdmin={isAdmin}
        tempSettings={tempSettings}
        setTempSettings={setTempSettings}
      />

      <div className="border-t" />

      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-base font-semibold mb-1">External APIs</h2>
          <p className="text-sm text-muted-foreground">
            Status of all external services used by SpotiFLAC. Results are
            cached for 30 seconds.
          </p>
        </div>
        <Button
          variant="outline"
          size="sm"
          onClick={loadApiStatuses}
          disabled={apisLoading}
          className="gap-1.5 shrink-0"
        >
          <RefreshCw
            className={`h-3.5 w-3.5 ${apisLoading ? "animate-spin" : ""}`}
          />
          {apisLoading ? "Checking..." : "Refresh"}
        </Button>
      </div>

      {apiStatuses === null ? (
        apisLoading ? (
          <div className="text-sm text-muted-foreground">
            Checking all services...
          </div>
        ) : (
          <div className="text-sm text-muted-foreground">
            Click Refresh to check service status.
          </div>
        )
      ) : (
        <div className="space-y-1.5">
          {apiStatuses.map((svc) => (
            <div
              key={svc.name}
              className="flex items-center gap-3 border rounded-lg px-3 py-2.5 bg-muted/10"
            >
              <span
                className={`h-2 w-2 rounded-full shrink-0 ${
                  svc.status === "ok"
                    ? "bg-green-500"
                    : svc.status === "ratelimited"
                      ? "bg-yellow-500"
                      : svc.status === "unconfigured"
                        ? "bg-muted-foreground"
                        : "bg-red-500"
                }`}
              />
              <div className="flex-1 min-w-0">
                <p className="text-sm font-medium truncate">{svc.name}</p>
                <p className="text-xs text-muted-foreground truncate">
                  {svc.url}
                </p>
                {svc.error && (
                  <p className="text-xs text-destructive truncate">
                    {svc.error}
                  </p>
                )}
              </div>
              <div className="text-right shrink-0">
                <p
                  className={`text-xs font-medium ${
                    svc.status === "ok"
                      ? "text-green-600 dark:text-green-400"
                      : svc.status === "ratelimited"
                        ? "text-yellow-600 dark:text-yellow-400"
                        : "text-red-600 dark:text-red-400"
                  }`}
                >
                  {svc.status === "ok"
                    ? "OK"
                    : svc.status === "ratelimited"
                      ? "Rate limited"
                      : svc.status === "unconfigured"
                        ? "—"
                        : "Down"}
                </p>
                {svc.latency_ms !== undefined && (
                  <p className="text-xs text-muted-foreground">
                    {svc.latency_ms}ms
                  </p>
                )}
              </div>
            </div>
          ))}
        </div>
      )}

    </div>
  );
}
