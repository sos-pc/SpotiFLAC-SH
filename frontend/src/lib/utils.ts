import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";
import { parseTemplate, type Settings, type TemplateData } from "./settings";
export function cn(...inputs: ClassValue[]) {
    return twMerge(clsx(inputs));
}
export function sanitizePath(input: string, os: string): string {
    const sanitized = input.trim();
    if (os === "Windows") {
        return sanitized.replace(/[<>:"/\\|?*]/g, "_");
    }
    return sanitized.replace(/\//g, "_");
}
export function joinPath(os: string, ...parts: string[]): string {
    const sep = os === "Windows" ? "\\" : "/";
    const filtered = parts.filter(Boolean);
    if (filtered.length === 0)
        return "";
    const joined = filtered
        .map((p, i) => {
        if (i === 0) {
            return p.replace(/[/\\]+$/g, "");
        }
        return p.replace(/^[/\\]+|[/\\]+$/g, "");
    })
        .filter(Boolean)
        .join(sep);
    return joined;
}
// The slash-in-metadata-value placeholder trick: a track/artist/album name
// containing "/" (e.g. "AC/DC") would otherwise be split into extra path
// segments when a folder template gets parsed and split on "/". Escaped
// here before template parsing, restored to a literal " " once the
// template result has already been split into path segments.
const SLASH_PLACEHOLDER = "__SLASH_PLACEHOLDER__";

export interface OutputPathTrackInfo {
    artistName?: string;
    albumName?: string;
    albumArtist?: string;
    trackName?: string;
    playlistName?: string;
    trackNumber?: number;
    releaseDate?: string;
    // Whether this download is happening from an album view rather than a
    // playlist view — a folder template that already places tracks in an
    // album/album-artist/playlist subfolder shouldn't also get a redundant
    // playlist subfolder layered on top when browsing an album.
    isAlbum?: boolean;
}

export interface ResolvedOutputPath {
    outputDir: string;
    displayArtist?: string;
    displayAlbumArtist?: string;
}

// resolveOutputPath is the single implementation of "where does this
// track/lyrics/cover file land on disk" — settings.downloadPath, optionally
// under a playlist subfolder, optionally further split into a
// settings.folderTemplate-derived path ({artist}/{album}/... etc).
//
// This used to be reimplemented independently at five call sites (the main
// track downloader, and both the single-track and bulk-download paths in
// useLyrics/useCover), which had drifted out of sync with each other: only
// the track-download copy checked settings.createPlaylistFolder before
// adding a playlist subfolder — the four lyrics/cover copies added one
// unconditionally, silently ignoring that setting. Consolidating here means
// there's only one place left to get this right.
export function resolveOutputPath(settings: Settings, info: OutputPathTrackInfo): ResolvedOutputPath {
    const os = settings.operatingSystem;
    let outputDir = settings.downloadPath;

    const displayArtist = settings.useFirstArtistOnly && info.artistName
        ? getFirstArtist(info.artistName)
        : info.artistName;
    const displayAlbumArtist = settings.useFirstArtistOnly && info.albumArtist
        ? getFirstArtist(info.albumArtist)
        : info.albumArtist;

    const templateData: TemplateData = {
        artist: displayArtist?.replace(/\//g, SLASH_PLACEHOLDER),
        album: info.albumName?.replace(/\//g, SLASH_PLACEHOLDER),
        album_artist:
            displayAlbumArtist?.replace(/\//g, SLASH_PLACEHOLDER) ||
            displayArtist?.replace(/\//g, SLASH_PLACEHOLDER),
        title: info.trackName?.replace(/\//g, SLASH_PLACEHOLDER),
        track: info.trackNumber,
        year: info.releaseDate?.substring(0, 4),
        date: info.releaseDate,
        playlist: info.playlistName?.replace(/\//g, SLASH_PLACEHOLDER),
    };

    const folderTemplate = settings.folderTemplate || "";
    const useAlbumSubfolder =
        folderTemplate.includes("{album}") ||
        folderTemplate.includes("{album_artist}") ||
        folderTemplate.includes("{playlist}");

    if (
        settings.createPlaylistFolder &&
        info.playlistName &&
        (!info.isAlbum || !useAlbumSubfolder)
    ) {
        outputDir = joinPath(
            os,
            outputDir,
            sanitizePath(info.playlistName.replace(/\//g, " "), os),
        );
    }

    if (settings.folderTemplate) {
        const folderPath = parseTemplate(settings.folderTemplate, templateData);
        if (folderPath) {
            const parts = folderPath.split("/").filter((p: string) => p.trim());
            for (const part of parts) {
                const sanitizedPart = part.replace(new RegExp(SLASH_PLACEHOLDER, "g"), " ");
                outputDir = joinPath(os, outputDir, sanitizePath(sanitizedPart, os));
            }
        }
    }

    return { outputDir, displayArtist, displayAlbumArtist };
}
// resolvePlaylistBaseDir computes just the "downloadPath + optional playlist
// subfolder" portion used by the bulk-download existence-check pass, where a
// single shared per-track folder-template resolution isn't possible (each
// selected track can have a different artist/album/title) — the rest of the
// folder template is resolved per-track server-side instead.
export function resolvePlaylistBaseDir(
    settings: Settings,
    folderName: string | undefined,
    isAlbum: boolean | undefined,
): string {
    const os = settings.operatingSystem;
    let outputDir = settings.downloadPath;
    const useAlbumTag = settings.folderTemplate?.includes("{album}");
    if (
        settings.createPlaylistFolder &&
        folderName &&
        (!isAlbum || !useAlbumTag)
    ) {
        outputDir = joinPath(
            os,
            outputDir,
            sanitizePath(folderName.replace(/\//g, " "), os),
        );
    }
    return outputDir;
}
export function openExternal(url: string) {
    if (!url)
        return;
    window.open(url, "_blank", "noopener,noreferrer");
}
export function getFirstArtist(artistString: string): string {
    if (!artistString)
        return artistString;
    const delimiters = /[,&]|(?:\s+(?:feat\.?|ft\.?|featuring)\s+)/i;
    const parts = artistString.split(delimiters);
    return parts[0].trim();
}
