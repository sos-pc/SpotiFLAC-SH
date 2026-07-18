import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";
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
