package storage

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func TestDownloadFromPresignedURLWithProgress(t *testing.T) {
	body := []byte("presigned-object-bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	client := NewPresignedClient(logging.NewLogger())
	var lastProgress int64
	var buf bytes.Buffer
	n, err := client.DownloadFromPresignedURL(context.Background(), srv.URL, &buf, func(downloaded int64) {
		lastProgress = downloaded
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != int64(len(body)) || buf.String() != string(body) {
		t.Fatalf("download mismatch: n=%d content=%q", n, buf.String())
	}
	// progressWriter must report cumulative bytes, ending at the full size.
	if lastProgress != int64(len(body)) {
		t.Fatalf("final progress = %d, want %d", lastProgress, len(body))
	}
}

func TestDownloadFromPresignedURLHTTPErrorRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "denied", http.StatusForbidden)
	}))
	defer srv.Close()

	client := NewPresignedClient(logging.NewLogger())
	var buf bytes.Buffer
	if _, err := client.DownloadFromPresignedURL(context.Background(), srv.URL, &buf, nil); err == nil {
		t.Fatal("a non-2xx presigned GET must error")
	}
}

func TestDownloadToFileFromPresignedURLAtomicRename(t *testing.T) {
	body := []byte("file-payload")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	dst := filepath.Join(t.TempDir(), "nested", "asset.bin")
	client := NewPresignedClient(logging.NewLogger())
	if err := client.DownloadToFileFromPresignedURL(context.Background(), srv.URL, dst, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil || string(got) != string(body) {
		t.Fatalf("downloaded file mismatch: content=%q err=%v", got, err)
	}
	// The .downloading temp must have been renamed away, not left behind.
	if _, err := os.Stat(dst + ".downloading"); !os.IsNotExist(err) {
		t.Fatalf("temp download file should be gone, stat err=%v", err)
	}
}

func TestDownloadToFileFromPresignedURLErrorCleansTemp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	dst := filepath.Join(t.TempDir(), "asset.bin")
	client := NewPresignedClient(logging.NewLogger())
	if err := client.DownloadToFileFromPresignedURL(context.Background(), srv.URL, dst, nil); err == nil {
		t.Fatal("a failed download must return an error")
	}
	// Neither the final file nor the temp must survive a failed download.
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("final file must not exist after failure, stat err=%v", err)
	}
	if _, err := os.Stat(dst + ".downloading"); !os.IsNotExist(err) {
		t.Fatalf("temp file must be cleaned after failure, stat err=%v", err)
	}
}

// Uploading with a progress callback exercises progressReader, the read-side
// counterpart of the download progressWriter.
func TestUploadToPresignedURLWithProgressTracksRead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	const payload = "upload-progress-payload"
	client := NewPresignedClient(logging.NewLogger())
	var lastProgress int64
	err := client.UploadToPresignedURL(context.Background(), srv.URL, strings.NewReader(payload), int64(len(payload)), func(uploaded int64) {
		lastProgress = uploaded
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lastProgress != int64(len(payload)) {
		t.Fatalf("final upload progress = %d, want %d", lastProgress, len(payload))
	}
}
