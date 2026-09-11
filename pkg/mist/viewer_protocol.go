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
