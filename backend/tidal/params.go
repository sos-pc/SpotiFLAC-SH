package tidal

// DownloadParams groups the inputs of TidalDownloader.Download,
// DownloadByURL and DownloadByURLWithFallback.
//
// Replaces 25-argument positional signatures. URL and SpotifyTrackID are
// mutually exclusive: URL is used by Download* methods that already have a
// Tidal URL; SpotifyTrackID is used by Download() to resolve the URL itself.
type DownloadParams struct {
	// Source — exactly one of these is read depending on the method called.
	URL            string
	SpotifyTrackID string

	// Output
	OutputDir      string
	Quality        string
	FilenameFormat string
	Position       int

	// Filename construction
	IncludeTrackNumber  bool
	UseAlbumTrackNumber bool
	UseFirstArtistOnly  bool

	// Spotify-sourced metadata used for tagging the FLAC file
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

	// Behaviour flags
	AllowFallback        bool
	EmbedMaxQualityCover bool
	UseSingleGenre       bool
	EmbedGenre           bool
}
