package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"

	"frameworks/api_balancing/internal/database/foghorndb"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
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
	DVRHash                     string
	InternalName                string
	StreamID                    string
	StreamInternalName          string
	PlaybackID                  string
	TenantID                    string
	Status                      string
	RecordingNode               string
	RequiresAuth                bool
	AllowPlatformSharedPlayback bool
	// ClusterPeers is the tenant's freshly-resolved cluster-peer envelope from
	// Commodore. A cross-cluster DVR arrange must gate the recording peer
	// against this so a revoked peer can't keep serving rolling DVR off stale
	// registry state.
	ClusterPeers []*clusterpeerpb.TenantClusterPeer
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
	return ResolveConnectedDVRArtifactDispatch(ctx, artifact)
}

// ResolveConnectedDVRArtifactDispatch completes a connected artifact lookup
// with the source-stream catalog fields that are not part of the artifact
// response. The caller may reuse the same response for signed-authority shadow
// comparison instead of issuing the artifact lookup twice.
func ResolveConnectedDVRArtifactDispatch(ctx context.Context, artifact *commodorepb.ResolveArtifactInternalNameResponse) (*DVRArtifactDispatch, error) {
	if CommodoreClient == nil || artifact == nil || !artifact.GetFound() || artifact.GetContentType() != "dvr" {
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
		DVRHash:                     artifact.GetArtifactHash(),
		InternalName:                artifact.GetInternalName(),
		StreamID:                    dvr.GetStreamId(),
		StreamInternalName:          dvr.GetStreamInternalName(),
		PlaybackID:                  dvr.GetPlaybackId(),
		TenantID:                    dvr.GetTenantId(),
		RequiresAuth:                artifact.GetRequiresAuth(),
		AllowPlatformSharedPlayback: true,
		ClusterPeers:                artifact.GetClusterPeers(),
	}
	return populateDVRArtifactRuntime(ctx, out)
}

// ResolveLocalDVRArtifactDispatch builds the same dispatch contract entirely
// from signed identity plus Foghorn's durable artifact/session state.
func ResolveLocalDVRArtifactDispatch(ctx context.Context, artifact *commodorepb.ResolveArtifactInternalNameResponse, playbackID string, allowPlatformShared bool) (*DVRArtifactDispatch, error) {
	if artifact == nil || !artifact.GetFound() || artifact.GetContentType() != "dvr" {
		return nil, nil
	}
	out := &DVRArtifactDispatch{
		DVRHash: artifact.GetArtifactHash(), InternalName: artifact.GetInternalName(), StreamID: artifact.GetStreamId(),
		PlaybackID: playbackID, TenantID: artifact.GetTenantId(), RequiresAuth: artifact.GetRequiresAuth(),
		AllowPlatformSharedPlayback: allowPlatformShared, ClusterPeers: artifact.GetClusterPeers(),
	}
	out.StreamInternalName = artifact.GetParentStreamInternalName()
	if db != nil {
		identity, err := foghorndb.New(db).GetProcessingArtifactLifecycle(ctx, out.DVRHash)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if err == nil {
			if identity.ArtifactType != "dvr" || (identity.StreamID != "" && identity.StreamID != out.StreamID) {
				return nil, errors.New("durable DVR identity conflicts with signed authority")
			}
			if identity.StreamInternalName != "" {
				out.StreamInternalName = identity.StreamInternalName
			}
			if out.StreamID == "" {
				out.StreamID = identity.StreamID
			}
		}
	}
	return populateDVRArtifactRuntime(ctx, out)
}

func populateDVRArtifactRuntime(ctx context.Context, out *DVRArtifactDispatch) (*DVRArtifactDispatch, error) {
	if out == nil {
		return nil, nil
	}
	if db == nil {
		return out, nil
	}
	status, scanErr := foghorndb.New(db).GetDVRDispatchStatus(ctx, out.DVRHash)
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
	rows, err := foghorndb.New(db).ListDVRDispatchNodes(ctx, out.DVRHash)
	if err != nil {
		return out, err
	}
	var candidates []string
	var orphanedCandidates []string
	for _, row := range rows {
		nodeID, isOrphaned := row.NodeID, row.IsOrphaned
		switch {
		case nodeID == "":
		case isOrphaned:
			orphanedCandidates = append(orphanedCandidates, nodeID)
		default:
			candidates = append(candidates, nodeID)
		}
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
