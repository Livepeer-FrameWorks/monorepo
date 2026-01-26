package testutil

import (
	"database/sql/driver"
	"time"
)

// DatabaseFixtures provides test data fixtures for database testing
type DatabaseFixtures struct{}

// NewDatabaseFixtures creates a new database fixtures helper
func NewDatabaseFixtures() *DatabaseFixtures {
	return &DatabaseFixtures{}
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
		"timestamp", "internal_name", "selected_node", "status",
		"details", "score", "client_ip", "client_country",
		"client_latitude", "client_longitude",
		"node_latitude", "node_longitude", "node_name",
		"routing_distance_km", "latency_ms", "candidates_count",
		"event_type", "source",
	}
}

// GetRoutingEventsQuery returns the ClickHouse query for routing events
func (f *DatabaseFixtures) GetRoutingEventsQuery() string {
	return `
		SELECT 
			timestamp,
			internal_name,
			selected_node,
			status,
			details,
			score,
			client_ip,
			client_country,
			client_latitude,
			client_longitude,
			node_latitude,
			node_longitude,
			node_name,
			routing_distance_km,
			latency_ms,
			candidates_count,
			event_type,
			source
		FROM routing_decisions
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
