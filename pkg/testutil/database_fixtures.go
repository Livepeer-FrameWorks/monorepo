package testutil

import (
	"database/sql/driver"
	"time"

	"frameworks/pkg/models"
)

// DatabaseFixtures provides test data fixtures for database testing
type DatabaseFixtures struct{}

// NewDatabaseFixtures creates a new database fixtures helper
func NewDatabaseFixtures() *DatabaseFixtures {
	return &DatabaseFixtures{}
}

// StreamAnalyticsWithNulls creates test data with NULL fields
func (f *DatabaseFixtures) StreamAnalyticsWithNulls() *models.StreamAnalytics {
	return &models.StreamAnalytics{
		ID:               "stream-null-test",
		TenantID:         "tenant-123",
		InternalName:     "test-stream",
		SessionStartTime: nil, // NULL pointer
		SessionEndTime:   nil, // NULL pointer
		CurrentViewers:   0,   // 0 represents NULL
		PeakViewers:      0,   // 0 represents NULL
		TotalConnections: 0,   // 0 represents NULL
		BandwidthIn:      0,   // 0 represents NULL
		BandwidthOut:     0,   // 0 represents NULL
		TotalBandwidthGB: 0.0, // 0.0 represents NULL
		BitrateKbps:      nil, // NULL pointer
		Resolution:       nil, // NULL pointer
		PacketsSent:      0,   // 0 represents NULL
		PacketsLost:      0,   // 0 represents NULL
		PacketsRetrans:   0,   // 0 represents NULL
		FirstMs:          nil, // NULL pointer
		LastMs:           nil, // NULL pointer
	}
}

// StreamAnalyticsValid creates valid test data
func (f *DatabaseFixtures) StreamAnalyticsValid() *models.StreamAnalytics {
	startTime := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	endTime := time.Date(2024, 1, 15, 11, 0, 0, 0, time.UTC)
	bitrateKbps := 2000
	resolution := "1920x1080"
	firstMs := 100
	lastMs := 200
	nodeID := "node-123"
	nodeName := "Test Node"
	latitude := 40.7128
	longitude := -74.0060
	location := "New York"
	status := "active"

	return &models.StreamAnalytics{
		ID:               "stream-valid-test",
		TenantID:         "tenant-123",
		InternalName:     "test-stream",
		SessionStartTime: &startTime,
		SessionEndTime:   &endTime,
		CurrentViewers:   150,
		PeakViewers:      200,
		TotalConnections: 300,
		BandwidthIn:      1000,
		BandwidthOut:     2000,
		TotalBandwidthGB: 1.5,
		BitrateKbps:      &bitrateKbps,
		Resolution:       &resolution,
		PacketsSent:      10000,
		PacketsLost:      50,
		PacketsRetrans:   5,
		Upbytes:          500000,
		Downbytes:        400000,
		FirstMs:          &firstMs,
		LastMs:           &lastMs,
		TrackCount:       2,
		Inputs:           2,
		Outputs:          2,
		NodeID:           &nodeID,
		NodeName:         &nodeName,
		Latitude:         &latitude,
		Longitude:        &longitude,
		Location:         &location,
		Status:           &status,
		LastUpdated:      time.Now(),
		CreatedAt:        time.Now().Add(-1 * time.Hour),
	}
}

// StreamAnalyticsPartialNulls creates test data with some NULL fields
func (f *DatabaseFixtures) StreamAnalyticsPartialNulls() *models.StreamAnalytics {
	startTime := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	bitrateKbps := 2000

	return &models.StreamAnalytics{
		ID:               "stream-partial-test",
		TenantID:         "tenant-123",
		InternalName:     "test-stream",
		SessionStartTime: &startTime,   // Valid
		SessionEndTime:   nil,          // NULL
		CurrentViewers:   150,          // Valid
		PeakViewers:      0,            // NULL
		TotalConnections: 200,          // Valid
		BandwidthIn:      0,            // NULL
		BandwidthOut:     1500,         // Valid
		TotalBandwidthGB: 0.0,          // NULL
		BitrateKbps:      &bitrateKbps, // Valid
		Resolution:       nil,          // NULL
		PacketsSent:      5000,         // Valid
		PacketsLost:      0,            // NULL
		PacketsRetrans:   2,            // Valid
		FirstMs:          nil,          // NULL
		LastMs:           nil,          // NULL
	}
}

// GetStreamAnalyticsColumns returns the column names for stream analytics queries
func (f *DatabaseFixtures) GetStreamAnalyticsColumns() []string {
	return []string{
		"id", "tenant_id", "internal_name", "internal_name",
		"session_start_time", "session_end_time", "total_session_duration",
		"current_viewers", "peak_viewers", "total_connections",
		"bandwidth_in", "bandwidth_out", "total_bandwidth_gb",
		"bitrate_kbps", "resolution", "packets_sent", "packets_lost",
		"packets_retrans", "upbytes", "downbytes", "first_ms", "last_ms",
		"track_count", "inputs", "outputs", "node_id", "node_name", "latitude",
		"longitude", "location", "status", "last_updated", "created_at",
	}
}

// GetStreamAnalyticsRowData returns row data for a given StreamAnalytics model
func (f *DatabaseFixtures) GetStreamAnalyticsRowData(data *models.StreamAnalytics) []interface{} {
	return []interface{}{
		data.ID, data.TenantID, data.InternalName, data.InternalName,
		data.SessionStartTime, data.SessionEndTime, data.TotalSessionDuration,
		data.CurrentViewers, data.PeakViewers, data.TotalConnections,
		data.BandwidthIn, data.BandwidthOut, data.TotalBandwidthGB,
		data.BitrateKbps, data.Resolution, data.PacketsSent, data.PacketsLost,
		data.PacketsRetrans, data.Upbytes, data.Downbytes, data.FirstMs, data.LastMs,
		data.TrackCount, data.Inputs, data.Outputs, data.NodeID, data.NodeName, data.Latitude,
		data.Longitude, data.Location, data.Status, data.LastUpdated, data.CreatedAt,
	}
}

// GetCOALESCEQuery returns a SQL query with COALESCE for NULL handling
func (f *DatabaseFixtures) GetCOALESCEQuery() string {
	return `
		SELECT 
			sa.id, 
			sa.tenant_id, 
			sa.internal_name, 
			COALESCE(sa.session_start_time, '1970-01-01 00:00:00'::timestamp) as session_start_time,
			COALESCE(sa.session_end_time, '1970-01-01 00:00:00'::timestamp) as session_end_time,
			COALESCE(sa.current_viewers, 0) as current_viewers,
			COALESCE(sa.peak_viewers, 0) as peak_viewers,
			COALESCE(sa.total_connections, 0) as total_connections,
			COALESCE(sa.bandwidth_in, 0) as bandwidth_in,
			COALESCE(sa.bandwidth_out, 0) as bandwidth_out
		FROM stream_analytics sa
		WHERE sa.tenant_id = $1 AND sa.internal_name = $2
	`
}

// ViewerMetricsData creates test viewer metrics data
func (f *DatabaseFixtures) ViewerMetricsData() []map[string]interface{} {
	return []map[string]interface{}{
		{
			"timestamp":    time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			"stream_name":  "test-stream-1",
			"viewer_count": 100,
			"peak_viewers": 150,
			"bandwidth":    1000.5,
		},
		{
			"timestamp":    time.Date(2024, 1, 15, 10, 5, 0, 0, time.UTC),
			"stream_name":  "test-stream-1",
			"viewer_count": 120,
			"peak_viewers": 150,
			"bandwidth":    1200.7,
		},
		{
			"timestamp":    time.Date(2024, 1, 15, 10, 10, 0, 0, time.UTC),
			"stream_name":  "test-stream-1",
			"viewer_count": 90,
			"peak_viewers": 150,
			"bandwidth":    900.3,
		},
	}
}

// GetViewerMetricsColumns returns column names for viewer metrics queries
func (f *DatabaseFixtures) GetViewerMetricsColumns() []string {
	return []string{"timestamp", "stream_name", "viewer_count", "peak_viewers", "bandwidth"}
}

// GetViewerMetricsQuery returns the ClickHouse query for viewer metrics
func (f *DatabaseFixtures) GetViewerMetricsQuery() string {
	return `
		SELECT 
			timestamp,
			stream_name,
			viewer_count,
			peak_viewers,
			bandwidth
		FROM viewer_metrics
		WHERE tenant_id = $1 
		AND timestamp BETWEEN $2 AND $3
		ORDER BY timestamp DESC
	`
}

// RoutingEventsData creates test routing events data
func (f *DatabaseFixtures) RoutingEventsData() []map[string]interface{} {
	return []map[string]interface{}{
		{
			"timestamp":     time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			"stream_name":   "test-stream",
			"selected_node": "node-1",
			"status":        "success",
			"details":       "Route selected successfully",
			"score":         95.5,
			"client_ip":     "192.168.1.100",
		},
		{
			"timestamp":     time.Date(2024, 1, 15, 10, 1, 0, 0, time.UTC),
			"stream_name":   "test-stream",
			"selected_node": "node-2",
			"status":        "success",
			"details":       "Fallback route used",
			"score":         87.2,
			"client_ip":     "192.168.1.101",
		},
	}
}

// GetRoutingEventsColumns returns column names for routing events queries
func (f *DatabaseFixtures) GetRoutingEventsColumns() []string {
	return []string{
		"timestamp", "stream_name", "selected_node", "status",
		"details", "score", "client_ip", "client_country",
		"client_region", "client_city", "client_latitude", "client_longitude",
		"node_scores", "routing_metadata",
	}
}

// GetRoutingEventsQuery returns the ClickHouse query for routing events
func (f *DatabaseFixtures) GetRoutingEventsQuery() string {
	return `
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
	`
}

// NullTimeValue represents a nullable time value for SQL mocking
type NullTimeValue struct {
	Time  time.Time
	Valid bool
}

// Match implements sqlmock.Argument interface
func (n NullTimeValue) Match(v driver.Value) bool {
	switch val := v.(type) {
	case time.Time:
		return n.Valid && val.Equal(n.Time)
	case nil:
		return !n.Valid
	default:
		return false
	}
}

// NullStringValue represents a nullable string value for SQL mocking
type NullStringValue struct {
	String string
	Valid  bool
}

// Match implements sqlmock.Argument interface
func (n NullStringValue) Match(v driver.Value) bool {
	switch val := v.(type) {
	case string:
		return n.Valid && val == n.String
	case nil:
		return !n.Valid
	default:
		return false
	}
}

// NullIntValue represents a nullable int value for SQL mocking
type NullIntValue struct {
	Int   int
	Valid bool
}

// Match implements sqlmock.Argument interface
func (n NullIntValue) Match(v driver.Value) bool {
	switch val := v.(type) {
	case int:
		return n.Valid && val == n.Int
	case int64:
		return n.Valid && int64(n.Int) == val
	case nil:
		return !n.Valid
	default:
		return false
	}
}

// COALESCEDefaultTime returns a COALESCE default time (epoch)
func COALESCEDefaultTime() time.Time {
	return time.Unix(0, 0) // 1970-01-01 00:00:00 UTC
}

// COALESCETimeOrDefault returns the time if valid, otherwise the default
func COALESCETimeOrDefault(t time.Time, defaultTime time.Time) time.Time {
	if t.IsZero() {
		return defaultTime
	}
	return t
}

// COALESCEIntOrDefault returns the int if not zero, otherwise the default
func COALESCEIntOrDefault(i int, defaultInt int) int {
	if i == 0 {
		return defaultInt
	}
	return i
}

// COALESCEStringOrDefault returns the string if not empty, otherwise the default
func COALESCEStringOrDefault(s string, defaultString string) string {
	if s == "" {
		return defaultString
	}
	return s
}
