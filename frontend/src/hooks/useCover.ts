import { useState, useRef } from "react";
import { downloadCover } from "@/lib/api";
import { toastWithSound as toast } from "@/lib/toast-with-sound";
import { logger } from "@/lib/logger";
import type { TrackMetadata } from "@/types/api";
export function useCover() {
    const [downloadingCover, setDownloadingCover] = useState(false);
    const [downloadingCoverTrack, setDownloadingCoverTrack] = useState<string | null>(null);
    const [downloadedCovers, setDownloadedCovers] = useState<Set<string>>(new Set());
    const [failedCovers, setFailedCovers] = useState<Set<string>>(new Set());
    const [skippedCovers, setSkippedCovers] = useState<Set<string>>(new Set());
    const [isBulkDownloadingCovers, setIsBulkDownloadingCovers] = useState(false);
    const [coverDownloadProgress, setCoverDownloadProgress] = useState(0);
    const stopBulkDownloadRef = useRef(false);
    const handleDownloadCover = async (coverUrl: string, trackName: string, artistName: string, albumName?: string, playlistName?: string, position?: number, trackId?: string, albumArtist?: string, releaseDate?: string, discNumber?: number) => {
        if (!coverUrl) {
            toast.error("No cover URL found for this track");
            return;
        }
        const id = trackId || `${trackName}-${artistName}`;
        logger.info(`downloading cover: ${trackName} - ${artistName}`);
        setDownloadingCover(true);
        setDownloadingCoverTrack(id);
        try {
            // Identity + context only. Where the cover lands and what it is
            // called are the server's call (docs/settings-source-of-truth.md D4),
            // so it sits beside the audio file rather than wherever a second,
            // slightly different client-side rule would have put it.
            const response = await downloadCover({
                cover_url: coverUrl,
                track_name: trackName,
                artist_name: artistName,
                album_name: albumName || "",
                album_artist: albumArtist || "",
                release_date: releaseDate || "",
                playlist_name: playlistName,
                position: position || 0,
                disc_number: discNumber || 0,
            });
            if (response.success) {
                if (response.already_exists) {
                    toast.info("Cover file already exists");
                    setSkippedCovers((prev) => new Set(prev).add(id));
                }
                else {
                    toast.success("Cover downloaded successfully");
                    setDownloadedCovers((prev) => new Set(prev).add(id));
                }
                setFailedCovers((prev) => {
                    const newSet = new Set(prev);
                    newSet.delete(id);
                    return newSet;
                });
            }
            else {
                toast.error(response.error || "Failed to download cover");
                setFailedCovers((prev) => new Set(prev).add(id));
            }
        }
        catch (err) {
            toast.error(err instanceof Error ? err.message : "Failed to download cover");
            setFailedCovers((prev) => new Set(prev).add(id));
        }
        finally {
            setDownloadingCover(false);
            setDownloadingCoverTrack(null);
        }
    };
    const handleDownloadAllCovers = async (tracks: TrackMetadata[], playlistName?: string) => {
        if (tracks.length === 0) {
            toast.error("No tracks to download covers");
            return;
        }
        setIsBulkDownloadingCovers(true);
        setCoverDownloadProgress(0);
        stopBulkDownloadRef.current = false;
        let completed = 0;
        let success = 0;
        let skipped = 0;
        let failed = 0;
        for (let i = 0; i < tracks.length; i++) {
            if (stopBulkDownloadRef.current) {
                toast.info("Cover download stopped");
                break;
            }
            const track = tracks[i];
            if (!track.images) {
                completed++;
                setCoverDownloadProgress(Math.round((completed / tracks.length) * 100));
                continue;
            }
            const id = track.spotify_id || `${track.name}-${track.artists}`;
            setDownloadingCoverTrack(id);
            try {
                const response = await downloadCover({
                    cover_url: track.images,
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
                        setSkippedCovers((prev) => new Set(prev).add(id));
                    }
                    else {
                        success++;
                        setDownloadedCovers((prev) => new Set(prev).add(id));
                    }
                }
                else {
                    failed++;
                    setFailedCovers((prev) => new Set(prev).add(id));
                }
            }
            catch {
                failed++;
                setFailedCovers((prev) => new Set(prev).add(id));
            }
            completed++;
            setCoverDownloadProgress(Math.round((completed / tracks.length) * 100));
        }
        setDownloadingCoverTrack(null);
        setIsBulkDownloadingCovers(false);
        setCoverDownloadProgress(0);
        if (!stopBulkDownloadRef.current) {
            toast.success(`Covers: ${success} downloaded, ${skipped} skipped, ${failed} failed`);
        }
    };
    const handleStopCoverDownload = () => {
        stopBulkDownloadRef.current = true;
    };
    const resetCoverState = () => {
        setDownloadedCovers(new Set());
        setFailedCovers(new Set());
        setSkippedCovers(new Set());
    };
    return {
        downloadingCover,
        downloadingCoverTrack,
        downloadedCovers,
        failedCovers,
        skippedCovers,
        isBulkDownloadingCovers,
        coverDownloadProgress,
        handleDownloadCover,
        handleDownloadAllCovers,
        handleStopCoverDownload,
        resetCoverState,
    };
}
