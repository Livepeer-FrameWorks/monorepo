package periscopeingestdb

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const insertLedgerRebuildCursor = `INSERT INTO periscope.ledger_rebuild_cursors (
	ledger_name, last_processed_projection_ms, updated_at_ms
)`

type LedgerRebuildCursorRow struct {
	LedgerName                string
	LastProcessedProjectionMS int64
	UpdatedAtMS               int64
}

func PrepareLedgerRebuildCursor(ctx context.Context, db BatchPreparer) (*Writer[LedgerRebuildCursorRow], error) {
	return prepare(ctx, db, insertLedgerRebuildCursor, func(row LedgerRebuildCursorRow) []interface{} {
		return []interface{}{row.LedgerName, row.LastProcessedProjectionMS, row.UpdatedAtMS}
	})
}

const insertViewerUsage5m = `INSERT INTO periscope.viewer_usage_5m (
	window_start, tenant_id, cluster_id, stream_id, node_id, session_id,
	seconds_observed, up_bytes_observed, down_bytes_observed,
	source_event_id, projection_version_ms
)`

type ViewerUsage5mRow struct {
	WindowStart         time.Time
	TenantID            uuid.UUID
	ClusterID           string
	StreamID            uuid.UUID
	NodeID              string
	SessionID           string
	SecondsObserved     uint32
	UpBytesObserved     uint64
	DownBytesObserved   uint64
	SourceEventID       string
	ProjectionVersionMS int64
}

func PrepareViewerUsage5m(ctx context.Context, db BatchPreparer) (*Writer[ViewerUsage5mRow], error) {
	return prepare(ctx, db, insertViewerUsage5m, func(row ViewerUsage5mRow) []interface{} {
		return []interface{}{
			row.WindowStart, row.TenantID, row.ClusterID, row.StreamID, row.NodeID, row.SessionID,
			row.SecondsObserved, row.UpBytesObserved, row.DownBytesObserved,
			row.SourceEventID, row.ProjectionVersionMS,
		}
	})
}

const insertStreamRuntime5m = `INSERT INTO periscope.stream_runtime_5m (
	window_start, tenant_id, cluster_id, stream_id,
	active_seconds, peak_viewers,
	source_event_id, projection_version_ms
)`

type StreamRuntime5mRow struct {
	WindowStart         time.Time
	TenantID            uuid.UUID
	ClusterID           string
	StreamID            uuid.UUID
	ActiveSeconds       uint32
	PeakViewers         uint32
	SourceEventID       string
	ProjectionVersionMS int64
}

func PrepareStreamRuntime5m(ctx context.Context, db BatchPreparer) (*Writer[StreamRuntime5mRow], error) {
	return prepare(ctx, db, insertStreamRuntime5m, func(row StreamRuntime5mRow) []interface{} {
		return []interface{}{
			row.WindowStart, row.TenantID, row.ClusterID, row.StreamID,
			row.ActiveSeconds, row.PeakViewers,
			row.SourceEventID, row.ProjectionVersionMS,
		}
	})
}

const insertStorageGBSeconds5m = `INSERT INTO periscope.storage_gb_seconds_5m (
	window_start, tenant_id, cluster_id, storage_scope,
	storage_provider_tenant_id, storage_provider_cluster_id, storage_backend,
	gb_seconds, file_count, projection_version_ms
)`

type StorageGBSeconds5mRow struct {
	WindowStart              time.Time
	TenantID                 uuid.UUID
	ClusterID                string
	StorageScope             string
	StorageProviderTenantID  string
	StorageProviderClusterID string
	StorageBackend           string
	GBSeconds                float64
	FileCount                uint64
	ProjectionVersionMS      int64
}

func PrepareStorageGBSeconds5m(ctx context.Context, db BatchPreparer) (*Writer[StorageGBSeconds5mRow], error) {
	return prepare(ctx, db, insertStorageGBSeconds5m, func(row StorageGBSeconds5mRow) []interface{} {
		return []interface{}{
			row.WindowStart, row.TenantID, row.ClusterID, row.StorageScope,
			row.StorageProviderTenantID, row.StorageProviderClusterID, row.StorageBackend,
			row.GBSeconds, row.FileCount, row.ProjectionVersionMS,
		}
	})
}

const insertProcessing5m = `INSERT INTO periscope.processing_5m (
	window_start, tenant_id, cluster_id, stream_id, process_type, output_codec, track_type, source_event_id,
	media_seconds, projection_version_ms
)`

type Processing5mRow struct {
	WindowStart         time.Time
	TenantID            uuid.UUID
	ClusterID           string
	StreamID            uuid.UUID
	ProcessType         string
	OutputCodec         string
	TrackType           string
	SourceEventID       string
	MediaSeconds        float64
	ProjectionVersionMS int64
}

func PrepareProcessing5m(ctx context.Context, db BatchPreparer) (*Writer[Processing5mRow], error) {
	return prepare(ctx, db, insertProcessing5m, func(row Processing5mRow) []interface{} {
		return []interface{}{
			row.WindowStart, row.TenantID, row.ClusterID, row.StreamID, row.ProcessType, row.OutputCodec, row.TrackType, row.SourceEventID,
			row.MediaSeconds, row.ProjectionVersionMS,
		}
	})
}
