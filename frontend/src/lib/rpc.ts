// rpc.ts — client HTTP REST /api/v1/*

// ─── Helpers ──────────────────────────────────────────────────────────────────

function authHeaders(): Record<string, string> {
  const token = localStorage.getItem("spotiflac_token");
  return token ? { Authorization: `Bearer ${token}` } : {};
}

async function rest<T>(method: string, path: string, body?: unknown): Promise<T> {
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
    try { const j = await res.json(); msg = j.error || msg; } catch { /* ignore */ }
    throw new Error(msg);
  }
  return res.json();
}

// ─── Spotify ──────────────────────────────────────────────────────────────────

export const GetSpotifyMetadata = (req: any): Promise<any> => {
  const url = req.url ?? req.URL ?? "";
  const batch = req.batch !== false;
  return rest<any>("GET", `/search?url=${encodeURIComponent(url)}&batch=${batch}`);
};

export const GetStreamingURLs = (id: string, region: string): Promise<any> =>
  rest<{ urls: any }>("GET", `/tracks/${encodeURIComponent(id)}/links?region=${encodeURIComponent(region)}`)
    .then(r => r.urls);

export const CheckTrackAvailability = (id: string): Promise<any> =>
  rest<any>("GET", `/tracks/${encodeURIComponent(id)}/availability`);

export const SearchSpotify = (req: any): Promise<any> =>
  rest<any>("GET", `/search/query?q=${encodeURIComponent(req.query || "")}&limit=${req.limit || 10}`);

export const SearchSpotifyByType = (req: any): Promise<any> =>
  rest<any>(
    "GET",
    `/search/query?q=${encodeURIComponent(req.query || "")}&type=${encodeURIComponent(req.search_type || "")}&limit=${req.limit || 10}&offset=${req.offset || 0}`,
  );

export const GetPreviewURL = (id: string): Promise<string> =>
  rest<{ url: string }>("GET", `/tracks/${encodeURIComponent(id)}/preview`).then(r => r.url);

// ─── Download ─────────────────────────────────────────────────────────────────

export const DownloadTrack        = (req: any) => rest<any>("POST", "/downloads/track", req);
export const DownloadLyrics       = (req: any) => rest<any>("POST", "/media/lyrics", req);
export const DownloadCover        = (req: any) => rest<any>("POST", "/media/cover", req);
export const DownloadHeader       = (req: any) => rest<any>("POST", "/media/header", req);
export const DownloadGalleryImage = (req: any) => rest<any>("POST", "/media/gallery", req);
export const DownloadAvatar       = (req: any) => rest<any>("POST", "/media/avatar", req);
export const EnqueueBatch         = (req: any) => rest<any>("POST", "/jobs", req);

// ─── Queue / Progress ─────────────────────────────────────────────────────────

export const GetDownloadQueue        = () => rest<any>("GET", "/jobs");
export const GetDownloadProgress     = () => rest<any>("GET", "/jobs/progress");
export const ClearCompletedDownloads = () => rest<void>("DELETE", "/jobs/completed");
export const ClearAllDownloads       = () => rest<void>("DELETE", "/jobs");
export const CancelAllQueuedItems    = () => rest<void>("DELETE", "/jobs/pending");
export const AddToDownloadQueue      = (spotifyId: string, trackName: string, artistName: string, albumName: string) =>
  rest<{ id: string }>("POST", "/jobs/legacy/enqueue", {
    spotify_id: spotifyId, track_name: trackName, artist_name: artistName, album_name: albumName,
  }).then(r => r.id);
export const SkipDownloadItem        = (itemId: string, filePath: string) =>
  rest<void>("POST", "/jobs/legacy/skip", { item_id: itemId, file_path: filePath });
export const MarkDownloadItemFailed  = (itemId: string, errorMsg: string) =>
  rest<void>("POST", "/jobs/legacy/fail", { item_id: itemId, error_msg: errorMsg });

// ─── History ──────────────────────────────────────────────────────────────────

export const GetDownloadHistory        = () => rest<any[]>("GET", "/history/downloads");
export const ClearDownloadHistory      = () => rest<void>("DELETE", "/history/downloads");
export const DeleteDownloadHistoryItem = (id: string) => rest<void>("DELETE", `/history/downloads/${encodeURIComponent(id)}`);
export const GetFetchHistory           = () => rest<any[]>("GET", "/history/fetch");
export const AddFetchHistory           = (item: any) => rest<void>("POST", "/history/fetch", item);
export const ClearFetchHistory         = () => rest<void>("DELETE", "/history/fetch");
export const ClearFetchHistoryByType   = (itemType: string) => rest<void>("DELETE", `/history/fetch?type=${encodeURIComponent(itemType)}`);
export const DeleteFetchHistoryItem    = (id: string) => rest<void>("DELETE", `/history/fetch/${encodeURIComponent(id)}`);
export const ExportFailedDownloads     = () =>
  rest<{ message: string }>("GET", "/history/downloads/export").then(r => r.message);

// ─── Settings ─────────────────────────────────────────────────────────────────

export const LoadSettings  = () => rest<any>("GET", "/settings");
export const SaveSettings  = (settings: any) => rest<void>("PUT", "/settings", settings);
export const GetDefaults   = () => rest<any>("GET", "/system/defaults");
export const GetConfigPath = () =>
  rest<{ os: string; config_path: string; home_dir: string; version: string }>("GET", "/system/info").then(r => r.config_path);
export const GetOSInfo = () =>
  rest<{ os: string; config_path: string; home_dir: string; version: string }>("GET", "/system/info").then(r => r.os);
export const GetUserHomeDir = () =>
  rest<{ os: string; config_path: string; home_dir: string; version: string }>("GET", "/system/info").then(r => r.home_dir);

// ─── Audio / File ─────────────────────────────────────────────────────────────

export const ConvertAudio          = (req: any) => rest<any[]>("POST", "/audio/convert", req);
export const AnalyzeTrack          = (filePath: string) => rest<any>("POST", "/audio/analyze", { file_path: filePath });
export const AnalyzeMultipleTracks = (filePaths: string[]) => rest<any>("POST", "/audio/analyze/batch", { file_paths: filePaths });
export const GetFileSizes          = (filePaths: string[]) => rest<any>("POST", "/files/sizes", { file_paths: filePaths });
export const ListDirectoryFiles    = (dirPath: string) => rest<any[]>("GET", `/files?path=${encodeURIComponent(dirPath)}`);
export const ListAudioFilesInDir   = (dirPath: string) => rest<any[]>("GET", `/files/audio?path=${encodeURIComponent(dirPath)}`);
export const ReadFileMetadata      = (filePath: string) => rest<any>("GET", `/files/metadata?path=${encodeURIComponent(filePath)}`);
export const ReadImageAsBase64     = (filePath: string) =>
  rest<{ data: string }>("GET", `/files/image?path=${encodeURIComponent(filePath)}`).then(r => r.data);
export const ReadTextFile          = (filePath: string) =>
  rest<{ content: string }>("POST", "/files/read", { file_path: filePath }).then(r => r.content);
export const RenameFileTo          = (oldPath: string, newName: string) =>
  rest<void>("POST", "/files/rename", { old_path: oldPath, new_name: newName });
export const RenameFilesByMetadata = (files: string[], format: string) =>
  rest<any[]>("POST", "/files/rename/batch", { files, format });
export const PreviewRenameFiles    = (files: string[], format: string) =>
  rest<any[]>("POST", "/files/rename/preview", { files, format });
export const UploadImage           = (filePath: string) =>
  rest<{ url: string }>("POST", "/files/upload/path", { file_path: filePath }).then(r => r.url);
export const UploadImageBytes      = (filename: string, base64Data: string) =>
  rest<{ url: string }>("POST", "/files/upload/image", { filename, base64_data: base64Data }).then(r => r.url);
export const CreateM3U8File        = (m3u8Name: string, outputDir: string, filePaths: string[], jellyfinMusicPath: string) =>
  rest<void>("POST", "/files/m3u8", { m3u8_name: m3u8Name, output_dir: outputDir, file_paths: filePaths, jellyfin_music_path: jellyfinMusicPath });
export const CheckFilesExistence   = (outputDir: string, rootDir: string, tracks: any[]) =>
  rest<any[]>("POST", "/files/exists", { output_dir: outputDir, root_dir: rootDir, tracks });

// ─── Folder / File (désactivés en web, l'UI utilise des champs texte) ─────────

export const OpenFolder       = (_path: string) => Promise.resolve();
export const SelectFolder     = (_defaultPath?: string) => Promise.resolve("");
export const SelectFile       = () => Promise.resolve("");
export const SelectAudioFiles = () => Promise.resolve([] as string[]);
export const SelectImageVideo = () => Promise.resolve([] as string[]);

// ─── Watchlist ────────────────────────────────────────────────────────────────

export const AddToWatchlist      = (req: any) => rest<any>("POST", "/watchlists", req);
export const RemoveFromWatchlist = (id: string) => rest<void>("DELETE", `/watchlists/${encodeURIComponent(id)}`);
export const GetWatchlists       = () => rest<any[]>("GET", "/watchlists");
export const UpdateWatchlist     = (req: { id: string; interval_hours: number; sync_deletions: boolean }) =>
  rest<void>("PUT", `/watchlists/${encodeURIComponent(req.id)}`, req);
export const GetWatchlistStats   = (id: string) => rest<any>("GET", `/watchlists/${encodeURIComponent(id)}/stats`);
export const GetWatchlistHistory = (id: string) => rest<any[]>("GET", `/watchlists/${encodeURIComponent(id)}/history`);
export const SyncWatchlist       = (id: string) => rest<void>("POST", `/watchlists/${encodeURIComponent(id)}/sync`);

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

export const ListAPIKeys   = () => rest<APIKeyMeta[]>("GET", "/auth/keys");
export const CreateAPIKey  = (name: string, permissions: string[]) =>
  rest<CreatedAPIKey>("POST", "/auth/keys", { name, permissions });
export const DeleteAPIKey  = (id: string) => rest<void>("DELETE", `/auth/keys/${encodeURIComponent(id)}`);

// ─── Tidal Auth ───────────────────────────────────────────────────────────────

export interface TidalStatus {
  connected: boolean;
  expires_at?: number;   // unix timestamp
  username?: string;
}

export const GetTidalAuthURL      = () => rest<{ url: string }>("GET", "/auth/tidal/url").then(r => r.url);
export const SubmitTidalCallback  = (callbackURL: string) =>
  rest<void>("POST", "/auth/tidal/callback", { callback_url: callbackURL });
export const GetTidalStatus       = () => rest<TidalStatus>("GET", "/auth/tidal/status");
export const DisconnectTidal      = () => rest<void>("DELETE", "/auth/tidal");

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

export const StartTidalDeviceAuth = () => rest<TidalDeviceAuth>("POST", "/auth/tidal/device/start", {});
export const PollTidalDeviceAuth  = (deviceCode: string) =>
  rest<TidalDevicePollResult>("POST", "/auth/tidal/device/poll", { device_code: deviceCode });

// ─── API Library ──────────────────────────────────────────────────────────────

export interface ServiceStatus {
  name: string;
  url: string;
  status: "ok" | "down" | "ratelimited" | "unconfigured";
  latency_ms?: number;
  checked_at: number;
  error?: string;
}

export const GetAPIStatuses = () => rest<ServiceStatus[]>("GET", "/apis/status");

export interface ProxyConfig {
  tidal_proxies: string[];
  qobuz_providers: string[];
  amazon_proxies: string[];
  deezer_proxies: string[];
  /** Override manuel du client_id OAuth Tidal. Vide = auto-découverte. */
  tidal_client_id: string;
}

export const GetAPIProxies    = () => rest<ProxyConfig>("GET", "/apis/proxies");
export const UpdateAPIProxies = (cfg: ProxyConfig) => rest<void>("PUT", "/apis/proxies", cfg);
