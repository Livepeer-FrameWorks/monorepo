package periscopeingestdb

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const insertPushRewriteEvent = `INSERT INTO stream_event_log (
	timestamp, event_id, tenant_id, stream_id, internal_name, node_id, cluster_id, event_type, status,
	request_url, protocol, latitude, longitude, location, country_code, city,
	event_data, source_region, stream_origin_region, stream_origin_cluster_id, schema_version
)`

type PushRewriteEventRow struct {
	Timestamp                                               time.Time
	EventID, TenantID, StreamID                             uuid.UUID
	InternalName, NodeID, ClusterID, EventType              string
	Status, RequestURL, Protocol                            *string
	Latitude, Longitude                                     *float64
	Location, CountryCode, City                             *string
	EventData                                               string
	SourceRegion, StreamOriginRegion, StreamOriginClusterID string
	SchemaVersion                                           uint8
}

func PreparePushRewriteEvent(ctx context.Context, db BatchPreparer) (*Writer[PushRewriteEventRow], error) {
	return prepare(ctx, db, insertPushRewriteEvent, func(row PushRewriteEventRow) []interface{} {
		return []interface{}{row.Timestamp, row.EventID, row.TenantID, row.StreamID, row.InternalName, row.NodeID, row.ClusterID, row.EventType, row.Status, row.RequestURL, row.Protocol, row.Latitude, row.Longitude, row.Location, row.CountryCode, row.City, row.EventData, row.SourceRegion, row.StreamOriginRegion, row.StreamOriginClusterID, row.SchemaVersion}
	})
}

const insertRoutingDecision = `INSERT INTO routing_decisions (
	timestamp, tenant_id, stream_id, internal_name, selected_node, status, details, score,
	client_ip, client_country, client_latitude, client_longitude, client_bucket_h3, client_bucket_res,
	node_latitude, node_longitude, node_name, node_bucket_h3, node_bucket_res,
	selected_node_id, routing_distance_km, stream_tenant_id, cluster_id, remote_cluster_id, latency_ms,
	candidates_count, event_type, source, source_region, stream_origin_region, stream_origin_cluster_id, schema_version
)`

type RoutingDecisionRow struct {
	Timestamp             time.Time
	TenantID              uuid.UUID
	StreamID              uuid.UUID
	InternalName          string
	SelectedNode          string
	Status                string
	Details               string
	Score                 int64
	ClientIP              string
	ClientCountry         string
	ClientLatitude        float64
	ClientLongitude       float64
	ClientBucketH3        *uint64
	ClientBucketRes       *uint8
	NodeLatitude          float64
	NodeLongitude         float64
	NodeName              string
	NodeBucketH3          *uint64
	NodeBucketRes         *uint8
	SelectedNodeID        *string
	RoutingDistanceKM     *float64
	StreamTenantID        *uuid.UUID
	ClusterID             string
	RemoteClusterID       string
	LatencyMS             *float32
	CandidatesCount       *int32
	EventType             *string
	Source                *string
	SourceRegion          string
	StreamOriginRegion    string
	StreamOriginClusterID string
	SchemaVersion         uint8
}

func PrepareRoutingDecision(ctx context.Context, db BatchPreparer) (*Writer[RoutingDecisionRow], error) {
	return prepare(ctx, db, insertRoutingDecision, func(row RoutingDecisionRow) []interface{} {
		return []interface{}{row.Timestamp, row.TenantID, row.StreamID, row.InternalName, row.SelectedNode, row.Status, row.Details, row.Score, row.ClientIP, row.ClientCountry, row.ClientLatitude, row.ClientLongitude, row.ClientBucketH3, row.ClientBucketRes, row.NodeLatitude, row.NodeLongitude, row.NodeName, row.NodeBucketH3, row.NodeBucketRes, row.SelectedNodeID, row.RoutingDistanceKM, row.StreamTenantID, row.ClusterID, row.RemoteClusterID, row.LatencyMS, row.CandidatesCount, row.EventType, row.Source, row.SourceRegion, row.StreamOriginRegion, row.StreamOriginClusterID, row.SchemaVersion}
	})
}

const insertClientQOESample = `INSERT INTO client_qoe_samples (
	timestamp, event_id, tenant_id, stream_id, internal_name, session_id, node_id, protocol, host,
	connection_time, position, bandwidth_in, bandwidth_out, bytes_downloaded, bytes_uploaded,
	packets_sent, packets_lost, packets_retransmitted, connection_quality,
	source_region, stream_origin_region, stream_origin_cluster_id, schema_version
)`

type ClientQOESampleRow struct {
	Timestamp             time.Time
	EventID               *uuid.UUID
	TenantID              uuid.UUID
	StreamID              uuid.UUID
	InternalName          string
	SessionID             string
	NodeID                string
	Protocol              string
	Host                  string
	ConnectionTime        float32
	Position              *float32
	BandwidthIn           uint64
	BandwidthOut          uint64
	BytesDownloaded       uint64
	BytesUploaded         uint64
	PacketsSent           uint64
	PacketsLost           uint64
	PacketsRetransmitted  uint64
	ConnectionQuality     *float32
	SourceRegion          string
	StreamOriginRegion    string
	StreamOriginClusterID string
	SchemaVersion         uint8
}

func PrepareClientQOESample(ctx context.Context, db BatchPreparer) (*Writer[ClientQOESampleRow], error) {
	return prepare(ctx, db, insertClientQOESample, func(row ClientQOESampleRow) []interface{} {
		return []interface{}{row.Timestamp, row.EventID, row.TenantID, row.StreamID, row.InternalName, row.SessionID, row.NodeID, row.Protocol, row.Host, row.ConnectionTime, row.Position, row.BandwidthIn, row.BandwidthOut, row.BytesDownloaded, row.BytesUploaded, row.PacketsSent, row.PacketsLost, row.PacketsRetransmitted, row.ConnectionQuality, row.SourceRegion, row.StreamOriginRegion, row.StreamOriginClusterID, row.SchemaVersion}
	})
}

const insertPlayerBootSample = `INSERT INTO player_boot_samples (
	timestamp, event_id, tenant_id, stream_id, artifact_hash, internal_name, session_id, trace_id,
	node_id, serving_cluster_id, origin_cluster_id, cluster_attributed,
	total_ttf_ms, gateway_resolve_ms, mist_hydrate_ms, player_select_ms, connect_ms, prebuffer_ms,
	outcome, error_code, player_type, protocol, content_type, is_live, connection_type, player_version,
	manifest_url, manifest_ms, manifest_transfer_size, first_segment_url, first_segment_ms, first_segment_transfer_size,
	cdn_cache_status, age_seconds, resources, source_region, stream_origin_region, stream_origin_cluster_id, schema_version
)`

type PlayerBootSampleRow struct {
	Timestamp                time.Time
	EventID                  uuid.UUID
	TenantID                 uuid.UUID
	StreamID                 *uuid.UUID
	ArtifactHash             string
	InternalName             string
	SessionID                string
	TraceID                  string
	NodeID                   string
	ServingClusterID         string
	OriginClusterID          string
	ClusterAttributed        uint8
	TotalTTFMS               uint32
	GatewayResolveMS         uint32
	MistHydrateMS            uint32
	PlayerSelectMS           uint32
	ConnectMS                uint32
	PrebufferMS              uint32
	Outcome                  string
	ErrorCode                string
	PlayerType               string
	Protocol                 string
	ContentType              string
	IsLive                   uint8
	ConnectionType           string
	PlayerVersion            string
	ManifestURL              string
	ManifestMS               uint32
	ManifestTransferSize     uint64
	FirstSegmentURL          string
	FirstSegmentMS           uint32
	FirstSegmentTransferSize uint64
	CDNCacheStatus           string
	AgeSeconds               *uint32
	Resources                string
	SourceRegion             string
	StreamOriginRegion       string
	StreamOriginClusterID    string
	SchemaVersion            uint8
}

func PreparePlayerBootSample(ctx context.Context, db BatchPreparer) (*Writer[PlayerBootSampleRow], error) {
	return prepare(ctx, db, insertPlayerBootSample, func(row PlayerBootSampleRow) []interface{} {
		return []interface{}{row.Timestamp, row.EventID, row.TenantID, row.StreamID, row.ArtifactHash, row.InternalName, row.SessionID, row.TraceID, row.NodeID, row.ServingClusterID, row.OriginClusterID, row.ClusterAttributed, row.TotalTTFMS, row.GatewayResolveMS, row.MistHydrateMS, row.PlayerSelectMS, row.ConnectMS, row.PrebufferMS, row.Outcome, row.ErrorCode, row.PlayerType, row.Protocol, row.ContentType, row.IsLive, row.ConnectionType, row.PlayerVersion, row.ManifestURL, row.ManifestMS, row.ManifestTransferSize, row.FirstSegmentURL, row.FirstSegmentMS, row.FirstSegmentTransferSize, row.CDNCacheStatus, row.AgeSeconds, row.Resources, row.SourceRegion, row.StreamOriginRegion, row.StreamOriginClusterID, row.SchemaVersion}
	})
}

const insertClientQOESessionDelta = `INSERT INTO client_qoe_session_deltas (
	timestamp, event_id, tenant_id, stream_id, artifact_hash, internal_name, content_id, session_id,
	beacon_seq, is_final, flush_reason, node_id, serving_cluster_id, origin_cluster_id, cluster_attributed,
	player_type, protocol, content_type, is_live, connection_type, player_version,
	played_ms, rebuffer_ms, rebuffer_count, seek_wait_ms, frame_stats_supported, frames_decoded, frames_dropped, frames_corrupted,
	first_frame, fatal_error, error_code, bitrate_bps_seconds, abr_upswitch_count, abr_downswitch_count, play_intent, live_edge_latency_ms,
	bucket_width_s, asset_duration_s, max_bucket_reached, source_region, stream_origin_region, stream_origin_cluster_id, schema_version
)`

type ClientQOESessionDeltaRow struct {
	Timestamp                                                         time.Time
	EventID                                                           uuid.UUID
	TenantID                                                          uuid.UUID
	StreamID                                                          *uuid.UUID
	ArtifactHash, InternalName, ContentID, SessionID                  string
	BeaconSeq                                                         uint32
	IsFinal                                                           uint8
	FlushReason, NodeID, ServingClusterID, OriginClusterID            string
	ClusterAttributed                                                 uint8
	PlayerType, Protocol, ContentType                                 string
	IsLive                                                            uint8
	ConnectionType, PlayerVersion                                     string
	PlayedMS, RebufferMS                                              uint64
	RebufferCount                                                     uint32
	SeekWaitMS                                                        uint64
	FrameStatsSupported                                               uint8
	FramesDecoded, FramesDropped, FramesCorrupted                     uint64
	FirstFrame, FatalError                                            uint8
	ErrorCode                                                         string
	BitrateBPSSeconds                                                 uint64
	ABRUpswitchCount, ABRDownswitchCount                              uint32
	PlayIntent                                                        uint8
	LiveEdgeLatencyMS, BucketWidthS, AssetDurationS, MaxBucketReached uint32
	SourceRegion, StreamOriginRegion, StreamOriginClusterID           string
	SchemaVersion                                                     uint8
}

func PrepareClientQOESessionDelta(ctx context.Context, db BatchPreparer) (*Writer[ClientQOESessionDeltaRow], error) {
	return prepare(ctx, db, insertClientQOESessionDelta, func(row ClientQOESessionDeltaRow) []interface{} {
		return []interface{}{row.Timestamp, row.EventID, row.TenantID, row.StreamID, row.ArtifactHash, row.InternalName, row.ContentID, row.SessionID, row.BeaconSeq, row.IsFinal, row.FlushReason, row.NodeID, row.ServingClusterID, row.OriginClusterID, row.ClusterAttributed, row.PlayerType, row.Protocol, row.ContentType, row.IsLive, row.ConnectionType, row.PlayerVersion, row.PlayedMS, row.RebufferMS, row.RebufferCount, row.SeekWaitMS, row.FrameStatsSupported, row.FramesDecoded, row.FramesDropped, row.FramesCorrupted, row.FirstFrame, row.FatalError, row.ErrorCode, row.BitrateBPSSeconds, row.ABRUpswitchCount, row.ABRDownswitchCount, row.PlayIntent, row.LiveEdgeLatencyMS, row.BucketWidthS, row.AssetDurationS, row.MaxBucketReached, row.SourceRegion, row.StreamOriginRegion, row.StreamOriginClusterID, row.SchemaVersion}
	})
}

const insertVODRetentionBucket = `INSERT INTO vod_retention_buckets (
	timestamp, event_id, tenant_id, artifact_hash, internal_name, content_id, session_id,
	beacon_seq, bucket_width_s, asset_duration_s, bucket_index, seconds_watched, source_region, schema_version
)`

type VODRetentionBucketRow struct {
	Timestamp                                            time.Time
	EventID                                              uuid.UUID
	TenantID                                             uuid.UUID
	ArtifactHash, InternalName, ContentID, SessionID     string
	BeaconSeq, BucketWidthS, AssetDurationS, BucketIndex uint32
	SecondsWatched                                       float32
	SourceRegion                                         string
	SchemaVersion                                        uint8
}

func PrepareVODRetentionBucket(ctx context.Context, db BatchPreparer) (*Writer[VODRetentionBucketRow], error) {
	return prepare(ctx, db, insertVODRetentionBucket, func(row VODRetentionBucketRow) []interface{} {
		return []interface{}{row.Timestamp, row.EventID, row.TenantID, row.ArtifactHash, row.InternalName, row.ContentID, row.SessionID, row.BeaconSeq, row.BucketWidthS, row.AssetDurationS, row.BucketIndex, row.SecondsWatched, row.SourceRegion, row.SchemaVersion}
	})
}

const insertNodeState = `INSERT INTO node_state_current (
	tenant_id, cluster_id, node_id, cpu_percent, ram_used_bytes, ram_total_bytes,
	disk_used_bytes, disk_total_bytes, up_speed, down_speed, bw_limit,
	active_streams, is_healthy, operational_mode, latitude, longitude, location, metadata, updated_at
)`

type NodeStateRow struct {
	TenantID                                                   uuid.UUID
	ClusterID, NodeID                                          string
	CPUPercent                                                 float32
	RAMUsedBytes, RAMTotalBytes, DiskUsedBytes, DiskTotalBytes uint64
	UpSpeed, DownSpeed, BWLimit                                uint64
	ActiveStreams                                              uint32
	IsHealthy                                                  uint8
	OperationalMode                                            string
	Latitude, Longitude                                        float64
	Location                                                   string
	Metadata                                                   []byte
	UpdatedAt                                                  time.Time
}

func PrepareNodeState(ctx context.Context, db BatchPreparer) (*Writer[NodeStateRow], error) {
	return prepare(ctx, db, insertNodeState, func(row NodeStateRow) []interface{} {
		return []interface{}{row.TenantID, row.ClusterID, row.NodeID, row.CPUPercent, row.RAMUsedBytes, row.RAMTotalBytes, row.DiskUsedBytes, row.DiskTotalBytes, row.UpSpeed, row.DownSpeed, row.BWLimit, row.ActiveStreams, row.IsHealthy, row.OperationalMode, row.Latitude, row.Longitude, row.Location, row.Metadata, row.UpdatedAt}
	})
}

const insertNodeMetricsSample = `INSERT INTO node_metrics_samples (
	timestamp, tenant_id, cluster_id, node_id, cpu_usage, ram_max, ram_current,
	shm_total_bytes, shm_used_bytes, disk_total_bytes, disk_used_bytes,
	bandwidth_in, bandwidth_out, up_speed, down_speed, connections_current,
	stream_count, is_healthy, operational_mode, latitude, longitude, metadata,
	source_region, stream_origin_region, stream_origin_cluster_id, schema_version
)`

type NodeMetricsSampleRow struct {
	Timestamp                                                                      time.Time
	TenantID                                                                       uuid.UUID
	ClusterID, NodeID                                                              string
	CPUUsage                                                                       float32
	RAMMax, RAMCurrent, SHMTotalBytes, SHMUsedBytes, DiskTotalBytes, DiskUsedBytes uint64
	BandwidthIn, BandwidthOut, UpSpeed, DownSpeed                                  uint64
	ConnectionsCurrent, StreamCount                                                uint32
	IsHealthy                                                                      uint8
	OperationalMode                                                                string
	Latitude, Longitude                                                            float64
	Metadata                                                                       []byte
	SourceRegion, StreamOriginRegion, StreamOriginClusterID                        string
	SchemaVersion                                                                  uint8
}

func PrepareNodeMetricsSample(ctx context.Context, db BatchPreparer) (*Writer[NodeMetricsSampleRow], error) {
	return prepare(ctx, db, insertNodeMetricsSample, func(row NodeMetricsSampleRow) []interface{} {
		return []interface{}{row.Timestamp, row.TenantID, row.ClusterID, row.NodeID, row.CPUUsage, row.RAMMax, row.RAMCurrent, row.SHMTotalBytes, row.SHMUsedBytes, row.DiskTotalBytes, row.DiskUsedBytes, row.BandwidthIn, row.BandwidthOut, row.UpSpeed, row.DownSpeed, row.ConnectionsCurrent, row.StreamCount, row.IsHealthy, row.OperationalMode, row.Latitude, row.Longitude, row.Metadata, row.SourceRegion, row.StreamOriginRegion, row.StreamOriginClusterID, row.SchemaVersion}
	})
}
