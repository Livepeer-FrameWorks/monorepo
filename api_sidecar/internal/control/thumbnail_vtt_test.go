package control

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestNormalizeThumbnailVTTReferences(t *testing.T) {
	input := `WEBVTT

00:00:00.000 --> 00:00:04.000
/processing+abc123.jpg?track=3#xywh=0,0,120,90

00:00:04.000 --> 00:00:08.000
/live+stream.jpg?track=7#xywh=120,0,120,90
`

	got := normalizeThumbnailVTTReferences(input)
	want := `WEBVTT

00:00:00.000 --> 00:00:04.000
sprite.jpg#xywh=0,0,120,90

00:00:04.000 --> 00:00:08.000
sprite.jpg#xywh=120,0,120,90
`
	if got != want {
		t.Fatalf("normalizeThumbnailVTTReferences() = %q, want %q", got, want)
	}
}

func TestHandleThumbnailUploadResponseDoesNotMarkPartialUploadReady(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fail" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	dir := t.TempDir()
	posterPath := filepath.Join(dir, "poster.jpg")
	spritePath := filepath.Join(dir, "sprite.jpg")
	if err := os.WriteFile(posterPath, []byte("poster"), 0o644); err != nil {
		t.Fatalf("write poster: %v", err)
	}
	if err := os.WriteFile(spritePath, []byte("sprite"), 0o644); err != nil {
		t.Fatalf("write sprite: %v", err)
	}

	var sent bool
	handleThumbnailUploadResponse(logging.NewLogger(), &ipcpb.ThumbnailUploadResponse{
		ThumbnailKey: "stream-1",
		Uploads: []*ipcpb.ThumbnailUploadResponse_PresignedUpload{
			{FileName: "poster.jpg", LocalPath: posterPath, PresignedUrl: server.URL + "/ok", S3Key: "thumbs/stream-1/poster.jpg"},
			{FileName: "sprite.jpg", LocalPath: spritePath, PresignedUrl: server.URL + "/fail", S3Key: "thumbs/stream-1/sprite.jpg"},
		},
	}, func(*ipcpb.ControlMessage) {
		sent = true
	})

	if sent {
		t.Fatal("ThumbnailUploaded was sent after a partial upload")
	}
}
