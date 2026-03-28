import type { SpotifyMetadataResponse, DownloadRequest, DownloadResponse, HealthResponse, LyricsDownloadRequest, LyricsDownloadResponse, CoverDownloadRequest, CoverDownloadResponse, HeaderDownloadRequest, HeaderDownloadResponse, GalleryImageDownloadRequest, GalleryImageDownloadResponse, AvatarDownloadRequest, AvatarDownloadResponse, } from "@/types/api";
import { GetSpotifyMetadata, DownloadTrack, DownloadLyrics, DownloadCover, DownloadHeader, DownloadGalleryImage, DownloadAvatar } from "@/lib/rpc";

export async function fetchSpotifyMetadata(url: string, batch: boolean = true, _delay: number = 1.0, _timeout: number = 300.0): Promise<SpotifyMetadataResponse> {
    return GetSpotifyMetadata({ url, batch });
}

export async function downloadTrack(request: DownloadRequest): Promise<DownloadResponse> {
    return await DownloadTrack(request);
}

export async function checkHealth(): Promise<HealthResponse> {
    return {
        status: "ok",
        time: new Date().toISOString(),
    };
}

export async function downloadLyrics(request: LyricsDownloadRequest): Promise<LyricsDownloadResponse> {
    return await DownloadLyrics(request);
}

export async function downloadCover(request: CoverDownloadRequest): Promise<CoverDownloadResponse> {
    return await DownloadCover(request);
}

export async function downloadHeader(request: HeaderDownloadRequest): Promise<HeaderDownloadResponse> {
    return await DownloadHeader(request);
}

export async function downloadGalleryImage(request: GalleryImageDownloadRequest): Promise<GalleryImageDownloadResponse> {
    return await DownloadGalleryImage(request);
}

export async function downloadAvatar(request: AvatarDownloadRequest): Promise<AvatarDownloadResponse> {
    return await DownloadAvatar(request);
}
