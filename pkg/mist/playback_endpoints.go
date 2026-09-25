package mist

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

// AutoPlaybackProtocol is an internal placement capability requirement. It
// means that a node must expose at least one browser playback output; it does
// not choose or constrain the protocol used by the eventual viewer.
const AutoPlaybackProtocol = "auto"

// PlaybackProtocol normalizes a requested playback format, not a trusted Mist
// connector observation. Listener support must still be checked separately.
func PlaybackProtocol(requested string) string {
	switch strings.ToLower(strings.TrimSpace(requested)) {
	case "webrtc", "mist_webrtc", "wswebrtc":
		return "webrtc"
	case "mews", "wsmp4":
		return "wsmp4"
	case "hls_cmaf", "cmaf":
		return "cmaf"
	case "hss", "smoothstreaming":
		return "smoothstreaming"
	case "http", "mist_html":
		return "mist_html"
	case "hls", "dash", "whep", "rtmp", "rtsp", "dtsc", "srt", "mp4", "webm", "mkv", "mews_webm", "ts", "aac", "h264", "h264_ws", "raw_ws", "json_ws", "flv", "hds", "sdp":
		return strings.ToLower(strings.TrimSpace(requested))
	default:
		return ""
	}
}

// ResolvePlaybackURL resolves one protocol from reported listeners. Unlike a
// player output catalog, it does not infer HLS, WHEP or progressive support from
// a public HTTP hostname alone. Identity is escaped as one path/query component.
func ResolvePlaybackURL(outputs map[string]any, publicBase, protocol, streamName string) string {
	if streamName == "" || strings.TrimSpace(streamName) != streamName || strings.IndexFunc(streamName, unicode.IsControl) >= 0 || streamName == "." || streamName == ".." {
		return ""
	}
	if protocol == AutoPlaybackProtocol {
		for _, candidate := range []string{"hls", "whep", "webrtc", "dash", "cmaf", "mp4", "wsmp4", "webm"} {
			if endpoint := ResolvePlaybackURL(outputs, publicBase, candidate, streamName); endpoint != "" {
				return endpoint
			}
		}
		return ""
	}
	template := playbackTemplate(outputs, protocol)
	if protocol == "srt" {
		return resolveIngestTemplate(template, publicBase, streamName, "srt")
	}
	u := parseIngestTemplate(template)
	if u == nil || u.RawQuery != "" || u.ForceQuery || strings.Count(u.Path, "$") != 1 || strings.ContainsAny(u.Host, "$\\ \t\r\n") {
		return ""
	}
	websocket := protocol == "webrtc" || protocol == "mist_webrtc" || protocol == "wswebrtc" || protocol == "mews" || protocol == "wsmp4" || protocol == "mews_webm" || protocol == "raw_ws" || protocol == "h264_ws" || protocol == "json_ws"
	direct := protocol == "rtmp" || protocol == "rtsp" || protocol == "dtsc"
	if direct {
		if u.Scheme != protocol && (protocol != "rtmp" || u.Scheme != "rtmps") {
			return ""
		}
	} else if u.Scheme != "http" && u.Scheme != "https" && (!websocket || (u.Scheme != "ws" && u.Scheme != "wss")) {
		return ""
	}
	if u.Hostname() == "HOST" {
		base, err := url.Parse(publicBase)
		if err != nil || base.Hostname() == "" || base.Hostname() == "HOST" || base.User != nil || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" || base.Opaque != "" || strings.Contains(base.Path, "$") ||
			(base.Scheme != "http" && base.Scheme != "https") {
			return ""
		}
		if port := base.Port(); port != "" {
			value, parseErr := strconv.Atoi(port)
			if parseErr != nil || value < 1 || value > 65535 {
				return ""
			}
		}
		if direct {
			if u.Port() != "" {
				u.Host = net.JoinHostPort(base.Hostname(), u.Port())
			} else {
				u.Host = base.Hostname()
				if strings.Contains(u.Host, ":") {
					u.Host = "[" + u.Host + "]"
				}
			}
		} else {
			u.Scheme, u.Host = base.Scheme, base.Host
			if unprefixedPlaybackPath(u.Path) {
				rawPath := strings.TrimRight(base.EscapedPath(), "/") + u.EscapedPath()
				u.Path = strings.TrimRight(base.Path, "/") + u.Path
				u.RawPath = rawPath
			}
		}
	}
	if strings.ContainsAny(u.Host, "$\\ \t\r\n") {
		return ""
	}
	if websocket {
		switch u.Scheme {
		case "http":
			u.Scheme = "ws"
		case "https":
			u.Scheme = "wss"
		}
	}
	// DTSC listeners default to 4200 when the connector has no explicit port.
	// Every DTSC URL that names the same listener must be one canonical string:
	// the origin-pull arrangement and the placement receipt compare the stream
	// advertisement's URL byte-for-byte against the arranged pull's URL.
	if protocol == "dtsc" && u.Port() == "" {
		u.Host = net.JoinHostPort(u.Hostname(), "4200")
	}
	return strings.ReplaceAll(u.String(), "$", EncodeStreamNamePath(streamName))
}

func playbackTemplate(outputs map[string]any, protocol string) string {
	var aliases []string
	switch protocol {
	case "hls":
		aliases = []string{"HLS", "HLS (TS)"}
	case "rtmp":
		aliases = []string{"RTMP"}
	case "rtsp":
		aliases = []string{"RTSP"}
	case "dtsc":
		aliases = []string{"DTSC"}
	case "srt":
		aliases = []string{"SRT", "TSSRT"}
	case "mp4", "mews", "wsmp4":
		aliases = []string{"MP4", "MP4 progressive", "MP4 WebSocket"}
	case "webm", "mkv", "mews_webm":
		aliases = []string{"WEBM", "MKV", "EBML", "MKV progressive", "WebM progressive", "WebM WebSocket"}
	case "ts":
		aliases = []string{"TS", "HTTPTS", "TS HTTP progressive"}
	case "aac":
		aliases = []string{"AAC", "AAC progressive"}
	case "h264", "h264_ws":
		aliases = []string{"H264", "Annex B progressive", "Annex B WebSocket"}
	case "raw_ws":
		aliases = []string{"WSRaw", "Raw WebSocket"}
	case "json_ws":
		aliases = []string{"JSON WebSocket"}
	case "flv":
		aliases = []string{"FLV", "FLV progressive"}
	case "hds":
		aliases = []string{"HDS", "Dynamic"}
	case "sdp":
		aliases = []string{"SDP"}
	case "http", "mist_html":
		if hasReportedListener(outputs, "HTTPS") {
			return ingestTemplate(outputs, "HTTPS")
		}
		aliases = []string{"HTTP"}
	case "whep", "webrtc", "mist_webrtc", "wswebrtc":
		if protocol == "whep" {
			if hasReportedListener(outputs, "WHEP", "WebRTC with WHEP signalling") {
				return ingestTemplate(outputs, "WHEP", "WebRTC with WHEP signalling")
			}
		}
		template := whipIngestTemplate(outputs)
		if protocol == "whep" {
			template = strings.Replace(template, "/webrtc/$", "/whep/$", 1)
			template = strings.Replace(template, "/whip/$", "/whep/$", 1)
		}
		return template
	case "dash", "cmaf", "hls_cmaf", "smoothstreaming":
		name, suffix := "DASH", "index.mpd"
		if protocol == "cmaf" || protocol == "hls_cmaf" {
			name, suffix = "HLS (CMAF)", "index.m3u8"
		}
		if protocol == "smoothstreaming" {
			name, suffix = "SmoothStreaming", "Manifest"
		}
		if hasReportedListener(outputs, name) {
			return ingestTemplate(outputs, name)
		}
		u := parseIngestTemplate(ingestTemplate(outputs, "CMAF"))
		if u == nil || u.RawQuery != "" || !strings.HasSuffix(u.Path, "/cmaf/$/") {
			return ""
		}
		rawPath := u.EscapedPath() + suffix
		u.Path += suffix
		u.RawPath = rawPath
		return u.String()
	default:
		return ""
	}
	return ingestTemplate(outputs, aliases...)
}

func hasReportedListener(outputs map[string]any, names ...string) bool {
	for key := range outputs {
		for _, name := range names {
			if strings.EqualFold(key, name) {
				return true
			}
		}
	}
	return false
}

func unprefixedPlaybackPath(path string) bool {
	if strings.HasPrefix(path, "/$.") {
		return true
	}
	for _, prefix := range []string{"/hls/$/", "/cmaf/$/", "/dash/$/", "/dynamic/$/", "/smooth/$/"} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return path == "/webrtc/$" || path == "/whep/$" || path == "/whip/$"
}
