package mist

import (
	"errors"
	"net/url"
	"strings"
)

// Stream names travel inside URL paths: Livepeer push targets, relay artifact
// paths, playback and ingest templates. Every producer encodes through
// EncodeStreamNamePath and every matcher decodes through DecodeStreamNamePath,
// so a name compares equal however many hops re-encoded it on the way.

// maxStreamNameEncodingLayers bounds decoding across the request hops that
// percent-encode a stream path before it reaches the receiver.
const maxStreamNameEncodingLayers = 2

var errInvalidStreamNamePath = errors.New("invalid stream name in URL path")

// EncodeStreamNamePath returns name as one URL path segment.
func EncodeStreamNamePath(name string) string {
	return url.PathEscape(name)
}

// DecodeStreamNamePath returns the stream name carried by one URL path
// segment, whether it arrives literal, single- or double-encoded. Stream names
// never contain '%', so a '%' left after decoding is another encoding layer,
// not name content. Malformed escapes and characters outside the stream-name
// alphabet are rejected rather than guessed at.
func DecodeStreamNamePath(segment string) (string, error) {
	name := segment
	for range maxStreamNameEncodingLayers {
		if !strings.Contains(name, "%") {
			break
		}
		decoded, err := url.PathUnescape(name)
		if err != nil {
			return "", errInvalidStreamNamePath
		}
		name = decoded
	}
	if !validStreamName(name) {
		return "", errInvalidStreamNamePath
	}
	return name, nil
}

// validStreamName accepts MistServer's sanitized stream-name alphabet: a base
// of letters, digits, '_', '.' and '-', optionally followed by '+' and a
// wildcard suffix of the same alphabet.
func validStreamName(name string) bool {
	if name == "" {
		return false
	}
	base, suffix, wildcard := strings.Cut(name, "+")
	if base == "" || !streamNameChars(base) {
		return false
	}
	return !wildcard || (suffix != "" && streamNameChars(suffix))
}

func streamNameChars(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
		default:
			return false
		}
	}
	return true
}
