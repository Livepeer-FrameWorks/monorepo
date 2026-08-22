package periscopeingestdb

import (
	"context"

	"github.com/google/uuid"
)

const insertViewerSessionFinal = `INSERT INTO periscope.viewer_sessions_final (
	tenant_id, node_id, session_id, source_event_id,
	cluster_id, stream_id, stream_name, connector, host,
	country_code, city, latitude, longitude, tags,
	duration_seconds, uploaded_bytes, downloaded_bytes, seconds_connected,
	source_started_at_ms, source_ended_at_ms, edge_received_at_ms, projection_version_ms,
	closed_reason, stream_times, connector_times, host_times, payload_raw
)`

type ViewerSessionFinalRow struct {
	TenantID            uuid.UUID
	NodeID              string
	SessionID           string
	SourceEventID       string
	ClusterID           string
	StreamID            uuid.UUID
	StreamName          string
	Connector           string
	Host                string
	CountryCode         string
	City                string
	Latitude            float64
	Longitude           float64
	Tags                string
	DurationSeconds     uint32
	UploadedBytes       uint64
	DownloadedBytes     uint64
	SecondsConnected    uint64
	SourceStartedAtMS   int64
	SourceEndedAtMS     int64
	EdgeReceivedAtMS    int64
	ProjectionVersionMS int64
	ClosedReason        string
	StreamTimes         [][]any
	ConnectorTimes      [][]any
	HostTimes           [][]any
	PayloadRaw          []byte
}

func PrepareViewerSessionFinal(ctx context.Context, db BatchPreparer) (*Writer[ViewerSessionFinalRow], error) {
	return prepare(ctx, db, insertViewerSessionFinal, func(row ViewerSessionFinalRow) []interface{} {
		return []interface{}{
			row.TenantID, row.NodeID, row.SessionID, row.SourceEventID,
			row.ClusterID, row.StreamID, row.StreamName, row.Connector, row.Host,
			row.CountryCode, row.City, row.Latitude, row.Longitude, row.Tags,
			row.DurationSeconds, row.UploadedBytes, row.DownloadedBytes, row.SecondsConnected,
			row.SourceStartedAtMS, row.SourceEndedAtMS, row.EdgeReceivedAtMS, row.ProjectionVersionMS,
			row.ClosedReason, row.StreamTimes, row.ConnectorTimes, row.HostTimes, row.PayloadRaw,
		}
	})
}

const insertStreamSessionFinal = `INSERT INTO periscope.stream_sessions_final (
	tenant_id, node_id, stream_id, source_event_id,
	cluster_id, stream_name,
	downloaded_bytes, uploaded_bytes, total_viewers, total_inputs, total_outputs, viewer_seconds,
	source_started_at_ms, source_ended_at_ms, edge_received_at_ms, projection_version_ms,
	closed_reason, payload_raw
)`

type StreamSessionFinalRow struct {
	TenantID            uuid.UUID
	NodeID              string
	StreamID            uuid.UUID
	SourceEventID       string
	ClusterID           string
	StreamName          string
	DownloadedBytes     int64
	UploadedBytes       int64
	TotalViewers        int64
	TotalInputs         int64
	TotalOutputs        int64
	ViewerSeconds       int64
	SourceStartedAtMS   int64
	SourceEndedAtMS     int64
	EdgeReceivedAtMS    int64
	ProjectionVersionMS int64
	ClosedReason        string
	PayloadRaw          []byte
}

func PrepareStreamSessionFinal(ctx context.Context, db BatchPreparer) (*Writer[StreamSessionFinalRow], error) {
	return prepare(ctx, db, insertStreamSessionFinal, func(row StreamSessionFinalRow) []interface{} {
		return []interface{}{
			row.TenantID, row.NodeID, row.StreamID, row.SourceEventID,
			row.ClusterID, row.StreamName,
			row.DownloadedBytes, row.UploadedBytes, row.TotalViewers, row.TotalInputs, row.TotalOutputs, row.ViewerSeconds,
			row.SourceStartedAtMS, row.SourceEndedAtMS, row.EdgeReceivedAtMS, row.ProjectionVersionMS,
			row.ClosedReason, row.PayloadRaw,
		}
	})
}

const insertProcessingSegmentFinal = `INSERT INTO periscope.processing_segments_final (
	tenant_id, node_id, stream_id, process_type, output_codec, track_type, segment_number,
	source_event_id,
	cluster_id, stream_name, input_codec, media_seconds,
	width, height, rendition_count, input_bytes, output_bytes_total, turnaround_ms, speed_factor, livepeer_session_id, renditions_json,
	input_frames, output_frames, input_frames_delta, output_frames_delta, input_bytes_delta, output_bytes_delta,
	rtf_in, rtf_out, is_final,
	source_started_at_ms, source_ended_at_ms, edge_received_at_ms, projection_version_ms,
	payload_raw
)`

type ProcessingSegmentFinalRow struct {
	TenantID            uuid.UUID
	NodeID              string
	StreamID            uuid.UUID
	ProcessType         string
	OutputCodec         string
	TrackType           string
	SegmentNumber       int32
	SourceEventID       string
	ClusterID           string
	StreamName          string
	InputCodec          string
	MediaSeconds        float64
	Width               int32
	Height              int32
	RenditionCount      int32
	InputBytes          int64
	OutputBytesTotal    int64
	TurnaroundMS        int64
	SpeedFactor         float64
	LivepeerSessionID   string
	RenditionsJSON      string
	InputFrames         int64
	OutputFrames        int64
	InputFramesDelta    int64
	OutputFramesDelta   int64
	InputBytesDelta     int64
	OutputBytesDelta    int64
	RTFIn               float64
	RTFOut              float64
	IsFinal             uint8
	SourceStartedAtMS   int64
	SourceEndedAtMS     int64
	EdgeReceivedAtMS    int64
	ProjectionVersionMS int64
	PayloadRaw          []byte
}

func PrepareProcessingSegmentFinal(ctx context.Context, db BatchPreparer) (*Writer[ProcessingSegmentFinalRow], error) {
	return prepare(ctx, db, insertProcessingSegmentFinal, func(row ProcessingSegmentFinalRow) []interface{} {
		return []interface{}{
			row.TenantID, row.NodeID, row.StreamID, row.ProcessType, row.OutputCodec, row.TrackType, row.SegmentNumber,
			row.SourceEventID,
			row.ClusterID, row.StreamName, row.InputCodec, row.MediaSeconds,
			row.Width, row.Height, row.RenditionCount, row.InputBytes, row.OutputBytesTotal, row.TurnaroundMS, row.SpeedFactor, row.LivepeerSessionID, row.RenditionsJSON,
			row.InputFrames, row.OutputFrames, row.InputFramesDelta, row.OutputFramesDelta, row.InputBytesDelta, row.OutputBytesDelta,
			row.RTFIn, row.RTFOut, row.IsFinal,
			row.SourceStartedAtMS, row.SourceEndedAtMS, row.EdgeReceivedAtMS, row.ProjectionVersionMS,
			row.PayloadRaw,
		}
	})
}

const insertProjectionDivergence = `INSERT INTO periscope.projection_divergences (
	observed_at_ms, table_name, meter, field,
	natural_key_json, prior_value_json, new_value_json, source_event_id
)`

type ProjectionDivergenceRow struct {
	ObservedAtMS   int64
	TableName      string
	Meter          string
	Field          string
	NaturalKeyJSON string
	PriorValueJSON string
	NewValueJSON   string
	SourceEventID  string
}

func PrepareProjectionDivergence(ctx context.Context, db BatchPreparer) (*Writer[ProjectionDivergenceRow], error) {
	return prepare(ctx, db, insertProjectionDivergence, func(row ProjectionDivergenceRow) []interface{} {
		return []interface{}{
			row.ObservedAtMS, row.TableName, row.Meter, row.Field,
			row.NaturalKeyJSON, row.PriorValueJSON, row.NewValueJSON, row.SourceEventID,
		}
	})
}
