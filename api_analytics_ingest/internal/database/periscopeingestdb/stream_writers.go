package periscopeingestdb

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const insertStreamBufferEvent = `INSERT INTO stream_event_log (
	timestamp, event_id, tenant_id, stream_id, internal_name, node_id, cluster_id, event_type, status,
	buffer_state, has_issues, issues_description, track_count,
	quality_tier, primary_width, primary_height, primary_fps, event_data,
	source_region, stream_origin_region, stream_origin_cluster_id, schema_version
)`

type StreamBufferEventRow struct {
	Timestamp                                               time.Time
	EventID, TenantID, StreamID                             uuid.UUID
	InternalName, NodeID, ClusterID, EventType              string
	Status, BufferState                                     *string
	HasIssues                                               *uint8
	IssuesDescription                                       *string
	TrackCount                                              *uint16
	QualityTier                                             *string
	PrimaryWidth, PrimaryHeight                             *uint16
	PrimaryFPS                                              *float32
	EventData                                               string
	SourceRegion, StreamOriginRegion, StreamOriginClusterID string
	SchemaVersion                                           uint8
}

func PrepareStreamBufferEvent(ctx context.Context, db BatchPreparer) (*Writer[StreamBufferEventRow], error) {
	return prepare(ctx, db, insertStreamBufferEvent, func(row StreamBufferEventRow) []interface{} {
		return []interface{}{row.Timestamp, row.EventID, row.TenantID, row.StreamID, row.InternalName, row.NodeID, row.ClusterID, row.EventType, row.Status, row.BufferState, row.HasIssues, row.IssuesDescription, row.TrackCount, row.QualityTier, row.PrimaryWidth, row.PrimaryHeight, row.PrimaryFPS, row.EventData, row.SourceRegion, row.StreamOriginRegion, row.StreamOriginClusterID, row.SchemaVersion}
	})
}

const insertStreamBufferHealth = `INSERT INTO stream_health_samples (
	timestamp, tenant_id, stream_id, internal_name, node_id, buffer_state,
	has_issues, issues_description, track_count, track_metadata,
	bitrate, fps, width, height, codec, quality_tier,
	frame_ms_max, frame_ms_min, keyframe_ms_max, keyframe_ms_min, frame_jitter_ms,
	frames_max, frames_min, gop_size, buffer_size, buffer_health,
	audio_channels, audio_sample_rate, audio_codec, audio_bitrate,
	source_region, stream_origin_region, stream_origin_cluster_id, schema_version
)`

type StreamBufferHealthRow struct {
	Timestamp                                                           time.Time
	TenantID, StreamID                                                  uuid.UUID
	InternalName, NodeID, BufferState                                   string
	HasIssues                                                           *uint8
	IssuesDescription                                                   *string
	TrackCount                                                          *uint16
	TrackMetadata                                                       string
	Bitrate                                                             *uint32
	FPS                                                                 *float32
	Width, Height                                                       *uint16
	Codec, QualityTier                                                  *string
	FrameMSMax, FrameMSMin, KeyframeMSMax, KeyframeMSMin, FrameJitterMS *float32
	FramesMax, FramesMin                                                *uint32
	GOPSize                                                             *uint16
	BufferSize                                                          *uint32
	BufferHealth                                                        *float32
	AudioChannels                                                       *uint8
	AudioSampleRate                                                     *uint32
	AudioCodec                                                          *string
	AudioBitrate                                                        *uint32
	SourceRegion, StreamOriginRegion, StreamOriginClusterID             string
	SchemaVersion                                                       uint8
}

func PrepareStreamBufferHealth(ctx context.Context, db BatchPreparer) (*Writer[StreamBufferHealthRow], error) {
	return prepare(ctx, db, insertStreamBufferHealth, func(row StreamBufferHealthRow) []interface{} {
		return []interface{}{row.Timestamp, row.TenantID, row.StreamID, row.InternalName, row.NodeID, row.BufferState, row.HasIssues, row.IssuesDescription, row.TrackCount, row.TrackMetadata, row.Bitrate, row.FPS, row.Width, row.Height, row.Codec, row.QualityTier, row.FrameMSMax, row.FrameMSMin, row.KeyframeMSMax, row.KeyframeMSMin, row.FrameJitterMS, row.FramesMax, row.FramesMin, row.GOPSize, row.BufferSize, row.BufferHealth, row.AudioChannels, row.AudioSampleRate, row.AudioCodec, row.AudioBitrate, row.SourceRegion, row.StreamOriginRegion, row.StreamOriginClusterID, row.SchemaVersion}
	})
}

const insertStreamEndEvent = `INSERT INTO stream_event_log (
	timestamp, event_id, tenant_id, stream_id, internal_name, node_id, cluster_id, event_type,
	downloaded_bytes, uploaded_bytes, total_viewers, total_inputs, total_outputs,
	viewer_seconds, event_data, source_region, stream_origin_region, stream_origin_cluster_id, schema_version
)`

type StreamEndEventRow struct {
	Timestamp                                               time.Time
	EventID, TenantID, StreamID                             uuid.UUID
	InternalName, NodeID, ClusterID, EventType              string
	DownloadedBytes, UploadedBytes                          *uint64
	TotalViewers                                            *uint32
	TotalInputs, TotalOutputs                               *uint16
	ViewerSeconds                                           *uint64
	EventData                                               string
	SourceRegion, StreamOriginRegion, StreamOriginClusterID string
	SchemaVersion                                           uint8
}

func PrepareStreamEndEvent(ctx context.Context, db BatchPreparer) (*Writer[StreamEndEventRow], error) {
	return prepare(ctx, db, insertStreamEndEvent, func(row StreamEndEventRow) []interface{} {
		return []interface{}{row.Timestamp, row.EventID, row.TenantID, row.StreamID, row.InternalName, row.NodeID, row.ClusterID, row.EventType, row.DownloadedBytes, row.UploadedBytes, row.TotalViewers, row.TotalInputs, row.TotalOutputs, row.ViewerSeconds, row.EventData, row.SourceRegion, row.StreamOriginRegion, row.StreamOriginClusterID, row.SchemaVersion}
	})
}

const insertTrackListEvent = `INSERT INTO track_list_events (
	timestamp, event_id, tenant_id, stream_id, internal_name, node_id,
	track_list, track_count, video_track_count, audio_track_count,
	primary_width, primary_height, primary_fps, primary_video_codec, primary_video_bitrate,
	quality_tier, primary_audio_channels, primary_audio_sample_rate, primary_audio_codec, primary_audio_bitrate,
	source_region, stream_origin_region, stream_origin_cluster_id, schema_version
)`

type TrackListEventRow struct {
	Timestamp                                               time.Time
	EventID, TenantID, StreamID                             uuid.UUID
	InternalName, NodeID, TrackList                         string
	TrackCount, VideoTrackCount, AudioTrackCount            uint16
	PrimaryWidth, PrimaryHeight                             *uint16
	PrimaryFPS                                              *float32
	PrimaryVideoCodec                                       *string
	PrimaryVideoBitrate                                     *uint32
	QualityTier                                             *string
	PrimaryAudioChannels                                    *uint8
	PrimaryAudioSampleRate                                  *uint32
	PrimaryAudioCodec                                       *string
	PrimaryAudioBitrate                                     *uint32
	SourceRegion, StreamOriginRegion, StreamOriginClusterID string
	SchemaVersion                                           uint8
}

func PrepareTrackListEvent(ctx context.Context, db BatchPreparer) (*Writer[TrackListEventRow], error) {
	return prepare(ctx, db, insertTrackListEvent, func(row TrackListEventRow) []interface{} {
		return []interface{}{row.Timestamp, row.EventID, row.TenantID, row.StreamID, row.InternalName, row.NodeID, row.TrackList, row.TrackCount, row.VideoTrackCount, row.AudioTrackCount, row.PrimaryWidth, row.PrimaryHeight, row.PrimaryFPS, row.PrimaryVideoCodec, row.PrimaryVideoBitrate, row.QualityTier, row.PrimaryAudioChannels, row.PrimaryAudioSampleRate, row.PrimaryAudioCodec, row.PrimaryAudioBitrate, row.SourceRegion, row.StreamOriginRegion, row.StreamOriginClusterID, row.SchemaVersion}
	})
}

const insertTrackListStreamEvent = `INSERT INTO stream_event_log (
	timestamp, event_id, tenant_id, stream_id, internal_name, node_id, cluster_id, event_type, status,
	event_data, source_region, stream_origin_region, stream_origin_cluster_id, schema_version
)`

type TrackListStreamEventRow struct {
	Timestamp                                               time.Time
	EventID, TenantID, StreamID                             uuid.UUID
	InternalName, NodeID, ClusterID, EventType              string
	Status                                                  *string
	EventData                                               string
	SourceRegion, StreamOriginRegion, StreamOriginClusterID string
	SchemaVersion                                           uint8
}

func PrepareTrackListStreamEvent(ctx context.Context, db BatchPreparer) (*Writer[TrackListStreamEventRow], error) {
	return prepare(ctx, db, insertTrackListStreamEvent, func(row TrackListStreamEventRow) []interface{} {
		return []interface{}{row.Timestamp, row.EventID, row.TenantID, row.StreamID, row.InternalName, row.NodeID, row.ClusterID, row.EventType, row.Status, row.EventData, row.SourceRegion, row.StreamOriginRegion, row.StreamOriginClusterID, row.SchemaVersion}
	})
}
