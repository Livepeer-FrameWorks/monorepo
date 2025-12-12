package grpc

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"frameworks/api_analytics_query/internal/metrics"
	"frameworks/pkg/database"
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

var streamNameRegex = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// PeriscopeServer implements all Periscope gRPC services
// All queries use ClickHouse only - no PostgreSQL dependency
type PeriscopeServer struct {
	pb.UnimplementedStreamAnalyticsServiceServer
	pb.UnimplementedViewerAnalyticsServiceServer
	pb.UnimplementedTrackAnalyticsServiceServer
	pb.UnimplementedConnectionAnalyticsServiceServer
	pb.UnimplementedNodeAnalyticsServiceServer
	pb.UnimplementedRoutingAnalyticsServiceServer
	pb.UnimplementedRealtimeAnalyticsServiceServer
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

// validateStreamName validates stream internal name format
func validateStreamName(name string) bool {
	if name == "" || len(name) > 255 {
		return false
	}
	return streamNameRegex.MatchString(name)
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

// validateTimeRange validates time range parameters and returns defaults if needed
func validateTimeRange(startTimeStr, endTimeStr string) (time.Time, time.Time, error) {
	var startTime, endTime time.Time
	var err error

	if startTimeStr == "" {
		startTime = time.Now().Add(-24 * time.Hour)
	} else {
		startTime, err = time.Parse(time.RFC3339, startTimeStr)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
	}

	if endTimeStr == "" {
		endTime = time.Now()
	} else {
		endTime, err = time.Parse(time.RFC3339, endTimeStr)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
	}

	if endTime.Before(startTime) {
		return time.Time{}, time.Time{}, fmt.Errorf("end time must be after start time")
	}

	if endTime.Sub(startTime) > 90*24*time.Hour {
		return time.Time{}, time.Time{}, fmt.Errorf("time range cannot exceed 90 days")
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

// buildKeysetCondition returns a WHERE clause fragment for keyset pagination.
// Forward: (ts, id) < (cursor_ts, cursor_id) - fetches older items
// Backward: (ts, id) > (cursor_ts, cursor_id) - fetches newer items
func buildKeysetCondition(params *pagination.Params, tsCol, idCol string) (string, []interface{}) {
	if params.Cursor == nil {
		return "", nil
	}
	if params.Direction == pagination.Backward {
		return fmt.Sprintf(" AND (%s, %s) > (?, ?)", tsCol, idCol), []interface{}{params.Cursor.Timestamp, params.Cursor.ID}
	}
	return fmt.Sprintf(" AND (%s, %s) < (?, ?)", tsCol, idCol), []interface{}{params.Cursor.Timestamp, params.Cursor.ID}
}

// buildOrderBy returns an ORDER BY clause for keyset pagination.
// Forward: DESC (newest first), Backward: ASC (oldest first, then reverse in Go)
func buildOrderBy(params *pagination.Params, tsCol, idCol string) string {
	if params.Direction == pagination.Backward {
		return fmt.Sprintf(" ORDER BY %s ASC, %s ASC", tsCol, idCol)
	}
	return fmt.Sprintf(" ORDER BY %s DESC, %s DESC", tsCol, idCol)
}

// countAsync runs a COUNT query in a goroutine and returns the result via channel.
// This allows the count query to run in parallel with the main data query,
// cutting total latency roughly in half for paginated queries.
func (s *PeriscopeServer) countAsync(ctx context.Context, query string, args ...interface{}) <-chan int32 {
	ch := make(chan int32, 1)
	go func() {
		var count int32
		if err := s.clickhouse.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
			s.logger.WithError(err).Debug("Async count query failed")
			count = 0
		}
		ch <- count
	}()
	return ch
}

// ============================================================================
// StreamAnalyticsService Implementation
// ============================================================================

func (s *PeriscopeServer) GetStreamAnalytics(ctx context.Context, req *pb.GetStreamAnalyticsRequest) (*pb.GetStreamAnalyticsResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	streamID := req.GetStreamId()
	if streamID != "" && !validateStreamName(streamID) {
		return nil, status.Error(codes.InvalidArgument, "invalid stream name format")
	}

	params, err := getCursorPagination(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	// Start count query in parallel (runs while main query executes)
	countQuery := `SELECT count() FROM live_streams FINAL WHERE tenant_id = ?`
	countArgs := []interface{}{tenantID}
	if streamID != "" {
		countQuery += " AND internal_name = ?"
		countArgs = append(countArgs, streamID)
	}
	countCh := s.countAsync(ctx, countQuery, countArgs...)

	// Simple query against live_streams - no JOINs, no aggregations
	// All data comes directly from Foghorn's state snapshots
	query := `
		SELECT
			internal_name,
			node_id,
			status,
			buffer_state,
			current_viewers,
			total_inputs,
			uploaded_bytes,
			downloaded_bytes,
			viewer_seconds,
			has_issues,
			issues_description,
			track_count,
			quality_tier,
			primary_width,
			primary_height,
			primary_fps,
			primary_codec,
			primary_bitrate,
			started_at,
			updated_at
		FROM live_streams FINAL
		WHERE tenant_id = ?
	`
	args := []interface{}{tenantID}

	if streamID != "" {
		query += " AND internal_name = ?"
		args = append(args, streamID)
	}

	// Add keyset pagination condition (direction-aware)
	keysetCond, keysetArgs := buildKeysetCondition(params, "updated_at", "internal_name")
	if keysetCond != "" {
		query += keysetCond
		args = append(args, keysetArgs...)
	}

	query += buildOrderBy(params, "updated_at", "internal_name")
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		s.logger.WithError(err).Error("Failed to query stream analytics from ClickHouse")
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var streams []*pb.StreamAnalytics
	for rows.Next() {
		var stream pb.StreamAnalytics
		var nodeID, bufferState, streamStatus, issuesDesc, qualityTier *string
		var primaryFps *float32
		var hasIssues *uint8
		var trackCount, primaryWidth, primaryHeight *uint16
		var primaryCodec *string
		var primaryBitrate *int32
		var startedAt, updatedAt *time.Time
		var downloadedBytes, uploadedBytes, viewerSeconds uint64
		var currentViewers uint32
		var totalInputs uint16

		err := rows.Scan(
			&stream.InternalName, &nodeID, &streamStatus, &bufferState,
			&currentViewers, &totalInputs, &uploadedBytes, &downloadedBytes,
			&viewerSeconds, &hasIssues, &issuesDesc,
			&trackCount, &qualityTier, &primaryWidth, &primaryHeight, &primaryFps,
			&primaryCodec, &primaryBitrate,
			&startedAt, &updatedAt,
		)
		if err != nil {
			s.logger.WithError(err).Error("Failed to scan stream analytics row")
			continue
		}

		// Map to protobuf fields
		stream.Id = stream.InternalName
		stream.TenantId = tenantID
		stream.StreamId = stream.InternalName
		stream.CurrentViewers = int32(currentViewers)
		stream.BandwidthIn = int64(downloadedBytes)
		stream.BandwidthOut = int64(uploadedBytes)
		stream.TotalBandwidthGb = float64(uploadedBytes) / 1073741824.0

		if nodeID != nil {
			stream.NodeId = nodeID
		}
		if streamStatus != nil {
			stream.Status = streamStatus
		}
		if startedAt != nil && !startedAt.IsZero() {
			stream.SessionStartTime = timestamppb.New(*startedAt)
		}
		if updatedAt != nil && !updatedAt.IsZero() {
			stream.LastUpdated = timestamppb.New(*updatedAt)
		}

		// Map remaining fields from live_streams (proto uses optional/oneof - assign pointers)
		if primaryCodec != nil {
			stream.CurrentCodec = primaryCodec
		}
		if primaryBitrate != nil {
			kbps := *primaryBitrate / 1000 // Convert to Kbps
			stream.BitrateKbps = &kbps
		}
		if primaryWidth != nil && primaryHeight != nil {
			res := fmt.Sprintf("%dx%d", *primaryWidth, *primaryHeight)
			stream.Resolution = &res
		}
		if primaryFps != nil {
			stream.CurrentFps = primaryFps
		}
		if qualityTier != nil {
			stream.QualityTier = qualityTier
		}

		streams = append(streams, &stream)
	}

	// Batch query for historical analytics from stream_analytics_daily
	if len(streams) > 0 {
		// Collect stream names for batch query
		streamNames := make([]string, len(streams))
		streamMap := make(map[string]*pb.StreamAnalytics)
		for i, stream := range streams {
			streamNames[i] = stream.InternalName
			streamMap[stream.InternalName] = stream
		}

		// Build IN clause with placeholders
		placeholders := make([]string, len(streamNames))
		batchArgs := []interface{}{tenantID}
		for i, name := range streamNames {
			placeholders[i] = "?"
			batchArgs = append(batchArgs, name)
		}

		// Query stream_analytics_daily for all streams in one batch (7-day window)
		analyticsQuery := fmt.Sprintf(`
			SELECT
				internal_name,
				sum(total_views) AS total_views,
				sum(unique_viewers) AS unique_viewers,
				sum(unique_countries) AS unique_countries,
				sum(unique_cities) AS unique_cities,
				sum(egress_bytes) AS egress_bytes
			FROM periscope.stream_analytics_daily
			WHERE tenant_id = ? AND internal_name IN (%s)
			AND day >= today() - 7
			GROUP BY internal_name
		`, strings.Join(placeholders, ","))

		analyticsRows, err := s.clickhouse.QueryContext(ctx, analyticsQuery, batchArgs...)
		if err != nil {
			s.logger.WithError(err).Debug("Failed to batch query stream_analytics_daily")
		} else {
			defer analyticsRows.Close()
			for analyticsRows.Next() {
				var name string
				var totalViews, uniqueViewers, uniqueCountries, uniqueCities uint64
				var egressBytes uint64
				if err := analyticsRows.Scan(&name, &totalViews, &uniqueViewers, &uniqueCountries, &uniqueCities, &egressBytes); err != nil {
					s.logger.WithError(err).Debug("Failed to scan stream_analytics_daily row")
					continue
				}
				if stream, ok := streamMap[name]; ok {
					stream.TotalViews = int32(totalViews)
					stream.UniqueViewers = int32(uniqueViewers)
					stream.UniqueCountries = int32(uniqueCountries)
					stream.UniqueCities = int32(uniqueCities)
				}
			}
		}

		// Batch query for peak/avg viewers from stream_events
		peakQuery := fmt.Sprintf(`
			SELECT
				internal_name,
				max(total_viewers) AS peak_viewers,
				avg(total_viewers) AS avg_viewers
			FROM periscope.stream_events
			WHERE tenant_id = ? AND internal_name IN (%s)
			AND timestamp >= now() - INTERVAL 7 DAY
			AND total_viewers IS NOT NULL
			GROUP BY internal_name
		`, strings.Join(placeholders, ","))

		peakRows, err := s.clickhouse.QueryContext(ctx, peakQuery, batchArgs...)
		if err != nil {
			s.logger.WithError(err).Debug("Failed to batch query stream_events for peak viewers")
		} else {
			defer peakRows.Close()
			for peakRows.Next() {
				var name string
				var peakViewers uint32
				var avgViewers float64
				if err := peakRows.Scan(&name, &peakViewers, &avgViewers); err != nil {
					s.logger.WithError(err).Debug("Failed to scan stream_events row")
					continue
				}
				if stream, ok := streamMap[name]; ok {
					stream.PeakViewers = int32(peakViewers)
					stream.AvgViewers = avgViewers
				}
			}
		}

		// Batch query for health metrics from stream_health_5m
		healthQuery := fmt.Sprintf(`
			SELECT
				internal_name,
				avg(avg_bitrate) AS avg_bitrate,
				avg(packet_loss_percentage) AS packet_loss_rate
			FROM periscope.stream_health_5m
			WHERE tenant_id = ? AND internal_name IN (%s)
			AND timestamp_5m >= now() - INTERVAL 7 DAY
			GROUP BY internal_name
		`, strings.Join(placeholders, ","))

		healthRows, err := s.clickhouse.QueryContext(ctx, healthQuery, batchArgs...)
		if err != nil {
			s.logger.WithError(err).Debug("Failed to batch query stream_health_5m")
		} else {
			defer healthRows.Close()
			for healthRows.Next() {
				var name string
				var avgBitrate, packetLossRate *float32
				if err := healthRows.Scan(&name, &avgBitrate, &packetLossRate); err != nil {
					s.logger.WithError(err).Debug("Failed to scan stream_health_5m row")
					continue
				}
				if stream, ok := streamMap[name]; ok {
					if avgBitrate != nil {
						stream.AvgBitrate = int32(*avgBitrate)
					}
					if packetLossRate != nil {
						stream.PacketLossRate = *packetLossRate
					}
				}
			}
		}

		// Batch query for connection quality from client_metrics_5m (latest per stream)
		qualityQuery := fmt.Sprintf(`
			SELECT
				internal_name,
				argMax(avg_connection_quality, timestamp_5m) AS avg_connection_quality
			FROM periscope.client_metrics_5m
			WHERE tenant_id = ? AND internal_name IN (%s)
			GROUP BY internal_name
		`, strings.Join(placeholders, ","))

		qualityRows, err := s.clickhouse.QueryContext(ctx, qualityQuery, batchArgs...)
		if err != nil {
			s.logger.WithError(err).Debug("Failed to batch query client_metrics_5m")
		} else {
			defer qualityRows.Close()
			for qualityRows.Next() {
				var name string
				var avgConnQuality *float32
				if err := qualityRows.Scan(&name, &avgConnQuality); err != nil {
					s.logger.WithError(err).Debug("Failed to scan client_metrics_5m row")
					continue
				}
				if stream, ok := streamMap[name]; ok && avgConnQuality != nil {
					stream.AvgConnectionQuality = *avgConnQuality
				}
			}
		}
	}

	resultsLen := len(streams)
	if resultsLen > params.Limit {
		streams = streams[:params.Limit]
	}

	// Reverse for backward pagination (query was ASC, we want DESC order)
	if params.Direction == pagination.Backward {
		slices.Reverse(streams)
	}

	// Wait for parallel count query to complete
	total := <-countCh

	var startCursor, endCursor string
	if len(streams) > 0 {
		first := streams[0]
		last := streams[len(streams)-1]
		if first.LastUpdated != nil {
			startCursor = pagination.EncodeCursor(first.LastUpdated.AsTime(), first.Id)
		}
		if last.LastUpdated != nil {
			endCursor = pagination.EncodeCursor(last.LastUpdated.AsTime(), last.Id)
		}
	}

	return &pb.GetStreamAnalyticsResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Streams:    streams,
	}, nil
}

func (s *PeriscopeServer) GetStreamDetails(ctx context.Context, req *pb.GetStreamDetailsRequest) (*pb.GetStreamDetailsResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	internalName := req.GetInternalName()
	if internalName == "" {
		return nil, status.Error(codes.InvalidArgument, "internal_name required")
	}
	if !validateStreamName(internalName) {
		return nil, status.Error(codes.InvalidArgument, "invalid stream name format")
	}

	// Simple single-row query against live_streams
	query := `
		SELECT
			internal_name,
			node_id,
			status,
			buffer_state,
			current_viewers,
			total_inputs,
			uploaded_bytes,
			downloaded_bytes,
			viewer_seconds,
			has_issues,
			issues_description,
			track_count,
			quality_tier,
			primary_width,
			primary_height,
			primary_fps,
			primary_codec,
			primary_bitrate,
			started_at,
			updated_at
		FROM live_streams FINAL
		WHERE tenant_id = ? AND internal_name = ?
	`

	var stream pb.StreamAnalytics
	var nodeID, bufferState, streamStatus, issuesDesc, qualityTier *string
	var primaryFps *float32
	var hasIssues *uint8
	var trackCount, primaryWidth, primaryHeight *uint16
	var primaryCodec *string
	var primaryBitrate *int32
	var startedAt, updatedAt *time.Time
	var downloadedBytes, uploadedBytes, viewerSeconds uint64
	var currentViewers uint32
	var totalInputs uint16

	err := s.clickhouse.QueryRowContext(ctx, query, tenantID, internalName).Scan(
		&stream.InternalName, &nodeID, &streamStatus, &bufferState,
		&currentViewers, &totalInputs, &uploadedBytes, &downloadedBytes,
		&viewerSeconds, &hasIssues, &issuesDesc,
		&trackCount, &qualityTier, &primaryWidth, &primaryHeight, &primaryFps,
		&primaryCodec, &primaryBitrate,
		&startedAt, &updatedAt,
	)
	if err != nil {
		s.logger.WithError(err).WithField("internal_name", internalName).Error("Stream not found")
		return nil, status.Error(codes.NotFound, "stream not found")
	}

	// Map to protobuf
	stream.Id = stream.InternalName
	stream.TenantId = tenantID
	stream.StreamId = stream.InternalName
	stream.CurrentViewers = int32(currentViewers)
	stream.BandwidthIn = int64(downloadedBytes)
	stream.BandwidthOut = int64(uploadedBytes)
	stream.TotalBandwidthGb = float64(uploadedBytes) / 1073741824.0

	if nodeID != nil {
		stream.NodeId = nodeID
	}
	if streamStatus != nil {
		stream.Status = streamStatus
	}
	if startedAt != nil && !startedAt.IsZero() {
		stream.SessionStartTime = timestamppb.New(*startedAt)
	}
	if updatedAt != nil && !updatedAt.IsZero() {
		stream.LastUpdated = timestamppb.New(*updatedAt)
	}

	// Fetch avg_connection_quality from client_metrics_5m
	var avgConnectionQuality *float32
	avgConnQuery := `
		SELECT avg_connection_quality
		FROM periscope.client_metrics_5m FINAL
		WHERE tenant_id = ? AND internal_name = ?
		ORDER BY timestamp_5m DESC
		LIMIT 1
	`
	err = s.clickhouse.QueryRowContext(ctx, avgConnQuery, tenantID, internalName).Scan(&avgConnectionQuality)
	if err != nil && err != sql.ErrNoRows {
		s.logger.WithError(err).WithField("internal_name", internalName).Debug("Failed to query avg_connection_quality")
	}
	if avgConnectionQuality != nil {
		stream.AvgConnectionQuality = *avgConnectionQuality
	}

	// Fetch avg_bitrate and packet_loss_rate from stream_health_5m (7-day average)
	var avgBitrate *float32
	var packetLossRate *float32
	healthQuery := `
		SELECT avg(avg_bitrate), avg(packet_loss_percentage)
		FROM periscope.stream_health_5m
		WHERE tenant_id = ? AND internal_name = ?
		AND timestamp_5m >= now() - INTERVAL 7 DAY
	`
	err = s.clickhouse.QueryRowContext(ctx, healthQuery, tenantID, internalName).Scan(&avgBitrate, &packetLossRate)
	if err != nil && err != sql.ErrNoRows {
		s.logger.WithError(err).WithField("internal_name", internalName).Debug("Failed to query stream health metrics")
	}
	if avgBitrate != nil {
		stream.AvgBitrate = int32(*avgBitrate)
	}
	if packetLossRate != nil {
		stream.PacketLossRate = *packetLossRate
	}

	// Fetch unique_countries, unique_cities, total_views, unique_viewers from connection_events (7-day window)
	var uniqueCountries, uniqueCities, totalViews, uniqueViewers uint64
	viewerQuery := `
		SELECT
			uniq(country_code),
			uniq(city),
			countIf(event_type = 'connect'),
			uniq(session_id)
		FROM periscope.connection_events
		WHERE tenant_id = ? AND internal_name = ?
		AND timestamp >= now() - INTERVAL 7 DAY
	`
	err = s.clickhouse.QueryRowContext(ctx, viewerQuery, tenantID, internalName).Scan(&uniqueCountries, &uniqueCities, &totalViews, &uniqueViewers)
	if err != nil && err != sql.ErrNoRows {
		s.logger.WithError(err).WithField("internal_name", internalName).Debug("Failed to query viewer metrics")
	}
	stream.UniqueCountries = int32(uniqueCountries)
	stream.UniqueCities = int32(uniqueCities)
	stream.TotalViews = int32(totalViews)
	stream.UniqueViewers = int32(uniqueViewers)

	// Fetch peak_viewers and avg_viewers from stream_events (7-day window)
	var peakViewers *uint32
	var avgViewers *float64
	peakQuery := `
		SELECT max(total_viewers), avg(total_viewers)
		FROM periscope.stream_events
		WHERE tenant_id = ? AND internal_name = ?
		AND timestamp >= now() - INTERVAL 7 DAY
		AND total_viewers IS NOT NULL
	`
	err = s.clickhouse.QueryRowContext(ctx, peakQuery, tenantID, internalName).Scan(&peakViewers, &avgViewers)
	if err != nil && err != sql.ErrNoRows {
		s.logger.WithError(err).WithField("internal_name", internalName).Debug("Failed to query peak/avg viewers")
	}
	if peakViewers != nil {
		stream.PeakViewers = int32(*peakViewers)
	}
	if avgViewers != nil {
		stream.AvgViewers = *avgViewers
	}

	return &pb.GetStreamDetailsResponse{
		Stream: &stream,
	}, nil
}

func (s *PeriscopeServer) GetStreamEvents(ctx context.Context, req *pb.GetStreamEventsRequest) (*pb.GetStreamEventsResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	internalName := req.GetInternalName()
	if internalName == "" {
		return nil, status.Error(codes.InvalidArgument, "internal_name required")
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
		SELECT event_id, timestamp, event_type, status, node_id, event_data, internal_name
		FROM periscope.stream_events
		WHERE tenant_id = ? AND internal_name = ? AND timestamp >= ? AND timestamp <= ?
	`
	args := []interface{}{tenantID, internalName, startTime, endTime}

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

		err := rows.Scan(&event.EventId, &ts, &event.EventType, &event.Status, &event.NodeId, &eventData, &event.InternalName)
		if err != nil {
			continue
		}

		event.Timestamp = timestamppb.New(ts)
		event.EventData = eventData
		// Parse event_data JSON into event_payload struct
		if eventData != "" {
			event.EventPayload = parseEventPayload(eventData)
		}
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

	var total int32
	s.clickhouse.QueryRowContext(ctx, `
		SELECT count(*) FROM periscope.stream_events
		WHERE tenant_id = ? AND internal_name = ? AND timestamp >= ? AND timestamp <= ?
	`, tenantID, internalName, startTime, endTime).Scan(&total)

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

	internalName := req.GetInternalName()
	if internalName == "" {
		return nil, status.Error(codes.InvalidArgument, "internal_name required")
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	params, err := getCursorPagination(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	// stream_buffer events are stored in stream_events with event_type filter
	query := `
		SELECT event_id, timestamp, buffer_state, node_id, event_data
		FROM stream_events
		WHERE tenant_id = ? AND internal_name = ? AND event_type = 'stream_buffer'
			AND timestamp >= ? AND timestamp <= ?
	`
	args := []interface{}{tenantID, internalName, startTime, endTime}

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

	var total int32
	s.clickhouse.QueryRowContext(ctx, `
		SELECT count(*) FROM stream_events
		WHERE tenant_id = ? AND internal_name = ? AND event_type = 'stream_buffer'
			AND timestamp >= ? AND timestamp <= ?
	`, tenantID, internalName, startTime, endTime).Scan(&total)

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

func (s *PeriscopeServer) GetEndEvents(ctx context.Context, req *pb.GetEndEventsRequest) (*pb.GetEndEventsResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	internalName := req.GetInternalName()
	if internalName == "" {
		return nil, status.Error(codes.InvalidArgument, "internal_name required")
	}

	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time range: %v", err)
	}

	params, err := getCursorPagination(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	// stream_end events are stored in stream_events with event_type filter
	query := `
		SELECT event_id, timestamp, status, node_id, event_data
		FROM stream_events
		WHERE tenant_id = ? AND internal_name = ? AND event_type = 'stream_end'
			AND timestamp >= ? AND timestamp <= ?
	`
	args := []interface{}{tenantID, internalName, startTime, endTime}

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

	var events []*pb.EndEvent
	for rows.Next() {
		var event pb.EndEvent
		var ts time.Time
		var eventData string
		var eventID uuid.UUID
		var eventStatus *string

		err := rows.Scan(&eventID, &ts, &eventStatus, &event.NodeId, &eventData)
		if err != nil {
			continue
		}

		event.EventId = eventID.String()
		event.Timestamp = timestamppb.New(ts)
		if eventStatus != nil {
			event.Status = *eventStatus
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

	var total int32
	s.clickhouse.QueryRowContext(ctx, `
		SELECT count(*) FROM stream_events
		WHERE tenant_id = ? AND internal_name = ? AND event_type = 'stream_end'
			AND timestamp >= ? AND timestamp <= ?
	`, tenantID, internalName, startTime, endTime).Scan(&total)

	var startCursor, endCursor string
	if len(events) > 0 {
		startCursor = pagination.EncodeCursor(events[0].Timestamp.AsTime(), events[0].EventId)
		endCursor = pagination.EncodeCursor(events[len(events)-1].Timestamp.AsTime(), events[len(events)-1].EventId)
	}

	return &pb.GetEndEventsResponse{
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
		SELECT timestamp, tenant_id, internal_name, node_id,
			bitrate, fps, gop_size, width, height,
			buffer_size, buffer_used, buffer_health,
			packets_sent, packets_lost, packets_retransmitted,
			codec, profile, buffer_state, track_metadata,
			has_issues, issues_description, track_count,
			audio_channels, audio_sample_rate, audio_codec, audio_bitrate
		FROM stream_health_metrics
		WHERE tenant_id = ? AND timestamp >= ? AND timestamp <= ?
	`
	args := []interface{}{tenantID, startTime, endTime}

	if internalName := req.GetInternalName(); internalName != "" {
		query += " AND internal_name = ?"
		args = append(args, internalName)
	}

	keysetCond, keysetArgs := buildKeysetCondition(params, "timestamp", "internal_name")
	if keysetCond != "" {
		query += keysetCond
		args = append(args, keysetArgs...)
	}

	query += buildOrderBy(params, "timestamp", "internal_name")
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

		err := rows.Scan(
			&ts, &tenantID, &m.InternalName, &m.NodeId,
			&m.Bitrate, &m.Fps, &m.GopSize, &m.Width, &m.Height,
			&m.BufferSize, &m.BufferUsed, &m.BufferHealth,
			&m.PacketsSent, &m.PacketsLost, &m.PacketsRetransmitted,
			&m.Codec, &m.Profile, &m.BufferState, &trackMetadata,
			&hasIssues, &issuesDesc, &trackCount,
			&audioChannels, &audioSampleRate, &audioCodec, &audioBitrate,
		)
		if err != nil {
			continue
		}

		// Generate composite ID for pagination
		m.Id = fmt.Sprintf("%s_%s", ts.Format(time.RFC3339), m.InternalName)
		m.TenantId = tenantID
		m.Timestamp = timestamppb.New(ts)
		m.TrackMetadata = trackMetadata
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
	if internalName := req.GetInternalName(); internalName != "" {
		s.clickhouse.QueryRowContext(ctx, `
			SELECT count(*) FROM stream_health_metrics
			WHERE tenant_id = ? AND timestamp >= ? AND timestamp <= ? AND internal_name = ?
		`, tenantID, startTime, endTime, internalName).Scan(&total)
	} else {
		s.clickhouse.QueryRowContext(ctx, `
			SELECT count(*) FROM stream_health_metrics
			WHERE tenant_id = ? AND timestamp >= ? AND timestamp <= ?
		`, tenantID, startTime, endTime).Scan(&total)
	}

	var startCursor, endCursor string
	if len(metrics) > 0 {
		startCursor = pagination.EncodeCursor(metrics[0].Timestamp.AsTime(), metrics[0].InternalName)
		endCursor = pagination.EncodeCursor(metrics[len(metrics)-1].Timestamp.AsTime(), metrics[len(metrics)-1].InternalName)
	}

	return &pb.GetStreamHealthMetricsResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Metrics:    metrics,
	}, nil
}

// GetStreamStatus returns operational state for a single stream (Control/Data plane separation)
// This is the source of truth for stream status - queries live_streams directly
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

	// Simple query against live_streams - source of truth for stream state
	var streamStatus string
	var startedAt, updatedAt *time.Time
	var currentViewers uint32
	var bufferState, qualityTier, primaryCodec, issuesDescription *string
	var primaryWidth, primaryHeight, primaryBitrate *int32
	var primaryFps *float32
	var hasIssues *bool

	err := s.clickhouse.QueryRowContext(ctx, `
		SELECT status, current_viewers, started_at, updated_at,
			buffer_state, quality_tier, primary_width, primary_height,
			primary_fps, primary_codec, primary_bitrate, has_issues, issues_description
		FROM live_streams FINAL
		WHERE tenant_id = ? AND internal_name = ?
	`, tenantID, streamID).Scan(&streamStatus, &currentViewers, &startedAt, &updatedAt,
		&bufferState, &qualityTier, &primaryWidth, &primaryHeight,
		&primaryFps, &primaryCodec, &primaryBitrate, &hasIssues, &issuesDescription)

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
	}

	return resp, nil
}

// GetStreamsStatus returns operational state for multiple streams (batch lookup)
// Queries live_streams directly - no JOINs needed
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

	// Simple batch query against live_streams
	query := fmt.Sprintf(`
		SELECT internal_name, status, current_viewers, started_at, updated_at,
			buffer_state, quality_tier, primary_width, primary_height,
			primary_fps, primary_codec, primary_bitrate, has_issues, issues_description
		FROM live_streams FINAL
		WHERE tenant_id = ? AND internal_name IN (%s)
	`, joinStrings(placeholders, ", "))

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		s.logger.WithError(err).Error("Failed to batch query stream status from ClickHouse")
		return &pb.StreamsStatusResponse{Statuses: statuses}, nil
	}
	defer rows.Close()

	for rows.Next() {
		var internalName, streamStatus string
		var startedAt, updatedAt *time.Time
		var currentViewers uint32
		var bufferState, qualityTier, primaryCodec, issuesDescription *string
		var primaryWidth, primaryHeight, primaryBitrate *int32
		var primaryFps *float32
		var hasIssues *bool

		err := rows.Scan(&internalName, &streamStatus, &currentViewers, &startedAt, &updatedAt,
			&bufferState, &qualityTier, &primaryWidth, &primaryHeight,
			&primaryFps, &primaryCodec, &primaryBitrate, &hasIssues, &issuesDescription)
		if err != nil {
			continue
		}

		resp := &pb.StreamStatusResponse{
			StreamId:       internalName,
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

		statuses[internalName] = resp
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

// ============================================================================
// ViewerAnalyticsService Implementation
// ============================================================================

func (s *PeriscopeServer) GetViewerStats(ctx context.Context, req *pb.GetViewerStatsRequest) (*pb.GetViewerStatsResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	internalName := req.GetInternalName()
	if internalName == "" {
		return nil, status.Error(codes.InvalidArgument, "internal_name required")
	}

	var resp pb.GetViewerStatsResponse

	// Get current viewers from live_streams (real-time state from Foghorn)
	err := s.clickhouse.QueryRowContext(ctx, `
		SELECT current_viewers
		FROM periscope.live_streams FINAL
		WHERE tenant_id = ? AND internal_name = ?
	`, tenantID, internalName).Scan(&resp.CurrentViewers)
	if err != nil {
		s.logger.WithError(err).Debug("No live stream found for current viewers")
	}

	// Get historical stats from stream_events (total_viewers is logged per event)
	err = s.clickhouse.QueryRowContext(ctx, `
		SELECT
			max(total_viewers) as peak_viewers
		FROM periscope.stream_events
		WHERE tenant_id = ? AND internal_name = ?
		AND timestamp >= now() - INTERVAL 24 HOUR
		AND total_viewers IS NOT NULL
	`, tenantID, internalName).Scan(&resp.PeakViewers)
	if err != nil {
		s.logger.WithError(err).Debug("Failed to get peak viewers from stream_events")
	}

	// Get total connections from connection_events
	err = s.clickhouse.QueryRowContext(ctx, `
		SELECT countIf(event_type = 'connect') as total_connections
		FROM periscope.connection_events
		WHERE tenant_id = ? AND internal_name = ?
		AND timestamp >= now() - INTERVAL 24 HOUR
	`, tenantID, internalName).Scan(&resp.TotalConnections)
	if err != nil {
		s.logger.WithError(err).Debug("Failed to get total connections")
	}

	// Get viewer history from stream_events (time-series of total_viewers)
	historyQuery := `
		SELECT timestamp, total_viewers
		FROM periscope.stream_events
		WHERE tenant_id = ? AND internal_name = ?
		AND timestamp >= now() - INTERVAL 24 HOUR
		AND total_viewers IS NOT NULL
		ORDER BY timestamp DESC
		LIMIT 100
	`
	rows, err := s.clickhouse.QueryContext(ctx, historyQuery, tenantID, internalName)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var ts time.Time
			var viewerCount int32
			if err := rows.Scan(&ts, &viewerCount); err == nil {
				resp.ViewerHistory = append(resp.ViewerHistory, &pb.ViewerHistoryEntry{
					Timestamp:   timestamppb.New(ts),
					ViewerCount: viewerCount,
				})
			}
		}
	}

	// Get geographic stats from connection_events
	geoQuery := `
		SELECT
			uniq(country_code) as unique_countries,
			uniq(city) as unique_cities
		FROM periscope.connection_events
		WHERE tenant_id = ? AND internal_name = ?
		AND timestamp >= now() - INTERVAL 24 HOUR
	`
	var geoStats pb.ViewerGeographicStats
	if err := s.clickhouse.QueryRowContext(ctx, geoQuery, tenantID, internalName).Scan(&geoStats.UniqueCountries, &geoStats.UniqueCities); err == nil {
		resp.GeoStats = &geoStats
	}

	return &resp, nil
}

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
	countQuery := `SELECT count(*) FROM periscope.viewer_sessions FINAL WHERE tenant_id = ? AND connected_at >= ? AND connected_at <= ?`
	countArgs := []interface{}{tenantID, startTime, endTime}
	if streamID != "" {
		countQuery += " AND internal_name = ?"
		countArgs = append(countArgs, streamID)
	}
	countCh := s.countAsync(ctx, countQuery, countArgs...)

	// Query viewer_sessions with correct column names
	query := `
		SELECT session_id, connected_at, internal_name, node_id,
			connector, country_code, city,
			latitude, longitude, session_duration, bytes_transferred
		FROM periscope.viewer_sessions FINAL
		WHERE tenant_id = ? AND connected_at >= ? AND connected_at <= ?
	`
	args := []interface{}{tenantID, startTime, endTime}

	if streamID != "" {
		query += " AND internal_name = ?"
		args = append(args, streamID)
	}

	keysetCond, keysetArgs := buildKeysetCondition(params, "connected_at", "session_id")
	if keysetCond != "" {
		query += keysetCond
		args = append(args, keysetArgs...)
	}

	query += buildOrderBy(params, "connected_at", "session_id")
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var sessions []*pb.ViewerSession
	for rows.Next() {
		var session pb.ViewerSession
		var connectedAt time.Time
		var sessionDuration uint32
		var bytesTransferred uint64

		err := rows.Scan(
			&session.SessionId, &connectedAt, &session.InternalName, &session.NodeId,
			&session.Connector, &session.CountryCode, &session.City,
			&session.Latitude, &session.Longitude, &sessionDuration, &bytesTransferred,
		)
		if err != nil {
			s.logger.WithError(err).Debug("Failed to scan viewer session row")
			continue
		}

		session.TenantId = tenantID
		session.Timestamp = timestamppb.New(connectedAt)
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

	// Query stream_events.total_viewers - this is the actual viewer count logged by Foghorn
	// on every StreamLifecycleUpdate event. It's NOT an event count, it's the concurrent viewer count.
	query := fmt.Sprintf(`
		SELECT
			toStartOfInterval(timestamp, INTERVAL %s) as bucket,
			internal_name,
			max(total_viewers) as viewer_count
		FROM periscope.stream_events
		WHERE tenant_id = ? AND timestamp >= ? AND timestamp <= ?
		AND total_viewers IS NOT NULL
	`, clickhouseInterval)
	args := []interface{}{tenantID, startTime, endTime}

	if stream := req.GetStream(); stream != "" {
		query += " AND internal_name = ?"
		args = append(args, stream)
	}

	query += " GROUP BY bucket, internal_name ORDER BY bucket ASC"

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var buckets []*pb.ViewerCountBucket
	for rows.Next() {
		var bucket time.Time
		var internalName string
		var viewerCount int32

		if err := rows.Scan(&bucket, &internalName, &viewerCount); err != nil {
			s.logger.WithError(err).Debug("Failed to scan viewer count bucket row")
			continue
		}

		buckets = append(buckets, &pb.ViewerCountBucket{
			Timestamp:    timestamppb.New(bucket),
			InternalName: internalName,
			ViewerCount:  viewerCount,
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

	// Build base WHERE clause - query connection_events for geographic viewer distribution
	whereClause := "WHERE tenant_id = ? AND timestamp >= ? AND timestamp <= ? AND event_type = 'connect'"
	args := []interface{}{tenantID, startTime, endTime}

	if stream := req.GetStream(); stream != "" {
		whereClause += " AND internal_name = ?"
		args = append(args, stream)
	}

	// Query for country aggregates - count distinct sessions per country
	countryQuery := fmt.Sprintf(`
		SELECT country_code, count(DISTINCT session_id) as cnt
		FROM periscope.connection_events
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

	// Query for city aggregates - count distinct sessions per city
	cityQuery := fmt.Sprintf(`
		SELECT city, country_code, count(DISTINCT session_id) as cnt, any(latitude) as lat, any(longitude) as lon
		FROM periscope.connection_events
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

	// Query for unique counts
	uniqueQuery := fmt.Sprintf(`
		SELECT uniq(country_code), uniq(city)
		FROM periscope.connection_events
		%s
	`, whereClause)

	var uniqueCountries, uniqueCities int32
	if err := s.clickhouse.QueryRowContext(ctx, uniqueQuery, args...).Scan(&uniqueCountries, &uniqueCities); err != nil {
		s.logger.WithError(err).Warn("Failed to get unique geographic counts")
	}

	return &pb.GetGeographicDistributionResponse{
		TopCountries:    topCountries,
		TopCities:       topCities,
		UniqueCountries: uniqueCountries,
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

	internalName := req.GetInternalName()
	if internalName == "" {
		return nil, status.Error(codes.InvalidArgument, "internal_name required")
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
		WHERE tenant_id = ? AND internal_name = ? AND timestamp >= ? AND timestamp <= ?
	`, tenantID, internalName, startTime, endTime)

	query := `
		SELECT event_id, timestamp, node_id, track_list, track_count, internal_name
		FROM track_list_events
		WHERE tenant_id = ? AND internal_name = ? AND timestamp >= ? AND timestamp <= ?
	`
	args := []interface{}{tenantID, internalName, startTime, endTime}

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
		var streamName string

		err := rows.Scan(&eventID, &ts, &event.NodeId, &trackListJSON, &event.TrackCount, &streamName)
		if err != nil {
			continue
		}

		event.Id = eventID.String()
		event.Timestamp = timestamppb.New(ts)
		event.TrackList = trackListJSON
		event.Stream = streamName

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
	stream := req.GetStream()
	countQuery := `SELECT count(*) FROM periscope.connection_events WHERE tenant_id = ? AND timestamp >= ? AND timestamp <= ?`
	countArgs := []interface{}{tenantID, startTime, endTime}
	if stream != "" {
		countQuery += " AND internal_name = ?"
		countArgs = append(countArgs, stream)
	}
	countCh := s.countAsync(ctx, countQuery, countArgs...)

	query := `
		SELECT event_id, timestamp, tenant_id, internal_name, session_id,
		       connection_addr, connector, node_id, country_code, city,
		       latitude, longitude,
		       client_bucket_h3, client_bucket_res, node_bucket_h3, node_bucket_res,
		       event_type, session_duration, bytes_transferred
		FROM periscope.connection_events
		WHERE tenant_id = ? AND timestamp >= ? AND timestamp <= ?
	`
	args := []interface{}{tenantID, startTime, endTime}

	if stream != "" {
		query += " AND internal_name = ?"
		args = append(args, stream)
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

		err := rows.Scan(
			&event.EventId, &ts, &event.TenantId, &event.InternalName, &event.SessionId,
			&event.ConnectionAddr, &event.Connector, &event.NodeId, &event.CountryCode, &event.City,
			&event.Latitude, &event.Longitude,
			&clientBucketH3, &clientBucketRes, &nodeBucketH3, &nodeBucketRes,
			&event.EventType, &event.SessionDurationSeconds, &event.BytesTransferred,
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
	countQuery := `SELECT count(*) FROM node_metrics WHERE tenant_id = ? AND timestamp >= ? AND timestamp <= ?`
	countArgs := []interface{}{tenantID, startTime, endTime}
	if nodeID != "" {
		countQuery += " AND node_id = ?"
		countArgs = append(countArgs, nodeID)
	}
	countCh := s.countAsync(ctx, countQuery, countArgs...)

	query := `
		SELECT timestamp, node_id, cpu_usage, ram_max, ram_current,
		       shm_total_bytes, shm_used_bytes, disk_total_bytes, disk_used_bytes,
		       bandwidth_in, bandwidth_out, up_speed, down_speed,
		       connections_current, stream_count, is_healthy,
		       latitude, longitude
		FROM node_metrics
		WHERE tenant_id = ? AND timestamp >= ? AND timestamp <= ?
	`
	args := []interface{}{tenantID, startTime, endTime}

	if nodeID != "" {
		query += " AND node_id = ?"
		args = append(args, nodeID)
	}

	keysetCond, keysetArgs := buildKeysetCondition(params, "timestamp", "node_id")
	if keysetCond != "" {
		query += keysetCond
		args = append(args, keysetArgs...)
	}

	query += buildOrderBy(params, "timestamp", "node_id")
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

		err := rows.Scan(
			&ts, &m.NodeId, &m.CpuUsage, &m.RamMax, &m.RamCurrent,
			&m.ShmTotalBytes, &m.ShmUsedBytes, &m.DiskTotalBytes, &m.DiskUsedBytes,
			&m.BandwidthIn, &m.BandwidthOut, &m.UpSpeed, &m.DownSpeed,
			&m.ConnectionsCurrent, &m.StreamCount, &m.IsHealthy,
			&m.Latitude, &m.Longitude,
		)
		if err != nil {
			continue
		}

		// Generate composite ID for pagination
		m.Id = fmt.Sprintf("%s_%s", ts.Format(time.RFC3339), m.NodeId)
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
		startCursor = pagination.EncodeCursor(metrics[0].Timestamp.AsTime(), metrics[0].NodeId)
		endCursor = pagination.EncodeCursor(metrics[len(metrics)-1].Timestamp.AsTime(), metrics[len(metrics)-1].NodeId)
	}

	return &pb.GetNodeMetricsResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Metrics:    metrics,
	}, nil
}

func (s *PeriscopeServer) GetNodeMetrics1H(ctx context.Context, req *pb.GetNodeMetrics1HRequest) (*pb.GetNodeMetrics1HResponse, error) {
	// Note: node_metrics_1h is NOT tenant-scoped - it's infrastructure-level aggregation
	// The materialized view aggregates from node_metrics which has tenant_id, but the
	// hourly rollup is per-node across all tenants (nodes are shared infrastructure)

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
	countQuery := `SELECT count(*) FROM periscope.node_metrics_1h WHERE timestamp_1h >= ? AND timestamp_1h <= ?`
	countArgs := []interface{}{startTime, endTime}
	if nodeID != "" {
		countQuery += " AND node_id = ?"
		countArgs = append(countArgs, nodeID)
	}
	countCh := s.countAsync(ctx, countQuery, countArgs...)

	// Query uses actual schema columns (no id column)
	query := `
		SELECT timestamp_1h, node_id, avg_cpu, peak_cpu, avg_memory, peak_memory,
		       avg_disk, peak_disk, avg_shm, peak_shm, total_bandwidth_in, total_bandwidth_out, was_healthy
		FROM periscope.node_metrics_1h
		WHERE timestamp_1h >= ? AND timestamp_1h <= ?
	`
	args := []interface{}{startTime, endTime}

	if nodeID != "" {
		query += " AND node_id = ?"
		args = append(args, nodeID)
	}

	// Note: This table has no unique ID column, so keyset uses timestamp only
	if params.Cursor != nil {
		if params.Direction == pagination.Backward {
			query += " AND timestamp_1h > ?"
		} else {
			query += " AND timestamp_1h < ?"
		}
		args = append(args, params.Cursor.Timestamp)
	}

	if params.Direction == pagination.Backward {
		query += " ORDER BY timestamp_1h ASC, node_id"
	} else {
		query += " ORDER BY timestamp_1h DESC, node_id"
	}
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
		var wasHealthy uint8

		err := rows.Scan(
			&ts, &m.NodeId, &m.AvgCpu, &m.PeakCpu, &m.AvgMemory, &m.PeakMemory,
			&m.AvgDisk, &m.PeakDisk, &m.AvgShm, &m.PeakShm, &m.TotalBandwidthIn, &m.TotalBandwidthOut, &wasHealthy,
		)
		if err != nil {
			s.logger.WithError(err).Warn("Failed to scan node_metrics_1h row")
			continue
		}

		m.Timestamp = timestamppb.New(ts)
		m.WasHealthy = wasHealthy == 1
		m.Id = fmt.Sprintf("%d-%s", ts.Unix(), m.NodeId)
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
	countQuery := fmt.Sprintf(`SELECT count(*) FROM periscope.routing_events WHERE %s AND timestamp >= ? AND timestamp <= ?`, inClause)
	countArgs := []interface{}{}
	for _, id := range allIDs {
		countArgs = append(countArgs, id)
	}
	countArgs = append(countArgs, startTime, endTime)

	if stream := req.GetStream(); stream != "" {
		countQuery += " AND internal_name = ?"
		countArgs = append(countArgs, stream)
	}

	var total int32
	if err := s.clickhouse.QueryRowContext(ctx, countQuery, countArgs...).Scan(&total); err != nil {
		s.logger.WithError(err).Warn("Failed to get routing events count")
	}

	// Main query with all geographic columns from ClickHouse schema
	query := fmt.Sprintf(`
		SELECT timestamp, internal_name, selected_node, status, details, score,
		       client_country, client_latitude, client_longitude, client_bucket_h3, client_bucket_res,
		       node_latitude, node_longitude, node_name, node_bucket_h3, node_bucket_res,
		       routing_distance_km, tenant_id
		FROM periscope.routing_events
		WHERE %s AND timestamp >= ? AND timestamp <= ?
	`, inClause)

	args := []interface{}{}
	for _, id := range allIDs {
		args = append(args, id)
	}
	args = append(args, startTime, endTime)

	if stream := req.GetStream(); stream != "" {
		query += " AND internal_name = ?"
		args = append(args, stream)
	}

	if params.Cursor != nil {
		// Cursor ID contains "internal_name|selected_node"
		parts := strings.SplitN(params.Cursor.ID, "|", 2)
		if len(parts) == 2 {
			if params.Direction == pagination.Backward {
				query += " AND (timestamp, internal_name, selected_node) > (?, ?, ?)"
			} else {
				query += " AND (timestamp, internal_name, selected_node) < (?, ?, ?)"
			}
			args = append(args, params.Cursor.Timestamp, parts[0], parts[1])
		}
	}

	if params.Direction == pagination.Backward {
		query += " ORDER BY timestamp ASC, internal_name ASC, selected_node ASC"
	} else {
		query += " ORDER BY timestamp DESC, internal_name DESC, selected_node DESC"
	}
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var events []*pb.RoutingEvent
	for rows.Next() {
		var ts time.Time
		var streamName, selectedNode, statusStr, rowTenantID string
		var details *string
		var score *int64
		var clientCountry *string
		var clientLat, clientLon, nodeLat, nodeLon *float64
		var clientBucketH3, nodeBucketH3 *uint64
		var clientBucketRes, nodeBucketRes *uint8
		var nodeName *string
		var routingDistance *float64

		err := rows.Scan(&ts, &streamName, &selectedNode, &statusStr, &details, &score,
			&clientCountry, &clientLat, &clientLon, &clientBucketH3, &clientBucketRes,
			&nodeLat, &nodeLon, &nodeName, &nodeBucketH3, &nodeBucketRes,
			&routingDistance, &rowTenantID)
		if err != nil {
			s.logger.WithError(err).Debug("Failed to scan routing event row")
			continue
		}

		event := &pb.RoutingEvent{
			Id:           fmt.Sprintf("%d-%s-%s", ts.UnixNano(), streamName, selectedNode),
			Timestamp:    timestamppb.New(ts),
			Stream:       streamName,
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
		if routingDistance != nil {
			event.RoutingDistance = routingDistance
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
		startCursor = pagination.EncodeCursor(first.Timestamp.AsTime(), first.Stream+"|"+first.SelectedNode)
		endCursor = pagination.EncodeCursor(last.Timestamp.AsTime(), last.Stream+"|"+last.SelectedNode)
	}

	return &pb.GetRoutingEventsResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Events:     events,
	}, nil
}

// ============================================================================
// RealtimeAnalyticsService Implementation
// ============================================================================

func (s *PeriscopeServer) GetRealtimeStreams(ctx context.Context, req *pb.GetRealtimeStreamsRequest) (*pb.GetRealtimeStreamsResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	// Simple query against live_streams for currently live streams
	query := `
		SELECT
			internal_name,
			current_viewers,
			downloaded_bytes,
			uploaded_bytes,
			status,
			node_id,
			quality_tier
		FROM live_streams FINAL
		WHERE tenant_id = ? AND status = 'live'
		ORDER BY current_viewers DESC
		LIMIT 100
	`

	rows, err := s.clickhouse.QueryContext(ctx, query, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var streams []*pb.RealtimeStream
	for rows.Next() {
		var stream pb.RealtimeStream
		var qualityTier *string

		err := rows.Scan(
			&stream.InternalName, &stream.CurrentViewers,
			&stream.BandwidthIn, &stream.BandwidthOut,
			&stream.Status, &stream.NodeId,
			&qualityTier,
		)
		if err != nil {
			continue
		}

		streams = append(streams, &stream)
	}

	return &pb.GetRealtimeStreamsResponse{
		Streams: streams,
		Count:   int32(len(streams)),
	}, nil
}

func (s *PeriscopeServer) GetRealtimeViewers(ctx context.Context, req *pb.GetRealtimeViewersRequest) (*pb.GetRealtimeViewersResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	// Query live_streams for real-time viewer counts (from Foghorn state snapshots)
	query := `
		SELECT internal_name, current_viewers
		FROM periscope.live_streams FINAL
		WHERE tenant_id = ? AND status = 'live'
		ORDER BY current_viewers DESC
		LIMIT 1000
	`

	rows, err := s.clickhouse.QueryContext(ctx, query, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var streamViewers []*pb.RealtimeStreamViewer
	var totalViewers int32
	streamNames := make([]string, 0)
	streamViewerMap := make(map[string]*pb.RealtimeStreamViewer)

	for rows.Next() {
		var internalName string
		var currentViewers uint32
		if err := rows.Scan(&internalName, &currentViewers); err != nil {
			continue
		}
		sv := &pb.RealtimeStreamViewer{
			InternalName: internalName,
			AvgViewers:   float64(currentViewers),
			PeakViewers:  float64(currentViewers),
		}
		streamViewers = append(streamViewers, sv)
		streamViewerMap[internalName] = sv
		streamNames = append(streamNames, internalName)
		totalViewers += int32(currentViewers)
	}

	// Get geographic counts from connection_events for these streams (last 5 minutes)
	if len(streamNames) > 0 {
		geoQuery := `
			SELECT internal_name, uniq(country_code) as unique_countries, uniq(city) as unique_cities
			FROM periscope.connection_events
			WHERE tenant_id = ? AND timestamp >= now() - INTERVAL 5 MINUTE
			AND event_type = 'connect'
			GROUP BY internal_name
		`
		geoRows, err := s.clickhouse.QueryContext(ctx, geoQuery, tenantID)
		if err == nil {
			defer geoRows.Close()
			for geoRows.Next() {
				var internalName string
				var uniqueCountries, uniqueCities int32
				if err := geoRows.Scan(&internalName, &uniqueCountries, &uniqueCities); err == nil {
					if sv, ok := streamViewerMap[internalName]; ok {
						sv.UniqueCountries = uniqueCountries
						sv.UniqueCities = uniqueCities
					}
				}
			}
		}
	}

	return &pb.GetRealtimeViewersResponse{
		TotalViewers:  totalViewers,
		StreamViewers: streamViewers,
	}, nil
}

func (s *PeriscopeServer) GetRealtimeEvents(ctx context.Context, req *pb.GetRealtimeEventsRequest) (*pb.GetRealtimeEventsResponse, error) {
	tenantID := getTenantID(ctx, req.GetTenantId())
	if tenantID == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	query := `
		SELECT timestamp, event_type, event_id, status, node_id, internal_name
		FROM periscope.stream_events
		WHERE tenant_id = $1 AND timestamp >= now() - INTERVAL 5 MINUTE
		ORDER BY timestamp DESC
		LIMIT 100
	`

	rows, err := s.clickhouse.QueryContext(ctx, query, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var events []*pb.RealtimeEvent
	for rows.Next() {
		var ts time.Time
		var eventType, eventID, eventStatus, nodeID, internalName string

		err := rows.Scan(&ts, &eventType, &eventID, &eventStatus, &nodeID, &internalName)
		if err != nil {
			continue
		}

		event := &pb.RealtimeEvent{
			Timestamp: timestamppb.New(ts),
			EventType: eventType,
			StreamEvent: &pb.StreamEvent{
				EventId:      eventID,
				EventType:    eventType,
				Status:       eventStatus,
				NodeId:       nodeID,
				InternalName: internalName,
				Timestamp:    timestamppb.New(ts),
			},
		}
		events = append(events, event)
	}

	return &pb.GetRealtimeEventsResponse{
		Events: events,
		Count:  int32(len(events)),
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

	// Parse time range from request, default to last 30 days if not provided
	startTime, endTime, err := validateTimeRangeProto(req.GetTimeRange())
	if err != nil {
		// Default to last 30 days if no valid time range provided
		endTime = time.Now()
		startTime = endTime.AddDate(0, 0, -30)
	}

	resp := &pb.GetPlatformOverviewResponse{
		TenantId:    tenantID,
		GeneratedAt: timestamppb.Now(),
	}

	// Get current stream stats from live_streams (real-time snapshot)
	liveQuery := `
		SELECT
			count() as total_streams,
			countIf(status = 'live') as active_streams,
			sum(current_viewers) as total_viewers,
			avg(current_viewers) as avg_viewers,
			max(uploaded_bytes) as peak_bandwidth,
			sum(uploaded_bytes) as total_upload_bytes,
			sum(downloaded_bytes) as total_download_bytes
		FROM live_streams FINAL
		WHERE tenant_id = ?
	`

	err = s.clickhouse.QueryRowContext(ctx, liveQuery, tenantID).Scan(
		&resp.TotalStreams, &resp.ActiveStreams, &resp.TotalViewers, &resp.AverageViewers, &resp.PeakBandwidth,
		&resp.TotalUploadBytes, &resp.TotalDownloadBytes,
	)
	if err != nil {
		s.logger.WithError(err).Error("Failed to get platform overview from live_streams")
	}

	// Get historical metrics from tenant_viewer_daily (pre-aggregated from connection_events)
	// This table has: egress_gb, viewer_hours, unique_viewers, total_sessions
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
		s.logger.WithError(err).Debug("Failed to get historical metrics from tenant_viewer_daily")
	}

	// Get ingest hours (total time streams were live) from stream_events
	// Counts distinct stream-hour buckets as a proxy for streaming hours
	ingestQuery := `
		SELECT COALESCE(
			countDistinct(concat(internal_name, toString(toStartOfHour(timestamp)))),
			0
		) as stream_hours
		FROM periscope.stream_events
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
		s.logger.WithError(err).Debug("Failed to get stream hours from stream_events")
	}

	// Get true peak concurrent viewers (max at any instant) from stream_events
	peakConcurrentQuery := `
		SELECT COALESCE(max(total_viewers), 0) as peak_concurrent
		FROM periscope.stream_events
		WHERE tenant_id = ?
		AND timestamp BETWEEN ? AND ?
		AND total_viewers IS NOT NULL
	`

	var peakConcurrent int32
	err = s.clickhouse.QueryRowContext(ctx, peakConcurrentQuery, tenantID, startTime, endTime).Scan(&peakConcurrent)
	if err == nil {
		resp.PeakConcurrentViewers = peakConcurrent
	} else {
		s.logger.WithError(err).Debug("Failed to get peak concurrent viewers from stream_events")
	}

	// Get total views count from tenant_analytics_daily (pre-computed rollup)
	// This table aggregates connection_events by day for efficient dashboard queries
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
		s.logger.WithError(err).Debug("Failed to get total views from tenant_analytics_daily")
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
	internalName := req.GetInternalName()
	stage := req.GetStage()
	countQuery := `SELECT count(*) FROM periscope.clip_events WHERE tenant_id = ? AND timestamp >= ? AND timestamp <= ?`
	countArgs := []interface{}{tenantID, startTime, endTime}
	if internalName != "" {
		countQuery += " AND internal_name = ?"
		countArgs = append(countArgs, internalName)
	}
	if stage != "" {
		countQuery += " AND stage = ?"
		countArgs = append(countArgs, stage)
	}
	countCh := s.countAsync(ctx, countQuery, countArgs...)

	query := `
		SELECT request_id, timestamp, internal_name, stage, content_type,
		       start_unix, stop_unix, ingest_node_id, percent, message, file_path, s3_url, size_bytes
		FROM periscope.clip_events
		WHERE tenant_id = ? AND timestamp >= ? AND timestamp <= ?
	`
	args := []interface{}{tenantID, startTime, endTime}

	if internalName != "" {
		query += " AND internal_name = ?"
		args = append(args, internalName)
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

		err := rows.Scan(
			&event.RequestId, &ts, &event.InternalName, &event.Stage, &contentType,
			&startUnix, &stopUnix, &ingestNodeID, &percent, &message, &filePath, &s3URL, &sizeBytes,
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
		SELECT tenant_id, request_id, internal_name, content_type, stage,
		       progress_percent, error_message, requested_at, started_at, completed_at,
		       clip_start_unix, clip_stop_unix, segment_count, manifest_path,
		       file_path, s3_url, size_bytes, processing_node_id, updated_at
		FROM live_artifacts FINAL
		WHERE tenant_id = ? AND request_id = ?
	`

	var artifact pb.ArtifactState
	var errorMessage, manifestPath, filePath, s3URL, processingNodeID *string
	var startedAt, completedAt *time.Time
	var clipStartUnix, clipStopUnix *int64
	var segmentCount *uint32
	var sizeBytes *uint64
	var requestedAt, updatedAt time.Time
	var progressPercent uint8

	err := s.clickhouse.QueryRowContext(ctx, query, tenantID, requestID).Scan(
		&artifact.TenantId, &artifact.RequestId, &artifact.InternalName, &artifact.ContentType, &artifact.Stage,
		&progressPercent, &errorMessage, &requestedAt, &startedAt, &completedAt,
		&clipStartUnix, &clipStopUnix, &segmentCount, &manifestPath,
		&filePath, &s3URL, &sizeBytes, &processingNodeID, &updatedAt,
	)
	if err != nil {
		s.logger.WithError(err).WithField("request_id", requestID).Debug("Artifact not found")
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
	internalName := req.GetInternalName()
	contentType := req.GetContentType()
	stage := req.GetStage()

	countQuery := `SELECT count(*) FROM live_artifacts FINAL WHERE tenant_id = ?`
	countArgs := []interface{}{tenantID}
	if internalName != "" {
		countQuery += " AND internal_name = ?"
		countArgs = append(countArgs, internalName)
	}
	if contentType != "" {
		countQuery += " AND content_type = ?"
		countArgs = append(countArgs, contentType)
	}
	if stage != "" {
		countQuery += " AND stage = ?"
		countArgs = append(countArgs, stage)
	}
	countCh := s.countAsync(ctx, countQuery, countArgs...)

	query := `
		SELECT tenant_id, request_id, internal_name, content_type, stage,
		       progress_percent, error_message, requested_at, started_at, completed_at,
		       clip_start_unix, clip_stop_unix, segment_count, manifest_path,
		       file_path, s3_url, size_bytes, processing_node_id, updated_at
		FROM live_artifacts FINAL
		WHERE tenant_id = ?
	`
	args := []interface{}{tenantID}

	if internalName != "" {
		query += " AND internal_name = ?"
		args = append(args, internalName)
	}

	if contentType != "" {
		query += " AND content_type = ?"
		args = append(args, contentType)
	}

	if stage != "" {
		query += " AND stage = ?"
		args = append(args, stage)
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
		var progressPercent uint8

		err := rows.Scan(
			&artifact.TenantId, &artifact.RequestId, &artifact.InternalName, &artifact.ContentType, &artifact.Stage,
			&progressPercent, &errorMessage, &requestedAt, &startedAt, &completedAt,
			&clipStartUnix, &clipStopUnix, &segmentCount, &manifestPath,
			&filePath, &s3URL, &sizeBytes, &processingNodeID, &updatedAt,
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

	stream := req.GetStream()

	// Build count query
	countQuery := `SELECT count(*) FROM stream_connection_hourly WHERE tenant_id = ? AND hour >= ? AND hour <= ?`
	countArgs := []interface{}{tenantID, startTime, endTime}
	if stream != "" {
		countQuery += " AND internal_name = ?"
		countArgs = append(countArgs, stream)
	}
	countCh := s.countAsync(ctx, countQuery, countArgs...)

	// AggregatingMergeTree requires Merge functions to read aggregated values
	query := `
		SELECT hour, tenant_id, internal_name,
		       sumMerge(total_bytes) as total_bytes,
		       uniqMerge(unique_viewers) as unique_viewers,
		       countMerge(total_sessions) as total_sessions
		FROM stream_connection_hourly
		WHERE tenant_id = ? AND hour >= ? AND hour <= ?
	`
	args := []interface{}{tenantID, startTime, endTime}

	if stream != "" {
		query += " AND internal_name = ?"
		args = append(args, stream)
	}

	query += " GROUP BY hour, tenant_id, internal_name"

	if params.Cursor != nil {
		if params.Direction == pagination.Backward {
			query = `SELECT * FROM (` + query + `) WHERE (hour, internal_name) > (?, ?)`
		} else {
			query = `SELECT * FROM (` + query + `) WHERE (hour, internal_name) < (?, ?)`
		}
		args = append(args, params.Cursor.Timestamp, params.Cursor.ID)
	}

	if params.Direction == pagination.Backward {
		query += " ORDER BY hour ASC, internal_name"
	} else {
		query += " ORDER BY hour DESC, internal_name"
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
		var tenantIDStr, internalName string
		var totalBytes, uniqueViewers, totalSessions uint64

		err := rows.Scan(&hour, &tenantIDStr, &internalName, &totalBytes, &uniqueViewers, &totalSessions)
		if err != nil {
			s.logger.WithError(err).Error("Failed to scan stream_connection_hourly row")
			continue
		}

		records = append(records, &pb.StreamConnectionHourly{
			Id:            fmt.Sprintf("%s_%s", hour.Format(time.RFC3339), internalName),
			Hour:          timestamppb.New(hour),
			TenantId:      tenantIDStr,
			InternalName:  internalName,
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
		startCursor = pagination.EncodeCursor(records[0].Hour.AsTime(), records[0].InternalName)
		endCursor = pagination.EncodeCursor(records[len(records)-1].Hour.AsTime(), records[len(records)-1].InternalName)
	}

	return &pb.GetStreamConnectionHourlyResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Records:    records,
	}, nil
}

// GetClientMetrics5M returns 5-minute client metrics aggregates from client_metrics_5m MV
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

	stream := req.GetStream()
	nodeID := req.GetNodeId()

	// Build count query
	countQuery := `SELECT count(*) FROM client_metrics_5m WHERE tenant_id = ? AND timestamp_5m >= ? AND timestamp_5m <= ?`
	countArgs := []interface{}{tenantID, startTime, endTime}
	if stream != "" {
		countQuery += " AND internal_name = ?"
		countArgs = append(countArgs, stream)
	}
	if nodeID != "" {
		countQuery += " AND node_id = ?"
		countArgs = append(countArgs, nodeID)
	}
	countCh := s.countAsync(ctx, countQuery, countArgs...)

	query := `
		SELECT timestamp_5m, tenant_id, internal_name, node_id, active_sessions,
		       avg_bw_in, avg_bw_out, avg_connection_time, pkt_loss_rate, avg_connection_quality
		FROM client_metrics_5m
		WHERE tenant_id = ? AND timestamp_5m >= ? AND timestamp_5m <= ?
	`
	args := []interface{}{tenantID, startTime, endTime}

	if stream != "" {
		query += " AND internal_name = ?"
		args = append(args, stream)
	}

	if nodeID != "" {
		query += " AND node_id = ?"
		args = append(args, nodeID)
	}

	if params.Cursor != nil {
		if params.Direction == pagination.Backward {
			query += " AND (timestamp_5m, internal_name, node_id) > (?, ?, ?)"
		} else {
			query += " AND (timestamp_5m, internal_name, node_id) < (?, ?, ?)"
		}
		args = append(args, params.Cursor.Timestamp, params.Cursor.ID, params.Cursor.ID)
	}

	if params.Direction == pagination.Backward {
		query += " ORDER BY timestamp_5m ASC, internal_name, node_id"
	} else {
		query += " ORDER BY timestamp_5m DESC, internal_name, node_id"
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
		var tenantIDStr, internalName, nodeIDStr string
		var activeSessions uint32
		var avgBwIn, avgBwOut float64
		var avgConnTime float32
		var pktLossRate, avgConnQuality sql.NullFloat64

		err := rows.Scan(&timestamp, &tenantIDStr, &internalName, &nodeIDStr, &activeSessions,
			&avgBwIn, &avgBwOut, &avgConnTime, &pktLossRate, &avgConnQuality)
		if err != nil {
			s.logger.WithError(err).Error("Failed to scan client_metrics_5m row")
			continue
		}

		record := &pb.ClientMetrics5M{
			Id:                fmt.Sprintf("%s_%s_%s", timestamp.Format(time.RFC3339), internalName, nodeIDStr),
			Timestamp:         timestamppb.New(timestamp),
			TenantId:          tenantIDStr,
			InternalName:      internalName,
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
		if avgConnQuality.Valid {
			v := float32(avgConnQuality.Float64)
			record.AvgConnectionQuality = &v
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
		startCursor = pagination.EncodeCursor(records[0].Timestamp.AsTime(), records[0].InternalName)
		endCursor = pagination.EncodeCursor(records[len(records)-1].Timestamp.AsTime(), records[len(records)-1].InternalName)
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

	stream := req.GetStream()

	// Build count query
	countQuery := `SELECT count(*) FROM quality_tier_daily WHERE tenant_id = ? AND day >= toDate(?) AND day <= toDate(?)`
	countArgs := []interface{}{tenantID, startTime, endTime}
	if stream != "" {
		countQuery += " AND internal_name = ?"
		countArgs = append(countArgs, stream)
	}
	countCh := s.countAsync(ctx, countQuery, countArgs...)

	// SummingMergeTree sums numeric columns - need to SUM in query
	query := `
		SELECT day, tenant_id, internal_name,
		       sum(tier_1080p_minutes), sum(tier_720p_minutes), sum(tier_480p_minutes), sum(tier_sd_minutes),
		       any(primary_tier), sum(codec_h264_minutes), sum(codec_h265_minutes),
		       avg(avg_bitrate), avg(avg_fps)
		FROM quality_tier_daily
		WHERE tenant_id = ? AND day >= toDate(?) AND day <= toDate(?)
	`
	args := []interface{}{tenantID, startTime, endTime}

	if stream != "" {
		query += " AND internal_name = ?"
		args = append(args, stream)
	}

	query += " GROUP BY day, tenant_id, internal_name"

	if params.Cursor != nil {
		if params.Direction == pagination.Backward {
			query = `SELECT * FROM (` + query + `) WHERE (day, internal_name) > (?, ?)`
		} else {
			query = `SELECT * FROM (` + query + `) WHERE (day, internal_name) < (?, ?)`
		}
		args = append(args, params.Cursor.Timestamp, params.Cursor.ID)
	}

	if params.Direction == pagination.Backward {
		query += " ORDER BY day ASC, internal_name"
	} else {
		query += " ORDER BY day DESC, internal_name"
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
		var tenantIDStr, internalName, primaryTier string
		var tier1080p, tier720p, tier480p, tierSD, codecH264, codecH265, avgBitrate uint32
		var avgFps float32

		err := rows.Scan(&day, &tenantIDStr, &internalName, &tier1080p, &tier720p, &tier480p, &tierSD,
			&primaryTier, &codecH264, &codecH265, &avgBitrate, &avgFps)
		if err != nil {
			s.logger.WithError(err).Error("Failed to scan quality_tier_daily row")
			continue
		}

		records = append(records, &pb.QualityTierDaily{
			Id:                fmt.Sprintf("%s_%s", day.Format("2006-01-02"), internalName),
			Day:               timestamppb.New(day),
			TenantId:          tenantIDStr,
			InternalName:      internalName,
			Tier_1080PMinutes: tier1080p,
			Tier_720PMinutes:  tier720p,
			Tier_480PMinutes:  tier480p,
			TierSdMinutes:     tierSD,
			PrimaryTier:       primaryTier,
			CodecH264Minutes:  codecH264,
			CodecH265Minutes:  codecH265,
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
		startCursor = pagination.EncodeCursor(records[0].Day.AsTime(), records[0].InternalName)
		endCursor = pagination.EncodeCursor(records[len(records)-1].Day.AsTime(), records[len(records)-1].InternalName)
	}

	return &pb.GetQualityTierDailyResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Records:    records,
	}, nil
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

	// Build count query
	countQuery := `SELECT count(*) FROM storage_snapshots WHERE tenant_id = ? AND timestamp >= ? AND timestamp <= ?`
	countArgs := []interface{}{tenantID, startTime, endTime}
	if nodeID != "" {
		countQuery += " AND node_id = ?"
		countArgs = append(countArgs, nodeID)
	}
	countCh := s.countAsync(ctx, countQuery, countArgs...)

	query := `
		SELECT timestamp, tenant_id, node_id, total_bytes, file_count,
		       dvr_bytes, clip_bytes, recording_bytes
		FROM storage_snapshots
		WHERE tenant_id = ? AND timestamp >= ? AND timestamp <= ?
	`
	args := []interface{}{tenantID, startTime, endTime}

	if nodeID != "" {
		query += " AND node_id = ?"
		args = append(args, nodeID)
	}

	keysetCond, keysetArgs := buildKeysetCondition(params, "timestamp", "node_id")
	if keysetCond != "" {
		query += keysetCond
		args = append(args, keysetArgs...)
	}

	query += buildOrderBy(params, "timestamp", "node_id")
	query += fmt.Sprintf(" LIMIT %d", params.Limit+1)

	rows, err := s.clickhouse.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	defer rows.Close()

	var records []*pb.StorageUsageRecord
	for rows.Next() {
		var timestamp time.Time
		var tenantIDStr, nodeIDStr string
		var totalBytes, dvrBytes, clipBytes, recordingBytes uint64
		var fileCount uint32

		err := rows.Scan(&timestamp, &tenantIDStr, &nodeIDStr, &totalBytes, &fileCount,
			&dvrBytes, &clipBytes, &recordingBytes)
		if err != nil {
			s.logger.WithError(err).Error("Failed to scan storage_snapshots row")
			continue
		}

		records = append(records, &pb.StorageUsageRecord{
			Id:             fmt.Sprintf("%s_%s", timestamp.Format(time.RFC3339), nodeIDStr),
			Timestamp:      timestamppb.New(timestamp),
			TenantId:       tenantIDStr,
			NodeId:         nodeIDStr,
			TotalBytes:     totalBytes,
			FileCount:      fileCount,
			DvrBytes:       dvrBytes,
			ClipBytes:      clipBytes,
			RecordingBytes: recordingBytes,
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
		startCursor = pagination.EncodeCursor(records[0].Timestamp.AsTime(), records[0].NodeId)
		endCursor = pagination.EncodeCursor(records[len(records)-1].Timestamp.AsTime(), records[len(records)-1].NodeId)
	}

	return &pb.GetStorageUsageResponse{
		Pagination: buildCursorResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
		Records:    records,
	}, nil
}

// ============================================================================
// Server Setup
// ============================================================================

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
		grpc.ChainUnaryInterceptor(authInterceptor, unaryInterceptor(cfg.Logger)),
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
	pb.RegisterRealtimeAnalyticsServiceServer(server, periscopeServer)
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
		return resp, err
	}
}

// Helper to convert map to structpb.Struct
func mapToStruct(m map[string]interface{}) *structpb.Struct {
	s, err := structpb.NewStruct(m)
	if err != nil {
		return nil
	}
	return s
}
