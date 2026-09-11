package mist

import (
	"net/url"
	"testing"
)

func TestPlaybackProtocolSeparatesRequestsFromConnectorObservations(t *testing.T) {
	for requested, want := range map[string]string{
		"MIST_WEBRTC": "webrtc", "wswebrtc": "webrtc", "WHEP": "whep", "mews": "wsmp4", "wsmp4": "wsmp4",
		"hls_cmaf": "cmaf", "hss": "smoothstreaming", "smoothstreaming": "smoothstreaming", "raw_ws": "raw_ws",
		"json_ws": "json_ws", "h264_ws": "h264_ws", "mews_webm": "mews_webm", "http": "mist_html",
		"hls": "hls", "HLS,HTTP": "", "json": "", "unknown": "", "": "",
	} {
		if got := PlaybackProtocol(requested); got != want {
			t.Errorf("PlaybackProtocol(%q)=%q, want %q", requested, got, want)
		}
		if want != "" && PlaybackProtocol(want) != want {
			t.Errorf("canonical protocol %q is not stable", want)
		}
	}
	if _, err := ViewerProtocol("json_ws"); err == nil {
		t.Fatal("request normalization broadened trusted connector evidence")
	}
}

func TestResolvePlaybackURLUsesReportedListenersAndExactIdentity(t *testing.T) {
	outputs := map[string]any{
		"HTTP": "http://HOST:18080/$.html", "WebRTC": "ws://HOST:18203/webrtc/$",
		"HLS": "http://HOST:18080/hls/$/index.m3u8", "CMAF": "http://HOST:18080/cmaf/$/",
		"MP4": "http://HOST:18080/$.mp4", "MKV": "http://HOST:18080/$.mkv",
		"RTMP": "rtmp://HOST:11935/play/$", "RTSP": "rtsp://HOST:1554/$", "DTSC": "dtsc://HOST:14200/$",
		"TSSRT": "srt://HOST:18889/?streamid=$",
	}
	base := "https://[2001:db8::1]:8443/view%2Ftenant"
	for protocol, want := range map[string]string{
		"hls":    "https://[2001:db8::1]:8443/view%2Ftenant/hls/live+stream%2Fpart%3F/index.m3u8",
		"dash":   "https://[2001:db8::1]:8443/view%2Ftenant/cmaf/live+stream%2Fpart%3F/index.mpd",
		"cmaf":   "https://[2001:db8::1]:8443/view%2Ftenant/cmaf/live+stream%2Fpart%3F/index.m3u8",
		"whep":   "https://[2001:db8::1]:8443/view%2Ftenant/whep/live+stream%2Fpart%3F",
		"webrtc": "wss://[2001:db8::1]:8443/view%2Ftenant/webrtc/live+stream%2Fpart%3F",
		"mp4":    "https://[2001:db8::1]:8443/view%2Ftenant/live+stream%2Fpart%3F.mp4",
		"mews":   "wss://[2001:db8::1]:8443/view%2Ftenant/live+stream%2Fpart%3F.mp4",
		"webm":   "https://[2001:db8::1]:8443/view%2Ftenant/live+stream%2Fpart%3F.mkv",
		"rtmp":   "rtmp://[2001:db8::1]:11935/play/live+stream%2Fpart%3F",
		"rtsp":   "rtsp://[2001:db8::1]:1554/live+stream%2Fpart%3F",
		"dtsc":   "dtsc://[2001:db8::1]:14200/live+stream%2Fpart%3F",
	} {
		t.Run(protocol, func(t *testing.T) {
			if got := ResolvePlaybackURL(outputs, base, protocol, "live+stream/part?"); got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		})
	}
	srt, err := url.Parse(ResolvePlaybackURL(outputs, base, "srt", "view?&key=other"))
	if err != nil || srt.Host != "[2001:db8::1]:18889" || srt.Query().Get("streamid") != "view?&key=other" || len(srt.Query()) != 1 {
		t.Fatalf("invalid SRT identity: %+v, %v", srt, err)
	}
}

// A DTSC connector without an explicit port listens on 4200; the resolved URL
// must carry that port so the stream advertisement, the placement claim and the
// arranged origin pull all name the listener with one string.
func TestResolvePlaybackURLCanonicalizesDefaultDTSCPort(t *testing.T) {
	outputs := map[string]any{"DTSC": "dtsc://HOST/$", "RTMP": "rtmp://HOST/play/$"}
	if got := ResolvePlaybackURL(outputs, "http://edge.example:18090/view", "dtsc", "live+key"); got != "dtsc://edge.example:4200/live+key" {
		t.Fatalf("DTSC without port resolved to %q", got)
	}
	if got := ResolvePlaybackURL(map[string]any{"DTSC": "dtsc://origin.example/$"}, "http://edge.example", "dtsc", "live+key"); got != "dtsc://origin.example:4200/live+key" {
		t.Fatalf("explicit DTSC host without port resolved to %q", got)
	}
	if got := ResolvePlaybackURL(outputs, "http://edge.example:18090/view", "rtmp", "live+key"); got != "rtmp://edge.example/play/live+key" {
		t.Fatalf("RTMP must not gain a DTSC port: %q", got)
	}
}

func TestResolvePlaybackURLPreservesPublicOverridesWithoutDoublePrefix(t *testing.T) {
	for _, raw := range []string{"https://media.example:7443/custom%2Fpath/hls/$/index.m3u8", "http://HOST:18080/custom%2Fpath/hls/$/index.m3u8"} {
		want := "https://media.example:7443/custom%2Fpath/hls/key/index.m3u8"
		base := "https://fallback.example:8443/view"
		if raw[0:11] == "http://HOST" {
			want = "https://fallback.example:8443/custom%2Fpath/hls/key/index.m3u8"
		}
		if got := ResolvePlaybackURL(map[string]any{"HLS": raw}, base, "hls", "key"); got != want {
			t.Fatalf("public override changed: %q", got)
		}
	}
	outputs := map[string]any{"HTTP": "https://media.example/view%2Ftenant/$.html", "WebRTC": "ws://HOST:18203/webrtc/$"}
	if got := ResolvePlaybackURL(outputs, "https://fallback.example", "whep", "key"); got != "https://media.example/view%2Ftenant/whep/key" {
		t.Fatalf("WHEP escaped prefix changed: %q", got)
	}
}

func TestResolvePlaybackURLDoesNotInventUnreportedProtocols(t *testing.T) {
	for _, protocol := range []string{"hls", "mp4", "whep", "webrtc", "dash", "rtmp", "dtsc", "srt"} {
		if got := ResolvePlaybackURL(map[string]any{"HTTP": "http://HOST:8080/$.html"}, "https://edge.example", protocol, "key"); got != "" {
			t.Fatalf("HTTP invented %s: %q", protocol, got)
		}
	}
	if got := ResolvePlaybackURL(map[string]any{"WebRTC": "ws://HOST:18203/webrtc/$"}, "https://edge.example", "webrtc", "key"); got != "" {
		t.Fatalf("UDP listener became signalling URL: %q", got)
	}
	if got := ResolvePlaybackURL(map[string]any{"HLS": "https://media/hls/$/index.m3u8"}, "https://edge", "HLS", "key"); got != "" {
		t.Fatal("noncanonical protocol accepted")
	}
}

func TestResolvePlaybackURLSelectsExplicitHTTPSListener(t *testing.T) {
	outputs := map[string]any{"HTTP": "http://media:8080/$.html", "HTTPS": "https://media:8443/$.html"}
	if got := ResolvePlaybackURL(outputs, "https://fallback", "http", "key"); got != "https://media:8443/key.html" {
		t.Fatalf("dual listeners became ambiguous: %q", got)
	}
	outputs["HTTPS"] = true
	if got := ResolvePlaybackURL(outputs, "https://fallback", "http", "key"); got != "" {
		t.Fatalf("malformed explicit HTTPS listener downgraded: %q", got)
	}
}

func TestResolvePlaybackURLRejectsAmbiguousOrUnsafeReports(t *testing.T) {
	for name, raw := range map[string]any{
		"userinfo":         "https://secret:password@media/hls/$/index.m3u8",
		"fragment":         "https://media/hls/$/index.m3u8#fragment",
		"query":            "https://media/hls/$/index.m3u8?token=private",
		"empty_query":      "https://media/hls/$/index.m3u8?",
		"scheme":           "file://media/hls/$/index.m3u8",
		"bad_port":         "https://media:99999/hls/$/index.m3u8",
		"no_identity":      "https://media/hls/fixed/index.m3u8",
		"two_identities":   "https://media/$/hls/$/index.m3u8",
		"encoded_identity": "https://media/%24/hls/$/index.m3u8",
		"host_identity":    "https://$.media/hls/fixed/index.m3u8",
		"boolean":          true,
		"two_addresses":    []any{"https://one/hls/$/index.m3u8", "https://two/hls/$/index.m3u8"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := ResolvePlaybackURL(map[string]any{"HLS": raw}, "https://edge", "hls", "key"); got != "" {
				t.Fatalf("unsafe report accepted: %q", got)
			}
		})
	}
	for _, base := range []string{"https://$host.example", "https://edge/$", "https://edge/%24", "https://edge?private=key", "https://edge#fragment", "https://u:p@edge"} {
		if got := ResolvePlaybackURL(map[string]any{"HLS": "http://HOST:8080/hls/$/index.m3u8"}, base, "hls", "key"); got != "" {
			t.Fatalf("unsafe base %q accepted: %q", base, got)
		}
	}
	outputs := map[string]any{"CMAF": "https://media/cmaf/$/", "DASH": true}
	if got := ResolvePlaybackURL(outputs, "https://edge", "dash", "key"); got != "" {
		t.Fatalf("malformed explicit DASH bypassed: %q", got)
	}
	outputs = map[string]any{"HTTP": "http://HOST:8080/$.html", "WebRTC": "ws://HOST:8200/webrtc/$", "WHEP": true}
	if got := ResolvePlaybackURL(outputs, "https://edge", "whep", "key"); got != "" {
		t.Fatalf("malformed explicit WHEP bypassed: %q", got)
	}
}
