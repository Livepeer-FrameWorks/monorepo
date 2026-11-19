package handlers

import (
	"testing"

	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

func TestExplicitMP4CannotRedirectToAnotherFormat(t *testing.T) {
	for _, format := range []string{"HLS", "html", "WEBM", ""} {
		endpoint := &sharedpb.ViewerEndpoint{Protocol: format, Url: "https://edge/other"}
		if got := resolvedViewerProtocolURL(endpoint, "mp4"); got != "" {
			t.Fatalf("MP4 redirected to %s: %s", format, got)
		}
	}
	endpoint := &sharedpb.ViewerEndpoint{Protocol: "HLS", Url: "https://edge/master.m3u8", Outputs: map[string]*sharedpb.OutputEndpoint{"MP4": {Protocol: "MP4", Url: "https://edge/clip.mp4"}}}
	if got := resolvedViewerProtocolURL(endpoint, "mp4"); got != "https://edge/clip.mp4" {
		t.Fatal(got)
	}
	endpoint = &sharedpb.ViewerEndpoint{Protocol: "MP4", Url: "https://edge/clip.mp4"}
	if got := resolvedViewerProtocolURL(endpoint, "mp4"); got != endpoint.Url {
		t.Fatal(got)
	}
}

func TestWebSocketMP4UsesCanonicalCatalogAlias(t *testing.T) {
	endpoint := &sharedpb.ViewerEndpoint{
		Protocol: "HLS", Url: "https://edge/master.m3u8",
		Outputs: map[string]*sharedpb.OutputEndpoint{
			"MEWS": {Protocol: "MEWS", Url: "wss://edge/clip.mp4"},
		},
	}
	if got := resolvedViewerProtocolURL(endpoint, "wsmp4"); got != "wss://edge/clip.mp4" {
		t.Fatalf("explicit WebSocket MP4 lost its canonical MEWS output: %q", got)
	}
	if got := resolvedViewerProtocolURL(endpoint, "mp4"); got != "" {
		t.Fatalf("WebSocket MP4 substituted for progressive HTTP MP4: %q", got)
	}
}
