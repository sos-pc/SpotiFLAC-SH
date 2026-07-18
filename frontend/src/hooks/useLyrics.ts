import { useState, useRef } from "react";
import { downloadLyrics } from "@/lib/api";
import { toastWithSound as toast } from "@/lib/toast-with-sound";
import { logger } from "@/lib/logger";
import type { TrackMetadata } from "@/types/api";
export function useLyrics() {
    const [downloadingLyricsTrack, setDownloadingLyricsTrack] = useState<string | null>(null);
    const [downloadedLyrics, setDownloadedLyrics] = useState<Set<string>>(new Set());
    const [failedLyrics, setFailedLyrics] = useState<Set<string>>(new Set());
    const [skippedLyrics, setSkippedLyrics] = useState<Set<string>>(new Set());
    const [isBulkDownloadingLyrics, setIsBulkDownloadingLyrics] = useState(false);
    const [lyricsDownloadProgress, setLyricsDownloadProgress] = useState(0);
    const stopBulkDownloadRef = useRef(false);
    const handleDownloadLyrics = async (spotifyId: string, trackName: string, artistName: string, albumName?: string, playlistName?: string, position?: number, albumArtist?: string, releaseDate?: string, discNumber?: number) => {
        if (!spotifyId) {
            toast.error("No Spotify ID found for this track");
            return;
        }
        logger.info(`downloading lyrics: ${trackName} - ${artistName}`);
        setDownloadingLyricsTrack(spotifyId);
        try {
            // Identity + context only — the server decides where the .lrc goes
            // and what it is called (docs/settings-source-of-truth.md D4).
            const response = await downloadLyrics({
                spotify_id: spotifyId,
                track_name: trackName,
                artist_name: artistName,
                album_name: albumName,
                album_artist: albumArtist,
                release_date: releaseDate,
                playlist_name: playlistName,
                position: position || 0,
                disc_number: discNumber,
            });
            if (response.success) {
                if (response.already_exists) {
                    toast.info("Lyrics file already exists");
                    setSkippedLyrics((prev) => new Set(prev).add(spotifyId));
                }
                else {
                    toast.success("Lyrics downloaded successfully");
                    setDownloadedLyrics((prev) => new Set(prev).add(spotifyId));
                }
                setFailedLyrics((prev) => {
                    const newSet = new Set(prev);
                    newSet.delete(spotifyId);
                    return newSet;
                });
            }
            else {
                toast.error(response.error || "Failed to download lyrics");
                setFailedLyrics((prev) => new Set(prev).add(spotifyId));
            }
        }
        catch (err) {
            toast.error(err instanceof Error ? err.message : "Failed to download lyrics");
            setFailedLyrics((prev) => new Set(prev).add(spotifyId));
        }
        finally {
            setDownloadingLyricsTrack(null);
        }
    };
    const handleDownloadAllLyrics = async (tracks: TrackMetadata[], playlistName?: string, _isArtistDiscography?: boolean) => {
        const tracksWithSpotifyId = tracks.filter((track) => track.spotify_id);
        if (tracksWithSpotifyId.length === 0) {
            toast.error("No tracks with Spotify ID available for lyrics download");
            return;
        }
        setIsBulkDownloadingLyrics(true);
        setLyricsDownloadProgress(0);
        stopBulkDownloadRef.current = false;
        let completed = 0;
        let success = 0;
        let failed = 0;
        let skipped = 0;
        const total = tracksWithSpotifyId.length;
        for (let i = 0; i < tracksWithSpotifyId.length; i++) {
            const track = tracksWithSpotifyId[i];
            if (stopBulkDownloadRef.current) {
                toast.info("Lyrics download stopped by user");
                break;
            }
            const id = track.spotify_id!;
            setDownloadingLyricsTrack(id);
            setLyricsDownloadProgress(Math.round((completed / total) * 100));
            try {
                const response = await downloadLyrics({
                    spotify_id: id,
                    track_name: track.name,
                    artist_name: track.artists,
                    album_name: track.album_name,
                    album_artist: track.album_artist,
                    release_date: track.release_date,
                    playlist_name: playlistName,
                    // Both raw: the server chooses which to use.
                    position: i + 1,
                    album_track_number: track.track_number,
                    disc_number: track.disc_number,
                });
                if (response.success) {
                    if (response.already_exists) {
                        skipped++;
                        setSkippedLyrics((prev) => new Set(prev).add(id));
                    }
                    else {
                        success++;
                        setDownloadedLyrics((prev) => new Set(prev).add(id));
                    }
                    setFailedLyrics((prev) => {
                        const newSet = new Set(prev);
                        newSet.delete(id);
                        return newSet;
                    });
                }
                else {
                    failed++;
                    setFailedLyrics((prev) => new Set(prev).add(id));
                }
            }
            catch (err) {
                failed++;
                logger.error(`error downloading lyrics: ${track.name} - ${err}`);
                setFailedLyrics((prev) => new Set(prev).add(id));
            }
            completed++;
        }
        setDownloadingLyricsTrack(null);
        setIsBulkDownloadingLyrics(false);
        setLyricsDownloadProgress(0);
        if (!stopBulkDownloadRef.current) {
            toast.success(`Lyrics: ${success} downloaded, ${skipped} skipped, ${failed} failed`);
        }
    };
    const handleStopLyricsDownload = () => {
        logger.info("lyrics download stopped by user");
        stopBulkDownloadRef.current = true;
        toast.info("Stopping lyrics download...");
    };
    const resetLyricsState = () => {
        setDownloadedLyrics(new Set());
        setFailedLyrics(new Set());
        setSkippedLyrics(new Set());
    };
    return {
        downloadingLyricsTrack,
        downloadedLyrics,
        failedLyrics,
        skippedLyrics,
        isBulkDownloadingLyrics,
        lyricsDownloadProgress,
        handleDownloadLyrics,
        handleDownloadAllLyrics,
        handleStopLyricsDownload,
        resetLyricsState,
    };
}
