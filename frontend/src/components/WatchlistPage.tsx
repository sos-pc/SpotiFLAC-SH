import { useState, useEffect } from "react";
import {
  AddToWatchlist,
  RemoveFromWatchlist,
  GetWatchlists,
  SyncWatchlist,
  UpdateWatchlist,
  GetWatchlistStats,
  GetWatchlistHistory,
} from "@/lib/rpc";
import { getSettings } from "@/lib/settings";
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
}

interface WatchedPlaylist {
  id: string;
  spotify_url: string;
  name: string;
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
  const [isAddModalOpen, setIsAddModalOpen] = useState(false);
  const [loading, setLoading] = useState(false);
  const [syncing, setSyncing] = useState<string | null>(null);
  const [stats, setStats] = useState<Record<string, WatchlistStats>>({});
  const [history, setHistory] = useState<Record<string, HistoryItem[]>>({});
  const [expandedHistory, setExpandedHistory] = useState<string | null>(null);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [editInterval, setEditInterval] = useState("12");
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
          } catch {}
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
    loadWatchlists();
    const interval = setInterval(loadWatchlists, 30000);
    return () => clearInterval(interval);
  }, []);

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
  };

  const handleEditSave = async () => {
    if (!editingId) return;
    try {
      await UpdateWatchlist({
        id: editingId,
        interval_hours: parseInt(editInterval, 10),
        sync_deletions: editSyncDeletions,
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
      } catch {}
    }
  };

  // Bouton unique Sync : nouveaux tracks Spotify + retry des failed
  const reloadStats = async (id: string) => {
    try {
      const s = await GetWatchlistStats(id);
      setStats((prev) => ({ ...prev, [id]: s }));
    } catch {}
  };

  const handleSync = async (id: string) => {
    setSyncing(id);
    try {
      await SyncWatchlist(id);
      toast.success("Sync triggered — new tracks + failed tracks requeued");
      setTimeout(() => reloadStats(id), 2000);
    } catch (err) {
      toast.error(`Sync failed: ${err}`);
    } finally {
      setSyncing(null);
    }
  };

  const handleSyncAll = async () => {
    setLoading(true);
    try {
      await Promise.all(watchlists.map((l) => SyncWatchlist(l.id)));
      toast.success(`Sync triggered for ${watchlists.length} playlist(s)`);
      setTimeout(loadWatchlists, 2000);
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
            <p className="text-sm mt-1">
              Add a Spotify playlist to start auto-syncing new tracks.
            </p>
          </div>
        ) : (
          watchlists.map((list) => {
            const { total, present, absent, pending, sizeMB } =
              getPlaylistStats(list);
            const displayName = isURL(list.name) ? "Loading..." : list.name;

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
                      disabled={syncing === list.id}
                      title="Sync: fetch new tracks + retry failed"
                    >
                      <RefreshCw
                        className={`h-4 w-4 ${syncing === list.id ? "animate-spin" : ""}`}
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
                          className="flex items-center gap-2 text-xs py-0.5 border-b last:border-0"
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
    </div>
  );
}
