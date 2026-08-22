package periscopeingestdb

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const insertRawMistTrigger = `INSERT INTO periscope.raw_mist_triggers (
	node_id, trigger_type, source_request_id,
	payload, tenant_id, cluster_id,
	received_at_ms, forwarded_at_ms, ingested_at_ms, schema_version
)`

type RawMistTriggerRow struct {
	NodeID          string
	TriggerType     string
	SourceRequestID string
	Payload         []byte
	TenantID        string
	ClusterID       string
	ReceivedAtMS    int64
	ForwardedAtMS   int64
	IngestedAtMS    int64
	SchemaVersion   int32
}

func PrepareRawMistTrigger(ctx context.Context, db BatchPreparer) (*Writer[RawMistTriggerRow], error) {
	return prepare(ctx, db, insertRawMistTrigger, func(row RawMistTriggerRow) []interface{} {
		return []interface{}{
			row.NodeID, row.TriggerType, row.SourceRequestID,
			row.Payload, row.TenantID, row.ClusterID,
			row.ReceivedAtMS, row.ForwardedAtMS, row.IngestedAtMS, row.SchemaVersion,
		}
	})
}

const insertStorageSnapshot = `INSERT INTO storage_snapshots (
	timestamp, node_id, tenant_id, cluster_id, storage_scope,
	storage_provider_tenant_id, storage_provider_cluster_id, storage_backend,
	total_bytes, file_count, dvr_bytes, clip_bytes, vod_bytes,
	frozen_dvr_bytes, frozen_clip_bytes, frozen_vod_bytes,
	ingested_at_ms
)`

type StorageSnapshotRow struct {
	Timestamp                time.Time
	NodeID                   string
	TenantID                 uuid.UUID
	ClusterID                string
	StorageScope             string
	StorageProviderTenantID  string
	StorageProviderClusterID string
	StorageBackend           string
	TotalBytes               uint64
	FileCount                uint32
	DVRBytes                 uint64
	ClipBytes                uint64
	VODBytes                 uint64
	FrozenDVRBytes           uint64
	FrozenClipBytes          uint64
	FrozenVODBytes           uint64
	IngestedAtMS             int64
}

func PrepareStorageSnapshot(ctx context.Context, db BatchPreparer) (*Writer[StorageSnapshotRow], error) {
	return prepare(ctx, db, insertStorageSnapshot, func(row StorageSnapshotRow) []interface{} {
		return []interface{}{
			row.Timestamp, row.NodeID, row.TenantID, row.ClusterID, row.StorageScope,
			row.StorageProviderTenantID, row.StorageProviderClusterID, row.StorageBackend,
			row.TotalBytes, row.FileCount, row.DVRBytes, row.ClipBytes, row.VODBytes,
			row.FrozenDVRBytes, row.FrozenClipBytes, row.FrozenVODBytes,
			row.IngestedAtMS,
		}
	})
}

const insertStreamLifecycleState = `INSERT INTO stream_state_current (
	tenant_id, stream_id, internal_name, node_id, status, buffer_state,
	current_viewers, total_inputs, uploaded_bytes, downloaded_bytes,
	viewer_seconds, has_issues, issues_description,
	track_count, quality_tier, primary_width, primary_height,
	primary_fps, primary_codec, primary_bitrate,
	packets_sent, packets_lost, packets_retransmitted,
	started_at, updated_at
)`

type StreamLifecycleStateRow struct {
	TenantID             uuid.UUID
	StreamID             uuid.UUID
	InternalName         string
	NodeID               string
	Status               string
	BufferState          string
	CurrentViewers       uint32
	TotalInputs          uint16
	UploadedBytes        uint64
	DownloadedBytes      uint64
	ViewerSeconds        uint64
	HasIssues            *uint8
	IssuesDescription    *string
	TrackCount           *uint16
	QualityTier          *string
	PrimaryWidth         *uint16
	PrimaryHeight        *uint16
	PrimaryFPS           *float32
	PrimaryCodec         *string
	PrimaryBitrate       *uint32
	PacketsSent          *uint64
	PacketsLost          *uint64
	PacketsRetransmitted *uint64
	StartedAt            *time.Time
	UpdatedAt            time.Time
}

func PrepareStreamLifecycleState(ctx context.Context, db BatchPreparer) (*Writer[StreamLifecycleStateRow], error) {
	return prepare(ctx, db, insertStreamLifecycleState, func(row StreamLifecycleStateRow) []interface{} {
		return []interface{}{
			row.TenantID, row.StreamID, row.InternalName, row.NodeID, row.Status, row.BufferState,
			row.CurrentViewers, row.TotalInputs, row.UploadedBytes, row.DownloadedBytes,
			row.ViewerSeconds, row.HasIssues, row.IssuesDescription,
			row.TrackCount, row.QualityTier, row.PrimaryWidth, row.PrimaryHeight,
			row.PrimaryFPS, row.PrimaryCodec, row.PrimaryBitrate,
			row.PacketsSent, row.PacketsLost, row.PacketsRetransmitted,
			row.StartedAt, row.UpdatedAt,
		}
	})
}

const insertStreamLifecycleEvent = `INSERT INTO stream_event_log (
	timestamp, event_id, tenant_id, stream_id, internal_name, node_id, cluster_id, event_type, status,
	buffer_state, downloaded_bytes, uploaded_bytes, total_viewers, total_inputs,
	total_outputs, viewer_seconds, has_issues, issues_description,
	track_count, quality_tier, primary_width, primary_height, primary_fps, event_data,
	source_region, stream_origin_region, stream_origin_cluster_id, schema_version
)`

type StreamLifecycleEventRow struct {
	Timestamp             time.Time
	EventID               uuid.UUID
	TenantID              uuid.UUID
	StreamID              uuid.UUID
	InternalName          string
	NodeID                string
	ClusterID             string
	EventType             string
	Status                *string
	BufferState           *string
	DownloadedBytes       *uint64
	UploadedBytes         *uint64
	TotalViewers          *uint32
	TotalInputs           *uint16
	TotalOutputs          *uint16
	ViewerSeconds         *uint64
	HasIssues             *uint8
	IssuesDescription     *string
	TrackCount            *uint16
	QualityTier           *string
	PrimaryWidth          *uint16
	PrimaryHeight         *uint16
	PrimaryFPS            *float32
	EventData             string
	SourceRegion          string
	StreamOriginRegion    string
	StreamOriginClusterID string
	SchemaVersion         uint8
}

func PrepareStreamLifecycleEvent(ctx context.Context, db BatchPreparer) (*Writer[StreamLifecycleEventRow], error) {
	return prepare(ctx, db, insertStreamLifecycleEvent, func(row StreamLifecycleEventRow) []interface{} {
		return []interface{}{
			row.Timestamp, row.EventID, row.TenantID, row.StreamID, row.InternalName, row.NodeID, row.ClusterID, row.EventType, row.Status,
			row.BufferState, row.DownloadedBytes, row.UploadedBytes, row.TotalViewers, row.TotalInputs,
			row.TotalOutputs, row.ViewerSeconds, row.HasIssues, row.IssuesDescription,
			row.TrackCount, row.QualityTier, row.PrimaryWidth, row.PrimaryHeight, row.PrimaryFPS, row.EventData,
			row.SourceRegion, row.StreamOriginRegion, row.StreamOriginClusterID, row.SchemaVersion,
		}
	})
}

const insertStreamLifecycleHealth = `INSERT INTO stream_health_samples (
	timestamp, tenant_id, stream_id, internal_name, node_id,
	bitrate, fps, width, height, codec, quality_tier,
	buffer_state, buffer_size, buffer_health,
	has_issues, issues_description, track_count,
	track_metadata,
	audio_channels, audio_sample_rate, audio_codec, audio_bitrate,
	source_region, stream_origin_region, stream_origin_cluster_id, schema_version
)`

type StreamLifecycleHealthRow struct {
	Timestamp             time.Time
	TenantID              uuid.UUID
	StreamID              uuid.UUID
	InternalName          string
	NodeID                string
	Bitrate               *uint32
	FPS                   *float32
	Width                 *uint16
	Height                *uint16
	Codec                 *string
	QualityTier           *string
	BufferState           string
	BufferSize            *uint32
	BufferHealth          *float32
	HasIssues             *uint8
	IssuesDescription     *string
	TrackCount            *uint16
	TrackMetadata         string
	AudioChannels         *uint8
	AudioSampleRate       *uint32
	AudioCodec            *string
	AudioBitrate          *uint32
	SourceRegion          string
	StreamOriginRegion    string
	StreamOriginClusterID string
	SchemaVersion         uint8
}

func PrepareStreamLifecycleHealth(ctx context.Context, db BatchPreparer) (*Writer[StreamLifecycleHealthRow], error) {
	return prepare(ctx, db, insertStreamLifecycleHealth, func(row StreamLifecycleHealthRow) []interface{} {
		return []interface{}{
			row.Timestamp, row.TenantID, row.StreamID, row.InternalName, row.NodeID,
			row.Bitrate, row.FPS, row.Width, row.Height, row.Codec, row.QualityTier,
			row.BufferState, row.BufferSize, row.BufferHealth,
			row.HasIssues, row.IssuesDescription, row.TrackCount,
			row.TrackMetadata,
			row.AudioChannels, row.AudioSampleRate, row.AudioCodec, row.AudioBitrate,
			row.SourceRegion, row.StreamOriginRegion, row.StreamOriginClusterID, row.SchemaVersion,
		}
	})
}

const insertViewerConnectionEvent = `INSERT INTO viewer_connection_events (
	event_id, timestamp, tenant_id, stream_id, internal_name,
	session_id, connection_addr, connector, node_id,
	cluster_id, origin_cluster_id,
	request_url,
	country_code, city, latitude, longitude,
	client_bucket_h3, client_bucket_res, node_bucket_h3, node_bucket_res,
	event_type, session_duration, bytes_transferred,
	source_region, stream_origin_region, stream_origin_cluster_id, schema_version
)`

type ViewerConnectionEventRow struct {
	EventID               uuid.UUID
	Timestamp             time.Time
	TenantID              uuid.UUID
	StreamID              uuid.UUID
	InternalName          string
	SessionID             string
	ConnectionAddr        string
	Connector             string
	NodeID                string
	ClusterID             string
	OriginClusterID       string
	RequestURL            *string
	CountryCode           string
	City                  string
	Latitude              float64
	Longitude             float64
	ClientBucketH3        *uint64
	ClientBucketRes       *uint8
	NodeBucketH3          *uint64
	NodeBucketRes         *uint8
	EventType             string
	SessionDuration       uint32
	BytesTransferred      uint64
	SourceRegion          string
	StreamOriginRegion    string
	StreamOriginClusterID string
	SchemaVersion         uint8
}

func PrepareViewerConnectionEvent(ctx context.Context, db BatchPreparer) (*Writer[ViewerConnectionEventRow], error) {
	return prepare(ctx, db, insertViewerConnectionEvent, func(row ViewerConnectionEventRow) []interface{} {
		return []interface{}{
			row.EventID, row.Timestamp, row.TenantID, row.StreamID, row.InternalName,
			row.SessionID, row.ConnectionAddr, row.Connector, row.NodeID,
			row.ClusterID, row.OriginClusterID,
			row.RequestURL,
			row.CountryCode, row.City, row.Latitude, row.Longitude,
			row.ClientBucketH3, row.ClientBucketRes, row.NodeBucketH3, row.NodeBucketRes,
			row.EventType, row.SessionDuration, row.BytesTransferred,
			row.SourceRegion, row.StreamOriginRegion, row.StreamOriginClusterID, row.SchemaVersion,
		}
	})
}

const insertIngestError = `INSERT INTO ingest_errors (
	received_at, event_id, event_type, source, tenant_id, stream_id, error, payload_json,
	source_region, stream_origin_region, stream_origin_cluster_id, schema_version
)`

type IngestErrorRow struct {
	ReceivedAt            time.Time
	EventID               string
	EventType             string
	Source                string
	TenantID              string
	StreamID              string
	Error                 string
	PayloadJSON           string
	SourceRegion          string
	StreamOriginRegion    string
	StreamOriginClusterID string
	SchemaVersion         uint8
}

func PrepareIngestError(ctx context.Context, db BatchPreparer) (*Writer[IngestErrorRow], error) {
	return prepare(ctx, db, insertIngestError, func(row IngestErrorRow) []interface{} {
		return []interface{}{
			row.ReceivedAt, row.EventID, row.EventType, row.Source, row.TenantID, row.StreamID, row.Error, row.PayloadJSON,
			row.SourceRegion, row.StreamOriginRegion, row.StreamOriginClusterID, row.SchemaVersion,
		}
	})
}
