package relay

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestWarmTransportStreamContentType(t *testing.T) {
	for _, extension := range []string{"ts", "mts", "m2ts", "MTS", "M2TS"} {
		t.Run(extension, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "source."+extension)
			packet := make([]byte, 192)
			packet[4] = 0x47
			if err := os.WriteFile(file, packet, 0600); err != nil {
				t.Fatal(err)
			}
			// Explicit classification must not depend on the host MIME database.
			if got := contentTypeForFile(file); got != "video/mp2t" {
				t.Errorf("transport stream has no stable MIME mapping: %q", got)
			}
			response := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(response)
			ctx.Request = httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/source."+extension, nil)
			ctx.Request.Header.Set("Range", "bytes=0-191")
			if served := (&Server{}).serveWarmIfPresent(ctx, file); !served {
				t.Fatal("cached transport stream was not served")
			}
			if response.Code != http.StatusPartialContent || response.Header().Get("Content-Type") != "video/mp2t" || !bytes.Equal(response.Body.Bytes(), packet) {
				t.Fatalf("transport response: status=%d type=%q bytes=%d", response.Code, response.Header().Get("Content-Type"), response.Body.Len())
			}
		})
	}
}
