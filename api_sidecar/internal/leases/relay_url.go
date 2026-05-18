package leases

import (
	"net/url"
	"path"
	"strings"
)

// RelayArtifactPath is the URL prefix Helmsman uses for its internal
// read-through artifact relay served from /internal/artifact/*.
const RelayArtifactPath = "/internal/artifact/"

// IsRelayArtifactResponse reports whether a STREAM_SOURCE response is a
// Helmsman relay URL (i.e. http(s)://host/internal/artifact/...). Such
// responses look like external HTTP redirects to the old leasing filter
// but are actually local-cache-backed and must produce a SourceLease.
func IsRelayArtifactResponse(response string) bool {
	if response == "" {
		return false
	}
	u, err := url.Parse(response)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	return strings.HasPrefix(u.Path, RelayArtifactPath)
}

// ParseArtifactRelayURL maps a relay URL back to an AssetKey. Returns ok=false
// for malformed URLs, unknown kinds, or paths outside the relay prefix.
//
// Accepted shapes (ext preserved verbatim, flat layout to match on-disk
// storage/<kind>/<hash>.<ext> with the clip nested-by-stream variant):
//
//	/internal/artifact/vod/<hash>.<ext>[.dtsh]
//	/internal/artifact/clip/<hash>.<ext>[.dtsh]                  ← flat
//	/internal/artifact/clip/<stream>/<hash>.<ext>[.dtsh]         ← nested
//	/internal/artifact/upload/<hash>.<ext>
//
// DVR chapter routes (dvr/<dvr_hash>/chapter/...) have retired —
// chapter playback uses vod/<chapter_artifact_hash> with
// origin_type='dvr_chapter' on the foghorn.artifacts row.
func ParseArtifactRelayURL(s string) (AssetKey, bool) {
	u, err := url.Parse(s)
	if err != nil {
		return AssetKey{}, false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return AssetKey{}, false
	}
	if !strings.HasPrefix(u.Path, RelayArtifactPath) {
		return AssetKey{}, false
	}
	rest := strings.TrimPrefix(u.Path, RelayArtifactPath)
	parts := strings.Split(rest, "/")
	if len(parts) < 2 {
		return AssetKey{}, false
	}
	kind := parts[0]
	switch kind {
	case "vod", "upload":
		// Flat: /<kind>/<hash>.<ext>[.dtsh] — strip extensions to recover
		// the AssetKey.Hash.
		hash := stripExt(parts[1])
		if hash == "" {
			return AssetKey{}, false
		}
		return AssetKey{Type: kind, Hash: hash}, true
	case "clip":
		// Clip accepts both the flat (no stream) and nested
		// (stream-scoped) shapes. The last path segment is always
		// <hash>.<ext>[.dtsh]; anything in front of it is stream
		// context that doesn't affect the AssetKey.
		hash := stripExt(parts[len(parts)-1])
		if hash == "" {
			return AssetKey{}, false
		}
		return AssetKey{Type: "clip", Hash: hash}, true
	default:
		return AssetKey{}, false
	}
}

// stripExt removes the trailing extension from a path segment, treating
// `.dtsh` as a secondary suffix (so foo.mkv.dtsh and foo.mkv both map to foo).
func stripExt(seg string) string {
	seg = strings.TrimSuffix(seg, ".dtsh")
	if ext := path.Ext(seg); ext != "" {
		seg = strings.TrimSuffix(seg, ext)
	}
	return seg
}

// ExtFromRelayURL returns the playback-format extension for a relay URL
// (".mkv", ".mp4", etc.), with `.dtsh` stripped so a sidecar URL maps to the
// same extension as its media. Returns "" if the URL is not a relay URL or
// has no extension.
func ExtFromRelayURL(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	if !strings.HasPrefix(u.Path, RelayArtifactPath) {
		return ""
	}
	p := strings.TrimSuffix(u.Path, ".dtsh")
	return path.Ext(p)
}

// StreamInternalFromRelayURL returns the owning stream's internal_name
// when the URL is a stream-scoped clip path (clip/<stream>/<hash>.<ext>).
// The stream is encoded in the path — not a query parameter — because
// Mist's ".dtsh" suffix mutation would corrupt a query-encoded stream
// (see input.cpp's input + ".dtsh" sidecar pattern). Empty when the URL
// is not a clip relay URL or has no stream segment.
func StreamInternalFromRelayURL(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	if !strings.HasPrefix(u.Path, RelayArtifactPath) {
		return ""
	}
	rest := strings.TrimPrefix(u.Path, RelayArtifactPath)
	parts := strings.Split(rest, "/")
	// Stream-scoped clip: /clip/<stream>/<hash>.<ext>[.dtsh]
	if len(parts) >= 3 && parts[0] == "clip" {
		return parts[1]
	}
	return ""
}
