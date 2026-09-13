package control

import (
	"context"

	"frameworks/api_balancing/internal/database/foghorndb"
)

// The recording owner's clock anchors chapters. Repeated reports cannot move
// that anchor; a terminal acknowledgement may supply it after StopDVR won the
// finalization race, allowing the terminal chapter backfill to recover.
func recordDVRStartTime(ctx context.Context, hash, tenantID, nodeID string, startedAt int64) error {
	if startedAt <= 0 {
		return nil
	}
	return foghorndb.New(db).RecordDVRStartTime(ctx, foghorndb.RecordDVRStartTimeParams{
		ArtifactHash: hash, TenantID: tenantID, NodeID: nodeID, StartedAtUnix: startedAt,
	})
}
