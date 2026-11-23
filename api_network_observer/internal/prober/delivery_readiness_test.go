package prober

import (
	"github.com/Livepeer-FrameWorks/monorepo/pkg/telemetryspool"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecentWorkDoesNotHideUnreadableSpool(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "spool")
	q, err := telemetryspool.Open(telemetryspool.Config{Directory: dir})
	if err != nil {
		t.Fatal(err)
	}
	p := &Prober{Queue: q}
	p.LastWork.Store(time.Now().Unix())
	if renameErr := os.Rename(dir, filepath.Join(root, "offline")); renameErr != nil {
		t.Fatal(renameErr)
	}
	response := httptest.NewRecorder()
	p.Health().ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/ready", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("fresh work masked unreadable delivery state: ready=%d", response.Code)
	}
}
