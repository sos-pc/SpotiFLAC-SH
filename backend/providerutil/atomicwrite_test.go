package providerutil

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDownloadToFileAtomicWritesCompleteFile(t *testing.T) {
	dir := t.TempDir()
	finalPath := filepath.Join(dir, "track.flac")

	written, err := DownloadToFileAtomic(finalPath, strings.NewReader("audio bytes"), nil)
	if err != nil {
		t.Fatalf("DownloadToFileAtomic: %v", err)
	}
	if written != int64(len("audio bytes")) {
		t.Errorf("written = %d, want %d", written, len("audio bytes"))
	}

	got, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "audio bytes" {
		t.Errorf("content = %q, want %q", got, "audio bytes")
	}

	// No leftover temp file.
	if _, err := os.Stat(finalPath + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("expected no leftover .tmp file, stat err = %v", err)
	}
}

// erroringReader fails partway through, simulating a network drop
// mid-download — the regression scenario for Providers-1 (Qobuz writing a
// truncated file straight to its final path, which a later size-only
// exists-check then mistook for a complete download forever).
type erroringReader struct {
	data []byte
	n    int
}

func (r *erroringReader) Read(p []byte) (int, error) {
	if r.n >= len(r.data) {
		return 0, errors.New("simulated network drop")
	}
	n := copy(p, r.data[r.n:])
	r.n += n
	return n, nil
}

func TestDownloadToFileAtomicLeavesNoPartialFileOnFailure(t *testing.T) {
	dir := t.TempDir()
	finalPath := filepath.Join(dir, "track.flac")

	_, err := DownloadToFileAtomic(finalPath, &erroringReader{data: []byte("partial")}, nil)
	if err == nil {
		t.Fatal("expected an error from the failing reader, got nil")
	}

	// The whole point: finalPath must not exist at all after a failed
	// download — not a 0-byte file, not a truncated one.
	if _, statErr := os.Stat(finalPath); !os.IsNotExist(statErr) {
		t.Errorf("finalPath exists after a failed download (stat err = %v) — a caller's exists-check would mistake this for a complete file", statErr)
	}
	if _, statErr := os.Stat(finalPath + ".tmp"); !os.IsNotExist(statErr) {
		t.Errorf("leftover .tmp file after a failed download (stat err = %v)", statErr)
	}
}

func TestDownloadToFileAtomicReportsProgress(t *testing.T) {
	dir := t.TempDir()
	finalPath := filepath.Join(dir, "track.flac")

	// util.ProgressWriter only invokes its callback every 256KB — this must
	// clear that threshold at least once to observe any callback at all.
	payload := strings.Repeat("x", 300*1024)

	var lastMB float64
	calls := 0
	_, err := DownloadToFileAtomic(finalPath, strings.NewReader(payload), func(mbDownloaded, speedMBps float64) {
		calls++
		lastMB = mbDownloaded
	})
	if err != nil {
		t.Fatalf("DownloadToFileAtomic: %v", err)
	}
	if calls == 0 {
		t.Error("speedCallback was never invoked")
	}
	if lastMB <= 0 {
		t.Errorf("last reported mbDownloaded = %v, want > 0", lastMB)
	}
}

func TestDownloadToFileAtomicOverwritesExistingFile(t *testing.T) {
	dir := t.TempDir()
	finalPath := filepath.Join(dir, "track.flac")
	if err := os.WriteFile(finalPath, []byte("old content"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if _, err := DownloadToFileAtomic(finalPath, strings.NewReader("new content"), nil); err != nil {
		t.Fatalf("DownloadToFileAtomic: %v", err)
	}

	got, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "new content" {
		t.Errorf("content = %q, want %q (redownload should replace an existing file)", got, "new content")
	}
}

var _ io.Reader = (*erroringReader)(nil)
