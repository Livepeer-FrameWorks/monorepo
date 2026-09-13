package mist

import (
	"errors"
	"strings"
	"unicode"
)

// ViewerProtocol maps Mist output/statistics labels to a canonical playback
// protocol. Transport/JSON companion labels cannot choose a protocol, automation
// and metadata sessions cannot become viewers, and mixed media protocols fail.
// URL parameters are deliberately not a source of listener capability evidence.
func ViewerProtocol(connector string) (string, error) {
	if len(connector) > 255 || strings.IndexFunc(connector, unicode.IsControl) >= 0 {
		return "", errors.New("invalid viewer connector")
	}
	selected := ""
	for part := range strings.SplitSeq(connector, ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		var protocol string
		switch part {
		case "http", "https", "json":
			continue
		case "hls", "dash", "cmaf", "mp4", "webm", "mkv", "flv", "rtmp", "rtsp", "srt", "dtsc", "dtscquic", "webrtc", "whep", "h264", "aac", "mp3", "flac", "wav", "ogg", "opus":
			protocol = part
		case "hss":
			protocol = "smoothstreaming"
		case "ts", "httpts":
			protocol = "ts"
		case "ebml":
			protocol = "mkv"
		case "raw/ws", "wsraw":
			protocol = "raw_ws"
		case "webrtc/ws":
			protocol = "webrtc"
		case "mp4/ws":
			protocol = "wsmp4"
		case "ebml/ws":
			protocol = "mews_webm"
		case "h264/ws":
			protocol = "h264_ws"
		case "json/ws":
			protocol = "json_ws"
		default:
			return "", errors.New("unsupported viewer connector")
		}
		if selected != "" && selected != protocol {
			return "", errors.New("ambiguous viewer connector")
		}
		selected = protocol
	}
	if selected == "" {
		return "", errors.New("viewer media protocol is unavailable")
	}
	return selected, nil
}

// PlayRewriteProtocol includes Mist's HTTP stream-info/page and preview connectors. These
// requests need source admission before stream discovery, but are not media
// sessions. The mist_html capability still requires an observed HTTP listener;
// the eventual media connection must independently pass ViewerProtocol admission.
func PlayRewriteProtocol(connector string) (string, error) {
	switch strings.ToLower(connector) {
	case "http", "https", "thumbvtt", "jpg":
		// Preview helpers are HTTP dependents, not independent listeners in
		// Mist's output inventory. Their emitting connector is still trusted
		// evidence of the request kind; media never enters this branch.
		return "mist_html", nil
	}
	return ViewerProtocol(connector)
}
