package periscopeingestdb

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const insertStorageEvent = `INSERT INTO storage_events (
	timestamp, tenant_id, stream_id, internal_name, asset_hash,
	action, asset_type, size_bytes, s3_url, local_path,
	node_id, duration_ms, warm_duration_ms, error, cluster_id, origin_cluster_id,
	source_region, stream_origin_region, stream_origin_cluster_id, schema_version
)`

type StorageEventRow struct {
	Timestamp                                                                           time.Time
	TenantID, StreamID                                                                  uuid.UUID
	InternalName, AssetHash, Action, AssetType                                          string
	SizeBytes                                                                           uint64
	S3URL, LocalPath, NodeID                                                            *string
	DurationMS, WarmDurationMS                                                          *int64
	Error                                                                               *string
	ClusterID, OriginClusterID, SourceRegion, StreamOriginRegion, StreamOriginClusterID string
	SchemaVersion                                                                       uint8
}

func PrepareStorageEvent(ctx context.Context, db BatchPreparer) (*Writer[StorageEventRow], error) {
	return prepare(ctx, db, insertStorageEvent, func(row StorageEventRow) []interface{} {
		return []interface{}{row.Timestamp, row.TenantID, row.StreamID, row.InternalName, row.AssetHash, row.Action, row.AssetType, row.SizeBytes, row.S3URL, row.LocalPath, row.NodeID, row.DurationMS, row.WarmDurationMS, row.Error, row.ClusterID, row.OriginClusterID, row.SourceRegion, row.StreamOriginRegion, row.StreamOriginClusterID, row.SchemaVersion}
	})
}

const insertFederationEvent = `INSERT INTO federation_events (
	timestamp, tenant_id, event_type, local_cluster, remote_cluster, stream_name, stream_id,
	source_node, dest_node, dtsc_url, latency_ms, time_to_live_ms, failure_reason,
	queried_clusters, responding_clusters, total_candidates, best_remote_score,
	peer_cluster, role, reason, blocked_cluster, existing_replication_cluster,
	local_lat, local_lon, remote_lat, remote_lon,
	source_region, stream_origin_region, stream_origin_cluster_id, schema_version
)`

type FederationEventRow struct {
	Timestamp                                               time.Time
	TenantID                                                uuid.UUID
	EventType, LocalCluster, RemoteCluster, StreamName      string
	StreamID                                                *uuid.UUID
	SourceNode, DestNode, DTSCURL                           *string
	LatencyMS, TimeToLiveMS                                 *float32
	FailureReason                                           *string
	QueriedClusters, RespondingClusters, TotalCandidates    *uint32
	BestRemoteScore                                         *uint64
	PeerCluster                                             *string
	Role                                                    string
	Reason, BlockedCluster, ExistingReplicationCluster      *string
	LocalLat, LocalLon, RemoteLat, RemoteLon                *float64
	SourceRegion, StreamOriginRegion, StreamOriginClusterID string
	SchemaVersion                                           uint8
}

func PrepareFederationEvent(ctx context.Context, db BatchPreparer) (*Writer[FederationEventRow], error) {
	return prepare(ctx, db, insertFederationEvent, func(row FederationEventRow) []interface{} {
		return []interface{}{row.Timestamp, row.TenantID, row.EventType, row.LocalCluster, row.RemoteCluster, row.StreamName, row.StreamID, row.SourceNode, row.DestNode, row.DTSCURL, row.LatencyMS, row.TimeToLiveMS, row.FailureReason, row.QueriedClusters, row.RespondingClusters, row.TotalCandidates, row.BestRemoteScore, row.PeerCluster, row.Role, row.Reason, row.BlockedCluster, row.ExistingReplicationCluster, row.LocalLat, row.LocalLon, row.RemoteLat, row.RemoteLon, row.SourceRegion, row.StreamOriginRegion, row.StreamOriginClusterID, row.SchemaVersion}
	})
}

const insertProcessingEvent = `INSERT INTO processing_events (
	timestamp, tenant_id, node_id, cluster_id, origin_cluster_id, stream_id, internal_name,
	process_type, track_type, duration_ms, input_codec, output_codec,
	segment_number, width, height, rendition_count, broadcaster_url, upload_time_us,
	livepeer_session_id, segment_start_ms, input_bytes, output_bytes_total,
	attempt_count, turnaround_ms, speed_factor, renditions_json,
	input_frames, output_frames, decode_us_per_frame, transform_us_per_frame, encode_us_per_frame, is_final,
	input_frames_delta, output_frames_delta, input_bytes_delta, output_bytes_delta,
	input_width, input_height, output_width, output_height,
	input_fpks, output_fps_measured, sample_rate, channels,
	source_timestamp_ms, sink_timestamp_ms, source_advanced_ms, sink_advanced_ms,
	rtf_in, rtf_out, pipeline_lag_ms, output_bitrate_bps,
	source_region, stream_origin_region, stream_origin_cluster_id, schema_version
)`

type ProcessingEventRow struct {
	Timestamp                                                                          time.Time
	TenantID                                                                           uuid.UUID
	NodeID, ClusterID, OriginClusterID                                                 string
	StreamID                                                                           uuid.UUID
	InternalName, ProcessType, TrackType                                               string
	DurationMS                                                                         int64
	InputCodec, OutputCodec                                                            *string
	SegmentNumber, Width, Height, RenditionCount                                       *int32
	BroadcasterURL                                                                     *string
	UploadTimeUS                                                                       *int64
	LivepeerSessionID                                                                  *string
	SegmentStartMS, InputBytes, OutputBytesTotal                                       *int64
	AttemptCount                                                                       *int32
	TurnaroundMS                                                                       *int64
	SpeedFactor                                                                        *float64
	RenditionsJSON                                                                     *string
	InputFrames, OutputFrames, DecodeUSPerFrame, TransformUSPerFrame, EncodeUSPerFrame *int64
	IsFinal                                                                            *uint8
	InputFramesDelta, OutputFramesDelta, InputBytesDelta, OutputBytesDelta             *int64
	InputWidth, InputHeight, OutputWidth, OutputHeight, InputFPKS                      *int32
	OutputFPSMeasured                                                                  *float64
	SampleRate, Channels                                                               *int32
	SourceTimestampMS, SinkTimestampMS, SourceAdvancedMS, SinkAdvancedMS               *int64
	RTFIn, RTFOut                                                                      *float64
	PipelineLagMS, OutputBitrateBPS                                                    *int64
	SourceRegion, StreamOriginRegion, StreamOriginClusterID                            string
	SchemaVersion                                                                      uint8
}

func PrepareProcessingEvent(ctx context.Context, db BatchPreparer) (*Writer[ProcessingEventRow], error) {
	return prepare(ctx, db, insertProcessingEvent, func(row ProcessingEventRow) []interface{} {
		return []interface{}{row.Timestamp, row.TenantID, row.NodeID, row.ClusterID, row.OriginClusterID, row.StreamID, row.InternalName, row.ProcessType, row.TrackType, row.DurationMS, row.InputCodec, row.OutputCodec, row.SegmentNumber, row.Width, row.Height, row.RenditionCount, row.BroadcasterURL, row.UploadTimeUS, row.LivepeerSessionID, row.SegmentStartMS, row.InputBytes, row.OutputBytesTotal, row.AttemptCount, row.TurnaroundMS, row.SpeedFactor, row.RenditionsJSON, row.InputFrames, row.OutputFrames, row.DecodeUSPerFrame, row.TransformUSPerFrame, row.EncodeUSPerFrame, row.IsFinal, row.InputFramesDelta, row.OutputFramesDelta, row.InputBytesDelta, row.OutputBytesDelta, row.InputWidth, row.InputHeight, row.OutputWidth, row.OutputHeight, row.InputFPKS, row.OutputFPSMeasured, row.SampleRate, row.Channels, row.SourceTimestampMS, row.SinkTimestampMS, row.SourceAdvancedMS, row.SinkAdvancedMS, row.RTFIn, row.RTFOut, row.PipelineLagMS, row.OutputBitrateBPS, row.SourceRegion, row.StreamOriginRegion, row.StreamOriginClusterID, row.SchemaVersion}
	})
}
