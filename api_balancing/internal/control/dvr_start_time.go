package control

import (
	"context"

	"frameworks/api_balancing/internal/database/foghorndb"
)

// The first recorded segment anchors chapters (InsertDVRSegment). When no
// segment set the anchor, the recording owner's terminal acknowledgement
// supplies its start time so the terminal chapter backfill can still run.
// An anchor that is already set never moves.
func recordDVRStartTime(ctx context.Context, hash, tenantID, nodeID string, startedAt int64) error {
	if startedAt <= 0 {
		return nil
	}
	return foghorndb.New(db).RecordDVRStartTime(ctx, foghorndb.RecordDVRStartTimeParams{
		ArtifactHash: hash, TenantID: tenantID, NodeID: nodeID, StartedAtUnix: startedAt,
	})
}
