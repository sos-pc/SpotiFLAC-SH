package util

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Separator joins multi-valued fields (artists, genres, …) in filenames and tags.
const Separator = ", "

// UnknownGenre is written when every genre source (Apple/Deezer/MusicBrainz —
// see backend/meta/genre.go) answered and none had a genre for a recording.
// This is a real, distinct value — not blank — for two reasons: a blank genre
// reads as "not processed yet" rather than "genuinely unclassified", so a
// library view can group these into a real "Unknown Genre" collection instead
// of leaving them looking broken; and unlike a download that fails and is
// never retried, a source's catalog can gain the data later (a label backfills
// it, Apple ingests the release), so this value must never be treated as
// resolved for retry purposes — see GetTracksNeedingRetag's selection clause
// and retagOneTrack's skip-guard, both of which must keep re-attempting a
// track tagged with this value exactly as if it were still blank.
const UnknownGenre = "Unknown Genre"

// ResolveTrackNumber picks the number that goes in a filename: the track's
// position in the enclosing list, unless the folder layout is album-based and a
// real album track number exists.
//
// It exists because this choice used to be made independently in five places
// (both existence checks, the Tidal/Qobuz filename builders, the download
// path), and they disagreed — which meant a file could be written under one
// name and looked for under another, so it was re-downloaded forever.
func ResolveTrackNumber(listPosition, albumTrackNumber int, useAlbumTrackNumber bool) int {
	if useAlbumTrackNumber && albumTrackNumber > 0 {
		return albumTrackNumber
	}
	return listPosition
}

// BuildExpectedFilename builds the on-disk name for a track.
//
// trackNumberToPrint is the number to WRITE — already resolved by the caller
// via ResolveTrackNumber. This parameter used to be called `position` and sat
// next to a `useAlbumTrackNumber` flag the function never read, so callers
// reasonably assumed it resolved the choice itself. It did not: two of them
// passed a raw list index and got a different filename than the one on disk.
func BuildExpectedFilename(trackName, artistName, albumName, albumArtist, releaseDate, filenameFormat, playlistName, playlistOwner string, includeTrackNumber bool, trackNumberToPrint, discNumber int) string {

	safeTitle := SanitizeFilename(trackName)
	safeArtist := SanitizeFilename(artistName)
	safeAlbum := SanitizeFilename(albumName)
	safeAlbumArtist := SanitizeFilename(albumArtist)

	safePlaylist := SanitizeFilename(playlistName)
	safeCreator := SanitizeFilename(playlistOwner)

	year := ""
	if len(releaseDate) >= 4 {
		year = releaseDate[:4]
	}

	var filename string

	if strings.Contains(filenameFormat, "{") {
		filename = filenameFormat
		filename = strings.ReplaceAll(filename, "{title}", safeTitle)
		filename = strings.ReplaceAll(filename, "{artist}", safeArtist)
		filename = strings.ReplaceAll(filename, "{album}", safeAlbum)
		filename = strings.ReplaceAll(filename, "{album_artist}", safeAlbumArtist)
		filename = strings.ReplaceAll(filename, "{year}", year)
		filename = strings.ReplaceAll(filename, "{date}", SanitizeFilename(releaseDate))
		filename = strings.ReplaceAll(filename, "{playlist}", safePlaylist)
		filename = strings.ReplaceAll(filename, "{creator}", safeCreator)

		if discNumber > 0 {
			filename = strings.ReplaceAll(filename, "{disc}", fmt.Sprintf("%d", discNumber))
		} else {
			filename = strings.ReplaceAll(filename, "{disc}", "")
		}

		if trackNumberToPrint > 0 {
			filename = strings.ReplaceAll(filename, "{track}", fmt.Sprintf("%02d", trackNumberToPrint))
		} else {

			filename = regexp.MustCompile(`\{track\}\.\s*`).ReplaceAllString(filename, "")
			filename = regexp.MustCompile(`\{track\}\s*-\s*`).ReplaceAllString(filename, "")
			filename = regexp.MustCompile(`\{track\}\s*`).ReplaceAllString(filename, "")
		}
	} else {

		switch filenameFormat {
		case "artist-title":
			filename = fmt.Sprintf("%s - %s", safeArtist, safeTitle)
		case "title":
			filename = safeTitle
		default:
			filename = fmt.Sprintf("%s - %s", safeTitle, safeArtist)
		}

		if includeTrackNumber && trackNumberToPrint > 0 {
			filename = fmt.Sprintf("%02d. %s", trackNumberToPrint, filename)
		}
	}

	return filename + ".flac"
}

func SanitizeFilename(name string) string {

	sanitized := strings.ReplaceAll(name, "/", " ")

	re := regexp.MustCompile(`[<>:"\\|?*]`)
	sanitized = re.ReplaceAllString(sanitized, " ")

	var result strings.Builder
	for _, r := range sanitized {

		if r < 0x20 && r != 0x09 && r != 0x0A && r != 0x0D {
			continue
		}
		if r == 0x7F {
			continue
		}

		if unicode.IsControl(r) && r != 0x09 && r != 0x0A && r != 0x0D {
			continue
		}

		result.WriteRune(r)
	}

	sanitized = result.String()
	sanitized = strings.TrimSpace(sanitized)

	sanitized = strings.Trim(sanitized, ". ")

	re = regexp.MustCompile(`\s+`)
	sanitized = re.ReplaceAllString(sanitized, " ")

	re = regexp.MustCompile(`_+`)
	sanitized = re.ReplaceAllString(sanitized, "_")

	sanitized = strings.Trim(sanitized, "_ ")

	if sanitized == "" {
		return "Unknown"
	}

	if !utf8.ValidString(sanitized) {

		sanitized = strings.ToValidUTF8(sanitized, "_")
	}

	return sanitized
}

func GetFirstArtist(artistString string) string {
	if artistString == "" {
		return ""
	}
	delimiters := []string{", ", " & ", " feat. ", " ft. ", " featuring "}
	for _, d := range delimiters {
		if idx := strings.Index(strings.ToLower(artistString), d); idx != -1 {
			return strings.TrimSpace(artistString[:idx])
		}
	}
	return artistString
}

func NormalizePath(folderPath string) string {

	return strings.ReplaceAll(folderPath, "/", string(filepath.Separator))
}

func SanitizeFolderPath(folderPath string) string {

	normalizedPath := strings.ReplaceAll(folderPath, "\\", "/")
	normalizedPath = strings.ReplaceAll(normalizedPath, "/", string(filepath.Separator))

	sep := string(filepath.Separator)

	parts := strings.Split(normalizedPath, sep)
	sanitizedParts := make([]string, 0, len(parts))

	for i, part := range parts {

		if i == 0 && len(part) == 2 && part[1] == ':' {
			sanitizedParts = append(sanitizedParts, part)
			continue
		}

		if i == 0 && part == "" {
			sanitizedParts = append(sanitizedParts, part)
			continue
		}

		sanitized := SanitizeFilename(part)
		if sanitized != "" {
			sanitizedParts = append(sanitizedParts, sanitized)
		}
	}

	return strings.Join(sanitizedParts, sep)
}
