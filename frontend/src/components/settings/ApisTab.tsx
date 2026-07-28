import { useState, useEffect, useCallback } from "react";
import { Button } from "@/components/ui/button";
import { InputWithContext } from "@/components/ui/input-with-context";
import { Label } from "@/components/ui/label";
import { Trash2, RefreshCw, Save } from "lucide-react";
import { toastWithSound as toast } from "@/lib/toast-with-sound";
import {
  GetAPIStatuses,
  GetAPIProxies,
  UpdateAPIProxies,
  type ServiceStatus,
  type ProxyConfig,
} from "@/lib/rpc";


// ApisTab — external service status board + per-service proxy configuration.
// Loads statuses and the proxy config on mount (the tab only renders while
// active), replacing the parent's activeTab-gated load effect.
export function ApisTab() {
  const [apiStatuses, setApiStatuses] = useState<ServiceStatus[] | null>(null);
  const [apisLoading, setApisLoading] = useState(false);
  const [proxies, setProxies] = useState<ProxyConfig | null>(null);
  const [proxySaving, setProxySaving] = useState(false);
  const [newProxyURL, setNewProxyURL] = useState("");

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

  const loadProxies = useCallback(async () => {
    try {
      setProxies(await GetAPIProxies());
    } catch {
      /* ignore */
    }
  }, []);

  useEffect(() => {
    // Loading API/proxy status on mount (tab became active) is
    // external-system sync, not a derived render value.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    loadApiStatuses();
    loadProxies();
  }, [loadApiStatuses, loadProxies]);

  const handleSaveProxies = async () => {
    if (!proxies) return;
    setProxySaving(true);
    try {
      await UpdateAPIProxies(proxies);
      toast.success("Proxy configuration saved");
    } catch (err) {
      toast.error("Failed to save", {
        description: err instanceof Error ? err.message : "Unknown error",
      });
    } finally {
      setProxySaving(false);
    }
  };

  const handleAddProxy = () => {
    const url = newProxyURL.trim();
    if (!url || !proxies) return;
    setProxies((prev) =>
      prev ? { ...prev, tidal_proxies: [...prev.tidal_proxies, url] } : prev,
    );
    setNewProxyURL("");
  };

  const handleRemoveProxy = (idx: number) => {
    setProxies((prev) =>
      prev
        ? {
            ...prev,
            tidal_proxies: prev.tidal_proxies.filter((_, i) => i !== idx),
          }
        : prev,
    );
  };

  return (
    <div className="space-y-6 max-w-2xl">
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

      {proxies && (
        <div className="space-y-5 border-t pt-4">
          <div>
            <h3 className="text-sm font-semibold mb-1">Proxy Configuration</h3>
            <p className="text-xs text-muted-foreground">
              Tidal proxies, tried in order with automatic fallback. The other
              providers are handled by the download engine and have no proxy
              list here.
            </p>
          </div>

          {/* Add proxy form — Tidal only: every other provider is served by the
              download engine, which carries its own routes. */}
          <div className="flex gap-2">
            <InputWithContext
              value={newProxyURL}
              onChange={(e) => setNewProxyURL(e.target.value)}
              placeholder="https://my-proxy.example.com"
              className="flex-1 font-mono text-xs"
              onKeyDown={(e) => e.key === "Enter" && handleAddProxy()}
            />
            <Button
              variant="outline"
              size="sm"
              onClick={handleAddProxy}
              disabled={!newProxyURL.trim()}
              className="shrink-0"
            >
              Add
            </Button>
          </div>

          {/* Tidal list */}
          <div className="space-y-1.5">
            <Label className="text-xs font-medium text-muted-foreground uppercase tracking-wide">
              Tidal ({proxies.tidal_proxies.length} configured)
            </Label>
            {proxies.tidal_proxies.map((p, i) => (
              <div key={i} className="flex items-center gap-2">
                <code className="flex-1 text-xs font-mono truncate border rounded px-2 py-1.5 bg-muted/20">
                  {p}
                </code>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => handleRemoveProxy(i)}
                  className="text-destructive hover:text-destructive px-2 shrink-0"
                >
                  <Trash2 className="h-3.5 w-3.5" />
                </Button>
              </div>
            ))}
          </div>

          <Button
            onClick={handleSaveProxies}
            disabled={proxySaving}
            className="gap-1.5"
          >
            <Save className="h-4 w-4" />
            {proxySaving ? "Saving..." : "Save Proxy Config"}
          </Button>
        </div>
      )}
    </div>
  );
}
