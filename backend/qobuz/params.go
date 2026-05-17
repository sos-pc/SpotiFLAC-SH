package qobuz

// DownloadParams groups the inputs of QobuzDownloader.DownloadTrack and
// DownloadTrackWithISRC. Replaces 24-argument positional signatures.
//
// DeezerISRC is read by DownloadTrackWithISRC; DownloadTrack resolves it
// from SpotifyID before delegating.
type DownloadParams struct {
	DeezerISRC string
	SpotifyID  string

	OutputDir      string
	Quality        string
	FilenameFormat string
	Position       int

	IncludeTrackNumber  bool
	UseAlbumTrackNumber bool
	UseFirstArtistOnly  bool

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

	AllowFallback        bool
	EmbedMaxQualityCover bool
	UseSingleGenre       bool
	EmbedGenre           bool
}
