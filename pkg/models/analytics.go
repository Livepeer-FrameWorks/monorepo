package models

import "time"

// AnalyticsViewerSession represents a single viewer metric row from ClickHouse
type AnalyticsViewerSession struct {
	Timestamp         time.Time `json:"timestamp"`
	InternalName      string    `json:"internal_name"`
	ViewerCount       int       `json:"viewer_count"`
	ConnectionType    string    `json:"connection_type"`
	NodeID            string    `json:"node_id"`
	CountryCode       string    `json:"country_code"`
	City              string    `json:"city"`
	Latitude          float64   `json:"latitude"`
	Longitude         float64   `json:"longitude"`
	ConnectionQuality float32   `json:"connection_quality"`
	BufferHealth      float32   `json:"buffer_health"`
}

// AnalyticsRoutingEvent represents a routing event row from ClickHouse
type AnalyticsRoutingEvent struct {
	Timestamp       time.Time `json:"timestamp"`
	StreamName      string    `json:"stream_name"`
	SelectedNode    string    `json:"selected_node"`
	Status          string    `json:"status"`
	Details         string    `json:"details"`
	Score           int       `json:"score"`
	ClientIP        string    `json:"client_ip"`
	ClientCountry   string    `json:"client_country"`
	ClientLatitude  float64   `json:"client_latitude"`
	ClientLongitude float64   `json:"client_longitude"`
	NodeLatitude    float64   `json:"node_latitude"`
	NodeLongitude   float64   `json:"node_longitude"`
	NodeName        string    `json:"node_name"`
	RoutingDistance *float64  `json:"routing_distance_km"`
}

// AnalyticsViewerSession5m represents a 5-minute aggregated viewer metric row
type AnalyticsViewerSession5m struct {
	Timestamp       time.Time `json:"timestamp"`
	InternalName    string    `json:"internal_name"`
	NodeID          string    `json:"node_id"`
	PeakViewers     int       `json:"peak_viewers"`
	AvgViewers      float64   `json:"avg_viewers"`
	UniqueCountries int       `json:"unique_countries"`
	UniqueCities    int       `json:"unique_cities"`
	AvgBufferHealth float32   `json:"avg_buffer_health"`
}
