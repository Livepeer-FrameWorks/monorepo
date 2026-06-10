package grpc

import (
	"context"
	"database/sql"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// MarkArtifactThumbnailsReady flips has_thumbnails to TRUE and stamps
// storage_cluster_id on the matching artifact row after Foghorn confirms
// the thumbnail bytes landed. Idempotent at the DB level via the WHERE clause.
func (s *CommodoreServer) MarkArtifactThumbnailsReady(ctx context.Context, req *commodorepb.MarkArtifactThumbnailsReadyRequest) (*commodorepb.MarkArtifactThumbnailsReadyResponse, error) {
	tenantID := req.GetTenantId()
	assetKey := req.GetAssetKey()
	cluster := req.GetStorageClusterId()
	if tenantID == "" || assetKey == "" || cluster == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id, asset_key, and storage_cluster_id are required")
	}

	table, keyCol, err := artifactAssetTable(req.GetAssetType())
	if err != nil {
		return nil, err
	}

	// Idempotent: only write when something would actually change.
	query := `UPDATE ` + table + `
		SET has_thumbnails = TRUE,
		    storage_cluster_id = $1,
		    updated_at = NOW()
		WHERE tenant_id = $2::uuid
		  AND ` + keyCol + ` = $3
		  AND (has_thumbnails = FALSE OR storage_cluster_id IS DISTINCT FROM $1)`
	res, execErr := s.db.ExecContext(ctx, query, cluster, tenantID, assetKey)
	if execErr != nil {
		s.logger.WithError(execErr).WithFields(logging.Fields{
			"tenant_id":  tenantID,
			"asset_type": req.GetAssetType().String(),
			"asset_key":  assetKey,
		}).Error("MarkArtifactThumbnailsReady failed")
		return nil, status.Errorf(codes.Internal, "update failed: %v", execErr)
	}
	rows, _ := res.RowsAffected() //nolint:errcheck // pq always populates RowsAffected on UPDATE
	return &commodorepb.MarkArtifactThumbnailsReadyResponse{Updated: rows > 0}, nil
}

// UpdateArtifactStorageCluster updates storage_cluster_id only. It never
// flips has_thumbnails — a storage move on a thumbnail-less artifact must
// not falsely flip readiness.
func (s *CommodoreServer) UpdateArtifactStorageCluster(ctx context.Context, req *commodorepb.UpdateArtifactStorageClusterRequest) (*commodorepb.UpdateArtifactStorageClusterResponse, error) {
	tenantID := req.GetTenantId()
	assetKey := req.GetAssetKey()
	cluster := req.GetStorageClusterId()
	if tenantID == "" || assetKey == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id and asset_key are required")
	}

	table, keyCol, err := artifactAssetTable(req.GetAssetType())
	if err != nil {
		return nil, err
	}

	storageArg := sql.NullString{String: cluster, Valid: cluster != ""}
	query := `UPDATE ` + table + `
		SET storage_cluster_id = $1,
		    updated_at = NOW()
		WHERE tenant_id = $2::uuid
		  AND ` + keyCol + ` = $3
		  AND storage_cluster_id IS DISTINCT FROM $1`
	res, execErr := s.db.ExecContext(ctx, query, storageArg, tenantID, assetKey)
	if execErr != nil {
		s.logger.WithError(execErr).WithFields(logging.Fields{
			"tenant_id":  tenantID,
			"asset_type": req.GetAssetType().String(),
			"asset_key":  assetKey,
		}).Error("UpdateArtifactStorageCluster failed")
		return nil, status.Errorf(codes.Internal, "update failed: %v", execErr)
	}
	rows, _ := res.RowsAffected() //nolint:errcheck // pq always populates RowsAffected on UPDATE
	return &commodorepb.UpdateArtifactStorageClusterResponse{Updated: rows > 0}, nil
}

// UpdateArtifactSize projects Foghorn's authoritative artifact byte count into
// the registry row used by catalog/storage queries.
func (s *CommodoreServer) UpdateArtifactSize(ctx context.Context, req *commodorepb.UpdateArtifactSizeRequest) (*commodorepb.UpdateArtifactSizeResponse, error) {
	tenantID := req.GetTenantId()
	assetKey := req.GetAssetKey()
	sizeBytes := req.GetSizeBytes()
	durationMs := req.GetDurationMs()
	if tenantID == "" || assetKey == "" || sizeBytes < 0 {
		return nil, status.Error(codes.InvalidArgument, "tenant_id, asset_key, and non-negative size_bytes are required")
	}
	// Only commodore.clips carries a duration column; the other registry
	// tables must not receive a duration projection.
	if durationMs > 0 && req.GetAssetType() != commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_CLIP {
		return nil, status.Error(codes.InvalidArgument, "duration_ms projection is only supported for clips")
	}
	if sizeBytes == 0 && durationMs <= 0 {
		return &commodorepb.UpdateArtifactSizeResponse{Updated: false}, nil
	}

	table, keyCol, err := artifactAssetTable(req.GetAssetType())
	if err != nil {
		return nil, err
	}

	// size_bytes <= 0 leaves the stored size untouched so a duration-only
	// projection cannot zero a previously-projected size.
	query := `UPDATE ` + table + `
		SET size_bytes = CASE WHEN $1::bigint > 0 THEN $1::bigint ELSE size_bytes END,
		    updated_at = NOW()
		WHERE tenant_id = $2::uuid
		  AND ` + keyCol + ` = $3
		  AND size_bytes IS DISTINCT FROM $1::bigint`
	args := []any{sizeBytes, tenantID, assetKey}
	if durationMs > 0 {
		query = `UPDATE ` + table + `
		SET size_bytes = CASE WHEN $1::bigint > 0 THEN $1::bigint ELSE size_bytes END,
		    duration = $4::bigint,
		    updated_at = NOW()
		WHERE tenant_id = $2::uuid
		  AND ` + keyCol + ` = $3
		  AND (size_bytes IS DISTINCT FROM $1::bigint OR duration IS DISTINCT FROM $4::bigint)`
		args = append(args, durationMs)
	}
	res, execErr := s.db.ExecContext(ctx, query, args...)
	if execErr != nil {
		s.logger.WithError(execErr).WithFields(logging.Fields{
			"tenant_id":  tenantID,
			"asset_type": req.GetAssetType().String(),
			"asset_key":  assetKey,
		}).Error("UpdateArtifactSize failed")
		return nil, status.Errorf(codes.Internal, "update failed: %v", execErr)
	}
	rows, _ := res.RowsAffected() //nolint:errcheck // pq always populates RowsAffected on UPDATE
	return &commodorepb.UpdateArtifactSizeResponse{Updated: rows > 0}, nil
}

// artifactAssetTable maps the proto enum to the registry table and its
// hash column. clip_hash, dvr_hash, vod_hash are the asset keys used at
// the Foghorn thumbnail-upload path.
func artifactAssetTable(t commodorepb.ArtifactAssetType) (table, keyCol string, err error) {
	switch t {
	case commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_CLIP:
		return "commodore.clips", "clip_hash", nil
	case commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_DVR:
		return "commodore.dvr_recordings", "dvr_hash", nil
	case commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_VOD:
		return "commodore.vod_assets", "vod_hash", nil
	default:
		return "", "", status.Errorf(codes.InvalidArgument, "unsupported asset_type: %s", t.String())
	}
}
