import { useState, useRef, useEffect } from "react";
import { getSettings } from "@/lib/settings";
import { toastWithSound as toast } from "@/lib/toast-with-sound";
import { getFirstArtist } from "@/lib/utils";
import { logger } from "@/lib/logger";
import { getStreamToken } from "@/lib/auth";
import { downloadWithAutoFallback } from "@/lib/downloadFallback";
import { useJobsStreamEvent } from "./useJobsStreamEvent";
import type { TrackMetadata, FileExistsCheck } from "@/types/api";
import type { Settings } from "@/lib/settings";
import { CheckFilesExistence, EnqueueBatch } from "@/lib/rpc";

// buildExistenceCheckRequests and enqueueTracksBatch are shared by
// handleDownloadSelected and handleDownloadAll below, which used to
// reimplement them independently and had drifted apart.
//
// M3U8 writing is no longer here at all: the client used to call it right after
// enqueue with the paths from the existence check, so it listed only tracks
// already on disk and wrote nothing for a brand-new playlist. The server now
// writes it when the batch finishes (docs/settings-source-of-truth.md D5).
// No settings argument by design: the server derives the folder, the filename
// template, the track-number rule and the first-artist trimming from the user's
// saved settings, so the check targets exactly where a download lands
// (docs/settings-source-of-truth.md). The client sends raw identity only.
function buildExistenceCheckRequests(
  tracks: TrackMetadata[],
): FileExistsCheck[] {
  return tracks.map((track) => ({
    spotify_id: track.spotify_id || "",
    track_name: track.name || "",
    artist_name: track.artists || "",
    album_name: track.album_name || "",
    album_artist: track.album_artist || "",
    release_date: track.release_date || "",
    track_number: track.track_number || 0,
    disc_number: track.disc_number || 0,
    position: 0,
    audio_format: "flac",
  }));
}

async function enqueueTracksBatch(
  settings: Settings,
  orderedTracks: TrackMetadata[],
  tracksToDownload: TrackMetadata[],
  folderName: string | undefined,
  skippedCount: number,
  downloadMode: "server" | "browser",
  browserBatchIdsRef: { current: Set<string> },
  sourceId?: string,
) {
  if (tracksToDownload.length === 0) {
    toast.info(`${skippedCount} tracks already exist`);
    return;
  }
  const jobTracks = tracksToDownload.map((track) => {
    const originalIndex = orderedTracks.indexOf(track);
    return {
      spotify_id: track.spotify_id || "",
      track_name: track.name || "",
      // Raw names: the server applies useFirstArtistOnly itself.
      artist_name: track.artists || "",
      album_name: track.album_name || "",
      album_artist: track.album_artist || "",
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
  // Backend-authoritative (docs/settings-source-of-truth.md): the server rebuilds
  // every download setting from the user's saved settings for a normal batch.
  // Only the service override travels with the request.
  const jobSettings = {
    service: settings.downloader || "tidal",
  };
  try {
    const resp = await EnqueueBatch({
      tracks: jobTracks,
      settings: jobSettings,
      // The server writes the M3U8 when the batch finishes, from the tracks
      // that actually landed on disk (docs/settings-source-of-truth.md D5).
      m3u8_name: folderName || "",
      m3u8_source_id: sourceId || "",
    });
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

  // Tracks enqueued but not yet finished, keyed by Spotify id → the id the UI
  // uses for its badges (they differ in some views).
  //
  // POST /downloads/track and /jobs return on ENQUEUE, not on completion, so
  // treating that response as the outcome marked every track "downloaded" the
  // moment it was queued — including ones that went on to fail. The real
  // outcome only ever arrives on the jobs SSE stream, which is what resolves
  // these entries (docs/override-rework-plan.md phase 2b).
  const pendingTracksRef = useRef<Map<string, string>>(new Map());

  const markTrackPending = (spotifyId: string, uiId: string) => {
    if (!spotifyId) return;
    pendingTracksRef.current.set(spotifyId, uiId);
  };

  // Terminal job statuses seen on the stream resolve the badge for their track.
  useJobsStreamEvent("job_update", (e: MessageEvent) => {
    const job = JSON.parse(e.data) as {
      spotify_id?: string;
      status?: string;
      error?: string;
    };
    if (!job.spotify_id) return;
    const uiId = pendingTracksRef.current.get(job.spotify_id);
    if (!uiId) return;
    if (
      job.status !== "done" &&
      job.status !== "failed" &&
      job.status !== "skipped"
    ) {
      return;
    }
    pendingTracksRef.current.delete(job.spotify_id);
    setDownloadingTrack((cur) => (cur === uiId ? null : cur));
    if (job.status === "failed") {
      logger.error(`download failed: ${uiId} — ${job.error || "unknown error"}`);
      setFailedTracks((prev) => new Set(prev).add(uiId));
      setDownloadedTracks((prev) => {
        const next = new Set(prev);
        next.delete(uiId);
        return next;
      });
      return;
    }
    if (job.status === "skipped") {
      setSkippedTracks((prev) => new Set(prev).add(uiId));
    }
    setDownloadedTracks((prev) => new Set(prev).add(uiId));
    setFailedTracks((prev) => {
      const next = new Set(prev);
      next.delete(uiId);
      return next;
    });
  });

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
          // The only case the response settles on its own: the server checked
          // the disk before queuing anything.
          toast.info(response.message);
          setSkippedTracks((prev) => new Set(prev).add(id));
          setDownloadedTracks((prev) => new Set(prev).add(id));
          setFailedTracks((prev) => {
            const newSet = new Set(prev);
            newSet.delete(id);
            return newSet;
          });
          setDownloadingTrack(null);
          return;
        }
        // Queued, not downloaded. The badge stays in its downloading state
        // until the jobs stream reports the real outcome (phase 2b).
        toast.info(response.message);
        markTrackPending(spotifyId || id, id);
        return;
      }
      toast.error(response.error || "Download failed");
      setFailedTracks((prev) => new Set(prev).add(id));
      setDownloadingTrack(null);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Download failed");
      setFailedTracks((prev) => new Set(prev).add(id));
      setDownloadingTrack(null);
    }
  };
  const handleDownloadSelected = async (
    selectedTracks: string[],
    allTracks: TrackMetadata[],
    folderName?: string,
    // Identity of what was fetched, used only to disambiguate the M3U8
    // filename (two playlists whose names sanitise identically), the way a
    // watchlist id does. isAlbum used to sit here too: it fed the client-side
    // playlist-folder rule, which the server now owns.
    sourceId?: string,
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
    const selectedTrackObjects = selectedTracks
      .map((id) => allTracks.find((t) => t.spotify_id === id))
      .filter((t): t is TrackMetadata => t !== undefined);
    logger.info(`checking existing files in parallel...`);
    const existenceChecks = buildExistenceCheckRequests(selectedTrackObjects);
    // Directories ignored by the server — it derives them from the settings.
    const existenceResults = await CheckFilesExistence("", "", existenceChecks);
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
      sourceId,
    );
    // Queued, not downloaded — the stream settles each badge (phase 2b).
    for (const track of tracksToDownload) {
      markTrackPending(track.spotify_id || "", track.spotify_id || "");
    }
    setDownloadingTrack(null);
    setCurrentDownloadInfo(null);
    setIsDownloading(false);
    setBulkDownloadType(null);
    shouldStopDownloadRef.current = false;
  };
  const handleDownloadAll = async (
    tracks: TrackMetadata[],
    folderName?: string,
    sourceId?: string,
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
    logger.info(`checking existing files in parallel...`);
    const existenceChecks = buildExistenceCheckRequests(tracksWithId);
    // Directories ignored by the server — it derives them from the settings.
    const existenceResults = await CheckFilesExistence("", "", existenceChecks);
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
      sourceId,
    );
    // Queued, not downloaded — the stream settles each badge (phase 2b).
    for (const track of tracksToDownload) {
      markTrackPending(track.spotify_id || "", track.spotify_id || "");
    }
    setDownloadingTrack(null);
    setCurrentDownloadInfo(null);
    setIsDownloading(false);
    setBulkDownloadType(null);
    shouldStopDownloadRef.current = false;
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
