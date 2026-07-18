package main

import (
	"fmt"

	"github.com/afkarxyz/SpotiFLAC/backend/meta"
)

// MediaService groups the auxiliary media downloads (lyrics, cover, artist
// header/avatar/gallery). Stateless — each call spins up its own meta client.
// Extracted from the former App god-object (R3).
type MediaService struct{}

// LyricsDownloadRequest carries the track's identity plus the per-download
// context. OutputDir, FilenameFormat, TrackNumber and UseAlbumTrackNumber are
// NOT read from the client: the handler overwrites them from the user's server
// settings so a .lrc lands beside its track. See docs/settings-source-of-truth.md D4.
type LyricsDownloadRequest struct {
	SpotifyID   string `json:"spotify_id"`
	TrackName   string `json:"track_name"`
	ArtistName  string `json:"artist_name"`
	AlbumName   string `json:"album_name"`
	AlbumArtist string `json:"album_artist"`
	ReleaseDate string `json:"release_date"`
	// PlaylistName is context, not a setting: it is the folder the enclosing
	// view downloads into, and must match what the track download was given.
	PlaylistName string `json:"playlist_name"`
	// Position is the track's index in the enclosing list; AlbumTrackNumber is
	// its number within its album. Both are sent raw and the SERVER picks
	// between them from its own UseAlbumTrackNumber rule — the client used to
	// collapse the two into one value using its own copy of the setting.
	Position            int    `json:"position"`
	AlbumTrackNumber    int    `json:"album_track_number"`
	DiscNumber          int    `json:"disc_number"`
	OutputDir           string `json:"output_dir"`
	FilenameFormat      string `json:"filename_format"`
	TrackNumber         bool   `json:"track_number"`
	UseAlbumTrackNumber bool   `json:"use_album_track_number"`
}

func (m *MediaService) DownloadLyrics(req LyricsDownloadRequest) (meta.LyricsDownloadResponse, error) {
	if req.SpotifyID == "" {
		return meta.LyricsDownloadResponse{Success: false, Error: "Spotify ID is required"},
			fmt.Errorf("spotify ID is required")
	}
	client := meta.NewLyricsClient()
	backendReq := meta.LyricsDownloadRequest{
		SpotifyID:           req.SpotifyID,
		TrackName:           req.TrackName,
		ArtistName:          req.ArtistName,
		AlbumName:           req.AlbumName,
		AlbumArtist:         req.AlbumArtist,
		ReleaseDate:         req.ReleaseDate,
		OutputDir:           req.OutputDir,
		FilenameFormat:      req.FilenameFormat,
		TrackNumber:         req.TrackNumber,
		Position:            req.Position,
		UseAlbumTrackNumber: req.UseAlbumTrackNumber,
		DiscNumber:          req.DiscNumber,
	}
	resp, err := client.DownloadLyrics(backendReq)
	if err != nil {
		return meta.LyricsDownloadResponse{Success: false, Error: err.Error()}, err
	}
	return *resp, nil
}

// CoverDownloadRequest — same contract as LyricsDownloadRequest: identity and
// context from the client, placement from the server's settings.
type CoverDownloadRequest struct {
	CoverURL     string `json:"cover_url"`
	TrackName    string `json:"track_name"`
	ArtistName   string `json:"artist_name"`
	AlbumName    string `json:"album_name"`
	AlbumArtist  string `json:"album_artist"`
	ReleaseDate  string `json:"release_date"`
	PlaylistName string `json:"playlist_name"`
	// See LyricsDownloadRequest: raw values, the server picks between them.
	Position         int `json:"position"`
	AlbumTrackNumber int `json:"album_track_number"`
	DiscNumber       int `json:"disc_number"`

	OutputDir      string `json:"output_dir"`
	FilenameFormat string `json:"filename_format"`
	TrackNumber    bool   `json:"track_number"`
}

func (m *MediaService) DownloadCover(req CoverDownloadRequest) (meta.CoverDownloadResponse, error) {
	if req.CoverURL == "" {
		return meta.CoverDownloadResponse{Success: false, Error: "Cover URL is required"},
			fmt.Errorf("cover URL is required")
	}
	client := meta.NewCoverClient()
	backendReq := meta.CoverDownloadRequest{
		CoverURL:       req.CoverURL,
		TrackName:      req.TrackName,
		ArtistName:     req.ArtistName,
		AlbumName:      req.AlbumName,
		AlbumArtist:    req.AlbumArtist,
		ReleaseDate:    req.ReleaseDate,
		OutputDir:      req.OutputDir,
		FilenameFormat: req.FilenameFormat,
		TrackNumber:    req.TrackNumber,
		Position:       req.Position,
		DiscNumber:     req.DiscNumber,
	}
	resp, err := client.DownloadCover(backendReq)
	if err != nil {
		return meta.CoverDownloadResponse{Success: false, Error: err.Error()}, err
	}
	return *resp, nil
}

type HeaderDownloadRequest struct {
	HeaderURL  string `json:"header_url"`
	ArtistName string `json:"artist_name"`
	OutputDir  string `json:"output_dir"`
}

func (m *MediaService) DownloadHeader(req HeaderDownloadRequest) (meta.HeaderDownloadResponse, error) {
	if req.HeaderURL == "" {
		return meta.HeaderDownloadResponse{Success: false, Error: "Header URL is required"},
			fmt.Errorf("header URL is required")
	}
	if req.ArtistName == "" {
		return meta.HeaderDownloadResponse{Success: false, Error: "Artist name is required"},
			fmt.Errorf("artist name is required")
	}
	client := meta.NewCoverClient()
	resp, err := client.DownloadHeader(meta.HeaderDownloadRequest{
		HeaderURL:  req.HeaderURL,
		ArtistName: req.ArtistName,
		OutputDir:  req.OutputDir,
	})
	if err != nil {
		return meta.HeaderDownloadResponse{Success: false, Error: err.Error()}, err
	}
	return *resp, nil
}

type GalleryImageDownloadRequest struct {
	ImageURL   string `json:"image_url"`
	ArtistName string `json:"artist_name"`
	ImageIndex int    `json:"image_index"`
	OutputDir  string `json:"output_dir"`
}

func (m *MediaService) DownloadGalleryImage(req GalleryImageDownloadRequest) (meta.GalleryImageDownloadResponse, error) {
	if req.ImageURL == "" {
		return meta.GalleryImageDownloadResponse{Success: false, Error: "Image URL is required"},
			fmt.Errorf("image URL is required")
	}
	if req.ArtistName == "" {
		return meta.GalleryImageDownloadResponse{Success: false, Error: "Artist name is required"},
			fmt.Errorf("artist name is required")
	}
	client := meta.NewCoverClient()
	resp, err := client.DownloadGalleryImage(meta.GalleryImageDownloadRequest{
		ImageURL:   req.ImageURL,
		ArtistName: req.ArtistName,
		ImageIndex: req.ImageIndex,
		OutputDir:  req.OutputDir,
	})
	if err != nil {
		return meta.GalleryImageDownloadResponse{Success: false, Error: err.Error()}, err
	}
	return *resp, nil
}

type AvatarDownloadRequest struct {
	AvatarURL  string `json:"avatar_url"`
	ArtistName string `json:"artist_name"`
	OutputDir  string `json:"output_dir"`
}

func (m *MediaService) DownloadAvatar(req AvatarDownloadRequest) (meta.AvatarDownloadResponse, error) {
	if req.AvatarURL == "" {
		return meta.AvatarDownloadResponse{Success: false, Error: "Avatar URL is required"},
			fmt.Errorf("avatar URL is required")
	}
	if req.ArtistName == "" {
		return meta.AvatarDownloadResponse{Success: false, Error: "Artist name is required"},
			fmt.Errorf("artist name is required")
	}
	client := meta.NewCoverClient()
	resp, err := client.DownloadAvatar(meta.AvatarDownloadRequest{
		AvatarURL:  req.AvatarURL,
		ArtistName: req.ArtistName,
		OutputDir:  req.OutputDir,
	})
	if err != nil {
		return meta.AvatarDownloadResponse{Success: false, Error: err.Error()}, err
	}
	return *resp, nil
}
