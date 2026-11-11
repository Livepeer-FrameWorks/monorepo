package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/gin-gonic/gin"
)

func TestAuthClientMetadataUsesTrustedPeerAndBoundsDisplayHints(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(CaptureAuthClientMetadata(nil))
	called := false
	router.GET("/auth/test", func(c *gin.Context) {
		called = true
		info := ctxkeys.GetAuthClientMetadata(c.Request.Context())
		if info.IP != "192.0.2.1" || ctxkeys.GetClientIP(c.Request.Context()) != info.IP {
			t.Errorf("spoofable client IP: %+v", info)
		}
		if info.UserAgent != "Browser/1" || utf8.RuneCountInString(info.DeviceLabel) != 200 || strings.ContainsAny(info.DeviceLabel, "\r\n") || len(info.Version) != 100 {
			t.Errorf("unbounded display hints: %+v", info)
		}
		c.Status(http.StatusOK)
	})
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/auth/test", nil)
	request.RemoteAddr = "192.0.2.1:5678"
	request.Header.Set("X-Forwarded-For", "203.0.113.99")
	request.Header.Set("X-Client-IP", "203.0.113.100")
	request.Header.Set("User-Agent", "Browser/1")
	request.Header.Set("X-Device-Label", strings.Repeat("é", 205)+"\r\n")
	request.Header.Set("X-Client-Version", strings.Repeat("x", 110))
	router.ServeHTTP(httptest.NewRecorder(), request)
	if !called {
		t.Fatal("request metadata handler not invoked")
	}
}
