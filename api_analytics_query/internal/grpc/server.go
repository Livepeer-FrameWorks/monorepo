package grpc

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"frameworks/api_analytics_query/internal/metrics"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pagination"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// PeriscopeServer implements all Periscope gRPC services
// All queries use ClickHouse only - no PostgreSQL dependency
type PeriscopeServer struct {
	periscopepb.UnimplementedStreamAnalyticsServiceServer
	periscopepb.UnimplementedViewerAnalyticsServiceServer
	periscopepb.UnimplementedTrackAnalyticsServiceServer
	periscopepb.UnimplementedConnectionAnalyticsServiceServer
	periscopepb.UnimplementedNodeAnalyticsServiceServer
	periscopepb.UnimplementedRoutingAnalyticsServiceServer
	periscopepb.UnimplementedFederationAnalyticsServiceServer
	periscopepb.UnimplementedPlatformAnalyticsServiceServer
	periscopepb.UnimplementedClipAnalyticsServiceServer
	periscopepb.UnimplementedAggregatedAnalyticsServiceServer
	periscopepb.UnimplementedOrchestratorAnalyticsServiceServer

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
	var data map[string]any
	if err := json.Unmarshal([]byte(eventData), &data); err != nil {
		return nil
	}
	s, err := structpb.NewStruct(data)
	if err != nil {
		return nil
	}
	return s
}

// requireTenantID extracts tenant_id from request or context.
// Ensures authenticated tenant IDs cannot be overridden by request payloads.
func requireTenantID(ctx context.Context, reqTenantID string) (string, error) {
	ctxTenantID := middleware.GetTenantID(ctx)
	if ctxTenantID != "" {
		if reqTenantID != "" && reqTenantID != ctxTenantID {
			return "", status.Error(codes.PermissionDenied, "tenant_id mismatch")
		}
		return ctxTenantID, nil
	}
	if reqTenantID == "" {
		return "", status.Error(codes.InvalidArgument, "tenant_id required")
	}
	return reqTenantID, nil
}

func validateRelatedTenantIDs(ctx context.Context, relatedIDs []string) error {
	if len(relatedIDs) == 0 {
		return nil
	}

	// related_tenant_ids are used by Gateway calls (user JWT) to fetch routing/live-node
	// data across subscribed clusters. Don’t block purely because a tenant_id exists.
	// If we want to restrict this further, we need to check for actual service creds/role,
	// not just the presence of tenant context.
	if middleware.IsServiceCall(ctx) {
		return nil
	}
	if middleware.GetUserID(ctx) == "" && ctxkeys.GetServiceToken(ctx) == "" {
		return status.Error(codes.PermissionDenied, "related_tenant_ids require authentication")
	}
	return nil
}

// validateTimeRangeProto validates time range from proto message
func validateTimeRangeProto(tr *commonpb.TimeRange) (time.Time, time.Time, error) {
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
	if endTime.Before(startTime) {
		return time.Time{}, time.Time{}, fmt.Errorf("end time must be after start time")
	}
	return startTime, endTime, nil
}

const clickhouseDefaultTimeout = 5 * time.Second

func withClickhouseTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, clickhouseDefaultTimeout)
}

// sanitizeFloat32 zeroes out NaN/Inf so the value can be marshaled via the
// non-null GraphQL contract. ClickHouse avg() over an empty window returns
// NaN; degenerate divisions can produce Inf.
func sanitizeFloat32(v float64) float32 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return float32(v)
}

// sanitizeFloat64 is the float64 variant of sanitizeFloat32.
func sanitizeFloat64(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}

func (s *PeriscopeServer) queryStreamRuntimeSummary(ctx context.Context, tenantID string, startTime, endTime time.Time) (float64, int32, int32, error) {
	var streamHours float64
	var peakConcurrent, totalStreams int32
	err := s.clickhouse.QueryRowContext(ctx, `
		SELECT
			COALESCE(sum(active_seconds) / 3600.0, 0) AS stream_hours,
			toInt32(COALESCE(max(peak_viewers), 0)) AS peak_concurrent,
			toInt32(COALESCE(uniqExact(stream_id), 0)) AS total_streams
		FROM (
			SELECT
				stream_id,
				toFloat64(active_seconds) AS active_seconds,
				toUInt32(peak_viewers) AS peak_viewers
			FROM periscope.stream_runtime_5m_v
			WHERE tenant_id = ?
			  AND window_start >= ?
			  AND window_start <  ?

			UNION ALL

			SELECT
				stream_id,
				toFloat64(greatest(0, dateDiff('second', greatest(started_at, ?), least(now(), ?)))) AS active_seconds,
				toUInt32(current_viewers) AS peak_viewers
			FROM (
				SELECT
					s.stream_id AS stream_id,
					s.current_viewers AS current_viewers,
					least(
						ifNull(min(e.timestamp), if(ifNull(s.started_at, toDateTime(0)) > ifNull(last_end.ended_at, toDateTime(0)), ifNull(s.started_at, s.updated_at), s.updated_at)),
						if(ifNull(s.started_at, toDateTime(0)) > ifNull(last_end.ended_at, toDateTime(0)), ifNull(s.started_at, s.updated_at), s.updated_at)
					) AS started_at
				FROM periscope.stream_state_current AS s FINAL
				LEFT JOIN (
					SELECT stream_id, node_id, internal_name, max(timestamp) AS ended_at
					FROM periscope.stream_event_log
					WHERE tenant_id = ?
					  AND event_type = 'stream_end'
					GROUP BY stream_id, node_id, internal_name
				) AS last_end
					ON last_end.stream_id = s.stream_id
				   AND last_end.node_id = s.node_id
				   AND last_end.internal_name = s.internal_name
				LEFT JOIN periscope.stream_event_log AS e
					ON e.tenant_id = s.tenant_id
				   AND e.stream_id = s.stream_id
				   AND e.node_id = s.node_id
				   AND e.internal_name = s.internal_name
				   AND e.timestamp > ifNull(last_end.ended_at, toDateTime(0))
				   AND e.status = 'live'
				   AND e.event_type IN ('stream_start', 'stream_lifecycle', 'stream_buffer', 'track_list_update')
				WHERE s.tenant_id = ?
				  AND s.status = 'live'
				GROUP BY s.stream_id, s.current_viewers, s.started_at, s.updated_at, last_end.ended_at
			)
			WHERE started_at < ?
			  AND now() >= ?
		)
	`, tenantID, startTime, endTime, startTime, endTime, tenantID, tenantID, endTime, startTime).Scan(&streamHours, &peakConcurrent, &totalStreams)
	return streamHours, peakConcurrent, totalStreams, err
}

func wrapClickhouseError(err error, message string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, message)
	}
	if errors.Is(err, context.Canceled) {
		return status.Error(codes.Canceled, message)
	}
	if errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone) {
		return status.Error(codes.Unavailable, message)
	}
	var timeoutErr interface{ Timeout() bool }
	if errors.As(err, &timeoutErr) && timeoutErr.Timeout() {
		return status.Error(codes.DeadlineExceeded, message)
	}
	return status.Errorf(codes.Internal, "%s: %v", message, err)
}

// getCursorPagination extracts cursor pagination with defaults.
// Supports bidirectional pagination: forward (first/after) and backward (last/before).
func getCursorPagination(req *commonpb.CursorPaginationRequest) (*pagination.Params, error) {
	return pagination.Parse(req)
}

// buildCursorResponse creates a CursorPaginationResponse from results.
// resultsLen: length before trimming, limit: requested limit
// direction: pagination direction, totalCount: from COUNT query
func buildCursorResponse(resultsLen, limit int, direction pagination.Direction, totalCount int32, startCursor, endCursor string) *commonpb.CursorPaginationResponse {
	return pagination.BuildResponse(resultsLen, limit, direction, totalCount, startCursor, endCursor)
}

// buildKeysetConditionN returns a WHERE clause fragment for keyset pagination with N columns.
// Forward: (ts, cols...) < (cursor_ts, cursor_cols...) - fetches older items
// Backward: (ts, cols...) > (cursor_ts, cursor_cols...) - fetches newer items
func buildKeysetConditionN(params *pagination.Params, tsCol string, cols []string, cursorParts []string) (string, []any, error) {
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

	args := make([]any, 0, len(allCols))
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
func buildKeysetCondition(params *pagination.Params, tsCol, idCol string) (string, []any) {
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
func buildKeysetConditionSingle(params *pagination.Params, col string) (string, []any) {
	if params.Cursor == nil {
		return "", nil
	}
	if params.Direction == pagination.Backward {
		return fmt.Sprintf(" AND %s > ?", col), []any{params.Cursor.Timestamp}
	}
	return fmt.Sprintf(" AND %s < ?", col), []any{params.Cursor.Timestamp}
}

// buildOrderBySingle returns an ORDER BY clause for single-column keyset pagination.
func buildOrderBySingle(params *pagination.Params, col string) string {
	if params.Direction == pagination.Backward {
		return fmt.Sprintf(" ORDER BY %s ASC", col)
	}
	return fmt.Sprintf(" ORDER BY %s DESC", col)
}

// clickhouseInterval maps a coarse interval token ("5m"/"15m"/"1h"/"1d") to a
// ClickHouse INTERVAL expression for toStartOfInterval bucketing. Unknown or empty
// tokens fall back to 5 minutes.
func clickhouseInterval(token string) string {
	switch token {
	case "15m":
		return "15 MINUTE"
	case "1h":
		return "1 HOUR"
	case "1d":
		return "1 DAY"
	default:
		return "5 MINUTE"
	}
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
func (s *PeriscopeServer) countAsync(ctx context.Context, query string, args ...any) <-chan int32 {
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

func (s *PeriscopeServer) GetStreamEvents(ctx context.Context, req *periscopepb.GetStreamEventsRequest) (*periscopepb.GetStreamEventsResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
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
	args := []any{tenantID, streamID, startTime, endTime}

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
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var events []*periscopepb.StreamEvent
	for rows.Next() {
		var event periscopepb.StreamEvent
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

	return &periscopepb.GetStreamEventsResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Events:     events,
	}, nil
}

func (s *PeriscopeServer) GetBufferEvents(ctx context.Context, req *periscopepb.GetBufferEventsRequest) (*periscopepb.GetBufferEventsResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
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
	args := []any{tenantID, streamID, startTime, endTime}

	keysetCond, keysetArgs := buildKeysetCondition(params, "timestamp", "event_id")
	if keysetCond != "" {
		query += keysetCond
		args = append(args, keysetArgs...)
	}

	query += buildOrderBy(params, "timestamp", "event_id")
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var events []*periscopepb.BufferEvent
	for rows.Next() {
		var event periscopepb.BufferEvent
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

	return &periscopepb.GetBufferEventsResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Events:     events,
	}, nil
}

func (s *PeriscopeServer) GetStreamHealthMetrics(ctx context.Context, req *periscopepb.GetStreamHealthMetricsRequest) (*periscopepb.GetStreamHealthMetricsResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
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
			codec, quality_tier, toString(track_metadata) as track_metadata,
			has_issues, issues_description, track_count,
			audio_channels, audio_sample_rate, audio_codec, audio_bitrate
		FROM stream_health_samples
		WHERE tenant_id = ? AND timestamp >= ? AND timestamp <= ?
	`
	args := []any{tenantID, startTime, endTime}

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
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var metrics []*periscopepb.StreamHealthMetric
	for rows.Next() {
		var m periscopepb.StreamHealthMetric
		var ts time.Time
		var metricTenantID string
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
			&ts, &metricTenantID, &m.StreamId, &m.NodeId,
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
		m.TenantId = metricTenantID
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

	return &periscopepb.GetStreamHealthMetricsResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Metrics:    metrics,
	}, nil
}

// GetStreamStatus returns operational state for a single stream (Control/Data plane separation)
// This is the source of truth for stream status - queries stream_state_current directly
func (s *PeriscopeServer) GetStreamStatus(ctx context.Context, req *periscopepb.GetStreamStatusRequest) (*periscopepb.StreamStatusResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}

	streamID := req.GetStreamId()
	if streamID == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_id required")
	}

	resp := &periscopepb.StreamStatusResponse{
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

	err = s.clickhouse.QueryRowContext(ctx, `
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

		if streamStatus == "live" {
			if liveStartedAt, ok := s.lookupLiveIntervalStarts(ctx, tenantID, []string{streamID})[streamID]; ok && !liveStartedAt.IsZero() {
				startedAt = &liveStartedAt
			}
		}
		if startedAt != nil && !startedAt.IsZero() {
			resp.StartedAt = timestamppb.New(*startedAt)
			if streamStatus == "live" {
				resp.DurationSeconds = max(0, int64(time.Since(*startedAt).Seconds()))
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
func (s *PeriscopeServer) GetStreamsStatus(ctx context.Context, req *periscopepb.GetStreamsStatusRequest) (*periscopepb.StreamsStatusResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}

	streamIDs := req.GetStreamIds()
	if len(streamIDs) == 0 {
		return &periscopepb.StreamsStatusResponse{Statuses: make(map[string]*periscopepb.StreamStatusResponse)}, nil
	}

	// Initialize response with defaults. UpdatedAt is set to the snapshot time
	// so the non-null GraphQL field has a value for streams that have never had
	// analytics events (Periscope reads ClickHouse stream_state_current and has
	// no access to commodore.streams.created_at).
	now := timestamppb.Now()
	statuses := make(map[string]*periscopepb.StreamStatusResponse, len(streamIDs))
	for _, id := range streamIDs {
		statuses[id] = &periscopepb.StreamStatusResponse{
			StreamId:  id,
			Status:    "offline",
			UpdatedAt: now,
		}
	}

	// Build IN clause for ClickHouse batch query
	placeholders := make([]string, len(streamIDs))
	args := make([]any, len(streamIDs)+1)
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
		return &periscopepb.StreamsStatusResponse{Statuses: statuses}, nil
	}
	defer func() { _ = rows.Close() }()

	liveStarts := s.lookupLiveIntervalStarts(ctx, tenantID, streamIDs)
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

		resp := &periscopepb.StreamStatusResponse{
			StreamId:       streamID,
			Status:         streamStatus,
			CurrentViewers: int64(currentViewers),
		}

		if streamStatus == "live" {
			if liveStartedAt, ok := liveStarts[streamID]; ok && !liveStartedAt.IsZero() {
				startedAt = &liveStartedAt
			}
		}
		if startedAt != nil && !startedAt.IsZero() {
			resp.StartedAt = timestamppb.New(*startedAt)
			if streamStatus == "live" {
				resp.DurationSeconds = max(0, int64(time.Since(*startedAt).Seconds()))
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

		statuses[streamID] = resp
	}

	return &periscopepb.StreamsStatusResponse{Statuses: statuses}, nil
}

// joinStrings joins strings with a separator (helper for SQL IN clause)
func joinStrings(strs []string, sep string) string {
	return strings.Join(strs, sep)
}

func (s *PeriscopeServer) lookupLiveIntervalStarts(ctx context.Context, tenantID string, streamIDs []string) map[string]time.Time {
	result := make(map[string]time.Time, len(streamIDs))
	if len(streamIDs) == 0 {
		return result
	}
	parsed := make([]uuid.UUID, 0, len(streamIDs))
	for _, id := range streamIDs {
		streamUUID, err := uuid.Parse(id)
		if err == nil && streamUUID != uuid.Nil {
			parsed = append(parsed, streamUUID)
		}
	}
	if len(parsed) == 0 {
		return result
	}

	placeholders := make([]string, len(parsed))
	args := make([]any, 0, 1+len(parsed)+1+len(parsed))
	args = append(args, tenantID)
	for i, id := range parsed {
		placeholders[i] = "?"
		args = append(args, id)
	}
	args = append(args, tenantID)
	for _, id := range parsed {
		args = append(args, id)
	}

	inClause := joinStrings(placeholders, ", ")
	query := fmt.Sprintf(`
		WITH last_end AS (
			SELECT stream_id, node_id, internal_name, max(timestamp) AS ended_at
			FROM periscope.stream_event_log
			WHERE tenant_id = ?
			  AND stream_id IN (%s)
			  AND event_type = 'stream_end'
			GROUP BY stream_id, node_id, internal_name
		)
		SELECT
			toString(s.stream_id) AS stream_id,
			if(
				ifNull(s.started_at, toDateTime(0)) > ifNull(last_end.ended_at, toDateTime(0)),
				ifNull(s.started_at, s.updated_at),
				ifNull(min(e.timestamp), s.updated_at)
			) AS started_at
		FROM periscope.stream_state_current AS s FINAL
		LEFT JOIN last_end
			ON last_end.stream_id = s.stream_id
		   AND last_end.node_id = s.node_id
		   AND last_end.internal_name = s.internal_name
		LEFT JOIN periscope.stream_event_log AS e
			ON e.tenant_id = s.tenant_id
		   AND e.stream_id = s.stream_id
		   AND e.node_id = s.node_id
		   AND e.internal_name = s.internal_name
		   AND e.timestamp > ifNull(last_end.ended_at, toDateTime(0))
		   AND e.status = 'live'
		   AND e.event_type IN ('stream_start', 'stream_lifecycle', 'stream_buffer', 'track_list_update')
		WHERE s.tenant_id = ?
		  AND s.stream_id IN (%s)
		  AND s.status = 'live'
		GROUP BY s.stream_id, s.started_at, s.updated_at, last_end.ended_at
	`, inClause, inClause)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		s.logger.WithError(err).Warn("Failed to resolve live interval starts from stream_event_log")
		return result
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var streamID string
		var startedAt time.Time
		if err := rows.Scan(&streamID, &startedAt); err != nil {
			s.logger.WithError(err).Warn("Failed to scan live interval start")
			continue
		}
		result[streamID] = startedAt
	}
	if err := rows.Err(); err != nil {
		s.logger.WithError(err).Warn("Failed while resolving live interval starts")
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

func (s *PeriscopeServer) GetViewerMetrics(ctx context.Context, req *periscopepb.GetViewerMetricsRequest) (*periscopepb.GetViewerMetricsResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
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
	sessionStartExpr := "ifNull(connected_at, disconnected_at)"
	sessionEndExpr := "ifNull(disconnected_at, ?)"
	overlapWhere := fmt.Sprintf("tenant_id = ? AND %s <= ? AND %s >= ?", sessionStartExpr, sessionEndExpr)
	countQuery := fmt.Sprintf(`SELECT count(*) FROM periscope.viewer_sessions_current FINAL WHERE %s`, overlapWhere)
	countArgs := []any{tenantID, endTime, endTime, startTime}
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
		WHERE ` + overlapWhere + `
	`
	args := []any{tenantID, endTime, endTime, startTime}

	if streamID != "" {
		query += " AND stream_id = ?"
		args = append(args, streamID)
	}

	keysetCond, keysetArgs := buildKeysetCondition(params, sessionStartExpr, "session_id")
	if keysetCond != "" {
		query += keysetCond
		args = append(args, keysetArgs...)
	}

	query += buildOrderBy(params, sessionStartExpr, "session_id")
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var sessions []*periscopepb.ViewerSession
	for rows.Next() {
		var session periscopepb.ViewerSession
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

	return &periscopepb.GetViewerMetricsResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Sessions:   sessions,
	}, nil
}

func (s *PeriscopeServer) GetViewerCountTimeSeries(ctx context.Context, req *periscopepb.GetViewerCountTimeSeriesRequest) (*periscopepb.GetViewerCountTimeSeriesResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	// Query finalized stream runtime windows instead of legacy stream_event_log rollups.
	query := fmt.Sprintf(`
		SELECT
			toStartOfInterval(window_start, INTERVAL %s) as bucket,
			stream_id,
			max(peak_viewers) as viewer_count
		FROM periscope.stream_runtime_5m_v
		WHERE tenant_id = ? AND window_start >= ? AND window_start < ?
	`, clickhouseInterval(req.GetInterval()))
	args := []any{tenantID, startTime, endTime}

	if streamID := req.GetStreamId(); streamID != "" {
		query += " AND stream_id = ?"
		args = append(args, streamID)
	}

	query += " GROUP BY bucket, stream_id ORDER BY bucket ASC"

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var buckets []*periscopepb.ViewerCountBucket
	for rows.Next() {
		var bucket time.Time
		var streamID string
		var viewerCount int32

		if err := rows.Scan(&bucket, &streamID, &viewerCount); err != nil {
			s.logger.WithError(err).Info("Failed to scan viewer count bucket row")
			continue
		}

		buckets = append(buckets, &periscopepb.ViewerCountBucket{
			Timestamp:   timestamppb.New(bucket),
			StreamId:    streamID,
			ViewerCount: viewerCount,
		})
	}

	return &periscopepb.GetViewerCountTimeSeriesResponse{
		Buckets: buckets,
	}, nil
}

func (s *PeriscopeServer) GetGeographicDistribution(ctx context.Context, req *periscopepb.GetGeographicDistributionRequest) (*periscopepb.GetGeographicDistributionResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	topN := req.GetTopN()
	if topN <= 0 || topN > 100 {
		topN = 10
	}

	sessionStartExpr := "ifNull(connected_at, disconnected_at)"
	sessionEndExpr := "ifNull(disconnected_at, ?)"
	whereClause := fmt.Sprintf("WHERE tenant_id = ? AND %s <= ? AND %s >= ?", sessionStartExpr, sessionEndExpr)
	args := []any{tenantID, endTime, endTime, startTime}

	if streamID := req.GetStreamId(); streamID != "" {
		whereClause += " AND stream_id = ?"
		args = append(args, streamID)
	}

	countryQuery := fmt.Sprintf(`
		SELECT country_code, uniqExact(node_id, session_id) as cnt
		FROM periscope.viewer_sessions_current FINAL
		%s AND country_code != ''
		GROUP BY country_code
		ORDER BY cnt DESC
		LIMIT %d
	`, whereClause, topN)

	countryRows, err := s.clickhouse.QueryContext(ctx, countryQuery, args...)
	if err != nil {
		return nil, wrapClickhouseError(err, "database error (countries)")
	}
	defer func() { _ = countryRows.Close() }()

	var topCountries []*periscopepb.CountryMetric
	var totalViewersForPercent uint64
	for countryRows.Next() {
		var countryCode string
		var count uint64
		if scanErr := countryRows.Scan(&countryCode, &count); scanErr != nil {
			return nil, wrapClickhouseError(scanErr, "database error (countries)")
		}
		topCountries = append(topCountries, &periscopepb.CountryMetric{
			CountryCode: countryCode,
			ViewerCount: int32(min(count, uint64(1<<31-1))),
		})
	}
	if rowsErr := countryRows.Err(); rowsErr != nil {
		return nil, wrapClickhouseError(rowsErr, "database error (countries)")
	}

	totalQuery := fmt.Sprintf(`
		SELECT uniqExact(node_id, session_id)
		FROM periscope.viewer_sessions_current FINAL
		%s AND country_code != ''
	`, whereClause)
	if queryErr := s.clickhouse.QueryRowContext(ctx, totalQuery, args...).Scan(&totalViewersForPercent); queryErr != nil {
		return nil, wrapClickhouseError(queryErr, "database error (countries)")
	}

	// Calculate percentages for countries
	for _, c := range topCountries {
		if totalViewersForPercent > 0 {
			c.Percentage = float32(c.ViewerCount) / float32(totalViewersForPercent) * 100
		}
	}

	cityQuery := fmt.Sprintf(`
		SELECT city, country_code, uniqExact(node_id, session_id) as cnt,
		       ifNull(any(latitude), 0) as lat, ifNull(any(longitude), 0) as lon
		FROM periscope.viewer_sessions_current FINAL
		%s AND city != ''
		GROUP BY city, country_code
		ORDER BY cnt DESC
		LIMIT %d
	`, whereClause, topN)

	cityRows, err := s.clickhouse.QueryContext(ctx, cityQuery, args...)
	if err != nil {
		return nil, wrapClickhouseError(err, "database error (cities)")
	}
	defer func() { _ = cityRows.Close() }()

	var topCities []*periscopepb.CityMetric
	for cityRows.Next() {
		var city, countryCode string
		var count uint64
		var lat, lon float64
		if err := cityRows.Scan(&city, &countryCode, &count, &lat, &lon); err != nil {
			return nil, wrapClickhouseError(err, "database error (cities)")
		}
		percentage := float32(0)
		if totalViewersForPercent > 0 {
			percentage = float32(count) / float32(totalViewersForPercent) * 100
		}
		topCities = append(topCities, &periscopepb.CityMetric{
			City:        city,
			CountryCode: countryCode,
			ViewerCount: int32(min(count, uint64(1<<31-1))),
			Percentage:  percentage,
			Latitude:    lat,
			Longitude:   lon,
		})
	}
	if err := cityRows.Err(); err != nil {
		return nil, wrapClickhouseError(err, "database error (cities)")
	}

	var uniqueCities uint64
	uniqueCityQuery := fmt.Sprintf(`
		SELECT uniqExact(city, country_code)
		FROM periscope.viewer_sessions_current FINAL
		%s AND city != ''
	`, whereClause)
	if err := s.clickhouse.QueryRowContext(ctx, uniqueCityQuery, args...).Scan(&uniqueCities); err != nil {
		s.logger.WithError(err).Warn("Failed to get unique city counts")
	}

	uniqueQuery := fmt.Sprintf(`
		SELECT uniqExact(country_code), uniqExact(node_id, session_id)
		FROM periscope.viewer_sessions_current FINAL
		%s AND country_code != ''
	`, whereClause)

	var uniqueCountries uint64
	var totalViewersAll uint64
	totalViewers := int32(0)
	if err := s.clickhouse.QueryRowContext(ctx, uniqueQuery, args...).Scan(&uniqueCountries, &totalViewersAll); err != nil {
		s.logger.WithError(err).Warn("Failed to get unique geographic counts")
	}
	totalViewers = int32(min(totalViewersAll, uint64(1<<31-1)))

	uniqueCountriesVal := int32(min(uniqueCountries, uint64(1<<31-1)))

	return &periscopepb.GetGeographicDistributionResponse{
		TopCountries:    topCountries,
		TopCities:       topCities,
		UniqueCountries: uniqueCountriesVal,
		UniqueCities:    int32(min(uniqueCities, uint64(1<<31-1))),
		TotalViewers:    totalViewers,
	}, nil
}

// ============================================================================
// TrackAnalyticsService Implementation
// ============================================================================

func (s *PeriscopeServer) GetTrackListEvents(ctx context.Context, req *periscopepb.GetTrackListEventsRequest) (*periscopepb.GetTrackListEventsResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
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
	args := []any{tenantID, streamID, startTime, endTime}

	keysetCond, keysetArgs := buildKeysetCondition(params, "timestamp", "event_id")
	if keysetCond != "" {
		query += keysetCond
		args = append(args, keysetArgs...)
	}

	query += buildOrderBy(params, "timestamp", "event_id")
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var events []*periscopepb.TrackListEvent
	for rows.Next() {
		var event periscopepb.TrackListEvent
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

		event.Tracks = decodeStoredStreamTracks(trackListJSON)

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

	return &periscopepb.GetTrackListEventsResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Events:     events,
	}, nil
}

// ============================================================================
// ConnectionAnalyticsService Implementation
// ============================================================================

func (s *PeriscopeServer) GetConnectionEvents(ctx context.Context, req *periscopepb.GetConnectionEventsRequest) (*periscopepb.GetConnectionEventsResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
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
	countArgs := []any{tenantID, startTime, endTime}
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
	args := []any{tenantID, startTime, endTime}

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
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var events []*periscopepb.ConnectionEvent
	for rows.Next() {
		var event periscopepb.ConnectionEvent
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
			event.ClientBucket = &ipcpb.GeoBucket{
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
			event.NodeBucket = &ipcpb.GeoBucket{
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

	return &periscopepb.GetConnectionEventsResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Events:     events,
	}, nil
}

// ============================================================================
// NodeAnalyticsService Implementation
// ============================================================================

func (s *PeriscopeServer) GetNodeMetrics(ctx context.Context, req *periscopepb.GetNodeMetricsRequest) (*periscopepb.GetNodeMetricsResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
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
	countArgs := []any{tenantID, startTime, endTime}
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
	args := []any{tenantID, startTime, endTime}

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
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var metrics []*periscopepb.NodeMetric
	for rows.Next() {
		var m periscopepb.NodeMetric
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

	return &periscopepb.GetNodeMetricsResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Metrics:    metrics,
	}, nil
}

func (s *PeriscopeServer) GetNodeMetrics1H(ctx context.Context, req *periscopepb.GetNodeMetrics1HRequest) (*periscopepb.GetNodeMetrics1HResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	params, err := getCursorPagination(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	// Start count query in parallel — subquery deduplicates AGT groups
	nodeID := req.GetNodeId()
	countQuery := `SELECT count(*) FROM (SELECT 1 FROM periscope.node_metrics_1h WHERE tenant_id = ? AND timestamp_1h >= ? AND timestamp_1h <= ?`
	countArgs := []any{tenantID, startTime, endTime}
	if nodeID != "" {
		countQuery += " AND node_id = ?"
		countArgs = append(countArgs, nodeID)
	}
	countQuery += " GROUP BY timestamp_1h, cluster_id, node_id) sub"
	countCh := s.countAsync(ctx, countQuery, countArgs...)

	query := `
		SELECT timestamp_1h, cluster_id, node_id,
		       sum(cpu_sum)/sum(cpu_count) as avg_cpu, max(peak_cpu) as peak_cpu,
		       sum(memory_sum)/sum(memory_count) as avg_memory, max(peak_memory) as peak_memory,
		       sum(disk_sum)/sum(disk_count) as avg_disk, max(peak_disk) as peak_disk,
		       sum(shm_sum)/sum(shm_count) as avg_shm, max(peak_shm) as peak_shm,
		       max(bw_in_max) - min(bw_in_min) as total_bandwidth_in,
		       max(bw_out_max) - min(bw_out_min) as total_bandwidth_out,
		       if(sum(healthy_sum)/sum(healthy_count) >= 0.5, 1, 0) as was_healthy
		FROM periscope.node_metrics_1h
		WHERE tenant_id = ? AND timestamp_1h >= ? AND timestamp_1h <= ?
	`
	args := []any{tenantID, startTime, endTime}

	if nodeID != "" {
		query += " AND node_id = ?"
		args = append(args, nodeID)
	}

	keysetCond, keysetArgs := buildKeysetCondition(params, "timestamp_1h", "concat(cluster_id, ':', node_id)")
	if keysetCond != "" {
		query += keysetCond
		args = append(args, keysetArgs...)
	}

	query += " GROUP BY timestamp_1h, cluster_id, node_id"
	query += buildOrderBy(params, "timestamp_1h", "concat(cluster_id, ':', node_id)")
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var metrics []*periscopepb.NodeMetricHourly
	for rows.Next() {
		var m periscopepb.NodeMetricHourly
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

	return &periscopepb.GetNodeMetrics1HResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Metrics:    metrics,
	}, nil
}

// GetNodeMetricsAggregated returns per-node aggregates for the requested time range.
func (s *PeriscopeServer) GetNodeMetricsAggregated(ctx context.Context, req *periscopepb.GetNodeMetricsAggregatedRequest) (*periscopepb.GetNodeMetricsAggregatedResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	query := `
		SELECT cluster_id, node_id,
		       avg(avg_cpu), avg(avg_memory), avg(avg_disk), avg(avg_shm),
		       sum(total_bandwidth_in), sum(total_bandwidth_out),
		       sum(sample_count)
		FROM (
		    SELECT timestamp_1h, cluster_id, node_id,
		           sum(cpu_sum)/sum(cpu_count) as avg_cpu,
		           sum(memory_sum)/sum(memory_count) as avg_memory,
		           sum(disk_sum)/sum(disk_count) as avg_disk,
		           sum(shm_sum)/sum(shm_count) as avg_shm,
		           max(bw_in_max) - min(bw_in_min) as total_bandwidth_in,
		           max(bw_out_max) - min(bw_out_min) as total_bandwidth_out,
		           sum(cpu_count) as sample_count
		    FROM periscope.node_metrics_1h
		    WHERE tenant_id = ? AND timestamp_1h >= ? AND timestamp_1h <= ?
	`
	args := []any{tenantID, startTime, endTime}
	if req.GetNodeId() != "" {
		query += " AND node_id = ?"
		args = append(args, req.GetNodeId())
	}
	query += `
		    GROUP BY timestamp_1h, cluster_id, node_id
		) sub
		GROUP BY cluster_id, node_id ORDER BY cluster_id, node_id`

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var metrics []*periscopepb.NodeMetricsAggregated
	for rows.Next() {
		var m periscopepb.NodeMetricsAggregated
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

	return &periscopepb.GetNodeMetricsAggregatedResponse{Metrics: metrics}, nil
}

// GetLiveNodes returns current state of nodes from node_state_current (ReplacingMergeTree)
// This is the source of truth for real-time node status - simple SELECT, no time-series
// Supports multi-tenant access for subscribed clusters via related_tenant_ids
func (s *PeriscopeServer) GetLiveNodes(ctx context.Context, req *periscopepb.GetLiveNodesRequest) (*periscopepb.GetLiveNodesResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	if validateErr := validateRelatedTenantIDs(ctx, req.GetRelatedTenantIds()); validateErr != nil {
		return nil, validateErr
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
		       toString(metadata) as metadata, updated_at
		FROM periscope.node_state_current FINAL
		WHERE %s
	`, inClause)
	args := []any{}
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
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var nodes []*periscopepb.LiveNode
	for rows.Next() {
		var n periscopepb.LiveNode
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

	return &periscopepb.GetLiveNodesResponse{
		Nodes: nodes,
		Count: int32(len(nodes)),
	}, nil
}

// ============================================================================
// RoutingAnalyticsService Implementation
// ============================================================================

func (s *PeriscopeServer) GetRoutingEvents(ctx context.Context, req *periscopepb.GetRoutingEventsRequest) (*periscopepb.GetRoutingEventsResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	if validateErr := validateRelatedTenantIDs(ctx, req.GetRelatedTenantIds()); validateErr != nil {
		return nil, validateErr
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
	countArgs := []any{}
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
	if countErr := s.clickhouse.QueryRowContext(ctx, countQuery, countArgs...).Scan(&total); countErr != nil {
		s.logger.WithError(countErr).Warn("Failed to get routing events count")
	}

	// Main query with all geographic columns from ClickHouse schema
	query := fmt.Sprintf(`
		SELECT timestamp, stream_id, selected_node, status, details, score,
		       client_country, client_latitude, client_longitude, client_bucket_h3, client_bucket_res,
		       node_latitude, node_longitude, node_name, node_bucket_h3, node_bucket_res,
		       selected_node_id, routing_distance_km, tenant_id, stream_tenant_id, cluster_id,
		       latency_ms, candidates_count, event_type, source, remote_cluster_id
		FROM periscope.routing_decisions
		WHERE %s AND timestamp >= ? AND timestamp <= ?
	`, inClause)

	args := []any{}
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
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var events []*periscopepb.RoutingEvent
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
		var remoteClusterID string

		err := rows.Scan(&ts, &streamID, &selectedNode, &statusStr, &details, &score,
			&clientCountry, &clientLat, &clientLon, &clientBucketH3, &clientBucketRes,
			&nodeLat, &nodeLon, &nodeName, &nodeBucketH3, &nodeBucketRes,
			&selectedNodeID, &routingDistance, &rowTenantID, &streamTenantID, &clusterID,
			&latencyMs, &candidatesCount, &eventType, &source, &remoteClusterID)
		if err != nil {
			s.logger.WithError(err).Info("Failed to scan routing event row")
			continue
		}

		event := &periscopepb.RoutingEvent{
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
			event.ClientBucket = &ipcpb.GeoBucket{
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
			event.NodeBucket = &ipcpb.GeoBucket{
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
		if remoteClusterID != "" {
			event.RemoteClusterId = &remoteClusterID
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

	return &periscopepb.GetRoutingEventsResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Events:     events,
	}, nil
}

// GetRoutingEfficiency returns pre-aggregated routing decision stats from routing_decisions.
func (s *PeriscopeServer) GetRoutingEfficiency(ctx context.Context, req *periscopepb.GetRoutingEfficiencyRequest) (*periscopepb.GetRoutingEfficiencyResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	query := `
		SELECT
			count() AS total,
			countIf(status = 'success') AS successes,
			avg(routing_distance_km) AS avg_dist,
			avg(latency_ms) AS avg_lat
		FROM periscope.routing_decisions
		WHERE tenant_id = ? AND timestamp >= ? AND timestamp <= ?
	`
	args := []any{tenantID, startTime, endTime}

	if streamID := req.GetStreamId(); streamID != "" {
		query += " AND stream_id = ?"
		args = append(args, streamID)
	}

	var total, successes int64
	var avgDist, avgLat float64
	if scanErr := s.clickhouse.QueryRowContext(ctx, query, args...).Scan(&total, &successes, &avgDist, &avgLat); scanErr != nil {
		return nil, wrapClickhouseError(scanErr, "database error")
	}

	var successRate float64
	if total > 0 {
		successRate = float64(successes) / float64(total)
	}

	// Top countries by request count
	countryQuery := `
		SELECT client_country, count() AS cnt
		FROM periscope.routing_decisions
		WHERE tenant_id = ? AND timestamp >= ? AND timestamp <= ?
	`
	countryArgs := []any{tenantID, startTime, endTime}
	if streamID := req.GetStreamId(); streamID != "" {
		countryQuery += " AND stream_id = ?"
		countryArgs = append(countryArgs, streamID)
	}
	countryQuery += " GROUP BY client_country ORDER BY cnt DESC LIMIT 10"

	rows, err := s.clickhouse.QueryContext(ctx, countryQuery, countryArgs...)
	if err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var countries []*periscopepb.RoutingCountryStat
	for rows.Next() {
		var code string
		var cnt int64
		if err := rows.Scan(&code, &cnt); err != nil {
			continue
		}
		countries = append(countries, &periscopepb.RoutingCountryStat{
			CountryCode:  code,
			RequestCount: cnt,
		})
	}

	return &periscopepb.GetRoutingEfficiencyResponse{
		Summary: &periscopepb.RoutingEfficiencySummary{
			TotalDecisions:     total,
			SuccessCount:       successes,
			SuccessRate:        successRate,
			AvgRoutingDistance: avgDist,
			AvgLatencyMs:       avgLat,
			TopCountries:       countries,
		},
	}, nil
}

// ============================================================================
// Cluster Traffic Matrix (routing_cluster_hourly MV)
// ============================================================================

func (s *PeriscopeServer) GetClusterTrafficMatrix(ctx context.Context, req *periscopepb.GetClusterTrafficMatrixRequest) (*periscopepb.GetClusterTrafficMatrixResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	query := `
		SELECT
			cluster_id, remote_cluster_id,
			sum(event_count) AS total_events,
			sum(success_count) AS total_successes,
			sum(sum_latency_ms) / greatest(total_events, 1) AS avg_latency_ms,
			sum(sum_distance_km) / greatest(total_events, 1) AS avg_distance_km,
			max(max_latency_ms) AS max_latency_ms
		FROM periscope.routing_cluster_hourly
		WHERE tenant_id = ? AND hour >= ? AND hour <= ?
		GROUP BY cluster_id, remote_cluster_id
		ORDER BY total_events DESC
	`

	rows, err := s.clickhouse.QueryContext(ctx, query, tenantID, startTime, endTime)
	if err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var pairs []*periscopepb.ClusterPairTraffic
	for rows.Next() {
		var clusterID, remoteClusterID string
		var eventCount, successCount uint64
		var avgLatency, avgDistance, maxLatency float64
		if err := rows.Scan(&clusterID, &remoteClusterID, &eventCount, &successCount, &avgLatency, &avgDistance, &maxLatency); err != nil {
			s.logger.WithError(err).Warn("Failed to scan cluster traffic matrix row")
			continue
		}
		var successRate float64
		if eventCount > 0 {
			successRate = float64(successCount) / float64(eventCount)
		}
		pairs = append(pairs, &periscopepb.ClusterPairTraffic{
			ClusterId:       clusterID,
			RemoteClusterId: remoteClusterID,
			EventCount:      eventCount,
			SuccessCount:    successCount,
			AvgLatencyMs:    avgLatency,
			AvgDistanceKm:   avgDistance,
			SuccessRate:     successRate,
			MaxLatencyMs:    maxLatency,
		})
	}

	return &periscopepb.GetClusterTrafficMatrixResponse{Pairs: pairs}, nil
}

// ============================================================================
// FederationAnalyticsService Implementation
// ============================================================================

func (s *PeriscopeServer) GetFederationEvents(ctx context.Context, req *periscopepb.GetFederationEventsRequest) (*periscopepb.GetFederationEventsResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	limit := int32(100)
	if req.GetLimit() > 0 && req.GetLimit() <= 1000 {
		limit = req.GetLimit()
	}

	// Count total matching rows before applying LIMIT
	countQuery := "SELECT count() FROM periscope.federation_events WHERE tenant_id = ? AND timestamp >= ? AND timestamp <= ?"
	countArgs := []any{tenantID, startTime, endTime}
	if eventType := req.GetEventType(); eventType != "" {
		countQuery += " AND event_type = ?"
		countArgs = append(countArgs, eventType)
	}
	var totalCount int32
	if countErr := s.clickhouse.QueryRowContext(ctx, countQuery, countArgs...).Scan(&totalCount); countErr != nil {
		s.logger.WithError(countErr).Warn("Failed to get federation events total count")
	}

	query := `
		SELECT
			timestamp, event_type, local_cluster, remote_cluster,
			stream_name, stream_id, source_node, dest_node, dtsc_url,
			latency_ms, time_to_live_ms, failure_reason,
			queried_clusters, responding_clusters, total_candidates,
			best_remote_score, peer_cluster, role, reason,
			local_lat, local_lon, remote_lat, remote_lon
		FROM periscope.federation_events
		WHERE tenant_id = ? AND timestamp >= ? AND timestamp <= ?
	`
	args := []any{tenantID, startTime, endTime}

	if eventType := req.GetEventType(); eventType != "" {
		query += " AND event_type = ?"
		args = append(args, eventType)
	}

	query += " ORDER BY timestamp DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var events []*periscopepb.FederationEvent
	for rows.Next() {
		var ts time.Time
		var eventType, localCluster, remoteCluster string
		var streamName, streamID, sourceNode, destNode, dtscURL sql.NullString
		var latencyMs, timeToLiveMs sql.NullFloat64
		var failureReason sql.NullString
		var queriedClusters, respondingClusters, totalCandidates sql.NullInt32
		var bestRemoteScore sql.NullInt64
		var peerCluster sql.NullString
		var role string
		var reason sql.NullString
		var localLat, localLon, remoteLat, remoteLon sql.NullFloat64

		if err := rows.Scan(&ts, &eventType, &localCluster, &remoteCluster,
			&streamName, &streamID, &sourceNode, &destNode, &dtscURL,
			&latencyMs, &timeToLiveMs, &failureReason,
			&queriedClusters, &respondingClusters, &totalCandidates,
			&bestRemoteScore, &peerCluster, &role, &reason,
			&localLat, &localLon, &remoteLat, &remoteLon); err != nil {
			s.logger.WithError(err).Warn("Failed to scan federation event row")
			continue
		}

		evt := &periscopepb.FederationEvent{
			Timestamp:     timestamppb.New(ts),
			EventType:     eventType,
			LocalCluster:  localCluster,
			RemoteCluster: remoteCluster,
			Role:          role,
		}
		if streamName.Valid && streamName.String != "" {
			evt.StreamName = &streamName.String
		}
		if streamID.Valid && streamID.String != "" {
			evt.StreamId = &streamID.String
		}
		if sourceNode.Valid {
			evt.SourceNode = &sourceNode.String
		}
		if destNode.Valid {
			evt.DestNode = &destNode.String
		}
		if dtscURL.Valid {
			evt.DtscUrl = &dtscURL.String
		}
		if latencyMs.Valid {
			v := sanitizeFloat32(latencyMs.Float64)
			evt.LatencyMs = &v
		}
		if timeToLiveMs.Valid {
			v := sanitizeFloat32(timeToLiveMs.Float64)
			evt.TimeToLiveMs = &v
		}
		if failureReason.Valid && failureReason.String != "" {
			evt.FailureReason = &failureReason.String
		}
		if queriedClusters.Valid {
			v := uint32(queriedClusters.Int32)
			evt.QueriedClusters = &v
		}
		if respondingClusters.Valid {
			v := uint32(respondingClusters.Int32)
			evt.RespondingClusters = &v
		}
		if totalCandidates.Valid {
			v := uint32(totalCandidates.Int32)
			evt.TotalCandidates = &v
		}
		if bestRemoteScore.Valid {
			v := uint64(bestRemoteScore.Int64)
			evt.BestRemoteScore = &v
		}
		if peerCluster.Valid {
			evt.PeerCluster = &peerCluster.String
		}
		if reason.Valid {
			evt.Reason = &reason.String
		}
		if localLat.Valid {
			evt.LocalLatitude = &localLat.Float64
		}
		if localLon.Valid {
			evt.LocalLongitude = &localLon.Float64
		}
		if remoteLat.Valid {
			evt.RemoteLatitude = &remoteLat.Float64
		}
		if remoteLon.Valid {
			evt.RemoteLongitude = &remoteLon.Float64
		}

		events = append(events, evt)
	}

	return &periscopepb.GetFederationEventsResponse{
		Events:     events,
		TotalCount: totalCount,
	}, nil
}

func (s *PeriscopeServer) GetFederationSummary(ctx context.Context, req *periscopepb.GetFederationSummaryRequest) (*periscopepb.GetFederationSummaryResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	query := `
		SELECT
			event_type,
			sum(event_count) AS total,
			sum(failure_count) AS failures,
			sum(sum_latency_ms) / greatest(sum(event_count), 1) AS avg_latency_ms
		FROM periscope.federation_hourly
		WHERE tenant_id = ? AND hour >= ? AND hour <= ?
		GROUP BY event_type
		ORDER BY total DESC
	`

	rows, err := s.clickhouse.QueryContext(ctx, query, tenantID, startTime, endTime)
	if err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var counts []*periscopepb.FederationEventCount
	var totalEvents, totalFailures uint64
	var totalLatencyWeighted float64
	for rows.Next() {
		var eventType string
		var total, failures uint64
		var avgLatency float64
		if err := rows.Scan(&eventType, &total, &failures, &avgLatency); err != nil {
			s.logger.WithError(err).Warn("Failed to scan federation summary row")
			continue
		}
		counts = append(counts, &periscopepb.FederationEventCount{
			EventType:    eventType,
			Count:        total,
			FailureCount: failures,
			AvgLatencyMs: avgLatency,
		})
		totalEvents += total
		totalFailures += failures
		totalLatencyWeighted += avgLatency * float64(total)
	}

	var overallAvgLatency, overallFailureRate float64
	if totalEvents > 0 {
		overallAvgLatency = totalLatencyWeighted / float64(totalEvents)
		overallFailureRate = float64(totalFailures) / float64(totalEvents)
	}

	return &periscopepb.GetFederationSummaryResponse{
		Summary: &periscopepb.FederationSummary{
			EventCounts:         counts,
			TotalEvents:         totalEvents,
			OverallAvgLatencyMs: overallAvgLatency,
			OverallFailureRate:  overallFailureRate,
		},
	}, nil
}

// ============================================================================
// PlatformAnalyticsService Implementation
// ============================================================================

func (s *PeriscopeServer) GetPlatformOverview(ctx context.Context, req *periscopepb.GetPlatformOverviewRequest) (*periscopepb.GetPlatformOverviewResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}

	// Parse time range from request (defaults to last 24h via validateTimeRangeProto)
	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	resp := &periscopepb.GetPlatformOverviewResponse{
		TenantId:    tenantID,
		GeneratedAt: timestamppb.Now(),
		TimeRange:   &commonpb.TimeRange{Start: timestamppb.New(startTime), End: timestamppb.New(endTime)},
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
		AND timestamp_5m >= ?
		AND timestamp_5m <  ?
	`
	err = s.clickhouse.QueryRowContext(ctx, peakBwQuery, tenantID, startTime, endTime).Scan(&resp.PeakBandwidth)
	if err != nil {
		s.logger.WithError(err).Info("Failed to get peak bandwidth from client_qoe_5m")
	}

	// Get historical metrics from finalized 5-minute viewer facts.
	historicalQuery := `
		SELECT
			COALESCE(sum(down_bytes_observed), 0) / 1073741824.0 as egress_gb,
			COALESCE(sum(seconds_observed), 0) / 3600.0 as viewer_hours,
			COALESCE(uniqExact(viewer_key), 0) as unique_viewers,
			COALESCE(uniqExact(session_key), 0) as total_views,
			COALESCE(max(window_viewers), 0) AS peak_viewers
		FROM (
			SELECT
				u.window_start,
				u.down_bytes_observed,
				u.seconds_observed,
				concat(toString(u.node_id), '|', u.session_id) AS session_key,
				if(s.host != '', s.host, concat(toString(u.node_id), '|', u.session_id)) AS viewer_key,
				uniqExact(concat(toString(u.node_id), '|', u.session_id)) OVER (PARTITION BY u.window_start) AS window_viewers
			FROM periscope.viewer_usage_5m_v AS u
			LEFT JOIN periscope.viewer_sessions_final_v AS s USING (tenant_id, node_id, session_id)
			WHERE u.tenant_id = ?
			  AND u.window_start >= ?
			  AND u.window_start <  ?
		)
	`

	var egressGb, viewerHours float64
	var uniqueViewers, totalViews, peakViewers int64
	err = s.clickhouse.QueryRowContext(ctx, historicalQuery, tenantID, startTime, endTime).Scan(
		&egressGb, &viewerHours, &uniqueViewers, &totalViews, &peakViewers,
	)
	if err == nil {
		resp.EgressGb = egressGb
		resp.ViewerHours = viewerHours
		resp.DeliveredMinutes = viewerHours * 60 // Convenience: viewer_hours * 60
		resp.UniqueViewers = int32(uniqueViewers)
		resp.TotalViews = totalViews
		resp.PeakViewers = int32(peakViewers)
	} else {
		s.logger.WithError(err).Info("Failed to get historical metrics from viewer_usage_5m_v")
	}

	streamHours, peakConcurrent, _, err := s.queryStreamRuntimeSummary(ctx, tenantID, startTime, endTime)
	if err == nil {
		resp.StreamHours = streamHours
		resp.IngestHours = streamHours // Alias for clarity
		resp.PeakConcurrentViewers = peakConcurrent
	} else {
		s.logger.WithError(err).Info("Failed to get stream runtime summary")
	}

	sanitizePlatformOverviewResponse(resp)
	return resp, nil
}

func sanitizePlatformOverviewResponse(resp *periscopepb.GetPlatformOverviewResponse) {
	if resp == nil {
		return
	}
	resp.AverageViewers = sanitizeFloat64(resp.AverageViewers)
	resp.PeakBandwidth = sanitizeFloat64(resp.PeakBandwidth)
	resp.StreamHours = sanitizeFloat64(resp.StreamHours)
	resp.EgressGb = sanitizeFloat64(resp.EgressGb)
	resp.ViewerHours = sanitizeFloat64(resp.ViewerHours)
	resp.DeliveredMinutes = sanitizeFloat64(resp.DeliveredMinutes)
	resp.IngestHours = sanitizeFloat64(resp.IngestHours)
}

// ListTenantActivity returns a cross-tenant activity rollup for the platform
// operator god view (`frameworks admin tenants activity`). There is
// deliberately no tenant scope: only service credentials (which carry no
// user/tenant identity) may call it — the auth interceptor has already
// rejected unauthenticated callers, so IsServiceCall is a positive gate here.
func (s *PeriscopeServer) ListTenantActivity(ctx context.Context, req *periscopepb.ListTenantActivityRequest) (*periscopepb.ListTenantActivityResponse, error) {
	if !middleware.IsServiceCall(ctx) {
		return nil, status.Error(codes.PermissionDenied, "service credentials required")
	}

	startTime := time.Now().Add(-7 * 24 * time.Hour)
	endTime := time.Now()
	if req.GetTimeRange() != nil {
		var err error
		startTime, endTime, err = validateTimeRangeProto(req.GetTimeRange())
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
		}
	}
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 100
	}

	activity := map[string]*periscopepb.TenantActivity{}
	get := func(tenantID string) *periscopepb.TenantActivity {
		if a, ok := activity[tenantID]; ok {
			return a
		}
		a := &periscopepb.TenantActivity{TenantId: tenantID}
		activity[tenantID] = a
		return a
	}
	scanRows := func(query string, scan func(rows *sql.Rows) error, args ...any) error {
		rows, err := s.clickhouse.QueryContext(ctx, query, args...)
		if err != nil {
			return wrapClickhouseError(err, "database error")
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			if err := scan(rows); err != nil {
				s.logger.WithError(err).Warn("Failed to scan tenant activity row")
			}
		}
		return rows.Err()
	}

	// Daily rollups are Date-grain; the end day is inclusive so a range
	// ending "now" still counts today's partial day.
	runtimeQuery := `
		SELECT toString(tenant_id) AS tenant_id,
		       sum(runtime_seconds) / 3600.0 AS ingest_hours,
		       max(day) AS last_stream_day
		FROM stream_runtime_daily
		WHERE day >= toDate(?) AND day <= toDate(?)
		GROUP BY tenant_id
	`
	if err := scanRows(runtimeQuery, func(rows *sql.Rows) error {
		var tenantID string
		var ingestHours float64
		var lastDay time.Time
		if err := rows.Scan(&tenantID, &ingestHours, &lastDay); err != nil {
			return err
		}
		a := get(tenantID)
		a.IngestHours = ingestHours
		a.LastStreamAt = timestamppb.New(lastDay)
		return nil
	}, startTime, endTime); err != nil {
		return nil, err
	}

	viewerQuery := `
		SELECT toString(tenant_id) AS tenant_id,
		       sum(viewer_hours) AS viewer_hours,
		       sum(egress_gb) AS egress_gb,
		       toInt64(uniqCombinedMerge(unique_viewers_state)) AS unique_viewers,
		       toInt64(sum(total_sessions)) AS total_sessions
		FROM tenant_viewer_daily
		WHERE day >= toDate(?) AND day <= toDate(?)
		GROUP BY tenant_id
	`
	if err := scanRows(viewerQuery, func(rows *sql.Rows) error {
		var tenantID string
		var viewerHours, egressGb float64
		var uniqueViewers, totalSessions int64
		if err := rows.Scan(&tenantID, &viewerHours, &egressGb, &uniqueViewers, &totalSessions); err != nil {
			return err
		}
		a := get(tenantID)
		a.ViewerHours = viewerHours
		a.EgressGb = egressGb
		a.UniqueViewers = uniqueViewers
		a.TotalSessions = totalSessions
		return nil
	}, startTime, endTime); err != nil {
		return nil, err
	}

	apiQuery := `
		SELECT toString(tenant_id) AS tenant_id,
		       toInt64(sum(requests)) AS requests,
		       toInt64(sum(errors)) AS errors
		FROM api_usage_daily
		WHERE day >= toDate(?) AND day <= toDate(?)
		GROUP BY tenant_id
	`
	if err := scanRows(apiQuery, func(rows *sql.Rows) error {
		var tenantID string
		var requests, errCount int64
		if err := rows.Scan(&tenantID, &requests, &errCount); err != nil {
			return err
		}
		a := get(tenantID)
		a.ApiRequests = requests
		a.ApiErrors = errCount
		return nil
	}, startTime, endTime); err != nil {
		return nil, err
	}

	liveQuery := `
		SELECT toString(tenant_id) AS tenant_id,
		       toInt32(countIf(status = 'live')) AS live_streams,
		       toInt32(sumIf(current_viewers, status = 'live')) AS current_viewers
		FROM stream_state_current FINAL
		GROUP BY tenant_id
		HAVING live_streams > 0
	`
	if err := scanRows(liveQuery, func(rows *sql.Rows) error {
		var tenantID string
		var liveStreams, currentViewers int32
		if err := rows.Scan(&tenantID, &liveStreams, &currentViewers); err != nil {
			return err
		}
		a := get(tenantID)
		a.LiveStreams = liveStreams
		a.CurrentViewers = currentViewers
		return nil
	}); err != nil {
		return nil, err
	}

	tenants := make([]*periscopepb.TenantActivity, 0, len(activity))
	for _, a := range activity {
		a.IngestHours = sanitizeFloat64(a.IngestHours)
		a.ViewerHours = sanitizeFloat64(a.ViewerHours)
		a.EgressGb = sanitizeFloat64(a.EgressGb)
		tenants = append(tenants, a)
	}
	slices.SortFunc(tenants, func(x, y *periscopepb.TenantActivity) int {
		if x.ViewerHours != y.ViewerHours {
			if x.ViewerHours > y.ViewerHours {
				return -1
			}
			return 1
		}
		if x.IngestHours != y.IngestHours {
			if x.IngestHours > y.IngestHours {
				return -1
			}
			return 1
		}
		return strings.Compare(x.TenantId, y.TenantId)
	})
	if len(tenants) > limit {
		tenants = tenants[:limit]
	}

	return &periscopepb.ListTenantActivityResponse{
		Tenants:     tenants,
		GeneratedAt: timestamppb.Now(),
		TimeRange:   &commonpb.TimeRange{Start: timestamppb.New(startTime), End: timestamppb.New(endTime)},
	}, nil
}

// GetNetworkLiveStats returns platform-wide live stats per cluster (no tenant filter).
func (s *PeriscopeServer) GetNetworkLiveStats(ctx context.Context, _ *periscopepb.GetNetworkLiveStatsRequest) (*periscopepb.GetNetworkLiveStatsResponse, error) {
	type clusterStats struct {
		activeStreams     int32
		viewers           int32
		uploadBPS         uint64
		downloadBPS       uint64
		egressCapacityBPS uint64
		activeNodes       int32
	}
	statsMap := make(map[string]*clusterStats)

	// Per-cluster node stats. Capacity sum excludes maintenance/draining nodes
	// (they still report load but don't contribute available delivery capacity).
	nodeQuery := `
		SELECT
			cluster_id,
			COALESCE(sum(up_speed), 0),
			COALESCE(sum(down_speed), 0),
			COALESCE(sumIf(bw_limit, is_healthy = 1 AND operational_mode = 'normal'), 0),
			countIf(is_healthy = 1)
		FROM periscope.node_state_current FINAL
		GROUP BY cluster_id
	`
	rows, err := s.clickhouse.QueryContext(ctx, nodeQuery)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to query node stats: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id string
		var cs clusterStats
		if scanErr := rows.Scan(&id, &cs.uploadBPS, &cs.downloadBPS, &cs.egressCapacityBPS, &cs.activeNodes); scanErr != nil {
			s.logger.WithError(scanErr).Warn("GetNetworkLiveStats: scan node row")
			continue
		}
		statsMap[id] = &cs
	}

	// Per-cluster stream counts + viewer counts from stream_state_current (authoritative source)
	streamQuery := `
		SELECT
			n.cluster_id,
			toInt32(count(*)),
			COALESCE(sum(s.current_viewers), 0)
		FROM periscope.stream_state_current AS s FINAL
		INNER JOIN periscope.node_state_current AS n FINAL ON s.node_id = n.node_id
		WHERE s.status = 'live'
		GROUP BY n.cluster_id
	`
	sRows, err := s.clickhouse.QueryContext(ctx, streamQuery)
	if err != nil {
		s.logger.WithError(err).Warn("GetNetworkLiveStats: stream query failed")
	} else {
		defer sRows.Close()
		for sRows.Next() {
			var id string
			var streams, viewers int32
			if err := sRows.Scan(&id, &streams, &viewers); err != nil {
				continue
			}
			if cs, ok := statsMap[id]; ok {
				cs.activeStreams = streams
				cs.viewers = viewers
			} else {
				statsMap[id] = &clusterStats{activeStreams: streams, viewers: viewers}
			}
		}
	}

	resp := &periscopepb.GetNetworkLiveStatsResponse{}
	for id, cs := range statsMap {
		resp.Clusters = append(resp.Clusters, &periscopepb.NetworkClusterLiveStats{
			ClusterId:           id,
			ActiveStreams:       cs.activeStreams,
			CurrentViewers:      cs.viewers,
			UploadBytesPerSec:   cs.uploadBPS,
			DownloadBytesPerSec: cs.downloadBPS,
			ActiveNodes:         cs.activeNodes,
			EgressCapacityBps:   cs.egressCapacityBPS,
		})
	}

	return resp, nil
}

func (s *PeriscopeServer) GetClipEvents(ctx context.Context, req *periscopepb.GetClipEventsRequest) (*periscopepb.GetClipEventsResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
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
	countArgs := []any{tenantID, startTime, endTime}
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
	args := []any{tenantID, startTime, endTime}

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
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var events []*periscopepb.ClipEvent
	for rows.Next() {
		var event periscopepb.ClipEvent
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

	return &periscopepb.GetClipEventsResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Events:     events,
	}, nil
}

// GetArtifactState returns the current state of a single artifact (clip/DVR)
func (s *PeriscopeServer) GetArtifactState(ctx context.Context, req *periscopepb.GetArtifactStateRequest) (*periscopepb.GetArtifactStateResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}

	requestID := req.GetRequestId()
	if requestID == "" {
		return nil, status.Error(codes.InvalidArgument, "request_id required")
	}

	query := `
		SELECT tenant_id, request_id, stream_id, content_type, stage,
		       progress_percent, error_message, requested_at, started_at, completed_at,
		       clip_start_unix, clip_stop_unix, segment_count, manifest_path,
		       file_path, s3_url, size_bytes, processing_node_id, updated_at, expires_at,
		       storage_location, sync_status, is_hot, is_synced, is_finalized, is_frozen
		FROM artifact_state_current FINAL
		WHERE tenant_id = ? AND request_id = ?
	`

	var artifact periscopepb.ArtifactState
	var errorMessage, manifestPath, filePath, s3URL, processingNodeID, storageLocation, syncStatus *string
	var startedAt, completedAt *time.Time
	var clipStartUnix, clipStopUnix *int64
	var segmentCount *uint32
	var sizeBytes *uint64
	var requestedAt, updatedAt time.Time
	var expiresAt *time.Time
	var progressPercent uint8
	var isHot, isSynced, isFinalized, isFrozen *bool

	err = s.clickhouse.QueryRowContext(ctx, query, tenantID, requestID).Scan(
		&artifact.TenantId, &artifact.RequestId, &artifact.StreamId, &artifact.ContentType, &artifact.Stage,
		&progressPercent, &errorMessage, &requestedAt, &startedAt, &completedAt,
		&clipStartUnix, &clipStopUnix, &segmentCount, &manifestPath,
		&filePath, &s3URL, &sizeBytes, &processingNodeID, &updatedAt, &expiresAt,
		&storageLocation, &syncStatus, &isHot, &isSynced, &isFinalized, &isFrozen,
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
	artifact.StorageLocation = storageLocation
	artifact.SyncStatus = syncStatus
	artifact.IsHot = isHot
	artifact.IsSynced = isSynced
	artifact.IsFinalized = isFinalized
	artifact.IsFrozen = isFrozen

	return &periscopepb.GetArtifactStateResponse{
		Artifact: &artifact,
	}, nil
}

// GetArtifactStates returns a list of artifact states with optional filtering
func (s *PeriscopeServer) GetArtifactStates(ctx context.Context, req *periscopepb.GetArtifactStatesRequest) (*periscopepb.GetArtifactStatesResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
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
	countArgs := []any{tenantID}
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
		       file_path, s3_url, size_bytes, processing_node_id, updated_at, expires_at,
		       storage_location, sync_status, is_hot, is_synced, is_finalized, is_frozen
		FROM artifact_state_current FINAL
		WHERE tenant_id = ?
	`
	args := []any{tenantID}

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
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var artifacts []*periscopepb.ArtifactState
	artifactIndex := make(map[string]int)
	for rows.Next() {
		artifact := &periscopepb.ArtifactState{}
		var errorMessage, manifestPath, filePath, s3URL, processingNodeID, storageLocation, syncStatus *string
		var startedAt, completedAt *time.Time
		var clipStartUnix, clipStopUnix *int64
		var segmentCount *uint32
		var sizeBytes *uint64
		var requestedAt, updatedAt time.Time
		var expiresAt *time.Time
		var progressPercent uint8
		var isHot, isSynced, isFinalized, isFrozen *bool

		err := rows.Scan(
			&artifact.TenantId, &artifact.RequestId, &artifact.StreamId, &artifact.ContentType, &artifact.Stage,
			&progressPercent, &errorMessage, &requestedAt, &startedAt, &completedAt,
			&clipStartUnix, &clipStopUnix, &segmentCount, &manifestPath,
			&filePath, &s3URL, &sizeBytes, &processingNodeID, &updatedAt, &expiresAt,
			&storageLocation, &syncStatus, &isHot, &isSynced, &isFinalized, &isFrozen,
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
		artifact.StorageLocation = storageLocation
		artifact.SyncStatus = syncStatus
		artifact.IsHot = isHot
		artifact.IsSynced = isSynced
		artifact.IsFinalized = isFinalized
		artifact.IsFrozen = isFrozen

		if idx, ok := artifactIndex[artifact.GetRequestId()]; ok {
			if preferArtifactState(artifact, artifacts[idx]) {
				artifacts[idx] = artifact
			}
			continue
		}
		artifactIndex[artifact.GetRequestId()] = len(artifacts)
		artifacts = append(artifacts, artifact)
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

	return &periscopepb.GetArtifactStatesResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Artifacts:  artifacts,
	}, nil
}

func preferArtifactState(candidate, current *periscopepb.ArtifactState) bool {
	if candidate == nil {
		return false
	}
	if current == nil {
		return true
	}
	candidateTime := artifactUpdatedAt(candidate)
	currentTime := artifactUpdatedAt(current)
	if !candidateTime.Equal(currentTime) {
		return candidateTime.After(currentTime)
	}
	return artifactStageRank(candidate.GetStage()) > artifactStageRank(current.GetStage())
}

func artifactUpdatedAt(artifact *periscopepb.ArtifactState) time.Time {
	if artifact == nil || artifact.GetUpdatedAt() == nil {
		return time.Time{}
	}
	return artifact.GetUpdatedAt().AsTime()
}

func artifactStageRank(stage string) int {
	switch strings.ToLower(stage) {
	case "failed", "failed_terminal", "error", "deleted", "evicted", "lost_local":
		return 100
	case "completed", "complete", "done", "ready", "synced":
		return 90
	case "processing":
		return 60
	case "queued", "progress":
		return 40
	case "requested", "uploading", "started", "recording":
		return 20
	default:
		return 0
	}
}

// ============================================================================
// AggregatedAnalyticsService Implementation (Materialized Views)
// ============================================================================

// GetStreamConnectionHourly returns hourly connection aggregates from stream_connection_hourly MV
func (s *PeriscopeServer) GetStreamConnectionHourly(ctx context.Context, req *periscopepb.GetStreamConnectionHourlyRequest) (*periscopepb.GetStreamConnectionHourlyResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
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
	countArgs := []any{tenantID, startTime, endTime}
	if streamID != "" {
		countQuery += " AND stream_id = ?"
		countArgs = append(countArgs, streamID)
	}
	countCh := s.countAsync(ctx, countQuery, countArgs...)

	// AggregatingMergeTree requires Merge functions to read aggregated values
	query := `
		SELECT hour, tenant_id, stream_id,
		       sum(total_bytes) as total_bytes,
		       uniqCombinedMerge(unique_viewers_state) as unique_viewers,
		       sum(total_sessions) as total_sessions
		FROM stream_connection_hourly
		WHERE tenant_id = ? AND hour >= ? AND hour <= ?
	`
	args := []any{tenantID, startTime, endTime}

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
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var records []*periscopepb.StreamConnectionHourly
	for rows.Next() {
		var hour time.Time
		var tenantIDStr, streamID string
		var totalBytes, uniqueViewers, totalSessions uint64

		err := rows.Scan(&hour, &tenantIDStr, &streamID, &totalBytes, &uniqueViewers, &totalSessions)
		if err != nil {
			s.logger.WithError(err).Error("Failed to scan stream_connection_hourly row")
			continue
		}

		records = append(records, &periscopepb.StreamConnectionHourly{
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

	return &periscopepb.GetStreamConnectionHourlyResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Records:    records,
	}, nil
}

// GetClientMetrics5M returns 5-minute client metrics aggregates from client_qoe_5m MV
func (s *PeriscopeServer) GetClientMetrics5M(ctx context.Context, req *periscopepb.GetClientMetrics5MRequest) (*periscopepb.GetClientMetrics5MResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
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
	countArgs := []any{tenantID, startTime, endTime}
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
	args := []any{tenantID, startTime, endTime}

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
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var records []*periscopepb.ClientMetrics5M
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

		record := &periscopepb.ClientMetrics5M{
			Id:                fmt.Sprintf("%s_%s_%s", timestamp.Format(time.RFC3339), streamIDStr, nodeIDStr),
			Timestamp:         timestamppb.New(timestamp),
			TenantId:          tenantIDStr,
			StreamId:          streamIDStr,
			NodeId:            nodeIDStr,
			ActiveSessions:    activeSessions,
			AvgBandwidthIn:    sanitizeFloat64(avgBwIn),
			AvgBandwidthOut:   sanitizeFloat64(avgBwOut),
			AvgConnectionTime: sanitizeFloat32(float64(avgConnTime)),
		}
		if pktLossRate.Valid {
			v := sanitizeFloat32(pktLossRate.Float64)
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

	return &periscopepb.GetClientMetrics5MResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Records:    records,
	}, nil
}

// GetQualityTierDaily returns daily quality tier distribution from quality_tier_daily MV
func (s *PeriscopeServer) GetQualityTierDaily(ctx context.Context, req *periscopepb.GetQualityTierDailyRequest) (*periscopepb.GetQualityTierDailyResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
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
	countArgs := []any{tenantID, startTime, endTime}
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
	args := []any{tenantID, startTime, endTime}

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
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var records []*periscopepb.QualityTierDaily
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

		records = append(records, &periscopepb.QualityTierDaily{
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

	return &periscopepb.GetQualityTierDailyResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Records:    records,
	}, nil
}

// GetStreamAnalyticsSummary returns range aggregates derived from materialized views only.
func (s *PeriscopeServer) GetStreamAnalyticsSummary(ctx context.Context, req *periscopepb.GetStreamAnalyticsSummaryRequest) (*periscopepb.GetStreamAnalyticsSummaryResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}

	streamID := req.GetStreamId()
	if streamID == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_id required")
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	summary := &periscopepb.StreamAnalyticsSummary{
		TenantId:  tenantID,
		StreamId:  streamID,
		TimeRange: req.GetTimeRange(),
	}
	// Ensure non-null GraphQL contract for rangeQuality even if the query returns no rows.
	summary.RangeQuality = &periscopepb.QualityTierSummary{}
	var totalSessionsVal int64
	var totalSessionSecondsVal int64
	var totalBytesVal int64

	// Viewer concurrency summary from finalized viewer usage windows.
	{
		var avgViewers sql.NullFloat64
		var peakViewers sql.NullInt64
		err := s.clickhouse.QueryRowContext(ctx, `
			SELECT avg(viewer_count), max(viewer_count)
			FROM (
				SELECT window_start, toInt64(uniqExact(node_id, session_id)) AS viewer_count
				FROM periscope.viewer_usage_5m_v
				WHERE tenant_id = ? AND stream_id = ? AND window_start >= ? AND window_start < ?
				GROUP BY window_start
			)
		`, tenantID, streamID, startTime, endTime).Scan(&avgViewers, &peakViewers)
		if err == nil {
			if avgViewers.Valid {
				summary.RangeAvgViewers = sanitizeFloat32(avgViewers.Float64)
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
				summary.RangeAvgBufferHealth = sanitizeFloat32(avgBufferHealth.Float64)
			}
			if avgBitrate.Valid {
				safe := sanitizeFloat64(avgBitrate.Float64)
				if safe < 0 {
					safe = 0
				}
				summary.RangeAvgBitrate = uint32(safe)
			}
			if avgFps.Valid {
				summary.RangeAvgFps = sanitizeFloat32(avgFps.Float64)
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
				summary.RangePacketLossRate = sanitizeFloat32(pktLossRate.Float64)
			}
			if avgConnTime.Valid {
				summary.RangeAvgConnectionTime = sanitizeFloat32(avgConnTime.Float64)
			}
		}
	}

	// Viewer duration and session counts combine finalized 5-minute usage with
	// current session facts that have not appeared in the usage ledger yet.
	{
		var totalSessionSeconds, totalBytes, egressBytes, uniqueViewers, totalSessions sql.NullInt64
		err := s.clickhouse.QueryRowContext(ctx, `
			SELECT
				toInt64(COALESCE(sum(seconds_observed), 0)),
				toInt64(COALESCE(sum(total_bytes_observed), 0)),
				toInt64(COALESCE(sum(down_bytes_observed), 0)),
				toInt64(uniqExact(session_key)),
				toInt64(uniqExact(session_key))
			FROM (
				SELECT
					toUInt64(seconds_observed) AS seconds_observed,
					toUInt64(up_bytes_observed + down_bytes_observed) AS total_bytes_observed,
					toUInt64(down_bytes_observed) AS down_bytes_observed,
					concat(toString(node_id), '|', session_id) AS session_key
				FROM periscope.viewer_usage_5m_v
				WHERE tenant_id = ? AND stream_id = ? AND window_start >= ? AND window_start < ?
				UNION ALL
				SELECT
					toUInt64(greatest(0, dateDiff('second', greatest(ifNull(connected_at, disconnected_at), ?), least(ifNull(disconnected_at, now()), ?)))) AS seconds_observed,
					toUInt64(0) AS total_bytes_observed,
					toUInt64(0) AS down_bytes_observed,
					concat(toString(node_id), '|', session_id) AS session_key
				FROM periscope.viewer_sessions_current FINAL
				WHERE tenant_id = ? AND stream_id = ?
				  AND ifNull(connected_at, disconnected_at) < ?
				  AND ifNull(disconnected_at, now()) >= ?
				  AND (tenant_id, node_id, session_id) NOT IN (
				      SELECT tenant_id, node_id, session_id
				      FROM periscope.viewer_usage_5m_v
				      WHERE tenant_id = ? AND stream_id = ? AND window_start >= ? AND window_start < ?
				  )
			)
		`, tenantID, streamID, startTime, endTime, startTime, endTime, tenantID, streamID, endTime, startTime, tenantID, streamID, startTime, endTime).Scan(&totalSessionSeconds, &totalBytes, &egressBytes, &uniqueViewers, &totalSessions)
		if err == nil {
			if totalSessionSeconds.Valid {
				summary.RangeViewerHours = float32(totalSessionSeconds.Int64) / 3600.0
				totalSessionSecondsVal = totalSessionSeconds.Int64
			}
			if totalBytes.Valid {
				totalBytesVal = totalBytes.Int64
			}
			if egressBytes.Valid {
				summary.RangeEgressGb = float32(float64(egressBytes.Int64) / (1024.0 * 1024.0 * 1024.0))
			}
			if uniqueViewers.Valid {
				summary.RangeUniqueViewers = uniqueViewers.Int64
			}
			if totalSessions.Valid {
				summary.RangeTotalSessions = totalSessions.Int64
				totalSessionsVal = totalSessions.Int64
			}
		}
	}

	// Current session geography is available before finalized usage ledgers.
	{
		var uniqueCountries sql.NullInt64
		err := s.clickhouse.QueryRowContext(ctx, `
			SELECT uniqExact(country_code)
			FROM periscope.viewer_sessions_current FINAL
			WHERE tenant_id = ? AND stream_id = ?
			  AND ifNull(connected_at, disconnected_at) <= ?
			  AND ifNull(disconnected_at, now()) >= ?
			  AND country_code != ''
		`, tenantID, streamID, endTime, startTime).Scan(&uniqueCountries)
		if err == nil && uniqueCountries.Valid {
			summary.RangeUniqueCountries = int32(uniqueCountries.Int64)
		}
	}

	if totalSessionsVal > 0 {
		summary.RangeAvgSessionSeconds = float32(totalSessionSecondsVal) / float32(totalSessionsVal)
		summary.RangeAvgBytesPerSession = float32(totalBytesVal) / float32(totalSessionsVal)
		summary.RangeTotalViews = totalSessionsVal
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
			summary.RangeQuality = &periscopepb.QualityTierSummary{
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

	return &periscopepb.GetStreamAnalyticsSummaryResponse{Summary: summary}, nil
}

// GetStreamAnalyticsSummaries returns bulk stream analytics summaries with share percentages.
// This aggregates finalized viewer usage facts for all streams in a tenant, sorted by the requested field.
// Uses keyset pagination with raw integer sort keys (egress_bytes, viewer_seconds) for precision.
func (s *PeriscopeServer) GetStreamAnalyticsSummaries(ctx context.Context, req *periscopepb.GetStreamAnalyticsSummariesRequest) (*periscopepb.GetStreamAnalyticsSummariesResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
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
	case periscopepb.StreamSummarySortField_STREAM_SUMMARY_SORT_FIELD_UNIQUE_VIEWERS:
		sortField = "unique_viewers"
	case periscopepb.StreamSummarySortField_STREAM_SUMMARY_SORT_FIELD_TOTAL_VIEWS:
		sortField = "total_views"
	case periscopepb.StreamSummarySortField_STREAM_SUMMARY_SORT_FIELD_VIEWER_HOURS:
		sortField = "viewer_seconds"
	default:
		sortField = "egress_bytes"
	}

	sortDesc := req.GetSortOrder() != commonpb.SortOrder_SORT_ORDER_ASC
	sortOrder := "DESC"
	if !sortDesc {
		sortOrder = "ASC"
	}

	// Build keyset WHERE clause if cursor provided
	// Cursor stores raw int64 sort_key + stream_id
	keysetWhere := ""
	var keysetArgs []any
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
		keysetArgs = []any{cursorSortKey, cursorStreamID}
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

	// Query aggregates from finalized 5-minute viewer usage facts. Raw integer
	// columns (egress_bytes, viewer_seconds) are used for sort/keyset precision;
	// derived columns (egress_gb, viewer_hours) are for display only.
	query := fmt.Sprintf(`
		WITH stream_totals AS (
			SELECT
				stream_id,
				toInt64(uniqExact(node_id, session_id)) AS total_views,
				toInt64(uniqExact(node_id, session_id)) AS unique_viewers,
				toInt64(sum(down_bytes_observed)) AS egress_bytes,
				toInt64(sum(seconds_observed)) AS viewer_seconds
			FROM periscope.viewer_usage_5m_v
			WHERE tenant_id = ? AND window_start >= ? AND window_start < ?
			GROUP BY stream_id
		),
		combined AS (
			SELECT
				stream_id,
				total_views,
				unique_viewers,
				egress_bytes,
				viewer_seconds
			FROM stream_totals
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
	args := []any{
		tenantID, startTime, endTime,
	}
	args = append(args, keysetArgs...)
	args = append(args, params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var summaries []*periscopepb.StreamAnalyticsSummary

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

		summary := &periscopepb.StreamAnalyticsSummary{
			TenantId:           tenantID,
			StreamId:           streamID,
			TimeRange:          req.GetTimeRange(),
			RangeTotalViews:    totalViews,
			RangeUniqueViewers: uniqueViewers,
			RangeEgressBytes:   egressBytes,
			RangeViewerSeconds: viewerSeconds,
			RangeQuality:       &periscopepb.QualityTierSummary{},
		}

		if egressGb.Valid {
			summary.RangeEgressGb = sanitizeFloat32(egressGb.Float64)
		}
		if viewerHours.Valid {
			summary.RangeViewerHours = sanitizeFloat32(viewerHours.Float64)
		}
		if egressSharePct.Valid {
			val := sanitizeFloat32(egressSharePct.Float64)
			summary.RangeEgressSharePercent = &val
		}
		if viewersSharePct.Valid {
			val := sanitizeFloat32(viewersSharePct.Float64)
			summary.RangeViewerSharePercent = &val
		}
		if viewerHoursSharePct.Valid {
			val := sanitizeFloat32(viewerHoursSharePct.Float64)
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
		FROM periscope.viewer_usage_5m_v
		WHERE tenant_id = ? AND window_start >= ? AND window_start < ?
	`, tenantID, startTime, endTime)
	if err := countRow.Scan(&totalCount); err != nil {
		s.logger.WithError(err).Warn("Failed to get stream analytics summaries total count")
	}

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

	resp := &periscopepb.GetStreamAnalyticsSummariesResponse{
		Pagination: &commonpb.CursorPaginationResponse{
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
func buildStreamSummaryCursor(summary *periscopepb.StreamAnalyticsSummary, sortField string) string {
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
func (s *PeriscopeServer) GetStorageUsage(ctx context.Context, req *periscopepb.GetStorageUsageRequest) (*periscopepb.GetStorageUsageResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
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
	countArgs := []any{tenantID, startTime, endTime}
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
	args := []any{tenantID, startTime, endTime}

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
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var records []*periscopepb.StorageUsageRecord
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
			return nil, status.Errorf(codes.Internal, "scan storage usage: %v", err)
		}

		idKey := fmt.Sprintf("%s:%s", storageScopeStr, nodeIDStr)
		records = append(records, &periscopepb.StorageUsageRecord{
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
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "iterate storage usage: %v", err)
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

	return &periscopepb.GetStorageUsageResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Records:    records,
	}, nil
}

// GetStorageEvents returns storage lifecycle events (freeze + read-through cache fill operations) from storage_events table
func (s *PeriscopeServer) GetStorageEvents(ctx context.Context, req *periscopepb.GetStorageEventsRequest) (*periscopepb.GetStorageEventsResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
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
	countArgs := []any{tenantID, startTime, endTime}
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
	args := []any{tenantID, startTime, endTime}

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
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var events []*periscopepb.StorageEvent
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

		event := &periscopepb.StorageEvent{
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

	return &periscopepb.GetStorageEventsResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Events:     events,
	}, nil
}

// ============================================================================
// Stream Health 5-Minute Aggregates
// ============================================================================

// GetStreamHealth5M returns 5-minute aggregated health metrics from stream_health_5m MV
func (s *PeriscopeServer) GetStreamHealth5M(ctx context.Context, req *periscopepb.GetStreamHealth5MRequest) (*periscopepb.GetStreamHealth5MResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
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
	args := []any{tenantID, streamID, startTime, endTime}

	keysetCond, keysetArgs := buildKeysetCondition(params, "timestamp_5m", "node_id")
	if keysetCond != "" {
		query += keysetCond
		args = append(args, keysetArgs...)
	}

	query += buildOrderBy(params, "timestamp_5m", "node_id")
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var records []*periscopepb.StreamHealth5M
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

		record := &periscopepb.StreamHealth5M{
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
			v := sanitizeFloat32(avgFrameJitterMs.Float64)
			record.AvgFrameJitterMs = &v
		}
		if maxFrameJitterMs.Valid {
			v := sanitizeFloat32(maxFrameJitterMs.Float64)
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

	return &periscopepb.GetStreamHealth5MResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Records:    records,
	}, nil
}

// GetStreamHealthSummary returns pre-aggregated health stats from stream_health_5m.
func (s *PeriscopeServer) GetStreamHealthSummary(ctx context.Context, req *periscopepb.GetStreamHealthSummaryRequest) (*periscopepb.GetStreamHealthSummaryResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	query := `
		SELECT
			avg(avg_bitrate) AS avg_bitrate,
			avg(avg_fps) AS avg_fps,
			avg(avg_buffer_health) AS avg_buffer_health,
			sum(rebuffer_count) AS total_rebuffers,
			sum(issue_count) AS total_issues,
			count() AS samples,
			countIf(issue_count > 0) AS issue_samples,
			argMax(quality_tier, timestamp_5m) AS latest_tier
		FROM stream_health_5m
		WHERE tenant_id = ? AND timestamp_5m >= ? AND timestamp_5m <= ?
	`
	args := []any{tenantID, startTime, endTime}

	if streamID := req.GetStreamId(); streamID != "" {
		query += " AND stream_id = ?"
		args = append(args, streamID)
	}

	var avgBitrate, avgFps, avgBufferHealth float64
	var totalRebuffers, totalIssues, samples, issueSamples int64
	var latestTier string
	if err := s.clickhouse.QueryRowContext(ctx, query, args...).Scan(
		&avgBitrate, &avgFps, &avgBufferHealth,
		&totalRebuffers, &totalIssues, &samples, &issueSamples, &latestTier,
	); err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}

	return &periscopepb.GetStreamHealthSummaryResponse{
		Summary: &periscopepb.StreamHealthSummary{
			AvgBitrate:         avgBitrate,
			AvgFps:             avgFps,
			AvgBufferHealth:    avgBufferHealth,
			TotalRebufferCount: totalRebuffers,
			TotalIssueCount:    totalIssues,
			SampleCount:        samples,
			HasActiveIssues:    issueSamples > 0,
			CurrentQualityTier: latestTier,
		},
	}, nil
}

// GetClientQoeSummary returns pre-aggregated client QoE stats from client_qoe_5m.
func (s *PeriscopeServer) GetClientQoeSummary(ctx context.Context, req *periscopepb.GetClientQoeSummaryRequest) (*periscopepb.GetClientQoeSummaryResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	query := `
		SELECT
			avg(pkt_loss_rate) AS avg_pkt_loss,
			max(pkt_loss_rate) AS peak_pkt_loss,
			avg(avg_bw_in) AS avg_bw_in,
			avg(avg_bw_out) AS avg_bw_out,
			avg(avg_connection_time) AS avg_conn_time,
			sum(active_sessions) AS total_sessions
		FROM client_qoe_5m
		WHERE tenant_id = ? AND timestamp_5m >= ? AND timestamp_5m <= ?
	`
	args := []any{tenantID, startTime, endTime}

	if streamID := req.GetStreamId(); streamID != "" {
		query += " AND stream_id = ?"
		args = append(args, streamID)
	}

	var avgPktLoss, peakPktLoss sql.NullFloat64
	var avgBwIn, avgBwOut, avgConnTime float64
	var totalSessions int64
	if err := s.clickhouse.QueryRowContext(ctx, query, args...).Scan(
		&avgPktLoss, &peakPktLoss, &avgBwIn, &avgBwOut, &avgConnTime, &totalSessions,
	); err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}

	summary := &periscopepb.ClientQoeSummary{
		AvgBandwidthIn:      sanitizeFloat64(avgBwIn),
		AvgBandwidthOut:     sanitizeFloat64(avgBwOut),
		AvgConnectionTime:   sanitizeFloat64(avgConnTime),
		TotalActiveSessions: totalSessions,
	}
	if avgPktLoss.Valid {
		v := sanitizeFloat64(avgPktLoss.Float64)
		summary.AvgPacketLossRate = &v
	}
	if peakPktLoss.Valid {
		v := sanitizeFloat64(peakPktLoss.Float64)
		summary.PeakPacketLossRate = &v
	}

	return &periscopepb.GetClientQoeSummaryResponse{
		Summary: summary,
	}, nil
}

// GetPlayerBootSummary returns the tenant-scoped player startup summary. TTF
// percentiles are computed at read time over the raw player_boot_samples table
// (no rollup MV — quantile() is not mergeable in a plain MergeTree). Percentiles
// consider only boots that reached first frame (total_ttf_ms > 0); error/abandon
// counts cover all rows. Diagnostic only.
func (s *PeriscopeServer) GetPlayerBootSummary(ctx context.Context, req *periscopepb.GetPlayerBootSummaryRequest) (*periscopepb.GetPlayerBootSummaryResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	query := `
		SELECT
			count() AS boot_count,
			countIf(outcome = 'error') AS error_count,
			quantileIf(0.5)(total_ttf_ms, total_ttf_ms > 0) AS p50,
			quantileIf(0.95)(total_ttf_ms, total_ttf_ms > 0) AS p95,
			quantileIf(0.99)(total_ttf_ms, total_ttf_ms > 0) AS p99,
			avgIf(gateway_resolve_ms, gateway_resolve_ms > 0) AS avg_gw,
			avgIf(mist_hydrate_ms, mist_hydrate_ms > 0) AS avg_mist,
			avgIf(player_select_ms, player_select_ms > 0) AS avg_sel,
			avgIf(connect_ms, connect_ms > 0) AS avg_conn,
			avgIf(prebuffer_ms, prebuffer_ms > 0) AS avg_pre,
			countIf(positionCaseInsensitive(cdn_cache_status, 'hit') > 0) / nullIf(countIf(cdn_cache_status != ''), 0) AS cache_hit_ratio
		FROM player_boot_samples
		WHERE tenant_id = ? AND timestamp >= ? AND timestamp <= ?
	`
	args := []any{tenantID, startTime, endTime}

	if streamID := req.GetStreamId(); streamID != "" {
		query += " AND stream_id = ?"
		args = append(args, streamID)
	}
	if artifactHash := req.GetArtifactHash(); artifactHash != "" {
		query += " AND artifact_hash = ?"
		args = append(args, artifactHash)
	}

	var bootCount, errorCount int64
	var p50, p95, p99 sql.NullFloat64
	var avgGw, avgMist, avgSel, avgConn, avgPre float64
	var cacheHitRatio sql.NullFloat64
	if err := s.clickhouse.QueryRowContext(ctx, query, args...).Scan(
		&bootCount, &errorCount, &p50, &p95, &p99,
		&avgGw, &avgMist, &avgSel, &avgConn, &avgPre, &cacheHitRatio,
	); err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}

	return &periscopepb.GetPlayerBootSummaryResponse{
		Summary: &periscopepb.PlayerBootSummary{
			BootCount:           bootCount,
			ErrorCount:          errorCount,
			P50TtfMs:            sanitizeFloat64(p50.Float64),
			P95TtfMs:            sanitizeFloat64(p95.Float64),
			P99TtfMs:            sanitizeFloat64(p99.Float64),
			AvgGatewayResolveMs: sanitizeFloat64(avgGw),
			AvgMistHydrateMs:    sanitizeFloat64(avgMist),
			AvgPlayerSelectMs:   sanitizeFloat64(avgSel),
			AvgConnectMs:        sanitizeFloat64(avgConn),
			AvgPrebufferMs:      sanitizeFloat64(avgPre),
			CacheHitRatio:       sanitizeFloat64(cacheHitRatio.Float64),
		},
	}, nil
}

// GetPlayerBootTimeSeries returns the tenant-scoped boot-startup summary bucketed
// by toStartOfInterval. TTF percentiles are computed at read time per bucket (a
// single read-time quantile per window — same raw player_boot_samples source as
// the scalar summary, just grouped). Half-open window [start, end) so a boundary
// sample lands in exactly one bucket. boot_count is the per-bucket denominator.
func (s *PeriscopeServer) GetPlayerBootTimeSeries(ctx context.Context, req *periscopepb.GetPlayerBootTimeSeriesRequest) (*periscopepb.GetPlayerBootTimeSeriesResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	query := fmt.Sprintf(`
		SELECT
			toStartOfInterval(timestamp, INTERVAL %s) AS bucket,
			count() AS boot_count,
			quantileIf(0.5)(total_ttf_ms, total_ttf_ms > 0) AS p50,
			quantileIf(0.95)(total_ttf_ms, total_ttf_ms > 0) AS p95,
			quantileIf(0.99)(total_ttf_ms, total_ttf_ms > 0) AS p99
		FROM player_boot_samples
		WHERE tenant_id = ? AND timestamp >= ? AND timestamp < ?
	`, clickhouseInterval(req.GetInterval()))
	args := []any{tenantID, startTime, endTime}
	if streamID := req.GetStreamId(); streamID != "" {
		query += " AND stream_id = ?"
		args = append(args, streamID)
	}
	if artifactHash := req.GetArtifactHash(); artifactHash != "" {
		query += " AND artifact_hash = ?"
		args = append(args, artifactHash)
	}
	query += " GROUP BY bucket ORDER BY bucket ASC"

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var buckets []*periscopepb.PlayerBootTimeSeriesBucket
	for rows.Next() {
		var bucket time.Time
		var bootCount int64
		var p50, p95, p99 sql.NullFloat64
		if err := rows.Scan(&bucket, &bootCount, &p50, &p95, &p99); err != nil {
			s.logger.WithError(err).Error("Failed to scan player boot time-series row")
			continue
		}
		buckets = append(buckets, &periscopepb.PlayerBootTimeSeriesBucket{
			Timestamp: timestamppb.New(bucket),
			BootCount: bootCount,
			P50TtfMs:  sanitizeFloat64(p50.Float64),
			P95TtfMs:  sanitizeFloat64(p95.Float64),
			P99TtfMs:  sanitizeFloat64(p99.Float64),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}
	return &periscopepb.GetPlayerBootTimeSeriesResponse{Buckets: buckets}, nil
}

// GetClusterBootOps returns the operator-facing boot aggregate for clusters the
// caller owns. It reads ONLY token-attributed rows (cluster_attributed = 1) and
// projects no content/stream/session/url/tenant identifiers — it is intentionally
// redacted. Cluster ownership is authorized at the API layer; this method trusts
// the cluster_ids it is given.
func (s *PeriscopeServer) GetClusterBootOps(ctx context.Context, req *periscopepb.GetClusterBootOpsRequest) (*periscopepb.GetClusterBootOpsResponse, error) {
	if _, err := requireTenantID(ctx, req.GetTenantId()); err != nil {
		return nil, err
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	clusterIDs := req.GetClusterIds()
	if len(clusterIDs) == 0 {
		// No owned clusters → nothing to aggregate.
		return &periscopepb.GetClusterBootOpsResponse{}, nil
	}

	placeholders := make([]string, len(clusterIDs))
	args := []any{startTime, endTime}
	for i, id := range clusterIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}

	query := fmt.Sprintf(`
		SELECT
			serving_cluster_id,
			node_id,
			protocol,
			count() AS boot_count,
			countIf(outcome = 'error') AS error_count,
			quantileIf(0.95)(total_ttf_ms, total_ttf_ms > 0) AS p95,
			countIf(positionCaseInsensitive(cdn_cache_status, 'hit') > 0) / nullIf(countIf(cdn_cache_status != ''), 0) AS cache_hit_ratio
		FROM player_boot_samples
		WHERE cluster_attributed = 1
		  AND timestamp >= ? AND timestamp <= ?
		  AND serving_cluster_id IN (%s)
		GROUP BY serving_cluster_id, node_id, protocol
		ORDER BY serving_cluster_id, node_id, protocol
		LIMIT 1000
	`, strings.Join(placeholders, ","))

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var out []*periscopepb.ClusterBootOps
	for rows.Next() {
		var servingClusterID, nodeID, protocol string
		var bootCount, errorCount int64
		var p95, cacheHitRatio sql.NullFloat64
		if err := rows.Scan(&servingClusterID, &nodeID, &protocol, &bootCount, &errorCount, &p95, &cacheHitRatio); err != nil {
			s.logger.WithError(err).Error("Failed to scan player_boot_samples cluster-ops row")
			continue
		}
		out = append(out, &periscopepb.ClusterBootOps{
			ServingClusterId: servingClusterID,
			NodeId:           nodeID,
			Protocol:         protocol,
			BootCount:        bootCount,
			ErrorCount:       errorCount,
			P95TtfMs:         sanitizeFloat64(p95.Float64),
			CacheHitRatio:    sanitizeFloat64(cacheHitRatio.Float64),
		})
	}

	return &periscopepb.GetClusterBootOpsResponse{Rows: out}, nil
}

// GetSessionQoeSummary returns the tenant-scoped viewer-experienced QoE summary.
// client_qoe_session_deltas is a ReplacingMergeTree of additive beacon deltas, so
// it is read with FINAL (collapses double-fired beacons) and ratios are
// sum(numerator)/sum(denominator). Per-session flags (EBVS, mid-stream failure)
// are rolled up per session in an inner query first — otherwise a pre-first-frame
// beacon would mark every session as EBVS. Diagnostic only.
func (s *PeriscopeServer) GetSessionQoeSummary(ctx context.Context, req *periscopepb.GetSessionQoeSummaryRequest) (*periscopepb.GetSessionQoeSummaryResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	filter := "tenant_id = ? AND timestamp >= ? AND timestamp <= ?"
	args := []any{tenantID, startTime, endTime}
	if streamID := req.GetStreamId(); streamID != "" {
		filter += " AND stream_id = ?"
		args = append(args, streamID)
	}
	if artifactHash := req.GetArtifactHash(); artifactHash != "" {
		filter += " AND artifact_hash = ?"
		args = append(args, artifactHash)
	}

	query := fmt.Sprintf(`
		SELECT
			count() AS session_count,
			toFloat64(sum(s_played_ms)) / 3600000.0 AS played_hours,
			sum(s_rebuffer_ms) / nullIf(sum(s_played_ms), 0) AS rebuffering_ratio,
			sum(s_rebuffer_count) / nullIf(toFloat64(sum(s_played_ms)) / 3600000.0, 0) AS rebuffers_per_hour,
			sum(s_rebuffer_ms) / nullIf(sum(s_rebuffer_count), 0) AS avg_rebuffer_ms,
			sum(s_frames_dropped) / nullIf(sum(s_frames_decoded), 0) AS frame_drop_ratio,
			countIf(s_fatal = 1 AND s_first = 1) / nullIf(countIf(s_first = 1), 0) AS playback_failure_rate,
			countIf(s_intent = 1 AND s_first = 0) / nullIf(countIf(s_intent = 1), 0) AS ebvs_rate,
			sum(s_bitrate_bps_seconds) / nullIf(toFloat64(sum(s_played_ms)) / 1000.0, 0) AS avg_bitrate_bps,
			sum(s_abr_switches) / nullIf(toFloat64(sum(s_played_ms)) / 3600000.0, 0) AS abr_switches_per_hour,
			avgIf(s_live_edge, s_live_edge > 0) AS avg_live_edge
		FROM (
			SELECT
				session_id,
				sum(played_ms) AS s_played_ms,
				sum(rebuffer_ms) AS s_rebuffer_ms,
				sum(rebuffer_count) AS s_rebuffer_count,
				sumIf(frames_decoded, frame_stats_supported = 1) AS s_frames_decoded,
				sumIf(frames_dropped, frame_stats_supported = 1) AS s_frames_dropped,
				sum(bitrate_bps_seconds) AS s_bitrate_bps_seconds,
				sum(abr_upswitch_count + abr_downswitch_count) AS s_abr_switches,
				max(fatal_error) AS s_fatal,
				max(first_frame) AS s_first,
				max(play_intent) AS s_intent,
				avgIf(live_edge_latency_ms, live_edge_latency_ms > 0) AS s_live_edge
			FROM client_qoe_session_deltas FINAL
			WHERE %s
			GROUP BY content_id, session_id
		)
	`, filter)

	var sessionCount int64
	var playedHours, rebufRatio, rebufPerHour, avgRebufMs, frameDropRatio sql.NullFloat64
	var failRate, ebvsRate, avgBitrate, abrPerHour, avgLiveEdge sql.NullFloat64
	if err := s.clickhouse.QueryRowContext(ctx, query, args...).Scan(
		&sessionCount, &playedHours, &rebufRatio, &rebufPerHour, &avgRebufMs, &frameDropRatio,
		&failRate, &ebvsRate, &avgBitrate, &abrPerHour, &avgLiveEdge,
	); err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}

	return &periscopepb.GetSessionQoeSummaryResponse{
		Summary: &periscopepb.SessionQoeSummary{
			SessionCount:         sessionCount,
			PlayedHours:          sanitizeFloat64(playedHours.Float64),
			RebufferingRatio:     sanitizeFloat64(rebufRatio.Float64),
			RebuffersPerHour:     sanitizeFloat64(rebufPerHour.Float64),
			AvgRebufferMs:        sanitizeFloat64(avgRebufMs.Float64),
			FrameDropRatio:       sanitizeFloat64(frameDropRatio.Float64),
			PlaybackFailureRate:  sanitizeFloat64(failRate.Float64),
			EbvsRate:             sanitizeFloat64(ebvsRate.Float64),
			AvgBitrateBps:        sanitizeFloat64(avgBitrate.Float64),
			AbrSwitchesPerHour:   sanitizeFloat64(abrPerHour.Float64),
			AvgLiveEdgeLatencyMs: sanitizeFloat64(avgLiveEdge.Float64),
		},
	}, nil
}

// GetSessionQoeTimeSeries returns the tenant-scoped viewer-experienced QoE summary
// bucketed by toStartOfInterval. The inner query rolls additive beacon deltas up
// per (bucket, session) over client_qoe_session_deltas FINAL — same sum-of-deltas
// semantics as the scalar summary — then the outer query computes per-bucket
// ratios. count() over the inner rows is the per-bucket distinct-session count.
// Half-open window [start, end); a session spanning two windows counts in both.
func (s *PeriscopeServer) GetSessionQoeTimeSeries(ctx context.Context, req *periscopepb.GetSessionQoeTimeSeriesRequest) (*periscopepb.GetSessionQoeTimeSeriesResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	filter := "tenant_id = ? AND timestamp >= ? AND timestamp < ?"
	args := []any{tenantID, startTime, endTime}
	if streamID := req.GetStreamId(); streamID != "" {
		filter += " AND stream_id = ?"
		args = append(args, streamID)
	}
	if artifactHash := req.GetArtifactHash(); artifactHash != "" {
		filter += " AND artifact_hash = ?"
		args = append(args, artifactHash)
	}

	query := fmt.Sprintf(`
		SELECT
			bucket,
			count() AS session_count,
			toFloat64(sum(s_played_ms)) / 3600000.0 AS played_hours,
			sum(s_rebuffer_ms) / nullIf(sum(s_played_ms), 0) AS rebuffering_ratio,
			sum(s_frames_dropped) / nullIf(sum(s_frames_decoded), 0) AS frame_drop_ratio,
			sum(s_bitrate_bps_seconds) / nullIf(toFloat64(sum(s_played_ms)) / 1000.0, 0) AS avg_bitrate_bps
		FROM (
			SELECT
				toStartOfInterval(timestamp, INTERVAL %s) AS bucket,
				session_id,
				sum(played_ms) AS s_played_ms,
				sum(rebuffer_ms) AS s_rebuffer_ms,
				sumIf(frames_decoded, frame_stats_supported = 1) AS s_frames_decoded,
				sumIf(frames_dropped, frame_stats_supported = 1) AS s_frames_dropped,
				sum(bitrate_bps_seconds) AS s_bitrate_bps_seconds
			FROM client_qoe_session_deltas FINAL
			WHERE %s
			GROUP BY bucket, content_id, session_id
		)
		GROUP BY bucket ORDER BY bucket ASC
	`, clickhouseInterval(req.GetInterval()), filter)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var buckets []*periscopepb.SessionQoeTimeSeriesBucket
	for rows.Next() {
		var bucket time.Time
		var sessionCount int64
		var playedHours, rebufRatio, frameDropRatio, avgBitrate sql.NullFloat64
		if err := rows.Scan(&bucket, &sessionCount, &playedHours, &rebufRatio, &frameDropRatio, &avgBitrate); err != nil {
			s.logger.WithError(err).Error("Failed to scan session QoE time-series row")
			continue
		}
		buckets = append(buckets, &periscopepb.SessionQoeTimeSeriesBucket{
			Timestamp:        timestamppb.New(bucket),
			SessionCount:     sessionCount,
			PlayedHours:      sanitizeFloat64(playedHours.Float64),
			RebufferingRatio: sanitizeFloat64(rebufRatio.Float64),
			FrameDropRatio:   sanitizeFloat64(frameDropRatio.Float64),
			AvgBitrateBps:    sanitizeFloat64(avgBitrate.Float64),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}
	return &periscopepb.GetSessionQoeTimeSeriesResponse{Buckets: buckets}, nil
}

// GetClusterQoeOps returns the operator-facing QoE aggregate for clusters the
// caller owns — token-attributed rows only (cluster_attributed = 1), redacted to
// serving cluster/node/protocol. Ratios are over the deduped (FINAL) beacon sums.
func (s *PeriscopeServer) GetClusterQoeOps(ctx context.Context, req *periscopepb.GetClusterQoeOpsRequest) (*periscopepb.GetClusterQoeOpsResponse, error) {
	if _, err := requireTenantID(ctx, req.GetTenantId()); err != nil {
		return nil, err
	}
	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}
	clusterIDs := req.GetClusterIds()
	if len(clusterIDs) == 0 {
		return &periscopepb.GetClusterQoeOpsResponse{}, nil
	}

	placeholders := make([]string, len(clusterIDs))
	args := []any{startTime, endTime}
	for i, id := range clusterIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}

	query := fmt.Sprintf(`
		SELECT
			serving_cluster_id,
			node_id,
			protocol,
			uniqExact(content_id, session_id) AS session_count,
			sum(rebuffer_ms) / nullIf(sum(played_ms), 0) AS rebuffering_ratio,
			sumIf(frames_dropped, frame_stats_supported = 1) / nullIf(sumIf(frames_decoded, frame_stats_supported = 1), 0) AS frame_drop_ratio,
			sum(bitrate_bps_seconds) / nullIf(toFloat64(sum(played_ms)) / 1000.0, 0) AS avg_bitrate_bps
		FROM client_qoe_session_deltas FINAL
		WHERE cluster_attributed = 1
		  AND timestamp >= ? AND timestamp <= ?
		  AND serving_cluster_id IN (%s)
		GROUP BY serving_cluster_id, node_id, protocol
		ORDER BY serving_cluster_id, node_id, protocol
		LIMIT 1000
	`, strings.Join(placeholders, ","))

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var out []*periscopepb.ClusterQoeOps
	for rows.Next() {
		var servingClusterID, nodeID, protocol string
		var sessionCount int64
		var rebufRatio, frameDropRatio, avgBitrate sql.NullFloat64
		if err := rows.Scan(&servingClusterID, &nodeID, &protocol, &sessionCount, &rebufRatio, &frameDropRatio, &avgBitrate); err != nil {
			s.logger.WithError(err).Error("Failed to scan client_qoe_session_deltas cluster-ops row")
			continue
		}
		out = append(out, &periscopepb.ClusterQoeOps{
			ServingClusterId: servingClusterID,
			NodeId:           nodeID,
			Protocol:         protocol,
			SessionCount:     sessionCount,
			RebufferingRatio: sanitizeFloat64(rebufRatio.Float64),
			FrameDropRatio:   sanitizeFloat64(frameDropRatio.Float64),
			AvgBitrateBps:    sanitizeFloat64(avgBitrate.Float64),
		})
	}
	return &periscopepb.GetClusterQoeOpsResponse{Rows: out}, nil
}

// GetVodRetention returns the per-bucket retention curve for one artifact. Watch
// density (Σ seconds watched) comes from vod_retention_buckets; the audience-retention
// curve (sessions reaching ≥ bucket ÷ total) is the suffix sum of per-session
// max_bucket_reached from client_qoe_session_deltas. Both read with FINAL.
func (s *PeriscopeServer) GetVodRetention(ctx context.Context, req *periscopepb.GetVodRetentionRequest) (*periscopepb.GetVodRetentionResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	artifactHash := req.GetArtifactHash()
	if artifactHash == "" {
		return nil, status.Error(codes.InvalidArgument, "artifact_hash is required")
	}
	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}
	args := []any{tenantID, artifactHash, startTime, endTime}

	// Audience-retention input + timeline geometry, both from the session beacons
	// (authoritative per-session). The inner per-session HAVING bw > 0 is the presence
	// gate: live / no-duration / malformed beacons leave bucket_width_s = 0 and are
	// excluded, so they can't contaminate the denominator as "reached bucket 0".
	// Geometry comes from here too, so a seek-only session (no density rows) still
	// carries the timeline. total_sessions is the retention denominator.
	// Bound the curve length against pathological inputs (the duration tiers keep real
	// assets well under this — a 3h asset at 15s buckets is ~720). reach and density
	// are both folded into this cap so the numerator and denominator stay consistent.
	const maxRetentionBucket = 5000
	reachHist := map[int64]int64{}
	var totalSessions, maxBucket, bucketWidth, assetDuration int64
	rrows, err := s.clickhouse.QueryContext(ctx, `
		SELECT reach, bw, dur, toInt64(count()) AS sessions FROM (
			SELECT session_id,
				toInt64(max(max_bucket_reached)) AS reach,
				toInt64(max(bucket_width_s)) AS bw,
				toInt64(max(asset_duration_s)) AS dur
			FROM client_qoe_session_deltas FINAL
			WHERE tenant_id = ? AND artifact_hash = ? AND timestamp >= ? AND timestamp <= ?
			GROUP BY content_id, session_id
			HAVING bw > 0
		) GROUP BY reach, bw, dur ORDER BY reach
	`, args...)
	if err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rrows.Close() }()
	for rrows.Next() {
		var reach, bw, dur, sessions int64
		if scanErr := rrows.Scan(&reach, &bw, &dur, &sessions); scanErr != nil {
			s.logger.WithError(scanErr).Error("Failed to scan vod reach row")
			continue
		}
		// Fold an over-cap reach into the cap bucket so the session still counts at
		// every bucket ≤ cap (dropping it would inflate the denominator vs the curve).
		if reach > maxRetentionBucket {
			reach = maxRetentionBucket
		}
		reachHist[reach] += sessions
		totalSessions += sessions
		if reach > maxBucket {
			maxBucket = reach
		}
		if bw > bucketWidth {
			bucketWidth = bw
		}
		if dur > assetDuration {
			assetDuration = dur
		}
	}
	if rowsErr := rrows.Err(); rowsErr != nil {
		return nil, wrapClickhouseError(rowsErr, "database error")
	}
	if totalSessions == 0 {
		return &periscopepb.GetVodRetentionResponse{Retention: &periscopepb.VodRetention{}}, nil
	}

	// Watch density: seconds watched per bucket.
	densityMap := map[int64]float64{}
	drows, err := s.clickhouse.QueryContext(ctx, `
		SELECT toInt64(bucket_index) AS bucket_index, toFloat64(sum(seconds_watched)) AS secs
		FROM vod_retention_buckets FINAL
		WHERE tenant_id = ? AND artifact_hash = ? AND timestamp >= ? AND timestamp <= ?
		GROUP BY bucket_index
		ORDER BY bucket_index
		LIMIT 10000
	`, args...)
	if err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = drows.Close() }()
	for drows.Next() {
		var bucketIndex int64
		var secs float64
		if scanErr := drows.Scan(&bucketIndex, &secs); scanErr != nil {
			s.logger.WithError(scanErr).Error("Failed to scan vod_retention_buckets row")
			continue
		}
		if bucketIndex > maxRetentionBucket {
			bucketIndex = maxRetentionBucket
		}
		densityMap[bucketIndex] += secs
		if bucketIndex > maxBucket {
			maxBucket = bucketIndex
		}
	}
	if rowsErr := drows.Err(); rowsErr != nil {
		return nil, wrapClickhouseError(rowsErr, "database error")
	}

	// reached[b] = sessions whose furthest position is at or beyond bucket b — the
	// suffix sum of the reach histogram, so the curve is monotonic non-increasing.
	reachedAtOrAfter := make([]int64, maxBucket+2)
	for b := maxBucket; b >= 0; b-- {
		reachedAtOrAfter[b] = reachedAtOrAfter[b+1] + reachHist[b]
	}

	points := make([]*periscopepb.VodRetentionPoint, 0, maxBucket+1)
	for b := int64(0); b <= maxBucket; b++ {
		points = append(points, &periscopepb.VodRetentionPoint{
			BucketIndex:    b,
			SecondsWatched: sanitizeFloat64(densityMap[b]),
			Reached:        reachedAtOrAfter[b],
		})
	}

	return &periscopepb.GetVodRetentionResponse{
		Retention: &periscopepb.VodRetention{
			BucketWidthS:   bucketWidth,
			AssetDurationS: assetDuration,
			TotalSessions:  totalSessions,
			Points:         points,
		},
	}, nil
}

// ListVodRetentionAssets lists retained playback artifacts with retention data in
// the window. Eligibility is owned here: VOD or clip content with at least one
// real reach sample (bucket_width_s > 0), matching GetVodRetention's presence
// gate. Human title/playback_id are composed at the gateway from the catalog.
// The per-artifact aggregate is wrapped in a subquery so the keyset columns
// (last_seen, artifact_hash) are concrete in the outer filter.
func (s *PeriscopeServer) ListVodRetentionAssets(ctx context.Context, req *periscopepb.ListVodRetentionAssetsRequest) (*periscopepb.ListVodRetentionAssetsResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}
	params, err := getCursorPagination(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	const eligible = `tenant_id = ? AND content_type IN ('vod', 'clip') AND bucket_width_s > 0 AND timestamp >= ? AND timestamp < ?`
	countCh := s.countAsync(ctx, fmt.Sprintf(`
		SELECT toInt32(uniqExact(artifact_hash))
		FROM client_qoe_session_deltas FINAL
		WHERE %s
	`, eligible), tenantID, startTime, endTime)

	query := fmt.Sprintf(`
		SELECT artifact_hash, total_sessions, duration_s, last_seen FROM (
			SELECT
				artifact_hash,
				toInt64(count(DISTINCT content_id, session_id)) AS total_sessions,
				toInt32(max(asset_duration_s)) AS duration_s,
				max(timestamp) AS last_seen
			FROM client_qoe_session_deltas FINAL
			WHERE %s
			GROUP BY artifact_hash
		)
		WHERE 1 = 1
	`, eligible)
	args := []any{tenantID, startTime, endTime}

	keysetCond, keysetArgs := buildKeysetCondition(params, "last_seen", "artifact_hash")
	if keysetCond != "" {
		query += keysetCond
		args = append(args, keysetArgs...)
	}
	query += buildOrderBy(params, "last_seen", "artifact_hash")
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var assets []*periscopepb.VodRetentionAsset
	for rows.Next() {
		var artifactHash string
		var totalSessions int64
		var durationS int32
		var lastSeen time.Time
		if err := rows.Scan(&artifactHash, &totalSessions, &durationS, &lastSeen); err != nil {
			s.logger.WithError(err).Error("Failed to scan vod retention asset row")
			continue
		}
		assets = append(assets, &periscopepb.VodRetentionAsset{
			ArtifactHash:  artifactHash,
			TotalSessions: totalSessions,
			DurationS:     durationS,
			LastSeen:      timestamppb.New(lastSeen),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}

	resultsLen := len(assets)
	if resultsLen > params.Limit {
		assets = assets[:params.Limit]
	}
	if params.Direction == pagination.Backward {
		slices.Reverse(assets)
	}

	total := <-countCh
	var startCursor, endCursor string
	if len(assets) > 0 {
		startCursor = pagination.EncodeCursor(assets[0].LastSeen.AsTime(), assets[0].ArtifactHash)
		endCursor = pagination.EncodeCursor(assets[len(assets)-1].LastSeen.AsTime(), assets[len(assets)-1].ArtifactHash)
	}
	page := buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor)
	if params.Cursor == nil {
		if params.Direction == pagination.Forward {
			page.HasPreviousPage = false
		} else {
			page.HasNextPage = false
		}
	}

	return &periscopepb.ListVodRetentionAssetsResponse{
		Pagination: page,
		Assets:     assets,
	}, nil
}

// ============================================================================
// Node Performance 5-Minute Aggregates
// ============================================================================

// GetNodePerformance5M returns 5-minute aggregated node performance from node_performance_5m MV
func (s *PeriscopeServer) GetNodePerformance5M(ctx context.Context, req *periscopepb.GetNodePerformance5MRequest) (*periscopepb.GetNodePerformance5MResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
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

	// Count query — subquery deduplicates AGT groups
	countQuery := `SELECT count(*) FROM (SELECT 1 FROM node_performance_5m WHERE tenant_id = ? AND timestamp_5m >= ? AND timestamp_5m <= ?`
	countArgs := []any{tenantID, startTime, endTime}
	if nodeID != "" {
		countQuery += " AND node_id = ?"
		countArgs = append(countArgs, nodeID)
	}
	countQuery += " GROUP BY timestamp_5m, cluster_id, node_id) sub"
	countCh := s.countAsync(ctx, countQuery, countArgs...)

	query := `
		SELECT timestamp_5m, cluster_id, node_id,
		       sum(cpu_sum) / sum(cpu_count) as avg_cpu,
		       max(max_cpu) as max_cpu,
		       sum(memory_sum) / sum(memory_count) as avg_memory,
		       max(max_memory) as max_memory,
		       (max(bw_in_max) - min(bw_in_min)) + (max(bw_out_max) - min(bw_out_min)) as total_bandwidth,
		       sum(streams_sum) / sum(streams_count) as avg_streams,
		       max(max_streams) as max_streams
		FROM node_performance_5m
		WHERE tenant_id = ? AND timestamp_5m >= ? AND timestamp_5m <= ?
	`
	args := []any{tenantID, startTime, endTime}

	if nodeID != "" {
		query += " AND node_id = ?"
		args = append(args, nodeID)
	}

	keysetCond, keysetArgs := buildKeysetCondition(params, "timestamp_5m", "concat(cluster_id, ':', node_id)")
	if keysetCond != "" {
		query += keysetCond
		args = append(args, keysetArgs...)
	}

	query += " GROUP BY timestamp_5m, cluster_id, node_id"
	query += buildOrderBy(params, "timestamp_5m", "concat(cluster_id, ':', node_id)")
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var records []*periscopepb.NodePerformance5M
	for rows.Next() {
		var timestamp time.Time
		var clusterID string
		var nodeIDStr string
		var avgCPU, maxCPU, avgMemory, maxMemory float32
		var totalBandwidth int64
		var avgStreams float64
		var maxStreams uint32

		err := rows.Scan(&timestamp, &clusterID, &nodeIDStr, &avgCPU, &maxCPU, &avgMemory, &maxMemory,
			&totalBandwidth, &avgStreams, &maxStreams)
		if err != nil {
			s.logger.WithError(err).Error("Failed to scan node_performance_5m row")
			continue
		}

		records = append(records, &periscopepb.NodePerformance5M{
			Id:             fmt.Sprintf("%s:%s", clusterID, nodeIDStr),
			Timestamp:      timestamppb.New(timestamp),
			NodeId:         nodeIDStr,
			AvgCpu:         avgCPU,
			MaxCpu:         maxCPU,
			AvgMemory:      avgMemory,
			MaxMemory:      maxMemory,
			TotalBandwidth: totalBandwidth,
			AvgStreams:     int32(avgStreams),
			MaxStreams:     int32(maxStreams),
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

	return &periscopepb.GetNodePerformance5MResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Records:    records,
	}, nil
}

// ============================================================================
// Viewer Hours Hourly Aggregates
// ============================================================================

// GetViewerHoursHourly returns hourly viewer hours aggregates from canonical viewer usage windows.
func (s *PeriscopeServer) GetViewerHoursHourly(ctx context.Context, req *periscopepb.GetViewerHoursHourlyRequest) (*periscopepb.GetViewerHoursHourlyResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
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
	countArgs := []any{tenantID, startTime, endTime}
	if streamID != "" {
		countQuery += " AND stream_id = ?"
		countArgs = append(countArgs, streamID)
	}
	countCh := s.countAsync(ctx, countQuery, countArgs...)

	// total_session_seconds and total_bytes are scalar in the rollup;
	// unique_viewers_state is the aggregate state column.
	query := `
		SELECT hour, tenant_id, stream_id, country_code,
		       toUInt32(finalizeAggregation(unique_viewers_state)) as unique_viewers,
		       total_session_seconds,
		       total_bytes,
		       egress_bytes
		FROM viewer_hours_hourly
		WHERE tenant_id = ? AND hour >= ? AND hour <= ?
	`
	args := []any{tenantID, startTime, endTime}

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
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var records []*periscopepb.ViewerHoursHourly
	for rows.Next() {
		var hour time.Time
		var tenantIDStr, streamIDStr, countryCode string
		var uniqueViewers int32
		var totalSessionSeconds, totalBytes, egressBytes int64

		err := rows.Scan(&hour, &tenantIDStr, &streamIDStr, &countryCode,
			&uniqueViewers, &totalSessionSeconds, &totalBytes, &egressBytes)
		if err != nil {
			s.logger.WithError(err).Error("Failed to scan viewer_hours_hourly row")
			continue
		}

		records = append(records, &periscopepb.ViewerHoursHourly{
			Id:                  fmt.Sprintf("%s_%s_%s", hour.Format(time.RFC3339), streamIDStr, countryCode),
			Hour:                timestamppb.New(hour),
			TenantId:            tenantIDStr,
			StreamId:            streamIDStr,
			CountryCode:         countryCode,
			UniqueViewers:       uniqueViewers,
			TotalSessionSeconds: totalSessionSeconds,
			TotalBytes:          totalBytes,
			EgressBytes:         egressBytes,
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

	return &periscopepb.GetViewerHoursHourlyResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Records:    records,
	}, nil
}

// ============================================================================
// Viewer Geographic Hourly Aggregates
// ============================================================================

// GetViewerGeoHourly returns hourly geographic breakdown from viewer_geo_hourly MV
func (s *PeriscopeServer) GetViewerGeoHourly(ctx context.Context, req *periscopepb.GetViewerGeoHourlyRequest) (*periscopepb.GetViewerGeoHourlyResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
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
	args := []any{tenantID, startTime, endTime}

	keysetCond, keysetArgs := buildKeysetCondition(params, "hour", "country_code")
	if keysetCond != "" {
		query += keysetCond
		args = append(args, keysetArgs...)
	}

	query += buildOrderBy(params, "hour", "country_code")
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var records []*periscopepb.ViewerGeoHourly
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

		records = append(records, &periscopepb.ViewerGeoHourly{
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

	return &periscopepb.GetViewerGeoHourlyResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Records:    records,
	}, nil
}

// ============================================================================
// Tenant Daily Stats
// ============================================================================

// GetTenantDailyStats returns daily tenant statistics from finalized viewer facts for PlatformOverview.dailyStats
func (s *PeriscopeServer) GetTenantDailyStats(ctx context.Context, req *periscopepb.GetTenantDailyStatsRequest) (*periscopepb.GetTenantDailyStatsResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}

	days := req.GetDays()
	if days <= 0 {
		days = 7 // default to 7 days
	}
	if days > 90 {
		days = 90 // max 90 days
	}

	query := `
		SELECT
		       toDate(d.window_start) AS day,
		       d.tenant_id,
		       sum(d.seconds_observed) / 3600.0 AS viewer_hours,
		       toInt32(uniqExact(d.node_id, d.session_id)) AS unique_viewers,
		       toInt32(uniqExact(d.node_id, d.session_id)) AS total_sessions,
		       sum(d.down_bytes_observed) / 1073741824.0 AS egress_gb,
		       toInt64(uniqExact(d.node_id, d.session_id)) AS total_views
		FROM viewer_usage_5m_v d
		WHERE d.tenant_id = ?
		  AND d.window_start >= toStartOfDay(today() - ?)
		  AND d.window_start <  toStartOfDay(today() + 1)
		GROUP BY day, d.tenant_id
		ORDER BY day DESC
		LIMIT ?
	`

	rows, err := s.clickhouse.QueryContext(ctx, query, tenantID, days, days)
	if err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var stats []*periscopepb.TenantDailyStat
	for rows.Next() {
		var day time.Time
		var tenantIDStr string
		var viewerHours, egressGB float64
		var uniqueViewers, totalSessions int32
		var totalViews int64

		err := rows.Scan(&day, &tenantIDStr, &viewerHours, &uniqueViewers, &totalSessions, &egressGB, &totalViews)
		if err != nil {
			s.logger.WithError(err).Error("Failed to scan viewer_usage_5m_v daily row")
			continue
		}

		stats = append(stats, &periscopepb.TenantDailyStat{
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

	return &periscopepb.GetTenantDailyStatsResponse{
		Stats: stats,
	}, nil
}

// ============================================================================
// Processing Usage Queries (from canonical processing ledgers and rollups)
// ============================================================================

// GetProcessingUsage returns processing usage records and/or daily summaries for billing
func (s *PeriscopeServer) GetProcessingUsage(ctx context.Context, req *periscopepb.GetProcessingUsageRequest) (*periscopepb.GetProcessingUsageResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
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

	response := &periscopepb.GetProcessingUsageResponse{}

	// processing_5m_v stores finalized per-window processing facts. Pivot to
	// the per-day per-codec summary shape the proto response exposes.
	summaryQuery := `
		SELECT toDate(window_start) AS day, tenant_id,
		       sumIf(media_seconds, process_type = 'Livepeer')                            AS livepeer_seconds,
		       toUInt64(uniqExactIf(source_event_id, process_type = 'Livepeer'))          AS livepeer_segment_count,
		       toUInt32(uniqExactIf(stream_id, process_type = 'Livepeer'))                AS livepeer_unique_streams,
		       sumIf(media_seconds, process_type = 'Livepeer' AND output_codec = 'h264')  AS livepeer_h264,
		       sumIf(media_seconds, process_type = 'Livepeer' AND output_codec = 'vp9')   AS livepeer_vp9,
		       sumIf(media_seconds, process_type = 'Livepeer' AND output_codec = 'av1')   AS livepeer_av1,
		       sumIf(media_seconds, process_type = 'Livepeer' AND output_codec IN ('hevc','h265')) AS livepeer_hevc,
		       sumIf(media_seconds, process_type = 'AV')                                  AS native_av_seconds,
		       toUInt64(uniqExactIf(source_event_id, process_type = 'AV'))                AS native_av_segment_count,
		       toUInt32(uniqExactIf(stream_id, process_type = 'AV'))                      AS native_av_unique_streams,
		       sumIf(media_seconds, process_type = 'AV' AND output_codec = 'h264')        AS native_av_h264,
		       sumIf(media_seconds, process_type = 'AV' AND output_codec = 'vp9')         AS native_av_vp9,
		       sumIf(media_seconds, process_type = 'AV' AND output_codec = 'av1')         AS native_av_av1,
		       sumIf(media_seconds, process_type = 'AV' AND output_codec IN ('hevc','h265')) AS native_av_hevc,
		       sumIf(media_seconds, process_type = 'AV' AND output_codec = 'aac')         AS native_av_aac,
		       sumIf(media_seconds, process_type = 'AV' AND output_codec = 'opus')        AS native_av_opus,
		       sumIf(media_seconds, track_type = 'audio')                                 AS audio_seconds,
		       sumIf(media_seconds, track_type = 'video')                                 AS video_seconds
		FROM processing_5m_v
		WHERE tenant_id = ? AND window_start >= ? AND window_start < ?
	`
	summaryArgs := []any{tenantID, startTime, endTime}
	if streamID != "" {
		summaryQuery += " AND stream_id = ?"
		summaryArgs = append(summaryArgs, streamID)
	}
	if processType != "" {
		summaryQuery += " AND process_type = ?"
		summaryArgs = append(summaryArgs, processType)
	}
	summaryQuery += `
		GROUP BY day, tenant_id
		ORDER BY day DESC
	`
	summaryRows, err := s.clickhouse.QueryContext(ctx, summaryQuery, summaryArgs...)
	if err != nil {
		s.logger.WithError(err).Error("Failed to query processing_5m_v")
	} else {
		defer func() { _ = summaryRows.Close() }()
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

			scanErr := summaryRows.Scan(&day, &tenantIDStr,
				&livepeerSeconds, &livepeerSegmentCount, &livepeerUniqueStreams,
				&livepeerH264, &livepeerVp9, &livepeerAv1, &livepeerHevc,
				&nativeAvSeconds, &nativeAvSegmentCount, &nativeAvUniqueStreams,
				&nativeAvH264, &nativeAvVp9, &nativeAvAv1, &nativeAvHevc,
				&nativeAvAac, &nativeAvOpus,
				&audioSeconds, &videoSeconds)
			if scanErr != nil {
				s.logger.WithError(scanErr).Error("Failed to scan processing_5m_v row")
				continue
			}

			response.Summaries = append(response.Summaries, &periscopepb.ProcessingUsageSummary{
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

	// Build count query for canonical finalized processing segment records.
	countQuery := `SELECT count(*) FROM processing_segments_final_v WHERE tenant_id = ? AND source_ended_at_ms >= ? AND source_ended_at_ms < ?`
	countArgs := []any{tenantID, startTime.UnixMilli(), endTime.UnixMilli()}
	if streamID != "" {
		countQuery += " AND stream_id = ?"
		countArgs = append(countArgs, streamID)
	}
	if processType != "" {
		countQuery += " AND process_type = ?"
		countArgs = append(countArgs, processType)
	}
	countCh := s.countAsync(ctx, countQuery, countArgs...)

	// Query detailed records from finalized processing facts. Raw
	// process_billing telemetry stays in processing_events for diagnostics;
	// this public usage surface follows the same canonical facts as billing.
	query := `
		SELECT toDateTime(intDiv(source_ended_at_ms, 1000)) AS timestamp,
		       tenant_id, node_id, stream_id, process_type, toInt64(round(media_seconds * 1000)) AS duration_ms,
		       nullIf(input_codec, ''), nullIf(output_codec, ''), nullIf(track_type, ''),
		       -- Livepeer fields
		       nullIf(segment_number, 0), nullIf(width, 0), nullIf(height, 0), nullIf(rendition_count, 0), NULL, NULL,
		       nullIf(livepeer_session_id, ''), nullIf(source_started_at_ms, 0), nullIf(input_bytes, 0), nullIf(output_bytes_total, 0),
		       NULL, nullIf(turnaround_ms, 0), nullIf(speed_factor, 0), NULL,
		       -- MistProcAV cumulative
		       nullIf(input_frames, 0), nullIf(output_frames, 0), NULL, NULL, NULL, is_final,
		       -- MistProcAV delta
		       nullIf(input_frames_delta, 0), nullIf(output_frames_delta, 0), nullIf(input_bytes_delta, 0), nullIf(output_bytes_delta, 0),
		       -- MistProcAV dimensions
		       NULL, NULL, nullIf(width, 0), nullIf(height, 0),
		       -- MistProcAV frame/audio
		       NULL, NULL, NULL, NULL,
		       -- MistProcAV timing
		       source_started_at_ms, source_ended_at_ms, NULL, NULL,
		       -- MistProcAV performance
		       nullIf(rtf_in, 0), nullIf(rtf_out, 0), NULL, NULL
		FROM processing_segments_final_v
		WHERE tenant_id = ? AND source_ended_at_ms >= ? AND source_ended_at_ms < ?
	`
	args := []any{tenantID, startTime.UnixMilli(), endTime.UnixMilli()}

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
	timestampExpr := "toDateTime(intDiv(source_ended_at_ms, 1000))"
	keysetCond, keysetArgs, err := buildKeysetConditionN(params, timestampExpr, []string{"stream_id", "node_id", "process_type"}, cursorParts)
	if err != nil {
		return nil, err
	}
	if keysetCond != "" {
		query += keysetCond
		args = append(args, keysetArgs...)
	}

	query += buildOrderByN(params, timestampExpr, []string{"stream_id", "node_id", "process_type"})
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var records []*periscopepb.ProcessingUsageRecord
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
			s.logger.WithError(err).Error("Failed to scan processing_segments_final_v row")
			continue
		}

		record := &periscopepb.ProcessingUsageRecord{
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
func (s *PeriscopeServer) GetLiveUsageSummary(ctx context.Context, req *periscopepb.GetLiveUsageSummaryRequest) (*periscopepb.GetLiveUsageSummaryResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	summary := &periscopepb.LiveUsageSummary{
		TenantId:    tenantID,
		PeriodStart: timestamppb.New(startTime),
		PeriodEnd:   timestamppb.New(endTime),
	}

	queryFailures := 0
	queryCount := 0
	var lastErr error
	recordQueryError := func(err error, message string) {
		if err == nil || errors.Is(err, database.ErrNoRows) {
			return
		}
		queryFailures++
		lastErr = err
		s.logger.WithError(err).Warn(message)
	}

	// Stream runtime combines closed 5-minute ledger rows with current live intervals.
	var maxViewers, totalStreams int32
	var streamHours float64
	queryCount++
	queryCtx, cancel := withClickhouseTimeout(ctx)
	streamHours, maxViewers, totalStreams, err = s.queryStreamRuntimeSummary(queryCtx, tenantID, startTime, endTime)
	cancel()
	recordQueryError(err, "Failed to query stream runtime summary")
	summary.MaxViewers = maxViewers
	summary.TotalStreams = totalStreams
	summary.StreamHours = streamHours

	// Viewer duration and session counts combine finalized 5-minute usage with
	// current session facts that have not appeared in the usage ledger yet.
	var totalSessionSeconds uint64
	var egressBytes uint64
	var totalViewers uint32
	var uniqueViewers uint32
	queryCount++
	queryCtx, cancel = withClickhouseTimeout(ctx)
	err = s.clickhouse.QueryRowContext(queryCtx, `
		SELECT
			toUInt64(COALESCE(sum(seconds_observed), 0)) AS total_session_seconds,
			toUInt64(COALESCE(sum(down_bytes_observed), 0)) AS egress_bytes,
			toUInt32(uniqExact(session_key)) AS total_viewers,
			toUInt32(uniqExact(viewer_key)) AS unique_viewers
		FROM (
			SELECT
				toUInt64(seconds_observed) AS seconds_observed,
				toUInt64(down_bytes_observed) AS down_bytes_observed,
				concat(toString(u.node_id), '|', u.session_id) AS session_key,
				if(s.host != '', s.host, concat(toString(u.node_id), '|', u.session_id)) AS viewer_key
			FROM viewer_usage_5m_v u
			LEFT JOIN viewer_sessions_final_v s USING (tenant_id, node_id, session_id)
			WHERE u.tenant_id = ?
			  AND u.window_start >= ?
			  AND u.window_start <  ?
			UNION ALL
			SELECT
				toUInt64(greatest(0, dateDiff('second', greatest(ifNull(connected_at, disconnected_at), ?), least(ifNull(disconnected_at, now()), ?)))) AS seconds_observed,
				toUInt64(0) AS down_bytes_observed,
				concat(toString(node_id), '|', session_id) AS session_key,
				concat(toString(node_id), '|', session_id) AS viewer_key
			FROM viewer_sessions_current FINAL
			WHERE tenant_id = ?
			  AND ifNull(connected_at, disconnected_at) < ?
			  AND ifNull(disconnected_at, now()) >= ?
			  AND (tenant_id, node_id, session_id) NOT IN (
			      SELECT tenant_id, node_id, session_id
			      FROM viewer_usage_5m_v
			      WHERE tenant_id = ?
			        AND window_start >= ?
			        AND window_start <  ?
			  )
		)
	`, tenantID, startTime, endTime, startTime, endTime, tenantID, endTime, startTime, tenantID, startTime, endTime).Scan(&totalSessionSeconds, &egressBytes, &totalViewers, &uniqueViewers)
	cancel()
	recordQueryError(err, "Failed to query viewer usage facts for live usage")
	summary.ViewerHours = float64(totalSessionSeconds) / 3600.0
	summary.EgressGb = float64(egressBytes) / (1024 * 1024 * 1024)
	summary.UniqueUsers = int32(uniqueViewers)
	summary.TotalViewers = int32(totalViewers)

	// Peak bandwidth from client_qoe_5m (avg_bw_out is bytes/sec, convert to Mbps)
	var peakBandwidthBytes float64
	queryCount++
	queryCtx, cancel = withClickhouseTimeout(ctx)
	err = s.clickhouse.QueryRowContext(queryCtx, `
		SELECT COALESCE(max(avg_bw_out), 0) AS peak_bandwidth
		FROM client_qoe_5m
		WHERE tenant_id = ?
		  AND timestamp_5m >= ?
		  AND timestamp_5m <  ?
	`, tenantID, startTime, endTime).Scan(&peakBandwidthBytes)
	cancel()
	recordQueryError(err, "Failed to query client_qoe_5m for peak bandwidth")
	summary.PeakBandwidthMbps = peakBandwidthBytes * 8 / (1024 * 1024) // bytes/sec to Mbps

	// Time-weighted average storage GB over the queried range. The rollup
	// stores per-window gb_seconds; dividing the sum by the elapsed seconds
	// gives a true range-average.
	// Read cold (S3) as the customer-facing storage product.
	rangeSeconds := endTime.Sub(startTime).Seconds()
	var avgGb float64
	if rangeSeconds > 0 {
		queryCount++
		queryCtx, cancel = withClickhouseTimeout(ctx)
		err = s.clickhouse.QueryRowContext(queryCtx, `
			SELECT COALESCE(sum(gb_seconds), 0) AS gb_seconds
			FROM storage_gb_seconds_5m_v
			WHERE tenant_id = ?
			  AND storage_scope = 'cold'
			  AND window_start >= ?
			  AND window_start <  ?
		`, tenantID, startTime, endTime).Scan(&avgGb)
		cancel()
		recordQueryError(err, "Failed to query storage_gb_seconds_5m_v")
		avgGb = avgGb / rangeSeconds
	}
	summary.DisplayStorageGb = avgGb

	// Per-codec processing breakdown + segment counts from finalized 5-minute processing facts.
	var livepeerH264, livepeerVp9, livepeerAv1, livepeerHevc float64
	var nativeAvH264, nativeAvVp9, nativeAvAv1, nativeAvHevc float64
	var nativeAvAac, nativeAvOpus float64
	var livepeerSegmentCount, nativeAvSegmentCount uint64
	var livepeerUniqueStreams, nativeAvUniqueStreams uint32
	queryCount++
	queryCtx, cancel = withClickhouseTimeout(ctx)
	err = s.clickhouse.QueryRowContext(queryCtx, `
		SELECT
			sumIf(media_seconds, process_type = 'Livepeer' AND output_codec = 'h264')                AS livepeer_h264,
			sumIf(media_seconds, process_type = 'Livepeer' AND output_codec = 'vp9')                 AS livepeer_vp9,
			sumIf(media_seconds, process_type = 'Livepeer' AND output_codec = 'av1')                 AS livepeer_av1,
			sumIf(media_seconds, process_type = 'Livepeer' AND output_codec IN ('hevc','h265'))      AS livepeer_hevc,
			sumIf(media_seconds, process_type = 'AV'       AND output_codec = 'h264')                AS native_av_h264,
			sumIf(media_seconds, process_type = 'AV'       AND output_codec = 'vp9')                 AS native_av_vp9,
			sumIf(media_seconds, process_type = 'AV'       AND output_codec = 'av1')                 AS native_av_av1,
			sumIf(media_seconds, process_type = 'AV'       AND output_codec IN ('hevc','h265'))      AS native_av_hevc,
			sumIf(media_seconds, process_type = 'AV'       AND output_codec = 'aac')                 AS native_av_aac,
			sumIf(media_seconds, process_type = 'AV'       AND output_codec = 'opus')                AS native_av_opus,
			toUInt64(uniqExactIf(source_event_id, process_type = 'Livepeer'))                        AS livepeer_segment_count,
			toUInt64(uniqExactIf(source_event_id, process_type = 'AV'))                              AS native_av_segment_count,
			toUInt32(uniqExactIf(stream_id, process_type = 'Livepeer'))                              AS livepeer_unique_streams,
			toUInt32(uniqExactIf(stream_id, process_type = 'AV'))                                    AS native_av_unique_streams
		FROM processing_5m_v
		WHERE tenant_id = ?
		  AND window_start >= ?
		  AND window_start <  ?
	`, tenantID, startTime, endTime).Scan(
		&livepeerH264, &livepeerVp9, &livepeerAv1, &livepeerHevc,
		&nativeAvH264, &nativeAvVp9, &nativeAvAv1, &nativeAvHevc,
		&nativeAvAac, &nativeAvOpus,
		&livepeerSegmentCount, &nativeAvSegmentCount,
		&livepeerUniqueStreams, &nativeAvUniqueStreams,
	)
	cancel()
	recordQueryError(err, "Failed to query processing_5m_v for per-codec breakdown")
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

	// Geographic summary from current session facts.
	var uniqueCountries, uniqueCities int32
	queryCount++
	queryCtx, cancel = withClickhouseTimeout(ctx)
	err = s.clickhouse.QueryRowContext(queryCtx, `
		SELECT
			toInt32(uniqExactIf(country_code, country_code != '')) AS unique_countries,
			toInt32(uniqExactIf(city, city != '')) AS unique_cities
		FROM (
			SELECT country_code, '' AS city
			FROM viewer_geo_hourly
			WHERE tenant_id = ?
			  AND hour >= ?
			  AND hour <= ?
			UNION ALL
			SELECT country_code, city
			FROM viewer_sessions_current FINAL
			WHERE tenant_id = ?
			  AND ifNull(connected_at, disconnected_at) <= ?
			  AND ifNull(disconnected_at, now()) >= ?
			  AND (tenant_id, node_id, session_id) NOT IN (
			      SELECT tenant_id, node_id, session_id
			      FROM viewer_usage_5m_v
			      WHERE tenant_id = ?
			        AND window_start >= ?
			        AND window_start <  ?
			  )
		)
	`, tenantID, startTime, endTime, tenantID, endTime, startTime, tenantID, startTime, endTime).Scan(&uniqueCountries, &uniqueCities)
	cancel()
	recordQueryError(err, "Failed to query current viewer geography")
	summary.UniqueCountries = uniqueCountries
	summary.UniqueCities = uniqueCities

	// Geo breakdown by country (top 20)
	queryCount++
	queryCtx, cancel = withClickhouseTimeout(ctx)
	rows, err := s.clickhouse.QueryContext(queryCtx, `
		SELECT
			country_code,
			toInt32(sum(viewer_count)) AS viewer_count,
			sum(viewer_hours) AS viewer_hours,
			sum(egress_gb) AS egress_gb
		FROM (
			SELECT
				country_code,
				viewer_count,
				viewer_hours,
				egress_gb
			FROM viewer_geo_hourly
			WHERE tenant_id = ?
			  AND hour >= ?
			  AND hour <= ?
			UNION ALL
			SELECT
				country_code,
				toUInt64(uniqExact(node_id, session_id)) AS viewer_count,
				sum(greatest(0, dateDiff('second', greatest(ifNull(connected_at, disconnected_at), ?), least(ifNull(disconnected_at, now()), ?)))) / 3600.0 AS viewer_hours,
				toFloat64(0) AS egress_gb
			FROM viewer_sessions_current FINAL
			WHERE tenant_id = ?
			  AND ifNull(connected_at, disconnected_at) <= ?
			  AND ifNull(disconnected_at, now()) >= ?
			  AND country_code != ''
			  AND (tenant_id, node_id, session_id) NOT IN (
			      SELECT tenant_id, node_id, session_id
			      FROM viewer_usage_5m_v
			      WHERE tenant_id = ?
			        AND window_start >= ?
			        AND window_start <  ?
			  )
			GROUP BY country_code
		)
		GROUP BY country_code
		ORDER BY viewer_hours DESC
		LIMIT 20
	`, tenantID, startTime, endTime, startTime, endTime, tenantID, endTime, startTime, tenantID, startTime, endTime)
	if err != nil {
		cancel()
		recordQueryError(err, "Failed to query current geo breakdown")
	} else if rows != nil {
		defer func() {
			_ = rows.Close()
			cancel()
		}()
		for rows.Next() {
			var countryCode string
			var viewerCount int32
			var viewerHours, egressGb float64
			if scanErr := rows.Scan(&countryCode, &viewerCount, &viewerHours, &egressGb); scanErr != nil {
				recordQueryError(scanErr, "Failed to scan geo breakdown row")
				continue
			}
			summary.GeoBreakdown = append(summary.GeoBreakdown, &periscopepb.CountryMetric{
				CountryCode: countryCode,
				ViewerCount: viewerCount,
				ViewerHours: viewerHours,
				EgressGb:    egressGb,
			})
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			recordQueryError(rowsErr, "Failed to read geo breakdown rows")
		}
	}

	// Storage lifecycle - artifact counts (from artifact_events)
	var clipsCreated, clipsDeleted, dvrCreated, dvrDeleted, vodCreated, vodDeleted uint32
	queryCount++
	queryCtx, cancel = withClickhouseTimeout(ctx)
	err = s.clickhouse.QueryRowContext(queryCtx, `
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
	cancel()
	recordQueryError(err, "Failed to query artifact_events for storage lifecycle")
	summary.ClipsCreated = clipsCreated
	summary.ClipsDeleted = clipsDeleted
	summary.DvrCreated = dvrCreated
	summary.DvrDeleted = dvrDeleted
	summary.VodCreated = vodCreated
	summary.VodDeleted = vodDeleted

	// Storage breakdown from latest snapshot (hot + cold)
	var hotClipBytes, hotDvrBytes, hotVodBytes uint64
	queryCount++
	queryCtx, cancel = withClickhouseTimeout(ctx)
	err = s.clickhouse.QueryRowContext(queryCtx, `
		SELECT
			COALESCE(argMax(clip_bytes, timestamp), 0),
			COALESCE(argMax(dvr_bytes, timestamp), 0),
			COALESCE(argMax(vod_bytes, timestamp), 0)
		FROM storage_snapshots
		WHERE tenant_id = ? AND storage_scope = 'hot' AND timestamp <= ?
	`, tenantID, endTime).Scan(
		&hotClipBytes, &hotDvrBytes, &hotVodBytes,
	)
	cancel()
	recordQueryError(err, "Failed to query hot storage_snapshots for storage breakdown")

	var coldFrozenClipBytes, coldFrozenDvrBytes, coldFrozenVodBytes uint64
	queryCount++
	queryCtx, cancel = withClickhouseTimeout(ctx)
	err = s.clickhouse.QueryRowContext(queryCtx, `
		SELECT
			COALESCE(argMax(frozen_clip_bytes, timestamp), 0),
			COALESCE(argMax(frozen_dvr_bytes, timestamp), 0),
			COALESCE(argMax(frozen_vod_bytes, timestamp), 0)
		FROM storage_snapshots
		WHERE tenant_id = ? AND storage_scope = 'cold' AND timestamp <= ?
	`, tenantID, endTime).Scan(
		&coldFrozenClipBytes, &coldFrozenDvrBytes, &coldFrozenVodBytes,
	)
	cancel()
	recordQueryError(err, "Failed to query cold storage_snapshots for storage breakdown")

	summary.ClipBytes = hotClipBytes
	summary.DvrBytes = hotDvrBytes
	summary.VodBytes = hotVodBytes
	summary.FrozenClipBytes = coldFrozenClipBytes
	summary.FrozenDvrBytes = coldFrozenDvrBytes
	summary.FrozenVodBytes = coldFrozenVodBytes

	// Freeze (S3 upload) operations from storage_events. Read-through cache
	// fills are tracked separately as relay observability, not as a tenant
	// usage metric.
	var freezeCount uint32
	var freezeBytes uint64
	queryCount++
	queryCtx, cancel = withClickhouseTimeout(ctx)
	err = s.clickhouse.QueryRowContext(queryCtx, `
			SELECT
				countIf(action = 'synced') AS freeze_count,
				sumIf(size_bytes, action = 'synced') AS freeze_bytes
			FROM storage_events
			WHERE tenant_id = ? AND timestamp BETWEEN ? AND ?
		`, tenantID, startTime, endTime).Scan(
		&freezeCount, &freezeBytes,
	)
	cancel()
	recordQueryError(err, "Failed to query storage_events for freeze operations")
	summary.FreezeCount = freezeCount
	summary.FreezeBytes = freezeBytes

	if queryFailures > 0 {
		return nil, wrapClickhouseError(lastErr, "database error")
	}

	return &periscopepb.GetLiveUsageSummaryResponse{Summary: summary}, nil
}

// ============================================================================
// Rebuffering Events (from rebuffering_events table)
// ============================================================================

// GetRebufferingEvents returns buffer state transition events
func (s *PeriscopeServer) GetRebufferingEvents(ctx context.Context, req *periscopepb.GetRebufferingEventsRequest) (*periscopepb.GetRebufferingEventsResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
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
	countArgs := []any{tenantID, startTime, endTime}
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
	args := []any{tenantID, startTime, endTime}

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
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var events []*periscopepb.RebufferingEvent
	for rows.Next() {
		var timestamp, rebufferStart, rebufferEnd time.Time
		var tenantIDStr, streamIDStr, nodeID, bufferState, prevState string

		err := rows.Scan(&timestamp, &tenantIDStr, &streamIDStr, &nodeID, &bufferState, &prevState, &rebufferStart, &rebufferEnd)
		if err != nil {
			s.logger.WithError(err).Error("Failed to scan rebuffering_events row")
			continue
		}

		events = append(events, &periscopepb.RebufferingEvent{
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

	return &periscopepb.GetRebufferingEventsResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Events:     events,
	}, nil
}

// ============================================================================
// Tenant Analytics Daily (from canonical viewer usage windows)
// ============================================================================

// GetTenantAnalyticsDaily returns daily tenant-level analytics rollups
func (s *PeriscopeServer) GetTenantAnalyticsDaily(ctx context.Context, req *periscopepb.GetTenantAnalyticsDailyRequest) (*periscopepb.GetTenantAnalyticsDailyResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
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
	countQuery := `
		SELECT count()
		FROM (
			SELECT toDate(window_start) AS day
			FROM viewer_usage_5m_v
			WHERE tenant_id = ? AND window_start >= ? AND window_start < ?
			GROUP BY day
		)`
	countCh := s.countAsync(ctx, countQuery, tenantID, startTime, endTime)

	query := `
		SELECT
			toDate(window_start) AS day,
			tenant_id,
			toInt32(uniqExact(stream_id)) AS total_streams,
			toInt64(uniqExact(node_id, session_id)) AS total_views,
			toInt32(uniqExact(node_id, session_id)) AS unique_viewers,
			toInt64(sum(down_bytes_observed)) AS egress_bytes
		FROM viewer_usage_5m_v
		WHERE tenant_id = ? AND window_start >= ? AND window_start < ?
		GROUP BY day, tenant_id
	`
	args := []any{tenantID, startTime, endTime}

	keysetCond, keysetArgs := buildKeysetConditionSingle(params, "day")
	if keysetCond != "" {
		query += keysetCond
		args = append(args, keysetArgs...)
	}

	query += buildOrderBySingle(params, "day")
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var records []*periscopepb.TenantAnalyticsDaily
	for rows.Next() {
		var day time.Time
		var tenantIDStr string
		var totalStreams int32
		var totalViews int64
		var uniqueViewers int32
		var egressBytes int64

		err := rows.Scan(&day, &tenantIDStr, &totalStreams, &totalViews, &uniqueViewers, &egressBytes)
		if err != nil {
			s.logger.WithError(err).Error("Failed to scan viewer_usage_5m_v tenant daily row")
			continue
		}

		records = append(records, &periscopepb.TenantAnalyticsDaily{
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

	return &periscopepb.GetTenantAnalyticsDailyResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Records:    records,
	}, nil
}

// ============================================================================
// Stream Analytics Daily (from canonical viewer session facts)
// ============================================================================

// GetStreamAnalyticsDaily returns daily stream-level analytics rollups
func (s *PeriscopeServer) GetStreamAnalyticsDaily(ctx context.Context, req *periscopepb.GetStreamAnalyticsDailyRequest) (*periscopepb.GetStreamAnalyticsDailyResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
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
	countQuery := `
		SELECT count()
		FROM (
			SELECT toDate(toDateTime(intDiv(source_ended_at_ms, 1000))) AS day, stream_id
			FROM viewer_sessions_final_v
			WHERE tenant_id = ? AND source_ended_at_ms >= ? AND source_ended_at_ms < ? AND closed_reason = 'final'
			GROUP BY day, stream_id
		)`
	countArgs := []any{tenantID, startTime.UnixMilli(), endTime.UnixMilli()}
	if streamID := req.GetStreamId(); streamID != "" {
		countQuery = strings.Replace(countQuery, " AND closed_reason = 'final'", " AND closed_reason = 'final' AND stream_id = ?", 1)
		countArgs = append(countArgs, streamID)
	}
	countCh := s.countAsync(ctx, countQuery, countArgs...)

	query := `
		SELECT
			toDate(toDateTime(intDiv(source_ended_at_ms, 1000))) AS day,
			tenant_id,
			stream_id,
			toInt64(uniqExact(node_id, session_id)) AS total_views,
			toInt32(uniqExact(node_id, session_id)) AS unique_viewers,
			toInt32(uniqExactIf(country_code, country_code != '')) AS unique_countries,
			toInt32(uniqExactIf(city, city != '')) AS unique_cities,
			toInt64(sum(downloaded_bytes)) AS egress_bytes
		FROM viewer_sessions_final_v
		WHERE tenant_id = ? AND source_ended_at_ms >= ? AND source_ended_at_ms < ? AND closed_reason = 'final'
	`
	args := []any{tenantID, startTime.UnixMilli(), endTime.UnixMilli()}

	if streamID := req.GetStreamId(); streamID != "" {
		query += ` AND stream_id = ?`
		args = append(args, streamID)
	}

	query += ` GROUP BY day, tenant_id, stream_id`

	keysetCond, keysetArgs := buildKeysetCondition(params, "day", "stream_id")
	if keysetCond != "" {
		query += keysetCond
		args = append(args, keysetArgs...)
	}

	query += buildOrderBy(params, "day", "stream_id")
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var records []*periscopepb.StreamAnalyticsDaily
	for rows.Next() {
		var day time.Time
		var tenantIDStr, streamIDStr string
		var totalViews int64
		var uniqueViewers, uniqueCountries, uniqueCities int32
		var egressBytes int64

		err := rows.Scan(&day, &tenantIDStr, &streamIDStr, &totalViews, &uniqueViewers, &uniqueCountries, &uniqueCities, &egressBytes)
		if err != nil {
			s.logger.WithError(err).Error("Failed to scan viewer_sessions_final_v stream daily row")
			continue
		}

		records = append(records, &periscopepb.StreamAnalyticsDaily{
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

	return &periscopepb.GetStreamAnalyticsDailyResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Records:    records,
	}, nil
}

// GetAPIUsage returns API usage records and/or daily summaries
func (s *PeriscopeServer) GetAPIUsage(ctx context.Context, req *periscopepb.GetAPIUsageRequest) (*periscopepb.GetAPIUsageResponse, error) {
	tenantID, err := requireTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
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

	response := &periscopepb.GetAPIUsageResponse{}

	// Get daily summaries (aggregated by auth_type)
	if operationName == "" {
		summaryQuery := `
			SELECT toDate(window_start) AS day, tenant_id, auth_type,
			       sum(requests) AS total_requests,
			       sum(errors) AS total_errors,
			       sum(duration_ms) AS total_duration_ms,
			       sum(complexity) AS total_complexity,
			       uniqCombinedMerge(unique_users_state)  AS unique_users,
			       uniqCombinedMerge(unique_tokens_state) AS unique_tokens
			FROM api_usage_5m_v
			WHERE tenant_id = ? AND window_start >= ? AND window_start < ?
		`
		summaryArgs := []any{tenantID, startTime, endTime}
		if authType != "" {
			summaryQuery += " AND auth_type = ?"
			summaryArgs = append(summaryArgs, authType)
		}
		if operationType != "" {
			summaryQuery += " AND operation_type = ?"
			summaryArgs = append(summaryArgs, operationType)
		}
		summaryQuery += " GROUP BY day, tenant_id, auth_type ORDER BY day DESC"

		summaryRows, summaryErr := s.clickhouse.QueryContext(ctx, summaryQuery, summaryArgs...)
		if summaryErr != nil {
			s.logger.WithError(summaryErr).Error("Failed to query api_usage_5m_v daily summary")
		} else {
			defer func() { _ = summaryRows.Close() }()
			for summaryRows.Next() {
				var day time.Time
				var tenantIDStr, authTypeStr string
				var totalRequests, totalErrors, totalDurationMs, totalComplexity, uniqueUsers, uniqueTokens uint64

				scanErr := summaryRows.Scan(&day, &tenantIDStr, &authTypeStr,
					&totalRequests, &totalErrors, &totalDurationMs, &totalComplexity,
					&uniqueUsers, &uniqueTokens)
				if scanErr != nil {
					s.logger.WithError(scanErr).Error("Failed to scan api_usage_5m_v daily summary row")
					continue
				}

				avgDuration := float64(0)
				if totalRequests > 0 {
					avgDuration = float64(totalDurationMs) / float64(totalRequests)
				}

				response.Summaries = append(response.Summaries, &periscopepb.APIUsageSummary{
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
		       sum(requests) AS total_requests,
		       sum(errors) AS total_errors,
		       sum(duration_ms) AS total_duration_ms,
		       sum(complexity) AS total_complexity,
	       uniqCombined(ifNull(operation_name, '')) AS unique_operations
		FROM api_usage_5m_v
		WHERE tenant_id = ? AND window_start >= ? AND window_start < ?
	`
	operationSummaryArgs := []any{tenantID, startTime, endTime}
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
		s.logger.WithError(err).Error("Failed to query api_usage_5m_v operation summaries")
	} else {
		defer func() { _ = operationRows.Close() }()
		for operationRows.Next() {
			var opTypeStr string
			var totalRequests, totalErrors, totalDurationMs, totalComplexity, uniqueOperations uint64

			if scanErr := operationRows.Scan(&opTypeStr, &totalRequests, &totalErrors, &totalDurationMs, &totalComplexity, &uniqueOperations); scanErr != nil {
				s.logger.WithError(scanErr).Error("Failed to scan api_usage_5m_v operation summary row")
				continue
			}

			avgDuration := float64(0)
			if totalRequests > 0 {
				avgDuration = float64(totalDurationMs) / float64(totalRequests)
			}

			response.OperationSummaries = append(response.OperationSummaries, &periscopepb.APIUsageOperationSummary{
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

	// Build count query for 5-minute records.
	countQuery := `SELECT count() FROM (
		SELECT window_start, auth_type, operation_type, operation_name
		FROM api_usage_5m_v
		WHERE tenant_id = ? AND window_start >= ? AND window_start < ?`
	countArgs := []any{tenantID, startTime, endTime}
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
	countQuery += " GROUP BY window_start, auth_type, operation_type, operation_name)"
	countCh := s.countAsync(ctx, countQuery, countArgs...)

	// Query 5-minute records — requests/errors/duration_ms/complexity are
	// scalar; uniques are AggregateFunction(uniqCombined, …) states.
	query := `
		SELECT window_start AS hour, tenant_id, auth_type, operation_type, operation_name,
		       sum(requests) AS total_requests,
		       sum(errors) AS total_errors,
		       sum(duration_ms) AS total_duration_ms,
		       sum(complexity) AS total_complexity,
		       uniqCombinedMerge(unique_users_state)  AS unique_users,
		       uniqCombinedMerge(unique_tokens_state) AS unique_tokens
		FROM api_usage_5m_v
		WHERE tenant_id = ? AND window_start >= ? AND window_start < ?
	`
	args := []any{tenantID, startTime, endTime}

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

	if params.Cursor != nil {
		parts := strings.SplitN(params.Cursor.ID, "|", 3)
		if len(parts) != 3 {
			return nil, status.Errorf(codes.InvalidArgument, "invalid cursor tuple: expected 3 parts, got %d", len(parts))
		}
		if params.Direction == pagination.Backward {
			query += " AND (window_start, auth_type, operation_type, ifNull(operation_name, '')) > (?, ?, ?, ?)"
		} else {
			query += " AND (window_start, auth_type, operation_type, ifNull(operation_name, '')) < (?, ?, ?, ?)"
		}
		args = append(args, params.Cursor.Timestamp, parts[0], parts[1], parts[2])
	}

	query += " GROUP BY hour, tenant_id, auth_type, operation_type, operation_name"

	if params.Direction == pagination.Backward {
		query += " ORDER BY hour ASC, auth_type ASC, operation_type ASC, ifNull(operation_name, '') ASC"
	} else {
		query += " ORDER BY hour DESC, auth_type DESC, operation_type DESC, ifNull(operation_name, '') DESC"
	}
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapClickhouseError(err, "database error")
	}
	defer func() { _ = rows.Close() }()

	var records []*periscopepb.APIUsageRecord
	for rows.Next() {
		var hour time.Time
		var tenantIDStr, authTypeStr, operationTypeStr string
		var operationName *string
		var totalRequests, totalErrors, totalDurationMs, totalComplexity, uniqueUsers, uniqueTokens uint64

		err := rows.Scan(&hour, &tenantIDStr, &authTypeStr, &operationTypeStr, &operationName,
			&totalRequests, &totalErrors, &totalDurationMs, &totalComplexity,
			&uniqueUsers, &uniqueTokens)
		if err != nil {
			s.logger.WithError(err).Error("Failed to scan api_usage_5m_v row")
			continue
		}

		opName := ""
		if operationName != nil {
			opName = *operationName
		}

		// Composite ID for cursor pagination (timestamp handled separately).
		id := fmt.Sprintf("%s|%s|%s", authTypeStr, operationTypeStr, opName)

		records = append(records, &periscopepb.APIUsageRecord{
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
	ClickHouse    database.ClickHouseConn
	Logger        logging.Logger
	ServiceToken  string
	JWTSecret     []byte
	Metrics       *metrics.Metrics
	CertFile      string
	KeyFile       string
	AllowInsecure bool
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

	// GRPCMetricsInterceptor sits outermost so Unauthenticated /
	// PermissionDenied rejections from authInterceptor still show up in
	// periscope_query_grpc_requests_total.
	opts := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(
			middleware.GRPCMetricsInterceptor(cfg.Metrics.GRPCRequests, cfg.Metrics.GRPCDuration),
			unaryInterceptor(cfg.Logger),
			authInterceptor,
		),
	}
	tlsCfg := grpcutil.ServerTLSConfig{
		CertFile:      cfg.CertFile,
		KeyFile:       cfg.KeyFile,
		AllowInsecure: cfg.AllowInsecure,
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := grpcutil.WaitForServerTLSFiles(waitCtx, tlsCfg, cfg.Logger); err != nil {
		cfg.Logger.WithError(err).Fatal("Timed out waiting for Periscope gRPC TLS files")
	}
	tlsOpt, err := grpcutil.ServerTLS(tlsCfg, cfg.Logger)
	if err != nil {
		cfg.Logger.WithError(err).Fatal("Failed to configure Periscope gRPC TLS")
	}
	if tlsOpt != nil {
		opts = append(opts, tlsOpt)
	}

	server := grpc.NewServer(opts...)
	periscopeServer := &PeriscopeServer{
		clickhouse: cfg.ClickHouse,
		logger:     cfg.Logger,
		metrics:    cfg.Metrics,
	}

	// Register all services
	periscopepb.RegisterStreamAnalyticsServiceServer(server, periscopeServer)
	periscopepb.RegisterViewerAnalyticsServiceServer(server, periscopeServer)
	periscopepb.RegisterTrackAnalyticsServiceServer(server, periscopeServer)
	periscopepb.RegisterConnectionAnalyticsServiceServer(server, periscopeServer)
	periscopepb.RegisterNodeAnalyticsServiceServer(server, periscopeServer)
	periscopepb.RegisterRoutingAnalyticsServiceServer(server, periscopeServer)
	periscopepb.RegisterFederationAnalyticsServiceServer(server, periscopeServer)
	periscopepb.RegisterPlatformAnalyticsServiceServer(server, periscopeServer)
	periscopepb.RegisterClipAnalyticsServiceServer(server, periscopeServer)
	periscopepb.RegisterAggregatedAnalyticsServiceServer(server, periscopeServer)
	periscopepb.RegisterOrchestratorAnalyticsServiceServer(server, periscopeServer)

	// Register gRPC health checking service
	hs := health.NewServer()
	grpc_health_v1.RegisterHealthServer(server, hs)
	reflection.Register(server)

	return server
}

// unaryInterceptor logs gRPC requests
func unaryInterceptor(logger logging.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
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

// GetNetworkUsage returns network-wide usage totals grouped by period.
func (s *PeriscopeServer) GetNetworkUsage(ctx context.Context, req *periscopepb.GetNetworkUsageRequest) (*periscopepb.GetNetworkUsageResponse, error) {
	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	periodExpr := "toStartOfDay(window_start)"
	switch req.GetGroupBy() {
	case periscopepb.NetworkUsageGroupBy_NETWORK_USAGE_GROUP_BY_WEEK:
		periodExpr = "toStartOfWeek(window_start)"
	case periscopepb.NetworkUsageGroupBy_NETWORK_USAGE_GROUP_BY_MONTH:
		periodExpr = "toStartOfMonth(window_start)"
	case periscopepb.NetworkUsageGroupBy_NETWORK_USAGE_GROUP_BY_DAY, periscopepb.NetworkUsageGroupBy_NETWORK_USAGE_GROUP_BY_UNSPECIFIED:
		periodExpr = "toStartOfDay(window_start)"
	}

	query := fmt.Sprintf(`
		WITH viewer AS (
			SELECT
				%s AS period_start,
				sum(seconds_observed) / 3600.0 AS viewer_hours,
				sum(down_bytes_observed) / 1073741824.0 AS egress_gb,
				uniqExact(node_id, session_id) AS total_sessions,
				uniqExact(node_id, session_id) AS unique_viewers
			FROM viewer_usage_5m_v
			WHERE window_start >= ? AND window_start < ?
			GROUP BY period_start
		),
		processing AS (
			SELECT
				%s AS period_start,
				sumIf(media_seconds, process_type = 'Livepeer') AS livepeer_seconds,
				sumIf(media_seconds, process_type = 'AV')       AS native_av_seconds
			FROM processing_5m_v
			WHERE window_start >= ? AND window_start < ?
			GROUP BY period_start
		),
		api_usage AS (
			SELECT
				%s AS period_start,
				sum(requests) AS total_requests,
				sum(errors) AS total_errors
			FROM api_usage_5m_v
			WHERE window_start >= ? AND window_start < ?
			GROUP BY period_start
		)
		SELECT
			viewer.period_start,
			viewer.viewer_hours,
			viewer.egress_gb,
			viewer.total_sessions,
			viewer.unique_viewers,
			coalesce(processing.livepeer_seconds, 0) AS livepeer_seconds,
			coalesce(processing.native_av_seconds, 0) AS native_av_seconds,
			coalesce(api_usage.total_requests, 0) AS total_requests,
			coalesce(api_usage.total_errors, 0) AS total_errors
		FROM viewer
		LEFT JOIN processing USING period_start
		LEFT JOIN api_usage USING period_start
		ORDER BY viewer.period_start
	`, periodExpr, periodExpr, periodExpr)

	rows, err := s.clickhouse.QueryContext(ctx, query, startTime, endTime, startTime, endTime, startTime, endTime)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to query network usage: %v", err)
	}
	defer func() { _ = rows.Close() }()

	resp := &periscopepb.GetNetworkUsageResponse{}
	for rows.Next() {
		var period time.Time
		var viewerHours, egressGb, livepeerSeconds, nativeAvSeconds float64
		var totalSessions, uniqueViewers, totalRequests, totalErrors uint64
		if err := rows.Scan(&period, &viewerHours, &egressGb, &totalSessions, &uniqueViewers, &livepeerSeconds, &nativeAvSeconds, &totalRequests, &totalErrors); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to scan network usage row: %v", err)
		}
		resp.Records = append(resp.Records, &periscopepb.NetworkUsageRecord{
			PeriodStart:      timestamppb.New(period),
			ViewerHours:      viewerHours,
			EgressGb:         egressGb,
			TotalSessions:    totalSessions,
			UniqueViewers:    uniqueViewers,
			LivepeerSeconds:  livepeerSeconds,
			NativeAvSeconds:  nativeAvSeconds,
			TotalApiRequests: totalRequests,
			TotalApiErrors:   totalErrors,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to iterate network usage rows: %v", err)
	}

	return resp, nil
}

// GetAcquisitionFunnel returns signup counts grouped by channel/method/UTM/referral.
func (s *PeriscopeServer) GetAcquisitionFunnel(ctx context.Context, req *periscopepb.GetAcquisitionFunnelRequest) (*periscopepb.GetAcquisitionFunnelResponse, error) {
	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	query := `
		SELECT
			signup_channel,
			signup_method,
			utm_source,
			utm_medium,
			utm_campaign,
			referral_code,
			is_agent,
			countDistinct(tenant_id) AS tenant_count
		FROM tenant_acquisition_events
		WHERE timestamp BETWEEN ? AND ?
		GROUP BY signup_channel, signup_method, utm_source, utm_medium, utm_campaign, referral_code, is_agent
		ORDER BY tenant_count DESC
	`

	rows, err := s.clickhouse.QueryContext(ctx, query, startTime, endTime)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to query acquisition funnel: %v", err)
	}
	defer func() { _ = rows.Close() }()

	resp := &periscopepb.GetAcquisitionFunnelResponse{}
	for rows.Next() {
		var signupChannel, signupMethod string
		var utmSource, utmMedium, utmCampaign, referralCode sql.NullString
		var isAgent uint8
		var tenantCount uint64
		if err := rows.Scan(&signupChannel, &signupMethod, &utmSource, &utmMedium, &utmCampaign, &referralCode, &isAgent, &tenantCount); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to scan acquisition funnel row: %v", err)
		}
		entry := &periscopepb.AcquisitionFunnelEntry{
			SignupChannel: signupChannel,
			SignupMethod:  signupMethod,
			UtmSource:     utmSource.String,
			UtmMedium:     utmMedium.String,
			UtmCampaign:   utmCampaign.String,
			ReferralCode:  referralCode.String,
			IsAgent:       isAgent == 1,
			TenantCount:   tenantCount,
		}
		resp.Entries = append(resp.Entries, entry)
	}

	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to iterate acquisition funnel rows: %v", err)
	}

	return resp, nil
}

// GetAcquisitionCohortUsage returns usage totals grouped by acquisition cohorts.
func (s *PeriscopeServer) GetAcquisitionCohortUsage(ctx context.Context, req *periscopepb.GetAcquisitionCohortUsageRequest) (*periscopepb.GetAcquisitionCohortUsageResponse, error) {
	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	params := []any{startTime, endTime, startTime, endTime}
	filters := ""
	if req.SignupChannel != nil && *req.SignupChannel != "" {
		filters += " AND cohort.signup_channel = ?"
		params = append(params, *req.SignupChannel)
	}
	if req.CohortMonth != nil {
		cohortMonth := req.CohortMonth.AsTime()
		filters += " AND cohort.cohort_month = ?"
		params = append(params, cohortMonth)
	}

	query := fmt.Sprintf(`
		WITH cohort AS (
			SELECT
				tenant_id,
				toStartOfMonth(min(timestamp)) AS cohort_month,
				any(signup_channel) AS signup_channel
			FROM tenant_acquisition_events
			GROUP BY tenant_id
		),
		viewer AS (
			SELECT
				tenant_id,
				toDate(window_start) AS day,
				sum(seconds_observed) / 3600.0 AS viewer_hours,
				sum(down_bytes_observed) / 1073741824.0 AS egress_gb
			FROM viewer_usage_5m_v
			WHERE window_start >= ? AND window_start < ?
			GROUP BY tenant_id, day
		),
		processing AS (
			SELECT
				tenant_id,
				toDate(window_start) AS day,
				sum(media_seconds) AS media_seconds
			FROM processing_5m_v
			WHERE window_start >= ? AND window_start < ?
			GROUP BY tenant_id, day
		)
		SELECT
			viewer.day AS day,
			cohort.signup_channel,
			cohort.cohort_month,
			sum(viewer.viewer_hours) AS viewer_hours,
			sum(viewer.egress_gb) AS egress_gb,
			sum(ifNull(processing.media_seconds, 0)) AS media_seconds
		FROM viewer
		INNER JOIN cohort ON cohort.tenant_id = viewer.tenant_id
		LEFT JOIN processing ON processing.tenant_id = viewer.tenant_id AND processing.day = viewer.day
		WHERE 1 = 1%s
		GROUP BY day, cohort.signup_channel, cohort.cohort_month
		ORDER BY day, cohort.signup_channel, cohort.cohort_month
	`, filters)

	rows, err := s.clickhouse.QueryContext(ctx, query, params...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to query acquisition cohort usage: %v", err)
	}
	defer func() { _ = rows.Close() }()

	resp := &periscopepb.GetAcquisitionCohortUsageResponse{}
	for rows.Next() {
		var day time.Time
		var signupChannel string
		var cohortMonth time.Time
		var viewerHours, egressGb, mediaSeconds float64
		if err := rows.Scan(&day, &signupChannel, &cohortMonth, &viewerHours, &egressGb, &mediaSeconds); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to scan acquisition cohort usage row: %v", err)
		}
		resp.Records = append(resp.Records, &periscopepb.AcquisitionCohortUsageRecord{
			Day:           timestamppb.New(day),
			SignupChannel: signupChannel,
			CohortMonth:   timestamppb.New(cohortMonth),
			ViewerHours:   viewerHours,
			EgressGb:      egressGb,
			MediaSeconds:  mediaSeconds,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to iterate acquisition cohort usage rows: %v", err)
	}

	return resp, nil
}
