package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestPublicPlaybackAllowsExternalOrigins(t *testing.T) {
	for _, path := range []string{"/play/playback/hls/index.m3u8", "/resolve/playback/hls/index.m3u8"} {
		for _, method := range []string{http.MethodGet, http.MethodOptions} {
			t.Run(method+path, func(t *testing.T) {
				router := gin.New()
				router.Use(CORSMiddleware([]string{"https://chartroom.frameworks.network"}, false))
				router.GET(path, func(c *gin.Context) { c.JSON(http.StatusNotFound, gin.H{"code": "STREAM_OFFLINE"}) })
				req := httptest.NewRequestWithContext(t.Context(), method, path, nil)
				req.Header.Set("Origin", "https://customer-player.example")
				req.Header.Set("Access-Control-Request-Method", "GET")
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				want := http.StatusNotFound
				if method == http.MethodOptions {
					want = http.StatusNoContent
				}
				if w.Code != want || w.Header().Get("Access-Control-Allow-Origin") != "*" || w.Header().Get("Access-Control-Allow-Credentials") != "" {
					t.Fatalf("external playback blocked: status=%d headers=%v", w.Code, w.Header())
				}
			})
		}
	}
}

func TestPublicPlaybackPreservesRedirectsAndRejectsForeignCookies(t *testing.T) {
	router := gin.New()
	router.Use(CORSMiddleware([]string{"https://chartroom.frameworks.network"}, false))
	router.GET("/play/live/hls", func(c *gin.Context) { c.Redirect(http.StatusTemporaryRedirect, "https://edge.example/live/index.m3u8") })
	for _, cookie := range []bool{false, true} {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/play/live/hls", nil)
		request.Header.Set("Origin", "https://customer-player.example")
		if cookie {
			request.Header.Set("Cookie", "session=foreign")
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		want := http.StatusTemporaryRedirect
		if cookie {
			want = http.StatusForbidden
		}
		if response.Code != want || response.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Fatalf("media CORS or cookie policy changed: %d %v", response.Code, response.Header())
		}
		if !cookie && response.Header().Get("Location") != "https://edge.example/live/index.m3u8" {
			t.Fatal("public CORS altered the prepared viewer redirect")
		}
	}
	for _, path := range []string{"/playback/private", "/resolver/private", "/admin/play/live", "/debug/config"} {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodOptions, path, nil)
		request.Header.Set("Origin", "https://customer-player.example")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden || response.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatalf("public media policy escaped its path boundary: %s", path)
		}
	}
}
