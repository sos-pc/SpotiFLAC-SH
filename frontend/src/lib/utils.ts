import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";
export function cn(...inputs: ClassValue[]) {
    return twMerge(clsx(inputs));
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
