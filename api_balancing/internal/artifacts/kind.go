package artifacts

import "strings"

// CanonicalByteKind maps playback-facing artifact kinds to the storage and
// federation kind that owns their bytes. A finalized DVR chapter is addressed
// as a chapter by playback APIs, but its file is an ordinary VOD object.
func CanonicalByteKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "chapter":
		return "vod"
	default:
		return strings.ToLower(strings.TrimSpace(kind))
	}
}

// SameByteKind compares kinds at a byte-storage boundary. It deliberately does
// not make chapter and VOD interchangeable in playback or policy dispatch.
func SameByteKind(left, right string) bool {
	left = CanonicalByteKind(left)
	right = CanonicalByteKind(right)
	return left != "" && left == right
}
