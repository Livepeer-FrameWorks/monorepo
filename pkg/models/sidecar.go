package models

import "time"

type DecklogEvent struct {
	EventType string                 `json:"event_type"`
	Data      map[string]interface{} `json:"data"`
}

type NodeInfo struct {
	NodeID     string                 `json:"node_id"`
	BaseURL    string                 `json:"base_url"`
	LastSeen   time.Time              `json:"last_seen"`
	JSONData   map[string]interface{} `json:"json_data"`
	IsHealthy  bool                   `json:"is_healthy"`
	ErrorCount int                    `json:"error_count"`
	LastError  string                 `json:"last_error,omitempty"`
	Latitude   *float64               `json:"latitude,omitempty"`
	Longitude  *float64               `json:"longitude,omitempty"`
	Location   string                 `json:"location,omitempty"`
}

type NodeUpdate struct {
	NodeID   string
	BaseURL  string
	JSONData map[string]interface{}
	Error    error
}
