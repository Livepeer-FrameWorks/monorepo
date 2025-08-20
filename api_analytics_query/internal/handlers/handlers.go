package handlers

import (
	"encoding/json"
	"math"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"frameworks/api_analytics_query/internal/metrics"
	"frameworks/pkg/api/periscope"
	"frameworks/pkg/database"
	"frameworks/pkg/logging"
	"frameworks/pkg/models"
)

var (
	yugaDB         database.PostgresConn
	clickhouse     database.ClickHouseConn
	logger         logging.Logger
	serviceMetrics *metrics.Metrics
)

// Init initializes the handlers package with database connections and metrics
func Init(ydb database.PostgresConn, ch database.ClickHouseConn, log logging.Logger, m *metrics.Metrics) {
	yugaDB = ydb
	clickhouse = ch
	logger = log
	serviceMetrics = m
}

// GetStreamAnalytics returns analytics for all streams with recent activity (tenant-scoped)
func GetStreamAnalytics(c *gin.Context) {
	start := time.Now()
	defer func() {
		if serviceMetrics != nil {
			serviceMetrics.QueryDuration.WithLabelValues("stream_analytics").Observe(time.Since(start).Seconds())
		}
	}()

	if serviceMetrics != nil {
		serviceMetrics.AnalyticsQueries.WithLabelValues("stream_analytics", "requested").Inc()
	}

	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		if serviceMetrics != nil {
			serviceMetrics.AnalyticsQueries.WithLabelValues("stream_analytics", "error").Inc()
		}
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Tenant context required"})
		return
	}

	// Parse optional query parameters
	streamInternalName := c.Query("stream_id") // Note: still using "stream_id" param for backward compatibility
	startTime := c.DefaultQuery("start_time", time.Now().Add(-24*time.Hour).Format(time.RFC3339))
	endTime := c.DefaultQuery("end_time", time.Now().Format(time.RFC3339))

	// Parse time parameters
	startParsed, err := time.Parse(time.RFC3339, startTime)
	if err != nil {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Invalid start_time format"})
		return
	}
	endParsed, err := time.Parse(time.RFC3339, endTime)
	if err != nil {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Invalid end_time format"})
		return
	}

	// Build query with COALESCE for NULL handling
	query := `
		SELECT sa.id, sa.tenant_id, sa.internal_name, sa.internal_name, 
		       COALESCE(sa.session_start_time, '1970-01-01 00:00:00'::timestamp) as session_start_time,
		       COALESCE(sa.session_end_time, '1970-01-01 00:00:00'::timestamp) as session_end_time, 
		       COALESCE(sa.total_session_duration, 0) as total_session_duration,
		       COALESCE(sa.current_viewers, 0) as current_viewers, 
		       COALESCE(sa.peak_viewers, 0) as peak_viewers, 
		       COALESCE(sa.total_connections, 0) as total_connections, 
		       COALESCE(sa.bandwidth_in, 0) as bandwidth_in, 
		       COALESCE(sa.bandwidth_out, 0) as bandwidth_out, 
		       COALESCE(sa.total_bandwidth_gb, 0.0) as total_bandwidth_gb,
		       COALESCE(sa.bitrate_kbps, 0) as bitrate_kbps, 
		       COALESCE(sa.resolution, '') as resolution, 
		       COALESCE(sa.packets_sent, 0) as packets_sent, 
		       COALESCE(sa.packets_lost, 0) as packets_lost,
		       COALESCE(sa.packets_retrans, 0) as packets_retrans, 
		       COALESCE(sa.upbytes, 0) as upbytes, 
		       COALESCE(sa.downbytes, 0) as downbytes, 
		       COALESCE(sa.first_ms, 0) as first_ms, 
		       COALESCE(sa.last_ms, 0) as last_ms,
		       COALESCE(sa.track_count, 0) as track_count, 
		       COALESCE(sa.inputs, '') as inputs, 
		       COALESCE(sa.outputs, '') as outputs, 
		       COALESCE(sa.node_id, '') as node_id, 
		       COALESCE(sa.node_name, '') as node_name, 
		       COALESCE(sa.latitude, 0.0) as latitude,
		       COALESCE(sa.longitude, 0.0) as longitude, 
		       COALESCE(sa.location, '') as location, 
		       COALESCE(sa.status, '') as status, 
		       sa.last_updated, sa.created_at
		FROM stream_analytics sa
		WHERE sa.tenant_id = $1 AND sa.last_updated >= $2 AND sa.last_updated <= $3`

	args := []interface{}{tenantID, startParsed, endParsed}

	// Only filter by stream if streamInternalName is provided and not empty
	if streamInternalName != "" && streamInternalName != "null" && streamInternalName != "undefined" {
		query += " AND sa.internal_name = $4"
		args = append(args, streamInternalName)
	}

	query += " ORDER BY sa.last_updated DESC"

	// Query YugaDB for state data
	rows, err := yugaDB.QueryContext(c.Request.Context(), query, args...)

	if err != nil {
		logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"error":     err,
		}).Error("Failed to fetch stream analytics from PostgreSQL")
		c.JSON(http.StatusInternalServerError, periscope.ErrorResponse{Error: "Failed to fetch analytics"})
		return
	}
	defer rows.Close()

	var analytics []models.StreamAnalytics
	for rows.Next() {
		var sa models.StreamAnalytics
		var discard string
		if err := rows.Scan(&sa.ID, &sa.TenantID, &discard, &sa.InternalName,
			&sa.SessionStartTime, &sa.SessionEndTime, &sa.TotalSessionDuration,
			&sa.CurrentViewers, &sa.PeakViewers, &sa.TotalConnections,
			&sa.BandwidthIn, &sa.BandwidthOut, &sa.TotalBandwidthGB,
			&sa.BitrateKbps, &sa.Resolution, &sa.PacketsSent, &sa.PacketsLost,
			&sa.PacketsRetrans, &sa.Upbytes, &sa.Downbytes, &sa.FirstMs, &sa.LastMs,
			&sa.TrackCount, &sa.Inputs, &sa.Outputs, &sa.NodeID, &sa.NodeName, &sa.Latitude,
			&sa.Longitude, &sa.Location, &sa.Status, &sa.LastUpdated, &sa.CreatedAt); err != nil {
			logger.WithError(err).Error("Failed to scan stream analytics")
			continue
		}
		// Do not expose StreamID in analytics
		sa.StreamID = ""
		analytics = append(analytics, sa)
	}

	// Enrich with time-series data from ClickHouse
	for i := range analytics {
		// Get viewer metrics from ClickHouse
		var viewerMetrics struct {
			AvgViewers      float64 `json:"avg_viewers"`
			UniqueCountries int     `json:"unique_countries"`
			UniqueCities    int     `json:"unique_cities"`
		}

		err := clickhouse.QueryRowContext(c.Request.Context(), `
			SELECT 
				avg(viewer_count) as avg_viewers,
				uniq(country_code) as unique_countries,
				uniq(city) as unique_cities
			FROM viewer_metrics
			WHERE tenant_id = ? AND internal_name = ?
			AND timestamp >= NOW() - INTERVAL 24 HOUR
		`, analytics[i].TenantID, analytics[i].InternalName).Scan(
			&viewerMetrics.AvgViewers,
			&viewerMetrics.UniqueCountries,
			&viewerMetrics.UniqueCities,
		)

		if err != nil && err != database.ErrNoRows {
			logger.WithError(err).Error("Failed to fetch viewer metrics from ClickHouse")
		} else if err == nil {
			analytics[i].AvgViewers = viewerMetrics.AvgViewers
			analytics[i].UniqueCountries = viewerMetrics.UniqueCountries
			analytics[i].UniqueCities = viewerMetrics.UniqueCities
		}

		// Get health metrics from ClickHouse
		var healthMetrics struct {
			AvgBufferHealth float32 `json:"avg_buffer_health"`
			AvgBitrate      int     `json:"avg_bitrate"`
			PacketLossRate  float32 `json:"packet_loss_rate"`
		}

		err = clickhouse.QueryRowContext(c.Request.Context(), `
			SELECT 
				avg(buffer_health) as avg_buffer_health,
				avg(bitrate) as avg_bitrate,
				sum(packets_lost) / sum(packets_sent) as packet_loss_rate
			FROM stream_health_metrics
			WHERE tenant_id = ? AND internal_name = ?
			AND timestamp >= NOW() - INTERVAL 24 HOUR
		`, analytics[i].TenantID, analytics[i].InternalName).Scan(
			&healthMetrics.AvgBufferHealth,
			&healthMetrics.AvgBitrate,
			&healthMetrics.PacketLossRate,
		)

		if err != nil && err != database.ErrNoRows {
			logger.WithError(err).Error("Failed to fetch health metrics from ClickHouse")
		} else if err == nil {
			analytics[i].AvgBufferHealth = healthMetrics.AvgBufferHealth
			analytics[i].AvgBitrate = healthMetrics.AvgBitrate
			analytics[i].PacketLossRate = healthMetrics.PacketLossRate
		}
	}

	// Convert to API response type
	response := periscope.StreamAnalyticsResponse(analytics)
	if serviceMetrics != nil {
		serviceMetrics.AnalyticsQueries.WithLabelValues("stream_analytics", "success").Inc()
	}
	c.JSON(http.StatusOK, response)
}

// GetViewerMetrics returns viewer metrics from ClickHouse
func GetViewerMetrics(c *gin.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Tenant context required"})
		return
	}

	// Parse query parameters
	streamID := c.Query("stream_id")
	startTimeStr := c.DefaultQuery("start_time", time.Now().Add(-24*time.Hour).Format(time.RFC3339))
	endTimeStr := c.DefaultQuery("end_time", time.Now().Format(time.RFC3339))

	// Parse time strings into time.Time objects for ClickHouse
	startTime, err := time.Parse(time.RFC3339, startTimeStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Invalid start_time format. Use RFC3339 format."})
		return
	}

	endTime, err := time.Parse(time.RFC3339, endTimeStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Invalid end_time format. Use RFC3339 format."})
		return
	}

	// Build query with optional stream filtering
	query := `
		SELECT 
			timestamp,
			internal_name,
			viewer_count,
			connection_type,
			node_id,
			country_code,
			city,
			latitude,
			longitude,
			connection_quality,
			buffer_health
		FROM viewer_metrics
		WHERE tenant_id = ?
		AND timestamp BETWEEN ? AND ?`

	args := []interface{}{tenantID, startTime, endTime}

	// Only filter by stream if streamID is provided and not empty
	if streamID != "" && streamID != "null" && streamID != "undefined" {
		query += " AND internal_name = ?"
		args = append(args, streamID)
	}

	query += " ORDER BY timestamp DESC"

	// Query ClickHouse for viewer metrics
	rows, err := clickhouse.QueryContext(c.Request.Context(), query, args...)

	if err != nil {
		logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"stream_id": streamID,
			"query":     query,
			"error":     err,
		}).Error("Failed to fetch viewer metrics from ClickHouse")

		// Return empty metrics instead of error - might be fresh setup or no data yet
		emptyMetrics := []models.AnalyticsViewerSession{}
		response := periscope.ViewerMetricsResponse(emptyMetrics)
		c.JSON(http.StatusOK, response)
		return
	}
	defer rows.Close()

	var metrics []models.AnalyticsViewerSession
	for rows.Next() {
		var m models.AnalyticsViewerSession
		if err := rows.Scan(
			&m.Timestamp,
			&m.InternalName,
			&m.ViewerCount,
			&m.ConnectionType,
			&m.NodeID,
			&m.CountryCode,
			&m.City,
			&m.Latitude,
			&m.Longitude,
			&m.ConnectionQuality,
			&m.BufferHealth,
		); err != nil {
			logger.WithError(err).Error("Failed to scan viewer metrics")
			continue
		}
		metrics = append(metrics, m)
	}

	// Convert to API response type
	response := periscope.ViewerMetricsResponse(metrics)
	c.JSON(http.StatusOK, response)
}

// GetRoutingEvents returns routing events from ClickHouse
func GetRoutingEvents(c *gin.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Tenant context required"})
		return
	}

	// Parse time range from query params
	startTimeStr := c.DefaultQuery("start_time", time.Now().Add(-24*time.Hour).Format(time.RFC3339))
	endTimeStr := c.DefaultQuery("end_time", time.Now().Format(time.RFC3339))

	// Parse time strings into time.Time objects for ClickHouse
	startTime, err := time.Parse(time.RFC3339, startTimeStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Invalid start_time format. Use RFC3339 format."})
		return
	}

	endTime, err := time.Parse(time.RFC3339, endTimeStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Invalid end_time format. Use RFC3339 format."})
		return
	}

	// Query ClickHouse for routing events
	rows, err := clickhouse.QueryContext(c.Request.Context(), `
		SELECT 
			timestamp,
			stream_name,
			selected_node,
			status,
			details,
			score,
			client_ip,
			client_country,
			client_region,
			client_city,
			client_latitude,
			client_longitude,
			node_scores,
			routing_metadata
		FROM routing_events
		WHERE tenant_id = ?
		AND timestamp BETWEEN ? AND ?
		ORDER BY timestamp DESC
	`, tenantID, startTime, endTime)

	if err != nil {
		logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"error":     err,
		}).Error("Failed to fetch routing events from ClickHouse")
		c.JSON(http.StatusInternalServerError, periscope.ErrorResponse{Error: "Failed to fetch routing events"})
		return
	}
	defer rows.Close()

	var events []models.AnalyticsRoutingEvent
	for rows.Next() {
		var e models.AnalyticsRoutingEvent
		if err := rows.Scan(
			&e.Timestamp,
			&e.StreamName,
			&e.SelectedNode,
			&e.Status,
			&e.Details,
			&e.Score,
			&e.ClientIP,
			&e.ClientCountry,
			&e.ClientRegion,
			&e.ClientCity,
			&e.ClientLatitude,
			&e.ClientLongitude,
			&e.NodeScores,
			&e.RoutingMetadata,
		); err != nil {
			logger.WithError(err).Error("Failed to scan routing event")
			continue
		}
		events = append(events, e)
	}

	response := periscope.RoutingEventsResponse(events)
	c.JSON(http.StatusOK, response)
}

// GetViewerMetrics5m returns aggregated viewer metrics from ClickHouse materialized view
func GetViewerMetrics5m(c *gin.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Tenant context required"})
		return
	}

	// Parse time range from query params
	startTimeStr := c.DefaultQuery("start_time", time.Now().Add(-24*time.Hour).Format(time.RFC3339))
	endTimeStr := c.DefaultQuery("end_time", time.Now().Format(time.RFC3339))

	// Parse time strings into time.Time objects for ClickHouse
	startTime, err := time.Parse(time.RFC3339, startTimeStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Invalid start_time format. Use RFC3339 format."})
		return
	}

	endTime, err := time.Parse(time.RFC3339, endTimeStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Invalid end_time format. Use RFC3339 format."})
		return
	}

	// Query ClickHouse materialized view
	rows, err := clickhouse.QueryContext(c.Request.Context(), `
		SELECT 
			timestamp_5m,
			internal_name,
			node_id,
			peak_viewers,
			avg_viewers,
			unique_countries,
			unique_cities,
			avg_connection_quality,
			avg_buffer_health
		FROM viewer_metrics_5m
		WHERE tenant_id = ?
		AND timestamp_5m BETWEEN ? AND ?
		ORDER BY timestamp_5m DESC
	`, tenantID, startTime, endTime)

	if err != nil {
		logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"error":     err,
		}).Error("Failed to fetch aggregated viewer metrics from ClickHouse")
		c.JSON(http.StatusInternalServerError, periscope.ErrorResponse{Error: "Failed to fetch aggregated metrics"})
		return
	}
	defer rows.Close()

	var metrics []models.AnalyticsViewerSession5m
	for rows.Next() {
		var m models.AnalyticsViewerSession5m
		if err := rows.Scan(
			&m.Timestamp,
			&m.InternalName,
			&m.NodeID,
			&m.PeakViewers,
			&m.AvgViewers,
			&m.UniqueCountries,
			&m.UniqueCities,
			&m.AvgConnectionQuality,
			&m.AvgBufferHealth,
		); err != nil {
			logger.WithError(err).Error("Failed to scan aggregated viewer metrics")
			continue
		}
		metrics = append(metrics, m)
	}

	response := periscope.ViewerMetrics5mResponse(metrics)
	c.JSON(http.StatusOK, response)
}

// GetStreamDetails returns detailed analytics for a specific stream
func GetStreamDetails(c *gin.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Tenant context required"})
		return
	}

	internalName := c.Param("internal_name")

	// Get state data from YugaDB
	var sa models.StreamAnalytics
	var discard string
	err := yugaDB.QueryRowContext(c.Request.Context(), `
		SELECT sa.id, sa.tenant_id, sa.internal_name, sa.internal_name, 
		       COALESCE(sa.session_start_time, '1970-01-01 00:00:00'::timestamp) as session_start_time,
		       COALESCE(sa.session_end_time, '1970-01-01 00:00:00'::timestamp) as session_end_time, 
		       COALESCE(sa.total_session_duration, 0) as total_session_duration,
		       COALESCE(sa.current_viewers, 0) as current_viewers, 
		       COALESCE(sa.peak_viewers, 0) as peak_viewers, 
		       COALESCE(sa.total_connections, 0) as total_connections, 
		       COALESCE(sa.bandwidth_in, 0) as bandwidth_in, 
		       COALESCE(sa.bandwidth_out, 0) as bandwidth_out, 
		       COALESCE(sa.total_bandwidth_gb, 0.0) as total_bandwidth_gb,
		       COALESCE(sa.bitrate_kbps, 0) as bitrate_kbps, 
		       COALESCE(sa.resolution, '') as resolution, 
		       COALESCE(sa.packets_sent, 0) as packets_sent, 
		       COALESCE(sa.packets_lost, 0) as packets_lost,
		       COALESCE(sa.packets_retrans, 0) as packets_retrans, 
		       COALESCE(sa.upbytes, 0) as upbytes, 
		       COALESCE(sa.downbytes, 0) as downbytes, 
		       COALESCE(sa.first_ms, 0) as first_ms, 
		       COALESCE(sa.last_ms, 0) as last_ms,
		       COALESCE(sa.track_count, 0) as track_count, 
		       COALESCE(sa.inputs, '') as inputs, 
		       COALESCE(sa.outputs, '') as outputs, 
		       COALESCE(sa.node_id, '') as node_id, 
		       COALESCE(sa.node_name, '') as node_name, 
		       COALESCE(sa.latitude, 0.0) as latitude,
		       COALESCE(sa.longitude, 0.0) as longitude, 
		       COALESCE(sa.location, '') as location, 
		       COALESCE(sa.status, '') as status, 
		       sa.last_updated, sa.created_at
		FROM stream_analytics sa
		WHERE sa.tenant_id = $1 AND sa.internal_name = $2
	`, tenantID, internalName).Scan(
		&sa.ID, &sa.TenantID, &discard, &sa.InternalName,
		&sa.SessionStartTime, &sa.SessionEndTime, &sa.TotalSessionDuration,
		&sa.CurrentViewers, &sa.PeakViewers, &sa.TotalConnections,
		&sa.BandwidthIn, &sa.BandwidthOut, &sa.TotalBandwidthGB,
		&sa.BitrateKbps, &sa.Resolution, &sa.PacketsSent, &sa.PacketsLost,
		&sa.PacketsRetrans, &sa.Upbytes, &sa.Downbytes, &sa.FirstMs, &sa.LastMs,
		&sa.TrackCount, &sa.Inputs, &sa.Outputs, &sa.NodeID, &sa.NodeName, &sa.Latitude,
		&sa.Longitude, &sa.Location, &sa.Status, &sa.LastUpdated, &sa.CreatedAt,
	)

	if err == database.ErrNoRows {
		c.JSON(http.StatusNotFound, periscope.ErrorResponse{Error: "Stream analytics not found"})
		return
	}

	if err != nil {
		logger.WithError(err).Error("Failed to fetch stream details from YugaDB")
		c.JSON(http.StatusInternalServerError, periscope.ErrorResponse{Error: "Failed to fetch stream details"})
		return
	}
	// Do not expose StreamID
	sa.StreamID = ""

	// Get time-series data from ClickHouse
	var viewerMetrics struct {
		AvgViewers      float64 `json:"avg_viewers"`
		UniqueCountries int     `json:"unique_countries"`
		UniqueCities    int     `json:"unique_cities"`
		AvgBufferHealth float32 `json:"avg_buffer_health"`
		AvgBitrate      int     `json:"avg_bitrate"`
		PacketLossRate  float32 `json:"packet_loss_rate"`
	}

	err = clickhouse.QueryRowContext(c.Request.Context(), `
		SELECT 
			avg(viewer_count) as avg_viewers,
			uniq(country_code) as unique_countries,
			uniq(city) as unique_cities,
			avg(buffer_health) as avg_buffer_health,
			avg(bitrate) as avg_bitrate,
			sum(packets_lost) / sum(packets_sent) as packet_loss_rate
		FROM stream_health_metrics
		WHERE tenant_id = ? AND internal_name = ?
		AND timestamp >= NOW() - INTERVAL 1 HOUR
	`, tenantID, sa.InternalName).Scan(
		&viewerMetrics.AvgViewers,
		&viewerMetrics.UniqueCountries,
		&viewerMetrics.UniqueCities,
		&viewerMetrics.AvgBufferHealth,
		&viewerMetrics.AvgBitrate,
		&viewerMetrics.PacketLossRate,
	)

	if err != nil && err != database.ErrNoRows {
		logger.WithError(err).Error("Failed to fetch viewer metrics from ClickHouse")
	} else if err == nil {
		sa.AvgViewers = viewerMetrics.AvgViewers
		sa.UniqueCountries = viewerMetrics.UniqueCountries
		sa.UniqueCities = viewerMetrics.UniqueCities
		sa.AvgBufferHealth = viewerMetrics.AvgBufferHealth
		sa.AvgBitrate = viewerMetrics.AvgBitrate
		sa.PacketLossRate = viewerMetrics.PacketLossRate
	}

	// Convert to API response type
	response := periscope.StreamDetailsResponse(sa)
	c.JSON(http.StatusOK, response)
}

// GetStreamEvents returns events for a specific stream
func GetStreamEvents(c *gin.Context) {
	tenantID := c.GetString("tenant_id")
	internalName := c.Param("internal_name")

	if tenantID == "" {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Tenant context required"})
		return
	}

	// Get events from ClickHouse
	rows, err := clickhouse.QueryContext(c.Request.Context(), `
		SELECT timestamp, event_id, event_type, status, node_id, event_data
		FROM stream_events 
		WHERE tenant_id = ? AND internal_name = ?
		ORDER BY timestamp DESC 
		LIMIT 100
	`, tenantID, internalName)

	if err != nil {
		logger.WithError(err).Error("Failed to fetch stream events from ClickHouse")
		c.JSON(http.StatusInternalServerError, periscope.ErrorResponse{Error: "Failed to fetch events"})
		return
	}
	defer rows.Close()

	var events []periscope.StreamEvent
	for rows.Next() {
		var timestamp time.Time
		var eventID, eventType, status, nodeID, eventData string

		if err := rows.Scan(&timestamp, &eventID, &eventType, &status, &nodeID, &eventData); err != nil {
			logger.WithError(err).Error("Failed to scan stream event")
			continue
		}

		events = append(events, periscope.StreamEvent{
			Timestamp: timestamp,
			EventID:   eventID,
			EventType: eventType,
			Status:    status,
			NodeID:    nodeID,
			EventData: eventData,
		})
	}

	response := periscope.StreamEventsResponse(events)
	c.JSON(http.StatusOK, response)
}

// GetTrackListEvents returns track list updates for a specific stream
func GetTrackListEvents(c *gin.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Tenant context required"})
		return
	}

	internalName := c.Param("internal_name")
	startTimeStr := c.DefaultQuery("start_time", time.Now().Add(-24*time.Hour).Format(time.RFC3339))
	endTimeStr := c.DefaultQuery("end_time", time.Now().Format(time.RFC3339))

	// Parse time strings into time.Time objects for ClickHouse
	startTime, err := time.Parse(time.RFC3339, startTimeStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Invalid start_time format. Use RFC3339 format."})
		return
	}

	endTime, err := time.Parse(time.RFC3339, endTimeStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Invalid end_time format. Use RFC3339 format."})
		return
	}

	rows, err := clickhouse.QueryContext(c.Request.Context(), `
		SELECT timestamp, node_id, track_list, track_count
		FROM track_list_events
		WHERE tenant_id = ? AND internal_name = ?
		AND timestamp BETWEEN ? AND ?
		ORDER BY timestamp DESC
	`, tenantID, internalName, startTime, endTime)
	if err != nil {
		logger.WithError(err).Error("Failed to fetch track list events from ClickHouse")
		c.JSON(http.StatusInternalServerError, periscope.ErrorResponse{Error: "Failed to fetch track list events"})
		return
	}
	defer rows.Close()

	var events []periscope.AnalyticsTrackListEvent
	for rows.Next() {
		var ts time.Time
		var nodeID, trackList string
		var trackCount int
		if err := rows.Scan(&ts, &nodeID, &trackList, &trackCount); err != nil {
			logger.WithError(err).Error("Failed to scan track list event")
			continue
		}
		events = append(events, periscope.AnalyticsTrackListEvent{
			Timestamp:  ts,
			NodeID:     nodeID,
			TrackList:  trackList,
			TrackCount: trackCount,
		})
	}

	response := periscope.TrackListEventsResponse(events)
	c.JSON(http.StatusOK, response)
}

// GetViewerStats returns viewer statistics for a specific stream
func GetViewerStats(c *gin.Context) {
	tenantID := c.GetString("tenant_id")
	internalName := c.Param("internal_name")

	if tenantID == "" {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Tenant context required"})
		return
	}

	// Get current state from YugaDB
	var currentViewers, peakViewers, totalConnections int
	err := yugaDB.QueryRowContext(c.Request.Context(), `
		SELECT current_viewers, peak_viewers, total_connections
		FROM stream_analytics
		WHERE tenant_id = $1 AND internal_name = $2
	`, tenantID, internalName).Scan(&currentViewers, &peakViewers, &totalConnections)

	if err == database.ErrNoRows {
		c.JSON(http.StatusNotFound, periscope.ErrorResponse{Error: "Stream analytics not found"})
		return
	}

	if err != nil {
		logger.WithError(err).Error("Failed to fetch viewer stats from YugaDB")
		c.JSON(http.StatusInternalServerError, periscope.ErrorResponse{Error: "Failed to fetch viewer stats"})
		return
	}

	// Get time-series data from ClickHouse
	rows, err := clickhouse.QueryContext(c.Request.Context(), `
		SELECT 
			timestamp,
			viewer_count,
			connection_type,
			buffer_health,
			connection_quality,
			country_code,
			city
		FROM viewer_metrics
		WHERE tenant_id = ? AND internal_name = ?
		AND timestamp >= NOW() - INTERVAL 24 HOUR
		ORDER BY timestamp DESC
	`, tenantID, internalName)

	if err != nil {
		logger.WithError(err).Error("Failed to fetch viewer history from ClickHouse")
		c.JSON(http.StatusInternalServerError, periscope.ErrorResponse{Error: "Failed to fetch viewer history"})
		return
	}
	defer rows.Close()

	var viewerHistory []map[string]interface{}
	for rows.Next() {
		var timestamp time.Time
		var viewerCount int
		var connectionType, countryCode, city string
		var bufferHealth, connQuality float32

		if err := rows.Scan(&timestamp, &viewerCount, &connectionType, &bufferHealth, &connQuality, &countryCode, &city); err != nil {
			continue
		}

		viewerHistory = append(viewerHistory, map[string]interface{}{
			"timestamp":          timestamp,
			"viewer_count":       viewerCount,
			"connection_type":    connectionType,
			"buffer_health":      bufferHealth,
			"connection_quality": connQuality,
			"country_code":       countryCode,
			"city":               city,
		})
	}

	// Get geographic distribution from ClickHouse
	var geoStats struct {
		UniqueCountries  int                       `json:"unique_countries"`
		UniqueCities     int                       `json:"unique_cities"`
		CountryBreakdown map[string]int            `json:"country_breakdown"`
		CityBreakdown    map[string]map[string]int `json:"city_breakdown"`
	}

	err = clickhouse.QueryRowContext(c.Request.Context(), `
		SELECT 
			uniq(country_code) as unique_countries,
			uniq(city) as unique_cities,
			groupArray((country_code, viewer_count)) as country_counts,
			groupArray((country_code, city, viewer_count)) as city_counts
		FROM viewer_metrics
		WHERE tenant_id = ? AND internal_name = ?
		AND timestamp >= NOW() - INTERVAL 1 HOUR
		GROUP BY tenant_id, internal_name
	`, tenantID, internalName).Scan(
		&geoStats.UniqueCountries,
		&geoStats.UniqueCities,
		&geoStats.CountryBreakdown,
		&geoStats.CityBreakdown,
	)

	if err != nil && err != database.ErrNoRows {
		logger.WithError(err).Error("Failed to fetch geographic stats from ClickHouse")
	}

	// Convert viewer history to typed format
	var typedViewerHistory []periscope.ViewerHistoryEntry
	for _, entry := range viewerHistory {
		typedViewerHistory = append(typedViewerHistory, periscope.ViewerHistoryEntry{
			Timestamp:         entry["timestamp"].(time.Time),
			ViewerCount:       entry["viewer_count"].(int),
			ConnectionType:    entry["connection_type"].(string),
			BufferHealth:      entry["buffer_health"].(float32),
			ConnectionQuality: entry["connection_quality"].(float32),
			CountryCode:       entry["country_code"].(string),
			City:              entry["city"].(string),
		})
	}

	// Create typed geo stats
	typedGeoStats := periscope.ViewerGeographicStats{
		UniqueCountries:  geoStats.UniqueCountries,
		UniqueCities:     geoStats.UniqueCities,
		CountryBreakdown: geoStats.CountryBreakdown,
		CityBreakdown:    geoStats.CityBreakdown,
	}

	response := periscope.ViewerStatsResponse{
		CurrentViewers:   currentViewers,
		PeakViewers:      peakViewers,
		TotalConnections: totalConnections,
		ViewerHistory:    typedViewerHistory,
		GeoStats:         typedGeoStats,
	}
	c.JSON(http.StatusOK, response)
}

// GetPlatformOverview returns high-level platform metrics
func GetPlatformOverview(c *gin.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Tenant context required"})
		return
	}

	logger.WithField("tenant_id", tenantID).Info("Getting platform overview")

	// Parse time range from query params (defaults to last 1 hour)
	startTime := c.DefaultQuery("start_time", time.Now().Add(-1*time.Hour).Format(time.RFC3339))
	endTime := c.DefaultQuery("end_time", time.Now().Format(time.RFC3339))

	// Parse time parameters
	startParsed, err := time.Parse(time.RFC3339, startTime)
	if err != nil {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Invalid start_time format"})
		return
	}
	endParsed, err := time.Parse(time.RFC3339, endTime)
	if err != nil {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Invalid end_time format"})
		return
	}

	// Get metrics from analytics data instead of directly querying users/streams tables
	var metrics struct {
		TotalUsers    int `json:"total_users"`
		ActiveUsers   int `json:"active_users"`
		TotalStreams  int `json:"total_streams"`
		ActiveStreams int `json:"active_streams"`
	}

	// Get user counts from ClickHouse analytics for the specified time range
	err = clickhouse.QueryRowContext(c.Request.Context(), `
		SELECT 
			uniq(user_id) as total_users,
			uniq(user_id) as active_users
		FROM connection_events 
		WHERE tenant_id = ? 
		AND timestamp BETWEEN ? AND ?
	`, tenantID, startParsed, endParsed).Scan(&metrics.TotalUsers, &metrics.ActiveUsers)

	if err != nil && err != database.ErrNoRows {
		logger.WithError(err).Warn("Failed to get user metrics from ClickHouse, using defaults")
		metrics.TotalUsers = 0
		metrics.ActiveUsers = 0
	}

	// Get stream counts from analytics data
	err = yugaDB.QueryRowContext(c.Request.Context(), `
		SELECT 
			COUNT(DISTINCT internal_name) as total_streams,
			COUNT(DISTINCT CASE WHEN status = 'live' THEN internal_name END) as active_streams
		FROM stream_analytics 
		WHERE tenant_id = $1 
		AND last_updated > NOW() - INTERVAL '24 hours'
	`, tenantID).Scan(&metrics.TotalStreams, &metrics.ActiveStreams)

	if err != nil {
		logger.WithError(err).Error("Failed to fetch stream metrics from analytics")
		c.JSON(http.StatusInternalServerError, periscope.ErrorResponse{Error: "Failed to fetch platform overview"})
		return
	}

	logger.WithFields(map[string]interface{}{
		"total_streams":  metrics.TotalStreams,
		"active_streams": metrics.ActiveStreams,
	}).Info("Stream metrics retrieved")

	// Get time-series data from ClickHouse
	var timeseriesMetrics struct {
		TotalViewers   int     `json:"total_viewers"`
		AverageViewers float64 `json:"average_viewers"`
		PeakBandwidth  float64 `json:"peak_bandwidth_mbps"`
	}

	err = clickhouse.QueryRowContext(c.Request.Context(), `
		SELECT 
			COALESCE(sum(viewer_count), 0) as total_viewers,
			COALESCE(avg(viewer_count), 0) as average_viewers,
			(SELECT COALESCE(max(bandwidth_out) / (1024*1024), 0) FROM stream_health_metrics WHERE tenant_id = ? AND timestamp BETWEEN ? AND ?) as peak_bandwidth_mbps
		FROM viewer_metrics 
		WHERE tenant_id = ? 
		AND timestamp BETWEEN ? AND ?
	`, tenantID, startParsed, endParsed, tenantID, startParsed, endParsed).Scan(
		&timeseriesMetrics.TotalViewers,
		&timeseriesMetrics.AverageViewers,
		&timeseriesMetrics.PeakBandwidth,
	)

	if err != nil && err != database.ErrNoRows {
		logger.WithError(err).Warn("Failed to get viewer metrics from ClickHouse")
		timeseriesMetrics.TotalViewers = 0
		timeseriesMetrics.AverageViewers = 0
		timeseriesMetrics.PeakBandwidth = 0
	}

	// Check for NaN values and replace with 0
	if math.IsNaN(timeseriesMetrics.AverageViewers) {
		logger.Warn("AverageViewers is NaN, setting to 0")
		timeseriesMetrics.AverageViewers = 0
	}
	if math.IsNaN(timeseriesMetrics.PeakBandwidth) {
		logger.Warn("PeakBandwidth is NaN, setting to 0")
		timeseriesMetrics.PeakBandwidth = 0
	}

	// Build structured response
	response := periscope.PlatformOverviewResponse{
		TenantID:       tenantID,
		TotalUsers:     metrics.TotalUsers,
		ActiveUsers:    metrics.ActiveUsers,
		TotalStreams:   metrics.TotalStreams,
		ActiveStreams:  metrics.ActiveStreams,
		TotalViewers:   timeseriesMetrics.TotalViewers,
		AverageViewers: timeseriesMetrics.AverageViewers,
		PeakBandwidth:  timeseriesMetrics.PeakBandwidth,
		GeneratedAt:    time.Now(),
	}

	// Debug marshaling to see what JSON is produced
	jsonBytes, err := json.Marshal(response)
	if err != nil {
		logger.WithError(err).Error("Failed to marshal platform overview response")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to marshal response"})
		return
	}

	c.Data(http.StatusOK, "application/json", jsonBytes)
}

// GetRealtimeStreams returns current live streams with analytics
func GetRealtimeStreams(c *gin.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Tenant context required"})
		return
	}

	// Get state data from YugaDB
	rows, err := yugaDB.QueryContext(c.Request.Context(), `
		SELECT sa.internal_name, sa.current_viewers, sa.bandwidth_in, 
		       sa.bandwidth_out, sa.status, sa.node_id, sa.location
		FROM stream_analytics sa
		WHERE sa.tenant_id = $1 AND sa.status = 'live' 
		AND sa.last_updated > NOW() - INTERVAL '5 minutes'
		ORDER BY sa.current_viewers DESC
	`, tenantID)

	if err != nil {
		logger.WithError(err).Error("Failed to fetch realtime streams from YugaDB")
		c.JSON(http.StatusInternalServerError, periscope.ErrorResponse{Error: "Failed to fetch realtime streams"})
		return
	}
	defer rows.Close()

	var streams []periscope.RealtimeStream
	for rows.Next() {
		var internalName, status, nodeID, location string
		var currentViewers int
		var bandwidthIn, bandwidthOut int64

		if err := rows.Scan(&internalName, &currentViewers, &bandwidthIn,
			&bandwidthOut, &status, &nodeID, &location); err != nil {
			logger.WithError(err).Error("Error scanning realtime stream")
			continue
		}

		// Get time-series data from ClickHouse
		var metrics struct {
			ViewerTrend       float64 `json:"viewer_trend"`
			BufferHealth      float32 `json:"buffer_health"`
			ConnectionQuality float32 `json:"connection_quality"`
			UniqueCountries   int     `json:"unique_countries"`
		}

		err = clickhouse.QueryRowContext(c.Request.Context(), `
			SELECT 
				(last_value(viewer_count) - first_value(viewer_count)) / 300 as viewer_trend,
				avg(buffer_health) as buffer_health,
				avg(connection_quality) as connection_quality,
				uniq(country_code) as unique_countries
			FROM viewer_metrics
			WHERE tenant_id = ? AND internal_name = ?
			AND timestamp >= NOW() - INTERVAL 5 MINUTE
		`, tenantID, internalName).Scan(
			&metrics.ViewerTrend,
			&metrics.BufferHealth,
			&metrics.ConnectionQuality,
			&metrics.UniqueCountries,
		)

		if err != nil && err != database.ErrNoRows {
			logger.WithError(err).Error("Failed to fetch stream metrics from ClickHouse")
		}

		streams = append(streams, periscope.RealtimeStream{
			InternalName:      internalName,
			CurrentViewers:    currentViewers,
			BandwidthIn:       bandwidthIn,
			BandwidthOut:      bandwidthOut,
			Status:            status,
			NodeID:            nodeID,
			Location:          location,
			ViewerTrend:       metrics.ViewerTrend,
			BufferHealth:      metrics.BufferHealth,
			ConnectionQuality: metrics.ConnectionQuality,
			UniqueCountries:   metrics.UniqueCountries,
		})
	}

	response := periscope.RealtimeStreamsResponse{
		Streams: streams,
		Count:   len(streams),
	}
	c.JSON(http.StatusOK, response)
}

// GetRealtimeViewers returns current viewer counts across all streams
func GetRealtimeViewers(c *gin.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Tenant context required"})
		return
	}

	// Get total viewers from ClickHouse (more accurate than YugaDB for real-time)
	var totalViewers int
	err := clickhouse.QueryRowContext(c.Request.Context(), `
		SELECT sum(viewer_count)
		FROM viewer_metrics
		WHERE tenant_id = ?
		AND timestamp >= NOW() - INTERVAL 5 MINUTE
	`, tenantID).Scan(&totalViewers)

	if err != nil && err != database.ErrNoRows {
		logger.WithError(err).Error("Failed to fetch total viewers from ClickHouse")
		c.JSON(http.StatusInternalServerError, periscope.ErrorResponse{Error: "Failed to fetch viewer count"})
		return
	}

	// Get per-stream breakdown from ClickHouse
	rows, err := clickhouse.QueryContext(c.Request.Context(), `
		SELECT 
			internal_name,
			avg(viewer_count) as avg_viewers,
			max(viewer_count) as peak_viewers,
			uniq(country_code) as unique_countries,
			uniq(city) as unique_cities
		FROM viewer_metrics
		WHERE tenant_id = ?
		AND timestamp >= NOW() - INTERVAL 5 MINUTE
		GROUP BY internal_name
		ORDER BY avg_viewers DESC
	`, tenantID)

	if err != nil {
		logger.WithError(err).Error("Failed to fetch stream viewers from ClickHouse")
		c.JSON(http.StatusInternalServerError, periscope.ErrorResponse{Error: "Failed to fetch stream viewers"})
		return
	}
	defer rows.Close()

	var streamViewers []periscope.RealtimeStreamViewer
	for rows.Next() {
		var internalName string
		var avgViewers, peakViewers float64
		var uniqueCountries, uniqueCities int

		if err := rows.Scan(&internalName, &avgViewers, &peakViewers, &uniqueCountries, &uniqueCities); err != nil {
			continue
		}

		streamViewers = append(streamViewers, periscope.RealtimeStreamViewer{
			InternalName:    internalName,
			AvgViewers:      avgViewers,
			PeakViewers:     peakViewers,
			UniqueCountries: uniqueCountries,
			UniqueCities:    uniqueCities,
		})
	}

	response := periscope.RealtimeViewersResponse{
		TotalViewers:  totalViewers,
		StreamViewers: streamViewers,
	}
	c.JSON(http.StatusOK, response)
}

// GetRealtimeEvents returns recent events across all streams
func GetRealtimeEvents(c *gin.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Tenant context required"})
		return
	}

	var events []interface{}

	// Fetch recent stream_events from ClickHouse
	rows, err := clickhouse.QueryContext(c.Request.Context(), `
		SELECT timestamp, internal_name, event_type, status, node_id, event_data
		FROM stream_events
		WHERE tenant_id = ? AND timestamp >= NOW() - INTERVAL 1 HOUR
		ORDER BY timestamp DESC
		LIMIT 200
	`, tenantID)
	if err != nil && err != database.ErrNoRows {
		logger.WithError(err).Error("Failed to fetch stream events from ClickHouse")
	} else if err == nil {
		defer rows.Close()
		for rows.Next() {
			var ts time.Time
			var internalName, eventType, status, nodeID, eventData string
			if err := rows.Scan(&ts, &internalName, &eventType, &status, &nodeID, &eventData); err != nil {
				continue
			}
			events = append(events, map[string]interface{}{
				"timestamp":     ts,
				"internal_name": internalName,
				"event_type":    eventType,
				"status":        status,
				"node_id":       nodeID,
				"event_data":    eventData,
			})
		}
	}

	// Fetch recent viewer metrics as secondary realtime signals
	rows, err = clickhouse.QueryContext(c.Request.Context(), `
		SELECT 
			timestamp,
			internal_name,
			viewer_count,
			connection_type,
			buffer_health,
			connection_quality,
			country_code,
			city
		FROM viewer_metrics
		WHERE tenant_id = ?
		AND timestamp >= NOW() - INTERVAL 1 HOUR
		ORDER BY timestamp DESC
		LIMIT 50
	`, tenantID)

	if err != nil && err != database.ErrNoRows {
		logger.WithError(err).Error("Failed to fetch viewer events from ClickHouse")
	} else if err == nil {
		defer rows.Close()
		for rows.Next() {
			var e struct {
				Timestamp      time.Time `json:"timestamp"`
				InternalName   string    `json:"internal_name"`
				ViewerCount    int       `json:"viewer_count"`
				ConnectionType string    `json:"connection_type"`
				BufferHealth   float32   `json:"buffer_health"`
				ConnQuality    float32   `json:"connection_quality"`
				CountryCode    string    `json:"country_code"`
				City           string    `json:"city"`
			}
			if err := rows.Scan(
				&e.Timestamp,
				&e.InternalName,
				&e.ViewerCount,
				&e.ConnectionType,
				&e.BufferHealth,
				&e.ConnQuality,
				&e.CountryCode,
				&e.City,
			); err != nil {
				continue
			}
			events = append(events, map[string]interface{}{
				"timestamp":          e.Timestamp,
				"internal_name":      e.InternalName,
				"viewer_count":       e.ViewerCount,
				"connection_type":    e.ConnectionType,
				"buffer_health":      e.BufferHealth,
				"connection_quality": e.ConnQuality,
				"country_code":       e.CountryCode,
				"city":               e.City,
			})
		}
	}

	response := periscope.RealtimeEventsResponse{
		Events: events,
		Count:  len(events),
	}
	c.JSON(http.StatusOK, response)
}

// GetConnectionEvents returns connection events from ClickHouse
func GetConnectionEvents(c *gin.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Tenant context required"})
		return
	}

	// Parse time range from query params
	startTimeStr := c.DefaultQuery("start_time", time.Now().Add(-24*time.Hour).Format(time.RFC3339))
	endTimeStr := c.DefaultQuery("end_time", time.Now().Format(time.RFC3339))

	// Parse time strings into time.Time objects for ClickHouse
	startTime, err := time.Parse(time.RFC3339, startTimeStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Invalid start_time format. Use RFC3339 format."})
		return
	}

	endTime, err := time.Parse(time.RFC3339, endTimeStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Invalid end_time format. Use RFC3339 format."})
		return
	}

	// Query ClickHouse for connection events
	rows, err := clickhouse.QueryContext(c.Request.Context(), `
		SELECT 
			event_id,
			timestamp,
			tenant_id,
			internal_name,
			user_id,
			session_id,
			connection_addr,
			user_agent,
			connector,
			node_id,
			country_code,
			city,
			latitude,
			longitude,
			event_type,
			session_duration,
			bytes_transferred
		FROM connection_events
		WHERE tenant_id = ?
		AND timestamp BETWEEN ? AND ?
		ORDER BY timestamp DESC
	`, tenantID, startTime, endTime)

	if err != nil {
		logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"error":     err,
		}).Error("Failed to fetch connection events from ClickHouse")
		c.JSON(http.StatusInternalServerError, periscope.ErrorResponse{Error: "Failed to fetch connection events"})
		return
	}
	defer rows.Close()

	var events []periscope.ConnectionEvent
	for rows.Next() {
		var e struct {
			EventID          string    `json:"event_id"`
			Timestamp        time.Time `json:"timestamp"`
			TenantID         string    `json:"tenant_id"`
			InternalName     string    `json:"internal_name"`
			UserID           string    `json:"user_id"`
			SessionID        string    `json:"session_id"`
			ConnectionAddr   string    `json:"connection_addr"`
			UserAgent        string    `json:"user_agent"`
			Connector        string    `json:"connector"`
			NodeID           string    `json:"node_id"`
			CountryCode      string    `json:"country_code"`
			City             string    `json:"city"`
			Latitude         float64   `json:"latitude"`
			Longitude        float64   `json:"longitude"`
			EventType        string    `json:"event_type"`
			SessionDuration  int       `json:"session_duration"`
			BytesTransferred int64     `json:"bytes_transferred"`
		}

		if err := rows.Scan(
			&e.EventID,
			&e.Timestamp,
			&e.TenantID,
			&e.InternalName,
			&e.UserID,
			&e.SessionID,
			&e.ConnectionAddr,
			&e.UserAgent,
			&e.Connector,
			&e.NodeID,
			&e.CountryCode,
			&e.City,
			&e.Latitude,
			&e.Longitude,
			&e.EventType,
			&e.SessionDuration,
			&e.BytesTransferred,
		); err != nil {
			logger.WithError(err).Error("Failed to scan connection event")
			continue
		}

		events = append(events, periscope.ConnectionEvent{
			EventID:          e.EventID,
			Timestamp:        e.Timestamp,
			TenantID:         e.TenantID,
			InternalName:     e.InternalName,
			UserID:           e.UserID,
			SessionID:        e.SessionID,
			ConnectionAddr:   e.ConnectionAddr,
			UserAgent:        e.UserAgent,
			Connector:        e.Connector,
			NodeID:           e.NodeID,
			CountryCode:      e.CountryCode,
			City:             e.City,
			Latitude:         e.Latitude,
			Longitude:        e.Longitude,
			EventType:        e.EventType,
			SessionDuration:  e.SessionDuration,
			BytesTransferred: e.BytesTransferred,
		})
	}

	response := periscope.ConnectionEventsResponse(events)
	c.JSON(http.StatusOK, response)
}

// GetNodeMetrics returns node metrics from ClickHouse
func GetNodeMetrics(c *gin.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Tenant context required"})
		return
	}

	// Parse time range from query params
	startTimeStr := c.DefaultQuery("start_time", time.Now().Add(-24*time.Hour).Format(time.RFC3339))
	endTimeStr := c.DefaultQuery("end_time", time.Now().Format(time.RFC3339))

	// Parse time strings into time.Time objects for ClickHouse
	startTime, err := time.Parse(time.RFC3339, startTimeStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Invalid start_time format. Use RFC3339 format."})
		return
	}

	endTime, err := time.Parse(time.RFC3339, endTimeStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Invalid end_time format. Use RFC3339 format."})
		return
	}

	// Query ClickHouse for node metrics
	rows, err := clickhouse.QueryContext(c.Request.Context(), `
		SELECT 
			timestamp,
			node_id,
			cpu_usage,
			memory_usage,
			disk_usage,
			ram_max,
			ram_current,
			bandwidth_in,
			bandwidth_out,
			up_speed,
			down_speed,
			connections_current,
			stream_count,
			health_score,
			is_healthy,
			latitude,
			longitude,
			tags,
			metadata
		FROM node_metrics
		WHERE tenant_id = ?
		AND timestamp BETWEEN ? AND ?
		ORDER BY timestamp DESC
	`, tenantID, startTime, endTime)

	if err != nil {
		logger.WithError(err).WithFields(logging.Fields{
			"tenant_id":  tenantID,
			"start_time": startTime,
			"end_time":   endTime,
		}).Error("Failed to fetch node metrics from ClickHouse")
		c.JSON(http.StatusInternalServerError, periscope.ErrorResponse{Error: "Failed to fetch node metrics"})
		return
	}
	defer rows.Close()

	var metrics []map[string]interface{}
	for rows.Next() {
		var m struct {
			Timestamp          time.Time `json:"timestamp"`
			NodeID             string    `json:"node_id"`
			CPUUsage           float32   `json:"cpu_usage"`
			MemoryUsage        float32   `json:"memory_usage"`
			DiskUsage          float32   `json:"disk_usage"`
			RAMMax             uint64    `json:"ram_max"`
			RAMCurrent         uint64    `json:"ram_current"`
			BandwidthIn        int64     `json:"bandwidth_in"`
			BandwidthOut       int64     `json:"bandwidth_out"`
			UpSpeed            uint64    `json:"up_speed"`
			DownSpeed          uint64    `json:"down_speed"`
			ConnectionsCurrent int       `json:"connections_current"`
			StreamCount        int       `json:"stream_count"`
			HealthScore        float32   `json:"health_score"`
			IsHealthy          bool      `json:"is_healthy"`
			Latitude           float64   `json:"latitude"`
			Longitude          float64   `json:"longitude"`
			Tags               []string  `json:"tags"`
			Metadata           string    `json:"metadata"`
		}

		if err := rows.Scan(
			&m.Timestamp,
			&m.NodeID,
			&m.CPUUsage,
			&m.MemoryUsage,
			&m.DiskUsage,
			&m.RAMMax,
			&m.RAMCurrent,
			&m.BandwidthIn,
			&m.BandwidthOut,
			&m.UpSpeed,
			&m.DownSpeed,
			&m.ConnectionsCurrent,
			&m.StreamCount,
			&m.HealthScore,
			&m.IsHealthy,
			&m.Latitude,
			&m.Longitude,
			&m.Tags,
			&m.Metadata,
		); err != nil {
			logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to scan node metrics")
			continue
		}

		metrics = append(metrics, map[string]interface{}{
			"timestamp":           m.Timestamp,
			"node_id":             m.NodeID,
			"cpu_usage":           m.CPUUsage,
			"memory_usage":        m.MemoryUsage,
			"disk_usage":          m.DiskUsage,
			"ram_max":             m.RAMMax,
			"ram_current":         m.RAMCurrent,
			"bandwidth_in":        m.BandwidthIn,
			"bandwidth_out":       m.BandwidthOut,
			"up_speed":            m.UpSpeed,
			"down_speed":          m.DownSpeed,
			"connections_current": m.ConnectionsCurrent,
			"stream_count":        m.StreamCount,
			"health_score":        m.HealthScore,
			"is_healthy":          m.IsHealthy,
			"latitude":            m.Latitude,
			"longitude":           m.Longitude,
			"tags":                m.Tags,
			"metadata":            m.Metadata,
		})
	}

	// Convert to typed response
	var typedMetrics []periscope.NodeMetric
	for _, m := range metrics {
		typedMetrics = append(typedMetrics, periscope.NodeMetric{
			Timestamp:          m["timestamp"].(time.Time),
			NodeID:             m["node_id"].(string),
			CPUUsage:           m["cpu_usage"].(float32),
			MemoryUsage:        m["memory_usage"].(float32),
			DiskUsage:          m["disk_usage"].(float32),
			RAMMax:             m["ram_max"].(uint64),
			RAMCurrent:         m["ram_current"].(uint64),
			BandwidthIn:        m["bandwidth_in"].(int64),
			BandwidthOut:       m["bandwidth_out"].(int64),
			UpSpeed:            m["up_speed"].(uint64),
			DownSpeed:          m["down_speed"].(uint64),
			ConnectionsCurrent: m["connections_current"].(int),
			StreamCount:        m["stream_count"].(int),
			HealthScore:        m["health_score"].(float32),
			IsHealthy:          m["is_healthy"].(bool),
			Latitude:           m["latitude"].(float64),
			Longitude:          m["longitude"].(float64),
			Tags:               m["tags"].([]string),
			Metadata:           m["metadata"].(string),
		})
	}

	response := periscope.NodeMetricsResponse{
		Metrics:   typedMetrics,
		Count:     len(typedMetrics),
		StartTime: startTime.Format(time.RFC3339),
		EndTime:   endTime.Format(time.RFC3339),
	}
	c.JSON(http.StatusOK, response)
}

// GetNodeMetrics1h returns hourly aggregated node metrics from ClickHouse materialized view
func GetNodeMetrics1h(c *gin.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Tenant context required"})
		return
	}

	// Parse time range from query params
	startTimeStr := c.DefaultQuery("start_time", time.Now().Add(-24*time.Hour).Format(time.RFC3339))
	endTimeStr := c.DefaultQuery("end_time", time.Now().Format(time.RFC3339))

	// Parse time strings into time.Time objects for ClickHouse
	startTime, err := time.Parse(time.RFC3339, startTimeStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Invalid start_time format. Use RFC3339 format."})
		return
	}

	endTime, err := time.Parse(time.RFC3339, endTimeStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Invalid end_time format. Use RFC3339 format."})
		return
	}

	// Query ClickHouse materialized view
	rows, err := clickhouse.QueryContext(c.Request.Context(), `
		SELECT 
			timestamp_1h,
			node_id,
			avg_cpu,
			peak_cpu,
			avg_memory,
			peak_memory,
			total_bandwidth_in,
			total_bandwidth_out,
			avg_health_score,
			was_healthy
		FROM node_metrics_1h
		WHERE timestamp_1h BETWEEN ? AND ?
		ORDER BY timestamp_1h DESC
	`, startTime, endTime)

	if err != nil {
		logger.WithError(err).Error("Failed to fetch hourly node metrics from ClickHouse")
		c.JSON(http.StatusInternalServerError, periscope.ErrorResponse{Error: "Failed to fetch hourly metrics"})
		return
	}
	defer rows.Close()

	var metrics []periscope.NodeMetricHourly
	for rows.Next() {
		var m struct {
			Timestamp         time.Time `json:"timestamp"`
			NodeID            string    `json:"node_id"`
			AvgCPU            float32   `json:"avg_cpu"`
			PeakCPU           float32   `json:"peak_cpu"`
			AvgMemory         float32   `json:"avg_memory"`
			PeakMemory        float32   `json:"peak_memory"`
			TotalBandwidthIn  int64     `json:"total_bandwidth_in"`
			TotalBandwidthOut int64     `json:"total_bandwidth_out"`
			AvgHealthScore    float32   `json:"avg_health_score"`
			WasHealthy        bool      `json:"was_healthy"`
		}

		if err := rows.Scan(
			&m.Timestamp,
			&m.NodeID,
			&m.AvgCPU,
			&m.PeakCPU,
			&m.AvgMemory,
			&m.PeakMemory,
			&m.TotalBandwidthIn,
			&m.TotalBandwidthOut,
			&m.AvgHealthScore,
			&m.WasHealthy,
		); err != nil {
			logger.WithError(err).Error("Failed to scan hourly node metrics")
			continue
		}

		metrics = append(metrics, periscope.NodeMetricHourly{
			Timestamp:         m.Timestamp,
			NodeID:            m.NodeID,
			AvgCPU:            m.AvgCPU,
			PeakCPU:           m.PeakCPU,
			AvgMemory:         m.AvgMemory,
			PeakMemory:        m.PeakMemory,
			TotalBandwidthIn:  m.TotalBandwidthIn,
			TotalBandwidthOut: m.TotalBandwidthOut,
			AvgHealthScore:    m.AvgHealthScore,
			WasHealthy:        m.WasHealthy,
		})
	}

	response := periscope.NodeMetrics1hResponse(metrics)
	c.JSON(http.StatusOK, response)
}

// GetStreamHealthMetrics returns stream health metrics from ClickHouse
func GetStreamHealthMetrics(c *gin.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Tenant context required"})
		return
	}

	// Parse time range from query params
	startTimeStr := c.DefaultQuery("start_time", time.Now().Add(-24*time.Hour).Format(time.RFC3339))
	endTimeStr := c.DefaultQuery("end_time", time.Now().Format(time.RFC3339))
	streamID := c.Query("stream_id")

	// Parse time strings into time.Time objects for ClickHouse
	startTime, err := time.Parse(time.RFC3339, startTimeStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Invalid start_time format. Use RFC3339 format."})
		return
	}

	endTime, err := time.Parse(time.RFC3339, endTimeStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Invalid end_time format. Use RFC3339 format."})
		return
	}

	// Build query with optional stream filtering
	query := `
		SELECT 
			timestamp,
			tenant_id,
			internal_name,
			node_id,
			bitrate,
			fps,
			gop_size,
			width,
			height,
			buffer_size,
			buffer_used,
			buffer_health,
			packets_sent,
			packets_lost,
			packets_retransmitted,
			bandwidth_in,
			bandwidth_out,
			codec,
			profile,
			track_metadata
		FROM stream_health_metrics
		WHERE tenant_id = ?
		AND timestamp BETWEEN ? AND ?`

	args := []interface{}{tenantID, startTime, endTime}

	// Only filter by stream if streamID is provided and not empty
	if streamID != "" && streamID != "null" && streamID != "undefined" {
		query += " AND internal_name = ?"
		args = append(args, streamID)
	}

	query += " ORDER BY timestamp DESC"

	// Query ClickHouse for stream health metrics
	rows, err := clickhouse.QueryContext(c.Request.Context(), query, args...)

	if err != nil {
		logger.WithFields(logging.Fields{
			"tenant_id":  tenantID,
			"stream_id":  streamID,
			"start_time": startTime,
			"end_time":   endTime,
			"query":      query,
			"error":      err,
		}).Error("Failed to fetch stream health metrics from ClickHouse")
		c.JSON(http.StatusInternalServerError, periscope.ErrorResponse{Error: "Failed to fetch stream health metrics"})
		return
	}
	defer rows.Close()

	var metrics []periscope.StreamHealthMetric
	for rows.Next() {
		var m struct {
			Timestamp            time.Time `json:"timestamp"`
			TenantID             string    `json:"tenant_id"`
			InternalName         string    `json:"internal_name"`
			NodeID               string    `json:"node_id"`
			Bitrate              int       `json:"bitrate"`
			FPS                  float32   `json:"fps"`
			GOPSize              int       `json:"gop_size"`
			Width                int       `json:"width"`
			Height               int       `json:"height"`
			BufferSize           int       `json:"buffer_size"`
			BufferUsed           int       `json:"buffer_used"`
			BufferHealth         float32   `json:"buffer_health"`
			PacketsSent          int64     `json:"packets_sent"`
			PacketsLost          int64     `json:"packets_lost"`
			PacketsRetransmitted int64     `json:"packets_retransmitted"`
			BandwidthIn          int64     `json:"bandwidth_in"`
			BandwidthOut         int64     `json:"bandwidth_out"`
			Codec                string    `json:"codec"`
			Profile              string    `json:"profile"`
			TrackMetadata        string    `json:"track_metadata"`
		}

		if err := rows.Scan(
			&m.Timestamp,
			&m.TenantID,
			&m.InternalName,
			&m.NodeID,
			&m.Bitrate,
			&m.FPS,
			&m.GOPSize,
			&m.Width,
			&m.Height,
			&m.BufferSize,
			&m.BufferUsed,
			&m.BufferHealth,
			&m.PacketsSent,
			&m.PacketsLost,
			&m.PacketsRetransmitted,
			&m.BandwidthIn,
			&m.BandwidthOut,
			&m.Codec,
			&m.Profile,
			&m.TrackMetadata,
		); err != nil {
			logger.WithError(err).Error("Failed to scan stream health metrics")
			continue
		}

		metrics = append(metrics, periscope.StreamHealthMetric{
			Timestamp:            m.Timestamp,
			TenantID:             m.TenantID,
			InternalName:         m.InternalName,
			NodeID:               m.NodeID,
			Bitrate:              m.Bitrate,
			FPS:                  m.FPS,
			GOPSize:              m.GOPSize,
			Width:                m.Width,
			Height:               m.Height,
			BufferSize:           m.BufferSize,
			BufferUsed:           m.BufferUsed,
			BufferHealth:         m.BufferHealth,
			PacketsSent:          m.PacketsSent,
			PacketsLost:          m.PacketsLost,
			PacketsRetransmitted: m.PacketsRetransmitted,
			BandwidthIn:          m.BandwidthIn,
			BandwidthOut:         m.BandwidthOut,
			Codec:                m.Codec,
			Profile:              m.Profile,
			TrackMetadata:        m.TrackMetadata,
		})
	}

	response := periscope.StreamHealthMetricsResponse(metrics)
	c.JSON(http.StatusOK, response)
}

// GetStreamBufferEvents returns buffer events for a specific stream
func GetStreamBufferEvents(c *gin.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Tenant context required"})
		return
	}
	internalName := c.Param("internal_name")
	startTimeStr := c.DefaultQuery("start_time", time.Now().Add(-24*time.Hour).Format(time.RFC3339))
	endTimeStr := c.DefaultQuery("end_time", time.Now().Format(time.RFC3339))

	// Parse time strings into time.Time objects for ClickHouse
	startTime, err := time.Parse(time.RFC3339, startTimeStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Invalid start_time format. Use RFC3339 format."})
		return
	}

	endTime, err := time.Parse(time.RFC3339, endTimeStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Invalid end_time format. Use RFC3339 format."})
		return
	}

	rows, err := clickhouse.QueryContext(c.Request.Context(), `
		SELECT timestamp, event_id, status, node_id, event_data
		FROM stream_events
		WHERE tenant_id = ? AND internal_name = ? AND event_type = 'stream-buffer'
		AND timestamp BETWEEN ? AND ?
		ORDER BY timestamp DESC
	`, tenantID, internalName, startTime, endTime)
	if err != nil {
		logger.WithError(err).Error("Failed to fetch buffer events")
		c.JSON(http.StatusInternalServerError, periscope.ErrorResponse{Error: "Failed to fetch buffer events"})
		return
	}
	defer rows.Close()

	var events []periscope.BufferEvent
	for rows.Next() {
		var ts time.Time
		var eventID, status, nodeID, eventData string
		if err := rows.Scan(&ts, &eventID, &status, &nodeID, &eventData); err != nil {
			continue
		}
		events = append(events, periscope.BufferEvent{
			Timestamp: ts,
			EventID:   eventID,
			Status:    status,
			NodeID:    nodeID,
			EventData: eventData,
		})
	}
	response := periscope.BufferEventsResponse(events)
	c.JSON(http.StatusOK, response)
}

// GetStreamEndEvents returns end events for a specific stream
func GetStreamEndEvents(c *gin.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Tenant context required"})
		return
	}
	internalName := c.Param("internal_name")
	startTimeStr := c.DefaultQuery("start_time", time.Now().Add(-24*time.Hour).Format(time.RFC3339))
	endTimeStr := c.DefaultQuery("end_time", time.Now().Format(time.RFC3339))

	// Parse time strings into time.Time objects for ClickHouse
	startTime, err := time.Parse(time.RFC3339, startTimeStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Invalid start_time format. Use RFC3339 format."})
		return
	}

	endTime, err := time.Parse(time.RFC3339, endTimeStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, periscope.ErrorResponse{Error: "Invalid end_time format. Use RFC3339 format."})
		return
	}

	rows, err := clickhouse.QueryContext(c.Request.Context(), `
		SELECT timestamp, event_id, status, node_id, event_data
		FROM stream_events
		WHERE tenant_id = ? AND internal_name = ? AND event_type = 'stream-end'
		AND timestamp BETWEEN ? AND ?
		ORDER BY timestamp DESC
	`, tenantID, internalName, startTime, endTime)
	if err != nil {
		logger.WithError(err).Error("Failed to fetch end events")
		c.JSON(http.StatusInternalServerError, periscope.ErrorResponse{Error: "Failed to fetch end events"})
		return
	}
	defer rows.Close()

	var events []periscope.EndEvent
	for rows.Next() {
		var ts time.Time
		var eventID, status, nodeID, eventData string
		if err := rows.Scan(&ts, &eventID, &status, &nodeID, &eventData); err != nil {
			continue
		}
		events = append(events, periscope.EndEvent{
			Timestamp: ts,
			EventID:   eventID,
			Status:    status,
			NodeID:    nodeID,
			EventData: eventData,
		})
	}
	response := periscope.EndEventsResponse(events)
	c.JSON(http.StatusOK, response)
}
