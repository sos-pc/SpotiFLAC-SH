package amazon

// DownloadParams groups the inputs of AmazonDownloader.DownloadByURL and
// DownloadBySpotifyID. Replaces 25-argument positional signatures.
//
// URL and SpotifyTrackID are mutually exclusive: URL is read by DownloadByURL,
// SpotifyTrackID by DownloadBySpotifyID (which resolves the URL via songlink).
type DownloadParams struct {
	URL            string
	SpotifyTrackID string

	OutputDir      string
	Quality        string
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
	UseSingleGenre       bool
	EmbedGenre           bool
}
