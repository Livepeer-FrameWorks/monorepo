package grpc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"frameworks/api_analytics_query/internal/metrics"
	"frameworks/pkg/database"
	"frameworks/pkg/grpcutil"
	"frameworks/pkg/logging"
	"frameworks/pkg/middleware"
	"frameworks/pkg/pagination"
	pb "frameworks/pkg/proto"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// PeriscopeServer implements all Periscope gRPC services
// All queries use ClickHouse only - no PostgreSQL dependency
type PeriscopeServer struct {
	pb.UnimplementedStreamAnalyticsServiceServer
	pb.UnimplementedViewerAnalyticsServiceServer
	pb.UnimplementedTrackAnalyticsServiceServer
	pb.UnimplementedConnectionAnalyticsServiceServer
	pb.UnimplementedNodeAnalyticsServiceServer
	pb.UnimplementedRoutingAnalyticsServiceServer
	pb.UnimplementedPlatformAnalyticsServiceServer
	pb.UnimplementedClipAnalyticsServiceServer
	pb.UnimplementedAggregatedAnalyticsServiceServer

	clickhouse database.ClickHouseConn
	logger     logging.Logger
	metrics    *metrics.Metrics
}

// NewPeriscopeServer creates a new Periscope gRPC server
func NewPeriscopeServer(clickhouse database.ClickHouseConn, logger logging.Logger) *PeriscopeServer {
	return &PeriscopeServer{
		clickhouse: clickhouse,
		logger:     logger,
	}
}

// parseEventPayload parses event_data JSON string into a protobuf Struct
func parseEventPayload(eventData string) *structpb.Struct {
	if eventData == "" {
		return nil
	}
	var data map[string]interface{}
	if err := json.Unmarshal([]byte(eventData), &data); err != nil {
		return nil
	}
	s, err := structpb.NewStruct(data)
	if err != nil {
		return nil
	}
	return s
}

// getTenantID extracts tenant_id from request or context.
// Prefers the request body value, falls back to context (set by auth interceptor).
func getTenantID(ctx context.Context, reqTenantID string) string {
	if reqTenantID != "" {
		return reqTenantID
	}
	return middleware.GetTenantID(ctx)
}

// validateTimeRangeProto validates time range from proto message
func validateTimeRangeProto(tr *pb.TimeRange) (time.Time, time.Time, error) {
	if tr == nil {
		// Default to last 24 hours
		return time.Now().Add(-24 * time.Hour), time.Now(), nil
	}
	startTime := tr.Start.AsTime()
	endTime := tr.End.AsTime()
	if startTime.IsZero() {
		startTime = time.Now().Add(-24 * time.Hour)
	}
	if endTime.IsZero() {
		endTime = time.Now()
	}
	return startTime, endTime, nil
}

// getCursorPagination extracts cursor pagination with defaults.
// Supports bidirectional pagination: forward (first/after) and backward (last/before).
func getCursorPagination(req *pb.CursorPaginationRequest) (*pagination.Params, error) {
	return pagination.Parse(req)
}

// buildCursorResponse creates a CursorPaginationResponse from results.
// resultsLen: length before trimming, limit: requested limit
// direction: pagination direction, totalCount: from COUNT query
func buildCursorResponse(resultsLen, limit int, direction pagination.Direction, totalCount int32, startCursor, endCursor string) *pb.CursorPaginationResponse {
	return pagination.BuildResponse(resultsLen, limit, direction, totalCount, startCursor, endCursor)
}

// buildKeysetConditionN returns a WHERE clause fragment for keyset pagination with N columns.
// Forward: (ts, cols...) < (cursor_ts, cursor_cols...) - fetches older items
// Backward: (ts, cols...) > (cursor_ts, cursor_cols...) - fetches newer items
func buildKeysetConditionN(params *pagination.Params, tsCol string, cols []string, cursorParts []string) (string, []interface{}, error) {
	if params.Cursor == nil || len(cols) == 0 {
		return "", nil, nil
	}
	// If a cursor is provided, it must match the expected tuple length.
	// Otherwise pagination would silently restart from the beginning.
	if len(cols) != len(cursorParts) {
		return "", nil, status.Errorf(codes.InvalidArgument, "invalid cursor tuple: expected %d parts, got %d", len(cols), len(cursorParts))
	}

	allCols := append([]string{tsCol}, cols...)
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(allCols)), ", ")
	tuple := fmt.Sprintf("(%s)", strings.Join(allCols, ", "))

	args := make([]interface{}, 0, len(allCols))
	args = append(args, params.Cursor.Timestamp)
	for _, part := range cursorParts {
		args = append(args, part)
	}

	if params.Direction == pagination.Backward {
		return fmt.Sprintf(" AND %s > (%s)", tuple, placeholders), args, nil
	}
	return fmt.Sprintf(" AND %s < (%s)", tuple, placeholders), args, nil
}

// buildOrderByN returns an ORDER BY clause for keyset pagination with N columns.
// Forward: DESC (newest first), Backward: ASC (oldest first, then reverse in Go)
func buildOrderByN(params *pagination.Params, tsCol string, cols []string) string {
	if len(cols) == 0 {
		return ""
	}

	allCols := append([]string{tsCol}, cols...)
	dir := "DESC"
	if params.Direction == pagination.Backward {
		dir = "ASC"
	}

	parts := make([]string, 0, len(allCols))
	for _, col := range allCols {
		parts = append(parts, fmt.Sprintf("%s %s", col, dir))
	}
	return " ORDER BY " + strings.Join(parts, ", ")
}

// buildKeysetCondition returns a WHERE clause fragment for keyset pagination.
// Forward: (ts, id) < (cursor_ts, cursor_id) - fetches older items
// Backward: (ts, id) > (cursor_ts, cursor_id) - fetches newer items
func buildKeysetCondition(params *pagination.Params, tsCol, idCol string) (string, []interface{}) {
	if params.Cursor == nil {
		return "", nil
	}
	cond, args, err := buildKeysetConditionN(params, tsCol, []string{idCol}, []string{params.Cursor.ID})
	if err != nil {
		return "", nil
	}
	return cond, args
}

// buildOrderBy returns an ORDER BY clause for keyset pagination.
// Forward: DESC (newest first), Backward: ASC (oldest first, then reverse in Go)
func buildOrderBy(params *pagination.Params, tsCol, idCol string) string {
	return buildOrderByN(params, tsCol, []string{idCol})
}

// buildKeysetConditionSingle returns a WHERE clause fragment for single-column keyset pagination.
func buildKeysetConditionSingle(params *pagination.Params, col string) (string, []interface{}) {
	if params.Cursor == nil {
		return "", nil
	}
	if params.Direction == pagination.Backward {
		return fmt.Sprintf(" AND %s > ?", col), []interface{}{params.Cursor.Timestamp}
	}
	return fmt.Sprintf(" AND %s < ?", col), []interface{}{params.Cursor.Timestamp}
}

// buildOrderBySingle returns an ORDER BY clause for single-column keyset pagination.
func buildOrderBySingle(params *pagination.Params, col string) string {
	if params.Direction == pagination.Backward {
		return fmt.Sprintf(" ORDER BY %s ASC", col)
	}
	return fmt.Sprintf(" ORDER BY %s DESC", col)
}

type cursorCollisionKey struct {
	Timestamp time.Time
	ID        string
}

func (s *PeriscopeServer) logCursorCollisions(label string, keys []cursorCollisionKey) {
	if len(keys) < 2 {
		return
	}
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if key.ID == "" || key.Timestamp.IsZero() {
			continue
		}
		cursorKey := fmt.Sprintf("%d:%s", key.Timestamp.UnixMilli(), key.ID)
		if _, ok := seen[cursorKey]; ok {
			s.logger.WithFields(logging.Fields{
				"cursor_key": cursorKey,
				"query":      label,
			}).Warn("Cursor collision detected")
			if s.metrics != nil && s.metrics.CursorCollisions != nil {
				s.metrics.CursorCollisions.WithLabelValues(label).Inc()
			}
			continue
		}
		seen[cursorKey] = struct{}{}
	}
}

// countAsync runs a COUNT query in a goroutine and returns the result via channel.
// This allows the count query to run in parallel with the main data query,
// cutting total latency roughly in half for paginated queries.
func (s *PeriscopeServer) countAsync(ctx context.Context, query string, args ...interface{}) <-chan int32 {
	ch := make(chan int32, 1)
	go func() {
		var count int32
		if err := s.clickhouse.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
			s.logger.WithError(err).Info("Async count query failed")
			count = 0
		}
		ch <- count
	}()
	return ch
}

// ============================================================================
// StreamAnalyticsService Implementation
// ============================================================================

func (s *PeriscopeServer) GetStreamEvents(ctx context.Context, req *pb.GetStreamEventsRequest) (*pb.GetStreamEventsResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	streamID := req.GetStreamId()
	if streamID == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_id required")
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	params, err := getCursorPagination(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	query := `
		SELECT event_id, timestamp, event_type, status, node_id, event_data, stream_id,
		       buffer_state, has_issues, track_count, quality_tier,
		       primary_width, primary_height, primary_fps, primary_codec, primary_bitrate,
		       downloaded_bytes, uploaded_bytes, total_viewers, total_inputs, total_outputs, viewer_seconds,
		       request_url, protocol, latitude, longitude, location, country_code, city
		FROM periscope.stream_event_log
		WHERE tenant_id = ? AND stream_id = ? AND timestamp >= ? AND timestamp <= ?
		  AND event_type IN ('stream_lifecycle','stream_buffer','stream_end','stream_start','track_list_update')
	`
	args := []interface{}{tenantID, streamID, startTime, endTime}

	// Add keyset pagination condition (direction-aware)
	keysetCond, keysetArgs := buildKeysetCondition(params, "timestamp", "event_id")
	if keysetCond != "" {
		query += keysetCond
		args = append(args, keysetArgs...)
	}

	query += buildOrderBy(params, "timestamp", "event_id")
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var events []*pb.StreamEvent
	for rows.Next() {
		var event pb.StreamEvent
		var ts time.Time
		var eventData string
		var bufferState, qualityTier, primaryCodec, requestURL, protocol, location, countryCode, city *string
		var hasIssues *uint8
		var trackCount *uint16
		var primaryWidth, primaryHeight *uint16
		var primaryFps *float32
		var primaryBitrate *uint32
		var downloadedBytes, uploadedBytes, viewerSeconds *uint64
		var totalViewers *uint32
		var totalInputs, totalOutputs *uint16
		var latitude, longitude *float64

		err := rows.Scan(
			&event.EventId, &ts, &event.EventType, &event.Status, &event.NodeId, &eventData, &event.StreamId,
			&bufferState, &hasIssues, &trackCount, &qualityTier,
			&primaryWidth, &primaryHeight, &primaryFps, &primaryCodec, &primaryBitrate,
			&downloadedBytes, &uploadedBytes, &totalViewers, &totalInputs, &totalOutputs, &viewerSeconds,
			&requestURL, &protocol, &latitude, &longitude, &location, &countryCode, &city,
		)
		if err != nil {
			continue
		}

		event.Timestamp = timestamppb.New(ts)
		event.EventData = eventData
		// Parse event_data JSON into event_payload struct
		if eventData != "" {
			event.EventPayload = parseEventPayload(eventData)
		}

		event.BufferState = bufferState
		if hasIssues != nil {
			v := *hasIssues != 0
			event.HasIssues = &v
		}
		if trackCount != nil {
			v := int32(*trackCount)
			event.TrackCount = &v
		}
		event.QualityTier = qualityTier
		if primaryWidth != nil {
			v := int32(*primaryWidth)
			event.PrimaryWidth = &v
		}
		if primaryHeight != nil {
			v := int32(*primaryHeight)
			event.PrimaryHeight = &v
		}
		if primaryFps != nil {
			event.PrimaryFps = primaryFps
		}
		event.PrimaryCodec = primaryCodec
		if primaryBitrate != nil {
			v := int32(*primaryBitrate)
			event.PrimaryBitrate = &v
		}
		if downloadedBytes != nil {
			event.DownloadedBytes = downloadedBytes
		}
		if uploadedBytes != nil {
			event.UploadedBytes = uploadedBytes
		}
		if totalViewers != nil {
			v := *totalViewers
			event.TotalViewers = &v
		}
		if totalInputs != nil {
			v := int32(*totalInputs)
			event.TotalInputs = &v
		}
		if totalOutputs != nil {
			v := int32(*totalOutputs)
			event.TotalOutputs = &v
		}
		if viewerSeconds != nil {
			event.ViewerSeconds = viewerSeconds
		}
		event.RequestUrl = requestURL
		event.Protocol = protocol
		if latitude != nil {
			event.Latitude = latitude
		}
		if longitude != nil {
			event.Longitude = longitude
		}
		event.Location = location
		event.CountryCode = countryCode
		event.City = city

		events = append(events, &event)
	}

	resultsLen := len(events)
	if resultsLen > params.Limit {
		events = events[:params.Limit]
	}

	// Reverse for backward pagination
	if params.Direction == pagination.Backward {
		slices.Reverse(events)
	}

	collisionKeys := make([]cursorCollisionKey, 0, len(events))
	for _, event := range events {
		if event.Timestamp == nil {
			continue
		}
		collisionKeys = append(collisionKeys, cursorCollisionKey{
			Timestamp: event.Timestamp.AsTime(),
			ID:        event.EventId,
		})
	}
	s.logCursorCollisions("stream_events", collisionKeys)

	var total int32
	if err := s.clickhouse.QueryRowContext(ctx, `
		SELECT count(*) FROM periscope.stream_event_log
		WHERE tenant_id = ? AND stream_id = ? AND timestamp >= ? AND timestamp <= ?
		  AND event_type IN ('stream_lifecycle','stream_buffer','stream_end','stream_start','track_list_update')
	`, tenantID, streamID, startTime, endTime).Scan(&total); err != nil {
		s.logger.WithError(err).Warn("Failed to get stream events total count")
	}

	var startCursor, endCursor string
	if len(events) > 0 {
		startCursor = pagination.EncodeCursor(events[0].Timestamp.AsTime(), events[0].EventId)
		endCursor = pagination.EncodeCursor(events[len(events)-1].Timestamp.AsTime(), events[len(events)-1].EventId)
	}

	return &pb.GetStreamEventsResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Events:     events,
	}, nil
}

func (s *PeriscopeServer) GetBufferEvents(ctx context.Context, req *pb.GetBufferEventsRequest) (*pb.GetBufferEventsResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	streamID := req.GetStreamId()
	if streamID == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_id required")
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	params, err := getCursorPagination(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	// stream_buffer events are stored in stream_event_log with event_type filter
	query := `
		SELECT event_id, timestamp, buffer_state, node_id, event_data
		FROM stream_event_log
		WHERE tenant_id = ? AND stream_id = ? AND event_type = 'stream_buffer'
			AND timestamp >= ? AND timestamp <= ?
	`
	args := []interface{}{tenantID, streamID, startTime, endTime}

	keysetCond, keysetArgs := buildKeysetCondition(params, "timestamp", "event_id")
	if keysetCond != "" {
		query += keysetCond
		args = append(args, keysetArgs...)
	}

	query += buildOrderBy(params, "timestamp", "event_id")
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var events []*pb.BufferEvent
	for rows.Next() {
		var event pb.BufferEvent
		var ts time.Time
		var eventData string
		var eventID uuid.UUID
		var bufferState *string

		err := rows.Scan(&eventID, &ts, &bufferState, &event.NodeId, &eventData)
		if err != nil {
			continue
		}

		event.EventId = eventID.String()
		event.Timestamp = timestamppb.New(ts)
		event.StreamId = streamID
		if bufferState != nil {
			event.Status = *bufferState
		}
		event.EventData = eventData
		if eventData != "" {
			event.EventPayload = parseEventPayload(eventData)
		}
		events = append(events, &event)
	}

	resultsLen := len(events)
	if resultsLen > params.Limit {
		events = events[:params.Limit]
	}

	if params.Direction == pagination.Backward {
		slices.Reverse(events)
	}

	collisionKeys := make([]cursorCollisionKey, 0, len(events))
	for _, event := range events {
		if event.Timestamp == nil {
			continue
		}
		collisionKeys = append(collisionKeys, cursorCollisionKey{
			Timestamp: event.Timestamp.AsTime(),
			ID:        event.EventId,
		})
	}
	s.logCursorCollisions("buffer_events", collisionKeys)

	var total int32
	if err := s.clickhouse.QueryRowContext(ctx, `
		SELECT count(*) FROM stream_event_log
		WHERE tenant_id = ? AND stream_id = ? AND event_type = 'stream_buffer'
			AND timestamp >= ? AND timestamp <= ?
	`, tenantID, streamID, startTime, endTime).Scan(&total); err != nil {
		s.logger.WithError(err).Warn("Failed to get buffer events total count")
	}

	var startCursor, endCursor string
	if len(events) > 0 {
		startCursor = pagination.EncodeCursor(events[0].Timestamp.AsTime(), events[0].EventId)
		endCursor = pagination.EncodeCursor(events[len(events)-1].Timestamp.AsTime(), events[len(events)-1].EventId)
	}

	return &pb.GetBufferEventsResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Events:     events,
	}, nil
}

func (s *PeriscopeServer) GetStreamHealthMetrics(ctx context.Context, req *pb.GetStreamHealthMetricsRequest) (*pb.GetStreamHealthMetricsResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	params, err := getCursorPagination(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	query := `
		SELECT timestamp, tenant_id, stream_id, node_id,
			bitrate, fps, gop_size, frame_ms_max, frame_ms_min, frames_max, frames_min, keyframe_ms_max, keyframe_ms_min, frame_jitter_ms, width, height,
			buffer_size, buffer_health, buffer_state,
			codec, quality_tier, track_metadata,
			has_issues, issues_description, track_count,
			audio_channels, audio_sample_rate, audio_codec, audio_bitrate
		FROM stream_health_samples
		WHERE tenant_id = ? AND timestamp >= ? AND timestamp <= ?
	`
	args := []interface{}{tenantID, startTime, endTime}

	if streamID := req.GetStreamId(); streamID != "" {
		query += " AND stream_id = ?"
		args = append(args, streamID)
	}

	keysetCond, keysetArgs := buildKeysetCondition(params, "timestamp", "concat(stream_id, ':', node_id)")
	if keysetCond != "" {
		query += keysetCond
		args = append(args, keysetArgs...)
	}

	query += buildOrderBy(params, "timestamp", "concat(stream_id, ':', node_id)")
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var metrics []*pb.StreamHealthMetric
	for rows.Next() {
		var m pb.StreamHealthMetric
		var ts time.Time
		var tenantID string
		var trackMetadata string
		var hasIssues *uint8
		var issuesDesc *string
		var trackCount *uint16
		var audioChannels *uint8
		var audioSampleRate, audioBitrate *uint32
		var audioCodec *string
		var frameMsMax, frameMsMin, keyframeMsMax, keyframeMsMin, frameJitterMs *float32
		var framesMax, framesMin *uint32

		err := rows.Scan(
			&ts, &tenantID, &m.StreamId, &m.NodeId,
			&m.Bitrate, &m.Fps, &m.GopSize, &frameMsMax, &frameMsMin, &framesMax, &framesMin, &keyframeMsMax, &keyframeMsMin, &frameJitterMs, &m.Width, &m.Height,
			&m.BufferSize, &m.BufferHealth, &m.BufferState,
			&m.Codec, &m.QualityTier, &trackMetadata,
			&hasIssues, &issuesDesc, &trackCount,
			&audioChannels, &audioSampleRate, &audioCodec, &audioBitrate,
		)
		if err != nil {
			continue
		}

		// Generate composite ID for pagination
		m.Id = fmt.Sprintf("%s_%s_%s", ts.Format(time.RFC3339), m.StreamId, m.NodeId)
		m.TenantId = tenantID
		m.Timestamp = timestamppb.New(ts)
		m.TrackMetadata = trackMetadata
		if frameMsMax != nil {
			m.FrameMsMax = frameMsMax
		}
		if frameMsMin != nil {
			m.FrameMsMin = frameMsMin
		}
		if framesMax != nil {
			v := int32(*framesMax)
			m.FramesMax = &v
		}
		if framesMin != nil {
			v := int32(*framesMin)
			m.FramesMin = &v
		}
		if keyframeMsMax != nil {
			m.KeyframeMsMax = keyframeMsMax
		}
		if keyframeMsMin != nil {
			m.KeyframeMsMin = keyframeMsMin
		}
		if frameJitterMs != nil {
			v := float64(*frameJitterMs)
			m.FrameJitterMs = &v
		}

		// Assign health issue fields (previously scanned but not assigned)
		if hasIssues != nil {
			hi := *hasIssues != 0
			m.HasIssues = &hi
		}
		m.IssuesDescription = issuesDesc
		if trackCount != nil {
			tc := int32(*trackCount)
			m.TrackCount = &tc
		}

		if audioChannels != nil {
			ch := int32(*audioChannels)
			m.PrimaryAudioChannels = &ch
		}
		if audioSampleRate != nil {
			sr := int32(*audioSampleRate)
			m.PrimaryAudioSampleRate = &sr
		}
		m.PrimaryAudioCodec = audioCodec
		if audioBitrate != nil {
			br := int32(*audioBitrate)
			m.PrimaryAudioBitrate = &br
		}
		metrics = append(metrics, &m)
	}

	resultsLen := len(metrics)
	if resultsLen > params.Limit {
		metrics = metrics[:params.Limit]
	}

	if params.Direction == pagination.Backward {
		slices.Reverse(metrics)
	}

	var total int32
	if streamID := req.GetStreamId(); streamID != "" {
		if err := s.clickhouse.QueryRowContext(ctx, `
			SELECT count(*) FROM stream_health_samples
			WHERE tenant_id = ? AND timestamp >= ? AND timestamp <= ? AND stream_id = ?
		`, tenantID, startTime, endTime, streamID).Scan(&total); err != nil {
			s.logger.WithError(err).Warn("Failed to get health samples total count")
		}
	} else {
		if err := s.clickhouse.QueryRowContext(ctx, `
			SELECT count(*) FROM stream_health_samples
			WHERE tenant_id = ? AND timestamp >= ? AND timestamp <= ?
		`, tenantID, startTime, endTime).Scan(&total); err != nil {
			s.logger.WithError(err).Warn("Failed to get health samples total count")
		}
	}

	var startCursor, endCursor string
	if len(metrics) > 0 {
		startCursor = pagination.EncodeCursor(metrics[0].Timestamp.AsTime(), fmt.Sprintf("%s:%s", metrics[0].StreamId, metrics[0].NodeId))
		endCursor = pagination.EncodeCursor(metrics[len(metrics)-1].Timestamp.AsTime(), fmt.Sprintf("%s:%s", metrics[len(metrics)-1].StreamId, metrics[len(metrics)-1].NodeId))
	}

	return &pb.GetStreamHealthMetricsResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Metrics:    metrics,
	}, nil
}

// GetStreamStatus returns operational state for a single stream (Control/Data plane separation)
// This is the source of truth for stream status - queries stream_state_current directly
func (s *PeriscopeServer) GetStreamStatus(ctx context.Context, req *pb.GetStreamStatusRequest) (*pb.StreamStatusResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	streamID := req.GetStreamId()
	if streamID == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_id required")
	}

	resp := &pb.StreamStatusResponse{
		StreamId: streamID,
		Status:   "offline", // Default
	}

	// Simple query against stream_state_current - source of truth for stream state
	var streamStatus string
	var startedAt, updatedAt *time.Time
	var currentViewers uint32
	var bufferState, qualityTier, primaryCodec, issuesDescription, nodeID *string
	var primaryWidth, primaryHeight, primaryBitrate *int32
	var primaryFps *float32
	var hasIssues *bool
	var trackCount, totalInputs *int32
	var uploadedBytes, downloadedBytes, viewerSeconds *uint64
	var packetsSent, packetsLost, packetsRetransmitted *uint64

	err := s.clickhouse.QueryRowContext(ctx, `
		SELECT status, current_viewers, started_at, updated_at,
			buffer_state, quality_tier, primary_width, primary_height,
			primary_fps, primary_codec, primary_bitrate, has_issues, issues_description,
			node_id, track_count, total_inputs, uploaded_bytes, downloaded_bytes, viewer_seconds,
			packets_sent, packets_lost, packets_retransmitted
		FROM stream_state_current FINAL
		WHERE tenant_id = ? AND stream_id = ?
	`, tenantID, streamID).Scan(&streamStatus, &currentViewers, &startedAt, &updatedAt,
		&bufferState, &qualityTier, &primaryWidth, &primaryHeight,
		&primaryFps, &primaryCodec, &primaryBitrate, &hasIssues, &issuesDescription,
		&nodeID, &trackCount, &totalInputs, &uploadedBytes, &downloadedBytes, &viewerSeconds,
		&packetsSent, &packetsLost, &packetsRetransmitted)

	if err == nil {
		resp.Status = streamStatus
		resp.CurrentViewers = int64(currentViewers)

		if startedAt != nil && !startedAt.IsZero() {
			resp.StartedAt = timestamppb.New(*startedAt)
			if streamStatus == "live" {
				resp.DurationSeconds = int64(time.Since(*startedAt).Seconds())
			}
		}
		if updatedAt != nil && !updatedAt.IsZero() {
			resp.LastEventAt = timestamppb.New(*updatedAt)
			resp.UpdatedAt = timestamppb.New(*updatedAt)
		}

		// Quality metrics
		resp.BufferState = bufferState
		resp.QualityTier = qualityTier
		resp.PrimaryWidth = primaryWidth
		resp.PrimaryHeight = primaryHeight
		resp.PrimaryFps = primaryFps
		resp.PrimaryCodec = primaryCodec
		resp.PrimaryBitrate = primaryBitrate
		resp.HasIssues = hasIssues
		resp.IssuesDescription = issuesDescription
		resp.NodeId = nodeID
		resp.TrackCount = trackCount
		resp.TotalInputs = totalInputs
		resp.UploadedBytes = uploadedBytes
		resp.DownloadedBytes = downloadedBytes
		resp.ViewerSeconds = viewerSeconds
		resp.PacketsSent = packetsSent
		resp.PacketsLost = packetsLost
		resp.PacketsRetransmitted = packetsRetransmitted
	}

	return resp, nil
}

// GetStreamsStatus returns operational state for multiple streams (batch lookup)
// Queries stream_state_current directly - no JOINs needed
func (s *PeriscopeServer) GetStreamsStatus(ctx context.Context, req *pb.GetStreamsStatusRequest) (*pb.StreamsStatusResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	streamIDs := req.GetStreamIds()
	if len(streamIDs) == 0 {
		return &pb.StreamsStatusResponse{Statuses: make(map[string]*pb.StreamStatusResponse)}, nil
	}

	// Initialize response with defaults
	statuses := make(map[string]*pb.StreamStatusResponse, len(streamIDs))
	for _, id := range streamIDs {
		statuses[id] = &pb.StreamStatusResponse{
			StreamId: id,
			Status:   "offline",
		}
	}

	// Build IN clause for ClickHouse batch query
	placeholders := make([]string, len(streamIDs))
	args := make([]interface{}, len(streamIDs)+1)
	args[0] = tenantID
	for i, id := range streamIDs {
		placeholders[i] = "?"
		args[i+1] = id
	}

	// Simple batch query against stream_state_current
	query := fmt.Sprintf(`
		SELECT stream_id, status, current_viewers, started_at, updated_at,
			buffer_state, quality_tier, primary_width, primary_height,
			primary_fps, primary_codec, primary_bitrate, has_issues, issues_description
		FROM stream_state_current FINAL
		WHERE tenant_id = ? AND stream_id IN (%s)
	`, joinStrings(placeholders, ", "))

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		s.logger.WithError(err).Error("Failed to batch query stream status from ClickHouse")
		return &pb.StreamsStatusResponse{Statuses: statuses}, nil
	}
	defer rows.Close()

	for rows.Next() {
		var streamID, streamStatus string
		var startedAt, updatedAt *time.Time
		var currentViewers uint32
		var bufferState, qualityTier, primaryCodec, issuesDescription *string
		var primaryWidth, primaryHeight, primaryBitrate *int32
		var primaryFps *float32
		var hasIssues *bool

		err := rows.Scan(&streamID, &streamStatus, &currentViewers, &startedAt, &updatedAt,
			&bufferState, &qualityTier, &primaryWidth, &primaryHeight,
			&primaryFps, &primaryCodec, &primaryBitrate, &hasIssues, &issuesDescription)
		if err != nil {
			continue
		}

		resp := &pb.StreamStatusResponse{
			StreamId:       streamID,
			Status:         streamStatus,
			CurrentViewers: int64(currentViewers),
		}

		if startedAt != nil && !startedAt.IsZero() {
			resp.StartedAt = timestamppb.New(*startedAt)
			if streamStatus == "live" {
				resp.DurationSeconds = int64(time.Since(*startedAt).Seconds())
			}
		}
		if updatedAt != nil && !updatedAt.IsZero() {
			resp.LastEventAt = timestamppb.New(*updatedAt)
		}

		// Quality metrics
		resp.BufferState = bufferState
		resp.QualityTier = qualityTier
		resp.PrimaryWidth = primaryWidth
		resp.PrimaryHeight = primaryHeight
		resp.PrimaryFps = primaryFps
		resp.PrimaryCodec = primaryCodec
		resp.PrimaryBitrate = primaryBitrate
		resp.HasIssues = hasIssues
		resp.IssuesDescription = issuesDescription

		statuses[streamID] = resp
	}

	return &pb.StreamsStatusResponse{Statuses: statuses}, nil
}

// joinStrings joins strings with a separator (helper for SQL IN clause)
func joinStrings(strs []string, sep string) string {
	if len(strs) == 0 {
		return ""
	}
	result := strs[0]
	for i := 1; i < len(strs); i++ {
		result += sep + strs[i]
	}
	return result
}

func nullInt64Value(v sql.NullInt64) int64 {
	if v.Valid {
		return v.Int64
	}
	return 0
}

// ============================================================================
// ViewerAnalyticsService Implementation
// ============================================================================

func (s *PeriscopeServer) GetViewerMetrics(ctx context.Context, req *pb.GetViewerMetricsRequest) (*pb.GetViewerMetricsResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	params, err := getCursorPagination(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	// Start count query in parallel
	streamID := req.GetStreamId()
	countQuery := `SELECT count(*) FROM periscope.viewer_sessions_current FINAL WHERE tenant_id = ? AND ((connected_at >= ? AND connected_at <= ?) OR (connected_at IS NULL AND disconnected_at >= ? AND disconnected_at <= ?))`
	countArgs := []interface{}{tenantID, startTime, endTime, startTime, endTime}
	if streamID != "" {
		countQuery += " AND stream_id = ?"
		countArgs = append(countArgs, streamID)
	}
	countCh := s.countAsync(ctx, countQuery, countArgs...)

	// Query viewer_sessions_current with correct column names
	query := `
		SELECT session_id, ifNull(connected_at, disconnected_at) AS session_start, connected_at, disconnected_at, stream_id, node_id,
			connector, country_code, city,
			latitude, longitude, session_duration, bytes_transferred
		FROM periscope.viewer_sessions_current FINAL
		WHERE tenant_id = ? AND ((connected_at >= ? AND connected_at <= ?) OR (connected_at IS NULL AND disconnected_at >= ? AND disconnected_at <= ?))
	`
	args := []interface{}{tenantID, startTime, endTime, startTime, endTime}

	if streamID != "" {
		query += " AND stream_id = ?"
		args = append(args, streamID)
	}

	// NOTE: session_start is a SELECT alias; ClickHouse doesn't allow SELECT aliases in WHERE.
	// Inline the expression for keyset pagination.
	sessionStartExpr := "ifNull(connected_at, disconnected_at)"
	keysetCond, keysetArgs := buildKeysetCondition(params, sessionStartExpr, "session_id")
	if keysetCond != "" {
		query += keysetCond
		args = append(args, keysetArgs...)
	}

	query += buildOrderBy(params, sessionStartExpr, "session_id")
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var sessions []*pb.ViewerSession
	for rows.Next() {
		var session pb.ViewerSession
		var sessionStart time.Time
		var connectedAt *time.Time
		var disconnectedAt *time.Time
		var sessionDuration uint32
		var bytesTransferred uint64

		err := rows.Scan(
			&session.SessionId, &sessionStart, &connectedAt, &disconnectedAt, &session.StreamId, &session.NodeId,
			&session.Connector, &session.CountryCode, &session.City,
			&session.Latitude, &session.Longitude, &sessionDuration, &bytesTransferred,
		)
		if err != nil {
			s.logger.WithError(err).Info("Failed to scan viewer session row")
			continue
		}

		session.TenantId = tenantID
		session.Timestamp = timestamppb.New(sessionStart)
		if connectedAt != nil && !connectedAt.IsZero() {
			session.ConnectedAt = timestamppb.New(*connectedAt)
		}
		if disconnectedAt != nil && !disconnectedAt.IsZero() {
			session.DisconnectedAt = timestamppb.New(*disconnectedAt)
		}
		session.DurationSeconds = int64(sessionDuration)
		session.BytesDown = int64(bytesTransferred)
		session.BytesUp = 0

		sessions = append(sessions, &session)
	}

	resultsLen := len(sessions)
	if resultsLen > params.Limit {
		sessions = sessions[:params.Limit]
	}

	if params.Direction == pagination.Backward {
		slices.Reverse(sessions)
	}

	// Wait for parallel count query
	total := <-countCh

	var startCursor, endCursor string
	if len(sessions) > 0 {
		startCursor = pagination.EncodeCursor(sessions[0].Timestamp.AsTime(), sessions[0].SessionId)
		endCursor = pagination.EncodeCursor(sessions[len(sessions)-1].Timestamp.AsTime(), sessions[len(sessions)-1].SessionId)
	}

	return &pb.GetViewerMetricsResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Sessions:   sessions,
	}, nil
}

func (s *PeriscopeServer) GetViewerCountTimeSeries(ctx context.Context, req *pb.GetViewerCountTimeSeriesRequest) (*pb.GetViewerCountTimeSeriesResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	// Determine interval - default to 5 minutes
	interval := req.GetInterval()
	if interval == "" {
		interval = "5m"
	}

	// Map interval string to ClickHouse interval
	var clickhouseInterval string
	switch interval {
	case "5m":
		clickhouseInterval = "5 MINUTE"
	case "15m":
		clickhouseInterval = "15 MINUTE"
	case "1h":
		clickhouseInterval = "1 HOUR"
	case "1d":
		clickhouseInterval = "1 DAY"
	default:
		clickhouseInterval = "5 MINUTE"
	}

	// Query stream_viewer_5m rollups instead of raw event logs.
	query := fmt.Sprintf(`
		SELECT
			toStartOfInterval(timestamp_5m, INTERVAL %s) as bucket,
			stream_id,
			max(max_viewers) as viewer_count
		FROM periscope.stream_viewer_5m
		WHERE tenant_id = ? AND timestamp_5m >= ? AND timestamp_5m <= ?
	`, clickhouseInterval)
	args := []interface{}{tenantID, startTime, endTime}

	if streamID := req.GetStreamId(); streamID != "" {
		query += " AND stream_id = ?"
		args = append(args, streamID)
	}

	query += " GROUP BY bucket, stream_id ORDER BY bucket ASC"

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var buckets []*pb.ViewerCountBucket
	for rows.Next() {
		var bucket time.Time
		var streamID string
		var viewerCount int32

		if err := rows.Scan(&bucket, &streamID, &viewerCount); err != nil {
			s.logger.WithError(err).Info("Failed to scan viewer count bucket row")
			continue
		}

		buckets = append(buckets, &pb.ViewerCountBucket{
			Timestamp:   timestamppb.New(bucket),
			StreamId:    streamID,
			ViewerCount: viewerCount,
		})
	}

	return &pb.GetViewerCountTimeSeriesResponse{
		Buckets: buckets,
	}, nil
}

func (s *PeriscopeServer) GetGeographicDistribution(ctx context.Context, req *pb.GetGeographicDistributionRequest) (*pb.GetGeographicDistributionResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	topN := req.GetTopN()
	if topN <= 0 || topN > 100 {
		topN = 10
	}

	// Build base WHERE clause - use viewer_hours_hourly MV (no raw events)
	whereClause := "WHERE tenant_id = ? AND hour >= ? AND hour <= ?"
	args := []interface{}{tenantID, startTime, endTime}

	if streamID := req.GetStreamId(); streamID != "" {
		whereClause += " AND stream_id = ?"
		args = append(args, streamID)
	}

	// Query for country aggregates - distinct viewers per country from MV
	countryQuery := fmt.Sprintf(`
		SELECT country_code, uniqMerge(unique_viewers) as cnt
		FROM periscope.viewer_hours_hourly
		%s AND country_code != ''
		GROUP BY country_code
		ORDER BY cnt DESC
		LIMIT %d
	`, whereClause, topN)

	countryRows, err := s.clickhouse.QueryContext(ctx, countryQuery, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error (countries): %v", err)
	}
	defer countryRows.Close()

	var topCountries []*pb.CountryMetric
	var totalViewers int32
	for countryRows.Next() {
		var countryCode string
		var count int64
		if err := countryRows.Scan(&countryCode, &count); err != nil {
			continue
		}
		totalViewers += int32(count)
		topCountries = append(topCountries, &pb.CountryMetric{
			CountryCode: countryCode,
			ViewerCount: int32(count),
		})
	}

	// Calculate percentages for countries
	for _, c := range topCountries {
		if totalViewers > 0 {
			c.Percentage = float32(c.ViewerCount) / float32(totalViewers) * 100
		}
	}

	// Query for city aggregates from viewer_city_hourly MV
	cityQuery := fmt.Sprintf(`
		SELECT city, country_code, uniqMerge(unique_viewers) as cnt,
		       anyMerge(latitude) as lat, anyMerge(longitude) as lon
		FROM periscope.viewer_city_hourly
		%s AND city != ''
		GROUP BY city, country_code
		ORDER BY cnt DESC
		LIMIT %d
	`, whereClause, topN)

	cityRows, err := s.clickhouse.QueryContext(ctx, cityQuery, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error (cities): %v", err)
	}
	defer cityRows.Close()

	var topCities []*pb.CityMetric
	for cityRows.Next() {
		var city, countryCode string
		var count int64
		var lat, lon float64
		if err := cityRows.Scan(&city, &countryCode, &count, &lat, &lon); err != nil {
			continue
		}
		percentage := float32(0)
		if totalViewers > 0 {
			percentage = float32(count) / float32(totalViewers) * 100
		}
		topCities = append(topCities, &pb.CityMetric{
			City:        city,
			CountryCode: countryCode,
			ViewerCount: int32(count),
			Percentage:  percentage,
			Latitude:    lat,
			Longitude:   lon,
		})
	}

	// Unique cities from MV (city + country)
	var uniqueCities int32
	uniqueCityQuery := fmt.Sprintf(`
		SELECT uniqExact(city, country_code)
		FROM periscope.viewer_city_hourly
		%s AND city != ''
	`, whereClause)
	if err := s.clickhouse.QueryRowContext(ctx, uniqueCityQuery, args...).Scan(&uniqueCities); err != nil {
		s.logger.WithError(err).Warn("Failed to get unique city counts")
	}

	// Query for unique countries and total viewers from MV
	uniqueQuery := fmt.Sprintf(`
		SELECT uniqExact(country_code), uniqMerge(unique_viewers)
		FROM periscope.viewer_hours_hourly
		%s AND country_code != ''
	`, whereClause)

	var uniqueCountries sql.NullInt64
	var totalViewersAll sql.NullInt64
	if err := s.clickhouse.QueryRowContext(ctx, uniqueQuery, args...).Scan(&uniqueCountries, &totalViewersAll); err != nil {
		s.logger.WithError(err).Warn("Failed to get unique geographic counts")
	}
	if totalViewersAll.Valid {
		totalViewers = int32(totalViewersAll.Int64)
	}

	uniqueCountriesVal := int32(0)
	if uniqueCountries.Valid {
		uniqueCountriesVal = int32(uniqueCountries.Int64)
	}

	return &pb.GetGeographicDistributionResponse{
		TopCountries:    topCountries,
		TopCities:       topCities,
		UniqueCountries: uniqueCountriesVal,
		UniqueCities:    uniqueCities,
		TotalViewers:    totalViewers,
	}, nil
}

// ============================================================================
// TrackAnalyticsService Implementation
// ============================================================================

func (s *PeriscopeServer) GetTrackListEvents(ctx context.Context, req *pb.GetTrackListEventsRequest) (*pb.GetTrackListEventsResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	streamID := req.GetStreamId()
	if streamID == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_id required")
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	params, err := getCursorPagination(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	// Start count query in parallel
	countCh := s.countAsync(ctx, `
		SELECT count(*) FROM track_list_events
		WHERE tenant_id = ? AND stream_id = ? AND timestamp >= ? AND timestamp <= ?
	`, tenantID, streamID, startTime, endTime)

	query := `
		SELECT event_id, timestamp, node_id, track_list, track_count, stream_id
		FROM track_list_events
		WHERE tenant_id = ? AND stream_id = ? AND timestamp >= ? AND timestamp <= ?
	`
	args := []interface{}{tenantID, streamID, startTime, endTime}

	keysetCond, keysetArgs := buildKeysetCondition(params, "timestamp", "event_id")
	if keysetCond != "" {
		query += keysetCond
		args = append(args, keysetArgs...)
	}

	query += buildOrderBy(params, "timestamp", "event_id")
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var events []*pb.TrackListEvent
	for rows.Next() {
		var event pb.TrackListEvent
		var ts time.Time
		var trackListJSON string
		var eventID uuid.UUID
		var streamID string

		err := rows.Scan(&eventID, &ts, &event.NodeId, &trackListJSON, &event.TrackCount, &streamID)
		if err != nil {
			continue
		}

		event.Id = eventID.String()
		event.Timestamp = timestamppb.New(ts)
		event.TrackList = trackListJSON
		event.StreamId = streamID

		var tracks []map[string]interface{}
		if err := json.Unmarshal([]byte(trackListJSON), &tracks); err == nil {
			for _, t := range tracks {
				track := &pb.StreamTrack{}
				if name, ok := t["name"].(string); ok {
					track.TrackName = name
				}
				if typ, ok := t["type"].(string); ok {
					track.TrackType = typ
				}
				if codec, ok := t["codec"].(string); ok {
					track.Codec = codec
				}
				event.Tracks = append(event.Tracks, track)
			}
		}

		events = append(events, &event)
	}

	resultsLen := len(events)
	if resultsLen > params.Limit {
		events = events[:params.Limit]
	}

	if params.Direction == pagination.Backward {
		slices.Reverse(events)
	}

	collisionKeys := make([]cursorCollisionKey, 0, len(events))
	for _, event := range events {
		if event.Timestamp == nil {
			continue
		}
		collisionKeys = append(collisionKeys, cursorCollisionKey{
			Timestamp: event.Timestamp.AsTime(),
			ID:        event.Id,
		})
	}
	s.logCursorCollisions("track_list_events", collisionKeys)

	// Wait for parallel count query
	total := <-countCh

	var startCursor, endCursor string
	if len(events) > 0 {
		startCursor = pagination.EncodeCursor(events[0].Timestamp.AsTime(), events[0].Id)
		endCursor = pagination.EncodeCursor(events[len(events)-1].Timestamp.AsTime(), events[len(events)-1].Id)
	}

	return &pb.GetTrackListEventsResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Events:     events,
	}, nil
}

// ============================================================================
// ConnectionAnalyticsService Implementation
// ============================================================================

func (s *PeriscopeServer) GetConnectionEvents(ctx context.Context, req *pb.GetConnectionEventsRequest) (*pb.GetConnectionEventsResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	params, err := getCursorPagination(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	// Start count query in parallel
	streamID := req.GetStreamId()
	countQuery := `SELECT count(*) FROM periscope.viewer_connection_events WHERE tenant_id = ? AND timestamp >= ? AND timestamp <= ?`
	countArgs := []interface{}{tenantID, startTime, endTime}
	if streamID != "" {
		countQuery += " AND stream_id = ?"
		countArgs = append(countArgs, streamID)
	}
	countCh := s.countAsync(ctx, countQuery, countArgs...)

	query := `
		SELECT event_id, timestamp, tenant_id, stream_id, session_id,
		       connection_addr, connector, node_id, country_code, city,
		       latitude, longitude,
		       client_bucket_h3, client_bucket_res, node_bucket_h3, node_bucket_res,
		       event_type, session_duration, bytes_transferred, request_url
		FROM periscope.viewer_connection_events
		WHERE tenant_id = ? AND timestamp >= ? AND timestamp <= ?
	`
	args := []interface{}{tenantID, startTime, endTime}

	if streamID != "" {
		query += " AND stream_id = ?"
		args = append(args, streamID)
	}

	keysetCond, keysetArgs := buildKeysetCondition(params, "timestamp", "event_id")
	if keysetCond != "" {
		query += keysetCond
		args = append(args, keysetArgs...)
	}

	query += buildOrderBy(params, "timestamp", "event_id")
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var events []*pb.ConnectionEvent
	for rows.Next() {
		var event pb.ConnectionEvent
		var ts time.Time

		var clientBucketH3, nodeBucketH3 *uint64
		var clientBucketRes, nodeBucketRes *uint8

		var requestURL *string
		err := rows.Scan(
			&event.EventId, &ts, &event.TenantId, &event.StreamId, &event.SessionId,
			&event.ConnectionAddr, &event.Connector, &event.NodeId, &event.CountryCode, &event.City,
			&event.Latitude, &event.Longitude,
			&clientBucketH3, &clientBucketRes, &nodeBucketH3, &nodeBucketRes,
			&event.EventType, &event.SessionDurationSeconds, &event.BytesTransferred, &requestURL,
		)
		if err != nil {
			continue
		}

		if clientBucketH3 != nil {
			event.ClientBucket = &pb.GeoBucket{
				H3Index: *clientBucketH3,
				Resolution: func() uint32 {
					if clientBucketRes != nil {
						return uint32(*clientBucketRes)
					}
					return 0
				}(),
			}
		}
		if nodeBucketH3 != nil {
			event.NodeBucket = &pb.GeoBucket{
				H3Index: *nodeBucketH3,
				Resolution: func() uint32 {
					if nodeBucketRes != nil {
						return uint32(*nodeBucketRes)
					}
					return 0
				}(),
			}
		}

		event.Timestamp = timestamppb.New(ts)
		event.RequestUrl = requestURL
		events = append(events, &event)
	}

	resultsLen := len(events)
	if resultsLen > params.Limit {
		events = events[:params.Limit]
	}

	if params.Direction == pagination.Backward {
		slices.Reverse(events)
	}

	collisionKeys := make([]cursorCollisionKey, 0, len(events))
	for _, event := range events {
		if event.Timestamp == nil {
			continue
		}
		collisionKeys = append(collisionKeys, cursorCollisionKey{
			Timestamp: event.Timestamp.AsTime(),
			ID:        event.EventId,
		})
	}
	s.logCursorCollisions("connection_events", collisionKeys)

	// Wait for parallel count query
	total := <-countCh

	var startCursor, endCursor string
	if len(events) > 0 {
		startCursor = pagination.EncodeCursor(events[0].Timestamp.AsTime(), events[0].EventId)
		endCursor = pagination.EncodeCursor(events[len(events)-1].Timestamp.AsTime(), events[len(events)-1].EventId)
	}

	return &pb.GetConnectionEventsResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Events:     events,
	}, nil
}

// ============================================================================
// NodeAnalyticsService Implementation
// ============================================================================

func (s *PeriscopeServer) GetNodeMetrics(ctx context.Context, req *pb.GetNodeMetricsRequest) (*pb.GetNodeMetricsResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	params, err := getCursorPagination(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	// Start count query in parallel
	nodeID := req.GetNodeId()
	countQuery := `SELECT count(*) FROM node_metrics_samples WHERE tenant_id = ? AND timestamp >= ? AND timestamp <= ?`
	countArgs := []interface{}{tenantID, startTime, endTime}
	if nodeID != "" {
		countQuery += " AND node_id = ?"
		countArgs = append(countArgs, nodeID)
	}
	countCh := s.countAsync(ctx, countQuery, countArgs...)

	query := `
		SELECT timestamp, cluster_id, node_id, cpu_usage, ram_max, ram_current,
		       shm_total_bytes, shm_used_bytes, disk_total_bytes, disk_used_bytes,
		       bandwidth_in, bandwidth_out, up_speed, down_speed,
		       connections_current, stream_count, is_healthy,
		       latitude, longitude
		FROM node_metrics_samples
		WHERE tenant_id = ? AND timestamp >= ? AND timestamp <= ?
	`
	args := []interface{}{tenantID, startTime, endTime}

	if nodeID != "" {
		query += " AND node_id = ?"
		args = append(args, nodeID)
	}

	keysetCond, keysetArgs := buildKeysetCondition(params, "timestamp", "concat(cluster_id, ':', node_id)")
	if keysetCond != "" {
		query += keysetCond
		args = append(args, keysetArgs...)
	}

	query += buildOrderBy(params, "timestamp", "concat(cluster_id, ':', node_id)")
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var metrics []*pb.NodeMetric
	for rows.Next() {
		var m pb.NodeMetric
		var ts time.Time
		var clusterID string

		err := rows.Scan(
			&ts, &clusterID, &m.NodeId, &m.CpuUsage, &m.RamMax, &m.RamCurrent,
			&m.ShmTotalBytes, &m.ShmUsedBytes, &m.DiskTotalBytes, &m.DiskUsedBytes,
			&m.BandwidthIn, &m.BandwidthOut, &m.UpSpeed, &m.DownSpeed,
			&m.ConnectionsCurrent, &m.StreamCount, &m.IsHealthy,
			&m.Latitude, &m.Longitude,
		)
		if err != nil {
			continue
		}

		// Generate composite ID for pagination
		m.Id = fmt.Sprintf("%s:%s", clusterID, m.NodeId)
		m.ClusterId = clusterID
		m.Timestamp = timestamppb.New(ts)
		metrics = append(metrics, &m)
	}

	resultsLen := len(metrics)
	if resultsLen > params.Limit {
		metrics = metrics[:params.Limit]
	}

	if params.Direction == pagination.Backward {
		slices.Reverse(metrics)
	}

	// Wait for parallel count query
	total := <-countCh

	var startCursor, endCursor string
	if len(metrics) > 0 {
		startCursor = pagination.EncodeCursor(metrics[0].Timestamp.AsTime(), metrics[0].Id)
		endCursor = pagination.EncodeCursor(metrics[len(metrics)-1].Timestamp.AsTime(), metrics[len(metrics)-1].Id)
	}

	return &pb.GetNodeMetricsResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Metrics:    metrics,
	}, nil
}

func (s *PeriscopeServer) GetNodeMetrics1H(ctx context.Context, req *pb.GetNodeMetrics1HRequest) (*pb.GetNodeMetrics1HResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	params, err := getCursorPagination(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	// Start count query in parallel
	nodeID := req.GetNodeId()
	countQuery := `SELECT count(*) FROM periscope.node_metrics_1h WHERE tenant_id = ? AND timestamp_1h >= ? AND timestamp_1h <= ?`
	countArgs := []interface{}{tenantID, startTime, endTime}
	if nodeID != "" {
		countQuery += " AND node_id = ?"
		countArgs = append(countArgs, nodeID)
	}
	countCh := s.countAsync(ctx, countQuery, countArgs...)

	// Query uses actual schema columns (no id column)
	query := `
		SELECT timestamp_1h, cluster_id, node_id, avg_cpu, peak_cpu, avg_memory, peak_memory,
		       avg_disk, peak_disk, avg_shm, peak_shm, total_bandwidth_in, total_bandwidth_out, was_healthy
		FROM periscope.node_metrics_1h
		WHERE tenant_id = ? AND timestamp_1h >= ? AND timestamp_1h <= ?
	`
	args := []interface{}{tenantID, startTime, endTime}

	if nodeID != "" {
		query += " AND node_id = ?"
		args = append(args, nodeID)
	}

	keysetCond, keysetArgs := buildKeysetCondition(params, "timestamp_1h", "concat(cluster_id, ':', node_id)")
	if keysetCond != "" {
		query += keysetCond
		args = append(args, keysetArgs...)
	}

	query += buildOrderBy(params, "timestamp_1h", "concat(cluster_id, ':', node_id)")
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var metrics []*pb.NodeMetricHourly
	for rows.Next() {
		var m pb.NodeMetricHourly
		var ts time.Time
		var clusterID string
		var wasHealthy uint8

		err := rows.Scan(
			&ts, &clusterID, &m.NodeId, &m.AvgCpu, &m.PeakCpu, &m.AvgMemory, &m.PeakMemory,
			&m.AvgDisk, &m.PeakDisk, &m.AvgShm, &m.PeakShm, &m.TotalBandwidthIn, &m.TotalBandwidthOut, &wasHealthy,
		)
		if err != nil {
			s.logger.WithError(err).Warn("Failed to scan node_metrics_1h row")
			continue
		}

		m.Timestamp = timestamppb.New(ts)
		m.WasHealthy = wasHealthy == 1
		m.Id = fmt.Sprintf("%s:%s", clusterID, m.NodeId)
		m.ClusterId = clusterID
		metrics = append(metrics, &m)
	}

	resultsLen := len(metrics)
	if resultsLen > params.Limit {
		metrics = metrics[:params.Limit]
	}

	if params.Direction == pagination.Backward {
		slices.Reverse(metrics)
	}

	// Wait for parallel count query
	total := <-countCh

	var startCursor, endCursor string
	if len(metrics) > 0 {
		startCursor = pagination.EncodeCursor(metrics[0].Timestamp.AsTime(), metrics[0].Id)
		endCursor = pagination.EncodeCursor(metrics[len(metrics)-1].Timestamp.AsTime(), metrics[len(metrics)-1].Id)
	}

	return &pb.GetNodeMetrics1HResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Metrics:    metrics,
	}, nil
}

// GetNodeMetricsAggregated returns per-node aggregates for the requested time range.
func (s *PeriscopeServer) GetNodeMetricsAggregated(ctx context.Context, req *pb.GetNodeMetricsAggregatedRequest) (*pb.GetNodeMetricsAggregatedResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	query := `
		SELECT cluster_id, node_id,
		       avg(avg_cpu), avg(avg_memory), avg(avg_disk), avg(avg_shm),
		       sum(total_bandwidth_in), sum(total_bandwidth_out),
		       count()
		FROM periscope.node_metrics_1h
		WHERE tenant_id = ? AND timestamp_1h >= ? AND timestamp_1h <= ?
	`
	args := []interface{}{tenantID, startTime, endTime}
	if req.GetNodeId() != "" {
		query += " AND node_id = ?"
		args = append(args, req.GetNodeId())
	}
	query += " GROUP BY cluster_id, node_id ORDER BY cluster_id, node_id"

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var metrics []*pb.NodeMetricsAggregated
	for rows.Next() {
		var m pb.NodeMetricsAggregated
		var sampleCount uint64
		if err := rows.Scan(
			&m.ClusterId,
			&m.NodeId,
			&m.AvgCpu,
			&m.AvgMemory,
			&m.AvgDisk,
			&m.AvgShm,
			&m.TotalBandwidthIn,
			&m.TotalBandwidthOut,
			&sampleCount,
		); err != nil {
			s.logger.WithError(err).Warn("Failed to scan node_metrics_1h aggregate row")
			continue
		}
		m.SampleCount = uint32(sampleCount)
		metrics = append(metrics, &m)
	}

	return &pb.GetNodeMetricsAggregatedResponse{Metrics: metrics}, nil
}

// GetLiveNodes returns current state of nodes from node_state_current (ReplacingMergeTree)
// This is the source of truth for real-time node status - simple SELECT, no time-series
// Supports multi-tenant access for subscribed clusters via related_tenant_ids
func (s *PeriscopeServer) GetLiveNodes(ctx context.Context, req *pb.GetLiveNodesRequest) (*pb.GetLiveNodesResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	// Support querying across multiple tenants (e.g. self + subscribed clusters)
	relatedIDs := req.GetRelatedTenantIds()
	allIDs := append([]string{tenantID}, relatedIDs...)

	placeholders := make([]string, len(allIDs))
	for i := range allIDs {
		placeholders[i] = "?"
	}
	inClause := fmt.Sprintf("tenant_id IN (%s)", strings.Join(placeholders, ", "))

	// Simple query against node_state_current - no JOINs, no aggregations
	query := fmt.Sprintf(`
		SELECT tenant_id, node_id, cpu_percent, ram_used_bytes, ram_total_bytes,
		       disk_used_bytes, disk_total_bytes, up_speed, down_speed,
		       active_streams, is_healthy, latitude, longitude, location,
		       metadata, updated_at
		FROM periscope.node_state_current FINAL
		WHERE %s
	`, inClause)
	args := []interface{}{}
	for _, id := range allIDs {
		args = append(args, id)
	}

	if nodeID := req.GetNodeId(); nodeID != "" {
		query += " AND node_id = ?"
		args = append(args, nodeID)
	}

	query += " ORDER BY node_id"

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		s.logger.WithError(err).Error("Failed to query node_state_current")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var nodes []*pb.LiveNode
	for rows.Next() {
		var n pb.LiveNode
		var updatedAt time.Time
		var isHealthy uint8
		var metadataJSON string

		err := rows.Scan(
			&n.TenantId, &n.NodeId, &n.CpuPercent, &n.RamUsedBytes, &n.RamTotalBytes,
			&n.DiskUsedBytes, &n.DiskTotalBytes, &n.UpSpeed, &n.DownSpeed,
			&n.ActiveStreams, &isHealthy, &n.Latitude, &n.Longitude, &n.Location,
			&metadataJSON, &updatedAt,
		)
		if err != nil {
			s.logger.WithError(err).Warn("Failed to scan node_state_current row")
			continue
		}

		n.IsHealthy = isHealthy == 1
		n.UpdatedAt = timestamppb.New(updatedAt)

		// Parse metadata JSON into protobuf Struct
		if metadataJSON != "" && metadataJSON != "{}" {
			var metaStruct structpb.Struct
			if jsonErr := json.Unmarshal([]byte(metadataJSON), &metaStruct); jsonErr == nil {
				n.Metadata = &metaStruct
			}
		}

		nodes = append(nodes, &n)
	}

	return &pb.GetLiveNodesResponse{
		Nodes: nodes,
		Count: int32(len(nodes)),
	}, nil
}

// ============================================================================
// RoutingAnalyticsService Implementation
// ============================================================================

func (s *PeriscopeServer) GetRoutingEvents(ctx context.Context, req *pb.GetRoutingEventsRequest) (*pb.GetRoutingEventsResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	params, err := getCursorPagination(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	// Support querying across multiple tenants (e.g. self + subscribed providers)
	relatedIDs := req.GetRelatedTenantIds()
	allIDs := append([]string{tenantID}, relatedIDs...)

	placeholders := make([]string, len(allIDs))
	for i := range allIDs {
		placeholders[i] = "?"
	}
	inClause := fmt.Sprintf("tenant_id IN (%s)", strings.Join(placeholders, ", "))

	// Count total for pagination
	countQuery := fmt.Sprintf(`SELECT count(*) FROM periscope.routing_decisions WHERE %s AND timestamp >= ? AND timestamp <= ?`, inClause)
	countArgs := []interface{}{}
	for _, id := range allIDs {
		countArgs = append(countArgs, id)
	}
	countArgs = append(countArgs, startTime, endTime)

	if streamID := req.GetStreamId(); streamID != "" {
		countQuery += " AND stream_id = ?"
		countArgs = append(countArgs, streamID)
	}
	// Dual-tenant filtering (RFC: routing-events-dual-tenant-attribution)
	if streamTenantID := req.GetStreamTenantId(); streamTenantID != "" {
		countQuery += " AND stream_tenant_id = ?"
		countArgs = append(countArgs, streamTenantID)
	}
	if clusterID := req.GetClusterId(); clusterID != "" {
		countQuery += " AND cluster_id = ?"
		countArgs = append(countArgs, clusterID)
	}

	var total int32
	if err := s.clickhouse.QueryRowContext(ctx, countQuery, countArgs...).Scan(&total); err != nil {
		s.logger.WithError(err).Warn("Failed to get routing events count")
	}

	// Main query with all geographic columns from ClickHouse schema
	query := fmt.Sprintf(`
		SELECT timestamp, stream_id, selected_node, status, details, score,
		       client_country, client_latitude, client_longitude, client_bucket_h3, client_bucket_res,
		       node_latitude, node_longitude, node_name, node_bucket_h3, node_bucket_res,
		       selected_node_id, routing_distance_km, tenant_id, stream_tenant_id, cluster_id,
		       latency_ms, candidates_count, event_type, source
		FROM periscope.routing_decisions
		WHERE %s AND timestamp >= ? AND timestamp <= ?
	`, inClause)

	args := []interface{}{}
	for _, id := range allIDs {
		args = append(args, id)
	}
	args = append(args, startTime, endTime)

	if streamID := req.GetStreamId(); streamID != "" {
		query += " AND stream_id = ?"
		args = append(args, streamID)
	}

	// Dual-tenant filtering (RFC: routing-events-dual-tenant-attribution)
	if streamTenantID := req.GetStreamTenantId(); streamTenantID != "" {
		query += " AND stream_tenant_id = ?"
		args = append(args, streamTenantID)
	}
	if clusterID := req.GetClusterId(); clusterID != "" {
		query += " AND cluster_id = ?"
		args = append(args, clusterID)
	}

	if params.Cursor != nil {
		// Cursor ID contains "tenant_id|stream_id|selected_node"
		parts := strings.SplitN(params.Cursor.ID, "|", 3)
		if len(parts) == 3 {
			keysetCond, keysetArgs, keysetErr := buildKeysetConditionN(params, "timestamp", []string{"tenant_id", "stream_id", "selected_node"}, parts)
			if keysetErr != nil {
				return nil, keysetErr
			}
			if keysetCond != "" {
				query += keysetCond
				args = append(args, keysetArgs...)
			}
		} else {
			return nil, status.Errorf(codes.InvalidArgument, "invalid cursor")
		}
	}

	query += buildOrderByN(params, "timestamp", []string{"tenant_id", "stream_id", "selected_node"})
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var events []*pb.RoutingEvent
	for rows.Next() {
		var ts time.Time
		var streamID, selectedNode, statusStr, rowTenantID string
		var details *string
		var score *int64
		var clientCountry *string
		var clientLat, clientLon, nodeLat, nodeLon *float64
		var clientBucketH3, nodeBucketH3 *uint64
		var clientBucketRes, nodeBucketRes *uint8
		var nodeName *string
		var selectedNodeID *string
		var routingDistance *float64
		var streamTenantID *string
		var clusterID string
		var latencyMs *float32
		var candidatesCount *int32
		var eventType *string
		var source *string

		err := rows.Scan(&ts, &streamID, &selectedNode, &statusStr, &details, &score,
			&clientCountry, &clientLat, &clientLon, &clientBucketH3, &clientBucketRes,
			&nodeLat, &nodeLon, &nodeName, &nodeBucketH3, &nodeBucketRes,
			&selectedNodeID, &routingDistance, &rowTenantID, &streamTenantID, &clusterID,
			&latencyMs, &candidatesCount, &eventType, &source)
		if err != nil {
			s.logger.WithError(err).Info("Failed to scan routing event row")
			continue
		}

		event := &pb.RoutingEvent{
			Id:           fmt.Sprintf("%d-%s-%s", ts.UnixNano(), streamID, selectedNode),
			Timestamp:    timestamppb.New(ts),
			StreamId:     streamID,
			SelectedNode: selectedNode,
			Status:       statusStr,
			TenantId:     rowTenantID,
		}

		if details != nil {
			event.Details = details
		}
		if score != nil {
			scoreInt32 := int32(*score)
			event.Score = &scoreInt32
		}
		if latencyMs != nil {
			event.LatencyMs = *latencyMs
		}
		if candidatesCount != nil {
			event.CandidatesCount = *candidatesCount
		}
		if eventType != nil {
			event.EventType = eventType
		}
		if source != nil {
			event.Source = source
		}
		if clientCountry != nil {
			event.ClientCountry = clientCountry
		}
		if clientLat != nil {
			event.ClientLatitude = clientLat
		}
		if clientLon != nil {
			event.ClientLongitude = clientLon
		}
		if clientBucketH3 != nil {
			event.ClientBucket = &pb.GeoBucket{
				H3Index: *clientBucketH3,
				Resolution: func() uint32 {
					if clientBucketRes != nil {
						return uint32(*clientBucketRes)
					}
					return 0
				}(),
			}
		}
		if nodeLat != nil {
			event.NodeLatitude = nodeLat
		}
		if nodeLon != nil {
			event.NodeLongitude = nodeLon
		}
		if nodeBucketH3 != nil {
			event.NodeBucket = &pb.GeoBucket{
				H3Index: *nodeBucketH3,
				Resolution: func() uint32 {
					if nodeBucketRes != nil {
						return uint32(*nodeBucketRes)
					}
					return 0
				}(),
			}
		}
		if nodeName != nil {
			event.NodeName = nodeName
		}
		if selectedNodeID != nil && *selectedNodeID != "" {
			event.NodeId = selectedNodeID
		}
		if routingDistance != nil {
			event.RoutingDistance = routingDistance
		}
		// Dual-tenant attribution (RFC: routing-events-dual-tenant-attribution)
		if streamTenantID != nil {
			event.StreamTenantId = streamTenantID
		}
		if clusterID != "" {
			event.ClusterId = &clusterID
		}

		events = append(events, event)
	}

	resultsLen := len(events)
	if resultsLen > params.Limit {
		events = events[:params.Limit]
	}

	if params.Direction == pagination.Backward {
		slices.Reverse(events)
	}

	var startCursor, endCursor string
	if len(events) > 0 {
		first := events[0]
		last := events[len(events)-1]
		startCursor = pagination.EncodeCursor(first.Timestamp.AsTime(), first.TenantId+"|"+first.StreamId+"|"+first.SelectedNode)
		endCursor = pagination.EncodeCursor(last.Timestamp.AsTime(), last.TenantId+"|"+last.StreamId+"|"+last.SelectedNode)
	}

	return &pb.GetRoutingEventsResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Events:     events,
	}, nil
}

// ============================================================================
// PlatformAnalyticsService Implementation
// ============================================================================

func (s *PeriscopeServer) GetPlatformOverview(ctx context.Context, req *pb.GetPlatformOverviewRequest) (*pb.GetPlatformOverviewResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	// Parse time range from request (defaults to last 24h via validateTimeRangeProto)
	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	resp := &pb.GetPlatformOverviewResponse{
		TenantId:    tenantID,
		GeneratedAt: timestamppb.Now(),
		TimeRange:   &pb.TimeRange{Start: timestamppb.New(startTime), End: timestamppb.New(endTime)},
	}

	// Get current stream stats from stream_state_current (real-time snapshot)
	liveQuery := `
		SELECT
			count() as total_streams,
			countIf(status = 'live') as active_streams,
			sum(current_viewers) as total_viewers,
			avg(current_viewers) as avg_viewers,
			sum(uploaded_bytes) as total_upload_bytes,
			sum(downloaded_bytes) as total_download_bytes
		FROM stream_state_current FINAL
		WHERE tenant_id = ?
	`

	err = s.clickhouse.QueryRowContext(ctx, liveQuery, tenantID).Scan(
		&resp.TotalStreams, &resp.ActiveStreams, &resp.TotalViewers, &resp.AverageViewers,
		&resp.TotalUploadBytes, &resp.TotalDownloadBytes,
	)
	if err != nil {
		s.logger.WithError(err).Error("Failed to get platform overview from stream_state_current")
	}

	// Get peak bandwidth from client_qoe_5m (actual bandwidth rate, not cumulative bytes)
	peakBwQuery := `
		SELECT COALESCE(max(avg_bw_out), 0) as peak_bandwidth
		FROM client_qoe_5m
		WHERE tenant_id = ?
		AND timestamp_5m BETWEEN ? AND ?
	`
	err = s.clickhouse.QueryRowContext(ctx, peakBwQuery, tenantID, startTime, endTime).Scan(&resp.PeakBandwidth)
	if err != nil {
		s.logger.WithError(err).Info("Failed to get peak bandwidth from client_qoe_5m")
	}

	// Get historical metrics from tenant_viewer_daily (pre-aggregated from viewer_connection_events)
	historicalQuery := `
		SELECT
			COALESCE(sum(egress_gb), 0) as egress_gb,
			COALESCE(sum(viewer_hours), 0) as viewer_hours,
			COALESCE(sum(unique_viewers), 0) as unique_viewers
		FROM periscope.tenant_viewer_daily
		WHERE tenant_id = ?
		AND day BETWEEN toDate(?) AND toDate(?)
	`

	var egressGb, viewerHours float64
	var uniqueViewers int64
	err = s.clickhouse.QueryRowContext(ctx, historicalQuery, tenantID, startTime, endTime).Scan(
		&egressGb, &viewerHours, &uniqueViewers,
	)
	if err == nil {
		resp.EgressGb = egressGb
		resp.ViewerHours = viewerHours
		resp.DeliveredMinutes = viewerHours * 60 // Convenience: viewer_hours * 60
		resp.UniqueViewers = int32(uniqueViewers)
		resp.PeakViewers = int32(uniqueViewers) // Note: This is unique viewers, not peak concurrent (legacy field)
	} else {
		s.logger.WithError(err).Info("Failed to get historical metrics from tenant_viewer_daily")
	}

	// Get ingest hours (total time streams were live) from stream_event_log
	// Counts distinct stream-hour buckets as a proxy for streaming hours
	ingestQuery := `
		SELECT COALESCE(
			countDistinct(concat(toString(stream_id), toString(toStartOfHour(timestamp)))),
			0
		) as stream_hours
		FROM periscope.stream_event_log
		WHERE tenant_id = ?
		AND timestamp BETWEEN ? AND ?
		AND total_viewers IS NOT NULL
	`

	var streamHours float64
	err = s.clickhouse.QueryRowContext(ctx, ingestQuery, tenantID, startTime, endTime).Scan(&streamHours)
	if err == nil {
		resp.StreamHours = streamHours
		resp.IngestHours = streamHours // Alias for clarity
	} else {
		s.logger.WithError(err).Info("Failed to get stream hours from stream_event_log")
	}

	// Get true peak concurrent viewers (max at any instant) from stream_event_log
	peakConcurrentQuery := `
		SELECT COALESCE(max(total_viewers), 0) as peak_concurrent
		FROM periscope.stream_event_log
		WHERE tenant_id = ?
		AND timestamp BETWEEN ? AND ?
		AND total_viewers IS NOT NULL
	`

	var peakConcurrent int32
	err = s.clickhouse.QueryRowContext(ctx, peakConcurrentQuery, tenantID, startTime, endTime).Scan(&peakConcurrent)
	if err == nil {
		resp.PeakConcurrentViewers = peakConcurrent
	} else {
		s.logger.WithError(err).Info("Failed to get peak concurrent viewers from stream_event_log")
	}

	// Get total views count from tenant_analytics_daily (pre-computed rollup)
	// This table aggregates viewer_connection_events by day for efficient dashboard queries
	totalViewsQuery := `
		SELECT COALESCE(sum(total_views), 0) as total_views
		FROM periscope.tenant_analytics_daily
		WHERE tenant_id = ?
		AND day BETWEEN toDate(?) AND toDate(?)
	`

	var totalViews int64
	err = s.clickhouse.QueryRowContext(ctx, totalViewsQuery, tenantID, startTime, endTime).Scan(&totalViews)
	if err == nil {
		resp.TotalViews = totalViews
	} else {
		s.logger.WithError(err).Info("Failed to get total views from tenant_analytics_daily")
	}

	return resp, nil
}

// ============================================================================
// ClipAnalyticsService Implementation
// ============================================================================

func (s *PeriscopeServer) GetClipEvents(ctx context.Context, req *pb.GetClipEventsRequest) (*pb.GetClipEventsResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	params, err := getCursorPagination(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	// Start count query in parallel
	streamID := req.GetStreamId()
	stage := req.GetStage()
	contentType := req.GetContentType()
	countQuery := `SELECT count(*) FROM periscope.artifact_events WHERE tenant_id = ? AND timestamp >= ? AND timestamp <= ?`
	countArgs := []interface{}{tenantID, startTime, endTime}
	if contentType != "" {
		countQuery += " AND content_type = ?"
		countArgs = append(countArgs, contentType)
	}
	if streamID != "" {
		countQuery += " AND stream_id = ?"
		countArgs = append(countArgs, streamID)
	}
	if stage != "" {
		countQuery += " AND stage = ?"
		countArgs = append(countArgs, stage)
	}
	countCh := s.countAsync(ctx, countQuery, countArgs...)

	query := `
		SELECT request_id, timestamp, stream_id, stage, content_type,
		       start_unix, stop_unix, ingest_node_id, percent, message, file_path, s3_url, size_bytes, expires_at
		FROM periscope.artifact_events
		WHERE tenant_id = ? AND timestamp >= ? AND timestamp <= ?
	`
	args := []interface{}{tenantID, startTime, endTime}

	if contentType != "" {
		query += " AND content_type = ?"
		args = append(args, contentType)
	}

	if streamID != "" {
		query += " AND stream_id = ?"
		args = append(args, streamID)
	}

	if stage != "" {
		query += " AND stage = ?"
		args = append(args, stage)
	}

	keysetCond, keysetArgs := buildKeysetCondition(params, "timestamp", "request_id")
	if keysetCond != "" {
		query += keysetCond
		args = append(args, keysetArgs...)
	}

	query += buildOrderBy(params, "timestamp", "request_id")
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var events []*pb.ClipEvent
	for rows.Next() {
		var event pb.ClipEvent
		var ts time.Time
		var contentType, ingestNodeID, message, filePath, s3URL *string
		var startUnix, stopUnix *int64
		var percent *uint32
		var sizeBytes *uint64
		var expiresAt *int64

		err := rows.Scan(
			&event.RequestId, &ts, &event.StreamId, &event.Stage, &contentType,
			&startUnix, &stopUnix, &ingestNodeID, &percent, &message, &filePath, &s3URL, &sizeBytes, &expiresAt,
		)
		if err != nil {
			continue
		}

		event.Timestamp = timestamppb.New(ts)
		event.ContentType = contentType
		event.StartUnix = startUnix
		event.StopUnix = stopUnix
		event.IngestNodeId = ingestNodeID
		event.Percent = percent
		event.Message = message
		event.FilePath = filePath
		event.S3Url = s3URL
		event.SizeBytes = sizeBytes
		event.ExpiresAt = expiresAt

		events = append(events, &event)
	}

	resultsLen := len(events)
	if resultsLen > params.Limit {
		events = events[:params.Limit]
	}

	if params.Direction == pagination.Backward {
		slices.Reverse(events)
	}

	// Wait for parallel count query
	total := <-countCh

	var startCursor, endCursor string
	if len(events) > 0 {
		startCursor = pagination.EncodeCursor(events[0].Timestamp.AsTime(), events[0].RequestId)
		endCursor = pagination.EncodeCursor(events[len(events)-1].Timestamp.AsTime(), events[len(events)-1].RequestId)
	}

	return &pb.GetClipEventsResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Events:     events,
	}, nil
}

// GetArtifactState returns the current state of a single artifact (clip/DVR)
func (s *PeriscopeServer) GetArtifactState(ctx context.Context, req *pb.GetArtifactStateRequest) (*pb.GetArtifactStateResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	requestID := req.GetRequestId()
	if requestID == "" {
		return nil, status.Error(codes.InvalidArgument, "request_id required")
	}

	query := `
		SELECT tenant_id, request_id, stream_id, content_type, stage,
		       progress_percent, error_message, requested_at, started_at, completed_at,
		       clip_start_unix, clip_stop_unix, segment_count, manifest_path,
		       file_path, s3_url, size_bytes, processing_node_id, updated_at, expires_at
		FROM artifact_state_current FINAL
		WHERE tenant_id = ? AND request_id = ?
	`

	var artifact pb.ArtifactState
	var errorMessage, manifestPath, filePath, s3URL, processingNodeID *string
	var startedAt, completedAt *time.Time
	var clipStartUnix, clipStopUnix *int64
	var segmentCount *uint32
	var sizeBytes *uint64
	var requestedAt, updatedAt time.Time
	var expiresAt *time.Time
	var progressPercent uint8

	err := s.clickhouse.QueryRowContext(ctx, query, tenantID, requestID).Scan(
		&artifact.TenantId, &artifact.RequestId, &artifact.StreamId, &artifact.ContentType, &artifact.Stage,
		&progressPercent, &errorMessage, &requestedAt, &startedAt, &completedAt,
		&clipStartUnix, &clipStopUnix, &segmentCount, &manifestPath,
		&filePath, &s3URL, &sizeBytes, &processingNodeID, &updatedAt, &expiresAt,
	)
	if err != nil {
		s.logger.WithError(err).WithField("request_id", requestID).Info("Artifact not found")
		return nil, status.Error(codes.NotFound, "artifact not found")
	}

	artifact.ProgressPercent = uint32(progressPercent)
	artifact.ErrorMessage = errorMessage
	artifact.RequestedAt = timestamppb.New(requestedAt)
	if startedAt != nil {
		artifact.StartedAt = timestamppb.New(*startedAt)
	}
	if completedAt != nil {
		artifact.CompletedAt = timestamppb.New(*completedAt)
	}
	artifact.ClipStartUnix = clipStartUnix
	artifact.ClipStopUnix = clipStopUnix
	artifact.SegmentCount = segmentCount
	artifact.ManifestPath = manifestPath
	artifact.FilePath = filePath
	artifact.S3Url = s3URL
	artifact.SizeBytes = sizeBytes
	artifact.ProcessingNodeId = processingNodeID
	artifact.UpdatedAt = timestamppb.New(updatedAt)
	if expiresAt != nil {
		artifact.ExpiresAt = timestamppb.New(*expiresAt)
	}

	return &pb.GetArtifactStateResponse{
		Artifact: &artifact,
	}, nil
}

// GetArtifactStates returns a list of artifact states with optional filtering
func (s *PeriscopeServer) GetArtifactStates(ctx context.Context, req *pb.GetArtifactStatesRequest) (*pb.GetArtifactStatesResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	params, err := getCursorPagination(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	// Start count query in parallel
	streamID := req.GetStreamId()
	contentType := req.GetContentType()
	stage := req.GetStage()
	requestIDs := req.GetRequestIds()

	countQuery := `SELECT count(*) FROM artifact_state_current FINAL WHERE tenant_id = ?`
	countArgs := []interface{}{tenantID}
	if streamID != "" {
		countQuery += " AND stream_id = ?"
		countArgs = append(countArgs, streamID)
	}
	if contentType != "" {
		countQuery += " AND content_type = ?"
		countArgs = append(countArgs, contentType)
	}
	if stage != "" {
		countQuery += " AND stage = ?"
		countArgs = append(countArgs, stage)
	}
	if len(requestIDs) > 0 {
		countQuery += " AND request_id IN (?)"
		countArgs = append(countArgs, requestIDs)
	}
	countCh := s.countAsync(ctx, countQuery, countArgs...)

	query := `
		SELECT tenant_id, request_id, stream_id, content_type, stage,
		       progress_percent, error_message, requested_at, started_at, completed_at,
		       clip_start_unix, clip_stop_unix, segment_count, manifest_path,
		       file_path, s3_url, size_bytes, processing_node_id, updated_at, expires_at
		FROM artifact_state_current FINAL
		WHERE tenant_id = ?
	`
	args := []interface{}{tenantID}

	if streamID != "" {
		query += " AND stream_id = ?"
		args = append(args, streamID)
	}

	if contentType != "" {
		query += " AND content_type = ?"
		args = append(args, contentType)
	}

	if stage != "" {
		query += " AND stage = ?"
		args = append(args, stage)
	}

	if len(requestIDs) > 0 {
		query += " AND request_id IN (?)"
		args = append(args, requestIDs)
	}

	keysetCond, keysetArgs := buildKeysetCondition(params, "updated_at", "request_id")
	if keysetCond != "" {
		query += keysetCond
		args = append(args, keysetArgs...)
	}

	query += buildOrderBy(params, "updated_at", "request_id")
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var artifacts []*pb.ArtifactState
	for rows.Next() {
		var artifact pb.ArtifactState
		var errorMessage, manifestPath, filePath, s3URL, processingNodeID *string
		var startedAt, completedAt *time.Time
		var clipStartUnix, clipStopUnix *int64
		var segmentCount *uint32
		var sizeBytes *uint64
		var requestedAt, updatedAt time.Time
		var expiresAt *time.Time
		var progressPercent uint8

		err := rows.Scan(
			&artifact.TenantId, &artifact.RequestId, &artifact.StreamId, &artifact.ContentType, &artifact.Stage,
			&progressPercent, &errorMessage, &requestedAt, &startedAt, &completedAt,
			&clipStartUnix, &clipStopUnix, &segmentCount, &manifestPath,
			&filePath, &s3URL, &sizeBytes, &processingNodeID, &updatedAt, &expiresAt,
		)
		if err != nil {
			s.logger.WithError(err).Error("Failed to scan artifact row")
			continue
		}

		artifact.ProgressPercent = uint32(progressPercent)
		artifact.ErrorMessage = errorMessage
		artifact.RequestedAt = timestamppb.New(requestedAt)
		if startedAt != nil {
			artifact.StartedAt = timestamppb.New(*startedAt)
		}
		if completedAt != nil {
			artifact.CompletedAt = timestamppb.New(*completedAt)
		}
		artifact.ClipStartUnix = clipStartUnix
		artifact.ClipStopUnix = clipStopUnix
		artifact.SegmentCount = segmentCount
		artifact.ManifestPath = manifestPath
		artifact.FilePath = filePath
		artifact.S3Url = s3URL
		artifact.SizeBytes = sizeBytes
		artifact.ProcessingNodeId = processingNodeID
		artifact.UpdatedAt = timestamppb.New(updatedAt)
		if expiresAt != nil {
			artifact.ExpiresAt = timestamppb.New(*expiresAt)
		}

		artifacts = append(artifacts, &artifact)
	}

	resultsLen := len(artifacts)
	if resultsLen > params.Limit {
		artifacts = artifacts[:params.Limit]
	}

	if params.Direction == pagination.Backward {
		slices.Reverse(artifacts)
	}

	// Wait for parallel count query
	total := <-countCh

	var startCursor, endCursor string
	if len(artifacts) > 0 {
		startCursor = pagination.EncodeCursor(artifacts[0].UpdatedAt.AsTime(), artifacts[0].RequestId)
		endCursor = pagination.EncodeCursor(artifacts[len(artifacts)-1].UpdatedAt.AsTime(), artifacts[len(artifacts)-1].RequestId)
	}

	return &pb.GetArtifactStatesResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Artifacts:  artifacts,
	}, nil
}

// ============================================================================
// AggregatedAnalyticsService Implementation (Materialized Views)
// ============================================================================

// GetStreamConnectionHourly returns hourly connection aggregates from stream_connection_hourly MV
func (s *PeriscopeServer) GetStreamConnectionHourly(ctx context.Context, req *pb.GetStreamConnectionHourlyRequest) (*pb.GetStreamConnectionHourlyResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	params, err := getCursorPagination(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	streamID := req.GetStreamId()

	// Build count query
	countQuery := `SELECT count(*) FROM stream_connection_hourly WHERE tenant_id = ? AND hour >= ? AND hour <= ?`
	countArgs := []interface{}{tenantID, startTime, endTime}
	if streamID != "" {
		countQuery += " AND stream_id = ?"
		countArgs = append(countArgs, streamID)
	}
	countCh := s.countAsync(ctx, countQuery, countArgs...)

	// AggregatingMergeTree requires Merge functions to read aggregated values
	query := `
		SELECT hour, tenant_id, stream_id,
		       sumMerge(total_bytes) as total_bytes,
		       uniqMerge(unique_viewers) as unique_viewers,
		       countMerge(total_sessions) as total_sessions
		FROM stream_connection_hourly
		WHERE tenant_id = ? AND hour >= ? AND hour <= ?
	`
	args := []interface{}{tenantID, startTime, endTime}

	if streamID != "" {
		query += " AND stream_id = ?"
		args = append(args, streamID)
	}

	query += " GROUP BY hour, tenant_id, stream_id"

	if params.Cursor != nil {
		if params.Direction == pagination.Backward {
			query = `SELECT * FROM (` + query + `) WHERE (hour, stream_id) > (?, ?)`
		} else {
			query = `SELECT * FROM (` + query + `) WHERE (hour, stream_id) < (?, ?)`
		}
		args = append(args, params.Cursor.Timestamp, params.Cursor.ID)
	}

	if params.Direction == pagination.Backward {
		query += " ORDER BY hour ASC, stream_id"
	} else {
		query += " ORDER BY hour DESC, stream_id"
	}
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var records []*pb.StreamConnectionHourly
	for rows.Next() {
		var hour time.Time
		var tenantIDStr, streamID string
		var totalBytes, uniqueViewers, totalSessions uint64

		err := rows.Scan(&hour, &tenantIDStr, &streamID, &totalBytes, &uniqueViewers, &totalSessions)
		if err != nil {
			s.logger.WithError(err).Error("Failed to scan stream_connection_hourly row")
			continue
		}

		records = append(records, &pb.StreamConnectionHourly{
			Id:            fmt.Sprintf("%s_%s", hour.Format(time.RFC3339), streamID),
			Hour:          timestamppb.New(hour),
			TenantId:      tenantIDStr,
			StreamId:      streamID,
			TotalBytes:    totalBytes,
			UniqueViewers: uniqueViewers,
			TotalSessions: totalSessions,
		})
	}

	resultsLen := len(records)
	if resultsLen > params.Limit {
		records = records[:params.Limit]
	}

	if params.Direction == pagination.Backward {
		slices.Reverse(records)
	}

	total := <-countCh

	var startCursor, endCursor string
	if len(records) > 0 {
		startCursor = pagination.EncodeCursor(records[0].Hour.AsTime(), records[0].StreamId)
		endCursor = pagination.EncodeCursor(records[len(records)-1].Hour.AsTime(), records[len(records)-1].StreamId)
	}

	return &pb.GetStreamConnectionHourlyResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Records:    records,
	}, nil
}

// GetClientMetrics5M returns 5-minute client metrics aggregates from client_qoe_5m MV
func (s *PeriscopeServer) GetClientMetrics5M(ctx context.Context, req *pb.GetClientMetrics5MRequest) (*pb.GetClientMetrics5MResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	params, err := getCursorPagination(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	streamID := req.GetStreamId()
	nodeID := req.GetNodeId()

	// Build count query
	countQuery := `SELECT count(*) FROM client_qoe_5m WHERE tenant_id = ? AND timestamp_5m >= ? AND timestamp_5m <= ?`
	countArgs := []interface{}{tenantID, startTime, endTime}
	if streamID != "" {
		countQuery += " AND stream_id = ?"
		countArgs = append(countArgs, streamID)
	}
	if nodeID != "" {
		countQuery += " AND node_id = ?"
		countArgs = append(countArgs, nodeID)
	}
	countCh := s.countAsync(ctx, countQuery, countArgs...)

	query := `
		SELECT timestamp_5m, tenant_id, stream_id, node_id, active_sessions,
		       avg_bw_in, avg_bw_out, avg_connection_time, pkt_loss_rate
		FROM client_qoe_5m
		WHERE tenant_id = ? AND timestamp_5m >= ? AND timestamp_5m <= ?
	`
	args := []interface{}{tenantID, startTime, endTime}

	if streamID != "" {
		query += " AND stream_id = ?"
		args = append(args, streamID)
	}

	if nodeID != "" {
		query += " AND node_id = ?"
		args = append(args, nodeID)
	}

	if params.Cursor != nil {
		if params.Direction == pagination.Backward {
			query += " AND (timestamp_5m, stream_id, node_id) > (?, ?, ?)"
		} else {
			query += " AND (timestamp_5m, stream_id, node_id) < (?, ?, ?)"
		}
		// Cursor ID is compound: "stream_id|node_id"
		cursorStreamID := params.Cursor.ID
		cursorNodeID := ""
		if parts := strings.SplitN(params.Cursor.ID, "|", 2); len(parts) == 2 {
			cursorStreamID = parts[0]
			cursorNodeID = parts[1]
		}
		args = append(args, params.Cursor.Timestamp, cursorStreamID, cursorNodeID)
	}

	if params.Direction == pagination.Backward {
		query += " ORDER BY timestamp_5m ASC, stream_id, node_id"
	} else {
		query += " ORDER BY timestamp_5m DESC, stream_id, node_id"
	}
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var records []*pb.ClientMetrics5M
	for rows.Next() {
		var timestamp time.Time
		var tenantIDStr, streamIDStr, nodeIDStr string
		var activeSessions uint32
		var avgBwIn, avgBwOut float64
		var avgConnTime float32
		var pktLossRate sql.NullFloat64

		err := rows.Scan(&timestamp, &tenantIDStr, &streamIDStr, &nodeIDStr, &activeSessions,
			&avgBwIn, &avgBwOut, &avgConnTime, &pktLossRate)
		if err != nil {
			s.logger.WithError(err).Error("Failed to scan client_qoe_5m row")
			continue
		}

		record := &pb.ClientMetrics5M{
			Id:                fmt.Sprintf("%s_%s_%s", timestamp.Format(time.RFC3339), streamIDStr, nodeIDStr),
			Timestamp:         timestamppb.New(timestamp),
			TenantId:          tenantIDStr,
			StreamId:          streamIDStr,
			NodeId:            nodeIDStr,
			ActiveSessions:    activeSessions,
			AvgBandwidthIn:    avgBwIn,
			AvgBandwidthOut:   avgBwOut,
			AvgConnectionTime: avgConnTime,
		}
		if pktLossRate.Valid {
			v := float32(pktLossRate.Float64)
			record.PacketLossRate = &v
		}
		records = append(records, record)
	}

	resultsLen := len(records)
	if resultsLen > params.Limit {
		records = records[:params.Limit]
	}

	if params.Direction == pagination.Backward {
		slices.Reverse(records)
	}

	total := <-countCh

	var startCursor, endCursor string
	if len(records) > 0 {
		// Encode compound ID: "stream_id|node_id" for proper 3-tuple pagination
		startCursor = pagination.EncodeCursor(records[0].Timestamp.AsTime(), records[0].StreamId+"|"+records[0].NodeId)
		endCursor = pagination.EncodeCursor(records[len(records)-1].Timestamp.AsTime(), records[len(records)-1].StreamId+"|"+records[len(records)-1].NodeId)
	}

	return &pb.GetClientMetrics5MResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Records:    records,
	}, nil
}

// GetQualityTierDaily returns daily quality tier distribution from quality_tier_daily MV
func (s *PeriscopeServer) GetQualityTierDaily(ctx context.Context, req *pb.GetQualityTierDailyRequest) (*pb.GetQualityTierDailyResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	params, err := getCursorPagination(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	streamID := req.GetStreamId()

	// Build count query
	countQuery := `SELECT count(*) FROM quality_tier_daily WHERE tenant_id = ? AND day >= toDate(?) AND day <= toDate(?)`
	countArgs := []interface{}{tenantID, startTime, endTime}
	if streamID != "" {
		countQuery += " AND stream_id = ?"
		countArgs = append(countArgs, streamID)
	}
	countCh := s.countAsync(ctx, countQuery, countArgs...)

	// SummingMergeTree sums numeric columns - need to SUM in query
	query := `
			SELECT day, tenant_id, stream_id,
			       sum(tier_2160p_minutes), sum(tier_1440p_minutes), sum(tier_1080p_minutes),
			       sum(tier_720p_minutes), sum(tier_480p_minutes), sum(tier_sd_minutes),
			       any(primary_tier), sum(codec_h264_minutes), sum(codec_h265_minutes),
			       sum(codec_vp9_minutes), sum(codec_av1_minutes),
			       avg(avg_bitrate), avg(avg_fps)
			FROM quality_tier_daily
			WHERE tenant_id = ? AND day >= toDate(?) AND day <= toDate(?)
		`
	args := []interface{}{tenantID, startTime, endTime}

	if streamID != "" {
		query += " AND stream_id = ?"
		args = append(args, streamID)
	}

	query += " GROUP BY day, tenant_id, stream_id"

	if params.Cursor != nil {
		if params.Direction == pagination.Backward {
			query = `SELECT * FROM (` + query + `) WHERE (day, stream_id) > (?, ?)`
		} else {
			query = `SELECT * FROM (` + query + `) WHERE (day, stream_id) < (?, ?)`
		}
		args = append(args, params.Cursor.Timestamp, params.Cursor.ID)
	}

	if params.Direction == pagination.Backward {
		query += " ORDER BY day ASC, stream_id"
	} else {
		query += " ORDER BY day DESC, stream_id"
	}
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var records []*pb.QualityTierDaily
	for rows.Next() {
		var day time.Time
		var tenantIDStr, streamIDStr, primaryTier string
		var tier2160p, tier1440p, tier1080p, tier720p, tier480p, tierSD, codecH264, codecH265, codecVp9, codecAv1, avgBitrate uint32
		var avgFps float32

		err := rows.Scan(&day, &tenantIDStr, &streamIDStr, &tier2160p, &tier1440p, &tier1080p, &tier720p, &tier480p, &tierSD,
			&primaryTier, &codecH264, &codecH265, &codecVp9, &codecAv1, &avgBitrate, &avgFps)
		if err != nil {
			s.logger.WithError(err).Error("Failed to scan quality_tier_daily row")
			continue
		}

		records = append(records, &pb.QualityTierDaily{
			Id:                fmt.Sprintf("%s_%s", day.Format("2006-01-02"), streamIDStr),
			Day:               timestamppb.New(day),
			TenantId:          tenantIDStr,
			StreamId:          streamIDStr,
			Tier_2160PMinutes: tier2160p,
			Tier_1440PMinutes: tier1440p,
			Tier_1080PMinutes: tier1080p,
			Tier_720PMinutes:  tier720p,
			Tier_480PMinutes:  tier480p,
			TierSdMinutes:     tierSD,
			PrimaryTier:       primaryTier,
			CodecH264Minutes:  codecH264,
			CodecH265Minutes:  codecH265,
			CodecVp9Minutes:   codecVp9,
			CodecAv1Minutes:   codecAv1,
			AvgBitrate:        avgBitrate,
			AvgFps:            avgFps,
		})
	}

	resultsLen := len(records)
	if resultsLen > params.Limit {
		records = records[:params.Limit]
	}

	if params.Direction == pagination.Backward {
		slices.Reverse(records)
	}

	total := <-countCh

	var startCursor, endCursor string
	if len(records) > 0 {
		startCursor = pagination.EncodeCursor(records[0].Day.AsTime(), records[0].StreamId)
		endCursor = pagination.EncodeCursor(records[len(records)-1].Day.AsTime(), records[len(records)-1].StreamId)
	}

	return &pb.GetQualityTierDailyResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Records:    records,
	}, nil
}

// GetStreamAnalyticsSummary returns range aggregates derived from materialized views only.
func (s *PeriscopeServer) GetStreamAnalyticsSummary(ctx context.Context, req *pb.GetStreamAnalyticsSummaryRequest) (*pb.GetStreamAnalyticsSummaryResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	streamID := req.GetStreamId()
	if streamID == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_id required")
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	summary := &pb.StreamAnalyticsSummary{
		TenantId:  tenantID,
		StreamId:  streamID,
		TimeRange: req.GetTimeRange(),
	}
	// Ensure non-null GraphQL contract for rangeQuality even if the query returns no rows.
	summary.RangeQuality = &pb.QualityTierSummary{}
	var totalSessionsVal int64
	var totalSessionSecondsVal int64
	var totalBytesVal int64

	// Viewer concurrency summary from stream_viewer_5m
	{
		var avgViewers sql.NullFloat64
		var peakViewers sql.NullInt64
		err := s.clickhouse.QueryRowContext(ctx, `
			SELECT avg(avg_viewers), max(max_viewers)
			FROM periscope.stream_viewer_5m
			WHERE tenant_id = ? AND stream_id = ? AND timestamp_5m >= ? AND timestamp_5m <= ?
		`, tenantID, streamID, startTime, endTime).Scan(&avgViewers, &peakViewers)
		if err == nil {
			if avgViewers.Valid {
				summary.RangeAvgViewers = float32(avgViewers.Float64)
			}
			if peakViewers.Valid {
				summary.RangePeakConcurrentViewers = uint32(peakViewers.Int64)
			}
		}
	}

	// Stream health aggregates from stream_health_5m
	{
		var avgBufferHealth, avgFps sql.NullFloat64
		var avgBitrate sql.NullFloat64
		var rebufferCount, issueCount, bufferDryCount sql.NullInt64
		err := s.clickhouse.QueryRowContext(ctx, `
			SELECT avg(avg_buffer_health), avg(avg_bitrate), avg(avg_fps),
			       sum(rebuffer_count), sum(issue_count), sum(buffer_dry_count)
			FROM periscope.stream_health_5m
			WHERE tenant_id = ? AND stream_id = ? AND timestamp_5m >= ? AND timestamp_5m <= ?
		`, tenantID, streamID, startTime, endTime).Scan(&avgBufferHealth, &avgBitrate, &avgFps, &rebufferCount, &issueCount, &bufferDryCount)
		if err == nil {
			if avgBufferHealth.Valid {
				summary.RangeAvgBufferHealth = float32(avgBufferHealth.Float64)
			}
			if avgBitrate.Valid {
				summary.RangeAvgBitrate = uint32(avgBitrate.Float64)
			}
			if avgFps.Valid {
				summary.RangeAvgFps = float32(avgFps.Float64)
			}
			if rebufferCount.Valid {
				summary.RangeRebufferCount = rebufferCount.Int64
			}
			if issueCount.Valid {
				summary.RangeIssueCount = issueCount.Int64
			}
			if bufferDryCount.Valid {
				summary.RangeBufferDryCount = bufferDryCount.Int64
			}
		}
	}

	// Client QoE aggregates from client_qoe_5m
	{
		var pktLossRate, avgConnTime sql.NullFloat64
		err := s.clickhouse.QueryRowContext(ctx, `
			SELECT avg(pkt_loss_rate), avg(avg_connection_time)
			FROM periscope.client_qoe_5m
			WHERE tenant_id = ? AND stream_id = ? AND timestamp_5m >= ? AND timestamp_5m <= ?
		`, tenantID, streamID, startTime, endTime).Scan(&pktLossRate, &avgConnTime)
		if err == nil {
			if pktLossRate.Valid {
				summary.RangePacketLossRate = float32(pktLossRate.Float64)
			}
			if avgConnTime.Valid {
				summary.RangeAvgConnectionTime = float32(avgConnTime.Float64)
			}
		}
	}

	// Viewer hours, egress, and unique countries from viewer_hours_hourly
	{
		var totalSessionSeconds, totalBytes sql.NullInt64
		var uniqueCountries sql.NullInt64
		err := s.clickhouse.QueryRowContext(ctx, `
			SELECT sumMerge(total_session_seconds), sumMerge(total_bytes), uniqExact(country_code)
			FROM periscope.viewer_hours_hourly
			WHERE tenant_id = ? AND stream_id = ? AND hour >= ? AND hour <= ?
		`, tenantID, streamID, startTime, endTime).Scan(&totalSessionSeconds, &totalBytes, &uniqueCountries)
		if err == nil {
			if totalSessionSeconds.Valid {
				summary.RangeViewerHours = float32(totalSessionSeconds.Int64) / 3600.0
				totalSessionSecondsVal = totalSessionSeconds.Int64
			}
			if totalBytes.Valid {
				summary.RangeEgressGb = float32(float64(totalBytes.Int64) / (1024.0 * 1024.0 * 1024.0))
				totalBytesVal = totalBytes.Int64
			}
			if uniqueCountries.Valid {
				summary.RangeUniqueCountries = int32(uniqueCountries.Int64)
			}
		}
	}

	// Unique viewers + total sessions from stream_connection_hourly
	{
		var uniqueViewers, totalSessions sql.NullInt64
		err := s.clickhouse.QueryRowContext(ctx, `
			SELECT uniqMerge(unique_viewers), countMerge(total_sessions)
			FROM periscope.stream_connection_hourly
			WHERE tenant_id = ? AND stream_id = ? AND hour >= ? AND hour <= ?
		`, tenantID, streamID, startTime, endTime).Scan(&uniqueViewers, &totalSessions)
		if err == nil {
			if uniqueViewers.Valid {
				summary.RangeUniqueViewers = uniqueViewers.Int64
			}
			if totalSessions.Valid {
				summary.RangeTotalSessions = totalSessions.Int64
				totalSessionsVal = totalSessions.Int64
			}
		}
	}

	if totalSessionsVal > 0 {
		summary.RangeAvgSessionSeconds = float32(totalSessionSecondsVal) / float32(totalSessionsVal)
		summary.RangeAvgBytesPerSession = float32(totalBytesVal) / float32(totalSessionsVal)
	}

	// Total views from stream_analytics_daily
	{
		var totalViews sql.NullInt64
		err := s.clickhouse.QueryRowContext(ctx, `
			SELECT sum(total_views)
			FROM periscope.stream_analytics_daily
			WHERE tenant_id = ? AND stream_id = ? AND day >= toDate(?) AND day <= toDate(?)
		`, tenantID, streamID, startTime, endTime).Scan(&totalViews)
		if err == nil && totalViews.Valid {
			summary.RangeTotalViews = totalViews.Int64
		}
	}

	// Quality tier summary from quality_tier_daily
	{
		var tier2160p, tier1440p, tier1080p, tier720p, tier480p, tierSD sql.NullInt64
		var codecH264, codecH265, codecVp9, codecAv1 sql.NullInt64
		err := s.clickhouse.QueryRowContext(ctx, `
			SELECT sum(tier_2160p_minutes), sum(tier_1440p_minutes), sum(tier_1080p_minutes),
			       sum(tier_720p_minutes), sum(tier_480p_minutes), sum(tier_sd_minutes),
			       sum(codec_h264_minutes), sum(codec_h265_minutes),
			       sum(codec_vp9_minutes), sum(codec_av1_minutes)
			FROM periscope.quality_tier_daily
			WHERE tenant_id = ? AND stream_id = ? AND day >= toDate(?) AND day <= toDate(?)
		`, tenantID, streamID, startTime, endTime).Scan(&tier2160p, &tier1440p, &tier1080p, &tier720p, &tier480p, &tierSD, &codecH264, &codecH265, &codecVp9, &codecAv1)
		if err == nil {
			summary.RangeQuality = &pb.QualityTierSummary{
				Tier_2160PMinutes: uint32(nullInt64Value(tier2160p)),
				Tier_1440PMinutes: uint32(nullInt64Value(tier1440p)),
				Tier_1080PMinutes: uint32(nullInt64Value(tier1080p)),
				Tier_720PMinutes:  uint32(nullInt64Value(tier720p)),
				Tier_480PMinutes:  uint32(nullInt64Value(tier480p)),
				TierSdMinutes:     uint32(nullInt64Value(tierSD)),
				CodecH264Minutes:  uint32(nullInt64Value(codecH264)),
				CodecH265Minutes:  uint32(nullInt64Value(codecH265)),
				CodecVp9Minutes:   uint32(nullInt64Value(codecVp9)),
				CodecAv1Minutes:   uint32(nullInt64Value(codecAv1)),
			}
		}
	}

	return &pb.GetStreamAnalyticsSummaryResponse{Summary: summary}, nil
}

// GetStreamAnalyticsSummaries returns bulk stream analytics summaries with share percentages.
// This aggregates data from multiple MVs for all streams in a tenant, sorted by the requested field.
// Uses keyset pagination with raw integer sort keys (egress_bytes, viewer_seconds) for precision.
func (s *PeriscopeServer) GetStreamAnalyticsSummaries(ctx context.Context, req *pb.GetStreamAnalyticsSummariesRequest) (*pb.GetStreamAnalyticsSummariesResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	params, err := getCursorPagination(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	// Map sort field to raw integer column for keyset precision
	// Display values (egress_gb, viewer_hours) are derived but we sort/cursor by raw integers
	var sortField string
	switch req.GetSortBy() {
	case pb.StreamSummarySortField_STREAM_SUMMARY_SORT_FIELD_UNIQUE_VIEWERS:
		sortField = "unique_viewers"
	case pb.StreamSummarySortField_STREAM_SUMMARY_SORT_FIELD_TOTAL_VIEWS:
		sortField = "total_views"
	case pb.StreamSummarySortField_STREAM_SUMMARY_SORT_FIELD_VIEWER_HOURS:
		sortField = "viewer_seconds"
	default:
		sortField = "egress_bytes"
	}

	sortDesc := req.GetSortOrder() != pb.SortOrder_SORT_ORDER_ASC
	sortOrder := "DESC"
	if !sortDesc {
		sortOrder = "ASC"
	}

	// Build keyset WHERE clause if cursor provided
	// Cursor stores raw int64 sort_key + stream_id
	keysetWhere := ""
	var keysetArgs []interface{}
	if params.Cursor != nil {
		cursorSortKey := params.Cursor.GetSortKey() // raw int64 from sk: cursor
		cursorStreamID := params.Cursor.ID

		if params.Direction == pagination.Backward {
			if sortDesc {
				keysetWhere = fmt.Sprintf("AND (%s, stream_id) > (?, ?)", sortField)
			} else {
				keysetWhere = fmt.Sprintf("AND (%s, stream_id) < (?, ?)", sortField)
			}
		} else {
			if sortDesc {
				keysetWhere = fmt.Sprintf("AND (%s, stream_id) < (?, ?)", sortField)
			} else {
				keysetWhere = fmt.Sprintf("AND (%s, stream_id) > (?, ?)", sortField)
			}
		}
		keysetArgs = []interface{}{cursorSortKey, cursorStreamID}
	}

	// For backward pagination, reverse the sort order in query, then reverse results
	querySortOrder := sortOrder
	if params.Direction == pagination.Backward {
		if sortDesc {
			querySortOrder = "ASC"
		} else {
			querySortOrder = "DESC"
		}
	}

	// Query aggregates from stream_analytics_daily + viewer_hours_hourly
	// Raw integer columns (egress_bytes, viewer_seconds) used for sort/keyset
	// Derived columns (egress_gb, viewer_hours) for display only
	query := fmt.Sprintf(`
		WITH daily_totals AS (
			SELECT
				stream_id,
				sum(total_views) as total_views,
				max(unique_viewers) as unique_viewers,
				sum(egress_bytes) as egress_bytes
			FROM periscope.stream_analytics_daily
			WHERE tenant_id = ? AND day >= toDate(?) AND day <= toDate(?)
			GROUP BY stream_id
		),
		hourly_totals AS (
			SELECT
				stream_id,
				sumMerge(total_session_seconds) as viewer_seconds,
				sumMerge(total_bytes) as viewer_bytes
			FROM periscope.viewer_hours_hourly
			WHERE tenant_id = ? AND hour >= ? AND hour <= ?
			GROUP BY stream_id
		),
		combined AS (
			SELECT
				d.stream_id,
				d.total_views,
				d.unique_viewers,
				d.egress_bytes,
				coalesce(h.viewer_seconds, 0) as viewer_seconds
			FROM daily_totals d
			LEFT JOIN hourly_totals h ON d.stream_id = h.stream_id
		),
		with_shares AS (
			SELECT
				stream_id,
				total_views,
				unique_viewers,
				egress_bytes,
				viewer_seconds,
				egress_bytes / (1024.0 * 1024.0 * 1024.0) as egress_gb,
				viewer_seconds / 3600.0 as viewer_hours,
				total_views * 100.0 / nullIf(sum(total_views) OVER (), 0) as views_share_pct,
				unique_viewers * 100.0 / nullIf(sum(unique_viewers) OVER (), 0) as viewers_share_pct,
				egress_bytes * 100.0 / nullIf(sum(egress_bytes) OVER (), 0) as egress_share_pct,
				viewer_seconds * 100.0 / nullIf(sum(viewer_seconds) OVER (), 0) as viewer_hours_share_pct
			FROM combined
		)
		SELECT stream_id, total_views, unique_viewers, egress_bytes, viewer_seconds,
		       egress_gb, viewer_hours,
		       views_share_pct, viewers_share_pct, egress_share_pct, viewer_hours_share_pct
		FROM with_shares
		WHERE 1=1 %s
		ORDER BY %s %s, stream_id %s
		LIMIT ?
	`, keysetWhere, sortField, querySortOrder, querySortOrder)

	// Build query args
	args := []interface{}{
		tenantID, startTime, endTime, // daily_totals
		tenantID, startTime, endTime, // hourly_totals
	}
	args = append(args, keysetArgs...)
	args = append(args, params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var summaries []*pb.StreamAnalyticsSummary

	for rows.Next() {
		var streamID string
		var totalViews, uniqueViewers, egressBytes, viewerSeconds int64
		var egressGb, viewerHours, viewsSharePct, viewersSharePct, egressSharePct, viewerHoursSharePct sql.NullFloat64

		if err := rows.Scan(&streamID, &totalViews, &uniqueViewers, &egressBytes, &viewerSeconds,
			&egressGb, &viewerHours,
			&viewsSharePct, &viewersSharePct, &egressSharePct, &viewerHoursSharePct); err != nil {
			s.logger.WithError(err).Error("Failed to scan stream summary row")
			continue
		}

		summary := &pb.StreamAnalyticsSummary{
			TenantId:           tenantID,
			StreamId:           streamID,
			TimeRange:          req.GetTimeRange(),
			RangeTotalViews:    totalViews,
			RangeUniqueViewers: uniqueViewers,
			RangeEgressBytes:   egressBytes,
			RangeViewerSeconds: viewerSeconds,
			RangeQuality:       &pb.QualityTierSummary{},
		}

		if egressGb.Valid {
			summary.RangeEgressGb = float32(egressGb.Float64)
		}
		if viewerHours.Valid {
			summary.RangeViewerHours = float32(viewerHours.Float64)
		}
		if egressSharePct.Valid {
			val := float32(egressSharePct.Float64)
			summary.RangeEgressSharePercent = &val
		}
		if viewersSharePct.Valid {
			val := float32(viewersSharePct.Float64)
			summary.RangeViewerSharePercent = &val
		}
		if viewerHoursSharePct.Valid {
			val := float32(viewerHoursSharePct.Float64)
			summary.RangeViewerHoursSharePercent = &val
		}

		summaries = append(summaries, summary)
	}

	// Check if there are more results
	hasMore := len(summaries) > params.Limit
	if hasMore {
		summaries = summaries[:params.Limit]
	}

	// Reverse results for backward pagination
	if params.Direction == pagination.Backward {
		slices.Reverse(summaries)
	}

	// Get total count
	var totalCount int64
	countRow := s.clickhouse.QueryRowContext(ctx, `
		SELECT count(DISTINCT stream_id)
		FROM periscope.stream_analytics_daily
		WHERE tenant_id = ? AND day >= toDate(?) AND day <= toDate(?)
	`, tenantID, startTime, endTime)
	_ = countRow.Scan(&totalCount)

	// Build keyset cursors using raw integer sort keys from proto fields
	var startCursor, endCursor string
	if len(summaries) > 0 {
		first := summaries[0]
		last := summaries[len(summaries)-1]

		startCursor = buildStreamSummaryCursor(first, sortField)
		endCursor = buildStreamSummaryCursor(last, sortField)
	}

	hasPrevious := params.Cursor != nil
	if params.Direction == pagination.Backward {
		hasPrevious, hasMore = hasMore, hasPrevious
	}

	resp := &pb.GetStreamAnalyticsSummariesResponse{
		Pagination: &pb.CursorPaginationResponse{
			TotalCount:      int32(totalCount),
			HasNextPage:     hasMore,
			HasPreviousPage: hasPrevious,
		},
		Summaries:  summaries,
		TotalCount: totalCount,
	}
	if startCursor != "" {
		resp.Pagination.StartCursor = &startCursor
	}
	if endCursor != "" {
		resp.Pagination.EndCursor = &endCursor
	}

	return resp, nil
}

// buildStreamSummaryCursor creates a keyset cursor using raw integer sort keys from proto fields.
func buildStreamSummaryCursor(summary *pb.StreamAnalyticsSummary, sortField string) string {
	var sortKey int64
	switch sortField {
	case "egress_bytes":
		sortKey = summary.RangeEgressBytes
	case "viewer_seconds":
		sortKey = summary.RangeViewerSeconds
	case "unique_viewers":
		sortKey = summary.RangeUniqueViewers
	case "total_views":
		sortKey = summary.RangeTotalViews
	}
	return pagination.EncodeCursorWithSortKey(sortKey, summary.StreamId)
}

// GetStorageUsage returns storage usage records from storage_snapshots table
func (s *PeriscopeServer) GetStorageUsage(ctx context.Context, req *pb.GetStorageUsageRequest) (*pb.GetStorageUsageResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	params, err := getCursorPagination(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	nodeID := req.GetNodeId()
	storageScope := req.GetStorageScope()

	// Build count query
	countQuery := `SELECT count(*) FROM storage_snapshots WHERE tenant_id = ? AND timestamp >= ? AND timestamp <= ?`
	countArgs := []interface{}{tenantID, startTime, endTime}
	if nodeID != "" {
		countQuery += " AND node_id = ?"
		countArgs = append(countArgs, nodeID)
	}
	if storageScope != "" {
		countQuery += " AND storage_scope = ?"
		countArgs = append(countArgs, storageScope)
	}
	countCh := s.countAsync(ctx, countQuery, countArgs...)

	query := `
		SELECT timestamp, tenant_id, node_id, storage_scope, total_bytes, file_count,
		       dvr_bytes, clip_bytes, vod_bytes,
		       frozen_dvr_bytes, frozen_clip_bytes, frozen_vod_bytes
		FROM storage_snapshots
		WHERE tenant_id = ? AND timestamp >= ? AND timestamp <= ?
	`
	args := []interface{}{tenantID, startTime, endTime}

	if nodeID != "" {
		query += " AND node_id = ?"
		args = append(args, nodeID)
	}
	if storageScope != "" {
		query += " AND storage_scope = ?"
		args = append(args, storageScope)
	}

	keysetCond, keysetArgs := buildKeysetCondition(params, "timestamp", "concat(storage_scope, ':', node_id)")
	if keysetCond != "" {
		query += keysetCond
		args = append(args, keysetArgs...)
	}

	query += buildOrderBy(params, "timestamp", "concat(storage_scope, ':', node_id)")
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var records []*pb.StorageUsageRecord
	for rows.Next() {
		var timestamp time.Time
		var tenantIDStr, nodeIDStr, storageScopeStr string
		var totalBytes, dvrBytes, clipBytes, vodBytes uint64
		var frozenDvrBytes, frozenClipBytes, frozenVodBytes uint64
		var fileCount uint32

		err := rows.Scan(&timestamp, &tenantIDStr, &nodeIDStr, &storageScopeStr, &totalBytes, &fileCount,
			&dvrBytes, &clipBytes, &vodBytes,
			&frozenDvrBytes, &frozenClipBytes, &frozenVodBytes)
		if err != nil {
			s.logger.WithError(err).Error("Failed to scan storage_snapshots row")
			continue
		}

		idKey := fmt.Sprintf("%s:%s", storageScopeStr, nodeIDStr)
		records = append(records, &pb.StorageUsageRecord{
			Id:              idKey,
			Timestamp:       timestamppb.New(timestamp),
			TenantId:        tenantIDStr,
			NodeId:          nodeIDStr,
			TotalBytes:      totalBytes,
			FileCount:       fileCount,
			DvrBytes:        dvrBytes,
			ClipBytes:       clipBytes,
			VodBytes:        vodBytes,
			FrozenDvrBytes:  frozenDvrBytes,
			FrozenClipBytes: frozenClipBytes,
			FrozenVodBytes:  frozenVodBytes,
			StorageScope:    storageScopeStr,
		})
	}

	resultsLen := len(records)
	if resultsLen > params.Limit {
		records = records[:params.Limit]
	}

	if params.Direction == pagination.Backward {
		slices.Reverse(records)
	}

	total := <-countCh

	var startCursor, endCursor string
	if len(records) > 0 {
		startID := records[0].Id
		endID := records[len(records)-1].Id
		startCursor = pagination.EncodeCursor(records[0].Timestamp.AsTime(), startID)
		endCursor = pagination.EncodeCursor(records[len(records)-1].Timestamp.AsTime(), endID)
	}

	return &pb.GetStorageUsageResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Records:    records,
	}, nil
}

// GetStorageEvents returns storage lifecycle events (freeze/defrost operations) from storage_events table
func (s *PeriscopeServer) GetStorageEvents(ctx context.Context, req *pb.GetStorageEventsRequest) (*pb.GetStorageEventsResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	params, err := getCursorPagination(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	streamID := req.GetStreamId()
	assetType := req.GetAssetType()

	// Build count query
	countQuery := `SELECT count(*) FROM storage_events WHERE tenant_id = ? AND timestamp >= ? AND timestamp <= ?`
	countArgs := []interface{}{tenantID, startTime, endTime}
	if streamID != "" {
		countQuery += " AND stream_id = ?"
		countArgs = append(countArgs, streamID)
	}
	if assetType != "" {
		countQuery += " AND asset_type = ?"
		countArgs = append(countArgs, assetType)
	}
	countCh := s.countAsync(ctx, countQuery, countArgs...)

	query := `
		SELECT timestamp, tenant_id, stream_id, asset_hash, action, asset_type,
		       size_bytes, s3_url, local_path, node_id, duration_ms, warm_duration_ms, error
		FROM storage_events
		WHERE tenant_id = ? AND timestamp >= ? AND timestamp <= ?
	`
	args := []interface{}{tenantID, startTime, endTime}

	if streamID != "" {
		query += " AND stream_id = ?"
		args = append(args, streamID)
	}
	if assetType != "" {
		query += " AND asset_type = ?"
		args = append(args, assetType)
	}

	keysetCond, keysetArgs := buildKeysetCondition(params, "timestamp", "asset_hash")
	if keysetCond != "" {
		query += keysetCond
		args = append(args, keysetArgs...)
	}

	query += buildOrderBy(params, "timestamp", "asset_hash")
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var events []*pb.StorageEvent
	for rows.Next() {
		var timestamp time.Time
		var tenantIDStr, streamIDStr, assetHash, action, assetTypeStr, nodeID string
		var sizeBytes uint64
		var s3URL, localPath, errorMsg sql.NullString
		var durationMs, warmDurationMs sql.NullInt64

		err := rows.Scan(&timestamp, &tenantIDStr, &streamIDStr, &assetHash, &action, &assetTypeStr,
			&sizeBytes, &s3URL, &localPath, &nodeID, &durationMs, &warmDurationMs, &errorMsg)
		if err != nil {
			s.logger.WithError(err).Error("Failed to scan storage_events row")
			continue
		}

		event := &pb.StorageEvent{
			Id:        fmt.Sprintf("%s_%s", timestamp.Format(time.RFC3339), assetHash),
			Timestamp: timestamppb.New(timestamp),
			TenantId:  tenantIDStr,
			StreamId:  streamIDStr,
			AssetHash: assetHash,
			Action:    action,
			AssetType: assetTypeStr,
			SizeBytes: sizeBytes,
			NodeId:    nodeID,
		}
		if s3URL.Valid {
			event.S3Url = &s3URL.String
		}
		if localPath.Valid {
			event.LocalPath = &localPath.String
		}
		if durationMs.Valid {
			event.DurationMs = &durationMs.Int64
		}
		if warmDurationMs.Valid {
			event.WarmDurationMs = &warmDurationMs.Int64
		}
		if errorMsg.Valid {
			event.Error = &errorMsg.String
		}

		events = append(events, event)
	}

	resultsLen := len(events)
	if resultsLen > params.Limit {
		events = events[:params.Limit]
	}

	if params.Direction == pagination.Backward {
		slices.Reverse(events)
	}

	total := <-countCh

	var startCursor, endCursor string
	if len(events) > 0 {
		startCursor = pagination.EncodeCursor(events[0].Timestamp.AsTime(), events[0].AssetHash)
		endCursor = pagination.EncodeCursor(events[len(events)-1].Timestamp.AsTime(), events[len(events)-1].AssetHash)
	}

	return &pb.GetStorageEventsResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Events:     events,
	}, nil
}

// ============================================================================
// Stream Health 5-Minute Aggregates
// ============================================================================

// GetStreamHealth5M returns 5-minute aggregated health metrics from stream_health_5m MV
func (s *PeriscopeServer) GetStreamHealth5M(ctx context.Context, req *pb.GetStreamHealth5MRequest) (*pb.GetStreamHealth5MResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	streamID := req.GetStreamId()
	if streamID == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_id required")
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	params, err := getCursorPagination(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	// Count query
	countQuery := `SELECT count(*) FROM stream_health_5m WHERE tenant_id = ? AND stream_id = ? AND timestamp_5m >= ? AND timestamp_5m <= ?`
	countCh := s.countAsync(ctx, countQuery, tenantID, streamID, startTime, endTime)

	query := `
		SELECT timestamp_5m, tenant_id, stream_id, node_id, rebuffer_count, issue_count,
		       sample_issues, avg_bitrate, avg_fps, avg_buffer_health, avg_frame_jitter_ms, max_frame_jitter_ms, buffer_dry_count, quality_tier
		FROM stream_health_5m
		WHERE tenant_id = ? AND stream_id = ? AND timestamp_5m >= ? AND timestamp_5m <= ?
	`
	args := []interface{}{tenantID, streamID, startTime, endTime}

	keysetCond, keysetArgs := buildKeysetCondition(params, "timestamp_5m", "node_id")
	if keysetCond != "" {
		query += keysetCond
		args = append(args, keysetArgs...)
	}

	query += buildOrderBy(params, "timestamp_5m", "node_id")
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var records []*pb.StreamHealth5M
	for rows.Next() {
		var timestamp time.Time
		var tenantIDStr, streamIDStr, nodeID, qualityTier string
		var rebufferCount, issueCount, bufferDryCount int32
		var sampleIssues sql.NullString
		var avgBitrate, avgFps, avgBufferHealth float32
		var avgFrameJitterMs, maxFrameJitterMs sql.NullFloat64

		err := rows.Scan(&timestamp, &tenantIDStr, &streamIDStr, &nodeID, &rebufferCount, &issueCount,
			&sampleIssues, &avgBitrate, &avgFps, &avgBufferHealth, &avgFrameJitterMs, &maxFrameJitterMs, &bufferDryCount, &qualityTier)
		if err != nil {
			s.logger.WithError(err).Error("Failed to scan stream_health_5m row")
			continue
		}

		record := &pb.StreamHealth5M{
			Id:              fmt.Sprintf("%s_%s_%s", timestamp.Format(time.RFC3339), streamIDStr, nodeID),
			Timestamp:       timestamppb.New(timestamp),
			TenantId:        tenantIDStr,
			StreamId:        streamIDStr,
			NodeId:          nodeID,
			RebufferCount:   rebufferCount,
			IssueCount:      issueCount,
			AvgBitrate:      int32(avgBitrate),
			AvgFps:          avgFps,
			AvgBufferHealth: avgBufferHealth,
			BufferDryCount:  bufferDryCount,
			QualityTier:     qualityTier,
		}
		if sampleIssues.Valid {
			record.SampleIssues = sampleIssues.String
		}
		if avgFrameJitterMs.Valid {
			v := float32(avgFrameJitterMs.Float64)
			record.AvgFrameJitterMs = &v
		}
		if maxFrameJitterMs.Valid {
			v := float32(maxFrameJitterMs.Float64)
			record.MaxFrameJitterMs = &v
		}

		records = append(records, record)
	}

	resultsLen := len(records)
	if resultsLen > params.Limit {
		records = records[:params.Limit]
	}

	if params.Direction == pagination.Backward {
		slices.Reverse(records)
	}

	total := <-countCh

	var startCursor, endCursor string
	if len(records) > 0 {
		startCursor = pagination.EncodeCursor(records[0].Timestamp.AsTime(), records[0].Id)
		endCursor = pagination.EncodeCursor(records[len(records)-1].Timestamp.AsTime(), records[len(records)-1].Id)
	}

	return &pb.GetStreamHealth5MResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Records:    records,
	}, nil
}

// ============================================================================
// Node Performance 5-Minute Aggregates
// ============================================================================

// GetNodePerformance5M returns 5-minute aggregated node performance from node_performance_5m MV
func (s *PeriscopeServer) GetNodePerformance5M(ctx context.Context, req *pb.GetNodePerformance5MRequest) (*pb.GetNodePerformance5MResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	params, err := getCursorPagination(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	nodeID := req.GetNodeId()

	// Count query
	countQuery := `SELECT count(*) FROM node_performance_5m WHERE tenant_id = ? AND timestamp_5m >= ? AND timestamp_5m <= ?`
	countArgs := []interface{}{tenantID, startTime, endTime}
	if nodeID != "" {
		countQuery += " AND node_id = ?"
		countArgs = append(countArgs, nodeID)
	}
	countCh := s.countAsync(ctx, countQuery, countArgs...)

	query := `
		SELECT timestamp_5m, cluster_id, node_id, avg_cpu, max_cpu, avg_memory, max_memory,
		       total_bandwidth, avg_streams, max_streams
		FROM node_performance_5m
		WHERE tenant_id = ? AND timestamp_5m >= ? AND timestamp_5m <= ?
	`
	args := []interface{}{tenantID, startTime, endTime}

	if nodeID != "" {
		query += " AND node_id = ?"
		args = append(args, nodeID)
	}

	keysetCond, keysetArgs := buildKeysetCondition(params, "timestamp_5m", "concat(cluster_id, ':', node_id)")
	if keysetCond != "" {
		query += keysetCond
		args = append(args, keysetArgs...)
	}

	query += buildOrderBy(params, "timestamp_5m", "concat(cluster_id, ':', node_id)")
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var records []*pb.NodePerformance5M
	for rows.Next() {
		var timestamp time.Time
		var clusterID string
		var nodeIDStr string
		var avgCPU, maxCPU, avgMemory, maxMemory, avgStreams float32
		var totalBandwidth int64
		var maxStreams int32

		err := rows.Scan(&timestamp, &clusterID, &nodeIDStr, &avgCPU, &maxCPU, &avgMemory, &maxMemory,
			&totalBandwidth, &avgStreams, &maxStreams)
		if err != nil {
			s.logger.WithError(err).Error("Failed to scan node_performance_5m row")
			continue
		}

		records = append(records, &pb.NodePerformance5M{
			Id:             fmt.Sprintf("%s:%s", clusterID, nodeIDStr),
			Timestamp:      timestamppb.New(timestamp),
			NodeId:         nodeIDStr,
			AvgCpu:         avgCPU,
			MaxCpu:         maxCPU,
			AvgMemory:      avgMemory,
			MaxMemory:      maxMemory,
			TotalBandwidth: totalBandwidth,
			AvgStreams:     int32(avgStreams),
			MaxStreams:     maxStreams,
		})
	}

	resultsLen := len(records)
	if resultsLen > params.Limit {
		records = records[:params.Limit]
	}

	if params.Direction == pagination.Backward {
		slices.Reverse(records)
	}

	total := <-countCh

	var startCursor, endCursor string
	if len(records) > 0 {
		startCursor = pagination.EncodeCursor(records[0].Timestamp.AsTime(), records[0].Id)
		endCursor = pagination.EncodeCursor(records[len(records)-1].Timestamp.AsTime(), records[len(records)-1].Id)
	}

	return &pb.GetNodePerformance5MResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Records:    records,
	}, nil
}

// ============================================================================
// Viewer Hours Hourly Aggregates
// ============================================================================

// GetViewerHoursHourly returns hourly viewer hours aggregates from viewer_hours_hourly MV
func (s *PeriscopeServer) GetViewerHoursHourly(ctx context.Context, req *pb.GetViewerHoursHourlyRequest) (*pb.GetViewerHoursHourlyResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	params, err := getCursorPagination(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	streamID := req.GetStreamId()

	// Count query
	countQuery := `SELECT count(*) FROM viewer_hours_hourly WHERE tenant_id = ? AND hour >= ? AND hour <= ?`
	countArgs := []interface{}{tenantID, startTime, endTime}
	if streamID != "" {
		countQuery += " AND stream_id = ?"
		countArgs = append(countArgs, streamID)
	}
	countCh := s.countAsync(ctx, countQuery, countArgs...)

	// Use finalizeAggregation for AggregateFunction columns
	query := `
		SELECT hour, tenant_id, stream_id, country_code,
		       finalizeAggregation(unique_viewers) as unique_viewers,
		       finalizeAggregation(total_session_seconds) as total_session_seconds,
		       finalizeAggregation(total_bytes) as total_bytes
		FROM viewer_hours_hourly
		WHERE tenant_id = ? AND hour >= ? AND hour <= ?
	`
	args := []interface{}{tenantID, startTime, endTime}

	if streamID != "" {
		query += " AND stream_id = ?"
		args = append(args, streamID)
	}

	keysetCond, keysetArgs := buildKeysetCondition(params, "hour", "concat(stream_id, ':', country_code)")
	if keysetCond != "" {
		query += keysetCond
		args = append(args, keysetArgs...)
	}

	query += buildOrderBy(params, "hour", "concat(stream_id, ':', country_code)")
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var records []*pb.ViewerHoursHourly
	for rows.Next() {
		var hour time.Time
		var tenantIDStr, streamIDStr, countryCode string
		var uniqueViewers int32
		var totalSessionSeconds, totalBytes int64

		err := rows.Scan(&hour, &tenantIDStr, &streamIDStr, &countryCode,
			&uniqueViewers, &totalSessionSeconds, &totalBytes)
		if err != nil {
			s.logger.WithError(err).Error("Failed to scan viewer_hours_hourly row")
			continue
		}

		records = append(records, &pb.ViewerHoursHourly{
			Id:                  fmt.Sprintf("%s_%s_%s", hour.Format(time.RFC3339), streamIDStr, countryCode),
			Hour:                timestamppb.New(hour),
			TenantId:            tenantIDStr,
			StreamId:            streamIDStr,
			CountryCode:         countryCode,
			UniqueViewers:       uniqueViewers,
			TotalSessionSeconds: totalSessionSeconds,
			TotalBytes:          totalBytes,
		})
	}

	resultsLen := len(records)
	if resultsLen > params.Limit {
		records = records[:params.Limit]
	}

	if params.Direction == pagination.Backward {
		slices.Reverse(records)
	}

	total := <-countCh

	var startCursor, endCursor string
	if len(records) > 0 {
		startCursor = pagination.EncodeCursor(records[0].Hour.AsTime(), fmt.Sprintf("%s:%s", records[0].StreamId, records[0].CountryCode))
		endCursor = pagination.EncodeCursor(records[len(records)-1].Hour.AsTime(), fmt.Sprintf("%s:%s", records[len(records)-1].StreamId, records[len(records)-1].CountryCode))
	}

	return &pb.GetViewerHoursHourlyResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Records:    records,
	}, nil
}

// ============================================================================
// Viewer Geographic Hourly Aggregates
// ============================================================================

// GetViewerGeoHourly returns hourly geographic breakdown from viewer_geo_hourly MV
func (s *PeriscopeServer) GetViewerGeoHourly(ctx context.Context, req *pb.GetViewerGeoHourlyRequest) (*pb.GetViewerGeoHourlyResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	params, err := getCursorPagination(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	// Note: viewer_geo_hourly doesn't have stream filter - it's tenant-wide geographic breakdown

	// Count query
	countQuery := `SELECT count(*) FROM viewer_geo_hourly WHERE tenant_id = ? AND hour >= ? AND hour <= ?`
	countCh := s.countAsync(ctx, countQuery, tenantID, startTime, endTime)

	query := `
		SELECT hour, tenant_id, country_code, viewer_count, viewer_hours, egress_gb
		FROM viewer_geo_hourly
		WHERE tenant_id = ? AND hour >= ? AND hour <= ?
	`
	args := []interface{}{tenantID, startTime, endTime}

	keysetCond, keysetArgs := buildKeysetCondition(params, "hour", "country_code")
	if keysetCond != "" {
		query += keysetCond
		args = append(args, keysetArgs...)
	}

	query += buildOrderBy(params, "hour", "country_code")
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var records []*pb.ViewerGeoHourly
	for rows.Next() {
		var hour time.Time
		var tenantIDStr, countryCode string
		var viewerCount int32
		var viewerHours, egressGB float64

		err := rows.Scan(&hour, &tenantIDStr, &countryCode, &viewerCount, &viewerHours, &egressGB)
		if err != nil {
			s.logger.WithError(err).Error("Failed to scan viewer_geo_hourly row")
			continue
		}

		records = append(records, &pb.ViewerGeoHourly{
			Id:          fmt.Sprintf("%s_%s", hour.Format(time.RFC3339), countryCode),
			Hour:        timestamppb.New(hour),
			TenantId:    tenantIDStr,
			CountryCode: countryCode,
			ViewerCount: viewerCount,
			ViewerHours: viewerHours,
			EgressGb:    egressGB,
		})
	}

	resultsLen := len(records)
	if resultsLen > params.Limit {
		records = records[:params.Limit]
	}

	if params.Direction == pagination.Backward {
		slices.Reverse(records)
	}

	total := <-countCh

	var startCursor, endCursor string
	if len(records) > 0 {
		startCursor = pagination.EncodeCursor(records[0].Hour.AsTime(), records[0].CountryCode)
		endCursor = pagination.EncodeCursor(records[len(records)-1].Hour.AsTime(), records[len(records)-1].CountryCode)
	}

	return &pb.GetViewerGeoHourlyResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Records:    records,
	}, nil
}

// ============================================================================
// Tenant Daily Stats
// ============================================================================

// GetTenantDailyStats returns daily tenant statistics from tenant_viewer_daily for PlatformOverview.dailyStats
func (s *PeriscopeServer) GetTenantDailyStats(ctx context.Context, req *pb.GetTenantDailyStatsRequest) (*pb.GetTenantDailyStatsResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	days := req.GetDays()
	if days <= 0 {
		days = 7 // default to 7 days
	}
	if days > 90 {
		days = 90 // max 90 days
	}

	query := `
		SELECT d.day, d.tenant_id, d.viewer_hours, d.unique_viewers, d.unique_viewers AS total_sessions, d.egress_gb,
		       COALESCE(a.total_views, 0) AS total_views
		FROM tenant_viewer_daily d
		LEFT JOIN tenant_analytics_daily a
		  ON d.tenant_id = a.tenant_id AND d.day = a.day
		WHERE d.tenant_id = ? AND d.day >= today() - ?
		ORDER BY d.day DESC
		LIMIT ?
	`

	rows, err := s.clickhouse.QueryContext(ctx, query, tenantID, days, days)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var stats []*pb.TenantDailyStat
	for rows.Next() {
		var day time.Time
		var tenantIDStr string
		var viewerHours, egressGB float64
		var uniqueViewers, totalSessions int32
		var totalViews int64

		err := rows.Scan(&day, &tenantIDStr, &viewerHours, &uniqueViewers, &totalSessions, &egressGB, &totalViews)
		if err != nil {
			s.logger.WithError(err).Error("Failed to scan tenant_viewer_daily row")
			continue
		}

		stats = append(stats, &pb.TenantDailyStat{
			Id:            fmt.Sprintf("%s_%s", day.Format("2006-01-02"), tenantIDStr),
			Date:          timestamppb.New(day),
			TenantId:      tenantIDStr,
			EgressGb:      egressGB,
			ViewerHours:   viewerHours,
			UniqueViewers: uniqueViewers,
			TotalSessions: totalSessions,
			TotalViews:    totalViews,
		})
	}

	return &pb.GetTenantDailyStatsResponse{
		Stats: stats,
	}, nil
}

// ============================================================================
// Processing Usage Queries (from processing_events and processing_daily tables)
// ============================================================================

// GetProcessingUsage returns processing usage records and/or daily summaries for billing
func (s *PeriscopeServer) GetProcessingUsage(ctx context.Context, req *pb.GetProcessingUsageRequest) (*pb.GetProcessingUsageResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	params, err := getCursorPagination(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	streamID := req.GetStreamId()
	processType := req.GetProcessType()
	summaryOnly := req.GetSummaryOnly()

	response := &pb.GetProcessingUsageResponse{}

	// Always get daily summaries with per-codec breakdown
	summaryQuery := `
		SELECT day, tenant_id,
		       livepeer_seconds, livepeer_segment_count, livepeer_unique_streams,
		       livepeer_h264_seconds, livepeer_vp9_seconds, livepeer_av1_seconds, livepeer_hevc_seconds,
		       native_av_seconds, native_av_segment_count, native_av_unique_streams,
		       native_av_h264_seconds, native_av_vp9_seconds, native_av_av1_seconds, native_av_hevc_seconds,
		       native_av_aac_seconds, native_av_opus_seconds,
		       audio_seconds, video_seconds
		FROM processing_daily
		WHERE tenant_id = ? AND day >= toDate(?) AND day <= toDate(?)
		ORDER BY day DESC
	`
	summaryRows, err := s.clickhouse.QueryContext(ctx, summaryQuery, tenantID, startTime, endTime)
	if err != nil {
		s.logger.WithError(err).Error("Failed to query processing_daily")
	} else {
		defer summaryRows.Close()
		for summaryRows.Next() {
			var day time.Time
			var tenantIDStr string
			var livepeerSeconds, nativeAvSeconds float64
			var livepeerSegmentCount, nativeAvSegmentCount uint64
			var livepeerUniqueStreams, nativeAvUniqueStreams uint32
			// Per-codec fields
			var livepeerH264, livepeerVp9, livepeerAv1, livepeerHevc float64
			var nativeAvH264, nativeAvVp9, nativeAvAv1, nativeAvHevc float64
			var nativeAvAac, nativeAvOpus float64
			var audioSeconds, videoSeconds float64

			err := summaryRows.Scan(&day, &tenantIDStr,
				&livepeerSeconds, &livepeerSegmentCount, &livepeerUniqueStreams,
				&livepeerH264, &livepeerVp9, &livepeerAv1, &livepeerHevc,
				&nativeAvSeconds, &nativeAvSegmentCount, &nativeAvUniqueStreams,
				&nativeAvH264, &nativeAvVp9, &nativeAvAv1, &nativeAvHevc,
				&nativeAvAac, &nativeAvOpus,
				&audioSeconds, &videoSeconds)
			if err != nil {
				s.logger.WithError(err).Error("Failed to scan processing_daily row")
				continue
			}

			response.Summaries = append(response.Summaries, &pb.ProcessingUsageSummary{
				Date:                  timestamppb.New(day),
				TenantId:              tenantIDStr,
				LivepeerSeconds:       livepeerSeconds,
				LivepeerSegmentCount:  livepeerSegmentCount,
				LivepeerUniqueStreams: livepeerUniqueStreams,
				LivepeerH264Seconds:   livepeerH264,
				LivepeerVp9Seconds:    livepeerVp9,
				LivepeerAv1Seconds:    livepeerAv1,
				LivepeerHevcSeconds:   livepeerHevc,
				NativeAvSeconds:       nativeAvSeconds,
				NativeAvSegmentCount:  nativeAvSegmentCount,
				NativeAvUniqueStreams: nativeAvUniqueStreams,
				NativeAvH264Seconds:   nativeAvH264,
				NativeAvVp9Seconds:    nativeAvVp9,
				NativeAvAv1Seconds:    nativeAvAv1,
				NativeAvHevcSeconds:   nativeAvHevc,
				NativeAvAacSeconds:    nativeAvAac,
				NativeAvOpusSeconds:   nativeAvOpus,
				AudioSeconds:          audioSeconds,
				VideoSeconds:          videoSeconds,
			})
		}
	}

	// Return early if only summaries are requested
	if summaryOnly {
		return response, nil
	}

	// Build count query for detailed records
	countQuery := `SELECT count(*) FROM processing_events WHERE tenant_id = ? AND timestamp >= ? AND timestamp <= ?`
	countArgs := []interface{}{tenantID, startTime, endTime}
	if streamID != "" {
		countQuery += " AND stream_id = ?"
		countArgs = append(countArgs, streamID)
	}
	if processType != "" {
		countQuery += " AND process_type = ?"
		countArgs = append(countArgs, processType)
	}
	countCh := s.countAsync(ctx, countQuery, countArgs...)

	// Query detailed records - includes all Livepeer and MistProcAV fields
	query := `
		SELECT timestamp, tenant_id, node_id, stream_id, process_type, duration_ms,
		       input_codec, output_codec, track_type,
		       -- Livepeer fields
		       segment_number, width, height, rendition_count, broadcaster_url, upload_time_us,
		       livepeer_session_id, segment_start_ms, input_bytes, output_bytes_total,
		       attempt_count, turnaround_ms, speed_factor, renditions_json,
		       -- MistProcAV cumulative
		       input_frames, output_frames, decode_us_per_frame, transform_us_per_frame, encode_us_per_frame, is_final,
		       -- MistProcAV delta
		       input_frames_delta, output_frames_delta, input_bytes_delta, output_bytes_delta,
		       -- MistProcAV dimensions
		       input_width, input_height, output_width, output_height,
		       -- MistProcAV frame/audio
		       input_fpks, output_fps_measured, sample_rate, channels,
		       -- MistProcAV timing
		       source_timestamp_ms, sink_timestamp_ms, source_advanced_ms, sink_advanced_ms,
		       -- MistProcAV performance
		       rtf_in, rtf_out, pipeline_lag_ms, output_bitrate_bps
		FROM processing_events
		WHERE tenant_id = ? AND timestamp >= ? AND timestamp <= ?
	`
	args := []interface{}{tenantID, startTime, endTime}

	if streamID != "" {
		query += " AND stream_id = ?"
		args = append(args, streamID)
	}
	if processType != "" {
		query += " AND process_type = ?"
		args = append(args, processType)
	}

	var cursorParts []string
	if params.Cursor != nil {
		cursorParts = strings.SplitN(params.Cursor.ID, "|", 3)
	}
	keysetCond, keysetArgs, err := buildKeysetConditionN(params, "timestamp", []string{"stream_id", "node_id", "process_type"}, cursorParts)
	if err != nil {
		return nil, err
	}
	if keysetCond != "" {
		query += keysetCond
		args = append(args, keysetArgs...)
	}

	query += buildOrderByN(params, "timestamp", []string{"stream_id", "node_id", "process_type"})
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var records []*pb.ProcessingUsageRecord
	for rows.Next() {
		var timestamp time.Time
		var tenantIDStr, nodeID, streamIDStr, processTypeStr string
		var durationMs int64
		// Common fields
		var inputCodec, outputCodec, trackType *string
		// Livepeer fields
		var segmentNumber, width, height, renditionCount, attemptCount *int32
		var uploadTimeUs, segmentStartMs, inputBytes, outputBytesTotal, turnaroundMs *int64
		var speedFactor *float64
		var livepeerSessionID, broadcasterURL, renditionsJSON *string
		// MistProcAV cumulative
		var inputFrames, outputFrames, decodeUs, transformUs, encodeUs *int64
		var isFinal *uint8
		// MistProcAV delta
		var inputFramesDelta, outputFramesDelta, inputBytesDelta, outputBytesDelta *int64
		// MistProcAV dimensions
		var inputWidth, inputHeight, outputWidth, outputHeight *int32
		// MistProcAV frame/audio
		var inputFpks, sampleRate, channels *int32
		var outputFpsMeasured *float64
		// MistProcAV timing
		var sourceTimestampMs, sinkTimestampMs, sourceAdvancedMs, sinkAdvancedMs *int64
		// MistProcAV performance
		var rtfIn, rtfOut *float64
		var pipelineLagMs, outputBitrateBps *int64

		err := rows.Scan(&timestamp, &tenantIDStr, &nodeID, &streamIDStr, &processTypeStr, &durationMs,
			&inputCodec, &outputCodec, &trackType,
			// Livepeer fields
			&segmentNumber, &width, &height, &renditionCount, &broadcasterURL, &uploadTimeUs,
			&livepeerSessionID, &segmentStartMs, &inputBytes, &outputBytesTotal,
			&attemptCount, &turnaroundMs, &speedFactor, &renditionsJSON,
			// MistProcAV cumulative
			&inputFrames, &outputFrames, &decodeUs, &transformUs, &encodeUs, &isFinal,
			// MistProcAV delta
			&inputFramesDelta, &outputFramesDelta, &inputBytesDelta, &outputBytesDelta,
			// MistProcAV dimensions
			&inputWidth, &inputHeight, &outputWidth, &outputHeight,
			// MistProcAV frame/audio
			&inputFpks, &outputFpsMeasured, &sampleRate, &channels,
			// MistProcAV timing
			&sourceTimestampMs, &sinkTimestampMs, &sourceAdvancedMs, &sinkAdvancedMs,
			// MistProcAV performance
			&rtfIn, &rtfOut, &pipelineLagMs, &outputBitrateBps)
		if err != nil {
			s.logger.WithError(err).Error("Failed to scan processing_events row")
			continue
		}

		record := &pb.ProcessingUsageRecord{
			Id:          fmt.Sprintf("%s_%s", timestamp.Format(time.RFC3339Nano), streamIDStr),
			Timestamp:   timestamppb.New(timestamp),
			TenantId:    tenantIDStr,
			NodeId:      nodeID,
			StreamId:    streamIDStr,
			ProcessType: processTypeStr,
			DurationMs:  durationMs,
			// Common fields
			InputCodec:  inputCodec,
			OutputCodec: outputCodec,
			TrackType:   trackType,
			// Livepeer fields
			SegmentNumber:     segmentNumber,
			Width:             width,
			Height:            height,
			RenditionCount:    renditionCount,
			BroadcasterUrl:    broadcasterURL,
			UploadTimeUs:      uploadTimeUs,
			LivepeerSessionId: livepeerSessionID,
			SegmentStartMs:    segmentStartMs,
			InputBytes:        inputBytes,
			OutputBytesTotal:  outputBytesTotal,
			AttemptCount:      attemptCount,
			TurnaroundMs:      turnaroundMs,
			SpeedFactor:       speedFactor,
			RenditionsJson:    renditionsJSON,
			// MistProcAV cumulative
			InputFrames:         inputFrames,
			OutputFrames:        outputFrames,
			DecodeUsPerFrame:    decodeUs,
			TransformUsPerFrame: transformUs,
			EncodeUsPerFrame:    encodeUs,
			// MistProcAV delta
			InputFramesDelta:  inputFramesDelta,
			OutputFramesDelta: outputFramesDelta,
			InputBytesDelta:   inputBytesDelta,
			OutputBytesDelta:  outputBytesDelta,
			// MistProcAV dimensions
			InputWidth:   inputWidth,
			InputHeight:  inputHeight,
			OutputWidth:  outputWidth,
			OutputHeight: outputHeight,
			// MistProcAV frame/audio
			InputFpks:         inputFpks,
			OutputFpsMeasured: outputFpsMeasured,
			SampleRate:        sampleRate,
			Channels:          channels,
			// MistProcAV timing
			SourceTimestampMs: sourceTimestampMs,
			SinkTimestampMs:   sinkTimestampMs,
			SourceAdvancedMs:  sourceAdvancedMs,
			SinkAdvancedMs:    sinkAdvancedMs,
			// MistProcAV performance
			RtfIn:            rtfIn,
			RtfOut:           rtfOut,
			PipelineLagMs:    pipelineLagMs,
			OutputBitrateBps: outputBitrateBps,
		}

		// Handle is_final separately (UInt8 → bool conversion)
		if isFinal != nil {
			isFinalBool := *isFinal == 1
			record.IsFinal = &isFinalBool
		}

		records = append(records, record)
	}

	resultsLen := len(records)
	if resultsLen > params.Limit {
		records = records[:params.Limit]
	}

	if params.Direction == pagination.Backward {
		slices.Reverse(records)
	}

	total := <-countCh

	var startCursor, endCursor string
	if len(records) > 0 {
		startCursor = pagination.EncodeCursor(records[0].Timestamp.AsTime(), records[0].StreamId+"|"+records[0].NodeId+"|"+records[0].ProcessType)
		endCursor = pagination.EncodeCursor(records[len(records)-1].Timestamp.AsTime(), records[len(records)-1].StreamId+"|"+records[len(records)-1].NodeId+"|"+records[len(records)-1].ProcessType)
	}

	response.Pagination = buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor)
	response.Records = records

	return response, nil
}

// GetLiveUsageSummary returns a near-real-time usage summary for billing dashboards.
// Structure matches UsageSummary for consistency between live and finalized invoices.
func (s *PeriscopeServer) GetLiveUsageSummary(ctx context.Context, req *pb.GetLiveUsageSummaryRequest) (*pb.GetLiveUsageSummaryResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	summary := &pb.LiveUsageSummary{
		TenantId:    tenantID,
		PeriodStart: timestamppb.New(startTime),
		PeriodEnd:   timestamppb.New(endTime),
	}

	// Stream metrics from stream_event_log (max_viewers, total_streams, stream_hours)
	var maxViewers, totalStreams int32
	var streamHours float64
	err = s.clickhouse.QueryRowContext(ctx, `
		SELECT
			COALESCE(max(total_viewers), 0) AS max_viewers,
			COALESCE(uniq(internal_name), 0) AS total_streams,
			COALESCE(countDistinct(concat(internal_name, toString(toStartOfHour(timestamp)))), 0) AS stream_hours
		FROM stream_event_log
		WHERE tenant_id = ?
		  AND timestamp BETWEEN ? AND ?
		  AND total_viewers IS NOT NULL
	`, tenantID, startTime, endTime).Scan(&maxViewers, &totalStreams, &streamHours)
	if err != nil && !errors.Is(err, database.ErrNoRows) {
		s.logger.WithError(err).Warn("Failed to query stream_event_log for live usage")
	}
	summary.MaxViewers = maxViewers
	summary.TotalStreams = totalStreams
	summary.StreamHours = streamHours

	// Viewer hours + egress + unique viewers (tenant_usage_5m for real-time data)
	var totalSessionSeconds uint64
	var totalBytes uint64
	var uniqueViewers uint32
	err = s.clickhouse.QueryRowContext(ctx, `
		SELECT
			sumMerge(total_session_seconds) AS total_session_seconds,
			sumMerge(total_bytes) AS total_bytes,
			toUInt32(uniqMerge(unique_viewers)) AS unique_viewers
		FROM tenant_usage_5m
		WHERE tenant_id = ?
		  AND timestamp_5m >= ?
		  AND timestamp_5m <= ?
	`, tenantID, startTime, endTime).Scan(&totalSessionSeconds, &totalBytes, &uniqueViewers)
	if err != nil && err != database.ErrNoRows {
		s.logger.WithError(err).Warn("Failed to query tenant_usage_5m for live usage")
	}
	summary.ViewerHours = float64(totalSessionSeconds) / 3600.0
	summary.EgressGb = float64(totalBytes) / (1024 * 1024 * 1024)
	summary.UniqueUsers = int32(uniqueViewers)
	summary.TotalViewers = int32(uniqueViewers) // total_viewers = unique viewers for live usage

	// Peak bandwidth from client_qoe_5m (avg_bw_out is bytes/sec, convert to Mbps)
	var peakBandwidthBytes float64
	err = s.clickhouse.QueryRowContext(ctx, `
		SELECT COALESCE(max(avg_bw_out), 0) AS peak_bandwidth
		FROM client_qoe_5m
		WHERE tenant_id = ?
		  AND timestamp_5m BETWEEN ? AND ?
	`, tenantID, startTime, endTime).Scan(&peakBandwidthBytes)
	if err != nil && !errors.Is(err, database.ErrNoRows) {
		s.logger.WithError(err).Warn("Failed to query client_qoe_5m for peak bandwidth")
	}
	summary.PeakBandwidthMbps = peakBandwidthBytes / (1024 * 1024) // bytes/sec to Mbps

	// Average storage (storage_usage_hourly)
	var avgTotalBytes uint64
	err = s.clickhouse.QueryRowContext(ctx, `
		SELECT COALESCE(toUInt64(avgMerge(avg_total_bytes)), 0) AS avg_total_bytes
		FROM storage_usage_hourly
		WHERE tenant_id = ?
		  AND hour >= ?
		  AND hour <= ?
	`, tenantID, startTime, endTime).Scan(&avgTotalBytes)
	if err != nil && err != database.ErrNoRows {
		s.logger.WithError(err).Warn("Failed to query storage_usage_hourly")
	}
	summary.AverageStorageGb = float64(avgTotalBytes) / (1024 * 1024 * 1024)

	// Per-codec processing breakdown + segment counts (processing_daily)
	var livepeerH264, livepeerVp9, livepeerAv1, livepeerHevc float64
	var nativeAvH264, nativeAvVp9, nativeAvAv1, nativeAvHevc float64
	var nativeAvAac, nativeAvOpus float64
	var livepeerSegmentCount, nativeAvSegmentCount uint64
	var livepeerUniqueStreams, nativeAvUniqueStreams uint32
	err = s.clickhouse.QueryRowContext(ctx, `
		SELECT
			COALESCE(sum(livepeer_h264_seconds), 0) AS livepeer_h264,
			COALESCE(sum(livepeer_vp9_seconds), 0) AS livepeer_vp9,
			COALESCE(sum(livepeer_av1_seconds), 0) AS livepeer_av1,
			COALESCE(sum(livepeer_hevc_seconds), 0) AS livepeer_hevc,
			COALESCE(sum(native_av_h264_seconds), 0) AS native_av_h264,
			COALESCE(sum(native_av_vp9_seconds), 0) AS native_av_vp9,
			COALESCE(sum(native_av_av1_seconds), 0) AS native_av_av1,
			COALESCE(sum(native_av_hevc_seconds), 0) AS native_av_hevc,
			COALESCE(sum(native_av_aac_seconds), 0) AS native_av_aac,
			COALESCE(sum(native_av_opus_seconds), 0) AS native_av_opus,
			COALESCE(sum(livepeer_segment_count), 0) AS livepeer_segment_count,
			COALESCE(sum(native_av_segment_count), 0) AS native_av_segment_count,
			COALESCE(max(livepeer_unique_streams), 0) AS livepeer_unique_streams,
			COALESCE(max(native_av_unique_streams), 0) AS native_av_unique_streams
		FROM processing_daily
		WHERE tenant_id = ?
		  AND day BETWEEN toDate(?) AND toDate(?)
	`, tenantID, startTime, endTime).Scan(
		&livepeerH264, &livepeerVp9, &livepeerAv1, &livepeerHevc,
		&nativeAvH264, &nativeAvVp9, &nativeAvAv1, &nativeAvHevc,
		&nativeAvAac, &nativeAvOpus,
		&livepeerSegmentCount, &nativeAvSegmentCount,
		&livepeerUniqueStreams, &nativeAvUniqueStreams,
	)
	if err != nil && !errors.Is(err, database.ErrNoRows) {
		s.logger.WithError(err).Warn("Failed to query processing_daily for per-codec breakdown")
	}
	summary.LivepeerH264Seconds = livepeerH264
	summary.LivepeerVp9Seconds = livepeerVp9
	summary.LivepeerAv1Seconds = livepeerAv1
	summary.LivepeerHevcSeconds = livepeerHevc
	summary.NativeAvH264Seconds = nativeAvH264
	summary.NativeAvVp9Seconds = nativeAvVp9
	summary.NativeAvAv1Seconds = nativeAvAv1
	summary.NativeAvHevcSeconds = nativeAvHevc
	summary.NativeAvAacSeconds = nativeAvAac
	summary.NativeAvOpusSeconds = nativeAvOpus
	summary.LivepeerSegmentCount = livepeerSegmentCount
	summary.NativeAvSegmentCount = nativeAvSegmentCount
	summary.LivepeerUniqueStreams = livepeerUniqueStreams
	summary.NativeAvUniqueStreams = nativeAvUniqueStreams

	// Geographic breakdown (viewer_geo_hourly)
	var uniqueCountries, uniqueCities int32
	err = s.clickhouse.QueryRowContext(ctx, `
		SELECT
			COALESCE(uniq(country_code), 0) AS unique_countries,
			COALESCE(uniq(city), 0) AS unique_cities
		FROM viewer_connection_events
		WHERE tenant_id = ?
		  AND timestamp BETWEEN ? AND ?
	`, tenantID, startTime, endTime).Scan(&uniqueCountries, &uniqueCities)
	if err != nil && !errors.Is(err, database.ErrNoRows) {
		s.logger.WithError(err).Warn("Failed to query viewer_connection_events for geo stats")
	}
	summary.UniqueCountries = uniqueCountries
	summary.UniqueCities = uniqueCities

	// Geo breakdown by country (top 20)
	rows, err := s.clickhouse.QueryContext(ctx, `
		SELECT
			country_code,
			toInt32(sum(viewer_count)) AS viewer_count,
			sum(viewer_hours) AS viewer_hours,
			sum(egress_gb) AS egress_gb
		FROM viewer_geo_hourly
		WHERE tenant_id = ?
		  AND hour >= ?
		  AND hour <= ?
		GROUP BY country_code
		ORDER BY viewer_hours DESC
		LIMIT 20
	`, tenantID, startTime, endTime)
	if err != nil && err != database.ErrNoRows {
		s.logger.WithError(err).Warn("Failed to query viewer_geo_hourly for geo breakdown")
	} else if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var countryCode string
			var viewerCount int32
			var viewerHours, egressGb float64
			if scanErr := rows.Scan(&countryCode, &viewerCount, &viewerHours, &egressGb); scanErr != nil {
				s.logger.WithError(err).Warn("Failed to scan geo breakdown row")
				continue
			}
			summary.GeoBreakdown = append(summary.GeoBreakdown, &pb.CountryMetric{
				CountryCode: countryCode,
				ViewerCount: viewerCount,
				ViewerHours: viewerHours,
				EgressGb:    egressGb,
			})
		}
	}

	// Storage lifecycle - artifact counts (from artifact_events)
	var clipsCreated, clipsDeleted, dvrCreated, dvrDeleted, vodCreated, vodDeleted uint32
	err = s.clickhouse.QueryRowContext(ctx, `
		SELECT
			countIf(content_type = 'clip' AND stage = 'completed') AS clips_created,
			countIf(content_type = 'clip' AND stage = 'deleted') AS clips_deleted,
			countIf(content_type = 'dvr' AND stage = 'completed') AS dvr_created,
			countIf(content_type = 'dvr' AND stage = 'deleted') AS dvr_deleted,
			countIf(content_type = 'vod' AND stage = 'completed') AS vod_created,
			countIf(content_type = 'vod' AND stage = 'deleted') AS vod_deleted
		FROM artifact_events
		WHERE tenant_id = ? AND timestamp BETWEEN ? AND ?
	`, tenantID, startTime, endTime).Scan(
		&clipsCreated, &clipsDeleted, &dvrCreated, &dvrDeleted, &vodCreated, &vodDeleted,
	)
	if err != nil && !errors.Is(err, database.ErrNoRows) {
		s.logger.WithError(err).Warn("Failed to query artifact_events for storage lifecycle")
	}
	summary.ClipsCreated = clipsCreated
	summary.ClipsDeleted = clipsDeleted
	summary.DvrCreated = dvrCreated
	summary.DvrDeleted = dvrDeleted
	summary.VodCreated = vodCreated
	summary.VodDeleted = vodDeleted

	// Storage breakdown from latest snapshot (hot + cold)
	var hotClipBytes, hotDvrBytes, hotVodBytes uint64
	err = s.clickhouse.QueryRowContext(ctx, `
		SELECT
			COALESCE(argMax(clip_bytes, timestamp), 0),
			COALESCE(argMax(dvr_bytes, timestamp), 0),
			COALESCE(argMax(vod_bytes, timestamp), 0)
		FROM storage_snapshots
		WHERE tenant_id = ? AND storage_scope = 'hot' AND timestamp <= ?
	`, tenantID, endTime).Scan(
		&hotClipBytes, &hotDvrBytes, &hotVodBytes,
	)
	if err != nil && !errors.Is(err, database.ErrNoRows) {
		s.logger.WithError(err).Warn("Failed to query hot storage_snapshots for storage breakdown")
	}

	var coldFrozenClipBytes, coldFrozenDvrBytes, coldFrozenVodBytes uint64
	err = s.clickhouse.QueryRowContext(ctx, `
		SELECT
			COALESCE(argMax(frozen_clip_bytes, timestamp), 0),
			COALESCE(argMax(frozen_dvr_bytes, timestamp), 0),
			COALESCE(argMax(frozen_vod_bytes, timestamp), 0)
		FROM storage_snapshots
		WHERE tenant_id = ? AND storage_scope = 'cold' AND timestamp <= ?
	`, tenantID, endTime).Scan(
		&coldFrozenClipBytes, &coldFrozenDvrBytes, &coldFrozenVodBytes,
	)
	if err != nil && !errors.Is(err, database.ErrNoRows) {
		s.logger.WithError(err).Warn("Failed to query cold storage_snapshots for storage breakdown")
	}

	summary.ClipBytes = hotClipBytes
	summary.DvrBytes = hotDvrBytes
	summary.VodBytes = hotVodBytes
	summary.FrozenClipBytes = coldFrozenClipBytes
	summary.FrozenDvrBytes = coldFrozenDvrBytes
	summary.FrozenVodBytes = coldFrozenVodBytes

	// Sync/cache operations (from storage_events)
	var freezeCount, defrostCount uint32
	var freezeBytes, defrostBytes uint64
	err = s.clickhouse.QueryRowContext(ctx, `
			SELECT
				countIf(action = 'synced') AS freeze_count,
				sumIf(size_bytes, action = 'synced') AS freeze_bytes,
				countIf(action = 'cached') AS defrost_count,
				sumIf(size_bytes, action = 'cached') AS defrost_bytes
			FROM storage_events
			WHERE tenant_id = ? AND timestamp BETWEEN ? AND ?
		`, tenantID, startTime, endTime).Scan(
		&freezeCount, &freezeBytes, &defrostCount, &defrostBytes,
	)
	if err != nil && !errors.Is(err, database.ErrNoRows) {
		s.logger.WithError(err).Warn("Failed to query storage_events for freeze/defrost operations")
	}
	summary.FreezeCount = freezeCount
	summary.FreezeBytes = freezeBytes
	summary.DefrostCount = defrostCount
	summary.DefrostBytes = defrostBytes

	return &pb.GetLiveUsageSummaryResponse{Summary: summary}, nil
}

// ============================================================================
// Rebuffering Events (from rebuffering_events table)
// ============================================================================

// GetRebufferingEvents returns buffer state transition events
func (s *PeriscopeServer) GetRebufferingEvents(ctx context.Context, req *pb.GetRebufferingEventsRequest) (*pb.GetRebufferingEventsResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	params, err := getCursorPagination(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	// Build count query
	countQuery := `SELECT count(*) FROM rebuffering_events WHERE tenant_id = ? AND timestamp >= ? AND timestamp <= ?`
	countArgs := []interface{}{tenantID, startTime, endTime}
	if streamID := req.GetStreamId(); streamID != "" {
		countQuery += ` AND stream_id = ?`
		countArgs = append(countArgs, streamID)
	}
	if req.NodeId != nil && *req.NodeId != "" {
		countQuery += ` AND node_id = ?`
		countArgs = append(countArgs, *req.NodeId)
	}
	countCh := s.countAsync(ctx, countQuery, countArgs...)

	// Build main query
	query := `
		SELECT timestamp, tenant_id, stream_id, node_id, buffer_state, prev_state, rebuffer_start, rebuffer_end
		FROM rebuffering_events
		WHERE tenant_id = ? AND timestamp >= ? AND timestamp <= ?
	`
	args := []interface{}{tenantID, startTime, endTime}

	if streamID := req.GetStreamId(); streamID != "" {
		query += ` AND stream_id = ?`
		args = append(args, streamID)
	}
	if req.NodeId != nil && *req.NodeId != "" {
		query += ` AND node_id = ?`
		args = append(args, *req.NodeId)
	}

	var cursorParts []string
	if params.Cursor != nil {
		cursorParts = strings.SplitN(params.Cursor.ID, "|", 2)
	}
	keysetCond, keysetArgs, err := buildKeysetConditionN(params, "timestamp", []string{"stream_id", "node_id"}, cursorParts)
	if err != nil {
		return nil, err
	}
	if keysetCond != "" {
		query += keysetCond
		args = append(args, keysetArgs...)
	}

	query += buildOrderByN(params, "timestamp", []string{"stream_id", "node_id"})
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var events []*pb.RebufferingEvent
	for rows.Next() {
		var timestamp, rebufferStart, rebufferEnd time.Time
		var tenantIDStr, streamIDStr, nodeID, bufferState, prevState string

		err := rows.Scan(&timestamp, &tenantIDStr, &streamIDStr, &nodeID, &bufferState, &prevState, &rebufferStart, &rebufferEnd)
		if err != nil {
			s.logger.WithError(err).Error("Failed to scan rebuffering_events row")
			continue
		}

		events = append(events, &pb.RebufferingEvent{
			Id:            fmt.Sprintf("%s_%s", timestamp.Format(time.RFC3339Nano), streamIDStr),
			Timestamp:     timestamppb.New(timestamp),
			TenantId:      tenantIDStr,
			StreamId:      streamIDStr,
			NodeId:        nodeID,
			BufferState:   bufferState,
			PrevState:     prevState,
			RebufferStart: timestamppb.New(rebufferStart),
			RebufferEnd:   timestamppb.New(rebufferEnd),
		})
	}

	resultsLen := len(events)
	if resultsLen > params.Limit {
		events = events[:params.Limit]
	}

	if params.Direction == pagination.Backward {
		slices.Reverse(events)
	}

	total := <-countCh

	var startCursor, endCursor string
	if len(events) > 0 {
		startCursor = pagination.EncodeCursor(events[0].Timestamp.AsTime(), events[0].StreamId+"|"+events[0].NodeId)
		endCursor = pagination.EncodeCursor(events[len(events)-1].Timestamp.AsTime(), events[len(events)-1].StreamId+"|"+events[len(events)-1].NodeId)
	}

	return &pb.GetRebufferingEventsResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Events:     events,
	}, nil
}

// ============================================================================
// Tenant Analytics Daily (from tenant_analytics_daily table)
// ============================================================================

// GetTenantAnalyticsDaily returns daily tenant-level analytics rollups
func (s *PeriscopeServer) GetTenantAnalyticsDaily(ctx context.Context, req *pb.GetTenantAnalyticsDailyRequest) (*pb.GetTenantAnalyticsDailyResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	params, err := getCursorPagination(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	// Count query
	countQuery := `SELECT count(*) FROM tenant_analytics_daily WHERE tenant_id = ? AND day >= toDate(?) AND day <= toDate(?)`
	countCh := s.countAsync(ctx, countQuery, tenantID, startTime, endTime)

	query := `
		SELECT day, tenant_id, total_streams, total_views, unique_viewers, egress_bytes
		FROM tenant_analytics_daily
		WHERE tenant_id = ? AND day >= toDate(?) AND day <= toDate(?)
	`
	args := []interface{}{tenantID, startTime, endTime}

	keysetCond, keysetArgs := buildKeysetConditionSingle(params, "day")
	if keysetCond != "" {
		query += keysetCond
		args = append(args, keysetArgs...)
	}

	query += buildOrderBySingle(params, "day")
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var records []*pb.TenantAnalyticsDaily
	for rows.Next() {
		var day time.Time
		var tenantIDStr string
		var totalStreams int32
		var totalViews int64
		var uniqueViewers int32
		var egressBytes int64

		err := rows.Scan(&day, &tenantIDStr, &totalStreams, &totalViews, &uniqueViewers, &egressBytes)
		if err != nil {
			s.logger.WithError(err).Error("Failed to scan tenant_analytics_daily row")
			continue
		}

		records = append(records, &pb.TenantAnalyticsDaily{
			Id:            fmt.Sprintf("%s_%s", day.Format("2006-01-02"), tenantIDStr),
			Day:           timestamppb.New(day),
			TenantId:      tenantIDStr,
			TotalStreams:  totalStreams,
			TotalViews:    totalViews,
			UniqueViewers: uniqueViewers,
			EgressBytes:   egressBytes,
		})
	}

	resultsLen := len(records)
	if resultsLen > params.Limit {
		records = records[:params.Limit]
	}

	if params.Direction == pagination.Backward {
		slices.Reverse(records)
	}

	total := <-countCh

	var startCursor, endCursor string
	if len(records) > 0 {
		startCursor = pagination.EncodeCursor(records[0].Day.AsTime(), "")
		endCursor = pagination.EncodeCursor(records[len(records)-1].Day.AsTime(), "")
	}

	return &pb.GetTenantAnalyticsDailyResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Records:    records,
	}, nil
}

// ============================================================================
// Stream Analytics Daily (from stream_analytics_daily table)
// ============================================================================

// GetStreamAnalyticsDaily returns daily stream-level analytics rollups
func (s *PeriscopeServer) GetStreamAnalyticsDaily(ctx context.Context, req *pb.GetStreamAnalyticsDailyRequest) (*pb.GetStreamAnalyticsDailyResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	params, err := getCursorPagination(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	// Build count query
	countQuery := `SELECT count(*) FROM stream_analytics_daily WHERE tenant_id = ? AND day >= toDate(?) AND day <= toDate(?)`
	countArgs := []interface{}{tenantID, startTime, endTime}
	if streamID := req.GetStreamId(); streamID != "" {
		countQuery += ` AND stream_id = ?`
		countArgs = append(countArgs, streamID)
	}
	countCh := s.countAsync(ctx, countQuery, countArgs...)

	query := `
		SELECT day, tenant_id, stream_id, total_views, unique_viewers, unique_countries, unique_cities, egress_bytes
		FROM stream_analytics_daily
		WHERE tenant_id = ? AND day >= toDate(?) AND day <= toDate(?)
	`
	args := []interface{}{tenantID, startTime, endTime}

	if streamID := req.GetStreamId(); streamID != "" {
		query += ` AND stream_id = ?`
		args = append(args, streamID)
	}

	keysetCond, keysetArgs := buildKeysetCondition(params, "day", "stream_id")
	if keysetCond != "" {
		query += keysetCond
		args = append(args, keysetArgs...)
	}

	query += buildOrderBy(params, "day", "stream_id")
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var records []*pb.StreamAnalyticsDaily
	for rows.Next() {
		var day time.Time
		var tenantIDStr, streamIDStr string
		var totalViews int64
		var uniqueViewers, uniqueCountries, uniqueCities int32
		var egressBytes int64

		err := rows.Scan(&day, &tenantIDStr, &streamIDStr, &totalViews, &uniqueViewers, &uniqueCountries, &uniqueCities, &egressBytes)
		if err != nil {
			s.logger.WithError(err).Error("Failed to scan stream_analytics_daily row")
			continue
		}

		records = append(records, &pb.StreamAnalyticsDaily{
			Id:              fmt.Sprintf("%s_%s", day.Format("2006-01-02"), streamIDStr),
			Day:             timestamppb.New(day),
			TenantId:        tenantIDStr,
			StreamId:        streamIDStr,
			TotalViews:      totalViews,
			UniqueViewers:   uniqueViewers,
			UniqueCountries: uniqueCountries,
			UniqueCities:    uniqueCities,
			EgressBytes:     egressBytes,
		})
	}

	resultsLen := len(records)
	if resultsLen > params.Limit {
		records = records[:params.Limit]
	}

	if params.Direction == pagination.Backward {
		slices.Reverse(records)
	}

	total := <-countCh

	var startCursor, endCursor string
	if len(records) > 0 {
		startCursor = pagination.EncodeCursor(records[0].Day.AsTime(), records[0].StreamId)
		endCursor = pagination.EncodeCursor(records[len(records)-1].Day.AsTime(), records[len(records)-1].StreamId)
	}

	return &pb.GetStreamAnalyticsDailyResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Records:    records,
	}, nil
}

// GetAPIUsage returns API usage records and/or daily summaries
func (s *PeriscopeServer) GetAPIUsage(ctx context.Context, req *pb.GetAPIUsageRequest) (*pb.GetAPIUsageResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	params, err := getCursorPagination(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	authType := req.GetAuthType()
	operationType := req.GetOperationType()
	operationName := req.GetOperationName()
	summaryOnly := req.GetSummaryOnly()

	response := &pb.GetAPIUsageResponse{}

	// Get daily summaries (aggregated by auth_type)
	if operationName == "" {
		summaryQuery := `
			SELECT day, tenant_id, auth_type,
			       sumMerge(total_requests) AS total_requests,
			       sumMerge(total_errors) AS total_errors,
			       sumMerge(total_duration_ms) AS total_duration_ms,
			       sumMerge(total_complexity) AS total_complexity,
			       uniqCombinedMerge(unique_users) AS unique_users,
			       uniqCombinedMerge(unique_tokens) AS unique_tokens
			FROM api_usage_daily
			WHERE tenant_id = ? AND day >= toDate(?) AND day <= toDate(?)
		`
		summaryArgs := []interface{}{tenantID, startTime, endTime}
		if authType != "" {
			summaryQuery += " AND auth_type = ?"
			summaryArgs = append(summaryArgs, authType)
		}
		if operationType != "" {
			summaryQuery += " AND operation_type = ?"
			summaryArgs = append(summaryArgs, operationType)
		}
		summaryQuery += " GROUP BY day, tenant_id, auth_type ORDER BY day DESC"

		summaryRows, err := s.clickhouse.QueryContext(ctx, summaryQuery, summaryArgs...)
		if err != nil {
			s.logger.WithError(err).Error("Failed to query api_usage_daily")
		} else {
			defer summaryRows.Close()
			for summaryRows.Next() {
				var day time.Time
				var tenantIDStr, authTypeStr string
				var totalRequests, totalErrors, totalDurationMs, totalComplexity, uniqueUsers, uniqueTokens uint64

				err := summaryRows.Scan(&day, &tenantIDStr, &authTypeStr,
					&totalRequests, &totalErrors, &totalDurationMs, &totalComplexity,
					&uniqueUsers, &uniqueTokens)
				if err != nil {
					s.logger.WithError(err).Error("Failed to scan api_usage_daily row")
					continue
				}

				avgDuration := float64(0)
				if totalRequests > 0 {
					avgDuration = float64(totalDurationMs) / float64(totalRequests)
				}

				response.Summaries = append(response.Summaries, &pb.APIUsageSummary{
					Date:            timestamppb.New(day),
					TenantId:        tenantIDStr,
					AuthType:        authTypeStr,
					TotalRequests:   totalRequests,
					TotalErrors:     totalErrors,
					AvgDurationMs:   avgDuration,
					TotalComplexity: totalComplexity,
					UniqueUsers:     uniqueUsers,
					UniqueTokens:    uniqueTokens,
				})
			}
		}
	}

	operationSummaryQuery := `
		SELECT operation_type,
		       sumMerge(total_requests) AS total_requests,
		       sumMerge(total_errors) AS total_errors,
		       sumMerge(total_duration_ms) AS total_duration_ms,
		       sumMerge(total_complexity) AS total_complexity,
	       uniqCombined(ifNull(operation_name, '')) AS unique_operations
		FROM api_usage_hourly
		WHERE tenant_id = ? AND hour >= toStartOfHour(?) AND hour <= toStartOfHour(?)
	`
	operationSummaryArgs := []interface{}{tenantID, startTime, endTime}
	if authType != "" {
		operationSummaryQuery += " AND auth_type = ?"
		operationSummaryArgs = append(operationSummaryArgs, authType)
	}
	if operationType != "" {
		operationSummaryQuery += " AND operation_type = ?"
		operationSummaryArgs = append(operationSummaryArgs, operationType)
	}
	if operationName != "" {
		operationSummaryQuery += " AND ifNull(operation_name, '') = ?"
		operationSummaryArgs = append(operationSummaryArgs, operationName)
	}
	operationSummaryQuery += " GROUP BY operation_type ORDER BY total_requests DESC"

	operationRows, err := s.clickhouse.QueryContext(ctx, operationSummaryQuery, operationSummaryArgs...)
	if err != nil {
		s.logger.WithError(err).Error("Failed to query api_usage_hourly operation summaries")
	} else {
		defer operationRows.Close()
		for operationRows.Next() {
			var opTypeStr string
			var totalRequests, totalErrors, totalDurationMs, totalComplexity, uniqueOperations uint64

			if err := operationRows.Scan(&opTypeStr, &totalRequests, &totalErrors, &totalDurationMs, &totalComplexity, &uniqueOperations); err != nil {
				s.logger.WithError(err).Error("Failed to scan api_usage_hourly operation summary row")
				continue
			}

			avgDuration := float64(0)
			if totalRequests > 0 {
				avgDuration = float64(totalDurationMs) / float64(totalRequests)
			}

			response.OperationSummaries = append(response.OperationSummaries, &pb.APIUsageOperationSummary{
				OperationType:    opTypeStr,
				TotalRequests:    totalRequests,
				TotalErrors:      totalErrors,
				UniqueOperations: uniqueOperations,
				AvgDurationMs:    avgDuration,
				TotalComplexity:  totalComplexity,
			})
		}
	}

	if summaryOnly {
		return response, nil
	}

	// Build count query for hourly records
	countQuery := `SELECT count(*) FROM api_usage_hourly WHERE tenant_id = ? AND hour >= toStartOfHour(?) AND hour <= toStartOfHour(?)`
	countArgs := []interface{}{tenantID, startTime, endTime}
	if authType != "" {
		countQuery += " AND auth_type = ?"
		countArgs = append(countArgs, authType)
	}
	if operationType != "" {
		countQuery += " AND operation_type = ?"
		countArgs = append(countArgs, operationType)
	}
	if operationName != "" {
		countQuery += " AND ifNull(operation_name, '') = ?"
		countArgs = append(countArgs, operationName)
	}
	countCh := s.countAsync(ctx, countQuery, countArgs...)

	// Query hourly records
	query := `
		SELECT hour, tenant_id, auth_type, operation_type, operation_name,
		       sumMerge(total_requests) AS total_requests,
		       sumMerge(total_errors) AS total_errors,
		       sumMerge(total_duration_ms) AS total_duration_ms,
		       sumMerge(total_complexity) AS total_complexity,
		       uniqCombinedMerge(unique_users) AS unique_users,
		       uniqCombinedMerge(unique_tokens) AS unique_tokens
		FROM api_usage_hourly
		WHERE tenant_id = ? AND hour >= toStartOfHour(?) AND hour <= toStartOfHour(?)
	`
	args := []interface{}{tenantID, startTime, endTime}

	if authType != "" {
		query += " AND auth_type = ?"
		args = append(args, authType)
	}
	if operationType != "" {
		query += " AND operation_type = ?"
		args = append(args, operationType)
	}
	if operationName != "" {
		query += " AND ifNull(operation_name, '') = ?"
		args = append(args, operationName)
	}

	query += " GROUP BY hour, tenant_id, auth_type, operation_type, operation_name"

	if params.Cursor != nil {
		cursorAuthType := ""
		cursorOpType := ""
		cursorOpName := ""
		if parts := strings.SplitN(params.Cursor.ID, "|", 3); len(parts) == 3 {
			cursorAuthType = parts[0]
			cursorOpType = parts[1]
			cursorOpName = parts[2]
		}
		if params.Direction == pagination.Backward {
			query += " AND (hour, auth_type, operation_type, ifNull(operation_name, '')) > (?, ?, ?, ?)"
		} else {
			query += " AND (hour, auth_type, operation_type, ifNull(operation_name, '')) < (?, ?, ?, ?)"
		}
		args = append(args, params.Cursor.Timestamp, cursorAuthType, cursorOpType, cursorOpName)
	}

	if params.Direction == pagination.Backward {
		query += " ORDER BY hour ASC, auth_type ASC, operation_type ASC, ifNull(operation_name, '') ASC"
	} else {
		query += " ORDER BY hour DESC, auth_type DESC, operation_type DESC, ifNull(operation_name, '') DESC"
	}
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var records []*pb.APIUsageRecord
	for rows.Next() {
		var hour time.Time
		var tenantIDStr, authTypeStr, operationTypeStr string
		var operationName *string
		var totalRequests, totalErrors, totalDurationMs, totalComplexity, uniqueUsers, uniqueTokens uint64

		err := rows.Scan(&hour, &tenantIDStr, &authTypeStr, &operationTypeStr, &operationName,
			&totalRequests, &totalErrors, &totalDurationMs, &totalComplexity,
			&uniqueUsers, &uniqueTokens)
		if err != nil {
			s.logger.WithError(err).Error("Failed to scan api_usage_hourly row")
			continue
		}

		opName := ""
		if operationName != nil {
			opName = *operationName
		}

		// Composite ID for cursor pagination (timestamp handled separately).
		id := fmt.Sprintf("%s|%s|%s", authTypeStr, operationTypeStr, opName)

		records = append(records, &pb.APIUsageRecord{
			Id:              id,
			Timestamp:       timestamppb.New(hour),
			TenantId:        tenantIDStr,
			AuthType:        authTypeStr,
			OperationType:   operationTypeStr,
			OperationName:   opName,
			RequestCount:    totalRequests,
			ErrorCount:      totalErrors,
			TotalDurationMs: totalDurationMs,
			TotalComplexity: totalComplexity,
			UniqueUsers:     uniqueUsers,
			UniqueTokens:    uniqueTokens,
		})
	}

	// Handle pagination
	total := <-countCh
	resultsLen := len(records)
	if resultsLen > params.Limit {
		records = records[:params.Limit]
	}
	if params.Direction == pagination.Backward {
		slices.Reverse(records)
	}

	var startCursor, endCursor string
	if len(records) > 0 {
		startCursor = pagination.EncodeCursor(records[0].Timestamp.AsTime(), records[0].Id)
		endCursor = pagination.EncodeCursor(records[len(records)-1].Timestamp.AsTime(), records[len(records)-1].Id)
	}

	response.Pagination = buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor)
	response.Records = records

	return response, nil
}

// GRPCServerConfig contains configuration for creating a Periscope gRPC server
// Note: All queries use ClickHouse only - no PostgreSQL dependency
type GRPCServerConfig struct {
	ClickHouse   database.ClickHouseConn
	Logger       logging.Logger
	ServiceToken string
	JWTSecret    []byte
	Metrics      *metrics.Metrics
}

// NewGRPCServer creates a new gRPC server for Periscope
func NewGRPCServer(cfg GRPCServerConfig) *grpc.Server {
	// Chain auth interceptor with logging interceptor
	authInterceptor := middleware.GRPCAuthInterceptor(middleware.GRPCAuthConfig{
		ServiceToken: cfg.ServiceToken,
		JWTSecret:    cfg.JWTSecret,
		Logger:       cfg.Logger,
		SkipMethods: []string{
			"/grpc.health.v1.Health/Check",
			"/grpc.health.v1.Health/Watch",
		},
	})

	opts := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(unaryInterceptor(cfg.Logger), authInterceptor),
	}

	server := grpc.NewServer(opts...)
	periscopeServer := &PeriscopeServer{
		clickhouse: cfg.ClickHouse,
		logger:     cfg.Logger,
		metrics:    cfg.Metrics,
	}

	// Register all services
	pb.RegisterStreamAnalyticsServiceServer(server, periscopeServer)
	pb.RegisterViewerAnalyticsServiceServer(server, periscopeServer)
	pb.RegisterTrackAnalyticsServiceServer(server, periscopeServer)
	pb.RegisterConnectionAnalyticsServiceServer(server, periscopeServer)
	pb.RegisterNodeAnalyticsServiceServer(server, periscopeServer)
	pb.RegisterRoutingAnalyticsServiceServer(server, periscopeServer)
	pb.RegisterPlatformAnalyticsServiceServer(server, periscopeServer)
	pb.RegisterClipAnalyticsServiceServer(server, periscopeServer)
	pb.RegisterAggregatedAnalyticsServiceServer(server, periscopeServer)

	// Register gRPC health checking service
	hs := health.NewServer()
	grpc_health_v1.RegisterHealthServer(server, hs)

	return server
}

// unaryInterceptor logs gRPC requests
func unaryInterceptor(logger logging.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		logger.WithFields(logging.Fields{
			"method":   info.FullMethod,
			"duration": time.Since(start),
			"error":    err,
		}).Debug("gRPC request processed")
		return resp, grpcutil.SanitizeError(err)
	}
}
