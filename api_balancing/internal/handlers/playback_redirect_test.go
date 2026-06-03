package handlers

import (
	"testing"

	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

func TestAppendManifestPath(t *testing.T) {
	tests := []struct {
		name         string
		redirectURL  string
		manifestPath string
		want         string
	}{
		{
			// HLS output is already a complete manifest URL: must NOT double-append.
			name:         "hls complete url is left untouched",
			redirectURL:  "https://edge.example.com/view/hls/stream/index.m3u8",
			manifestPath: "index.m3u8",
			want:         "https://edge.example.com/view/hls/stream/index.m3u8",
		},
		{
			// CMAF output is a bare container dir: LL-HLS manifest gets appended.
			name:         "cmaf base dir gets m3u8 appended",
			redirectURL:  "https://edge.example.com/view/cmaf/stream/",
			manifestPath: "index.m3u8",
			want:         "https://edge.example.com/view/cmaf/stream/index.m3u8",
		},
		{
			// Same CMAF base dir serves DASH via the .mpd manifest path.
			name:         "cmaf base dir gets mpd appended",
			redirectURL:  "https://edge.example.com/view/cmaf/stream/",
			manifestPath: "index.mpd",
			want:         "https://edge.example.com/view/cmaf/stream/index.mpd",
		},
		{
			// Smooth Streaming manifest (no extension) under the CMAF container.
			name:         "cmaf base dir gets Manifest appended",
			redirectURL:  "https://edge.example.com/view/cmaf/stream/",
			manifestPath: "Manifest",
			want:         "https://edge.example.com/view/cmaf/stream/Manifest",
		},
		{
			// Progressive MP4 has no manifest path; URL is returned unchanged.
			name:         "no manifest path returns url unchanged",
			redirectURL:  "https://edge.example.com/view/stream.mp4",
			manifestPath: "",
			want:         "https://edge.example.com/view/stream.mp4",
		},
		{
			// Query string on the resolved output must be preserved, with the
			// manifest segment landing on the path (never after the query).
			name:         "query string preserved on bare dir",
			redirectURL:  "https://edge.example.com/view/cmaf/stream/?token=abc",
			manifestPath: "index.mpd",
			want:         "https://edge.example.com/view/cmaf/stream/index.mpd?token=abc",
		},
		{
			// Already-complete manifest with a query is left untouched.
			name:         "complete manifest with query untouched",
			redirectURL:  "https://edge.example.com/view/hls/stream/index.m3u8?token=abc",
			manifestPath: "index.m3u8",
			want:         "https://edge.example.com/view/hls/stream/index.m3u8?token=abc",
		},
		{
			// Protocol-relative output URL (no scheme) is handled.
			name:         "protocol relative base dir",
			redirectURL:  "//edge.example.com:18090/view/cmaf/live+titan/",
			manifestPath: "index.mpd",
			want:         "//edge.example.com:18090/view/cmaf/live+titan/index.mpd",
		},
		{
			// Base without trailing slash still gets a single separator.
			name:         "missing trailing slash gets single separator",
			redirectURL:  "https://edge.example.com/view/cmaf/stream",
			manifestPath: "index.mpd",
			want:         "https://edge.example.com/view/cmaf/stream/index.mpd",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := appendManifestPath(tt.redirectURL, tt.manifestPath)
			if got != tt.want {
				t.Fatalf("appendManifestPath(%q, %q) = %q, want %q", tt.redirectURL, tt.manifestPath, got, tt.want)
			}
		})
	}
}

func TestParsePlaybackPath(t *testing.T) {
	tests := []struct {
		name                             string
		path                             string
		wantKey, wantProto, wantManifest string
	}{
		{"bare viewkey", "vk", "vk", "", ""},
		{"leading slash stripped", "/vk", "vk", "", ""},
		{"dot protocol", "vk.mp4", "vk", "mp4", ""},
		{"segment protocol + manifest", "vk/hls/index.m3u8", "vk", "hls", "index.m3u8"},
		{"cmaf dash manifest", "vk/cmaf/index.mpd", "vk", "cmaf", "index.mpd"},
		{"cmaf hls manifest", "vk/cmaf/index.m3u8", "vk", "cmaf", "index.m3u8"},
		{"html embed has no manifest", "vk.html", "vk", "html", ""},
		{"empty path -> empty key", "", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k, p, m := parsePlaybackPath(tt.path)
			if k != tt.wantKey || p != tt.wantProto || m != tt.wantManifest {
				t.Fatalf("parsePlaybackPath(%q) = (%q,%q,%q), want (%q,%q,%q)",
					tt.path, k, p, m, tt.wantKey, tt.wantProto, tt.wantManifest)
			}
		})
	}
}

func TestNormalizeProtocol(t *testing.T) {
	cases := map[string]string{
		"m3u8": "hls", "hls": "hls",
		"mpd": "dash", "dash": "dash",
		"cmaf": "cmaf", "llhls": "cmaf", "ll-hls": "cmaf",
		"whep": "webrtc", "webrtc": "webrtc",
		"mp4": "mp4", "progressive": "mp4",
	}
	for in, want := range cases {
		if got := normalizeProtocol(in); got != want {
			t.Errorf("normalizeProtocol(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFindProtocolURL(t *testing.T) {
	outputs := map[string]*pb.OutputEndpoint{
		"HLS":      {Url: "https://edge/view/hls/s/index.m3u8"},
		"DASH":     {Url: "https://edge/view/dash/s/index.mpd"},
		"HLS_CMAF": {Url: "https://edge/view/cmaf/s/"},
		"WHEP":     {Url: "https://edge/webrtc/s"},
	}
	cases := map[string]string{
		"hls":  "https://edge/view/hls/s/index.m3u8", // exact-match wins over HLS_CMAF
		"dash": "https://edge/view/dash/s/index.mpd", // exact
		"cmaf": "https://edge/view/cmaf/s/",          // fuzzy: contains "cmaf"
		"whep": "https://edge/webrtc/s",              // fuzzy: contains "whep"
	}
	for proto, want := range cases {
		if got := findProtocolURL(outputs, proto); got != want {
			t.Errorf("findProtocolURL(%q) = %q, want %q", proto, got, want)
		}
	}
	if got := findProtocolURL(outputs, "bogus"); got != "" {
		t.Errorf("findProtocolURL(bogus) = %q, want empty", got)
	}

	// With no literal DASH output (Mist's default), "dash" falls back to the CMAF
	// container, which serves the .mpd manifest.
	cmafOnly := map[string]*pb.OutputEndpoint{
		"HLS":      {Url: "https://edge/view/hls/s/index.m3u8"},
		"HLS_CMAF": {Url: "https://edge/view/cmaf/s/"},
	}
	if got := findProtocolURL(cmafOnly, "dash"); got != "https://edge/view/cmaf/s/" {
		t.Errorf("findProtocolURL(dash, cmaf-only) = %q, want CMAF base url", got)
	}
}

// The redirect handler appends the manifest path BEFORE the correlation ID, so
// the manifest segment must land on the path and the fwcid stay in the query.
func TestManifestThenCorrelationIDOrdering(t *testing.T) {
	url := appendManifestPath("https://edge/view/cmaf/s/", "index.mpd")
	url = appendCorrelationID(url, "viewer-123")
	want := "https://edge/view/cmaf/s/index.mpd?fwcid=viewer-123"
	if url != want {
		t.Fatalf("ordering = %q, want %q", url, want)
	}
}

func TestIsManifestPath(t *testing.T) {
	manifest := []string{
		"/view/hls/stream/index.m3u8",
		"/view/cmaf/stream/index.mpd",
		"/view/dynamic/manifest.f4m",
		"/view/cmaf/stream/Manifest",
	}
	for _, p := range manifest {
		if !isManifestPath(p) {
			t.Errorf("isManifestPath(%q) = false, want true", p)
		}
	}

	base := []string{
		"/view/cmaf/stream/",
		"/view/cmaf/stream",
		"",
		"/view/stream.mp4",
	}
	for _, p := range base {
		if isManifestPath(p) {
			t.Errorf("isManifestPath(%q) = true, want false", p)
		}
	}
}
