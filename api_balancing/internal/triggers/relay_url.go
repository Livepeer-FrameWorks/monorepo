package triggers

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"

	"frameworks/api_balancing/internal/control"
)

// buildVODRelayURL constructs the Mist source URL for a VOD/clip artifact
// served via Helmsman's read-through relay on the given node. The base URL
// is per-node (captured at Register from HELMSMAN_RELAY_BASE_URL on the
// sidecar) so production-colocated Mist sees 127.0.0.1 while container
// deployments see the inter-container address (e.g. helmsman:18007).
//
// For clips, streamInternal carries the owning stream's internal_name and
// is encoded as a path segment (clips/<stream>/<hash>.<ext>), NOT a query
// parameter. Mist appends ".dtsh" to the entire input string for sidecar
// read/write (mistserver/src/input/input.cpp); a query-string-encoded
// stream would mutate to ?s=stream.dtsh and the relay's dtsh dispatch
// would miss. Path encoding survives Mist's ".dtsh" suffixing intact.
// Empty streamInternal means "flat layout only"; safe for VOD which always
// lives at vod/<hash>.<ext>.
//
// Returns "" when the node has no advertised relay URL — callers treat that
// as a hard signal to abort STREAM_SOURCE rather than fabricating a default.
func buildVODRelayURL(nodeID, kind, artifactHash, format, streamInternal string) string {
	base := control.GetRelayBaseURL(nodeID)
	if base == "" {
		return ""
	}
	ext := normalizeExt(format)
	if ext == "" {
		return ""
	}
	if kind == "clip" && streamInternal != "" {
		return fmt.Sprintf("%s/internal/artifact/clip/%s/%s%s", base, url.PathEscape(streamInternal), artifactHash, ext)
	}
	return fmt.Sprintf("%s/internal/artifact/%s/%s%s", base, kind, artifactHash, ext)
}

// buildUploadRelayURL constructs the Mist source URL for a processing-input
// upload on the given node. Used by resolveProcessSource for safe-wrapper
// formats; unsafe wrappers (avi/flv/m4v) keep the local-stage path because
// Mist's FLV/AV inputs cannot open HTTP sources.
func buildUploadRelayURL(nodeID, uploadHash, ext string) string {
	base := control.GetRelayBaseURL(nodeID)
	if base == "" {
		return ""
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	return fmt.Sprintf("%s/internal/artifact/upload/%s%s", base, uploadHash, ext)
}

// normalizeExt accepts either "mkv" or ".mkv" and returns ".mkv". Empty in,
// empty out.
func normalizeExt(format string) string {
	format = strings.TrimSpace(format)
	if format == "" {
		return ""
	}
	if !strings.HasPrefix(format, ".") {
		format = "." + format
	}
	return strings.ToLower(format)
}

// isRelaySafeFormat reports whether a wrapper extension can be opened by
// MistServer over HTTP. Unsafe wrappers (avi/flv/m4v) fall back to local
// staging because Mist's FLV uses fopen and AV input only auto-matches local
// paths.
func isRelaySafeFormat(ext string) bool {
	switch strings.ToLower(normalizeExt(ext)) {
	case ".mp4", ".mov", ".mkv", ".webm", ".ts", ".m2ts", ".m3u8", ".m3u":
		return true
	default:
		return false
	}
}

// lookupArtifactDescriptor returns the format, artifact_type, and
// stream_internal_name for an artifact in one DB hit. STREAM_SOURCE uses
// format to pick the relay URL extension, artifact_type as the
// clip/vod/dvr classifier when Commodore's contentType is unavailable,
// and stream_internal_name to probe the nested clip warm path. Empty
// format means we can't fabricate a valid relay URL; callers fall back
// to abort.
type artifactDescriptor struct {
	Format         string
	ArtifactType   string
	StreamInternal string
}

func selectArtifactRelayFormat(desc artifactDescriptor, warmFormat string) string {
	if strings.TrimSpace(desc.Format) != "" {
		return desc.Format
	}
	return warmFormat
}

func lookupArtifactDescriptor(ctx context.Context, artifactHash string) artifactDescriptor {
	db := control.GetDB()
	if db == nil || artifactHash == "" {
		return artifactDescriptor{}
	}
	var (
		format         sql.NullString
		artifactType   sql.NullString
		streamInternal sql.NullString
	)
	err := db.QueryRowContext(ctx, `
		SELECT format, artifact_type, stream_internal_name FROM foghorn.artifacts
		WHERE artifact_hash = $1 AND status != 'deleted'
		LIMIT 1
	`, artifactHash).Scan(&format, &artifactType, &streamInternal)
	if err != nil {
		return artifactDescriptor{}
	}
	d := artifactDescriptor{}
	if format.Valid {
		d.Format = format.String
	}
	if artifactType.Valid {
		d.ArtifactType = artifactType.String
	}
	if streamInternal.Valid {
		d.StreamInternal = streamInternal.String
	}
	return d
}

// kindFromAssetType classifies a Commodore-resolved artifact contentType or
// a foghorn.artifacts.artifact_type value for relay URL routing. Returns
// "clip" for clip artifacts; "vod" for vod; "" for anything else (DVR,
// processing, unknown), letting the caller pick its own fallback. Note:
// Commodore's contentType and the DB's artifact_type share the same
// enum vocabulary here.
func kindFromAssetType(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "clip":
		return "clip"
	case "vod":
		return "vod"
	default:
		return ""
	}
}
