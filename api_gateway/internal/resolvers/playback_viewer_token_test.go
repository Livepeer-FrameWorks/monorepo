package resolvers

import (
	"context"
	"net/http"
	"testing"
)

func TestPlaybackViewerTokenFromRequest(t *testing.T) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "/query", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("X-Frameworks-Playback-JWT", " viewer-token ")
	if got := playbackViewerTokenFromRequest(req); got != "viewer-token" {
		t.Fatalf("token header got %q", got)
	}

	req.Header.Del("X-Frameworks-Playback-JWT")
	req.Header.Set("X-Playback-JWT", "fallback-token")
	if got := playbackViewerTokenFromRequest(req); got != "fallback-token" {
		t.Fatalf("fallback token header got %q", got)
	}

	req.Header.Del("X-Playback-JWT")
	req.Header.Set("X-Playback-Authorization", "Bearer bearer-token")
	if got := playbackViewerTokenFromRequest(req); got != "bearer-token" {
		t.Fatalf("bearer token got %q", got)
	}
}
