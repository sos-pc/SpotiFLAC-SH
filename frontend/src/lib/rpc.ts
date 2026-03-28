// rpc.ts — client HTTP REST + fallback /api/rpc pour les méthodes sans endpoint REST

const RPC_URL = "/api/rpc";

// ─── Helpers REST ──────────────────────────────────────────────────────────────

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

// ─── Fallback RPC (pour les méthodes sans endpoint REST) ──────────────────────

async function call<T>(method: string, params?: unknown): Promise<T> {
  const token = localStorage.getItem("spotiflac_token");
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (token) headers["Authorization"] = `Bearer ${token}`;
  const res = await fetch(RPC_URL, {
    method: "POST",
    headers,
    body: JSON.stringify({ method, params: params ?? null }),
  });
  if (res.status === 401) {
    window.dispatchEvent(new CustomEvent("auth:expired"));
    throw new Error("Session expired");
  }
  if (!res.ok) throw new Error(`RPC HTTP error: ${res.status}`);
  const json = await res.json();
  if (json.error) throw new Error(json.error);
  return json.result as T;
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

export const DownloadTrack          = (req: any) => call<any>("DownloadTrack", req);
export const DownloadLyrics         = (req: any) => rest<any>("POST", "/media/lyrics", req);
export const DownloadCover          = (req: any) => rest<any>("POST", "/media/cover", req);
export const DownloadHeader         = (req: any) => call<any>("DownloadHeader", req);
export const DownloadGalleryImage   = (req: any) => call<any>("DownloadGalleryImage", req);
export const DownloadAvatar         = (req: any) => call<any>("DownloadAvatar", req);
export const EnqueueBatch           = (req: any) => rest<any>("POST", "/jobs", req);

// ─── Queue / Progress ─────────────────────────────────────────────────────────

export const GetDownloadQueue        = () => rest<any>("GET", "/jobs");
export const GetDownloadProgress     = () => call<any>("GetDownloadProgress");
export const ClearCompletedDownloads = () => rest<void>("DELETE", "/jobs/completed");
export const ClearAllDownloads       = () => rest<void>("DELETE", "/jobs");
export const CancelAllQueuedItems    = () => call<void>("CancelAllQueuedItems");
export const AddToDownloadQueue      = (spotifyId: string, trackName: string, artistName: string, albumName: string) =>
  call<string>("AddToDownloadQueue", { spotify_id: spotifyId, track_name: trackName, artist_name: artistName, album_name: albumName });
export const SkipDownloadItem        = (itemId: string, filePath: string) =>
  call<void>("SkipDownloadItem", { item_id: itemId, file_path: filePath });
export const MarkDownloadItemFailed  = (itemId: string, errorMsg: string) =>
  call<void>("MarkDownloadItemFailed", { item_id: itemId, error_msg: errorMsg });

// ─── History ──────────────────────────────────────────────────────────────────

export const GetDownloadHistory        = () => rest<any[]>("GET", "/history/downloads");
export const ClearDownloadHistory      = () => rest<void>("DELETE", "/history/downloads");
export const DeleteDownloadHistoryItem = (id: string) => rest<void>("DELETE", `/history/downloads/${encodeURIComponent(id)}`);
export const GetFetchHistory           = () => rest<any[]>("GET", "/history/fetch");
export const AddFetchHistory           = (item: any) => rest<void>("POST", "/history/fetch", item);
export const ClearFetchHistory         = () => rest<void>("DELETE", "/history/fetch");
export const ClearFetchHistoryByType   = (itemType: string) => call<void>("ClearFetchHistoryByType", { item_type: itemType });
export const DeleteFetchHistoryItem    = (id: string) => rest<void>("DELETE", `/history/fetch/${encodeURIComponent(id)}`);

// ─── Settings ─────────────────────────────────────────────────────────────────

export const LoadSettings  = () => rest<any>("GET", "/settings");
export const SaveSettings  = (settings: any) => rest<void>("PUT", "/settings", settings);
export const GetDefaults   = () => call<any>("GetDefaults");
export const GetConfigPath = () =>
  rest<{ os: string; config_path: string; version: string }>("GET", "/system/info").then(r => r.config_path);
export const GetOSInfo = () =>
  rest<{ os: string; config_path: string; version: string }>("GET", "/system/info").then(r => r.os);

// ─── FFmpeg ───────────────────────────────────────────────────────────────────

export const IsFFmpegInstalled    = () => rest<boolean>("GET", "/system/ffmpeg");
export const IsFFprobeInstalled   = () => call<boolean>("IsFFprobeInstalled");
export const CheckFFmpegInstalled = () => rest<boolean>("GET", "/system/ffmpeg");
export const GetFFmpegPath        = () => call<string>("GetFFmpegPath");
export const DownloadFFmpeg       = () => rest<any>("POST", "/system/ffmpeg/install");

// ─── Audio / File ─────────────────────────────────────────────────────────────

export const ConvertAudio          = (req: any) => rest<any[]>("POST", "/audio/convert", req);
export const AnalyzeTrack          = (filePath: string) => rest<any>("POST", "/audio/analyze", { file_path: filePath });
export const AnalyzeMultipleTracks = (filePaths: string[]) => call<string>("AnalyzeMultipleTracks", { file_paths: filePaths });
export const GetFileSizes          = (filePaths: string[]) => call<any>("GetFileSizes", { file_paths: filePaths });
export const ListDirectoryFiles    = (dirPath: string) => rest<any[]>("GET", `/files?path=${encodeURIComponent(dirPath)}`);
export const GetUserHomeDir        = () => call<string>("GetUserHomeDir");
export const ListAudioFilesInDir   = (dirPath: string) => rest<any[]>("GET", `/files/audio?path=${encodeURIComponent(dirPath)}`);
export const ReadFileMetadata      = (filePath: string) => rest<any>("GET", `/files/metadata?path=${encodeURIComponent(filePath)}`);
export const ReadImageAsBase64     = (filePath: string) => call<string>("ReadImageAsBase64", { file_path: filePath });
export const ReadTextFile          = (filePath: string) => call<string>("ReadTextFile", { file_path: filePath });
export const RenameFileTo          = (oldPath: string, newName: string) =>
  rest<void>("POST", "/files/rename", { old_path: oldPath, new_name: newName });
export const RenameFilesByMetadata = (files: string[], format: string) =>
  call<any[]>("RenameFilesByMetadata", { files, format });
export const PreviewRenameFiles    = (files: string[], format: string) =>
  call<any[]>("PreviewRenameFiles", { files, format });
export const UploadImage           = (filePath: string) => call<string>("UploadImage", { file_path: filePath });
export const UploadImageBytes      = (filename: string, base64Data: string) =>
  call<string>("UploadImageBytes", { filename, base64_data: base64Data });
export const CreateM3U8File        = (m3u8Name: string, outputDir: string, filePaths: string[], jellyfinMusicPath: string) =>
  call<void>("CreateM3U8File", { m3u8_name: m3u8Name, output_dir: outputDir, file_paths: filePaths, jellyfin_music_path: jellyfinMusicPath });
export const CheckFilesExistence   = (outputDir: string, rootDir: string, tracks: any[]) =>
  call<any[]>("CheckFilesExistence", { output_dir: outputDir, root_dir: rootDir, tracks });
export const ExportFailedDownloads = () => call<string>("ExportFailedDownloads");

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
// Gardés pour compatibilité (ne plus utiliser dans l'UI)
export const RedownloadWatchlist = (id: string) => call<void>("RedownloadWatchlist", { id });
export const ForceSyncWatchlist  = (id: string) => call<void>("ForceSyncWatchlist", { id });
