package deezer

// DownloadParams groups the inputs of DeezerDownloader.Download.
// Replaces a 24-argument positional signature.
//
// Note: Deezer does not currently consume genre flags (useSingleGenre /
// embedGenre) — they exist on other clients but would be dead fields here.
// MusicBrainz lookup will be added when Deezer FLAC streaming becomes reliable
// enough to justify the extra round-trip.
type DownloadParams struct {
	SpotifyID string

	OutputDir      string
	FilenameFormat string
	Position       int

	IncludeTrackNumber bool
	UseFirstArtistOnly bool

	PlaylistName  string
	PlaylistOwner string

	SpotifyTrackName   string
	SpotifyArtistName  string
	SpotifyAlbumName   string
	SpotifyAlbumArtist string
	SpotifyReleaseDate string
	SpotifyCoverURL    string
	SpotifyTrackNumber int
	SpotifyDiscNumber  int
	SpotifyTotalTracks int
	SpotifyTotalDiscs  int
	SpotifyCopyright   string
	SpotifyPublisher   string
	SpotifyURL         string

	EmbedMaxQualityCover bool
}
