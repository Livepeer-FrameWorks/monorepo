package commodore

import "frameworks/pkg/models"

// ValidateStreamKeyResponse represents the response from the validate stream key API
type ValidateStreamKeyResponse = models.StreamValidationResponse

// ResolvePlaybackIDResponse represents the response from the resolve playback ID API
type ResolvePlaybackIDResponse struct {
	InternalName string `json:"internal_name"`
	TenantID     string `json:"tenant_id"`
	Status       string `json:"status"`
	PlaybackID   string `json:"playback_id"`
}

// ErrorResponse represents a standard error response from Commodore
type ErrorResponse struct {
	Error string `json:"error"`
}

// StreamEventRequest represents a request to forward stream events
type StreamEventRequest struct {
	NodeID       string      `json:"node_id"`
	StreamKey    string      `json:"stream_key,omitempty"`
	InternalName string      `json:"internal_name,omitempty"`
	Hostname     string      `json:"hostname,omitempty"`
	PushURL      string      `json:"push_url,omitempty"`
	EventType    string      `json:"event_type"`
	Timestamp    int64       `json:"timestamp"`
	ClusterID    string      `json:"cluster_id,omitempty"`
	FoghornURI   string      `json:"foghorn_uri,omitempty"`
	NodeName     string      `json:"node_name,omitempty"`
	Latitude     *float64    `json:"latitude,omitempty"`
	Longitude    *float64    `json:"longitude,omitempty"`
	Location     string      `json:"location,omitempty"`
	ExtraData    interface{} `json:"-"` // For any additional fields
}
