package periscopeingestdb

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type ArtifactStateBase struct {
	TenantID, StreamID                  uuid.UUID
	RequestID, InternalName             string
	Filename                            *string
	ContentType, Stage                  string
	ProgressPercent                     uint8
	ErrorMessage                        *string
	RequestedAt                         time.Time
	StartedAt, CompletedAt              *time.Time
	FilePath, S3URL                     *string
	SizeBytes                           *uint64
	ProcessingNodeID                    *string
	UpdatedAt                           time.Time
	ExpiresAt                           *time.Time
	StorageLocation, SyncStatus         *string
	HasLocalCopy, IsSynced, IsFinalized *bool
}

const insertClipArtifactState = `INSERT INTO artifact_state_current (
	tenant_id, stream_id, request_id, internal_name, filename, content_type, stage,
	progress_percent, error_message, requested_at, started_at, completed_at,
	clip_start_unix, clip_stop_unix, file_path, s3_url, size_bytes,
	processing_node_id, updated_at, expires_at,
	storage_location, sync_status, has_local_copy, is_synced, is_finalized
)`

type ClipArtifactStateRow struct {
	ArtifactStateBase
	ClipStartUnix, ClipStopUnix *int64
}

func PrepareClipArtifactState(ctx context.Context, db BatchPreparer) (*Writer[ClipArtifactStateRow], error) {
	return prepare(ctx, db, insertClipArtifactState, func(row ClipArtifactStateRow) []interface{} {
		return []interface{}{row.TenantID, row.StreamID, row.RequestID, row.InternalName, row.Filename, row.ContentType, row.Stage, row.ProgressPercent, row.ErrorMessage, row.RequestedAt, row.StartedAt, row.CompletedAt, row.ClipStartUnix, row.ClipStopUnix, row.FilePath, row.S3URL, row.SizeBytes, row.ProcessingNodeID, row.UpdatedAt, row.ExpiresAt, row.StorageLocation, row.SyncStatus, row.HasLocalCopy, row.IsSynced, row.IsFinalized}
	})
}

const insertDVRArtifactState = `INSERT INTO artifact_state_current (
	tenant_id, stream_id, request_id, internal_name, filename, content_type, stage,
	progress_percent, error_message, requested_at, started_at, completed_at,
	segment_count, manifest_path, file_path, size_bytes, processing_node_id, updated_at, expires_at,
	storage_location, sync_status, has_local_copy, is_synced, is_finalized
)`

type DVRArtifactStateRow struct {
	ArtifactStateBase
	SegmentCount *uint32
	ManifestPath *string
}

func PrepareDVRArtifactState(ctx context.Context, db BatchPreparer) (*Writer[DVRArtifactStateRow], error) {
	return prepare(ctx, db, insertDVRArtifactState, func(row DVRArtifactStateRow) []interface{} {
		return []interface{}{row.TenantID, row.StreamID, row.RequestID, row.InternalName, row.Filename, row.ContentType, row.Stage, row.ProgressPercent, row.ErrorMessage, row.RequestedAt, row.StartedAt, row.CompletedAt, row.SegmentCount, row.ManifestPath, row.FilePath, row.SizeBytes, row.ProcessingNodeID, row.UpdatedAt, row.ExpiresAt, row.StorageLocation, row.SyncStatus, row.HasLocalCopy, row.IsSynced, row.IsFinalized}
	})
}

const insertVODArtifactState = `INSERT INTO artifact_state_current (
	tenant_id, stream_id, request_id, internal_name, filename, content_type, stage,
	progress_percent, error_message, requested_at, started_at, completed_at,
	file_path, s3_url, size_bytes, processing_node_id, updated_at, expires_at,
	storage_location, sync_status, has_local_copy, is_synced, is_finalized
)`

type VODArtifactStateRow struct{ ArtifactStateBase }

func PrepareVODArtifactState(ctx context.Context, db BatchPreparer) (*Writer[VODArtifactStateRow], error) {
	return prepare(ctx, db, insertVODArtifactState, func(row VODArtifactStateRow) []interface{} {
		return []interface{}{row.TenantID, row.StreamID, row.RequestID, row.InternalName, row.Filename, row.ContentType, row.Stage, row.ProgressPercent, row.ErrorMessage, row.RequestedAt, row.StartedAt, row.CompletedAt, row.FilePath, row.S3URL, row.SizeBytes, row.ProcessingNodeID, row.UpdatedAt, row.ExpiresAt, row.StorageLocation, row.SyncStatus, row.HasLocalCopy, row.IsSynced, row.IsFinalized}
	})
}

type ArtifactEventBase struct {
	Timestamp                                               time.Time
	TenantID, StreamID                                      uuid.UUID
	InternalName, ClusterID, OriginClusterID                string
	Filename                                                *string
	RequestID, Stage, ContentType                           string
	IngestNodeID, Message, FilePath, S3URL                  *string
	SizeBytes                                               *uint64
	ExpiresAt                                               *int64
	SourceRegion, StreamOriginRegion, StreamOriginClusterID string
	SchemaVersion                                           uint8
	EventID                                                 string
}

type ArtifactSpeedFields struct {
	ProcessingWallMS                            *uint64
	SpeedMinX, SpeedAvgX, SpeedMaxX             *float32
	HardSlowTicks, StaleHoldTicks, LockoutTicks *uint32
	DrainMS                                     *uint64
}

const insertClipArtifactEvent = `INSERT INTO artifact_events (
	timestamp, tenant_id, stream_id, internal_name, cluster_id, origin_cluster_id,
	filename, request_id, stage, content_type, start_unix, stop_unix, ingest_node_id,
	percent, message, file_path, s3_url, size_bytes, expires_at,
	source_region, stream_origin_region, stream_origin_cluster_id, schema_version,
	processing_wall_ms, speed_min_x, speed_avg_x, speed_max_x,
	hard_slow_ticks, stale_hold_ticks, lockout_ticks, drain_ms, event_id
)`

type ClipArtifactEventRow struct {
	ArtifactEventBase
	StartUnix, StopUnix *int64
	Percent             *uint32
	ArtifactSpeedFields
}

func PrepareClipArtifactEvent(ctx context.Context, db BatchPreparer) (*Writer[ClipArtifactEventRow], error) {
	return prepare(ctx, db, insertClipArtifactEvent, func(row ClipArtifactEventRow) []interface{} {
		return []interface{}{row.Timestamp, row.TenantID, row.StreamID, row.InternalName, row.ClusterID, row.OriginClusterID, row.Filename, row.RequestID, row.Stage, row.ContentType, row.StartUnix, row.StopUnix, row.IngestNodeID, row.Percent, row.Message, row.FilePath, row.S3URL, row.SizeBytes, row.ExpiresAt, row.SourceRegion, row.StreamOriginRegion, row.StreamOriginClusterID, row.SchemaVersion, row.ProcessingWallMS, row.SpeedMinX, row.SpeedAvgX, row.SpeedMaxX, row.HardSlowTicks, row.StaleHoldTicks, row.LockoutTicks, row.DrainMS, row.EventID}
	})
}

const insertDVRArtifactEvent = `INSERT INTO artifact_events (
	timestamp, tenant_id, stream_id, internal_name, cluster_id, origin_cluster_id,
	filename, request_id, stage, content_type, start_unix, stop_unix, ingest_node_id,
	file_path, size_bytes, message, expires_at,
	source_region, stream_origin_region, stream_origin_cluster_id, schema_version, event_id
)`

type DVRArtifactEventRow struct {
	ArtifactEventBase
	StartUnix, StopUnix *int64
}

func PrepareDVRArtifactEvent(ctx context.Context, db BatchPreparer) (*Writer[DVRArtifactEventRow], error) {
	return prepare(ctx, db, insertDVRArtifactEvent, func(row DVRArtifactEventRow) []interface{} {
		return []interface{}{row.Timestamp, row.TenantID, row.StreamID, row.InternalName, row.ClusterID, row.OriginClusterID, row.Filename, row.RequestID, row.Stage, row.ContentType, row.StartUnix, row.StopUnix, row.IngestNodeID, row.FilePath, row.SizeBytes, row.Message, row.ExpiresAt, row.SourceRegion, row.StreamOriginRegion, row.StreamOriginClusterID, row.SchemaVersion, row.EventID}
	})
}

const insertVODArtifactEvent = `INSERT INTO artifact_events (
	timestamp, tenant_id, stream_id, internal_name, cluster_id, origin_cluster_id,
	filename, request_id, stage, content_type, ingest_node_id, file_path, s3_url, size_bytes, message, expires_at,
	source_region, stream_origin_region, stream_origin_cluster_id, schema_version,
	processing_wall_ms, speed_min_x, speed_avg_x, speed_max_x,
	hard_slow_ticks, stale_hold_ticks, lockout_ticks, drain_ms, event_id
)`

type VODArtifactEventRow struct {
	ArtifactEventBase
	ArtifactSpeedFields
}

func PrepareVODArtifactEvent(ctx context.Context, db BatchPreparer) (*Writer[VODArtifactEventRow], error) {
	return prepare(ctx, db, insertVODArtifactEvent, func(row VODArtifactEventRow) []interface{} {
		return []interface{}{row.Timestamp, row.TenantID, row.StreamID, row.InternalName, row.ClusterID, row.OriginClusterID, row.Filename, row.RequestID, row.Stage, row.ContentType, row.IngestNodeID, row.FilePath, row.S3URL, row.SizeBytes, row.Message, row.ExpiresAt, row.SourceRegion, row.StreamOriginRegion, row.StreamOriginClusterID, row.SchemaVersion, row.ProcessingWallMS, row.SpeedMinX, row.SpeedAvgX, row.SpeedMaxX, row.HardSlowTicks, row.StaleHoldTicks, row.LockoutTicks, row.DrainMS, row.EventID}
	})
}
