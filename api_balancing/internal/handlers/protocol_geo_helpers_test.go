package handlers

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"github.com/gin-gonic/gin"
)

// TestFindProtocolURL_FuzzyMatrix pins the protocol→output-URL resolution
// contract for the full alias/fuzzy-match switch. The redirect handler asks for
// a playback protocol by the name a player used in the URL ("dash", "whep",
// "mpegts", ...) and Foghorn must map it to whatever output MistServer actually
// advertised — output names vary across Mist versions, so each case carries its
// own fuzzy substring set. A regression here means a working output silently
// 404s for one protocol while others resolve.
//
// Each case uses an outputs map with exactly ONE output that can match, because
// findProtocolURL ranges over a Go map and returns the first match — with two
// matching outputs the winner is non-deterministic. That map-iteration
// non-determinism is a real caveat of this function (noted here so a future
// reader doesn't add a two-match case and get a flaky test), but in production
// a single Mist node advertises one output per container so it doesn't bite.
func TestFindProtocolURL_FuzzyMatrix(t *testing.T) {
	out := func(name string) map[string]*sharedpb.OutputEndpoint {
		return map[string]*sharedpb.OutputEndpoint{name: {Url: "u://" + name}}
	}
	cases := []struct {
		name       string
		outputName string // the single Mist output advertised
		protocol   string // protocol the player asked for
		want       string
	}{
		// Each alias must reach the canonical container output.
		{"webrtc via whep output", "WHEP", "webrtc", "u://WHEP"},
		{"whep via webrtc output", "WebRTC", "whep", "u://WebRTC"},
		{"html embed", "HTML", "html", "u://HTML"},
		{"embed alias to html", "HTML5_embed", "embed", "u://HTML5_embed"},
		{"mpegts to ts output", "TS", "mpegts", "u://TS"},
		{"ts to mpeg output", "MPEG", "ts", "u://MPEG"},
		{"mp4 progressive", "Progressive_MP4", "mp4", "u://Progressive_MP4"},
		{"webm", "WebM", "webm", "u://WebM"},
		{"mkv via matroska output", "Matroska", "mkv", "u://Matroska"},
		{"matroska alias", "MKV", "matroska", "u://MKV"},
		{"flv via flash output", "Flash_FLV", "flv", "u://Flash_FLV"},
		{"aac", "AAC", "aac", "u://AAC"},
		{"rtsp", "RTSP", "rtsp", "u://RTSP"},
		{"rtmp", "RTMP", "rtmp", "u://RTMP"},
		{"srt", "SRT", "srt", "u://SRT"},
		{"smooth streaming via hss", "HSS", "smoothstreaming", "u://HSS"},
		{"hss alias", "Smooth", "hss", "u://Smooth"},
		{"hds via adobe output", "Adobe_HDS", "hds", "u://Adobe_HDS"},
		{"sdp", "SDP", "sdp", "u://SDP"},
		{"h264 raw", "RAW_H264", "h264", "u://RAW_H264"},
		{"raw alias", "RAWp", "raw", "u://RAWp"},
		{"dtsc via mist output", "MistDTSC", "dtsc", "u://MistDTSC"},
		{"wsmp4 needs ws AND mp4", "WS_MP4", "wsmp4", "u://WS_MP4"},
		{"wswebrtc needs ws AND webrtc", "WebSocket_WebRTC", "wswebrtc", "u://WebSocket_WebRTC"},
		{"case-insensitive direct match", "hls", "HLS", "u://hls"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := findProtocolURL(out(tc.outputName), tc.protocol); got != tc.want {
				t.Fatalf("findProtocolURL(%q) = %q, want %q", tc.protocol, got, tc.want)
			}
		})
	}
}

// TestFindProtocolURL_CompoundProtocolsRequireBothTokens guards the two
// websocket-wrapped protocols whose match condition is an AND of two
// substrings, not an OR. A plain "mp4" or "webrtc" output must NOT satisfy a
// "wsmp4"/"wswebrtc" request — otherwise a websocket-only client would be
// handed a non-websocket URL.
func TestFindProtocolURL_CompoundProtocolsRequireBothTokens(t *testing.T) {
	mp4Only := map[string]*sharedpb.OutputEndpoint{"MP4": {Url: "u://mp4"}}
	if got := findProtocolURL(mp4Only, "wsmp4"); got != "" {
		t.Fatalf("wsmp4 must not match a plain MP4 output, got %q", got)
	}
	webrtcOnly := map[string]*sharedpb.OutputEndpoint{"WebRTC": {Url: "u://webrtc"}}
	if got := findProtocolURL(webrtcOnly, "wswebrtc"); got != "" {
		t.Fatalf("wswebrtc must not match a plain WebRTC output, got %q", got)
	}
}

// TestFindProtocolURL_UnknownProtocolEmpty confirms an unrecognized protocol
// resolves to "" (caller treats empty as "not available") rather than leaking
// an arbitrary output URL.
func TestFindProtocolURL_UnknownProtocolEmpty(t *testing.T) {
	outputs := map[string]*sharedpb.OutputEndpoint{"HLS": {Url: "u://hls"}}
	if got := findProtocolURL(outputs, "totally-unknown"); got != "" {
		t.Fatalf("unknown protocol = %q, want empty", got)
	}
	if got := findProtocolURL(map[string]*sharedpb.OutputEndpoint{}, "hls"); got != "" {
		t.Fatalf("empty outputs = %q, want empty", got)
	}
}

func ginCtxWithReq(req *http.Request) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = req
	return c
}

// TestGetLatLon pins the geo-coordinate source precedence: CloudFlare edge
// headers (most trustworthy, set by our CDN) win over a generic proxy header,
// which wins over a caller-supplied query param; absent any of those the result
// is NaN so downstream distance scoring can detect "no location" rather than
// silently treating the viewer as sitting at (0,0) off the African coast.
func TestGetLatLon(t *testing.T) {
	const cfHeader = "CF-IPLatitude"
	const proxyHeader = "X-Geo-Lat"

	t.Run("cloudflare header wins for lat", func(t *testing.T) {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/?lat=10", nil)
		req.Header.Set(cfHeader, "51.5")
		req.Header.Set(proxyHeader, "20")
		c := ginCtxWithReq(req)
		if got := getLatLon(c, req.URL.Query(), "lat", proxyHeader); got != 51.5 {
			t.Fatalf("got %v, want CF value 51.5", got)
		}
	})

	t.Run("proxy header wins over query when no CF header", func(t *testing.T) {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/?lat=10", nil)
		req.Header.Set(proxyHeader, "20")
		c := ginCtxWithReq(req)
		if got := getLatLon(c, req.URL.Query(), "lat", proxyHeader); got != 20 {
			t.Fatalf("got %v, want proxy header 20", got)
		}
	})

	t.Run("cloudflare header wins for lon", func(t *testing.T) {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/?lon=1", nil)
		req.Header.Set("CF-IPLongitude", "4.35")
		c := ginCtxWithReq(req)
		if got := getLatLon(c, req.URL.Query(), "lon", "X-Geo-Lon"); got != 4.35 {
			t.Fatalf("got %v, want CF longitude 4.35", got)
		}
	})

	t.Run("query param used as last resort", func(t *testing.T) {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/?lon=4.9", nil)
		c := ginCtxWithReq(req)
		// queryKey "lon" so the CF-IPLongitude branch is exercised (absent here).
		if got := getLatLon(c, req.URL.Query(), "lon", "X-Geo-Lon"); got != 4.9 {
			t.Fatalf("got %v, want query 4.9", got)
		}
	})

	t.Run("nothing present yields NaN", func(t *testing.T) {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
		c := ginCtxWithReq(req)
		if got := getLatLon(c, req.URL.Query(), "lat", proxyHeader); !math.IsNaN(got) {
			t.Fatalf("got %v, want NaN", got)
		}
	})

	t.Run("malformed value falls through to next source", func(t *testing.T) {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/?lat=12.5", nil)
		req.Header.Set(cfHeader, "not-a-number")
		c := ginCtxWithReq(req)
		// CF header is present but unparseable -> skip it, fall to query.
		if got := getLatLon(c, req.URL.Query(), "lat", proxyHeader); got != 12.5 {
			t.Fatalf("got %v, want query fallback 12.5", got)
		}
	})
}

// TestGetTagAdjustments pins the tag-weight-adjustment parse precedence (header
// JSON before query JSON) and, critically, that malformed JSON is swallowed
// into an empty map rather than propagating an error or a nil that a caller
// might index. Tag adjustments bias node selection, so a bad value must degrade
// to "no adjustment", never crash routing.
func TestGetTagAdjustments(t *testing.T) {
	t.Run("header JSON wins over query", func(t *testing.T) {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/?tag_adjust=%7B%22q%22%3A1%7D", nil)
		req.Header.Set("X-Tag-Adjust", `{"h":5}`)
		c := ginCtxWithReq(req)
		got := getTagAdjustments(c, req.URL.Query())
		if got["h"] != 5 {
			t.Fatalf("got %v, want header adjustment h=5", got)
		}
		if _, ok := got["q"]; ok {
			t.Fatalf("query adjustment must be ignored when header present: %v", got)
		}
	})

	t.Run("query JSON used when no header", func(t *testing.T) {
		q := url.Values{"tag_adjust": {`{"q":3}`}}
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/?"+q.Encode(), nil)
		c := ginCtxWithReq(req)
		got := getTagAdjustments(c, req.URL.Query())
		if got["q"] != 3 {
			t.Fatalf("got %v, want query adjustment q=3", got)
		}
	})

	t.Run("malformed header JSON degrades to empty, not error", func(t *testing.T) {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
		req.Header.Set("X-Tag-Adjust", "{not json")
		c := ginCtxWithReq(req)
		got := getTagAdjustments(c, req.URL.Query())
		if len(got) != 0 {
			t.Fatalf("malformed header should yield empty map, got %v", got)
		}
	})

	t.Run("nothing present yields empty non-nil map", func(t *testing.T) {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
		c := ginCtxWithReq(req)
		got := getTagAdjustments(c, req.URL.Query())
		if got == nil {
			t.Fatal("expected non-nil empty map")
		}
		if len(got) != 0 {
			t.Fatalf("expected empty map, got %v", got)
		}
	})
}
