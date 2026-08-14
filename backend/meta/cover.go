package meta

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sos-pc/SpotiFLAC-SH/backend/util"
)

const (
	spotifySize300 = "ab67616d00001e02"
	spotifySize640 = "ab67616d0000b273"
	spotifySizeMax = "ab67616d000082c1"
)

type CoverDownloadRequest struct {
	CoverURL       string `json:"cover_url"`
	TrackName      string `json:"track_name"`
	ArtistName     string `json:"artist_name"`
	AlbumName      string `json:"album_name"`
	AlbumArtist    string `json:"album_artist"`
	ReleaseDate    string `json:"release_date"`
	OutputDir      string `json:"output_dir"`
	FilenameFormat string `json:"filename_format"`
	TrackNumber    bool   `json:"track_number"`
	Position       int    `json:"position"`
	DiscNumber     int    `json:"disc_number"`
}

type CoverDownloadResponse struct {
	Success       bool   `json:"success"`
	Message       string `json:"message"`
	File          string `json:"file,omitempty"`
	Error         string `json:"error,omitempty"`
	AlreadyExists bool   `json:"already_exists,omitempty"`
}

type HeaderDownloadRequest struct {
	HeaderURL  string `json:"header_url"`
	ArtistName string `json:"artist_name"`
	OutputDir  string `json:"output_dir"`
}

type HeaderDownloadResponse struct {
	Success       bool   `json:"success"`
	Message       string `json:"message"`
	File          string `json:"file,omitempty"`
	Error         string `json:"error,omitempty"`
	AlreadyExists bool   `json:"already_exists,omitempty"`
}

type CoverClient struct {
	httpClient *http.Client
}

func NewCoverClient() *CoverClient {
	return &CoverClient{
		httpClient: util.NewHTTPClient(30 * time.Second),
	}
}


func convertSmallToMedium(imageURL string) string {
	if strings.Contains(imageURL, spotifySize300) {
		return strings.Replace(imageURL, spotifySize300, spotifySize640, 1)
	}
	return imageURL
}

func (c *CoverClient) getMaxResolutionURL(imageURL string) string {

	mediumURL := convertSmallToMedium(imageURL)
	if strings.Contains(mediumURL, spotifySize640) {
		return strings.Replace(mediumURL, spotifySize640, spotifySizeMax, 1)
	}
	return mediumURL
}

// downloadImageFile fetches imageURL and writes it to filePath.
// It is the single HTTP-download implementation shared by all cover/header/gallery/avatar methods.
func (c *CoverClient) downloadImageFile(imageURL, filePath string) error {
	resp, err := c.httpClient.Get(imageURL)
	if err != nil {
		return fmt.Errorf("failed to download image: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download image: HTTP %d", resp.StatusCode)
	}

	file, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	if _, err = io.Copy(file, resp.Body); err != nil {
		return fmt.Errorf("failed to write image file: %w", err)
	}
	return nil
}

func (c *CoverClient) DownloadCoverToPath(coverURL, outputPath string, embedMaxQualityCover bool) error {
	if coverURL == "" {
		return fmt.Errorf("cover URL is required")
	}

	downloadURL := convertSmallToMedium(coverURL)
	if embedMaxQualityCover {
		downloadURL = c.getMaxResolutionURL(downloadURL)
	}

	return c.downloadImageFile(downloadURL, outputPath)
}

func (c *CoverClient) DownloadCover(req CoverDownloadRequest) (*CoverDownloadResponse, error) {
	if req.CoverURL == "" {
		return &CoverDownloadResponse{
			Success: false,
			Error:   "Cover URL is required",
		}, fmt.Errorf("cover URL is required")
	}

	outputDir := req.OutputDir
	if outputDir == "" {
		outputDir = util.GetDefaultMusicPath()
	} else {
		outputDir = util.NormalizePath(outputDir)
	}

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return &CoverDownloadResponse{
			Success: false,
			Error:   fmt.Sprintf("failed to create output directory: %v", err),
		}, err
	}

	filenameFormat := req.FilenameFormat
	if filenameFormat == "" {
		// Must equal the audio file's default. This names the cover sidecar,
		// which has to land beside the track it belongs to — diverge and the
		// sidecar sits next to a file with a different name.
		filenameFormat = util.DefaultFilenameTemplate
	}
	filename := buildSidecarFilename(req.TrackName, req.ArtistName, req.AlbumName, req.AlbumArtist, req.ReleaseDate, filenameFormat, req.TrackNumber, req.Position, req.DiscNumber, coverTrackSeparator, ".jpg")
	filePath := filepath.Join(outputDir, filename)

	if fileInfo, err := os.Stat(filePath); err == nil && fileInfo.Size() > 0 {
		return &CoverDownloadResponse{
			Success:       true,
			Message:       "Cover file already exists",
			File:          filePath,
			AlreadyExists: true,
		}, nil
	}

	downloadURL := c.getMaxResolutionURL(req.CoverURL)

	if err := c.downloadImageFile(downloadURL, filePath); err != nil {
		return &CoverDownloadResponse{
			Success: false,
			Error:   err.Error(),
		}, err
	}

	return &CoverDownloadResponse{
		Success: true,
		Message: "Cover downloaded successfully",
		File:    filePath,
	}, nil
}

func (c *CoverClient) DownloadHeader(req HeaderDownloadRequest) (*HeaderDownloadResponse, error) {
	if req.HeaderURL == "" {
		return &HeaderDownloadResponse{
			Success: false,
			Error:   "Header URL is required",
		}, fmt.Errorf("header URL is required")
	}

	if req.ArtistName == "" {
		return &HeaderDownloadResponse{
			Success: false,
			Error:   "Artist name is required",
		}, fmt.Errorf("artist name is required")
	}

	outputDir := req.OutputDir
	if outputDir == "" {
		outputDir = util.GetDefaultMusicPath()
	} else {
		outputDir = util.NormalizePath(outputDir)
	}

	artistFolder := filepath.Join(outputDir, util.SanitizeFilename(req.ArtistName))
	if err := os.MkdirAll(artistFolder, 0755); err != nil {
		return &HeaderDownloadResponse{
			Success: false,
			Error:   fmt.Sprintf("failed to create artist folder: %v", err),
		}, err
	}

	filename := util.SanitizeFilename(req.ArtistName) + "_Header.jpg"
	filePath := filepath.Join(artistFolder, filename)

	if fileInfo, err := os.Stat(filePath); err == nil && fileInfo.Size() > 0 {
		return &HeaderDownloadResponse{
			Success:       true,
			Message:       "Header file already exists",
			File:          filePath,
			AlreadyExists: true,
		}, nil
	}

	if err := c.downloadImageFile(req.HeaderURL, filePath); err != nil {
		return &HeaderDownloadResponse{
			Success: false,
			Error:   err.Error(),
		}, err
	}

	return &HeaderDownloadResponse{
		Success: true,
		Message: "Header downloaded successfully",
		File:    filePath,
	}, nil
}

type GalleryImageDownloadRequest struct {
	ImageURL   string `json:"image_url"`
	ArtistName string `json:"artist_name"`
	ImageIndex int    `json:"image_index"`
	OutputDir  string `json:"output_dir"`
}

type GalleryImageDownloadResponse struct {
	Success       bool   `json:"success"`
	Message       string `json:"message"`
	File          string `json:"file,omitempty"`
	Error         string `json:"error,omitempty"`
	AlreadyExists bool   `json:"already_exists,omitempty"`
}

func (c *CoverClient) DownloadGalleryImage(req GalleryImageDownloadRequest) (*GalleryImageDownloadResponse, error) {
	if req.ImageURL == "" {
		return &GalleryImageDownloadResponse{
			Success: false,
			Error:   "Image URL is required",
		}, fmt.Errorf("image URL is required")
	}

	if req.ArtistName == "" {
		return &GalleryImageDownloadResponse{
			Success: false,
			Error:   "Artist name is required",
		}, fmt.Errorf("artist name is required")
	}

	outputDir := req.OutputDir
	if outputDir == "" {
		outputDir = util.GetDefaultMusicPath()
	} else {
		outputDir = util.NormalizePath(outputDir)
	}

	artistFolder := filepath.Join(outputDir, util.SanitizeFilename(req.ArtistName))
	if err := os.MkdirAll(artistFolder, 0755); err != nil {
		return &GalleryImageDownloadResponse{
			Success: false,
			Error:   fmt.Sprintf("failed to create artist folder: %v", err),
		}, err
	}

	filename := util.SanitizeFilename(req.ArtistName) + fmt.Sprintf("_Gallery_%d.jpg", req.ImageIndex+1)
	filePath := filepath.Join(artistFolder, filename)

	if fileInfo, err := os.Stat(filePath); err == nil && fileInfo.Size() > 0 {
		return &GalleryImageDownloadResponse{
			Success:       true,
			Message:       "Gallery image file already exists",
			File:          filePath,
			AlreadyExists: true,
		}, nil
	}

	if err := c.downloadImageFile(req.ImageURL, filePath); err != nil {
		return &GalleryImageDownloadResponse{
			Success: false,
			Error:   err.Error(),
		}, err
	}

	return &GalleryImageDownloadResponse{
		Success: true,
		Message: "Gallery image downloaded successfully",
		File:    filePath,
	}, nil
}

type AvatarDownloadRequest struct {
	AvatarURL  string `json:"avatar_url"`
	ArtistName string `json:"artist_name"`
	OutputDir  string `json:"output_dir"`
}

type AvatarDownloadResponse struct {
	Success       bool   `json:"success"`
	Message       string `json:"message"`
	File          string `json:"file,omitempty"`
	Error         string `json:"error,omitempty"`
	AlreadyExists bool   `json:"already_exists,omitempty"`
}

func (c *CoverClient) DownloadAvatar(req AvatarDownloadRequest) (*AvatarDownloadResponse, error) {
	if req.AvatarURL == "" {
		return &AvatarDownloadResponse{
			Success: false,
			Error:   "Avatar URL is required",
		}, fmt.Errorf("avatar URL is required")
	}

	if req.ArtistName == "" {
		return &AvatarDownloadResponse{
			Success: false,
			Error:   "Artist name is required",
		}, fmt.Errorf("artist name is required")
	}

	outputDir := req.OutputDir
	if outputDir == "" {
		outputDir = util.GetDefaultMusicPath()
	} else {
		outputDir = util.NormalizePath(outputDir)
	}

	artistFolder := filepath.Join(outputDir, util.SanitizeFilename(req.ArtistName))
	if err := os.MkdirAll(artistFolder, 0755); err != nil {
		return &AvatarDownloadResponse{
			Success: false,
			Error:   fmt.Sprintf("failed to create artist folder: %v", err),
		}, err
	}

	filename := util.SanitizeFilename(req.ArtistName) + "_Avatar.jpg"
	filePath := filepath.Join(artistFolder, filename)

	if fileInfo, err := os.Stat(filePath); err == nil && fileInfo.Size() > 0 {
		return &AvatarDownloadResponse{
			Success:       true,
			Message:       "Avatar file already exists",
			File:          filePath,
			AlreadyExists: true,
		}, nil
	}

	if err := c.downloadImageFile(req.AvatarURL, filePath); err != nil {
		return &AvatarDownloadResponse{
			Success: false,
			Error:   err.Error(),
		}, err
	}

	return &AvatarDownloadResponse{
		Success: true,
		Message: "Avatar downloaded successfully",
		File:    filePath,
	}, nil
}
