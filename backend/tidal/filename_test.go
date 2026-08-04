package tidal

import (
	"testing"

	"github.com/sos-pc/SpotiFLAC-SH/backend/util"
)

// The bug this file exists to prevent is silent and expensive: the download and
// the "is it already on disk?" check must agree on the filename, or every pass
// concludes the track is missing and downloads it again.
//
// They diverged for real. buildTidalFilename was a near-copy of
// util.BuildExpectedFilename that substituted every placeholder EXCEPT
// {playlist} and {creator}. With the default template ("{title} - {artist}")
// nothing showed, which is exactly why it survived. So the assertions below
// lead with a template containing both.

func paramsFor(format string) DownloadParams {
	return DownloadParams{
		FilenameFormat: format,
		// "Song / Title" on purpose: the slash exercises sanitization, which
		// collapses it and the surrounding spaces to a single space.
		SpotifyTrackName:    "Song / Title",
		SpotifyArtistName:   "Artist One, Artist Two",
		SpotifyAlbumName:    "The Album",
		SpotifyAlbumArtist:  "Album Artist",
		SpotifyReleaseDate:  "2019-04-05",
		SpotifyTrackNumber:  7,
		SpotifyDiscNumber:   2,
		PlaylistName:        "My Playlist",
		PlaylistOwner:       "Some Owner",
		Position:            3,
		IncludeTrackNumber:  true,
		UseAlbumTrackNumber: true,
	}
}

// canonical mirrors what backend/downloader.go's on-disk check computes for the
// same track. If buildFilename ever stops delegating, this diverges and fails.
func canonical(p DownloadParams) string {
	artist := p.SpotifyArtistName
	albumArtist := p.SpotifyAlbumArtist
	if p.UseFirstArtistOnly {
		artist = util.GetFirstArtist(artist)
		albumArtist = util.GetFirstArtist(albumArtist)
	}
	return util.BuildExpectedFilename(
		p.SpotifyTrackName, artist, p.SpotifyAlbumName, albumArtist,
		p.SpotifyReleaseDate, p.FilenameFormat,
		p.PlaylistName, p.PlaylistOwner,
		p.IncludeTrackNumber,
		util.ResolveTrackNumber(p.Position, p.SpotifyTrackNumber, p.UseAlbumTrackNumber),
		p.SpotifyDiscNumber,
	)
}

func TestBuildFilenameAgreesWithCanonicalBuilder(t *testing.T) {
	formats := []string{
		// The two that actually regressed. Keep them first.
		"{playlist} - {title}",
		"{creator} - {title}",
		"{playlist}/{creator} - {track}. {title}",
		// And the rest of the vocabulary, so a future edit to either builder
		// cannot quietly change one placeholder's rendering.
		"{title} - {artist}",
		"{track}. {title}",
		"{disc}-{track}. {title} ({year})",
		"{album_artist} - {album} - {title}",
		"{date} {title}",
		"artist-title",
		"title",
		"",
	}

	for _, format := range formats {
		t.Run(format, func(t *testing.T) {
			p := paramsFor(format)
			for _, firstOnly := range []bool{false, true} {
				p.UseFirstArtistOnly = firstOnly
				got := p.buildFilename()
				want := canonical(p)
				if got != want {
					t.Errorf("buildFilename() = %q, canonical builder = %q (UseFirstArtistOnly=%v)",
						got, want, firstOnly)
				}
			}
		})
	}
}

// The placeholders are not merely present in the output — they carry the real
// values. An implementation that stripped them would satisfy the agreement test
// above (both sides would strip), so assert the substitution itself.
func TestBuildFilenameSubstitutesPlaylistAndCreator(t *testing.T) {
	got := paramsFor("{playlist} - {creator} - {title}").buildFilename()
	want := "My Playlist - Some Owner - Song Title.flac"
	if got != want {
		t.Errorf("buildFilename() = %q, want %q", got, want)
	}
}

// Position 3 with UseAlbumTrackNumber must print the album number (7), not the
// list position — the guard and the printed value read one resolved number.
func TestBuildFilenameUsesTheResolvedTrackNumber(t *testing.T) {
	if got, want := paramsFor("{track}. {title}").buildFilename(), "07. Song Title.flac"; got != want {
		t.Errorf("album numbering: buildFilename() = %q, want %q", got, want)
	}

	p := paramsFor("{track}. {title}")
	p.UseAlbumTrackNumber = false
	if got, want := p.buildFilename(), "03. Song Title.flac"; got != want {
		t.Errorf("position numbering: buildFilename() = %q, want %q", got, want)
	}
}
