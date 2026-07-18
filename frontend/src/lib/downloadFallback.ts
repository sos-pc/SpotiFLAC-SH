import { downloadTrack, fetchSpotifyMetadata } from "@/lib/api";
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
  // Nothing settings-derived is computed here any more. The folder template,
  // filename template, quality, embed flags and the first-artist trimming all
  // live on the server (docs/settings-source-of-truth.md — backend-authoritative).
  // The client sends the track's identity, the per-download context (position,
  // playlist) and the one allowed override, `service`. Nothing else.

  if (trackName && artistName) {
    try {
      const checkRequest: FileExistsCheck = {
        spotify_id: spotifyId || id,
        track_name: trackName,
        artist_name: artistName,
        album_name: albumName,
        album_artist: albumArtist,
        release_date: finalReleaseDate || releaseDate,
        track_number: finalTrackNumber || spotifyTrackNumber || 0,
        disc_number: spotifyDiscNumber || 0,
        position: position || 0,
        audio_format: "flac",
      };
      // The directories are ignored by the server (it derives them from the
      // user's settings), and so are filename_format / include_track_number /
      // use_album_track_number — passing empty strings keeps the call honest
      // about who decides.
      const existenceResults = await CheckFilesExistence([checkRequest]);
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

  logger.debug(`enqueue ${service}: ${trackName} - ${artistName}`);
  return await downloadTrack({
    // The one allowed per-download override.
    service,
    // Identity + per-download context only. No output_dir, filename_format,
    // quality or embed flags: the server reads those from the user's settings.
    query,
    track_name: trackName,
    artist_name: artistName,
    album_name: albumName,
    album_artist: albumArtist,
    release_date: finalReleaseDate || releaseDate,
    cover_url: coverUrl,
    playlist_name: playlistName,
    position: position || 0,
    spotify_id: spotifyId,
    duration: durationSeconds,
    spotify_track_number: spotifyTrackNumber,
    spotify_disc_number: spotifyDiscNumber,
    spotify_total_tracks: spotifyTotalTracks,
    spotify_total_discs: spotifyTotalDiscs,
    copyright: copyright,
    publisher: publisher,
  });
}
