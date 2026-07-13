package providerutil

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestGetToFileWritesBodyOn200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("hello-flac"))
	}))
	defer srv.Close()

	dst := filepath.Join(t.TempDir(), "out.flac")
	n, err := GetToFile(srv.Client(), srv.URL, dst, nil)
	if err != nil {
		t.Fatalf("GetToFile: %v", err)
	}
	if n != int64(len("hello-flac")) {
		t.Errorf("written = %d, want %d", n, len("hello-flac"))
	}
	b, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(b) != "hello-flac" {
		t.Errorf("content = %q, want %q", b, "hello-flac")
	}
}

func TestGetToFileErrorsOnNon200WithoutWritingFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("<html>not found</html>"))
	}))
	defer srv.Close()

	dst := filepath.Join(t.TempDir(), "out.flac")
	if _, err := GetToFile(srv.Client(), srv.URL, dst, nil); err == nil {
		t.Fatal("expected an error on a 404 response")
	}
	// The atomic writer must not leave the error-page body behind as a
	// "downloaded" file — a later exists-check would mistake it for a track.
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Errorf("dst should not exist after a non-200, stat err = %v", err)
	}
}
