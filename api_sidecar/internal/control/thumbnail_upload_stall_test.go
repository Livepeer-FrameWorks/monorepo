package control

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// Staging rc16: during a clip burst, object storage left individual thumbnail PUTs without response headers while
// other keys answered at once, and the retry of the same key stalled too. A stalled key must not cost the other
// files of the batch their uploads, and the stalled key must still get the retries that let it land once the store
// answers again.
func TestHandleThumbnailUploadResponseSurvivesStalledObjectStorePUTs(t *testing.T) {
	var mu sync.Mutex
	puts := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		puts[r.URL.Path]++
		n := puts[r.URL.Path]
		mu.Unlock()
		if r.URL.Path == "/sprite.jpg" && n <= 2 {
			// Never answer: the client has to give up on this attempt.
			<-r.Context().Done()
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	dir := t.TempDir()
	uploads := make([]*ipcpb.ThumbnailUploadResponse_PresignedUpload, 0, 3)
	for _, name := range []string{"poster.jpg", "sprite.jpg", "sprite.vtt"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("thumbnail bytes for "+name), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		uploads = append(uploads, &ipcpb.ThumbnailUploadResponse_PresignedUpload{
			FileName: name, LocalPath: path, PresignedUrl: server.URL + "/" + name, S3Key: "thumbnails/stream-1/.staging/a1/" + name,
		})
	}

	var sent *ipcpb.ThumbnailUploaded
	done := make(chan struct{})
	go func() {
		defer close(done)
		handleThumbnailUploadResponse(logging.NewLogger(), &ipcpb.ThumbnailUploadResponse{
			ThumbnailKey: "stream-1", AttemptId: "a1", Uploads: uploads,
		}, func(m *ipcpb.ControlMessage) {
			sent = m.GetThumbnailUploaded()
		})
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Minute):
		t.Fatal("thumbnail upload did not finish")
	}

	mu.Lock()
	defer mu.Unlock()
	for _, name := range []string{"poster.jpg", "sprite.jpg", "sprite.vtt"} {
		if puts["/"+name] == 0 {
			t.Errorf("%s was never sent to object storage (puts=%v)", name, puts)
		}
	}
	if sent == nil {
		t.Fatalf("ThumbnailUploaded was not sent although object storage accepted every file on retry (puts=%v)", puts)
	}
	if len(sent.GetS3Keys()) != 3 || sent.GetAttemptId() != "a1" {
		t.Fatalf("ThumbnailUploaded = %+v, want all three staged keys for attempt a1", sent)
	}
}
