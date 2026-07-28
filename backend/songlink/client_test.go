package songlink

import "testing"

// getDeezerISRC extracts a track ID from a Deezer URL before it calls the API.
// A silent mis-parse here is worse than an error: it would send a request for
// the wrong track and return an ISRC belonging to something else, which then
// gets embedded in the file and used to look the track up on Tidal. So the
// shapes that must NOT be accepted are what this pins.
//
// Only the parse is covered — the request itself builds its own HTTP client, so
// the success path is not reachable without network. The rest of the package's
// tests went with GetAllURLsFromSpotify and searchITunes in item 7.
func TestGetDeezerISRCRejectsURLsWithoutATrackID(t *testing.T) {
	for _, url := range []string{
		"",
		"https://www.deezer.com/album/12345",
		"https://www.deezer.com/track/",
		"https://www.deezer.com/track/?utm=x",
		"https://www.deezer.com/fr/artist/123",
	} {
		if _, err := getDeezerISRC(url); err == nil {
			t.Errorf("getDeezerISRC(%q) = nil error; want a parse failure rather than a request for the wrong track", url)
		}
	}
}
