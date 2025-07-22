package handlers

import (
	"net/http"
	"time"

	"frameworks/pkg/database"
	"frameworks/pkg/logging"
	"frameworks/pkg/middleware"
	"frameworks/pkg/models"
)

var (
	yugaDB     database.PostgresConn
	clickhouse database.ClickHouseConn
	logger     logging.Logger
)

// Init initializes the handlers package with database connections
func Init(ydb database.PostgresConn, ch database.ClickHouseConn, log logging.Logger) {
	yugaDB = ydb
	clickhouse = ch
	logger = log
}

// GetStreamAnalytics returns analytics for all streams with recent activity (tenant-scoped)
func GetStreamAnalytics(c middleware.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, middleware.H{"error": "Tenant context required"})
		return
	}

	// Query YugaDB for state data
	rows, err := yugaDB.QueryContext(c.Request.Context(), `
		SELECT sa.id, sa.tenant_id, sa.internal_name, sa.internal_name, 
		       sa.session_start_time, sa.session_end_time, sa.total_session_duration,
		       sa.current_viewers, sa.peak_viewers, sa.total_connections, 
		       sa.bandwidth_in, sa.bandwidth_out, sa.total_bandwidth_gb,
		       sa.bitrate_kbps, sa.resolution, sa.packets_sent, sa.packets_lost,
		       sa.packets_retrans, sa.upbytes, sa.downbytes, sa.first_ms, sa.last_ms,
		       sa.track_count, sa.inputs, sa.outputs, sa.node_id, sa.node_name, sa.latitude,
		       sa.longitude, sa.location, sa.status, sa.last_updated, sa.created_at
		FROM stream_analytics sa
		WHERE sa.tenant_id = $1 AND sa.last_updated > NOW() - INTERVAL '24 hours'
		ORDER BY sa.last_updated DESC
	`, tenantID)

	if err != nil {
		logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"error":     err,
		}).Error("Failed to fetch stream analytics from PostgreSQL")
		c.JSON(http.StatusInternalServerError, middleware.H{"error": "Failed to fetch analytics"})
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
			WHERE tenant_id = $1 AND internal_name = $2
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
			WHERE tenant_id = $1 AND internal_name = $2
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

	c.JSON(http.StatusOK, analytics)
}

// GetViewerMetrics returns viewer metrics from ClickHouse
func GetViewerMetrics(c middleware.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, middleware.H{"error": "Tenant context required"})
		return
	}

	// Parse time range from query params
	startTime := c.DefaultQuery("start_time", time.Now().Add(-24*time.Hour).Format(time.RFC3339))
	endTime := c.DefaultQuery("end_time", time.Now().Format(time.RFC3339))

	// Query ClickHouse for viewer metrics
	rows, err := clickhouse.QueryContext(c.Request.Context(), `
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
		WHERE tenant_id = $1
		AND timestamp BETWEEN $2 AND $3
		ORDER BY timestamp DESC
	`, tenantID, startTime, endTime)

	if err != nil {
		logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"error":     err,
		}).Error("Failed to fetch viewer metrics from ClickHouse")
		c.JSON(http.StatusInternalServerError, middleware.H{"error": "Failed to fetch viewer metrics"})
		return
	}
	defer rows.Close()

	var metrics []models.AnalyticsViewerMetric
	for rows.Next() {
		var m models.AnalyticsViewerMetric
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

	c.JSON(http.StatusOK, metrics)
}

// GetRoutingEvents returns routing events from ClickHouse
func GetRoutingEvents(c middleware.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, middleware.H{"error": "Tenant context required"})
		return
	}

	// Parse time range from query params
	startTime := c.DefaultQuery("start_time", time.Now().Add(-24*time.Hour).Format(time.RFC3339))
	endTime := c.DefaultQuery("end_time", time.Now().Format(time.RFC3339))

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
		WHERE tenant_id = $1
		AND timestamp BETWEEN $2 AND $3
		ORDER BY timestamp DESC
	`, tenantID, startTime, endTime)

	if err != nil {
		logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"error":     err,
		}).Error("Failed to fetch routing events from ClickHouse")
		c.JSON(http.StatusInternalServerError, middleware.H{"error": "Failed to fetch routing events"})
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

	c.JSON(http.StatusOK, events)
}

// GetViewerMetrics5m returns aggregated viewer metrics from ClickHouse materialized view
func GetViewerMetrics5m(c middleware.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, middleware.H{"error": "Tenant context required"})
		return
	}

	// Parse time range from query params
	startTime := c.DefaultQuery("start_time", time.Now().Add(-24*time.Hour).Format(time.RFC3339))
	endTime := c.DefaultQuery("end_time", time.Now().Format(time.RFC3339))

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
		WHERE tenant_id = $1
		AND timestamp_5m BETWEEN $2 AND $3
		ORDER BY timestamp_5m DESC
	`, tenantID, startTime, endTime)

	if err != nil {
		logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"error":     err,
		}).Error("Failed to fetch aggregated viewer metrics from ClickHouse")
		c.JSON(http.StatusInternalServerError, middleware.H{"error": "Failed to fetch aggregated metrics"})
		return
	}
	defer rows.Close()

	var metrics []models.AnalyticsViewerMetrics5m
	for rows.Next() {
		var m models.AnalyticsViewerMetrics5m
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

	c.JSON(http.StatusOK, metrics)
}

// GetStreamDetails returns detailed analytics for a specific stream
func GetStreamDetails(c middleware.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, middleware.H{"error": "Tenant context required"})
		return
	}

	internalName := c.Param("internal_name")

	// Get state data from YugaDB
	var sa models.StreamAnalytics
	var discard string
	err := yugaDB.QueryRowContext(c.Request.Context(), `
		SELECT sa.id, sa.tenant_id, sa.internal_name, sa.internal_name, 
		       sa.session_start_time, sa.session_end_time, sa.total_session_duration,
		       sa.current_viewers, sa.peak_viewers, sa.total_connections, 
		       sa.bandwidth_in, sa.bandwidth_out, sa.total_bandwidth_gb,
		       sa.bitrate_kbps, sa.resolution, sa.packets_sent, sa.packets_lost,
		       sa.packets_retrans, sa.upbytes, sa.downbytes, sa.first_ms, sa.last_ms,
		       sa.track_count, sa.inputs, sa.outputs, sa.node_id, sa.node_name, sa.latitude,
		       sa.longitude, sa.location, sa.status, sa.last_updated, sa.created_at
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
		c.JSON(http.StatusNotFound, middleware.H{"error": "Stream analytics not found"})
		return
	}

	if err != nil {
		logger.WithError(err).Error("Failed to fetch stream details from YugaDB")
		c.JSON(http.StatusInternalServerError, middleware.H{"error": "Failed to fetch stream details"})
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
		WHERE tenant_id = $1 AND internal_name = $2
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

	c.JSON(http.StatusOK, sa)
}

// GetStreamEvents returns events for a specific stream
func GetStreamEvents(c middleware.Context) {
	tenantID := c.GetString("tenant_id")
	internalName := c.Param("internal_name")

	if tenantID == "" {
		c.JSON(http.StatusBadRequest, middleware.H{"error": "Tenant context required"})
		return
	}

	// Get events from ClickHouse
	rows, err := clickhouse.QueryContext(c.Request.Context(), `
		SELECT timestamp, event_id, event_type, status, node_id, event_data
		FROM stream_events 
		WHERE tenant_id = $1 AND internal_name = $2
		ORDER BY timestamp DESC 
		LIMIT 100
	`, tenantID, internalName)

	if err != nil {
		logger.WithError(err).Error("Failed to fetch stream events from ClickHouse")
		c.JSON(http.StatusInternalServerError, middleware.H{"error": "Failed to fetch events"})
		return
	}
	defer rows.Close()

	var events []map[string]interface{}
	for rows.Next() {
		var timestamp time.Time
		var eventID, eventType, status, nodeID, eventData string

		if err := rows.Scan(&timestamp, &eventID, &eventType, &status, &nodeID, &eventData); err != nil {
			logger.WithError(err).Error("Failed to scan stream event")
			continue
		}

		events = append(events, map[string]interface{}{
			"timestamp":  timestamp,
			"event_id":   eventID,
			"event_type": eventType,
			"status":     status,
			"node_id":    nodeID,
			"event_data": eventData,
		})
	}

	c.JSON(http.StatusOK, events)
}

// GetTrackListEvents returns track list updates for a specific stream
func GetTrackListEvents(c middleware.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, middleware.H{"error": "Tenant context required"})
		return
	}

	internalName := c.Param("internal_name")
	startTime := c.DefaultQuery("start_time", time.Now().Add(-24*time.Hour).Format(time.RFC3339))
	endTime := c.DefaultQuery("end_time", time.Now().Format(time.RFC3339))

	rows, err := clickhouse.QueryContext(c.Request.Context(), `
		SELECT timestamp, node_id, track_list, track_count
		FROM track_list_events
		WHERE tenant_id = $1 AND internal_name = $2
		AND timestamp BETWEEN $3 AND $4
		ORDER BY timestamp DESC
	`, tenantID, internalName, startTime, endTime)
	if err != nil {
		logger.WithError(err).Error("Failed to fetch track list events from ClickHouse")
		c.JSON(http.StatusInternalServerError, middleware.H{"error": "Failed to fetch track list events"})
		return
	}
	defer rows.Close()

	var events []map[string]interface{}
	for rows.Next() {
		var ts time.Time
		var nodeID, trackList string
		var trackCount int
		if err := rows.Scan(&ts, &nodeID, &trackList, &trackCount); err != nil {
			logger.WithError(err).Error("Failed to scan track list event")
			continue
		}
		events = append(events, map[string]interface{}{
			"timestamp":   ts,
			"node_id":     nodeID,
			"track_list":  trackList,
			"track_count": trackCount,
		})
	}

	c.JSON(http.StatusOK, events)
}

// GetViewerStats returns viewer statistics for a specific stream
func GetViewerStats(c middleware.Context) {
	tenantID := c.GetString("tenant_id")
	internalName := c.Param("internal_name")

	if tenantID == "" {
		c.JSON(http.StatusBadRequest, middleware.H{"error": "Tenant context required"})
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
		c.JSON(http.StatusNotFound, middleware.H{"error": "Stream analytics not found"})
		return
	}

	if err != nil {
		logger.WithError(err).Error("Failed to fetch viewer stats from YugaDB")
		c.JSON(http.StatusInternalServerError, middleware.H{"error": "Failed to fetch viewer stats"})
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
		WHERE tenant_id = $1 AND internal_name = $2
		AND timestamp >= NOW() - INTERVAL 24 HOUR
		ORDER BY timestamp DESC
	`, tenantID, internalName)

	if err != nil {
		logger.WithError(err).Error("Failed to fetch viewer history from ClickHouse")
		c.JSON(http.StatusInternalServerError, middleware.H{"error": "Failed to fetch viewer history"})
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
		WHERE tenant_id = $1 AND internal_name = $2
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

	c.JSON(http.StatusOK, middleware.H{
		"current_viewers":   currentViewers,
		"peak_viewers":      peakViewers,
		"total_connections": totalConnections,
		"viewer_history":    viewerHistory,
		"geo_stats":         geoStats,
	})
}

// GetPlatformOverview returns high-level platform metrics
func GetPlatformOverview(c middleware.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, middleware.H{"error": "Tenant context required"})
		return
	}

	// Define time window for overview (last 1 hour)
	overviewSince := time.Now().Add(-1 * time.Hour)

	// Get metrics from analytics data instead of directly querying users/streams tables
	var metrics struct {
		TotalUsers    int `json:"total_users"`
		ActiveUsers   int `json:"active_users"`
		TotalStreams  int `json:"total_streams"`
		ActiveStreams int `json:"active_streams"`
	}

	// Get user counts from ClickHouse analytics (last 24 hours)
	err := clickhouse.QueryRowContext(c.Request.Context(), `
		SELECT 
			uniq(user_id) as total_users,
			uniqIf(user_id, timestamp > now() - INTERVAL 1 HOUR) as active_users
		FROM connection_events 
		WHERE tenant_id = $1 
		AND timestamp > now() - INTERVAL 24 HOUR
	`, tenantID).Scan(&metrics.TotalUsers, &metrics.ActiveUsers)

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
		c.JSON(http.StatusInternalServerError, middleware.H{"error": "Failed to fetch platform overview"})
		return
	}

	// Get time-series data from ClickHouse
	var timeseriesMetrics struct {
		TotalViewers   int     `json:"total_viewers"`
		AverageViewers float64 `json:"average_viewers"`
		PeakBandwidth  float64 `json:"peak_bandwidth_mbps"`
	}

	err = clickhouse.QueryRowContext(c.Request.Context(), `
		SELECT 
			sum(viewer_count) as total_viewers,
			avg(viewer_count) as average_viewers,
			(SELECT COALESCE(max(bandwidth_out) / (1024*1024), 0) FROM stream_health_metrics WHERE tenant_id = $1 AND timestamp > $2) as peak_bandwidth_mbps
		FROM viewer_metrics 
		WHERE tenant_id = $1 
		AND timestamp > $2
	`, tenantID, overviewSince).Scan(
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

	// Combine metrics
	response := map[string]interface{}{
		"tenant_id":       tenantID,
		"total_users":     metrics.TotalUsers,
		"active_users":    metrics.ActiveUsers,
		"total_streams":   metrics.TotalStreams,
		"active_streams":  metrics.ActiveStreams,
		"total_viewers":   timeseriesMetrics.TotalViewers,
		"average_viewers": timeseriesMetrics.AverageViewers,
		"peak_bandwidth":  timeseriesMetrics.PeakBandwidth,
		"generated_at":    time.Now(),
	}

	c.JSON(http.StatusOK, response)
}

// GetRealtimeStreams returns current live streams with analytics
func GetRealtimeStreams(c middleware.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, middleware.H{"error": "Tenant context required"})
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
		c.JSON(http.StatusInternalServerError, middleware.H{"error": "Failed to fetch realtime streams"})
		return
	}
	defer rows.Close()

	var streams []map[string]interface{}
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
			WHERE tenant_id = $1 AND internal_name = $2
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

		streams = append(streams, map[string]interface{}{
			"internal_name":      internalName,
			"current_viewers":    currentViewers,
			"bandwidth_in":       bandwidthIn,
			"bandwidth_out":      bandwidthOut,
			"status":             status,
			"node_id":            nodeID,
			"location":           location,
			"viewer_trend":       metrics.ViewerTrend,
			"buffer_health":      metrics.BufferHealth,
			"connection_quality": metrics.ConnectionQuality,
			"unique_countries":   metrics.UniqueCountries,
		})
	}

	c.JSON(http.StatusOK, middleware.H{
		"streams": streams,
		"count":   len(streams),
	})
}

// GetRealtimeViewers returns current viewer counts across all streams
func GetRealtimeViewers(c middleware.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, middleware.H{"error": "Tenant context required"})
		return
	}

	// Get total viewers from ClickHouse (more accurate than YugaDB for real-time)
	var totalViewers int
	err := clickhouse.QueryRowContext(c.Request.Context(), `
		SELECT sum(viewer_count)
		FROM viewer_metrics
		WHERE tenant_id = $1
		AND timestamp >= NOW() - INTERVAL 5 MINUTE
	`, tenantID).Scan(&totalViewers)

	if err != nil && err != database.ErrNoRows {
		logger.WithError(err).Error("Failed to fetch total viewers from ClickHouse")
		c.JSON(http.StatusInternalServerError, middleware.H{"error": "Failed to fetch viewer count"})
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
		WHERE tenant_id = $1
		AND timestamp >= NOW() - INTERVAL 5 MINUTE
		GROUP BY internal_name
		ORDER BY avg_viewers DESC
	`, tenantID)

	if err != nil {
		logger.WithError(err).Error("Failed to fetch stream viewers from ClickHouse")
		c.JSON(http.StatusInternalServerError, middleware.H{"error": "Failed to fetch stream viewers"})
		return
	}
	defer rows.Close()

	var streamViewers []map[string]interface{}
	for rows.Next() {
		var internalName string
		var avgViewers, peakViewers float64
		var uniqueCountries, uniqueCities int

		if err := rows.Scan(&internalName, &avgViewers, &peakViewers, &uniqueCountries, &uniqueCities); err != nil {
			continue
		}

		streamViewers = append(streamViewers, map[string]interface{}{
			"internal_name":    internalName,
			"avg_viewers":      avgViewers,
			"peak_viewers":     peakViewers,
			"unique_countries": uniqueCountries,
			"unique_cities":    uniqueCities,
		})
	}

	c.JSON(http.StatusOK, middleware.H{
		"total_viewers":  totalViewers,
		"stream_viewers": streamViewers,
	})
}

// GetRealtimeEvents returns recent events across all streams
func GetRealtimeEvents(c middleware.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, middleware.H{"error": "Tenant context required"})
		return
	}

	var events []map[string]interface{}

	// Fetch recent stream_events from ClickHouse
	rows, err := clickhouse.QueryContext(c.Request.Context(), `
		SELECT timestamp, internal_name, event_type, status, node_id, event_data
		FROM stream_events
		WHERE tenant_id = $1 AND timestamp >= NOW() - INTERVAL 1 HOUR
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
		WHERE tenant_id = $1
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

	c.JSON(http.StatusOK, middleware.H{
		"events": events,
		"count":  len(events),
	})
}

// GetConnectionEvents returns connection events from ClickHouse
func GetConnectionEvents(c middleware.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, middleware.H{"error": "Tenant context required"})
		return
	}

	// Parse time range from query params
	startTime := c.DefaultQuery("start_time", time.Now().Add(-24*time.Hour).Format(time.RFC3339))
	endTime := c.DefaultQuery("end_time", time.Now().Format(time.RFC3339))

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
		WHERE tenant_id = $1
		AND timestamp BETWEEN $2 AND $3
		ORDER BY timestamp DESC
	`, tenantID, startTime, endTime)

	if err != nil {
		logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"error":     err,
		}).Error("Failed to fetch connection events from ClickHouse")
		c.JSON(http.StatusInternalServerError, middleware.H{"error": "Failed to fetch connection events"})
		return
	}
	defer rows.Close()

	var events []map[string]interface{}
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

		events = append(events, map[string]interface{}{
			"event_id":          e.EventID,
			"timestamp":         e.Timestamp,
			"tenant_id":         e.TenantID,
			"internal_name":     e.InternalName,
			"user_id":           e.UserID,
			"session_id":        e.SessionID,
			"connection_addr":   e.ConnectionAddr,
			"user_agent":        e.UserAgent,
			"connector":         e.Connector,
			"node_id":           e.NodeID,
			"country_code":      e.CountryCode,
			"city":              e.City,
			"latitude":          e.Latitude,
			"longitude":         e.Longitude,
			"event_type":        e.EventType,
			"session_duration":  e.SessionDuration,
			"bytes_transferred": e.BytesTransferred,
		})
	}

	c.JSON(http.StatusOK, events)
}

// GetNodeMetrics returns node metrics from ClickHouse
func GetNodeMetrics(c middleware.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, middleware.H{"error": "Tenant context required"})
		return
	}

	// Parse time range from query params
	startTime := c.DefaultQuery("start_time", time.Now().Add(-24*time.Hour).Format(time.RFC3339))
	endTime := c.DefaultQuery("end_time", time.Now().Format(time.RFC3339))

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
		WHERE tenant_id = $1
		AND timestamp BETWEEN $2 AND $3
		ORDER BY timestamp DESC
	`, tenantID, startTime, endTime)

	if err != nil {
		logger.WithError(err).WithFields(logging.Fields{
			"tenant_id":  tenantID,
			"start_time": startTime,
			"end_time":   endTime,
		}).Error("Failed to fetch node metrics from ClickHouse")
		c.JSON(http.StatusInternalServerError, middleware.H{"error": "Failed to fetch node metrics"})
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

	c.JSON(http.StatusOK, middleware.H{
		"metrics":    metrics,
		"count":      len(metrics),
		"start_time": startTime,
		"end_time":   endTime,
	})
}

// GetNodeMetrics1h returns hourly aggregated node metrics from ClickHouse materialized view
func GetNodeMetrics1h(c middleware.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, middleware.H{"error": "Tenant context required"})
		return
	}

	// Parse time range from query params
	startTime := c.DefaultQuery("start_time", time.Now().Add(-24*time.Hour).Format(time.RFC3339))
	endTime := c.DefaultQuery("end_time", time.Now().Format(time.RFC3339))

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
		WHERE timestamp_1h BETWEEN $1 AND $2
		ORDER BY timestamp_1h DESC
	`, startTime, endTime)

	if err != nil {
		logger.WithError(err).Error("Failed to fetch hourly node metrics from ClickHouse")
		c.JSON(http.StatusInternalServerError, middleware.H{"error": "Failed to fetch hourly metrics"})
		return
	}
	defer rows.Close()

	var metrics []map[string]interface{}
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

		metrics = append(metrics, map[string]interface{}{
			"timestamp":           m.Timestamp,
			"node_id":             m.NodeID,
			"avg_cpu":             m.AvgCPU,
			"peak_cpu":            m.PeakCPU,
			"avg_memory":          m.AvgMemory,
			"peak_memory":         m.PeakMemory,
			"total_bandwidth_in":  m.TotalBandwidthIn,
			"total_bandwidth_out": m.TotalBandwidthOut,
			"avg_health_score":    m.AvgHealthScore,
			"was_healthy":         m.WasHealthy,
		})
	}

	c.JSON(http.StatusOK, metrics)
}

// GetStreamHealthMetrics returns stream health metrics from ClickHouse
func GetStreamHealthMetrics(c middleware.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, middleware.H{"error": "Tenant context required"})
		return
	}

	// Parse time range from query params
	startTime := c.DefaultQuery("start_time", time.Now().Add(-24*time.Hour).Format(time.RFC3339))
	endTime := c.DefaultQuery("end_time", time.Now().Format(time.RFC3339))

	// Query ClickHouse for stream health metrics
	rows, err := clickhouse.QueryContext(c.Request.Context(), `
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
		WHERE tenant_id = $1
		AND timestamp BETWEEN $2 AND $3
		ORDER BY timestamp DESC
	`, tenantID, startTime, endTime)

	if err != nil {
		logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"error":     err,
		}).Error("Failed to fetch stream health metrics from ClickHouse")
		c.JSON(http.StatusInternalServerError, middleware.H{"error": "Failed to fetch stream health metrics"})
		return
	}
	defer rows.Close()

	var metrics []map[string]interface{}
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

		metrics = append(metrics, map[string]interface{}{
			"timestamp":             m.Timestamp,
			"tenant_id":             m.TenantID,
			"internal_name":         m.InternalName,
			"node_id":               m.NodeID,
			"bitrate":               m.Bitrate,
			"fps":                   m.FPS,
			"gop_size":              m.GOPSize,
			"width":                 m.Width,
			"height":                m.Height,
			"buffer_size":           m.BufferSize,
			"buffer_used":           m.BufferUsed,
			"buffer_health":         m.BufferHealth,
			"packets_sent":          m.PacketsSent,
			"packets_lost":          m.PacketsLost,
			"packets_retransmitted": m.PacketsRetransmitted,
			"bandwidth_in":          m.BandwidthIn,
			"bandwidth_out":         m.BandwidthOut,
			"codec":                 m.Codec,
			"profile":               m.Profile,
			"track_metadata":        m.TrackMetadata,
		})
	}

	c.JSON(http.StatusOK, metrics)
}

// GetStreamBufferEvents returns buffer events for a specific stream
func GetStreamBufferEvents(c middleware.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, middleware.H{"error": "Tenant context required"})
		return
	}
	internalName := c.Param("internal_name")
	startTime := c.DefaultQuery("start_time", time.Now().Add(-24*time.Hour).Format(time.RFC3339))
	endTime := c.DefaultQuery("end_time", time.Now().Format(time.RFC3339))

	rows, err := clickhouse.QueryContext(c.Request.Context(), `
		SELECT timestamp, event_id, status, node_id, event_data
		FROM stream_events
		WHERE tenant_id = $1 AND internal_name = $2 AND event_type = 'stream-buffer'
		AND timestamp BETWEEN $3 AND $4
		ORDER BY timestamp DESC
	`, tenantID, internalName, startTime, endTime)
	if err != nil {
		logger.WithError(err).Error("Failed to fetch buffer events")
		c.JSON(http.StatusInternalServerError, middleware.H{"error": "Failed to fetch buffer events"})
		return
	}
	defer rows.Close()

	var events []map[string]interface{}
	for rows.Next() {
		var ts time.Time
		var eventID, status, nodeID, eventData string
		if err := rows.Scan(&ts, &eventID, &status, &nodeID, &eventData); err != nil {
			continue
		}
		events = append(events, map[string]interface{}{
			"timestamp":  ts,
			"event_id":   eventID,
			"status":     status,
			"node_id":    nodeID,
			"event_data": eventData,
		})
	}
	c.JSON(http.StatusOK, events)
}

// GetStreamEndEvents returns end events for a specific stream
func GetStreamEndEvents(c middleware.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, middleware.H{"error": "Tenant context required"})
		return
	}
	internalName := c.Param("internal_name")
	startTime := c.DefaultQuery("start_time", time.Now().Add(-24*time.Hour).Format(time.RFC3339))
	endTime := c.DefaultQuery("end_time", time.Now().Format(time.RFC3339))

	rows, err := clickhouse.QueryContext(c.Request.Context(), `
		SELECT timestamp, event_id, status, node_id, event_data
		FROM stream_events
		WHERE tenant_id = $1 AND internal_name = $2 AND event_type = 'stream-end'
		AND timestamp BETWEEN $3 AND $4
		ORDER BY timestamp DESC
	`, tenantID, internalName, startTime, endTime)
	if err != nil {
		logger.WithError(err).Error("Failed to fetch end events")
		c.JSON(http.StatusInternalServerError, middleware.H{"error": "Failed to fetch end events"})
		return
	}
	defer rows.Close()

	var events []map[string]interface{}
	for rows.Next() {
		var ts time.Time
		var eventID, status, nodeID, eventData string
		if err := rows.Scan(&ts, &eventID, &status, &nodeID, &eventData); err != nil {
			continue
		}
		events = append(events, map[string]interface{}{
			"timestamp":  ts,
			"event_id":   eventID,
			"status":     status,
			"node_id":    nodeID,
			"event_data": eventData,
		})
	}
	c.JSON(http.StatusOK, events)
}
