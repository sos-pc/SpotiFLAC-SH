// rpc.ts — client HTTP qui remplace les Wails bindings
// Tous les appels vont vers POST /api/rpc { method, params }

const RPC_URL = "/api/rpc";

async function call<T>(method: string, params?: unknown): Promise<T> {
  const token = localStorage.getItem("spotiflac_token");
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (token) headers["Authorization"] = `Bearer ${token}`;
  const res = await fetch(RPC_URL, {
    method: "POST",
    headers,
    body: JSON.stringify({ method, params: params ?? null }),
  });

  if (!res.ok) {
    throw new Error(`RPC HTTP error: ${res.status}`);
  }

  const json = await res.json();

  if (json.error) {
    throw new Error(json.error);
  }

  return json.result as T;
}

// ─── Spotify ──────────────────────────────────────────────────────────────────
export const GetSpotifyMetadata     = (req: any) => call<string>("GetSpotifyMetadata", req);
export const GetStreamingURLs       = (id: string, region: string) => call<string>("GetStreamingURLs", { spotify_track_id: id, region });
export const CheckTrackAvailability = (id: string) => call<string>("CheckTrackAvailability", { spotify_track_id: id });
export const SearchSpotify          = (req: any) => call<any>("SearchSpotify", req);
export const SearchSpotifyByType    = (req: any) => call<any>("SearchSpotifyByType", req);
export const GetPreviewURL          = (id: string) => call<string>("GetPreviewURL", { track_id: id });

// ─── Download ─────────────────────────────────────────────────────────────────
export const DownloadTrack          = (req: any) => call<any>("DownloadTrack", req);
export const DownloadLyrics         = (req: any) => call<any>("DownloadLyrics", req);
export const DownloadCover          = (req: any) => call<any>("DownloadCover", req);
export const DownloadHeader         = (req: any) => call<any>("DownloadHeader", req);
export const DownloadGalleryImage   = (req: any) => call<any>("DownloadGalleryImage", req);
export const DownloadAvatar         = (req: any) => call<any>("DownloadAvatar", req);
export const EnqueueBatch           = (req: any) => call<any>("EnqueueBatch", req);

// ─── Queue / Progress ─────────────────────────────────────────────────────────
export const GetDownloadQueue        = () => call<any>("GetDownloadQueue");
export const GetDownloadProgress     = () => call<any>("GetDownloadProgress");
export const ClearCompletedDownloads = () => call<void>("ClearCompletedDownloads");
export const ClearAllDownloads       = () => call<void>("ClearAllDownloads");
export const CancelAllQueuedItems    = () => call<void>("CancelAllQueuedItems");
export const AddToDownloadQueue      = (spotifyId: string, trackName: string, artistName: string, albumName: string) =>
  call<string>("AddToDownloadQueue", { spotify_id: spotifyId, track_name: trackName, artist_name: artistName, album_name: albumName });
export const SkipDownloadItem        = (itemId: string, filePath: string) =>
  call<void>("SkipDownloadItem", { item_id: itemId, file_path: filePath });
export const MarkDownloadItemFailed  = (itemId: string, errorMsg: string) =>
  call<void>("MarkDownloadItemFailed", { item_id: itemId, error_msg: errorMsg });

// ─── History ──────────────────────────────────────────────────────────────────
export const GetDownloadHistory        = () => call<any[]>("GetDownloadHistory");
export const ClearDownloadHistory      = () => call<void>("ClearDownloadHistory");
export const DeleteDownloadHistoryItem = (id: string) => call<void>("DeleteDownloadHistoryItem", { id });
export const GetFetchHistory           = () => call<any[]>("GetFetchHistory");
export const AddFetchHistory           = (item: any) => call<void>("AddFetchHistory", item);
export const ClearFetchHistory         = () => call<void>("ClearFetchHistory");
export const ClearFetchHistoryByType   = (itemType: string) => call<void>("ClearFetchHistoryByType", { item_type: itemType });
export const DeleteFetchHistoryItem    = (id: string) => call<void>("DeleteFetchHistoryItem", { id });

// ─── Settings ─────────────────────────────────────────────────────────────────
export const LoadSettings  = () => call<any>("LoadSettings");
export const SaveSettings  = (settings: any) => call<void>("SaveSettings", { settings });
export const GetDefaults   = () => call<any>("GetDefaults");
export const GetConfigPath = () => call<string>("GetConfigPath");
export const GetOSInfo     = () => call<string>("GetOSInfo");

// ─── FFmpeg ───────────────────────────────────────────────────────────────────
export const IsFFmpegInstalled    = () => call<boolean>("IsFFmpegInstalled");
export const IsFFprobeInstalled   = () => call<boolean>("IsFFprobeInstalled");
export const CheckFFmpegInstalled = () => call<boolean>("CheckFFmpegInstalled");
export const GetFFmpegPath        = () => call<string>("GetFFmpegPath");
export const DownloadFFmpeg       = () => call<any>("DownloadFFmpeg");

// ─── Audio / File ─────────────────────────────────────────────────────────────
export const ConvertAudio          = (req: any) => call<any[]>("ConvertAudio", req);
export const AnalyzeTrack          = (filePath: string) => call<string>("AnalyzeTrack", { file_path: filePath });
export const AnalyzeMultipleTracks = (filePaths: string[]) => call<string>("AnalyzeMultipleTracks", { file_paths: filePaths });
export const GetFileSizes          = (filePaths: string[]) => call<any>("GetFileSizes", { file_paths: filePaths });
export const ListDirectoryFiles    = (dirPath: string) => call<any[]>("ListDirectoryFiles", { dir_path: dirPath });
export const GetUserHomeDir        = () => call<string>("GetUserHomeDir");
export const ListAudioFilesInDir   = (dirPath: string) => call<any[]>("ListAudioFilesInDir", { dir_path: dirPath });
export const ReadFileMetadata      = (filePath: string) => call<any>("ReadFileMetadata", { file_path: filePath });
export const ReadImageAsBase64     = (filePath: string) => call<string>("ReadImageAsBase64", { file_path: filePath });
export const ReadTextFile          = (filePath: string) => call<string>("ReadTextFile", { file_path: filePath });
export const RenameFileTo          = (oldPath: string, newName: string) => call<void>("RenameFileTo", { old_path: oldPath, new_name: newName });
export const RenameFilesByMetadata = (files: string[], format: string) => call<any[]>("RenameFilesByMetadata", { files, format });
export const PreviewRenameFiles    = (files: string[], format: string) => call<any[]>("PreviewRenameFiles", { files, format });
export const UploadImage           = (filePath: string) => call<string>("UploadImage", { file_path: filePath });
export const UploadImageBytes      = (filename: string, base64Data: string) => call<string>("UploadImageBytes", { filename, base64_data: base64Data });
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
export const AddToWatchlist      = (req: any) => call<any>("AddToWatchlist", req);
export const RemoveFromWatchlist = (id: string) => call<void>("RemoveFromWatchlist", { id });
export const GetWatchlists       = () => call<any[]>("GetWatchlists");
export const UpdateWatchlist     = (req: { id: string; interval_hours: number; sync_deletions: boolean }) => call<void>("UpdateWatchlist", req);
export const GetWatchlistStats   = (id: string) => call<{ watchlist_id: string; downloaded: number; failed: number; skipped: number; total_size_mb: number }>("GetWatchlistStats", { id });
export const GetWatchlistHistory = (id: string) => call<{ track_name: string; artist_name: string; album_name: string; status: string; total_size: number; updated_at: number; file_path: string; error: string }[]>("GetWatchlistHistory", { id });
// SyncWatchlist = nouveaux tracks Spotify + retry des jobs failed (remplace ForceSyncWatchlist + RedownloadWatchlist)
export const SyncWatchlist       = (id: string) => call<void>("SyncWatchlist", { id });
// Gardés pour compatibilité (ne plus utiliser dans l'UI)
export const RedownloadWatchlist = (id: string) => call<void>("RedownloadWatchlist", { id });
export const ForceSyncWatchlist  = (id: string) => call<void>("ForceSyncWatchlist", { id });
