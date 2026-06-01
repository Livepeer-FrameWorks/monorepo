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
// The URL carries NO auth token: Mist derives the sidecar request as
// input + ".dtsh" (mistserver/src/input/input.cpp), and a query token would
// mutate to ?artifact_relay_token=TOK.dtsh — leaving the path at <hash>.<ext>,
// so the request routes to the media handler and never reaches the .dtsh
// dispatch. The Mist→Helmsman hop is a same-node sidecar trust boundary
// instead: Helmsman's relay accepts it via loopback (production colocation) or
// HELMSMAN_RELAY_TRUSTED_CIDR (docker, where Mist dials helmsman:18007).
//
// For clips, streamInternal carries the owning stream's internal_name and is
// encoded as a path segment (clips/<stream>/<hash>.<ext>), NOT a query
// parameter, so it too survives Mist's ".dtsh" suffixing. A clip REQUIRES the
// stream segment: Helmsman only serves clips on the nested route and 404s a
// flat clip path, so an empty streamInternal here is a hard error, not a flat
// fallback. VOD/upload stay flat at <kind>/<hash>.<ext>.
//
// Returns "" when the node has no advertised relay URL, or for a clip with no
// stream — callers treat "" as a hard signal to abort STREAM_SOURCE rather than
// fabricating a default (or, here, an impossible-to-serve flat clip URL).
func buildVODRelayURL(nodeID, kind, artifactHash, format, streamInternal string) string {
	base := control.GetRelayBaseURL(nodeID)
	if base == "" {
		return ""
	}
	ext := normalizeExt(format)
	if ext == "" {
		return ""
	}
	path := relayArtifactPath(kind, artifactHash, ext, streamInternal)
	if path == "" {
		return ""
	}
	return base + path
}

// relayArtifactPath builds the Helmsman relay path for an artifact, or "" when
// no valid path exists. Clips REQUIRE the stream segment — Helmsman only serves
// them on the nested /clip/<stream>/<file> route and 404s a flat clip path — so
// an empty stream yields "" (caller aborts), never a flat fallback. VOD/upload
// are flat. The stream segment is PathEscaped to match the federation and
// same-cluster peer-relay builders so the minted grant path and the requested
// path are byte-identical. ext must already be normalized (e.g. ".mp4").
func relayArtifactPath(kind, artifactHash, ext, streamInternal string) string {
	if kind == "clip" {
		if streamInternal == "" {
			return ""
		}
		return fmt.Sprintf("/internal/artifact/clip/%s/%s%s", url.PathEscape(streamInternal), artifactHash, ext)
	}
	return fmt.Sprintf("/internal/artifact/%s/%s%s", kind, artifactHash, ext)
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
	path := fmt.Sprintf("/internal/artifact/upload/%s%s", uploadHash, ext)
	return base + path
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
	// Found reports whether a local foghorn.artifacts row exists. Cross-cluster
	// adoption keys on this, NOT on Format: warm in-memory state can carry a
	// format while the DB row is still missing, and RelayResolve 404s a missing
	// row — so the front door must adopt whenever the row is absent.
	Found          bool
	Format         string
	ArtifactType   string
	StreamInternal string
	// AuthoritativeCluster is the row's COALESCE(storage_cluster_id,
	// origin_cluster_id) — the cluster that actually holds the bytes.
	// STREAM_SOURCE reauthorizes an already-adopted cross-cluster row against
	// this before handing Mist a relay URL.
	AuthoritativeCluster string
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
		format               sql.NullString
		artifactType         sql.NullString
		streamInternal       sql.NullString
		authoritativeCluster sql.NullString
	)
	err := db.QueryRowContext(ctx, `
		SELECT format, artifact_type, stream_internal_name,
		       COALESCE(storage_cluster_id, origin_cluster_id)
		FROM foghorn.artifacts
		WHERE artifact_hash = $1 AND status != 'deleted'
		LIMIT 1
	`, artifactHash).Scan(&format, &artifactType, &streamInternal, &authoritativeCluster)
	if err != nil {
		return artifactDescriptor{}
	}
	d := artifactDescriptor{Found: true}
	if format.Valid {
		d.Format = format.String
	}
	if artifactType.Valid {
		d.ArtifactType = artifactType.String
	}
	if streamInternal.Valid {
		d.StreamInternal = streamInternal.String
	}
	if authoritativeCluster.Valid {
		d.AuthoritativeCluster = authoritativeCluster.String
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
