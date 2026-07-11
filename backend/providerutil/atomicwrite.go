package providerutil

import (
	"fmt"
	"io"
	"os"

	"github.com/afkarxyz/SpotiFLAC/backend/util"
)

// DownloadToFileAtomic streams src into a temp file next to finalPath, then
// renames it into place. finalPath either doesn't exist, or is the
// complete download — never a truncated partial file left over from a
// network error or process crash mid-download. Without this, a provider
// client writing straight to finalPath via os.Create+io.Copy leaves a
// truncated file there on any failure, which a caller's later "does this
// file already exist" check (typically just a size/existence check, not a
// checksum) then mistakes for a complete, valid download forever — the
// exists-check doesn't need to get smarter about detecting corruption once
// a corrupt file can no longer land at finalPath in the first place.
//
// speedCallback may be nil; when set, it's invoked the same way
// util.NewProgressWriterWithCallback already reports progress for a
// straight io.Copy.
func DownloadToFileAtomic(finalPath string, src io.Reader, speedCallback func(mbDownloaded, speedMBps float64)) (written int64, err error) {
	tmp := finalPath + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return 0, fmt.Errorf("create temp file: %w", err)
	}

	var dest io.Writer = out
	if speedCallback != nil {
		dest = util.NewProgressWriterWithCallback(out, speedCallback)
	}

	written, copyErr := io.Copy(dest, src)
	closeErr := out.Close()

	if copyErr != nil {
		os.Remove(tmp)
		return written, fmt.Errorf("write temp file: %w", copyErr)
	}
	if closeErr != nil {
		os.Remove(tmp)
		return written, fmt.Errorf("close temp file: %w", closeErr)
	}
	if err := os.Rename(tmp, finalPath); err != nil {
		os.Remove(tmp)
		return written, fmt.Errorf("rename into place: %w", err)
	}
	return written, nil
}
