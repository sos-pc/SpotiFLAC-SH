package meta

// Shared naming for sidecar files — the cover image and the .lrc written next to
// a downloaded track.
//
// cover.go and lyrics.go each carried their own copy of this, ~55 lines that
// agreed on everything except two details. Merging them naively would have
// renamed existing files, so both details are parameters and the observable
// output is unchanged for either caller.

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/sos-pc/SpotiFLAC-SH/backend/util"
)

// trackNumberSeparator values used by the two callers. They differ, and that
// difference is preserved deliberately rather than harmonised here: changing
// either one renames every sidecar already on disk, which is a migration
// decision, not a refactor. See docs/dead-code-removal-plan.md §6.1.
const (
	coverTrackSeparator  = " - " // "01 - Title - Artist.jpg"
	lyricsTrackSeparator = ". "  // "01. Title - Artist.lrc"  (matches the audio file)
)

// buildSidecarFilename renders the base name for a file that sits beside a
// track, then appends ext.
//
// trackNumSep is inserted between the zero-padded position and the name, and
// only in the non-template branch — a "{...}" format places the number itself
// via {track}.
func buildSidecarFilename(
	trackName, artistName, albumName, albumArtist, releaseDate, filenameFormat string,
	includeTrackNumber bool, position, discNumber int,
	trackNumSep, ext string,
) string {
	safeTitle := util.SanitizeFilename(trackName)
	safeArtist := util.SanitizeFilename(artistName)
	safeAlbum := util.SanitizeFilename(albumName)
	safeAlbumArtist := util.SanitizeFilename(albumArtist)

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
		filename = strings.ReplaceAll(filename, "{date}", util.SanitizeFilename(releaseDate))

		if discNumber > 0 {
			filename = strings.ReplaceAll(filename, "{disc}", fmt.Sprintf("%d", discNumber))
		} else {
			filename = strings.ReplaceAll(filename, "{disc}", "")
		}

		if position > 0 {
			filename = strings.ReplaceAll(filename, "{track}", fmt.Sprintf("%02d", position))
		} else {
			filename = regexp.MustCompile(`\{track\}\.\s*`).ReplaceAllString(filename, "")
			filename = regexp.MustCompile(`\{track\}\s*-\s*`).ReplaceAllString(filename, "")
			filename = regexp.MustCompile(`\{track\}\s*`).ReplaceAllString(filename, "")
		}
	} else {
		// "title-artist" is not listed: it is what default already produces, and
		// the lyrics copy never had the case. Spelling it out would only make the
		// two callers look more different than they are.
		switch filenameFormat {
		case "artist-title":
			filename = fmt.Sprintf("%s - %s", safeArtist, safeTitle)
		case "title":
			filename = safeTitle
		default:
			filename = fmt.Sprintf("%s - %s", safeTitle, safeArtist)
		}

		if includeTrackNumber && position > 0 {
			filename = fmt.Sprintf("%02d%s%s", position, trackNumSep, filename)
		}
	}

	return filename + ext
}
