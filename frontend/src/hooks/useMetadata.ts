import { useState, useRef } from "react";
import { fetchSpotifyMetadata } from "@/lib/api";
import { toastWithSound as toast } from "@/lib/toast-with-sound";
import { logger } from "@/lib/logger";
import { AddFetchHistory } from "@/lib/rpc";
import { getToken } from "@/lib/auth";
import type { SpotifyMetadataResponse } from "@/types/api";
export function useMetadata() {
    const [loading, setLoading] = useState(false);
    const [tracksLoading, setTracksLoading] = useState(false);
    const [metadata, setMetadata] = useState<SpotifyMetadataResponse | null>(null);
    const [showAlbumDialog, setShowAlbumDialog] = useState(false);
    const activeStreamRef = useRef<EventSource | null>(null);
    const [selectedAlbum, setSelectedAlbum] = useState<{
        id: string;
        name: string;
        external_urls: string;
    } | null>(null);
    const [pendingArtistName, setPendingArtistName] = useState<string | null>(null);
    const getUrlType = (url: string): string => {
        if (url.includes("/track/"))
            return "track";
        if (url.includes("/album/"))
            return "album";
        if (url.includes("/playlist/"))
            return "playlist";
        if (url.includes("/artist/"))
            return "artist";
        return "unknown";
    };
    const saveToHistory = async (url: string, data: SpotifyMetadataResponse) => {
        try {
            let name = "";
            let info = "";
            let image = "";
            let type = "unknown";
            if ("track" in data) {
                type = "track";
                name = data.track.name;
                info = data.track.artists;
                image = (data.track.images && data.track.images.length > 0) ? data.track.images : "";
            }
            else if ("album_info" in data) {
                type = "album";
                name = data.album_info.name;
                info = `${data.track_list.length} tracks`;
                image = data.album_info.images;
            }
            else if ("playlist_info" in data) {
                type = "playlist";
                if (data.playlist_info.name) {
                    name = data.playlist_info.name;
                }
                else if (data.playlist_info.owner.name) {
                    name = data.playlist_info.owner.name;
                }
                info = `${data.playlist_info.tracks.total} tracks`;
                image = data.playlist_info.cover || "";
            }
            else if ("artist_info" in data) {
                type = "artist";
                name = data.artist_info.name;
                info = `${data.artist_info.total_albums || data.album_list.length} albums`;
                image = data.artist_info.images;
            }
            const jsonStr = JSON.stringify(data);
            await AddFetchHistory({
                id: crypto.randomUUID(),
                url: url,
                type: type,
                name: name,
                info: info,
                image: image,
                data: jsonStr,
                timestamp: Math.floor(Date.now() / 1000)
            });
        }
        catch (err) {
            console.error("Failed to save fetch history:", err);
        }
    };
    const fetchArtistMetadataStreaming = (url: string): Promise<void> => {
        // Close any in-progress stream before starting a new one
        if (activeStreamRef.current) {
            activeStreamRef.current.close();
            activeStreamRef.current = null;
        }
        return new Promise((resolve, reject) => {
            setLoading(true);
            setTracksLoading(false);
            setMetadata(null);

            const token = getToken();
            const streamUrl = `/api/v1/search/stream?url=${encodeURIComponent(url)}&token=${encodeURIComponent(token ?? "")}`;
            const es = new EventSource(streamUrl);
            activeStreamRef.current = es;

            // Accumulate data in a plain object — avoids stale-closure issues with state
            const accumulated: { artist_info: any; album_list: any[]; track_list: any[] } = {
                artist_info: null,
                album_list: [],
                track_list: [],
            };

            es.addEventListener("artist_info", (e: MessageEvent) => {
                const data = JSON.parse(e.data);
                accumulated.artist_info = data.artist_info;
                accumulated.album_list = data.album_list ?? [];
                accumulated.track_list = [];
                setMetadata({ ...accumulated } as any);
                setLoading(false);
                setTracksLoading(true);
                logger.success(`fetched artist: ${data.artist_info?.name}`);
                logger.debug(`${accumulated.album_list.length} albums`);
            });

            es.addEventListener("album_tracks", (e: MessageEvent) => {
                const data = JSON.parse(e.data);
                accumulated.track_list = [...accumulated.track_list, ...(data.tracks ?? [])];
                setMetadata({ ...accumulated } as any);
            });

            es.addEventListener("done", () => {
                es.close();
                activeStreamRef.current = null;
                setTracksLoading(false);
                saveToHistory(url, { ...accumulated } as any);
                logger.info(`streaming complete: ${accumulated.track_list.length} tracks`);
                toast.success("Metadata fetched successfully");
                resolve();
            });

            es.addEventListener("stream_error", (e: MessageEvent) => {
                es.close();
                activeStreamRef.current = null;
                setLoading(false);
                setTracksLoading(false);
                const msg = (() => { try { return JSON.parse(e.data).message; } catch { return "Failed to fetch artist data"; } })();
                logger.error(`stream failed: ${msg}`);
                toast.error(msg);
                reject(new Error(msg));
            });

            es.onerror = () => {
                es.close();
                activeStreamRef.current = null;
                setLoading(false);
                setTracksLoading(false);
                const msg = "Stream connection error";
                logger.error(msg);
                reject(new Error(msg));
            };
        });
    };

    const fetchMetadataDirectly = async (url: string) => {
        const urlType = getUrlType(url);
        logger.info(`fetching ${urlType} metadata...`);
        logger.debug(`url: ${url}`);
        setLoading(true);
        setMetadata(null);
        try {
            const startTime = Date.now();
            const timeout = urlType === "artist" ? 60 : 300;
            const data = await fetchSpotifyMetadata(url, true, 1.0, timeout);
            const elapsed = ((Date.now() - startTime) / 1000).toFixed(2);
            if ("playlist_info" in data) {
                const playlistInfo = data.playlist_info;
                if (!playlistInfo.owner.name && playlistInfo.tracks.total === 0 && data.track_list.length === 0) {
                    logger.warning("playlist appears to be empty or private");
                    toast.error("Playlist not found or may be private");
                    setMetadata(null);
                    return;
                }
            }
            else if ("album_info" in data) {
                const albumInfo = data.album_info;
                if (!albumInfo.name && albumInfo.total_tracks === 0 && data.track_list.length === 0) {
                    logger.warning("album appears to be empty or not found");
                    toast.error("Album not found or may be private");
                    setMetadata(null);
                    return;
                }
            }
            setMetadata(data);
            saveToHistory(url, data);
            if ("track" in data) {
                logger.success(`fetched track: ${data.track.name} - ${data.track.artists}`);
                logger.debug(`duration: ${data.track.duration_ms}ms`);
            }
            else if ("album_info" in data) {
                logger.success(`fetched album: ${data.album_info.name}`);
                logger.debug(`${data.track_list.length} tracks, released: ${data.album_info.release_date}`);
            }
            else if ("playlist_info" in data) {
                logger.success(`fetched playlist: ${data.track_list.length} tracks`);
                logger.debug(`by ${data.playlist_info.owner.display_name || data.playlist_info.owner.name}`);
            }
            else if ("artist_info" in data) {
                logger.success(`fetched artist: ${data.artist_info.name}`);
                logger.debug(`${data.album_list.length} albums, ${data.track_list.length} tracks`);
            }
            logger.info(`fetch completed in ${elapsed}s`);
            toast.success("Metadata fetched successfully");
        }
        catch (err) {
            const errorMsg = err instanceof Error ? err.message : "Failed to fetch metadata";
            logger.error(`fetch failed: ${errorMsg}`);
            toast.error(errorMsg);
        }
        finally {
            setLoading(false);
        }
    };
    const loadFromCache = (cachedData: string) => {
        try {
            const data = JSON.parse(cachedData);
            setMetadata(data);
            toast.success("Loaded from cache");
        }
        catch (err) {
            console.error("Failed to load from cache:", err);
            toast.error("Failed to load from cache");
        }
    };
    const handleFetchMetadata = async (url: string) => {
        if (!url.trim()) {
            logger.warning("empty url provided");
            toast.error("Please enter a Spotify URL");
            return;
        }
        let urlToFetch = url.trim();
        const isSpotifyUrl = urlToFetch.includes("spotify.com") || urlToFetch.startsWith("spotify:");
        if (!isSpotifyUrl) {
            logger.warning("not a valid spotify url");
            toast.error("Please enter a valid Spotify URL (e.g. https://open.spotify.com/track/...)");
            return;
        }
        const isArtistUrl = urlToFetch.includes("/artist/");
        if (isArtistUrl && !urlToFetch.includes("/discography")) {
            urlToFetch = urlToFetch.replace(/\/$/, "") + "/discography/all";
            logger.debug("converted to discography url");
        }
        if (isArtistUrl) {
            logger.info("artist url detected — using streaming");
            setPendingArtistName(null);
            await fetchArtistMetadataStreaming(urlToFetch);
        }
        else {
            await fetchMetadataDirectly(urlToFetch);
        }
        return urlToFetch;
    };
    const handleAlbumClick = (album: {
        id: string;
        name: string;
        external_urls: string;
    }) => {
        logger.debug(`album clicked: ${album.name}`);
        setSelectedAlbum(album);
        setShowAlbumDialog(true);
    };
    const handleArtistClick = async (artist: {
        id: string;
        name: string;
        external_urls: string;
    }) => {
        logger.debug(`artist clicked: ${artist.name}`);
        const artistUrl = artist.external_urls.replace(/\/$/, "") + "/discography/all";
        setPendingArtistName(artist.name);
        await fetchArtistMetadataStreaming(artistUrl);
        return artistUrl;
    };
    const handleConfirmAlbumFetch = async () => {
        if (!selectedAlbum)
            return;
        const albumUrl = selectedAlbum.external_urls;
        logger.info(`fetching album: ${selectedAlbum.name}...`);
        logger.debug(`url: ${albumUrl}`);
        setShowAlbumDialog(false);
        setLoading(true);
        setMetadata(null);
        try {
            const startTime = Date.now();
            const data = await fetchSpotifyMetadata(albumUrl);
            const elapsed = ((Date.now() - startTime) / 1000).toFixed(2);
            if ("album_info" in data) {
                const albumInfo = data.album_info;
                if (!albumInfo.name && albumInfo.total_tracks === 0 && data.track_list.length === 0) {
                    logger.warning("album appears to be empty or not found");
                    toast.error("Album not found or may be private");
                    setMetadata(null);
                    setSelectedAlbum(null);
                    return albumUrl;
                }
            }
            setMetadata(data);
            saveToHistory(albumUrl, data);
            if ("album_info" in data) {
                logger.success(`fetched album: ${data.album_info.name}`);
                logger.debug(`${data.track_list.length} tracks, released: ${data.album_info.release_date}`);
            }
            logger.info(`fetch completed in ${elapsed}s`);
            toast.success("Album metadata fetched successfully");
            return albumUrl;
        }
        catch (err) {
            const errorMsg = err instanceof Error ? err.message : "Failed to fetch album metadata";
            logger.error(`fetch failed: ${errorMsg}`);
            toast.error(errorMsg);
        }
        finally {
            setLoading(false);
            setSelectedAlbum(null);
        }
    };
    return {
        loading,
        tracksLoading,
        metadata,
        showAlbumDialog,
        setShowAlbumDialog,
        selectedAlbum,
        pendingArtistName,
        handleFetchMetadata,
        handleAlbumClick,
        handleConfirmAlbumFetch,
        handleArtistClick,
        loadFromCache,
        resetMetadata: () => setMetadata(null),
    };
}
