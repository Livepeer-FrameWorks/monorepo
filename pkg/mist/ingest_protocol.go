package mist

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// ErrUnsupportedIngestProtocol marks a connector that placement policy cannot
// evaluate as a publisher protocol.
var ErrUnsupportedIngestProtocol = errors.New("unsupported publisher connector")

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
		return "", fmt.Errorf("%w: %s", ErrUnsupportedIngestProtocol, connector)
	}
}
