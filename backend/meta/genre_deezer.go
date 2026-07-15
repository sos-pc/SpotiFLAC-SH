package meta

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/afkarxyz/SpotiFLAC/backend/util"
)

// Deezer sits in the middle of the chain: like Apple it matches on the ISRC
// exactly, but its genres live on the ALBUM, not the track. Coarser, and wrong
// for a compilation whose tracks span genres — yet still far better than a
// name-matched guess, and it needs no credentials at all.
//
// Two hops: ISRC identifies the track (which carries its album id), then the
// album carries the genres.
const (
	deezerISRCEndpoint  = "https://api.deezer.com/track/isrc:"
	deezerAlbumEndpoint = "https://api.deezer.com/album/"
)

// deezerGenreNames returns the album's genres for an ISRC. An unknown ISRC or
// an album with no genre set yields an empty slice and no error: a miss, not a
// malfunction.
func deezerGenreNames(isrc string) ([]string, error) {
	var track struct {
		// Deezer answers 200 with an error object rather than an HTTP error.
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Album struct {
			ID int64 `json:"id"`
		} `json:"album"`
	}
	if err := deezerGet(deezerISRCEndpoint+isrc, &track); err != nil {
		return nil, err
	}
	if track.Error != nil || track.Album.ID == 0 {
		return nil, nil
	}

	var album struct {
		Genres struct {
			Data []struct {
				Name string `json:"name"`
			} `json:"data"`
		} `json:"genres"`
	}
	if err := deezerGet(fmt.Sprintf("%s%d", deezerAlbumEndpoint, track.Album.ID), &album); err != nil {
		return nil, err
	}

	names := make([]string, 0, len(album.Genres.Data))
	for _, g := range album.Genres.Data {
		if g.Name != "" {
			names = append(names, g.Name)
		}
	}
	return names, nil
}

func deezerGet(url string, into interface{}) error {
	// Short for the same reason as Apple's: this runs on the download's
	// critical path and must fail fast rather than hang.
	resp, err := util.NewHTTPClient(8 * time.Second).Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("deezer: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, into); err != nil {
		return fmt.Errorf("deezer: decode: %w", err)
	}
	return nil
}
