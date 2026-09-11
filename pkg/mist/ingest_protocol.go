package mist

import (
	"errors"
	"strings"
	"unicode"
)

// IngestProtocol maps the connector name Mist reports on PUSH_REWRITE to the
// canonical placement ingest protocol. Only connectors publishers actually use to
// contribute media are placement protocols; every other connector (DTSC pushes
// between nodes, HTTP/JSON uploads, RTSP announce) is not evaluable by policy
// and is reported as unsupported rather than guessed.
func IngestProtocol(connector string) (string, error) {
	connector = strings.TrimSpace(connector)
	if connector == "" || len(connector) > 64 || strings.IndexFunc(connector, unicode.IsControl) >= 0 {
		return "", errors.New("publisher connector attestation is missing")
	}
	switch strings.ToLower(connector) {
	case "rtmp":
		return "rtmp", nil
	case "tssrt":
		return "srt", nil
	case "webrtc":
		return "whip", nil
	default:
		return "", errors.New("unsupported publisher connector: " + connector)
	}
}
