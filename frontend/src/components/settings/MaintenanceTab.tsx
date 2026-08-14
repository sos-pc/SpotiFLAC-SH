import { useState, useCallback } from "react";
import { Button } from "@/components/ui/button";
import { RefreshCw, Users } from "lucide-react";
import { useJobsStreamEvent } from "@/hooks/useJobsStreamEvent";
import { toastWithSound as toast } from "@/lib/toast-with-sound";
import {
  LibraryRebuild,
  PublishHouseDefaults,
  RetagIncompleteMetadata,
  type LibraryRebuildResult,
  type RetagIncompleteMetadataResult,
} from "@/lib/rpc";

const StatRow = ({
  label,
  value,
  warn,
}: {
  label: string;
  value: number;
  warn?: boolean;
}) => (
  <div className="flex items-center justify-between gap-2">
    <span className="text-muted-foreground">{label}</span>
    <span
      className={`font-mono font-medium ${warn ? "text-destructive" : ""}`}
    >
      {value}
    </span>
  </div>
);

// MaintenanceTab — admin-only library maintenance actions.
//
// Both actions run in the background on the backend (202 + SSE, same pattern
// as watchlist repair) — a scan can take several minutes on a large library,
// which would otherwise outlive a reverse-proxy's read timeout and kill the
// request mid-scan (observed in production). The button shows a spinner until
// the matching SSE event arrives.
export function MaintenanceTab() {
  const [rebuildLoading, setRebuildLoading] = useState(false);
  const [rebuildResult, setRebuildResult] =
    useState<LibraryRebuildResult | null>(null);
  const [retagLoading, setRetagLoading] = useState(false);
  const [retagResult, setRetagResult] =
    useState<RetagIncompleteMetadataResult | null>(null);

  const runLibraryRebuild = useCallback(async () => {
    setRebuildLoading(true);
    try {
      await LibraryRebuild();
    } catch (err) {
      toast.error("Library rebuild failed to start", {
        description: err instanceof Error ? err.message : "Unknown error",
      });
      setRebuildLoading(false);
    }
  }, []);

  useJobsStreamEvent("library_rebuild_done", (e: MessageEvent) => {
    const result = JSON.parse(e.data) as LibraryRebuildResult;
    setRebuildLoading(false);
    setRebuildResult(result);
    if (result.timed_out) {
      toast.warning("Library rebuild timed out", {
        description:
          "Re-run to continue — already-scanned files are skipped fast.",
      });
    } else {
      toast.success("Library rebuild complete", {
        description: `${result.files_scanned} files scanned, ${result.imported} imported, ${result.failed} failed.`,
      });
    }
  });

  const runRetagIncompleteMetadata = useCallback(async () => {
    setRetagLoading(true);
    try {
      await RetagIncompleteMetadata();
    } catch (err) {
      toast.error("Retag failed to start", {
        description: err instanceof Error ? err.message : "Unknown error",
      });
      setRetagLoading(false);
    }
  }, []);

  useJobsStreamEvent("retag_incomplete_metadata_done", (e: MessageEvent) => {
    const result = JSON.parse(e.data) as RetagIncompleteMetadataResult;
    setRetagLoading(false);
    setRetagResult(result);
    toast.success("Retag complete", {
      description: `${result.scanned} tracks scanned, ${result.filled} filled, ${result.failed} failed.`,
    });
  });

  // Publishing the operator's own personal settings as what a new account
  // starts from. The migration seeded these once and then nothing could change
  // them: a PUT of a personal key goes to the caller's own profile by design,
  // so the instance store's copy stayed frozen at whatever it found.
  //
  // Not a second settings form. The gesture people actually have is "set the app
  // up the way I like it, then make that the starting point for everyone else",
  // and a form duplicating fifteen fields to allow a house default that differs
  // from your own preference answers a question nobody has asked yet.
  const [publishing, setPublishing] = useState(false);
  const publishDefaults = useCallback(async () => {
    setPublishing(true);
    try {
      const updated = await PublishHouseDefaults();
      toast.success(
        updated === 0
          ? "Defaults already match your settings"
          : `${updated} default${updated === 1 ? "" : "s"} updated for new accounts`,
      );
    } catch (e) {
      toast.error(`Could not publish defaults: ${e}`);
    } finally {
      setPublishing(false);
    }
  }, []);

  return (
    <div className="space-y-8 max-w-2xl">
      <div className="space-y-3">
        <div className="flex items-center justify-between gap-4">
          <div>
            <h2 className="text-base font-semibold mb-1">
              Defaults for new accounts
            </h2>
            <p className="text-sm text-muted-foreground">
              Copies your own quality, provider order and tagging preferences to
              what a new account starts from. It does not change anyone's saved
              settings — only the starting point for people who have not chosen
              otherwise. Folder and filename settings are not included: those
              apply to everyone already.
            </p>
          </div>
          <Button
            variant="outline"
            size="sm"
            onClick={publishDefaults}
            disabled={publishing}
            className="gap-1.5 shrink-0"
          >
            <Users className="h-3.5 w-3.5" />
            {publishing ? "Publishing..." : "Use mine"}
          </Button>
        </div>
      </div>

      <div className="space-y-3">
        <div className="flex items-center justify-between gap-4">
          <div>
            <h2 className="text-base font-semibold mb-1">
              Discover / Rebuild Library
            </h2>
            <p className="text-sm text-muted-foreground">
              Scans your configured music folder(s) and re-syncs the catalog
              from each file's embedded Spotify tag. Use this after
              moving/adding files outside of SpotiFLAC. Can take several minutes
              on a large library — leave this page open until it finishes.
            </p>
          </div>
          <Button
            variant="outline"
            size="sm"
            onClick={runLibraryRebuild}
            disabled={rebuildLoading}
            className="gap-1.5 shrink-0"
          >
            <RefreshCw
              className={`h-3.5 w-3.5 ${rebuildLoading ? "animate-spin" : ""}`}
            />
            {rebuildLoading ? "Scanning..." : "Run"}
          </Button>
        </div>

        {rebuildResult && (
          <div className="border rounded-lg p-3 bg-muted/10 space-y-2">
            {rebuildResult.timed_out && (
              <p className="text-xs text-yellow-600 dark:text-yellow-400">
                Timed out before finishing — re-run to continue.
              </p>
            )}
            <div className="grid grid-cols-3 gap-x-4 gap-y-1.5 text-sm">
              <StatRow label="Files scanned" value={rebuildResult.files_scanned} />
              <StatRow label="Imported" value={rebuildResult.imported} />
              <StatRow label="Verified" value={rebuildResult.verified} />
              <StatRow label="Moved" value={rebuildResult.moved} />
              <StatRow label="Duplicate" value={rebuildResult.duplicate} />
              <StatRow label="No tag" value={rebuildResult.no_tag} />
              <StatRow
                label="Failed"
                value={rebuildResult.failed}
                warn={rebuildResult.failed > 0}
              />
            </div>
            {rebuildResult.no_tag_sample && rebuildResult.no_tag_sample.length > 0 && (
              <details className="text-xs text-muted-foreground">
                <summary className="cursor-pointer">
                  {rebuildResult.no_tag_sample.length} file(s) without a Spotify tag (sample)
                </summary>
                <ul className="mt-1 space-y-0.5 font-mono">
                  {rebuildResult.no_tag_sample.map((p) => (
                    <li key={p} className="truncate">{p}</li>
                  ))}
                </ul>
              </details>
            )}
          </div>
        )}
      </div>

      <div className="space-y-3 border-t pt-6">
        <div className="flex items-center justify-between gap-4">
          <div>
            <h2 className="text-base font-semibold mb-1">
              Retag Incomplete Metadata
            </h2>
            <p className="text-sm text-muted-foreground">
              Re-fetches metadata (ISRC, genre, release date, etc.) for catalog
              tracks missing at least one field, and fills in only what's
              missing — existing tags and catalog values are never overwritten.
              One external lookup per track, throttled — can take a while on a
              library with many incomplete tracks.
            </p>
          </div>
          <Button
            variant="outline"
            size="sm"
            onClick={runRetagIncompleteMetadata}
            disabled={retagLoading}
            className="gap-1.5 shrink-0"
          >
            <RefreshCw
              className={`h-3.5 w-3.5 ${retagLoading ? "animate-spin" : ""}`}
            />
            {retagLoading ? "Retagging..." : "Run"}
          </Button>
        </div>

        {retagResult && (
          <div className="border rounded-lg p-3 bg-muted/10 space-y-2">
            <div className="grid grid-cols-3 gap-x-4 gap-y-1.5 text-sm">
              <StatRow label="Scanned" value={retagResult.scanned} />
              <StatRow label="Filled" value={retagResult.filled} />
              <StatRow label="Skipped" value={retagResult.skipped} />
              <StatRow
                label="Failed"
                value={retagResult.failed}
                warn={retagResult.failed > 0}
              />
            </div>
            {retagResult.failed_ids && retagResult.failed_ids.length > 0 && (
              <details className="text-xs text-muted-foreground">
                <summary className="cursor-pointer">
                  {retagResult.failed_ids.length} track(s) that failed
                </summary>
                <ul className="mt-1 space-y-0.5 font-mono">
                  {retagResult.failed_ids.map((id) => (
                    <li key={id} className="truncate">{id}</li>
                  ))}
                </ul>
              </details>
            )}
          </div>
        )}
      </div>
    </div>
  );
}
