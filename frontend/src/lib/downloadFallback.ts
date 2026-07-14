import { downloadTrack, fetchSpotifyMetadata } from "@/lib/api";
import { resolveOutputPath } from "@/lib/utils";
import { logger } from "@/lib/logger";
import { CheckFilesExistence, GetStreamingURLs } from "@/lib/rpc";
import type { Settings } from "@/lib/settings";
import type {
  FileExistsCheck,
  TrackAvailability,
  DownloadResponse,
} from "@/types/api";

// Parameters for a single-track download with automatic provider fallback.
// Extracted verbatim from useDownload's former inline downloadWithAutoFallback
// closure (R9) — it never touched the hook's state/refs, only `region`, so it
// lives here as a pure function. The former 18-positional-argument signature is
// replaced by this object to make call sites legible.
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

// downloadWithAutoFallback downloads one track, honoring settings.downloader:
// "auto" walks settings.autoOrder (tidal→amazon→qobuz→deezer), returning the
// first success; a specific service downloads only from it. Returns early with
// already_exists when CheckFilesExistence finds the file already on disk.
export async function downloadWithAutoFallback(
  p: AutoFallbackParams,
): Promise<DownloadResponse> {
  const {
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
  const serviceForCheck =
    service === "auto"
      ? "flac"
      : service === "tidal"
        ? "flac"
        : service === "qobuz"
          ? "flac"
          : "flac";

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
        audio_format: serviceForCheck,
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
  if (service === "auto") {
    let streamingURLs: TrackAvailability | null = null;
    if (spotifyId) {
      try {
        streamingURLs = await GetStreamingURLs(spotifyId, region);
      } catch (err) {
        console.error("Failed to get streaming URLs:", err);
      }
    }
    const durationSeconds = durationMs
      ? Math.round(durationMs / 1000)
      : undefined;
    const order = (settings.autoOrder || "tidal-amazon-qobuz").split("-");
    let lastResponse: DownloadResponse = {
      success: false,
      message: "No matching services found",
      error: "No matching services found",
    };
    const fallbackErrors: string[] = [];
    const is24Bit = (settings.autoQuality || "24") === "24";
    const tidalQuality = is24Bit ? "HI_RES_LOSSLESS" : "LOSSLESS";
    const qobuzQuality = is24Bit ? "27" : "6";
    for (const s of order) {
      if (s === "tidal" && streamingURLs?.tidal_url) {
        try {
          logger.debug(`trying tidal for: ${trackName} - ${artistName}`);
          const response = await downloadTrack({
            service: "tidal",
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
            position,
            use_album_track_number: useAlbumTrackNumber,
            spotify_id: spotifyId,
            embed_lyrics: settings.embedLyrics,
            embed_max_quality_cover: settings.embedMaxQualityCover,
            service_url: streamingURLs.tidal_url,
            duration: durationSeconds,
            audio_format: tidalQuality,
            spotify_track_number: spotifyTrackNumber,
            spotify_disc_number: spotifyDiscNumber,
            spotify_total_tracks: spotifyTotalTracks,
            spotify_total_discs: spotifyTotalDiscs,
            copyright: copyright,
            publisher: publisher,
            use_first_artist_only: settings.useFirstArtistOnly,
            use_single_genre: settings.useSingleGenre,
            embed_genre: settings.embedGenre,
          });
          if (response.success) {
            logger.success(`tidal: ${trackName} - ${artistName}`);
            return response;
          }
          const errMsg = response.error || response.message || "Failed";
          fallbackErrors.push(`[Tidal] ${errMsg}`);
          lastResponse = response;
          logger.warning(`tidal failed, trying next...`);
        } catch (err) {
          logger.error(`tidal error: ${err}`);
          fallbackErrors.push(`[Tidal] ${String(err)}`);
          lastResponse = { success: false, message: String(err), error: String(err) };
        }
      } else if (s === "amazon" && streamingURLs?.amazon_url) {
        try {
          logger.debug(`trying amazon for: ${trackName} - ${artistName}`);
          const response = await downloadTrack({
            service: "amazon",
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
            position,
            use_album_track_number: useAlbumTrackNumber,
            spotify_id: spotifyId,
            embed_lyrics: settings.embedLyrics,
            embed_max_quality_cover: settings.embedMaxQualityCover,
            service_url: streamingURLs.amazon_url,
            spotify_track_number: spotifyTrackNumber,
            spotify_disc_number: spotifyDiscNumber,
            spotify_total_tracks: spotifyTotalTracks,
            spotify_total_discs: spotifyTotalDiscs,
            copyright: copyright,
            publisher: publisher,
            use_single_genre: settings.useSingleGenre,
            embed_genre: settings.embedGenre,
          });
          if (response.success) {
            logger.success(`amazon: ${trackName} - ${artistName}`);
            return response;
          }
          const errMsg = response.error || response.message || "Failed";
          fallbackErrors.push(`[Amazon] ${errMsg}`);
          lastResponse = response;
          logger.warning(`amazon failed, trying next...`);
        } catch (err) {
          logger.error(`amazon error: ${err}`);
          fallbackErrors.push(`[Amazon] ${String(err)}`);
          lastResponse = { success: false, message: String(err), error: String(err) };
        }
      } else if (s === "qobuz") {
        try {
          logger.debug(`trying qobuz for: ${trackName} - ${artistName}`);
          const response = await downloadTrack({
            service: "qobuz",
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
            audio_format: qobuzQuality,
            spotify_track_number: spotifyTrackNumber,
            spotify_disc_number: spotifyDiscNumber,
            spotify_total_tracks: spotifyTotalTracks,
            spotify_total_discs: spotifyTotalDiscs,
            copyright: copyright,
            publisher: publisher,
            use_single_genre: settings.useSingleGenre,
            embed_genre: settings.embedGenre,
          });
          if (response.success) {
            logger.success(`qobuz: ${trackName} - ${artistName}`);
            return response;
          }
          const errMsg = response.error || response.message || "Failed";
          fallbackErrors.push(`[Qobuz] ${errMsg}`);
          lastResponse = response;
          logger.warning(`qobuz failed, trying next...`);
        } catch (err) {
          logger.error(`qobuz error: ${err}`);
          fallbackErrors.push(`[Qobuz] ${String(err)}`);
          lastResponse = { success: false, message: String(err), error: String(err) };
        }
      } else if (s === "deezer") {
        try {
          logger.debug(`trying deezer for: ${trackName} - ${artistName}`);
          const response = await downloadTrack({
            service: "deezer",
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
            position,
            use_album_track_number: useAlbumTrackNumber,
            spotify_id: spotifyId,
            embed_lyrics: settings.embedLyrics,
            embed_max_quality_cover: settings.embedMaxQualityCover,
            duration: durationSeconds,
            audio_format: "flac",
            spotify_track_number: spotifyTrackNumber,
            spotify_disc_number: spotifyDiscNumber,
            spotify_total_tracks: spotifyTotalTracks,
            spotify_total_discs: spotifyTotalDiscs,
            copyright: copyright,
            publisher: publisher,
            use_first_artist_only: settings.useFirstArtistOnly,
            use_single_genre: settings.useSingleGenre,
            embed_genre: settings.embedGenre,
          });
          if (response.success) {
            logger.success(`deezer: ${trackName} - ${artistName}`);
            return response;
          }
          const errMsg = response.error || response.message || "Failed";
          fallbackErrors.push(`[Deezer] ${errMsg}`);
          lastResponse = response;
          logger.warning(`deezer failed, trying next...`);
        } catch (err) {
          logger.error(`deezer error: ${err}`);
          fallbackErrors.push(`[Deezer] ${String(err)}`);
          lastResponse = { success: false, message: String(err), error: String(err) };
        }
      }
    }
    return lastResponse;
  }
  const durationSecondsForFallback = durationMs
    ? Math.round(durationMs / 1000)
    : undefined;
  let audioFormat: string | undefined;
  if (service === "tidal") {
    audioFormat = settings.tidalQuality || "LOSSLESS";
  } else if (service === "qobuz") {
    audioFormat = settings.qobuzQuality || "6";
  } else if (service === "deezer") {
    audioFormat = "flac";
  }
  logger.debug(`trying ${service} for: ${trackName} - ${artistName}`);
  const singleServiceResponse = await downloadTrack({
    service: service as "tidal" | "qobuz" | "amazon" | "deezer",
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
    duration: durationSecondsForFallback,
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
  return singleServiceResponse;
}
