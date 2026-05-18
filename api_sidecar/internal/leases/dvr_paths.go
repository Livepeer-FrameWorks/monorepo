package leases

import (
	"path/filepath"
	"strings"
)

// DeriveDvrHashFromRollingManifestPath extracts dvr_hash from the rolling
// DVR manifest path that Mist's HLS push writes:
// .../dvr/<stream>/<dvr_hash>/<dvr_hash>.m3u8. Returns "" on shape
// mismatch.
func DeriveDvrHashFromRollingManifestPath(manifestPath string) string {
	if manifestPath == "" {
		return ""
	}
	if filepath.Ext(manifestPath) != ".m3u8" {
		return ""
	}
	parentDir := filepath.Base(filepath.Dir(manifestPath))
	if parentDir == "" {
		return ""
	}
	stem := strings.TrimSuffix(filepath.Base(manifestPath), ".m3u8")
	if stem != parentDir {
		return ""
	}
	return stem
}
