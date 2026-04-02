import { useState, useEffect, useCallback, useLayoutEffect } from "react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, } from "@/components/ui/dialog";
import { Search, X, ArrowUp } from "lucide-react";
import { TooltipProvider } from "@/components/ui/tooltip";
import { getSettings, getSettingsWithDefaults, loadSettings, saveSettings, applyThemeMode, applyFont } from "@/lib/settings";
import { applyTheme } from "@/lib/themes";
import { OpenFolder } from "@/lib/rpc";
import { LoginPage } from "@/components/LoginPage";
import { isAuthenticated, clearAuth, getUser, tryLocalAuth, fetchMe } from "@/lib/auth";
import { toastWithSound as toast } from "@/lib/toast-with-sound";
import { TitleBar } from "@/components/TitleBar";
import { Sidebar, type PageType } from "@/components/Sidebar";
import { Header } from "@/components/Header";
import { SearchBar } from "@/components/SearchBar";
import { TrackInfo } from "@/components/TrackInfo";
import { AlbumInfo } from "@/components/AlbumInfo";
import { PlaylistInfo } from "@/components/PlaylistInfo";
import { ArtistInfo } from "@/components/ArtistInfo";
import { DownloadQueue } from "@/components/DownloadQueue";
import { DownloadProgressToast } from "@/components/DownloadProgressToast";
import { AudioAnalysisPage } from "@/components/AudioAnalysisPage";
import { AudioConverterPage } from "@/components/AudioConverterPage";
import { FileManagerPage } from "@/components/FileManagerPage";
import { SettingsPage } from "@/components/SettingsPage";
import { DebugLoggerPage } from "@/components/DebugLoggerPage";
import { AboutPage } from "@/components/AboutPage";
import { HistoryPage } from "@/components/HistoryPage";
import { WatchlistPage } from "@/components/WatchlistPage";
import type { HistoryItem } from "@/components/FetchHistory";
import { useDownload } from "@/hooks/useDownload";
import { useMetadata } from "@/hooks/useMetadata";
import { useLyrics } from "@/hooks/useLyrics";
import { useCover } from "@/hooks/useCover";
import { useAvailability } from "@/hooks/useAvailability";
import { useDownloadQueueDialog } from "@/hooks/useDownloadQueueDialog";
const HISTORY_KEY = "spotiflac_fetch_history";
const MAX_HISTORY = 5;
function App() {
    const [currentPage, setCurrentPage] = useState<PageType>("main");
    const [spotifyUrl, setSpotifyUrl] = useState("");
    const [selectedTracks, setSelectedTracks] = useState<string[]>([]);
    const [searchQuery, setSearchQuery] = useState("");
    const [sortBy, setSortBy] = useState<string>("default");
    const [currentListPage, setCurrentListPage] = useState(1);
    const [hasUpdate, setHasUpdate] = useState(false);
    const [releaseDate, setReleaseDate] = useState<string | null>(null);
    const [fetchHistory, setFetchHistory] = useState<HistoryItem[]>([]);
    const [isSearchMode, setIsSearchMode] = useState(false);
    const [region, setRegion] = useState(() => localStorage.getItem("spotiflac_region") || "US");
    useEffect(() => {
        localStorage.setItem("spotiflac_region", region);
    }, [region]);
    const [showScrollTop, setShowScrollTop] = useState(false);
    const [hasUnsavedSettings, setHasUnsavedSettings] = useState(false);
    const [pendingPageChange, setPendingPageChange] = useState<PageType | null>(null);
    const [showUnsavedChangesDialog, setShowUnsavedChangesDialog] = useState(false);
    const [resetSettingsFn, setResetSettingsFn] = useState<(() => void) | null>(null);
    const ITEMS_PER_PAGE = 50;
    const CURRENT_VERSION = __APP_VERSION__;
    const download = useDownload(region);
    const metadata = useMetadata();
    const lyrics = useLyrics();
    const cover = useCover();
    const availability = useAvailability();
    const downloadQueue = useDownloadQueueDialog();
    const [authed, setAuthed] = useState<boolean>(false);
    const [checkingLocalAuth, setCheckingLocalAuth] = useState<boolean>(true);
    useEffect(() => {
        const initAuth = async () => {
            if (isAuthenticated()) {
                const me = await fetchMe();
                if (me) {
                    setAuthed(true);
                    setAuthUser(me);
                    setCheckingLocalAuth(false);
                    return;
                }
                clearAuth();
            }
            const user = await tryLocalAuth();
            if (user) { setAuthed(true); setAuthUser(user); }
            setCheckingLocalAuth(false);
        };
        initAuth();
    }, []);
    const [authUser, setAuthUser] = useState(getUser());
    useLayoutEffect(() => {
        const savedSettings = getSettings();
        if (savedSettings) {
            applyThemeMode(savedSettings.themeMode);
            applyTheme(savedSettings.theme);
            applyFont(savedSettings.fontFamily);
        }
    }, []);
    useEffect(() => {
        const initSettings = async () => {
            const settings = await loadSettings();
            applyThemeMode(settings.themeMode);
            applyTheme(settings.theme);
            applyFont(settings.fontFamily);
            if (!settings.downloadPath) {
                const settingsWithDefaults = await getSettingsWithDefaults();
                await saveSettings(settingsWithDefaults);
            }
        };
        initSettings();
        const mediaQuery = window.matchMedia("(prefers-color-scheme: dark)");
        const handleChange = () => {
            const currentSettings = getSettings();
            if (currentSettings.themeMode === "auto") {
                applyThemeMode("auto");
                applyTheme(currentSettings.theme);
            }
        };
        mediaQuery.addEventListener("change", handleChange);
        checkForUpdates();
        loadHistory();
        const handleScroll = () => {
            setShowScrollTop(window.scrollY > 300);
        };
        window.addEventListener("scroll", handleScroll);
        const handleAuthExpired = () => {
            clearAuth();
            setAuthed(false);
            setAuthUser(null);
        };
        window.addEventListener("auth:expired", handleAuthExpired);
        return () => {
            mediaQuery.removeEventListener("change", handleChange);
            window.removeEventListener("scroll", handleScroll);
            window.removeEventListener("auth:expired", handleAuthExpired);
        };
    }, [authed]);
    const scrollToTop = useCallback(() => {
        window.scrollTo({ top: 0, behavior: "smooth" });
    }, []);
    useEffect(() => {
        setSelectedTracks([]);
        setSearchQuery("");
        download.resetDownloadedTracks();
        lyrics.resetLyricsState();
        cover.resetCoverState();
        availability.clearAvailability();
        setSortBy("default");
        setCurrentListPage(1);
    }, [metadata.metadata]);
    const checkForUpdates = async () => {
        try {
            const response = await fetch("https://api.github.com/repos/afkarxyz/SpotiFLAC/releases/latest");
            const data = await response.json();
            const latestVersion = data.tag_name?.replace(/^v/, "") || "";
            if (data.published_at) {
                setReleaseDate(data.published_at);
            }
            if (latestVersion && latestVersion > CURRENT_VERSION) {
                setHasUpdate(true);
            }
        }
        catch (err) {
            console.error("Failed to check for updates:", err);
        }
    };
    const loadHistory = () => {
        try {
            const saved = localStorage.getItem(HISTORY_KEY);
            if (saved) {
                setFetchHistory(JSON.parse(saved));
            }
        }
        catch (err) {
            console.error("Failed to load history:", err);
        }
    };
    const saveHistory = (history: HistoryItem[]) => {
        try {
            localStorage.setItem(HISTORY_KEY, JSON.stringify(history));
        }
        catch (err) {
            console.error("Failed to save history:", err);
        }
    };
    const addToHistory = (item: Omit<HistoryItem, "id" | "timestamp">) => {
        setFetchHistory((prev) => {
            const filtered = prev.filter((h) => h.url !== item.url);
            const newItem: HistoryItem = {
                ...item,
                id: crypto.randomUUID(),
                timestamp: Date.now(),
            };
            const updated = [newItem, ...filtered].slice(0, MAX_HISTORY);
            saveHistory(updated);
            return updated;
        });
    };
    const removeFromHistory = (id: string) => {
        setFetchHistory((prev) => {
            const updated = prev.filter((h) => h.id !== id);
            saveHistory(updated);
            return updated;
        });
    };
    const handleHistorySelect = async (item: HistoryItem) => {
        setSpotifyUrl(item.url);
        const updatedUrl = await metadata.handleFetchMetadata(item.url);
        if (updatedUrl) {
            setSpotifyUrl(updatedUrl);
        }
    };
    const handleFetchMetadata = async () => {
        const updatedUrl = await metadata.handleFetchMetadata(spotifyUrl);
        if (updatedUrl) {
            setSpotifyUrl(updatedUrl);
        }
    };
    useEffect(() => {
        if (!metadata.metadata || !spotifyUrl)
            return;
        let historyItem: Omit<HistoryItem, "id" | "timestamp"> | null = null;
        if ("track" in metadata.metadata) {
            const { track } = metadata.metadata;
            historyItem = {
                url: spotifyUrl,
                type: "track",
                name: track.name,
                artist: track.artists,
                image: track.images,
            };
        }
        else if ("album_info" in metadata.metadata) {
            const { album_info } = metadata.metadata;
            historyItem = {
                url: spotifyUrl,
                type: "album",
                name: album_info.name,
                artist: `${album_info.total_tracks.toLocaleString()} tracks`,
                image: album_info.images,
            };
        }
        else if ("playlist_info" in metadata.metadata) {
            const { playlist_info } = metadata.metadata;
            historyItem = {
                url: spotifyUrl,
                type: "playlist",
                name: playlist_info.owner.name,
                artist: `${playlist_info.tracks.total.toLocaleString()} tracks`,
                image: playlist_info.cover || playlist_info.owner.images || "",
            };
        }
        else if ("artist_info" in metadata.metadata) {
            const { artist_info } = metadata.metadata;
            historyItem = {
                url: spotifyUrl,
                type: "artist",
                name: artist_info.name,
                artist: `${artist_info.total_albums.toLocaleString()} albums`,
                image: artist_info.images,
            };
        }
        if (historyItem) {
            addToHistory(historyItem);
        }
    }, [metadata.metadata]);
    const handleSearchChange = (value: string) => {
        setSearchQuery(value);
        setCurrentListPage(1);
    };
    const toggleTrackSelection = (id: string) => {
        setSelectedTracks((prev) => prev.includes(id) ? prev.filter((prevId) => prevId !== id) : [...prev, id]);
    };
    const toggleSelectAll = (tracks: any[]) => {
        const tracksWithId = tracks.filter((track) => track.spotify_id).map((track) => track.spotify_id || "");
        if (tracksWithId.length === 0)
            return;
        const allSelected = tracksWithId.every(id => selectedTracks.includes(id));
        if (allSelected) {
            setSelectedTracks(prev => prev.filter(id => !tracksWithId.includes(id)));
        }
        else {
            setSelectedTracks(prev => Array.from(new Set([...prev, ...tracksWithId])));
        }
    };
    const handleOpenFolder = async () => {
        const settings = getSettings();
        if (!settings.downloadPath) {
            toast.error("Download path not set");
            return;
        }
        try {
            await OpenFolder(settings.downloadPath);
        }
        catch (error) {
            console.error("Error opening folder:", error);
            toast.error(`Error opening folder: ${error}`);
        }
    };
    const renderMetadata = () => {
        if (!metadata.metadata)
            return null;
        if ("track" in metadata.metadata) {
            const { track } = metadata.metadata;
            const trackId = track.spotify_id || "";
            return (<TrackInfo track={track} isDownloading={download.isDownloading} downloadingTrack={download.downloadingTrack} isDownloaded={download.downloadedTracks.has(trackId)} isFailed={download.failedTracks.has(trackId)} isSkipped={download.skippedTracks.has(trackId)} downloadingLyricsTrack={lyrics.downloadingLyricsTrack} downloadedLyrics={lyrics.downloadedLyrics.has(track.spotify_id || "")} failedLyrics={lyrics.failedLyrics.has(track.spotify_id || "")} skippedLyrics={lyrics.skippedLyrics.has(track.spotify_id || "")} checkingAvailability={availability.checkingTrackId === track.spotify_id} availability={availability.availabilityMap.get(track.spotify_id || "")} downloadingCover={cover.downloadingCoverTrack === (track.spotify_id || `${track.name}-${track.artists}`)} downloadedCover={cover.downloadedCovers.has(track.spotify_id || `${track.name}-${track.artists}`)} failedCover={cover.failedCovers.has(track.spotify_id || `${track.name}-${track.artists}`)} skippedCover={cover.skippedCovers.has(track.spotify_id || `${track.name}-${track.artists}`)} onDownload={download.handleDownloadTrack} onDownloadLyrics={(spotifyId, name, artists, albumName, albumArtist, releaseDate, discNumber) => lyrics.handleDownloadLyrics(spotifyId, name, artists, albumName, track.album_name, undefined, albumArtist, releaseDate, discNumber)} onDownloadCover={(coverUrl, trackName, artistName, albumName, _playlistName, _position, trackId, albumArtist, releaseDate, discNumber) => cover.handleDownloadCover(coverUrl, trackName, artistName, albumName, track.album_name, undefined, trackId, albumArtist, releaseDate, discNumber)} onCheckAvailability={availability.checkAvailability} onOpenFolder={handleOpenFolder} onBack={metadata.resetMetadata}/>);
        }
        if ("album_info" in metadata.metadata) {
            const { album_info, track_list } = metadata.metadata;
            return (<AlbumInfo albumInfo={album_info} trackList={track_list} searchQuery={searchQuery} sortBy={sortBy} selectedTracks={selectedTracks} downloadedTracks={download.downloadedTracks} failedTracks={download.failedTracks} skippedTracks={download.skippedTracks} downloadingTrack={download.downloadingTrack} isDownloading={download.isDownloading} bulkDownloadType={download.bulkDownloadType} downloadProgress={download.downloadProgress} currentDownloadInfo={download.currentDownloadInfo} currentPage={currentListPage} itemsPerPage={ITEMS_PER_PAGE} downloadedLyrics={lyrics.downloadedLyrics} failedLyrics={lyrics.failedLyrics} skippedLyrics={lyrics.skippedLyrics} downloadingLyricsTrack={lyrics.downloadingLyricsTrack} checkingAvailabilityTrack={availability.checkingTrackId} availabilityMap={availability.availabilityMap} downloadedCovers={cover.downloadedCovers} failedCovers={cover.failedCovers} skippedCovers={cover.skippedCovers} downloadingCoverTrack={cover.downloadingCoverTrack} isBulkDownloadingCovers={cover.isBulkDownloadingCovers} isBulkDownloadingLyrics={lyrics.isBulkDownloadingLyrics} onSearchChange={handleSearchChange} onSortChange={setSortBy} onToggleTrack={toggleTrackSelection} onToggleSelectAll={toggleSelectAll} onDownloadTrack={download.handleDownloadTrack} onDownloadLyrics={(spotifyId, name, artists, albumName, _folderName, _isArtistDiscography, position, albumArtist, releaseDate, discNumber) => lyrics.handleDownloadLyrics(spotifyId, name, artists, albumName, album_info.name, position, albumArtist, releaseDate, discNumber, true)} onDownloadCover={(coverUrl, trackName, artistName, albumName, _folderName, _isArtistDiscography, position, trackId, albumArtist, releaseDate, discNumber) => cover.handleDownloadCover(coverUrl, trackName, artistName, albumName, album_info.name, position, trackId, albumArtist, releaseDate, discNumber, true)} onCheckAvailability={availability.checkAvailability} onDownloadAllLyrics={() => lyrics.handleDownloadAllLyrics(track_list, album_info.name, undefined, true)} onDownloadAllCovers={() => cover.handleDownloadAllCovers(track_list, album_info.name, true)} onDownloadAll={() => download.handleDownloadAll(track_list, album_info.name, true)} onDownloadSelected={() => download.handleDownloadSelected(selectedTracks, track_list, album_info.name, true)} onStopDownload={download.handleStopDownload} onOpenFolder={handleOpenFolder} onPageChange={setCurrentListPage} onBack={metadata.resetMetadata} onArtistClick={async (artist) => {
                    const artistUrl = await metadata.handleArtistClick(artist);
                    if (artistUrl) {
                        setSpotifyUrl(artistUrl);
                    }
                }} onTrackClick={async (track) => {
                    if (track.external_urls) {
                        setSpotifyUrl(track.external_urls);
                        await metadata.handleFetchMetadata(track.external_urls);
                    }
                }}/>);
        }
        if ("playlist_info" in metadata.metadata) {
            const { playlist_info, track_list } = metadata.metadata;
            return (<PlaylistInfo playlistInfo={playlist_info} trackList={track_list} searchQuery={searchQuery} sortBy={sortBy} selectedTracks={selectedTracks} downloadedTracks={download.downloadedTracks} failedTracks={download.failedTracks} skippedTracks={download.skippedTracks} downloadingTrack={download.downloadingTrack} isDownloading={download.isDownloading} bulkDownloadType={download.bulkDownloadType} downloadProgress={download.downloadProgress} currentDownloadInfo={download.currentDownloadInfo} currentPage={currentListPage} itemsPerPage={ITEMS_PER_PAGE} downloadedLyrics={lyrics.downloadedLyrics} failedLyrics={lyrics.failedLyrics} skippedLyrics={lyrics.skippedLyrics} downloadingLyricsTrack={lyrics.downloadingLyricsTrack} checkingAvailabilityTrack={availability.checkingTrackId} availabilityMap={availability.availabilityMap} downloadedCovers={cover.downloadedCovers} failedCovers={cover.failedCovers} skippedCovers={cover.skippedCovers} downloadingCoverTrack={cover.downloadingCoverTrack} isBulkDownloadingCovers={cover.isBulkDownloadingCovers} isBulkDownloadingLyrics={lyrics.isBulkDownloadingLyrics} onSearchChange={handleSearchChange} onSortChange={setSortBy} onToggleTrack={toggleTrackSelection} onToggleSelectAll={toggleSelectAll} onDownloadTrack={download.handleDownloadTrack} onDownloadLyrics={(spotifyId, name, artists, albumName, _folderName, _isArtistDiscography, position, albumArtist, releaseDate, discNumber) => lyrics.handleDownloadLyrics(spotifyId, name, artists, albumName, playlist_info.owner.name, position, albumArtist, releaseDate, discNumber)} onDownloadCover={(coverUrl, trackName, artistName, albumName, _folderName, _isArtistDiscography, position, trackId, albumArtist, releaseDate, discNumber) => cover.handleDownloadCover(coverUrl, trackName, artistName, albumName, playlist_info.owner.name, position, trackId, albumArtist, releaseDate, discNumber)} onCheckAvailability={availability.checkAvailability} onDownloadAllLyrics={() => lyrics.handleDownloadAllLyrics(track_list, playlist_info.owner.name)} onDownloadAllCovers={() => cover.handleDownloadAllCovers(track_list, playlist_info.owner.name)} onDownloadAll={() => download.handleDownloadAll(track_list, playlist_info.owner.name)} onDownloadSelected={() => download.handleDownloadSelected(selectedTracks, track_list, playlist_info.owner.name)} onStopDownload={download.handleStopDownload} onOpenFolder={handleOpenFolder} onPageChange={setCurrentListPage} onBack={metadata.resetMetadata} onAlbumClick={metadata.handleAlbumClick} onArtistClick={async (artist) => {
                    const artistUrl = await metadata.handleArtistClick(artist);
                    if (artistUrl) {
                        setSpotifyUrl(artistUrl);
                    }
                }} onTrackClick={async (track) => {
                    if (track.external_urls) {
                        setSpotifyUrl(track.external_urls);
                        await metadata.handleFetchMetadata(track.external_urls);
                    }
                }}/>);
        }
        if ("artist_info" in metadata.metadata) {
            const { artist_info, album_list, track_list } = metadata.metadata;
            return (<ArtistInfo artistInfo={artist_info} albumList={album_list} trackList={track_list} tracksLoading={metadata.tracksLoading} searchQuery={searchQuery} sortBy={sortBy} selectedTracks={selectedTracks} downloadedTracks={download.downloadedTracks} failedTracks={download.failedTracks} skippedTracks={download.skippedTracks} downloadingTrack={download.downloadingTrack} isDownloading={download.isDownloading} bulkDownloadType={download.bulkDownloadType} downloadProgress={download.downloadProgress} currentDownloadInfo={download.currentDownloadInfo} currentPage={currentListPage} itemsPerPage={ITEMS_PER_PAGE} downloadedLyrics={lyrics.downloadedLyrics} failedLyrics={lyrics.failedLyrics} skippedLyrics={lyrics.skippedLyrics} downloadingLyricsTrack={lyrics.downloadingLyricsTrack} checkingAvailabilityTrack={availability.checkingTrackId} availabilityMap={availability.availabilityMap} downloadedCovers={cover.downloadedCovers} failedCovers={cover.failedCovers} skippedCovers={cover.skippedCovers} downloadingCoverTrack={cover.downloadingCoverTrack} isBulkDownloadingCovers={cover.isBulkDownloadingCovers} isBulkDownloadingLyrics={lyrics.isBulkDownloadingLyrics} onSearchChange={handleSearchChange} onSortChange={setSortBy} onToggleTrack={toggleTrackSelection} onToggleSelectAll={toggleSelectAll} onDownloadTrack={download.handleDownloadTrack} onDownloadLyrics={(spotifyId, name, artists, albumName, _folderName, _isArtistDiscography, position, albumArtist, releaseDate, discNumber) => lyrics.handleDownloadLyrics(spotifyId, name, artists, albumName, artist_info.name, position, albumArtist, releaseDate, discNumber)} onDownloadCover={(coverUrl, trackName, artistName, albumName, _folderName, _isArtistDiscography, position, trackId, albumArtist, releaseDate, discNumber) => cover.handleDownloadCover(coverUrl, trackName, artistName, albumName, artist_info.name, position, trackId, albumArtist, releaseDate, discNumber)} onCheckAvailability={availability.checkAvailability} onDownloadAllLyrics={() => lyrics.handleDownloadAllLyrics(track_list, artist_info.name)} onDownloadAllCovers={() => cover.handleDownloadAllCovers(track_list, artist_info.name)} onDownloadAll={() => download.handleDownloadAll(track_list, artist_info.name)} onDownloadSelected={() => download.handleDownloadSelected(selectedTracks, track_list, artist_info.name)} onStopDownload={download.handleStopDownload} onOpenFolder={handleOpenFolder} onAlbumClick={metadata.handleAlbumClick} onBack={metadata.resetMetadata} onArtistClick={async (artist) => {
                    const artistUrl = await metadata.handleArtistClick(artist);
                    if (artistUrl) {
                        setSpotifyUrl(artistUrl);
                    }
                }} onPageChange={setCurrentListPage} onTrackClick={async (track) => {
                    if (track.external_urls) {
                        setSpotifyUrl(track.external_urls);
                        await metadata.handleFetchMetadata(track.external_urls);
                    }
                }}/>);
        }
        return null;
    };
    const handlePageChange = (page: PageType) => {
        if (currentPage === "settings" && hasUnsavedSettings && page !== "settings") {
            setPendingPageChange(page);
            setShowUnsavedChangesDialog(true);
            return;
        }
        setCurrentPage(page);
    };
    const handleDiscardChanges = () => {
        setShowUnsavedChangesDialog(false);
        if (resetSettingsFn) {
            resetSettingsFn();
        }
        const savedSettings = getSettings();
        applyThemeMode(savedSettings.themeMode);
        applyTheme(savedSettings.theme);
        applyFont(savedSettings.fontFamily);
        if (pendingPageChange) {
            setCurrentPage(pendingPageChange);
            setPendingPageChange(null);
        }
    };
    const handleCancelNavigation = () => {
        setShowUnsavedChangesDialog(false);
        setPendingPageChange(null);
    };
    const renderPage = () => {
        switch (currentPage) {
            case "settings":
                return <SettingsPage onUnsavedChangesChange={setHasUnsavedSettings} onResetRequest={setResetSettingsFn}/>;
            case "debug":
                return <DebugLoggerPage />;
            case "watchlist":
                return <WatchlistPage />;
            case "about":
                return <AboutPage version={CURRENT_VERSION}/>;
            case "history":
                return <HistoryPage onHistorySelect={(cachedData) => {
                        metadata.loadFromCache(cachedData);
                        setCurrentPage("main");
                    }}/>;
            case "audio-analysis":
                return <AudioAnalysisPage />;
            case "audio-converter":
                return <AudioConverterPage />;
            case "file-manager":
                if (!authUser?.is_admin) return null;
                return <FileManagerPage />;
            default:
                return (<>
                    <Header version={CURRENT_VERSION} hasUpdate={hasUpdate} releaseDate={releaseDate}/>




                    <Dialog open={metadata.showAlbumDialog} onOpenChange={metadata.setShowAlbumDialog}>
                        <DialogContent className="sm:max-w-[425px] p-6 [&>button]:hidden">
                            <div className="absolute right-4 top-4">
                                <Button variant="ghost" size="icon" className="h-6 w-6 opacity-70 hover:opacity-100" onClick={() => metadata.setShowAlbumDialog(false)}>
                                    <X className="h-4 w-4"/>
                                </Button>
                            </div>
                            <DialogTitle className="text-sm font-medium">Fetch Album</DialogTitle>
                            <DialogDescription>
                                Do you want to fetch metadata for this album?
                            </DialogDescription>
                            {metadata.selectedAlbum && (<div className="py-2">
                                <p className="font-medium bg-muted/50 rounded-md px-3 py-2">{metadata.selectedAlbum.name}</p>
                            </div>)}
                            <DialogFooter>
                                <Button variant="outline" onClick={() => metadata.setShowAlbumDialog(false)}>
                                    Cancel
                                </Button>
                                <Button onClick={async () => {
                        const albumUrl = await metadata.handleConfirmAlbumFetch();
                        if (albumUrl) {
                            setSpotifyUrl(albumUrl);
                        }
                    }}>
                                    <Search className="h-4 w-4"/>
                                    Fetch Album
                                </Button>
                            </DialogFooter>
                        </DialogContent>
                    </Dialog>

                    <SearchBar url={spotifyUrl} loading={metadata.loading} onUrlChange={setSpotifyUrl} onFetch={handleFetchMetadata} onFetchUrl={async (url) => {
                        setSpotifyUrl(url);
                        const updatedUrl = await metadata.handleFetchMetadata(url);
                        if (updatedUrl) {
                            setSpotifyUrl(updatedUrl);
                        }
                    }} history={fetchHistory} onHistorySelect={handleHistorySelect} onHistoryRemove={removeFromHistory} hasResult={!!metadata.metadata} searchMode={isSearchMode} onSearchModeChange={setIsSearchMode} region={region} onRegionChange={setRegion}/>

                    {!isSearchMode && metadata.metadata && renderMetadata()}
                </>);
        }
    };
    if (checkingLocalAuth) {
        return <div className="min-h-screen flex items-center justify-center bg-background"><div className="text-muted-foreground text-sm">Connecting...</div></div>;
    }
    if (!authed) {
        return <LoginPage onLogin={() => {
        setAuthed(true);
        setAuthUser(getUser());
    }} />;
    }

    return (<TooltipProvider>
        <div className="min-h-screen bg-background flex flex-col">
            <TitleBar onLogout={() => { clearAuth(); setAuthed(false); setAuthUser(null); }} userName={authUser?.display_name} />
            <Sidebar currentPage={currentPage} onPageChange={handlePageChange} isAdmin={authUser?.is_admin ?? false}/>


            <div className="flex-1 ml-14 mt-10 p-4 md:p-8">
                <div className="max-w-4xl mx-auto space-y-6">
                    {renderPage()}
                </div>
            </div>


            <DownloadProgressToast onClick={downloadQueue.openQueue}/>


            <DownloadQueue isOpen={downloadQueue.isOpen} onClose={downloadQueue.closeQueue}/>


            {showScrollTop && (<Button onClick={scrollToTop} className="fixed bottom-6 right-6 z-50 h-10 w-10 rounded-full shadow-lg" size="icon">
                <ArrowUp className="h-5 w-5"/>
            </Button>)}


            <Dialog open={showUnsavedChangesDialog} onOpenChange={setShowUnsavedChangesDialog}>
                <DialogContent className="sm:max-w-[425px] [&>button]:hidden">
                    <DialogHeader>
                        <DialogTitle>Unsaved Changes</DialogTitle>
                        <DialogDescription>
                            You have unsaved changes in Settings. Are you sure you want to leave? Your changes will be lost.
                        </DialogDescription>
                    </DialogHeader>
                    <DialogFooter>
                        <Button variant="outline" onClick={handleCancelNavigation}>
                            Cancel
                        </Button>
                        <Button variant="destructive" onClick={handleDiscardChanges}>
                            Discard Changes
                        </Button>
                    </DialogFooter>
                </DialogContent>
            </Dialog>

            
        </div>
    </TooltipProvider>);
}
export default App;
