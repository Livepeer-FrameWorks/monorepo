package control

import (
	"context"
	"database/sql"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func projectArtifactSizeToCommodore(ctx context.Context, artifactHash string, sizeBytes int64, logger logging.Logger) {
	if CommodoreClient == nil || db == nil || artifactHash == "" {
		return
	}

	var artifactType, tenantID string
	var storedSize sql.NullInt64
	if err := db.QueryRowContext(ctx, `
		SELECT artifact_type, tenant_id::text, size_bytes
		  FROM foghorn.artifacts
		 WHERE artifact_hash = $1
		   AND tenant_id IS NOT NULL
	`, artifactHash).Scan(&artifactType, &tenantID, &storedSize); err != nil {
		if err != sql.ErrNoRows {
			logger.WithError(err).WithField("artifact_hash", artifactHash).Warn("Failed to resolve artifact for size projection")
		}
		return
	}

	if storedSize.Valid && storedSize.Int64 > 0 {
		sizeBytes = storedSize.Int64
	}
	if sizeBytes <= 0 {
		return
	}

	assetType, ok := artifactAssetTypeFromString(artifactType)
	if !ok {
		return
	}

	notifyCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := CommodoreClient.UpdateArtifactSize(notifyCtx, tenantID, assetType, artifactHash, sizeBytes); err != nil {
		logger.WithError(err).WithFields(logging.Fields{
			"artifact_hash": artifactHash,
			"asset_type":    artifactType,
			"size_bytes":    sizeBytes,
		}).Warn("Failed to notify Commodore of artifact size")
	}
}

// projectClipDurationToCommodore pushes the measured output duration onto the
// commodore clip registry row, so a partial clip lists with its real length
// instead of the requested span.
func projectClipDurationToCommodore(ctx context.Context, tenantID, artifactHash string, durationMs int64, logger logging.Logger) {
	if CommodoreClient == nil || tenantID == "" || artifactHash == "" || durationMs <= 0 {
		return
	}
	notifyCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := CommodoreClient.UpdateClipDuration(notifyCtx, tenantID, artifactHash, durationMs); err != nil {
		logger.WithError(err).WithFields(logging.Fields{
			"artifact_hash": artifactHash,
			"duration_ms":   durationMs,
		}).Warn("Failed to notify Commodore of clip duration")
	}
}
