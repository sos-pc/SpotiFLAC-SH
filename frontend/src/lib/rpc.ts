import type {
  SpotifyMetadataResponse,
  DownloadRequest,
  DownloadResponse,
  LyricsDownloadRequest,
  LyricsDownloadResponse,
  CoverDownloadRequest,
  CoverDownloadResponse,
  HeaderDownloadRequest,
  HeaderDownloadResponse,
  GalleryImageDownloadRequest,
  GalleryImageDownloadResponse,
  AvatarDownloadRequest,
  AvatarDownloadResponse,
  SpotifySearchResults,
  SpotifySearchTrack,
  SpotifySearchAlbum,
  SpotifySearchArtist,
  SpotifySearchPlaylist,
  AudioMetadata,
  FileEntry,
  RenameResult,
  FileExistsCheck,
  FileExistsResult,
  ConvertAudioRequest,
  ConvertAudioResult,
  SystemInfo,
  DownloadHistoryItem,
  FetchHistoryItem,
  WatchlistStats,
  WatchlistHistoryItem,
  WatchedPlaylist,
  EnqueueBatchResponse,
} from "@/types/api";
import type { Settings } from "@/lib/settings";

// rpc.ts — client HTTP REST /api/v1/*

// ─── Helpers ──────────────────────────────────────────────────────────────────

function authHeaders(): Record<string, string> {
  const token = localStorage.getItem("spotiflac_token");
  return token ? { Authorization: `Bearer ${token}` } : {};
}

async function rest<T>(
  method: string,
  path: string,
  body?: unknown,
): Promise<T> {
  const hasBody = body !== undefined;
  const res = await fetch(`/api/v1${path}`, {
    method,
    headers: {
      ...authHeaders(),
      ...(hasBody ? { "Content-Type": "application/json" } : {}),
    },
    ...(hasBody ? { body: JSON.stringify(body) } : {}),
  });
  if (res.status === 401) {
    window.dispatchEvent(new CustomEvent("auth:expired"));
    throw new Error("Session expired");
  }
  if (res.status === 204) return undefined as T;
  if (!res.ok) {
    let msg = `HTTP ${res.status}`;
    try {
      const j = await res.json();
      msg = j.error || msg;
    } catch {
      /* ignore */
    }
    throw new Error(msg);
  }
  return res.json();
}

// /api/upload lives outside /api/v1 (see server.go's registerRoutes), so it
// can't go through rest() — but it sits behind the same RequireAuth and needs
// the same Authorization header. Both callers used to fetch() it bare, with no
// header at all, which meant a permanent 401 for anyone not covered by the LAN
// bypass — i.e. every request arriving through a reverse proxy, since
// X-Forwarded-For disables that bypass.
//
// Deliberately no Content-Type: the browser must set multipart/form-data itself
// to add the boundary. Only the Authorization header is ours to add.
export async function UploadFile(file: File): Promise<{ path?: string }> {
  const formData = new FormData();
  formData.append("file", file);
  const res = await fetch("/api/upload", {
    method: "POST",
    headers: authHeaders(),
    body: formData,
  });
  // Mirrors rest()'s handling: a 401 body is valid JSON, so callers doing
  // res.json() saw `path: undefined` and silently did nothing rather than
  // surfacing an expired session.
  if (res.status === 401) {
    window.dispatchEvent(new CustomEvent("auth:expired"));
    throw new Error("Session expired");
  }
  if (!res.ok) {
    let msg = `HTTP ${res.status}`;
    try {
      const j = await res.json();
      msg = j.error || msg;
    } catch {
      /* ignore */
    }
    throw new Error(msg);
  }
  return res.json();
}

// ─── Spotify ──────────────────────────────────────────────────────────────────

export const GetSpotifyMetadata = (req: {
  url: string;
  batch?: boolean;
}): Promise<SpotifyMetadataResponse> => {
  const url = req.url ?? "";
  const batch = req.batch !== false;
  return rest<SpotifyMetadataResponse>(
    "GET",
    `/search?url=${encodeURIComponent(url)}&batch=${batch}`,
  );
};

export const SearchSpotify = (req: {
  query: string;
  limit?: number;
}): Promise<SpotifySearchResults> =>
  rest<SpotifySearchResults>(
    "GET",
    `/search/query?q=${encodeURIComponent(req.query || "")}&limit=${req.limit || 10}`,
  );

export const SearchSpotifyByType = (req: {
  query: string;
  search_type: string;
  limit?: number;
  offset?: number;
}) =>
  rest<
    SpotifySearchTrack[] | SpotifySearchAlbum[] | SpotifySearchArtist[] | SpotifySearchPlaylist[]
  >(
    "GET",
    `/search/query?q=${encodeURIComponent(req.query || "")}&type=${encodeURIComponent(req.search_type || "")}&limit=${req.limit || 10}&offset=${req.offset || 0}`,
  );

export const GetPreviewURL = (id: string): Promise<string> =>
  rest<{ url: string }>(
    "GET",
    `/tracks/${encodeURIComponent(id)}/preview`,
  ).then((r) => r.url);

// ─── Download ─────────────────────────────────────────────────────────────────

export const DownloadTrack = (req: DownloadRequest) =>
  rest<DownloadResponse>("POST", "/downloads/track", req);
export const DownloadLyrics = (req: LyricsDownloadRequest) =>
  rest<LyricsDownloadResponse>("POST", "/media/lyrics", req);
export const DownloadCover = (req: CoverDownloadRequest) =>
  rest<CoverDownloadResponse>("POST", "/media/cover", req);
export const DownloadHeader = (req: HeaderDownloadRequest) =>
  rest<HeaderDownloadResponse>("POST", "/media/header", req);
export const DownloadGalleryImage = (req: GalleryImageDownloadRequest) =>
  rest<GalleryImageDownloadResponse>("POST", "/media/gallery", req);
export const DownloadAvatar = (req: AvatarDownloadRequest) =>
  rest<AvatarDownloadResponse>("POST", "/media/avatar", req);
// m3u8_name / m3u8_source_id ask the SERVER to write the playlist's M3U8 once
// every job in the batch has finished (docs/settings-source-of-truth.md D5).
// The client used to write it itself right after enqueue, from the existence
// check alone, so it only ever listed tracks that were already on disk.
export const EnqueueBatch = (req: {
  tracks: unknown[];
  settings: unknown;
  m3u8_name?: string;
  m3u8_source_id?: string;
}) => rest<EnqueueBatchResponse>("POST", "/jobs", req);

// ─── Queue / Progress ─────────────────────────────────────────────────────────

export const ClearCompletedDownloads = () =>
  rest<void>("DELETE", "/jobs/completed");
export const ClearAllDownloads = () => rest<void>("DELETE", "/jobs");

// ─── History ──────────────────────────────────────────────────────────────────

export const GetDownloadHistory = () =>
  rest<DownloadHistoryItem[]>("GET", "/history/downloads");
export const ClearDownloadHistory = () =>
  rest<void>("DELETE", "/history/downloads");
export const DeleteDownloadHistoryItem = (id: string) =>
  rest<void>("DELETE", `/history/downloads/${encodeURIComponent(id)}`);
export const GetFetchHistory = () => rest<FetchHistoryItem[]>("GET", "/history/fetch");
export const AddFetchHistory = (item: FetchHistoryItem) =>
  rest<void>("POST", "/history/fetch", item);
export const ClearFetchHistory = () => rest<void>("DELETE", "/history/fetch");
export const ClearFetchHistoryByType = (itemType: string) =>
  rest<void>("DELETE", `/history/fetch?type=${encodeURIComponent(itemType)}`);
export const DeleteFetchHistoryItem = (id: string) =>
  rest<void>("DELETE", `/history/fetch/${encodeURIComponent(id)}`);
// Answers with a CSV to save or a message to show, never both. It used to
// answer with one string and let the caller tell them apart by an "EXPORT:"
// prefix.
export const ExportFailedDownloads = () =>
  rest<{ csv?: string; message?: string }>("GET", "/history/downloads/export");

// ─── Settings ─────────────────────────────────────────────────────────────────

// The backend answers with the resolved values plus who may write what:
// instance-scoped keys (download path, templates, the Jellyfin path) belong
// to the deployment and only an admin may change them.
export interface SettingsEnvelope {
  values: Settings;
  writableScope: "all" | "user";
  instanceKeys: string[];
}
// The values a new account starts from, and the one action that changes them.
// Separate from the settings endpoints because PUT /settings always routes a
// personal key to the caller's own profile — which is why an operator had no
// way to change a house default once the migration had seeded it.
export const LoadHouseDefaults = () =>
  rest<{ values: Record<string, unknown> }>("GET", "/settings/defaults").then(
    (r) => r.values,
  );
export const PublishHouseDefaults = () =>
  rest<{ updated: number }>("POST", "/settings/defaults").then((r) => r.updated);
export const LoadSettingsEnvelope = () =>
  rest<SettingsEnvelope>("GET", "/settings");
export const LoadSettings = () =>
  LoadSettingsEnvelope().then((e) => e.values);
export const SaveSettings = (settings: Settings) =>
  rest<void>("PUT", "/settings", settings);
// `os` is the server's runtime.GOOS ("linux"/"windows"/"darwin") — used to
// build download output paths for the server's filesystem, not the browser's.
export const GetDefaults = () =>
  rest<{ downloadPath?: string; os?: string }>("GET", "/system/defaults");
export const GetOSInfo = () =>
  rest<SystemInfo>("GET", "/system/info").then((r) => r.os);
export const GetUserHomeDir = () =>
  rest<SystemInfo>("GET", "/system/info").then((r) => r.home_dir);

// ─── Audio / File ─────────────────────────────────────────────────────────────

export const ConvertAudio = (req: ConvertAudioRequest) =>
  rest<ConvertAudioResult[]>("POST", "/audio/convert", req);
export const AnalyzeTrack = (filePath: string) =>
  rest<string>("POST", "/audio/analyze", { file_path: filePath });
export const AnalyzeMultipleTracks = (filePaths: string[]) =>
  rest<string>("POST", "/audio/analyze/batch", { file_paths: filePaths });
export const GetFileSizes = (filePaths: string[]) =>
  rest<Record<string, number>>("POST", "/files/sizes", { file_paths: filePaths });
export const ListDirectoryFiles = (dirPath: string) =>
  rest<FileEntry[]>("GET", `/files?path=${encodeURIComponent(dirPath)}`);
export const ListAudioFilesInDir = (dirPath: string) =>
  rest<FileEntry[]>("GET", `/files/audio?path=${encodeURIComponent(dirPath)}`);
export const ReadFileMetadata = (filePath: string) =>
  rest<AudioMetadata>("GET", `/files/metadata?path=${encodeURIComponent(filePath)}`);
export const ReadImageAsBase64 = (filePath: string) =>
  rest<{ data: string }>(
    "GET",
    `/files/image?path=${encodeURIComponent(filePath)}`,
  ).then((r) => r.data);
export const ReadTextFile = (filePath: string) =>
  rest<{ content: string }>("POST", "/files/read", {
    file_path: filePath,
  }).then((r) => r.content);
export const RenameFileTo = (oldPath: string, newName: string) =>
  rest<void>("POST", "/files/rename", { old_path: oldPath, new_name: newName });
export const RenameFilesByMetadata = (files: string[], format: string) =>
  rest<RenameResult[]>("POST", "/files/rename/batch", { files, format });
export const PreviewRenameFiles = (files: string[], format: string) =>
  rest<RenameResult[]>("POST", "/files/rename/preview", { files, format });
export const UploadImageBytes = (filename: string, base64Data: string) =>
  rest<{ url: string }>("POST", "/files/upload/image", {
    filename,
    base64_data: base64Data,
  }).then((r) => r.url);
// No directory arguments: the server derives where a download lands from the
// user's saved settings and ignores anything the client would send
// (docs/settings-source-of-truth.md). Every caller was already passing "".
export const CheckFilesExistence = (tracks: FileExistsCheck[]) =>
  rest<FileExistsResult[]>("POST", "/files/exists", { tracks });

// ─── Watchlist ────────────────────────────────────────────────────────────────

export const AddToWatchlist = (req: {
  spotify_url: string;
  interval_hours: number;
  sync_deletions: boolean;
  settings: Partial<Settings>;
}) => rest<WatchedPlaylist & { message?: string }>("POST", "/watchlists", req);
// One row of the playlist picker, whatever source produced it.
export interface PickerPlaylist {
  uri: string;
  id: string;
  name: string;
  image_url?: string;
  owner_name?: string;
  owner_uri?: string;
  owned: boolean;
}
export interface SpotifyProfile {
  id: string;
  display_name: string;
  image_url?: string;
}
export const SearchSpotifyProfiles = (q: string, limit = 10) =>
  rest<{ profiles: SpotifyProfile[] }>(
    "GET",
    `/spotify/profiles?q=${encodeURIComponent(q)}&limit=${limit}`,
  ).then((r) => r.profiles ?? []);
export const GetProfilePlaylists = (profileID: string) =>
  rest<{ playlists: PickerPlaylist[] }>(
    "GET",
    `/spotify/profiles/${encodeURIComponent(profileID)}/playlists`,
  ).then((r) => r.playlists ?? []);

export interface BatchAddOutcome {
  spotify_url: string;
  id?: string;
  name?: string;
  status: "added" | "already_watched" | "failed";
  error?: string;
}
export interface BatchAddResult {
  added: number;
  already_watched: number;
  failed: number;
  outcomes: BatchAddOutcome[];
}
// Always answers 200 with one outcome per playlist: a bulk add is partially
// successful by nature, and the caller has to be able to say WHICH failed.
export const AddWatchlistsBatch = (req: {
  spotify_urls: string[];
  interval_hours: number;
  sync_deletions: boolean;
}) => rest<BatchAddResult>("POST", "/watchlists/batch", req);

export const RemoveFromWatchlist = (id: string) =>
  rest<void>("DELETE", `/watchlists/${encodeURIComponent(id)}`);
export const GetWatchlists = () => rest<WatchedPlaylist[]>("GET", "/watchlists");
// custom_name is optional and tri-state on the server: omitted leaves the name
// alone, "" clears it back to Spotify's, a value sets it. A 409 means another
// watchlist here already writes to that filename.
export const UpdateWatchlist = (req: {
  id: string;
  interval_hours: number;
  sync_deletions: boolean;
  custom_name?: string;
}) => rest<void>("PUT", `/watchlists/${encodeURIComponent(req.id)}`, req);
export const GetWatchlistStats = (id: string) =>
  rest<WatchlistStats>("GET", `/watchlists/${encodeURIComponent(id)}/stats`);
export const GetWatchlistHistory = (id: string) =>
  rest<WatchlistHistoryItem[]>("GET", `/watchlists/${encodeURIComponent(id)}/history`);
export const SyncWatchlist = (id: string) =>
  rest<void>("POST", `/watchlists/${encodeURIComponent(id)}/sync`);

export interface WatchlistRepairResult {
  retag: { scanned: number; tagged: number; skipped: number; failed: number };
  rebuild: {
    files_scanned: number;
    imported: number;
    verified: number;
    moved: number;
    duplicate: number;
    no_tag: number;
    failed: number;
    timed_out?: boolean;
  };
  m3u8: {
    written: boolean;
    skipped: boolean;
    total: number;
    resolved: number;
    unresolved: number;
  };
  m3u8_error?: string;
}
export const RepairWatchlist = (id: string) =>
  rest<void>("POST", `/watchlists/${encodeURIComponent(id)}/repair`);

export interface WatchlistFreshnessReport {
  up_to_date: boolean;
  total_tracks: number;
  new_on_spotify: number;
  removed_from_spotify: number;
  missing_files: number;
  pending: number;
  failed: number;
  m3u8_enabled: boolean;
  m3u8_entry_count?: number;
  m3u8_stale?: boolean;
  checked_at: string;
}
export const CheckWatchlistFreshness = (id: string) =>
  rest<WatchlistFreshnessReport>(
    "GET",
    `/watchlists/${encodeURIComponent(id)}/freshness`,
  );

// ─── Admin — backend log buffer (Debug Logs page) ──────────────────────────────

export interface ServerLogEntry {
  time: string;
  level: string;
  message: string;
}
export const GetServerLogs = () => rest<ServerLogEntry[]>("GET", "/admin/logs");

// ─── Admin — library maintenance ───────────────────────────────────────────────

export interface LibraryRebuildResult {
  scan_roots: string[];
  files_scanned: number;
  imported: number;
  verified: number;
  moved: number;
  duplicate: number;
  no_tag: number;
  failed: number;
  no_tag_sample?: string[];
  timed_out?: boolean;
}
// Fire-and-forget: the backend runs the scan in the background and reports
// completion via the "library_rebuild_done" SSE event (see jobsStream.ts) —
// a synchronous response would outlive a reverse-proxy's read timeout on a
// large library.
export const LibraryRebuild = () =>
  rest<void>("POST", "/admin/library-rebuild");

export interface RetagLegacyResult {
  scanned: number;
  tagged: number;
  skipped: number;
  failed: number;
  failed_ids?: string[];
}
// Stamps SPOTIFY_ID onto library files that do not carry it, reading the
// identity from the job record that downloaded them.
//
// Synchronous, unlike its neighbours: it walks BoltDB jobs and writes a tag
// block per file, with no external lookup, so it finishes in seconds rather
// than outliving a proxy timeout.
//
// This is what makes an unidentified file visible to LibraryRebuild, which
// keys on that tag — and therefore what has to run BEFORE it. Nothing else
// can establish the identity: the tag is absent and the catalog has no row,
// so the job record is the only place the path→track mapping survives.
export const RetagLegacy = () =>
  rest<RetagLegacyResult>("POST", "/admin/retag-legacy");

export interface RetagIncompleteMetadataResult {
  scanned: number;
  filled: number;
  skipped: number;
  failed: number;
  failed_ids?: string[];
}
// Fire-and-forget — see LibraryRebuild; result arrives via the
// "retag_incomplete_metadata_done" SSE event.
export const RetagIncompleteMetadata = () =>
  rest<void>("POST", "/admin/retag-incomplete-metadata");

// ─── API Keys ─────────────────────────────────────────────────────────────────

export interface APIKeyMeta {
  id: string;
  name: string;
  permissions: string[];
  created_at: string;
  last_used_at?: string;
}

export interface CreatedAPIKey extends APIKeyMeta {
  key: string; // clé brute, affichée une seule fois
}

export const ListAPIKeys = () => rest<APIKeyMeta[]>("GET", "/auth/keys");
export const CreateAPIKey = (name: string, permissions: string[]) =>
  rest<CreatedAPIKey>("POST", "/auth/keys", { name, permissions });
export const DeleteAPIKey = (id: string) =>
  rest<void>("DELETE", `/auth/keys/${encodeURIComponent(id)}`);

// ─── Tidal Auth ───────────────────────────────────────────────────────────────

export interface TidalStatus {
  connected: boolean;
  expires_at?: number; // unix timestamp
  username?: string;
}

export const GetTidalAuthURL = () =>
  rest<{ url: string }>("GET", "/auth/tidal/url").then((r) => r.url);
export const SubmitTidalCallback = (callbackURL: string) =>
  rest<void>("POST", "/auth/tidal/callback", { callback_url: callbackURL });
export const GetTidalStatus = () =>
  rest<TidalStatus>("GET", "/auth/tidal/status");
export const DisconnectTidal = () => rest<void>("DELETE", "/auth/tidal");

export interface TidalDeviceAuth {
  device_code: string;
  user_code: string;
  verification_uri: string;
  verification_uri_complete: string;
  expires_in: number;
  interval: number;
}

export interface TidalDevicePollResult {
  status: "pending" | "authorized" | "expired" | "denied" | "error";
  error?: string;
}

export const StartTidalDeviceAuth = () =>
  rest<TidalDeviceAuth>("POST", "/auth/tidal/device/start", {});
export const PollTidalDeviceAuth = (deviceCode: string) =>
  rest<TidalDevicePollResult>("POST", "/auth/tidal/device/poll", {
    device_code: deviceCode,
  });

// ─── API Library ──────────────────────────────────────────────────────────────

export interface ServiceStatus {
  name: string;
  url: string;
  status: "ok" | "down" | "ratelimited" | "unconfigured";
  latency_ms?: number;
  checked_at: number;
  error?: string;
}

export const GetAPIStatuses = () =>
  rest<ServiceStatus[]>("GET", "/apis/status");

// ProxyConfig, GetAPIProxies and UpdateAPIProxies were removed on 2026-07-28
// with the Tidal community proxy list they configured: every host on it serves
// 30-second previews without a personal token, which the download path refuses.
// Both endpoints are gone server-side too.
