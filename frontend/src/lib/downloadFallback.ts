import { downloadTrack, fetchSpotifyMetadata } from "@/lib/api";
import { resolveOutputPath } from "@/lib/utils";
import { logger } from "@/lib/logger";
import { CheckFilesExistence } from "@/lib/rpc";
import type { Settings } from "@/lib/settings";
import type { FileExistsCheck, DownloadResponse } from "@/types/api";

// Parameters for a single-track download.
// Extracted verbatim from useDownload's former inline downloadWithAutoFallback
// closure (R9) — it never touched the hook's state/refs, so it lives here as a
// pure function. The former 18-positional-argument signature is replaced by
// this object to make call sites legible.
export interface AutoFallbackParams {
  region: string;
  id: string;
  settings: Settings;
  trackName?: string;
  artistName?: string;
  albumName?: string;
  playlistName?: string;
  position?: number;
  spotifyId?: string;
  durationMs?: number;
  albumArtist?: string;
  releaseDate?: string;
  coverUrl?: string;
  spotifyTrackNumber?: number;
  spotifyDiscNumber?: number;
  spotifyTotalTracks?: number;
  spotifyTotalDiscs?: number;
  copyright?: string;
  publisher?: string;
}

// downloadWithAutoFallback enqueues one track, honoring settings.downloader.
//
// It no longer iterates providers client-side: that loop couldn't observe a
// real download outcome (POST /downloads/track returns on *enqueue*, not
// completion — see docs/override-rework-plan.md §3.2), so it only ever tried
// the first URL-resolved service and never fell back. The whole selection now
// lives server-side in backend.ExecuteDownload: an explicit service downloads
// only from it; "auto" walks settings.autoOrder and stops at the first success.
// This function just resolves the output path, enriches metadata, short-circuits
// on an already-present file, and enqueues once.
export async function downloadWithAutoFallback(
  p: AutoFallbackParams,
): Promise<DownloadResponse> {
  const {
    id,
    settings,
    trackName,
    artistName,
    albumName,
    playlistName,
    position,
    spotifyId,
    durationMs,
    albumArtist,
    releaseDate,
    coverUrl,
    spotifyTrackNumber,
    spotifyDiscNumber,
    spotifyTotalTracks,
    spotifyTotalDiscs,
    copyright,
    publisher,
  } = p;
  const service = settings.downloader;
  const query =
    trackName && artistName ? `${trackName} ${artistName} ` : undefined;
  let useAlbumTrackNumber = false;
  let finalReleaseDate = releaseDate;
  let finalTrackNumber = spotifyTrackNumber || 0;
  if (spotifyId) {
    try {
      const trackURL = `https://open.spotify.com/track/${spotifyId}`;
      const trackMetadata = await fetchSpotifyMetadata(trackURL, false, 0, 10);
      if ("track" in trackMetadata && trackMetadata.track) {
        if (trackMetadata.track.release_date) {
          finalReleaseDate = trackMetadata.track.release_date;
        }
        if (trackMetadata.track.track_number > 0) {
          finalTrackNumber = trackMetadata.track.track_number;
        }
      }
    } catch {
      // Best-effort enrichment — keep the already-assigned fallback values.
    }
  }
  const hasSubfolder =
    settings.folderTemplate && settings.folderTemplate.trim() !== "";
  const trackNumberForTemplate =
    hasSubfolder && finalTrackNumber > 0 ? finalTrackNumber : position || 0;
  if (hasSubfolder) {
    useAlbumTrackNumber = true;
  }
  // isAlbum: true — a single-track download has no album/playlist page
  // context to distinguish, so this always suppresses the playlist
  // subfolder when the folder template already covers it, matching the
  // pre-consolidation behavior of this call site.
  const { outputDir, displayArtist, displayAlbumArtist } = resolveOutputPath(
    settings,
    {
      artistName,
      albumName,
      albumArtist,
      trackName,
      playlistName,
      trackNumber: trackNumberForTemplate,
      releaseDate: finalReleaseDate || releaseDate,
      isAlbum: true,
    },
  );

  if (trackName && artistName) {
    try {
      const checkRequest: FileExistsCheck = {
        spotify_id: spotifyId || id,
        track_name: trackName,
        artist_name: displayArtist || "",
        album_name: albumName,
        album_artist: displayAlbumArtist,
        release_date: finalReleaseDate || releaseDate,
        track_number: finalTrackNumber || spotifyTrackNumber || 0,
        disc_number: spotifyDiscNumber || 0,
        position: trackNumberForTemplate,
        use_album_track_number: useAlbumTrackNumber,
        filename_format: settings.filenameTemplate || "",
        include_track_number: settings.trackNumber || false,
        audio_format: "flac",
      };
      const existenceResults = await CheckFilesExistence(
        outputDir,
        settings.downloadPath,
        [checkRequest],
      );
      if (existenceResults.length > 0 && existenceResults[0].exists) {
        return {
          success: true,
          message: "File already exists",
          file: existenceResults[0].file_path || "",
          already_exists: true,
        };
      }
    } catch (err) {
      console.warn("File existence check failed:", err);
    }
  }

  const durationSeconds = durationMs
    ? Math.round(durationMs / 1000)
    : undefined;
  // Per-service audio format. For "auto" the backend derives each provider's
  // format from this single value (TidalQualityFor/QobuzQualityFor) while it
  // walks the AutoOrder chain, so one value is enough.
  const is24Bit = (settings.autoQuality || "24") === "24";
  let audioFormat: string | undefined;
  if (service === "tidal") {
    audioFormat = settings.tidalQuality || "LOSSLESS";
  } else if (service === "qobuz") {
    audioFormat = settings.qobuzQuality || "6";
  } else if (service === "deezer") {
    audioFormat = "flac";
  } else if (service === "auto") {
    audioFormat = is24Bit ? "HI_RES_LOSSLESS" : "LOSSLESS";
  }

  logger.debug(`enqueue ${service}: ${trackName} - ${artistName}`);
  return await downloadTrack({
    service,
    // Only "auto" consumes the chain; sending it for an explicit service would
    // change that request's shape for no reason (the backend ignores it there).
    // Without this the backend fell back to its own AutoOrder default, silently
    // ignoring the user's configured chain for single-track downloads.
    auto_order: service === "auto" ? settings.autoOrder : undefined,
    query,
    track_name: trackName,
    artist_name: displayArtist,
    album_name: albumName,
    album_artist: displayAlbumArtist,
    release_date: finalReleaseDate || releaseDate,
    cover_url: coverUrl,
    output_dir: outputDir,
    filename_format: settings.filenameTemplate,
    track_number: settings.trackNumber,
    position: trackNumberForTemplate,
    use_album_track_number: useAlbumTrackNumber,
    spotify_id: spotifyId,
    embed_lyrics: settings.embedLyrics,
    embed_max_quality_cover: settings.embedMaxQualityCover,
    duration: durationSeconds,
    audio_format: audioFormat,
    spotify_track_number: spotifyTrackNumber,
    spotify_disc_number: spotifyDiscNumber,
    spotify_total_tracks: spotifyTotalTracks,
    spotify_total_discs: spotifyTotalDiscs,
    copyright: copyright,
    publisher: publisher,
    use_single_genre: settings.useSingleGenre,
    embed_genre: settings.embedGenre,
  });
}
