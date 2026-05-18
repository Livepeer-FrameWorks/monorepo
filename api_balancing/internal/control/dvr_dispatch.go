package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
)

// DVRArtifactDispatch is the bundled state STREAM_SOURCE needs to route a
// dvr+<dvr_internal_name> request: artifact identity, the source stream
// name (for the on-disk path layout), recording status, and the recording
// origin node when the DVR is still active.
//
// RecordingNode is set only when the artifact's status indicates an
// in-flight recording. After finalize the field stays empty and callers
// fall back to chapter-based playback against the segment ledger.
type DVRArtifactDispatch struct {
	DVRHash            string
	InternalName       string
	StreamID           string
	StreamInternalName string
	PlaybackID         string
	TenantID           string
	Status             string
	RecordingNode      string
	RequiresAuth       bool
}

// ResolveDVRArtifactDispatch maps a DVR artifact internal_name (the token
// inside dvr+<dvr_internal_name>) to the dispatch state. Returns (nil,
// nil) when the token is not a DVR artifact — the caller should fall
// back to chapter resolution.
func ResolveDVRArtifactDispatch(ctx context.Context, dvrInternalName string) (*DVRArtifactDispatch, error) {
	if CommodoreClient == nil || dvrInternalName == "" {
		return nil, nil
	}
	artifact, err := CommodoreClient.ResolveArtifactInternalName(ctx, dvrInternalName)
	if err != nil {
		return nil, err
	}
	if !artifact.GetFound() || artifact.GetContentType() != "dvr" {
		return nil, nil
	}
	dvr, err := CommodoreClient.ResolveDVRHash(ctx, artifact.GetArtifactHash())
	if err != nil {
		return nil, err
	}
	if !dvr.GetFound() {
		return nil, nil
	}
	out := &DVRArtifactDispatch{
		DVRHash:            artifact.GetArtifactHash(),
		InternalName:       artifact.GetInternalName(),
		StreamID:           dvr.GetStreamId(),
		StreamInternalName: dvr.GetStreamInternalName(),
		PlaybackID:         dvr.GetPlaybackId(),
		TenantID:           dvr.GetTenantId(),
		RequiresAuth:       artifact.GetRequiresAuth(),
	}
	if db == nil {
		return out, nil
	}
	var status sql.NullString
	scanErr := db.QueryRowContext(ctx, `
		SELECT status
		  FROM foghorn.artifacts
		 WHERE artifact_hash = $1
		   AND artifact_type = 'dvr'
		 LIMIT 1
	`, out.DVRHash).Scan(&status)
	if scanErr != nil {
		if errors.Is(scanErr, sql.ErrNoRows) {
			return out, nil
		}
		return out, scanErr
	}
	if status.Valid {
		out.Status = status.String
	}
	if !IsActiveDVRStatus(out.Status) {
		return out, nil
	}

	// Recording-origin lookup. Invariant: during an active DVR session
	// (status in requested/starting/recording) the artifact's
	// only non-orphaned artifact_nodes row is the storage node assigned
	// by StartDVR — chapter-playback cache rows for the same hash only
	// appear after finalize, when status leaves the active set and this
	// branch is skipped. Enforce that invariant explicitly here: if more
	// than one row exists while status is active, bail rather than guess
	// which row is the recording origin.
	rows, err := db.QueryContext(ctx, `
		SELECT node_id, COALESCE(is_orphaned, false)
		  FROM foghorn.artifact_nodes
		 WHERE artifact_hash = $1
	`, out.DVRHash)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	var candidates []string
	var orphanedCandidates []string
	for rows.Next() {
		var nodeID string
		var isOrphaned bool
		if err := rows.Scan(&nodeID, &isOrphaned); err != nil {
			return out, err
		}
		switch {
		case nodeID == "":
		case isOrphaned:
			orphanedCandidates = append(orphanedCandidates, nodeID)
		default:
			candidates = append(candidates, nodeID)
		}
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	switch len(candidates) {
	case 0:
		// Active status but no non-orphaned artifact_nodes row. If a single
		// stale row exists, use it as the recording-origin hint: segment
		// progress refreshes the row back to non-orphaned, and refusing it
		// would wedge an otherwise healthy in-flight DVR until the next
		// control-plane write.
		if len(orphanedCandidates) == 1 {
			out.RecordingNode = orphanedCandidates[0]
		}
	case 1:
		out.RecordingNode = candidates[0]
	default:
		// Multiple rows while status is active violates the invariant.
		// Refuse to pick rather than risk routing to a stale warm-cache
		// edge that doesn't own the live segments.
		return out, fmt.Errorf("dispatch: active DVR %q has %d non-orphaned artifact_nodes rows; recording origin ambiguous", out.DVRHash, len(candidates))
	}
	return out, nil
}

// IsActiveDVRStatus reports whether a DVR artifact's status means a
// recording is in-flight on its assigned node and the rolling DVR
// manifest is the canonical playback surface. Routing callers use it
// to gate the live-style vs archive-style viewer path.
//
// 'finalizing' is excluded: FinalizeDVR has already claimed the stop,
// the rolling manifest is closing, and viewer resolution should fall
// to the latest playable chapter / not-ready response rather than the
// live-style dvr+<internal> lane.
func IsActiveDVRStatus(status string) bool {
	switch status {
	case "requested", "starting", "recording":
		return true
	}
	return false
}

// LocalRollingDVRManifestPath returns the on-disk path of the rolling
// DVR manifest on a node, derived from the node's StorageLocal root and
// the canonical layout used by the DVR push:
//
//	<storage>/dvr/<stream_id>/<dvr_hash>/<dvr_hash>.m3u8
//
// Returns "" when any input is missing or the node has no advertised
// storage root.
func LocalRollingDVRManifestPath(streamID, dvrHash, nodeID string) string {
	base := storageBasePathForNode(nodeID)
	if base == "" || streamID == "" || dvrHash == "" {
		return ""
	}
	return filepath.Join(base, "dvr", streamID, dvrHash, dvrHash+".m3u8")
}
