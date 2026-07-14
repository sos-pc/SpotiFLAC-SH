import { useState, useRef, useEffect } from "react";
import { getSettings } from "@/lib/settings";
import { toastWithSound as toast } from "@/lib/toast-with-sound";
import { getFirstArtist, resolvePlaylistBaseDir } from "@/lib/utils";
import { logger } from "@/lib/logger";
import { getStreamToken } from "@/lib/auth";
import { downloadWithAutoFallback } from "@/lib/downloadFallback";
import { useJobsStreamEvent } from "./useJobsStreamEvent";
import type { TrackMetadata, FileExistsCheck } from "@/types/api";
import type { Settings } from "@/lib/settings";
import { CheckFilesExistence, CreateM3U8File, EnqueueBatch } from "@/lib/rpc";

// buildExistenceCheckRequests, enqueueTracksBatch and maybeCreateM3U8 are
// shared by handleDownloadSelected and handleDownloadAll below, which used
// to independently reimplement all three — including a divergence where
// handleDownloadAll called CreateM3U8File with an empty path list (creating
// a bogus empty playlist + a misleading "M3U8 playlist created" toast on any
// batch with no pre-existing files, i.e. the common case of downloading a
// brand new playlist/album) while handleDownloadSelected correctly skipped
// the call in that case.
function buildExistenceCheckRequests(
  settings: Settings,
  tracks: TrackMetadata[],
): FileExistsCheck[] {
  const useAlbumTrackNumber =
    settings.folderTemplate?.includes("{album}") || false;
  return tracks.map((track) => {
    const displayArtist =
      settings.useFirstArtistOnly && track.artists
        ? getFirstArtist(track.artists)
        : track.artists;
    const displayAlbumArtist =
      settings.useFirstArtistOnly && track.album_artist
        ? getFirstArtist(track.album_artist)
        : track.album_artist;
    return {
      spotify_id: track.spotify_id || "",
      track_name: track.name || "",
      artist_name: displayArtist || "",
      album_name: track.album_name || "",
      album_artist: displayAlbumArtist || "",
      release_date: track.release_date || "",
      track_number: track.track_number || 0,
      disc_number: track.disc_number || 0,
      position: 0,
      use_album_track_number: useAlbumTrackNumber,
      filename_format: settings.filenameTemplate || "",
      include_track_number: settings.trackNumber || false,
      audio_format: "flac",
    };
  });
}

async function enqueueTracksBatch(
  settings: Settings,
  orderedTracks: TrackMetadata[],
  tracksToDownload: TrackMetadata[],
  folderName: string | undefined,
  skippedCount: number,
  downloadMode: "server" | "browser",
  browserBatchIdsRef: { current: Set<string> },
) {
  if (tracksToDownload.length === 0) {
    toast.info(`${skippedCount} tracks already exist`);
    return;
  }
  const is24Bit = (settings.autoQuality || "24") === "24";
  const jobTracks = tracksToDownload.map((track) => {
    const displayArtist =
      settings.useFirstArtistOnly && track.artists
        ? getFirstArtist(track.artists)
        : track.artists;
    const displayAlbumArtist =
      settings.useFirstArtistOnly && track.album_artist
        ? getFirstArtist(track.album_artist)
        : track.album_artist;
    const originalIndex = orderedTracks.indexOf(track);
    return {
      spotify_id: track.spotify_id || "",
      track_name: track.name || "",
      artist_name: displayArtist || "",
      album_name: track.album_name || "",
      album_artist: displayAlbumArtist || track.album_artist || "",
      release_date: track.release_date || "",
      cover_url: track.images || "",
      track_number: track.track_number || 0,
      disc_number: track.disc_number || 0,
      total_tracks: track.total_tracks || 0,
      total_discs: track.total_discs || 0,
      copyright: track.copyright || "",
      publisher: track.publisher || "",
      duration_ms: track.duration_ms || 0,
      position: originalIndex + 1,
      playlist_name: folderName || "",
    };
  });
  const jobSettings = {
    service: settings.downloader || "tidal",
    downloadPath: settings.downloadPath,
    filenameTemplate: settings.filenameTemplate || "",
    folderTemplate: settings.folderTemplate || "",
    trackNumber: settings.trackNumber ?? true,
    embedLyrics: settings.embedLyrics ?? false,
    embedMaxQualityCover: settings.embedMaxQualityCover ?? false,
    tidalQuality: is24Bit ? "HI_RES_LOSSLESS" : "LOSSLESS",
    qobuzQuality: is24Bit ? "27" : "6",
    autoOrder: settings.autoOrder || "tidal-amazon-qobuz",
    autoQuality: settings.autoQuality || "24",
    useFirstArtistOnly: settings.useFirstArtistOnly ?? false,
    useSingleGenre: settings.useSingleGenre ?? false,
    embedGenre: settings.embedGenre ?? false,
    createPlaylistFolder: settings.createPlaylistFolder ?? false,
  };
  try {
    const resp = await EnqueueBatch({ tracks: jobTracks, settings: jobSettings });
    logger.success(
      `[EnqueueBatch] ${resp.enqueued} tracks queued, ${resp.skipped} skipped`,
    );
    if (downloadMode === "browser" && resp.batch_id) {
      browserBatchIdsRef.current.add(resp.batch_id);
    }
    if (skippedCount === 0) {
      toast.success(`${resp.enqueued} tracks queued for background download`);
    } else {
      toast.info(`${resp.enqueued} queued, ${skippedCount} already exist`);
    }
  } catch (err) {
    logger.error(`EnqueueBatch failed: ${err}`);
    toast.error(`Failed to queue tracks: ${err}`);
  }
}

async function maybeCreateM3U8(
  settings: Settings,
  folderName: string | undefined,
  outputDir: string,
  paths: string[],
) {
  if (!settings.createM3u8File || !folderName || paths.length === 0) return;
  try {
    logger.info(`creating m3u8 playlist: ${folderName}`);
    await CreateM3U8File(
      folderName,
      outputDir,
      paths,
      settings.jellyfinMusicPath || "",
      settings.downloadPath || "",
    );
    toast.success("M3U8 playlist created");
  } catch (err) {
    logger.error(`failed to create m3u8 playlist: ${err}`);
    toast.error(`Failed to create M3U8 playlist: ${err}`);
  }
}

export function useDownload(region: string) {
  const [downloadProgress, setDownloadProgress] = useState<number>(0);
  const [isDownloading, setIsDownloading] = useState(false);
  const [downloadingTrack, setDownloadingTrack] = useState<string | null>(null);
  const [bulkDownloadType, setBulkDownloadType] = useState<
    "all" | "selected" | null
  >(null);
  const [downloadedTracks, setDownloadedTracks] = useState<Set<string>>(
    new Set(),
  );
  const [failedTracks, setFailedTracks] = useState<Set<string>>(new Set());
  const [skippedTracks, setSkippedTracks] = useState<Set<string>>(new Set());
  const [currentDownloadInfo, setCurrentDownloadInfo] = useState<{
    name: string;
    artists: string;
  } | null>(null);
  const shouldStopDownloadRef = useRef(false);

  // ── Browser download mode ─────────────────────────────────────────────────
  const [downloadMode, setDownloadModeInternal] = useState<
    "server" | "browser"
  >(
    () =>
      (localStorage.getItem("download_mode") as "server" | "browser") ||
      "server",
  );
  const browserBatchIdsRef = useRef<Set<string>>(new Set());
  const triggeredJobIdsRef = useRef<Set<string>>(new Set());

  // Sync with DownloadModeToggle dispatching "spotif:downloadModeChange"
  useEffect(() => {
    const handler = (e: Event) => {
      setDownloadModeInternal((e as CustomEvent<"server" | "browser">).detail);
    };
    window.addEventListener("spotif:downloadModeChange", handler);
    return () =>
      window.removeEventListener("spotif:downloadModeChange", handler);
  }, []);

  // SSE listener for browser-mode auto-download, on the shared jobs stream
  // connection (see lib/jobsStream.ts) — only active while browser mode is on.
  useJobsStreamEvent(
    "job_update",
    (e: MessageEvent) => {
      const job = JSON.parse(e.data) as {
        id: string;
        status: string;
        batch_id?: string;
        file_path?: string;
      };
      if (
        job.status === "done" &&
        job.batch_id &&
        browserBatchIdsRef.current.has(job.batch_id) &&
        job.file_path &&
        !triggeredJobIdsRef.current.has(job.id)
      ) {
        triggeredJobIdsRef.current.add(job.id);
        (async () => {
          // Short-lived stream token, not the 24h session JWT — this URL
          // can't set an Authorization header (it's a plain <a href>
          // download), so a full session token here would sit in browser
          // history / reverse-proxy logs for the rest of its lifetime.
          const t = await getStreamToken();
          if (!t) return;
          const a = document.createElement("a");
          a.href = `/api/v1/jobs/${job.id}/download?token=${encodeURIComponent(t)}`;
          a.download = "";
          document.body.appendChild(a);
          a.click();
          document.body.removeChild(a);
        })();
      }
    },
    downloadMode === "browser",
  );
  const handleDownloadTrack = async (
    id: string,
    trackName?: string,
    artistName?: string,
    albumName?: string,
    spotifyId?: string,
    playlistName?: string,
    durationMs?: number,
    position?: number,
    albumArtist?: string,
    releaseDate?: string,
    coverUrl?: string,
    spotifyTrackNumber?: number,
    spotifyDiscNumber?: number,
    spotifyTotalTracks?: number,
    spotifyTotalDiscs?: number,
    copyright?: string,
    publisher?: string,
  ) => {
    if (!id) {
      toast.error("No ID found for this track");
      return;
    }
    const settings = getSettings();
    const displayArtist =
      settings.useFirstArtistOnly && artistName
        ? getFirstArtist(artistName)
        : artistName;
    logger.info(`starting download: ${trackName} - ${displayArtist}`);
    setDownloadingTrack(id);
    try {
      const response = await downloadWithAutoFallback({
        region,
        id,
        settings,
        trackName,
        artistName,
        albumName,
        playlistName,
        position,
        spotifyId,
        durationMs,
        albumArtist: albumArtist || "",
        releaseDate,
        coverUrl,
        spotifyTrackNumber,
        spotifyDiscNumber,
        spotifyTotalTracks,
        spotifyTotalDiscs,
        copyright,
        publisher,
      });
      if (response.success) {
        if (response.already_exists) {
          toast.info(response.message);
          setSkippedTracks((prev) => new Set(prev).add(id));
        } else {
          toast.success(response.message);
        }
        setDownloadedTracks((prev) => new Set(prev).add(id));
        setFailedTracks((prev) => {
          const newSet = new Set(prev);
          newSet.delete(id);
          return newSet;
        });
      } else {
        toast.error(response.error || "Download failed");
        setFailedTracks((prev) => new Set(prev).add(id));
      }
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Download failed");
      setFailedTracks((prev) => new Set(prev).add(id));
    } finally {
      setDownloadingTrack(null);
    }
  };
  const handleDownloadSelected = async (
    selectedTracks: string[],
    allTracks: TrackMetadata[],
    folderName?: string,
    isAlbum?: boolean,
  ) => {
    if (selectedTracks.length === 0) {
      toast.error("No tracks selected");
      return;
    }
    logger.info(
      `starting batch download: ${selectedTracks.length} selected tracks`,
    );
    const settings = getSettings();
    setIsDownloading(true);
    setBulkDownloadType("selected");
    setDownloadProgress(0);
    const outputDir = resolvePlaylistBaseDir(settings, folderName, isAlbum);
    const selectedTrackObjects = selectedTracks
      .map((id) => allTracks.find((t) => t.spotify_id === id))
      .filter((t): t is TrackMetadata => t !== undefined);
    logger.info(`checking existing files in parallel...`);
    const existenceChecks = buildExistenceCheckRequests(
      settings,
      selectedTrackObjects,
    );
    const existenceResults = await CheckFilesExistence(
      outputDir,
      settings.downloadPath,
      existenceChecks,
    );
    const existingSpotifyIDs = new Set<string>();
    const finalFilePaths = new Map<string, string>();
    for (const result of existenceResults) {
      if (result.exists) {
        existingSpotifyIDs.add(result.spotify_id);
        finalFilePaths.set(result.spotify_id, result.file_path || "");
      }
    }
    logger.info(`found ${existingSpotifyIDs.size} existing files`);
    // Marquer les fichiers existants comme skippés dans l'UI
    for (const id of selectedTracks) {
      const track = allTracks.find((t) => t.spotify_id === id);
      if (!track) continue;
      const trackID = track.spotify_id || id;
      if (existingSpotifyIDs.has(trackID)) {
        setSkippedTracks((prev) => new Set(prev).add(id));
        setDownloadedTracks((prev) => new Set(prev).add(id));
      }
    }
    const tracksToDownload = selectedTrackObjects.filter((track) => {
      const trackID = track.spotify_id || "";
      return !existingSpotifyIDs.has(trackID);
    });
    const skippedCount = existingSpotifyIDs.size;
    const total = selectedTracks.length;
    setDownloadProgress(Math.round((skippedCount / total) * 100));
    // EnqueueBatch — envoie tous les tracks en background en une seule requête
    await enqueueTracksBatch(
      settings,
      selectedTrackObjects,
      tracksToDownload,
      folderName,
      skippedCount,
      downloadMode,
      browserBatchIdsRef,
    );
    setDownloadingTrack(null);
    setCurrentDownloadInfo(null);
    setIsDownloading(false);
    setBulkDownloadType(null);
    shouldStopDownloadRef.current = false;
    const paths = selectedTrackObjects
      .map((t) => finalFilePaths.get(t.spotify_id || "") || "")
      .filter((p) => p !== "");
    await maybeCreateM3U8(settings, folderName, outputDir, paths);
  };
  const handleDownloadAll = async (
    tracks: TrackMetadata[],
    folderName?: string,
    isAlbum?: boolean,
  ) => {
    const tracksWithId = tracks.filter((track) => track.spotify_id);
    if (tracksWithId.length === 0) {
      toast.error("No tracks available for download");
      return;
    }
    logger.info(`starting batch download: ${tracksWithId.length} tracks`);
    const settings = getSettings();
    setIsDownloading(true);
    setBulkDownloadType("all");
    setDownloadProgress(0);
    const outputDir = resolvePlaylistBaseDir(settings, folderName, isAlbum);
    logger.info(`checking existing files in parallel...`);
    const existenceChecks = buildExistenceCheckRequests(settings, tracksWithId);
    const existenceResults = await CheckFilesExistence(
      outputDir,
      settings.downloadPath,
      existenceChecks,
    );
    const finalFilePaths: string[] = new Array(tracksWithId.length).fill("");
    const existingSpotifyIDs = new Set<string>();
    for (let i = 0; i < existenceResults.length; i++) {
      const result = existenceResults[i];
      if (result.exists) {
        existingSpotifyIDs.add(result.spotify_id);
        finalFilePaths[i] = result.file_path || "";
      }
    }
    logger.info(`found ${existingSpotifyIDs.size} existing files`);
    // Marquer les fichiers existants comme skippés dans l'UI
    for (const track of tracksWithId) {
      const trackID = track.spotify_id || "";
      if (existingSpotifyIDs.has(trackID)) {
        setSkippedTracks((prev: Set<string>) => new Set(prev).add(trackID));
        setDownloadedTracks((prev: Set<string>) => new Set(prev).add(trackID));
      }
    }
    const tracksToDownload = tracksWithId.filter((track) => {
      const trackID = track.spotify_id || "";
      return !existingSpotifyIDs.has(trackID);
    });
    const skippedCount = existingSpotifyIDs.size;
    const total = tracksWithId.length;
    setDownloadProgress(Math.round((skippedCount / total) * 100));
    // EnqueueBatch — envoie tous les tracks en background en une seule requête
    await enqueueTracksBatch(
      settings,
      tracksWithId,
      tracksToDownload,
      folderName,
      skippedCount,
      downloadMode,
      browserBatchIdsRef,
    );
    setDownloadingTrack(null);
    setCurrentDownloadInfo(null);
    setIsDownloading(false);
    setBulkDownloadType(null);
    shouldStopDownloadRef.current = false;
    await maybeCreateM3U8(
      settings,
      folderName,
      outputDir,
      finalFilePaths.filter((p) => p !== ""),
    );
  };
  const handleStopDownload = () => {
    logger.info("download stopped by user");
    shouldStopDownloadRef.current = true;
    toast.info("Stopping download...");
  };
  const resetDownloadedTracks = () => {
    setDownloadedTracks(new Set());
    setFailedTracks(new Set());
    setSkippedTracks(new Set());
  };
  return {
    downloadProgress,
    isDownloading,
    downloadingTrack,
    bulkDownloadType,
    downloadedTracks,
    failedTracks,
    skippedTracks,
    currentDownloadInfo,
    downloadMode,
    handleDownloadTrack,
    handleDownloadSelected,
    handleDownloadAll,
    handleStopDownload,
    resetDownloadedTracks,
  };
}
