import { useState, useRef, useEffect } from "react";
import { downloadTrack, fetchSpotifyMetadata } from "@/lib/api";
import { getSettings, parseTemplate, type TemplateData } from "@/lib/settings";
import { toastWithSound as toast } from "@/lib/toast-with-sound";
import { joinPath, sanitizePath, getFirstArtist } from "@/lib/utils";
import { logger } from "@/lib/logger";
import { getToken } from "@/lib/auth";
import type { TrackMetadata } from "@/types/api";
interface CheckFileExistenceRequest {
    spotify_id: string;
    track_name: string;
    artist_name: string;
    album_name?: string;
    album_artist?: string;
    release_date?: string;
    track_number?: number;
    disc_number?: number;
    position?: number;
    use_album_track_number?: boolean;
    filename_format?: string;
    include_track_number?: boolean;
    audio_format?: string;
    relative_path?: string;
}
import { CheckFilesExistence, CreateM3U8File, EnqueueBatch, AddToDownloadQueue, GetStreamingURLs, MarkDownloadItemFailed } from "@/lib/rpc";
export function useDownload(region: string) {
    const [downloadProgress, setDownloadProgress] = useState<number>(0);
    const [isDownloading, setIsDownloading] = useState(false);
    const [downloadingTrack, setDownloadingTrack] = useState<string | null>(null);
    const [bulkDownloadType, setBulkDownloadType] = useState<"all" | "selected" | null>(null);
    const [downloadedTracks, setDownloadedTracks] = useState<Set<string>>(new Set());
    const [failedTracks, setFailedTracks] = useState<Set<string>>(new Set());
    const [skippedTracks, setSkippedTracks] = useState<Set<string>>(new Set());
    const [currentDownloadInfo, setCurrentDownloadInfo] = useState<{
        name: string;
        artists: string;
    } | null>(null);
    const shouldStopDownloadRef = useRef(false);

    // ── Browser download mode ─────────────────────────────────────────────────
    const [downloadMode, setDownloadModeInternal] = useState<"server" | "browser">(() =>
        (localStorage.getItem("download_mode") as "server" | "browser") || "server"
    );
    const browserBatchIdsRef = useRef<Set<string>>(new Set());
    const triggeredJobIdsRef = useRef<Set<string>>(new Set());

    // Sync with DownloadModeToggle dispatching "spotif:downloadModeChange"
    useEffect(() => {
        const handler = (e: Event) => {
            setDownloadModeInternal((e as CustomEvent<"server" | "browser">).detail);
        };
        window.addEventListener("spotif:downloadModeChange", handler);
        return () => window.removeEventListener("spotif:downloadModeChange", handler);
    }, []);

    // SSE listener for browser-mode auto-download
    useEffect(() => {
        if (downloadMode !== "browser") return;
        const token = getToken();
        if (!token) return;

        const es = new EventSource(`/api/v1/jobs/stream?token=${encodeURIComponent(token)}`);

        es.addEventListener("job_update", (e: MessageEvent) => {
            const job = JSON.parse(e.data) as { id: string; status: string; batch_id?: string; file_path?: string };
            if (
                job.status === "done" &&
                job.batch_id &&
                browserBatchIdsRef.current.has(job.batch_id) &&
                job.file_path &&
                !triggeredJobIdsRef.current.has(job.id)
            ) {
                triggeredJobIdsRef.current.add(job.id);
                const t = getToken();
                if (!t) return;
                const a = document.createElement("a");
                a.href = `/api/v1/jobs/${job.id}/download?token=${encodeURIComponent(t)}`;
                a.download = "";
                document.body.appendChild(a);
                a.click();
                document.body.removeChild(a);
            }
        });

        es.onerror = () => es.close();

        return () => es.close();
    }, [downloadMode]);
    const downloadWithAutoFallback = async (id: string, settings: any, trackName?: string, artistName?: string, albumName?: string, playlistName?: string, position?: number, spotifyId?: string, durationMs?: number, releaseYear?: string, albumArtist?: string, releaseDate?: string, coverUrl?: string, spotifyTrackNumber?: number, spotifyDiscNumber?: number, spotifyTotalTracks?: number, spotifyTotalDiscs?: number, copyright?: string, publisher?: string) => {
        const service = settings.downloader;
        const query = trackName && artistName ? `${trackName} ${artistName} ` : undefined;
        const os = settings.operatingSystem;
        let outputDir = settings.downloadPath;
        let useAlbumTrackNumber = false;
        const placeholder = "__SLASH_PLACEHOLDER__";
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
            }
            catch (err) {
            }
        }
        const yearValue = releaseYear || finalReleaseDate?.substring(0, 4);
        const hasSubfolder = settings.folderTemplate && settings.folderTemplate.trim() !== "";
        const trackNumberForTemplate = (hasSubfolder && finalTrackNumber > 0) ? finalTrackNumber : (position || 0);
        if (hasSubfolder) {
            useAlbumTrackNumber = true;
        }
        const displayArtist = settings.useFirstArtistOnly && artistName
            ? getFirstArtist(artistName)
            : artistName;
        const displayAlbumArtist = settings.useFirstArtistOnly && albumArtist
            ? getFirstArtist(albumArtist)
            : albumArtist;
        const templateData: TemplateData = {
            artist: displayArtist?.replace(/\//g, placeholder),
            album: albumName?.replace(/\//g, placeholder),
            album_artist: displayAlbumArtist?.replace(/\//g, placeholder) || displayArtist?.replace(/\//g, placeholder),
            title: trackName?.replace(/\//g, placeholder),
            track: trackNumberForTemplate,
            year: yearValue,
            date: releaseDate,
            playlist: playlistName?.replace(/\//g, placeholder),
        };
        const folderTemplate = settings.folderTemplate || "";
        const useAlbumSubfolder = folderTemplate.includes("{album}") || folderTemplate.includes("{album_artist}") || folderTemplate.includes("{playlist}");
        if (settings.createPlaylistFolder && playlistName && !useAlbumSubfolder) {
            outputDir = joinPath(os, outputDir, sanitizePath(playlistName.replace(/\//g, " "), os));
        }
        if (settings.folderTemplate) {
            const folderPath = parseTemplate(settings.folderTemplate, templateData);
            if (folderPath) {
                const parts = folderPath.split("/").filter((p: string) => p.trim());
                for (const part of parts) {
                    const sanitizedPart = part.replace(new RegExp(placeholder, "g"), " ");
                    outputDir = joinPath(os, outputDir, sanitizePath(sanitizedPart, os));
                }
            }
        }
        const serviceForCheck = service === "auto" ? "flac" : (service === "tidal" ? "flac" : (service === "qobuz" ? "flac" : "flac"));
        let fileExists = false;
        if (trackName && artistName) {
            try {
                const checkRequest: CheckFileExistenceRequest = {
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
                const existenceResults = await CheckFilesExistence(outputDir, settings.downloadPath, [checkRequest]);
                if (existenceResults.length > 0 && existenceResults[0].exists) {
                    fileExists = true;
                    return {
                        success: true,
                        message: "File already exists",
                        file: existenceResults[0].file_path || "",
                        already_exists: true,
                    };
                }
            }
            catch (err) {
                console.warn("File existence check failed:", err);
            }
        }
        // AddToDownloadQueue from rpc.ts
        let itemID: string | undefined;
        if (!fileExists) {
            itemID = await AddToDownloadQueue(id, trackName || "", displayArtist || "", albumName || "");
        }
        if (service === "auto") {
            let streamingURLs: any = null;
            if (spotifyId) {
                try {
                    streamingURLs = await GetStreamingURLs(spotifyId, region);
                }
                catch (err) {
                    console.error("Failed to get streaming URLs:", err);
                }
            }
            const durationSeconds = durationMs ? Math.round(durationMs / 1000) : undefined;
            const order = (settings.autoOrder || "tidal-amazon-qobuz").split("-");
            let lastResponse: any = { success: false, error: "No matching services found" };
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
                            item_id: itemID,
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
                    }
                    catch (err) {
                        logger.error(`tidal error: ${err}`);
                        fallbackErrors.push(`[Tidal] ${String(err)}`);
                        lastResponse = { success: false, error: String(err) };
                    }
                }
                else if (s === "amazon" && streamingURLs?.amazon_url) {
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
                            item_id: itemID,
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
                    }
                    catch (err) {
                        logger.error(`amazon error: ${err}`);
                        fallbackErrors.push(`[Amazon] ${String(err)}`);
                        lastResponse = { success: false, error: String(err) };
                    }
                }
                else if (s === "qobuz") {
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
                            item_id: itemID,
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
                    }
                    catch (err) {
                        logger.error(`qobuz error: ${err}`);
                        fallbackErrors.push(`[Qobuz] ${String(err)}`);
                        lastResponse = { success: false, error: String(err) };
                    }
                }
                else if (s === "deezer") {
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
                            item_id: itemID,
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
                    }
                    catch (err) {
                        logger.error(`deezer error: ${err}`);
                        fallbackErrors.push(`[Deezer] ${String(err)}`);
                        lastResponse = { success: false, error: String(err) };
                    }
                }
            }
            if (itemID) {
                // MarkDownloadItemFailed from rpc.ts
                const finalError = fallbackErrors.length > 0 ? fallbackErrors.join(" | ") : (lastResponse.error || "All services failed");
                await MarkDownloadItemFailed(itemID, finalError);
            }
            return lastResponse;
        }
        const durationSecondsForFallback = durationMs ? Math.round(durationMs / 1000) : undefined;
        let audioFormat: string | undefined;
        if (service === "tidal") {
            audioFormat = settings.tidalQuality || "LOSSLESS";
        }
        else if (service === "qobuz") {
            audioFormat = settings.qobuzQuality || "6";
        }
        else if (service === "deezer") {
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
            item_id: itemID,
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
        if (!singleServiceResponse.success && itemID) {
            // MarkDownloadItemFailed from rpc.ts
            await MarkDownloadItemFailed(itemID, singleServiceResponse.error || "Download failed");
        }
        return singleServiceResponse;
    };
    const handleDownloadTrack = async (id: string, trackName?: string, artistName?: string, albumName?: string, spotifyId?: string, playlistName?: string, durationMs?: number, position?: number, albumArtist?: string, releaseDate?: string, coverUrl?: string, spotifyTrackNumber?: number, spotifyDiscNumber?: number, spotifyTotalTracks?: number, spotifyTotalDiscs?: number, copyright?: string, publisher?: string) => {
        if (!id) {
            toast.error("No ID found for this track");
            return;
        }
        const settings = getSettings();
        const displayArtist = settings.useFirstArtistOnly && artistName ? getFirstArtist(artistName) : artistName;
        logger.info(`starting download: ${trackName} - ${displayArtist}`);
        setDownloadingTrack(id);
        try {
            const releaseYear = releaseDate?.substring(0, 4);
            const response = await downloadWithAutoFallback(id, settings, trackName, artistName, albumName, playlistName, position, spotifyId, durationMs, releaseYear, albumArtist || "", releaseDate, coverUrl, spotifyTrackNumber, spotifyDiscNumber, spotifyTotalTracks, spotifyTotalDiscs, copyright, publisher);
            if (response.success) {
                if (response.already_exists) {
                    toast.info(response.message);
                    setSkippedTracks((prev) => new Set(prev).add(id));
                }
                else {
                    toast.success(response.message);
                }
                setDownloadedTracks((prev) => new Set(prev).add(id));
                setFailedTracks((prev) => {
                    const newSet = new Set(prev);
                    newSet.delete(id);
                    return newSet;
                });
            }
            else {
                toast.error(response.error || "Download failed");
                setFailedTracks((prev) => new Set(prev).add(id));
            }
        }
        catch (err) {
            toast.error(err instanceof Error ? err.message : "Download failed");
            setFailedTracks((prev) => new Set(prev).add(id));
        }
        finally {
            setDownloadingTrack(null);
        }
    };
    const handleDownloadSelected = async (selectedTracks: string[], allTracks: TrackMetadata[], folderName?: string, isAlbum?: boolean) => {
        if (selectedTracks.length === 0) {
            toast.error("No tracks selected");
            return;
        }
        logger.info(`starting batch download: ${selectedTracks.length} selected tracks`);
        const settings = getSettings();
        setIsDownloading(true);
        setBulkDownloadType("selected");
        setDownloadProgress(0);
        let outputDir = settings.downloadPath;
        const os = settings.operatingSystem;
        const useAlbumTag = settings.folderTemplate?.includes("{album}");
        if (settings.createPlaylistFolder && folderName && (!isAlbum || !useAlbumTag)) {
            outputDir = joinPath(os, outputDir, sanitizePath(folderName.replace(/\//g, " "), os));
        }
        const selectedTrackObjects = selectedTracks
            .map((id) => allTracks.find((t) => t.spotify_id === id))
            .filter((t): t is TrackMetadata => t !== undefined);
        logger.info(`checking existing files in parallel...`);
        const useAlbumTrackNumber = settings.folderTemplate?.includes("{album}") || false;
        const audioFormat = "flac";
        const existenceChecks = selectedTrackObjects.map((track) => {
            const displayArtist = settings.useFirstArtistOnly && track.artists ? getFirstArtist(track.artists) : track.artists;
            const displayAlbumArtist = settings.useFirstArtistOnly && track.album_artist ? getFirstArtist(track.album_artist) : track.album_artist;
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
                audio_format: audioFormat,
            };
        });
        const existenceResults = await CheckFilesExistence(outputDir, settings.downloadPath, existenceChecks);
        const existingSpotifyIDs = new Set<string>();
        const existingFilePaths = new Map<string, string>();
        const finalFilePaths = new Map<string, string>();
        for (const result of existenceResults) {
            if (result.exists) {
                existingSpotifyIDs.add(result.spotify_id);
                existingFilePaths.set(result.spotify_id, result.file_path || "");
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
        if (tracksToDownload.length > 0) {
            const is24Bit = (settings.autoQuality || "24") === "24";
            const jobTracks = tracksToDownload.map((track) => {
                const displayArtist = settings.useFirstArtistOnly && track.artists ? getFirstArtist(track.artists) : track.artists;
                const displayAlbumArtist = settings.useFirstArtistOnly && track.album_artist ? getFirstArtist(track.album_artist) : track.album_artist;
                const originalIndex = selectedTrackObjects.indexOf(track);
                return {
                    spotify_id:   track.spotify_id || "",
                    track_name:   track.name || "",
                    artist_name:  displayArtist || "",
                    album_name:   track.album_name || "",
                    album_artist: displayAlbumArtist || track.album_artist || "",
                    release_date: track.release_date || "",
                    cover_url:    track.images || "",
                    track_number: track.track_number || 0,
                    disc_number:  track.disc_number || 0,
                    total_tracks: track.total_tracks || 0,
                    total_discs:  track.total_discs || 0,
                    copyright:    track.copyright || "",
                    publisher:    track.publisher || "",
                    duration_ms:  track.duration_ms || 0,
                    position:     originalIndex + 1,
                    playlist_name: folderName || "",
                };
            });
            const jobSettings = {
                service:              settings.downloader || "tidal",
                downloadPath:         settings.downloadPath,
                filenameTemplate:     settings.filenameTemplate || "",
                folderTemplate:       settings.folderTemplate || "",
                trackNumber:          settings.trackNumber ?? true,
                embedLyrics:          settings.embedLyrics ?? false,
                embedMaxQualityCover: settings.embedMaxQualityCover ?? false,
                tidalQuality:         is24Bit ? "HI_RES_LOSSLESS" : "LOSSLESS",
                qobuzQuality:         is24Bit ? "27" : "6",
                autoOrder:            settings.autoOrder || "tidal-amazon-qobuz",
                autoQuality:          settings.autoQuality || "24",
                useFirstArtistOnly:   settings.useFirstArtistOnly ?? false,
                useSingleGenre:       settings.useSingleGenre ?? false,
                embedGenre:           settings.embedGenre ?? false,
                createPlaylistFolder: settings.createPlaylistFolder ?? false,
            };
            try {
                const resp = await EnqueueBatch({ tracks: jobTracks, settings: jobSettings });
                logger.success(`[EnqueueBatch] ${resp.enqueued} tracks queued, ${resp.skipped} skipped`);
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
        } else {
            toast.info(`${skippedCount} tracks already exist`);
        }
        setDownloadingTrack(null);
        setCurrentDownloadInfo(null);
        setIsDownloading(false);
        setBulkDownloadType(null);
        shouldStopDownloadRef.current = false;
        if (settings.createM3u8File && folderName) {
            const paths = selectedTrackObjects.map((t) => finalFilePaths.get(t.spotify_id || "") || "").filter((p) => p !== "");
            if (paths.length > 0) {
                try {
                    logger.info(`creating m3u8 playlist: ${folderName}`);
                    await CreateM3U8File(folderName, outputDir, paths, settings.jellyfinMusicPath || "");
                    toast.success("M3U8 playlist created");
                }
                catch (err) {
                    logger.error(`failed to create m3u8 playlist: ${err}`);
                    toast.error(`Failed to create M3U8 playlist: ${err}`);
                }
            }
        }
    };
    const handleDownloadAll = async (tracks: TrackMetadata[], folderName?: string, isAlbum?: boolean) => {
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
        let outputDir = settings.downloadPath;
        const os = settings.operatingSystem;
        const useAlbumTag = settings.folderTemplate?.includes("{album}");
        if (settings.createPlaylistFolder && folderName && (!isAlbum || !useAlbumTag)) {
            outputDir = joinPath(os, outputDir, sanitizePath(folderName.replace(/\//g, " "), os));
        }
        logger.info(`checking existing files in parallel...`);
        const useAlbumTrackNumber = settings.folderTemplate?.includes("{album}") || false;
        const audioFormat = "flac";
        const existenceChecks = tracksWithId.map((track) => {
            const displayArtist = settings.useFirstArtistOnly && track.artists ? getFirstArtist(track.artists) : track.artists;
            const displayAlbumArtist = settings.useFirstArtistOnly && track.album_artist ? getFirstArtist(track.album_artist) : track.album_artist;
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
                audio_format: audioFormat,
            };
        });
        const existenceResults = await CheckFilesExistence(outputDir, settings.downloadPath, existenceChecks);
        const finalFilePaths: string[] = new Array(tracksWithId.length).fill("");
        const existingSpotifyIDs = new Set<string>();
        const existingFilePaths = new Map<string, string>();
        for (let i = 0; i < existenceResults.length; i++) {
            const result = existenceResults[i];
            if (result.exists) {
                existingSpotifyIDs.add(result.spotify_id);
                existingFilePaths.set(result.spotify_id, result.file_path || "");
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
        if (tracksToDownload.length > 0) {
            const is24Bit = (settings.autoQuality || "24") === "24";
            const jobTracks = tracksToDownload.map((track) => {
                const displayArtist = settings.useFirstArtistOnly && track.artists ? getFirstArtist(track.artists) : track.artists;
                const displayAlbumArtist = settings.useFirstArtistOnly && track.album_artist ? getFirstArtist(track.album_artist) : track.album_artist;
                const originalIndex = tracksWithId.findIndex((t) => t.spotify_id === track.spotify_id);
                return {
                    spotify_id:   track.spotify_id || "",
                    track_name:   track.name || "",
                    artist_name:  displayArtist || "",
                    album_name:   track.album_name || "",
                    album_artist: displayAlbumArtist || track.album_artist || "",
                    release_date: track.release_date || "",
                    cover_url:    track.images || "",
                    track_number: track.track_number || 0,
                    disc_number:  track.disc_number || 0,
                    total_tracks: track.total_tracks || 0,
                    total_discs:  track.total_discs || 0,
                    copyright:    track.copyright || "",
                    publisher:    track.publisher || "",
                    duration_ms:  track.duration_ms || 0,
                    position:     originalIndex + 1,
                    playlist_name: folderName || "",
                };
            });
            const jobSettings = {
                service:              settings.downloader || "tidal",
                downloadPath:         settings.downloadPath,
                filenameTemplate:     settings.filenameTemplate || "",
                folderTemplate:       settings.folderTemplate || "",
                trackNumber:          settings.trackNumber ?? true,
                embedLyrics:          settings.embedLyrics ?? false,
                embedMaxQualityCover: settings.embedMaxQualityCover ?? false,
                tidalQuality:         is24Bit ? "HI_RES_LOSSLESS" : "LOSSLESS",
                qobuzQuality:         is24Bit ? "27" : "6",
                autoOrder:            settings.autoOrder || "tidal-amazon-qobuz",
                autoQuality:          settings.autoQuality || "24",
                useFirstArtistOnly:   settings.useFirstArtistOnly ?? false,
                useSingleGenre:       settings.useSingleGenre ?? false,
                embedGenre:           settings.embedGenre ?? false,
                createPlaylistFolder: settings.createPlaylistFolder ?? false,
            };
            try {
                const resp = await EnqueueBatch({ tracks: jobTracks, settings: jobSettings });
                logger.success(`[EnqueueBatch] ${resp.enqueued} tracks queued, ${resp.skipped} skipped`);
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
        } else {
            toast.info(`${skippedCount} tracks already exist`);
        }
        setDownloadingTrack(null);
        setCurrentDownloadInfo(null);
        setIsDownloading(false);
        setBulkDownloadType(null);
        shouldStopDownloadRef.current = false;
        if (settings.createM3u8File && folderName) {
            try {
                logger.info(`creating m3u8 playlist: ${folderName}`);
                await CreateM3U8File(folderName, outputDir, finalFilePaths.filter(p => p !== ""), settings.jellyfinMusicPath || "");
                toast.success("M3U8 playlist created");
            }
            catch (err) {
                logger.error(`failed to create m3u8 playlist: ${err}`);
                toast.error(`Failed to create M3U8 playlist: ${err}`);
            }
        }
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
