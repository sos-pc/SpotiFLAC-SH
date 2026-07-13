import { useState, useEffect, useCallback } from "react";
import { Button } from "@/components/ui/button";
import { InputWithContext } from "@/components/ui/input-with-context";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Key, Trash2, RefreshCw, Copy, Check } from "lucide-react";
import { toastWithSound as toast } from "@/lib/toast-with-sound";
import {
  ListAPIKeys,
  CreateAPIKey,
  DeleteAPIKey,
  type APIKeyMeta,
  type CreatedAPIKey,
} from "@/lib/rpc";

// ApiKeysTab — create/list/revoke personal API keys. The "Admin" permission
// checkbox is only offered to admins, so isAdmin is passed in from the parent
// (which also uses it to gate the Maintenance tab). Loads the key list on
// mount (tab became active). The created-key dialog is rendered alongside the
// tab content — a Radix Dialog portals to the body, so its position in the
// tree doesn't matter.
export function ApiKeysTab({ isAdmin }: { isAdmin: boolean }) {
  const [apiKeys, setApiKeys] = useState<APIKeyMeta[]>([]);
  const [newKeyName, setNewKeyName] = useState("");
  const [newKeyPerms, setNewKeyPerms] = useState<string[]>(["read", "manage"]);
  const [createdKey, setCreatedKey] = useState<CreatedAPIKey | null>(null);
  const [copiedKey, setCopiedKey] = useState(false);
  const [keysLoading, setKeysLoading] = useState(false);

  const loadApiKeys = useCallback(async () => {
    setKeysLoading(true);
    try {
      setApiKeys(await ListAPIKeys());
    } catch {
      /* ignore */
    } finally {
      setKeysLoading(false);
    }
  }, []);

  useEffect(() => {
    // Loading the key list on mount (tab became active) is external-system
    // sync, not a derived render value.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    loadApiKeys();
  }, [loadApiKeys]);

  const toggleNewKeyPerm = (perm: string) => {
    setNewKeyPerms((prev) =>
      prev.includes(perm) ? prev.filter((p) => p !== perm) : [...prev, perm],
    );
  };

  const handleCreateKey = async () => {
    if (!newKeyName.trim() || newKeyPerms.length === 0) return;
    try {
      const result = await CreateAPIKey(newKeyName.trim(), newKeyPerms);
      setCreatedKey(result);
      setNewKeyName("");
      setNewKeyPerms(["read", "manage"]);
      loadApiKeys();
    } catch (err) {
      toast.error("Failed to create key", {
        description: err instanceof Error ? err.message : "Unknown error",
      });
    }
  };

  const handleRevokeKey = async (id: string) => {
    try {
      await DeleteAPIKey(id);
      setApiKeys((prev) => prev.filter((k) => k.id !== id));
    } catch (err) {
      toast.error("Failed to revoke key", {
        description: err instanceof Error ? err.message : "Unknown error",
      });
    }
  };

  const handleCopyKey = () => {
    if (!createdKey) return;
    navigator.clipboard.writeText(createdKey.key);
    setCopiedKey(true);
    setTimeout(() => setCopiedKey(false), 2000);
  };

  return (
    <>
      <div className="space-y-6 max-w-2xl">
        <div>
          <h2 className="text-base font-semibold mb-1">Personal API Keys</h2>
          <p className="text-sm text-muted-foreground mb-4">
            Create API keys to use SpotiFLAC from external applications. Pass
            the key as the
            <code className="mx-1 px-1 rounded bg-muted font-mono text-xs">
              X-API-Key
            </code>{" "}
            header.
          </p>
          <div className="flex gap-2">
            <InputWithContext
              value={newKeyName}
              onChange={(e) => setNewKeyName(e.target.value)}
              placeholder="Key name (e.g. my-app)"
              className="flex-1"
              onKeyDown={(e) => e.key === "Enter" && handleCreateKey()}
            />
            <Button
              onClick={handleCreateKey}
              disabled={!newKeyName.trim() || newKeyPerms.length === 0}
              className="gap-1.5"
            >
              <Key className="h-4 w-4" />
              Create Key
            </Button>
          </div>
          <div className="flex items-center gap-4 mt-3">
            <label className="flex items-center gap-2 text-sm cursor-pointer">
              <Checkbox
                checked={newKeyPerms.includes("read")}
                onCheckedChange={() => toggleNewKeyPerm("read")}
              />
              Read
            </label>
            <label className="flex items-center gap-2 text-sm cursor-pointer">
              <Checkbox
                checked={newKeyPerms.includes("manage")}
                onCheckedChange={() => toggleNewKeyPerm("manage")}
              />
              Manage
            </label>
            {isAdmin && (
              <label className="flex items-center gap-2 text-sm cursor-pointer">
                <Checkbox
                  checked={newKeyPerms.includes("admin")}
                  onCheckedChange={() => toggleNewKeyPerm("admin")}
                />
                Admin
              </label>
            )}
          </div>
        </div>

        {keysLoading ? (
          <div className="text-sm text-muted-foreground">Loading...</div>
        ) : apiKeys.length === 0 ? (
          <div className="text-sm text-muted-foreground border rounded-lg p-4 text-center">
            No API keys yet.
          </div>
        ) : (
          <div className="space-y-2">
            {apiKeys.map((key) => (
              <div
                key={key.id}
                className="flex items-center justify-between border rounded-lg px-4 py-3 bg-muted/20"
              >
                <div className="space-y-0.5">
                  <p className="text-sm font-medium">{key.name}</p>
                  <p className="text-xs text-muted-foreground font-mono">
                    ···{key.id.slice(-8)} &nbsp;·&nbsp; Created{" "}
                    {new Date(key.created_at).toLocaleDateString()} &nbsp;·&nbsp;
                    {key.permissions.join(", ")}
                  </p>
                </div>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => handleRevokeKey(key.id)}
                  className="text-destructive hover:text-destructive gap-1"
                >
                  <Trash2 className="h-4 w-4" />
                  Revoke
                </Button>
              </div>
            ))}
          </div>
        )}
        <Button
          variant="outline"
          size="sm"
          onClick={loadApiKeys}
          disabled={keysLoading}
          className="gap-1.5"
        >
          <RefreshCw className="h-3.5 w-3.5" />
          Refresh
        </Button>
      </div>

      <Dialog open={!!createdKey} onOpenChange={() => setCreatedKey(null)}>
        <DialogContent className="max-w-md [&>button]:hidden">
          <DialogHeader>
            <DialogTitle>API Key Created</DialogTitle>
            <DialogDescription>
              Copy this key now — it will not be shown again.
            </DialogDescription>
          </DialogHeader>
          <div className="flex items-center gap-2 rounded-lg border bg-muted p-3">
            <code className="flex-1 text-xs font-mono break-all">
              {createdKey?.key}
            </code>
            <Button
              variant="ghost"
              size="sm"
              onClick={handleCopyKey}
              className="shrink-0"
            >
              {copiedKey ? (
                <Check className="h-4 w-4 text-green-500" />
              ) : (
                <Copy className="h-4 w-4" />
              )}
            </Button>
          </div>
          <DialogFooter>
            <Button onClick={() => setCreatedKey(null)}>Done</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
