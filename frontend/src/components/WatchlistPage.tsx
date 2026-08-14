import { useState, useEffect } from "react";
import { PlaylistPicker } from "@/components/PlaylistPicker";
import {
  AddToWatchlist,
  RemoveFromWatchlist,
  GetWatchlists,
  SyncWatchlist,
  UpdateWatchlist,
  GetWatchlistStats,
  GetWatchlistHistory,
  RepairWatchlist,
  type WatchlistRepairResult,
  CheckWatchlistFreshness,
} from "@/lib/rpc";
import { getSettings } from "@/lib/settings";
import { useJobsStreamEvent } from "@/hooks/useJobsStreamEvent";
import { toastWithSound as toast } from "@/lib/toast-with-sound";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
  DialogDescription,
} from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Trash2,
  Plus,
  Clock,
  RefreshCw,
  Settings2,
  Eye,
  Pencil,
  ChevronDown,
  ChevronUp,
  CheckCircle2,
  XCircle,
  SkipForward,
  Wrench,
  ShieldCheck,
} from "lucide-react";

interface SyncLog {
  time: string;
  new_tracks: number;
  downloaded: number;
  skipped: number;
  failed: number;
  deleted: number;
}

interface WatchlistStats {
  watchlist_id: string;
  total_tracks: number;
  downloaded: number;
  skipped: number;
  failed: number;
  pending: number;
  total_size_mb: number;
}

interface HistoryItem {
  track_name: string;
  artist_name: string;
  album_name: string;
  status: string;
  total_size: number;
  updated_at: number;
  file_path: string;
  error: string;
  // Every row here is an attempt, while the card's summary partitions the
  // tracks currently in the playlist. These two say which rows still describe
  // the present, so "0 failed" above a list containing failures reads as two
  // answers to two questions rather than a contradiction.
  superseded?: boolean;
  still_tracked?: boolean;
}

interface WatchedPlaylist {
  id: string;
  spotify_url: string;
  name: string;
  // Set by the user; overrides `name` everywhere a human reads it. Empty or
  // absent means "whatever Spotify calls it".
  custom_name?: string;
  interval_hours: number;
  last_sync: string;
  track_ids: string[];
  created_at: string;
  sync_deletions: boolean;
  sync_logs?: SyncLog[];
}

// Retourne true si la "name" ressemble encore à une URL (cas juste après ajout)
function isURL(str: string): boolean {
  return (
    str.startsWith("http://") ||
    str.startsWith("https://") ||
    str.startsWith("spotify:")
  );
}

export function WatchlistPage() {
  const [watchlists, setWatchlists] = useState<WatchedPlaylist[]>([]);
  const [pickerOpen, setPickerOpen] = useState(false);
  const [isAddModalOpen, setIsAddModalOpen] = useState(false);
  const [loading, setLoading] = useState(false);
  const [syncing, setSyncing] = useState<Set<string>>(new Set());
  const [repairing, setRepairing] = useState<Set<string>>(new Set());
  const [checkingFreshness, setCheckingFreshness] = useState<Set<string>>(
    new Set(),
  );
  const [stats, setStats] = useState<Record<string, WatchlistStats>>({});
  const [history, setHistory] = useState<Record<string, HistoryItem[]>>({});
  const [expandedHistory, setExpandedHistory] = useState<string | null>(null);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [editInterval, setEditInterval] = useState("12");
  const [editName, setEditName] = useState("");
  const [editNamePlaceholder, setEditNamePlaceholder] = useState("");
  const [editSyncDeletions, setEditSyncDeletions] = useState(false);

  const [newUrl, setNewUrl] = useState("");
  const [newInterval, setNewInterval] = useState("12");
  const [newSyncDeletions, setNewSyncDeletions] = useState(false);

  const loadWatchlists = async () => {
    if (!localStorage.getItem("spotiflac_token")) return;
    setLoading(true);
    try {
      const lists = await GetWatchlists();
      setWatchlists(lists || []);
      const statsMap: Record<string, WatchlistStats> = {};
      await Promise.all(
        (lists || []).map(async (l) => {
          try {
            statsMap[l.id] = await GetWatchlistStats(l.id);
          } catch (err) {
            // One watchlist's stats failing shouldn't block the others —
            // it just won't have a stats entry in the UI.
            console.error(`Failed to load stats for watchlist ${l.id}:`, err);
          }
        }),
      );
      setStats(statsMap);
    } catch (err) {
      toast.error(`Failed to load watchlists: ${err}`);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    // Polling on an interval is exactly the kind of external-system sync
    // effects exist for.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    loadWatchlists();
    const interval = setInterval(loadWatchlists, 30000);
    return () => clearInterval(interval);
  }, []);

  const reloadStats = async (id: string) => {
    try {
      const s = await GetWatchlistStats(id);
      setStats((prev) => ({ ...prev, [id]: s }));
    } catch (err) {
      toast.error(`Failed to reload stats: ${err}`);
    }
  };

  // Écouter les événements SSE pour les syncs de watchlist (connexion
  // partagée — voir lib/jobsStream.ts)
  useJobsStreamEvent("watchlist_synced", (e: MessageEvent) => {
    const data = JSON.parse(e.data) as {
      watchlist_id: string;
      new_tracks: number;
      deleted: number;
      name: string;
    };
    // Retirer la playlist du set "en cours de sync"
    setSyncing((prev) => {
      const next = new Set(prev);
      next.delete(data.watchlist_id);
      return next;
    });
    // Recharger les stats + watchlists pour afficher le SyncLog à jour
    reloadStats(data.watchlist_id);
    loadWatchlists();
    // Toast de résultat
    if (data.new_tracks > 0 || data.deleted > 0) {
      const parts: string[] = [];
      if (data.new_tracks > 0) parts.push(`${data.new_tracks} new`);
      if (data.deleted > 0) parts.push(`${data.deleted} deleted`);
      toast.success(`${data.name}: ${parts.join(", ")}`);
    } else {
      toast.info(`${data.name}: up to date`);
    }
  });

  useJobsStreamEvent("watchlist_repaired", (e: MessageEvent) => {
    const data = JSON.parse(e.data) as {
      watchlist_id: string;
      name: string;
      result: WatchlistRepairResult;
    };
    setRepairing((prev) => {
      const next = new Set(prev);
      next.delete(data.watchlist_id);
      return next;
    });
    reloadStats(data.watchlist_id);
    loadWatchlists();
    const { retag, rebuild, m3u8, m3u8_error } = data.result;
    if (m3u8.unresolved === 0) {
      toast.success(
        `${data.name}: repaired, all ${m3u8.total} tracks resolved, M3U8 up to date`,
      );
    } else if (m3u8.resolved > 0) {
      toast.warning(
        `${data.name}: repaired, ${m3u8.resolved}/${m3u8.total} tracks resolved (${m3u8.unresolved} still missing — tagged ${retag.tagged}, imported ${rebuild.imported} into the catalog)`,
      );
    } else {
      toast.error(
        `${data.name}: repair found 0/${m3u8.total} tracks on disk — check the download path in this watchlist's settings`,
      );
    }
    if (m3u8_error) {
      toast.error(`${data.name}: M3U8 write failed: ${m3u8_error}`);
    }
  });

  const handleAdd = async () => {
    if (!newUrl.trim()) {
      toast.error("Please enter a Spotify URL");
      return;
    }
    if (!newUrl.includes("spotify.com")) {
      toast.error("Please enter a valid Spotify URL");
      return;
    }
    try {
      const settings = getSettings();
      const res = await AddToWatchlist({
        spotify_url: newUrl.trim(),
        interval_hours: parseInt(newInterval, 10),
        sync_deletions: newSyncDeletions,
        settings: {
          downloadPath: settings.downloadPath,
          downloader: settings.downloader,
          folderTemplate: settings.folderTemplate,
          filenameTemplate: settings.filenameTemplate,
          trackNumber: settings.trackNumber,
          embedLyrics: settings.embedLyrics,
          embedMaxQualityCover: settings.embedMaxQualityCover,
          tidalQuality: settings.tidalQuality,
          qobuzQuality: settings.qobuzQuality,
          amazonQuality: settings.amazonQuality,
          autoOrder: settings.autoOrder,
          autoQuality: settings.autoQuality,
          allowFallback: settings.allowFallback,
          createPlaylistFolder: settings.createPlaylistFolder,
          useFirstArtistOnly: settings.useFirstArtistOnly,
          useSingleGenre: settings.useSingleGenre,
          embedGenre: settings.embedGenre,
        },
      });
      toast.success(res?.message || `Watching '${res?.name}'`);
      setIsAddModalOpen(false);
      setNewUrl("");
      setNewInterval("12");
      setNewSyncDeletions(false);
      loadWatchlists();
    } catch (err) {
      toast.error(`Failed to add watchlist: ${err}`);
    }
  };

  const handleRemove = async (id: string) => {
    try {
      await RemoveFromWatchlist(id);
      toast.success("Removed from watchlist");
      loadWatchlists();
    } catch (err) {
      toast.error(`Failed to remove: ${err}`);
    }
  };

  const handleEdit = (list: WatchedPlaylist) => {
    setEditingId(list.id);
    setEditInterval(String(list.interval_hours));
    setEditSyncDeletions(list.sync_deletions);
    // Only the custom name goes in the field. Spotify's is the placeholder, so
    // an empty box reads as "use theirs" rather than as a name to be retyped.
    setEditName(list.custom_name || "");
    setEditNamePlaceholder(list.name);
  };

  const handleEditSave = async () => {
    if (!editingId) return;
    try {
      await UpdateWatchlist({
        id: editingId,
        interval_hours: parseInt(editInterval, 10),
        sync_deletions: editSyncDeletions,
        // Always sent: "" is how the server is told to go back to Spotify's name.
        custom_name: editName.trim(),
      });
      toast.success("Watchlist updated");
      setEditingId(null);
      loadWatchlists();
    } catch (err) {
      toast.error(`Failed to update: ${err}`);
    }
  };

  const toggleHistory = async (id: string) => {
    if (expandedHistory === id) {
      setExpandedHistory(null);
      return;
    }
    setExpandedHistory(id);
    if (!history[id]) {
      try {
        const items = await GetWatchlistHistory(id);
        setHistory((prev) => ({ ...prev, [id]: items || [] }));
      } catch (err) {
        toast.error(`Failed to load history: ${err}`);
      }
    }
  };

  const handleSync = async (id: string) => {
    setSyncing((prev) => new Set(prev).add(id));
    try {
      await SyncWatchlist(id);
      // Le spinner est retiré par l'événement SSE watchlist_synced
      // Le toast de résultat aussi — on ne montre rien ici
    } catch (err) {
      // En cas d'erreur HTTP, retirer du set immédiatement
      setSyncing((prev) => {
        const next = new Set(prev);
        next.delete(id);
        return next;
      });
      toast.error(`Sync failed: ${err}`);
    }
  };

  const handleRepair = async (id: string) => {
    setRepairing((prev) => new Set(prev).add(id));
    try {
      await RepairWatchlist(id);
      // Le spinner et le toast de résultat sont gérés par l'événement
      // SSE watchlist_repaired — la réparation tourne en arrière-plan
      // côté serveur et peut prendre plusieurs minutes.
    } catch (err) {
      // En cas d'erreur HTTP (échec du kickoff), retirer du set immédiatement
      setRepairing((prev) => {
        const next = new Set(prev);
        next.delete(id);
        return next;
      });
      toast.error(`Repair failed: ${err}`);
    }
  };

  const handleCheckFreshness = async (id: string, name: string) => {
    setCheckingFreshness((prev) => new Set(prev).add(id));
    try {
      const r = await CheckWatchlistFreshness(id);
      if (r.up_to_date) {
        toast.success(`${name}: up to date (${r.total_tracks} tracks)`);
      } else {
        const parts: string[] = [];
        if (r.new_on_spotify > 0)
          parts.push(`${r.new_on_spotify} new on Spotify`);
        if (r.removed_from_spotify > 0)
          parts.push(`${r.removed_from_spotify} removed from Spotify`);
        if (r.missing_files > 0)
          parts.push(`${r.missing_files} missing files`);
        if (r.pending > 0) parts.push(`${r.pending} pending`);
        if (r.failed > 0) parts.push(`${r.failed} failed`);
        if (r.m3u8_stale) parts.push("M3U8 out of date");
        toast.warning(`${name}: not up to date — ${parts.join(", ")}`);
      }
    } catch (err) {
      toast.error(`Freshness check failed: ${err}`);
    } finally {
      setCheckingFreshness((prev) => {
        const next = new Set(prev);
        next.delete(id);
        return next;
      });
    }
  };

  const handleSyncAll = async () => {
    setLoading(true);
    try {
      const results = await Promise.allSettled(
        watchlists.map((l) => SyncWatchlist(l.id)),
      );
      const failed = results.filter((r) => r.status === "rejected").length;
      if (failed === 0) {
        toast.success(`Sync triggered for ${watchlists.length} playlist(s)`);
      } else {
        toast.warning(
          `${watchlists.length - failed}/${watchlists.length} syncs triggered`,
        );
      }
    } catch (err) {
      toast.error(`Sync failed: ${err}`);
    } finally {
      setLoading(false);
    }
  };

  const formatLastSync = (lastSync: string) => {
    if (!lastSync || lastSync.startsWith("0001")) return "Never synced";
    return new Date(lastSync).toLocaleString();
  };

  const getPlaylistStats = (list: WatchedPlaylist) => {
    const s = stats[list.id];
    const total = s ? s.total_tracks : (list.track_ids?.length ?? 0);
    const present = s ? s.downloaded + s.skipped : 0;
    const absent = s ? s.failed : 0;
    const pending = s ? s.pending : 0;
    const sizeMB = s ? s.total_size_mb : 0;
    return { total, present, absent, pending, sizeMB };
  };

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold">Auto-Sync Playlists</h1>
        <div className="flex gap-2">
          <Button
            variant="outline"
            onClick={handleSyncAll}
            disabled={loading || watchlists.length === 0}
          >
            <RefreshCw
              className={`h-4 w-4 mr-2 ${loading ? "animate-spin" : ""}`}
            />
            Sync All
          </Button>
          <Button onClick={() => setIsAddModalOpen(true)}>
            <Plus className="h-4 w-4 mr-2" />
            Add Playlist
          </Button>
        </div>
      </div>

      {watchlists.length > 0 && (
        <div className="bg-primary/10 border border-primary/20 p-3 rounded-md flex items-center gap-3 text-sm text-primary">
          <Settings2 className="h-5 w-5 shrink-0" />
          <p>
            Auto-Sync uses your current <strong>Settings</strong> (destination
            folder, quality, lyrics) saved at the time you add the playlist.
          </p>
        </div>
      )}

      <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
        {watchlists.length === 0 ? (
          <div className="col-span-full py-12 text-center text-muted-foreground border-2 border-dashed rounded-lg">
            <Eye className="h-10 w-10 mx-auto mb-3 opacity-30" />
            <p>No playlists are being watched.</p>
            <p className="text-sm mt-1 mb-4">
              Watched playlists are re-checked regularly and new tracks are
              downloaded automatically.
            </p>
            {/* This state used to say what to do and offer no way to do it —
                no button, nothing — which is the first screen of every new
                account. */}
            <Button onClick={() => setPickerOpen(true)} className="gap-1.5">
              <Plus className="h-4 w-4" />
              Choose playlists
            </Button>
          </div>
        ) : (
          watchlists.map((list) => {
            const { total, present, absent, pending, sizeMB } =
              getPlaylistStats(list);
            const shownName = list.custom_name || list.name;
            const displayName = isURL(shownName) ? "Loading..." : shownName;

            return (
              <div
                key={list.id}
                className="p-4 border rounded-lg bg-card/50 flex flex-col gap-3"
              >
                <div className="flex justify-between items-start">
                  <div className="min-w-0 pr-2">
                    <h3
                      className={`font-bold truncate ${isURL(list.name) ? "text-muted-foreground italic text-sm" : ""}`}
                      title={list.name}
                    >
                      {displayName}
                    </h3>
                    <a
                      href={list.spotify_url}
                      target="_blank"
                      rel="noreferrer"
                      className="text-xs text-blue-500 hover:underline truncate block mt-1"
                    >
                      {list.spotify_url}
                    </a>
                  </div>
                  <div className="flex gap-1 shrink-0">
                    {/* Bouton Sync unique : nouveaux tracks + retry failed */}
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-8 w-8 text-muted-foreground hover:text-primary"
                      onClick={() => handleSync(list.id)}
                      disabled={syncing.has(list.id)}
                      title="Sync: fetch new tracks + retry failed"
                    >
                      <RefreshCw
                        className={`h-4 w-4 ${syncing.has(list.id) ? "animate-spin" : ""}`}
                      />
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-8 w-8 text-muted-foreground hover:text-amber-500"
                      onClick={() => handleRepair(list.id)}
                      disabled={repairing.has(list.id)}
                      title="Repair: retag legacy files, rebuild catalog, and force-regenerate the M3U8 for this playlist"
                    >
                      <Wrench
                        className={`h-4 w-4 ${repairing.has(list.id) ? "animate-pulse" : ""}`}
                      />
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-8 w-8 text-muted-foreground hover:text-emerald-500"
                      onClick={() => handleCheckFreshness(list.id, displayName)}
                      disabled={checkingFreshness.has(list.id)}
                      title="Check: is this playlist up to date with Spotify, fully downloaded, and is its M3U8 current?"
                    >
                      <ShieldCheck
                        className={`h-4 w-4 ${checkingFreshness.has(list.id) ? "animate-pulse" : ""}`}
                      />
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-8 w-8 text-muted-foreground hover:text-blue-500"
                      onClick={() => handleEdit(list)}
                      title="Edit watchlist settings"
                    >
                      <Pencil className="h-4 w-4" />
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-8 w-8 text-destructive hover:bg-destructive/10"
                      onClick={() => handleRemove(list.id)}
                    >
                      <Trash2 className="h-4 w-4" />
                    </Button>
                  </div>
                </div>

                <div className="text-sm text-muted-foreground bg-background p-3 rounded border space-y-1.5">
                  {/* Stats : total / téléchargés / manquants */}
                  <div className="flex items-center gap-3 text-xs pb-1.5 border-b">
                    <span className="text-foreground font-medium">
                      {total} tracks
                    </span>
                    <span className="text-green-500 flex items-center gap-1">
                      <CheckCircle2 className="h-3 w-3" />
                      {present}
                    </span>
                    {absent > 0 && (
                      <span className="text-red-500 flex items-center gap-1">
                        <XCircle className="h-3 w-3" />
                        {absent}
                      </span>
                    )}
                    {pending > 0 && (
                      <span className="text-blue-400 flex items-center gap-1">
                        <Clock className="h-3 w-3" />
                        {pending}
                      </span>
                    )}
                    {sizeMB > 0 && (
                      <span className="text-muted-foreground ml-auto">
                        {sizeMB.toFixed(1)} MB
                      </span>
                    )}
                  </div>

                  <div className="flex items-center gap-2">
                    <Clock className="h-3.5 w-3.5" />
                    <span>
                      Checks every <strong>{list.interval_hours}h</strong>
                    </span>
                    {list.sync_deletions && (
                      <span className="text-xs bg-red-500/10 text-red-500 px-1.5 py-0.5 rounded ml-2">
                        sync deletions
                      </span>
                    )}
                  </div>

                  {list.sync_logs && list.sync_logs.length > 0 && (
                    <div className="text-xs border-t pt-1.5 mt-1 space-y-0.5">
                      <span className="text-muted-foreground font-medium">
                        Recent syncs:
                      </span>
                      {[...list.sync_logs]
                        .reverse()
                        .slice(0, 5)
                        .map((log, i) => (
                          <div
                            key={i}
                            className="flex gap-2 text-xs text-muted-foreground flex-wrap"
                          >
                            <span className="shrink-0">
                              {new Date(log.time).toLocaleDateString()}
                            </span>
                            {log.new_tracks > 0 && (
                              <span className="text-blue-400">
                                ~{log.new_tracks} new
                              </span>
                            )}
                            {log.downloaded > 0 && (
                              <span className="text-green-500">
                                +{log.downloaded}
                              </span>
                            )}
                            {log.failed > 0 && (
                              <span className="text-red-500">
                                ⚠{log.failed} failed
                              </span>
                            )}
                            {log.skipped > 0 && (
                              <span className="text-yellow-500/70">
                                ={log.skipped} skipped
                              </span>
                            )}
                            {log.deleted > 0 && (
                              <span className="text-red-400">
                                -{log.deleted}
                              </span>
                            )}
                            {log.downloaded === 0 &&
                              log.failed === 0 &&
                              log.deleted === 0 &&
                              log.skipped === 0 && (
                                <span className="text-muted-foreground italic">
                                  no changes
                                </span>
                              )}
                          </div>
                        ))}
                    </div>
                  )}

                  <div className="text-xs border-t pt-1.5 mt-1">
                    Last sync: {formatLastSync(list.last_sync)}
                  </div>
                </div>

                <button
                  onClick={() => toggleHistory(list.id)}
                  className="text-xs text-muted-foreground hover:text-primary flex items-center gap-1 w-full pt-1 border-t"
                >
                  {expandedHistory === list.id ? (
                    <ChevronUp className="h-3 w-3" />
                  ) : (
                    <ChevronDown className="h-3 w-3" />
                  )}
                  History
                </button>
                {expandedHistory === list.id && (
                  <div className="max-h-48 overflow-y-auto space-y-1 border rounded p-2 bg-background">
                    {(history[list.id] || []).length === 0 ? (
                      <p className="text-xs text-muted-foreground text-center py-2">
                        No history
                      </p>
                    ) : (
                      (history[list.id] || []).map((item, i) => (
                        <div
                          key={i}
                          // Dimmed when the row no longer describes the
                          // playlist as it stands: a retry that a later attempt
                          // replaced, or a track that has since left. Those are
                          // exactly the rows that made the summary above look
                          // wrong — it counts current tracks, this counts
                          // attempts.
                          className={`flex items-center gap-2 text-xs py-0.5 border-b last:border-0 ${
                            item.superseded || item.still_tracked === false
                              ? "opacity-50"
                              : ""
                          }`}
                        >
                          {item.status === "done" && (
                            <CheckCircle2 className="h-3 w-3 text-green-500 shrink-0" />
                          )}
                          {item.status === "failed" && (
                            <XCircle className="h-3 w-3 text-red-500 shrink-0" />
                          )}
                          {item.status === "skipped" && (
                            <SkipForward className="h-3 w-3 text-yellow-500 shrink-0" />
                          )}
                          <div className="min-w-0 flex-1">
                            <span className="truncate block font-medium">
                              {item.track_name}
                              {item.superseded && (
                                <span
                                  className="ml-1 font-normal text-muted-foreground"
                                  title="A later attempt for this track replaced this one — the summary above counts the track once, by its current state."
                                >
                                  · retried since
                                </span>
                              )}
                              {item.still_tracked === false && (
                                <span
                                  className="ml-1 font-normal text-muted-foreground"
                                  title="This track has left the playlist. Its attempts stay in the log but nothing above counts it."
                                >
                                  · no longer in the playlist
                                </span>
                              )}
                            </span>
                            <span className="text-muted-foreground truncate block">
                              {item.artist_name}
                            </span>
                          </div>
                          {item.total_size > 0 && (
                            <span className="text-muted-foreground shrink-0">
                              {item.total_size.toFixed(1)}MB
                            </span>
                          )}
                          {item.status === "failed" && item.error && (
                            <span
                              className="text-red-400 truncate max-w-[120px]"
                              title={item.error}
                            >
                              {item.error}
                            </span>
                          )}
                        </div>
                      ))
                    )}
                  </div>
                )}
              </div>
            );
          })
        )}
      </div>

      {/* Modal edit */}
      <Dialog open={!!editingId} onOpenChange={(o) => !o && setEditingId(null)}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>Edit Watchlist</DialogTitle>
          </DialogHeader>
          <div className="space-y-4 py-2">
            <div className="space-y-2">
              <label className="text-sm font-medium">Playlist name</label>
              <input
                type="text"
                value={editName}
                onChange={(e) => setEditName(e.target.value)}
                placeholder={editNamePlaceholder}
                className="w-full rounded-md border bg-background px-3 py-2 text-sm"
              />
              <p className="text-xs text-muted-foreground">
                Leave empty to follow the name on Spotify. This is what the
                playlist file is called, so it is also what Jellyfin shows.
              </p>
            </div>
            <div className="space-y-2">
              <label className="text-sm font-medium">Check interval</label>
              <Select value={editInterval} onValueChange={setEditInterval}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="1">Every 1 hour</SelectItem>
                  <SelectItem value="6">Every 6 hours</SelectItem>
                  <SelectItem value="12">Every 12 hours</SelectItem>
                  <SelectItem value="24">Daily (24h)</SelectItem>
                  <SelectItem value="168">Weekly</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="border rounded-md p-3 bg-muted/30">
              <div className="flex items-center gap-2">
                <input
                  type="checkbox"
                  id="edit-sync-deletions"
                  checked={editSyncDeletions}
                  onChange={(e) => setEditSyncDeletions(e.target.checked)}
                  className="rounded"
                />
                <label
                  htmlFor="edit-sync-deletions"
                  className="text-sm cursor-pointer"
                >
                  Sync deletions{" "}
                  <span className="text-xs text-muted-foreground">
                    (delete file if removed from Spotify)
                  </span>
                </label>
              </div>
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setEditingId(null)}>
              Cancel
            </Button>
            <Button onClick={handleEditSave}>Save</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Modal add */}
      <Dialog open={isAddModalOpen} onOpenChange={setIsAddModalOpen}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>Watch a Spotify Playlist</DialogTitle>
            <DialogDescription>
              SpotiFLAC will periodically check this playlist for new tracks and
              download them automatically.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4 py-2">
            <div className="space-y-2">
              <label className="text-sm font-medium">Spotify URL</label>
              <input
                className="w-full px-3 py-2 text-sm border rounded-md bg-background focus:outline-none focus:ring-2 focus:ring-primary"
                placeholder="https://open.spotify.com/playlist/..."
                value={newUrl}
                onChange={(e) => setNewUrl(e.target.value)}
              />
            </div>
            <div className="space-y-2">
              <label className="text-sm font-medium">Check interval</label>
              <Select value={newInterval} onValueChange={setNewInterval}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="1">Every 1 hour</SelectItem>
                  <SelectItem value="6">Every 6 hours</SelectItem>
                  <SelectItem value="12">Every 12 hours</SelectItem>
                  <SelectItem value="24">Daily (24h)</SelectItem>
                  <SelectItem value="168">Weekly</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-2 border rounded-md p-3 bg-muted/30">
              <label className="text-sm font-medium">Options</label>
              <div className="flex items-center gap-2">
                <input
                  type="checkbox"
                  id="sync-deletions"
                  checked={newSyncDeletions}
                  onChange={(e) => setNewSyncDeletions(e.target.checked)}
                  className="rounded"
                />
                <label
                  htmlFor="sync-deletions"
                  className="text-sm cursor-pointer"
                >
                  Sync deletions{" "}
                  <span className="text-xs text-muted-foreground">
                    (delete file if removed from Spotify)
                  </span>
                </label>
              </div>
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setIsAddModalOpen(false)}>
              Cancel
            </Button>
            <Button onClick={handleAdd}>Start Watching</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      <PlaylistPicker
        isOpen={pickerOpen}
        onClose={() => setPickerOpen(false)}
        onAdded={loadWatchlists}
        watchedURLs={new Set(watchlists.map((l) => l.spotify_url))}
      />
    </div>
  );
}
