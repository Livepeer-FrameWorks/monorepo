package periscopeingestdb

import (
	"context"

	"github.com/google/uuid"
)

const insertViewerSessionAnomalous = `
	INSERT INTO periscope.viewer_sessions_anomalous (
		tenant_id, node_id, session_id,
		cluster_id, stream_id, stream_name,
		estimated_duration_seconds,
		observed_first_at_ms, observed_last_at_ms,
		closed_at_ms, closed_reason, projection_version_ms,
		notes
	)`

type ViewerSessionAnomalousRow struct {
	TenantID                 uuid.UUID
	NodeID                   string
	SessionID                string
	ClusterID                string
	StreamID                 uuid.UUID
	StreamName               string
	EstimatedDurationSeconds uint32
	ObservedFirstAtMS        int64
	ObservedLastAtMS         int64
	ClosedAtMS               int64
	ClosedReason             string
	ProjectionVersionMS      int64
	Notes                    string
}

func PrepareViewerSessionAnomalous(ctx context.Context, db BatchPreparer) (*Writer[ViewerSessionAnomalousRow], error) {
	return prepare(ctx, db, insertViewerSessionAnomalous, func(row ViewerSessionAnomalousRow) []interface{} {
		return []interface{}{
			row.TenantID, row.NodeID, row.SessionID,
			row.ClusterID, row.StreamID, row.StreamName,
			row.EstimatedDurationSeconds,
			row.ObservedFirstAtMS, row.ObservedLastAtMS,
			row.ClosedAtMS, row.ClosedReason, row.ProjectionVersionMS,
			row.Notes,
		}
	})
}

const insertStreamSessionAnomalous = `
	INSERT INTO periscope.stream_sessions_anomalous (
		tenant_id, node_id, stream_id,
		cluster_id, stream_name,
		estimated_duration_seconds,
		observed_first_at_ms, observed_last_at_ms,
		closed_at_ms, closed_reason, projection_version_ms,
		notes
	)`

type StreamSessionAnomalousRow struct {
	TenantID                 uuid.UUID
	NodeID                   string
	StreamID                 uuid.UUID
	ClusterID                string
	StreamName               string
	EstimatedDurationSeconds uint32
	ObservedFirstAtMS        int64
	ObservedLastAtMS         int64
	ClosedAtMS               int64
	ClosedReason             string
	ProjectionVersionMS      int64
	Notes                    string
}

func PrepareStreamSessionAnomalous(ctx context.Context, db BatchPreparer) (*Writer[StreamSessionAnomalousRow], error) {
	return prepare(ctx, db, insertStreamSessionAnomalous, func(row StreamSessionAnomalousRow) []interface{} {
		return []interface{}{
			row.TenantID, row.NodeID, row.StreamID,
			row.ClusterID, row.StreamName,
			row.EstimatedDurationSeconds,
			row.ObservedFirstAtMS, row.ObservedLastAtMS,
			row.ClosedAtMS, row.ClosedReason, row.ProjectionVersionMS,
			row.Notes,
		}
	})
}
