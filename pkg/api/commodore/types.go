package commodore

import (
	"time"

	"frameworks/pkg/api/common"
	"frameworks/pkg/models"
)

// ValidateStreamKeyResponse represents the response from the validate stream key API
type ValidateStreamKeyResponse struct {
	Valid        bool   `json:"valid"`
	StreamID     string `json:"stream_id,omitempty"`
	UserID       string `json:"user_id,omitempty"`
	TenantID     string `json:"tenant_id,omitempty"`
	InternalName string `json:"internal_name,omitempty"`
	Error        string `json:"error,omitempty"`
}

// ResolvePlaybackIDResponse represents the response from the resolve playback ID API
type ResolvePlaybackIDResponse struct {
	InternalName string `json:"internal_name"`
	TenantID     string `json:"tenant_id"`
	Status       string `json:"status"`
	PlaybackID   string `json:"playbook_id"`
}

// ErrorResponse is a type alias to the common error response
type ErrorResponse = common.ErrorResponse

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

// Authentication requests and responses
type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type RegisterRequest struct {
	Email     string `json:"email"`
	Password  string `json:"password"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

type AuthResponse struct {
	Token     string      `json:"token"`
	User      models.User `json:"user"`
	ExpiresAt time.Time   `json:"expires_at"`
}

// Registration response
type RegisterResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// Stream management requests
type CreateStreamRequest struct {
	Title       string `json:"title" binding:"required"`
	Description string `json:"description"`
	IsPublic    bool   `json:"is_public"`
	IsRecording bool   `json:"is_recording"`
	MaxViewers  int    `json:"max_viewers"`
}

type UpdateStreamRequest struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	Record      *bool   `json:"record,omitempty"`
}

// Clip management
type CreateClipRequest struct {
	StreamID    string `json:"stream_id"`
	StartTime   int64  `json:"start_time"`
	EndTime     int64  `json:"end_time"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
}

type ClipResponse struct {
	ID          string    `json:"id"`
	StreamID    string    `json:"stream_id"`
	TenantID    string    `json:"tenant_id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	StartTime   int64     `json:"start_time"`
	EndTime     int64     `json:"end_time"`
	Duration    int64     `json:"duration"`
	PlaybackID  string    `json:"playbook_id"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Stream responses
type StreamsResponse = []models.Stream
type StreamResponse = models.Stream

// === STREAM MANAGEMENT ===

// StreamRequest represents a request for stream routing (moved from pkg/models)
type StreamRequest struct {
	TenantID string `json:"tenant_id"`
	StreamID string `json:"stream_id"`
}

// Stream creation response
type CreateStreamResponse struct {
	ID           string `json:"id"`
	StreamKey    string `json:"stream_key"`
	PlaybackID   string `json:"playback_id"`
	InternalName string `json:"internal_name"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	Status       string `json:"status"`
}

// Stream metrics response
type StreamMetricsResponse struct {
	Metrics struct {
		Viewers      int       `json:"viewers"`
		Status       string    `json:"status"`
		BandwidthIn  *int64    `json:"bandwidth_in"`
		BandwidthOut *int64    `json:"bandwidth_out"`
		Resolution   *string   `json:"resolution"`
		Bitrate      *string   `json:"bitrate"`
		MaxViewers   *int      `json:"max_viewers"`
		UpdatedAt    time.Time `json:"updated_at"`
	} `json:"metrics"`
}

// Stream embed response
type StreamEmbedResponse struct {
	PlaybackID string `json:"playback_id"`
}

// User profile response
type UserProfileResponse struct {
	User    models.User     `json:"user"`
	Streams []models.Stream `json:"streams"`
}

// Stream refresh key response
type RefreshKeyResponse struct {
	Message           string `json:"message"`
	StreamID          string `json:"stream_id"`
	StreamKey         string `json:"stream_key"`
	PlaybackID        string `json:"playback_id"`
	OldKeyInvalidated bool   `json:"old_key_invalidated"`
}

// SuccessResponse is a type alias to the common success response
type SuccessResponse = common.SuccessResponse

// Stream delete response
type StreamDeleteResponse struct {
	Message     string    `json:"message"`
	StreamID    string    `json:"stream_id"`
	StreamTitle string    `json:"stream_title"`
	DeletedAt   time.Time `json:"deleted_at"`
}

// Node lookup response
type NodeLookupResponse struct {
	BaseURL   string `json:"base_url"`
	ClusterID string `json:"cluster_id"`
}

// API token response
type CreateAPITokenResponse struct {
	ID          string     `json:"id"`
	TokenValue  string     `json:"token_value"`
	TokenName   string     `json:"token_name"`
	Permissions []string   `json:"permissions"`
	ExpiresAt   *time.Time `json:"expires_at"`
	CreatedAt   time.Time  `json:"created_at"`
	Message     string     `json:"message"`
}

// Email verification response
type EmailVerificationResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// Internal name resolution response
type InternalNameResponse struct {
	InternalName string `json:"internal_name"`
	TenantID     string `json:"tenant_id"`
}

// Kafka config response
type KafkaConfigResponse struct {
	Brokers     []string `json:"brokers"`
	TopicPrefix string   `json:"topic_prefix"`
}

// User stream info for profile responses
type UserStreamInfo struct {
	ID         string `json:"id"`
	StreamKey  string `json:"stream_key"`
	PlaybackID string `json:"playback_id"`
	Title      string `json:"title"`
	Status     string `json:"status"`
}

// User profile with streams response
type UserWithStreamsResponse struct {
	User    models.User      `json:"user"`
	Streams []UserStreamInfo `json:"streams"`
}

// Not implemented response
type NotImplementedResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

// API token list response
type APITokenListResponse struct {
	Tokens []APITokenInfo `json:"tokens"`
	Count  int            `json:"count"`
}

// API token info (without sensitive data)
type APITokenInfo struct {
	ID          string     `json:"id"`
	TokenName   string     `json:"token_name"`
	Permissions []string   `json:"permissions"`
	Status      string     `json:"status"`
	LastUsedAt  *time.Time `json:"last_used_at"`
	ExpiresAt   *time.Time `json:"expires_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

// API token revocation response
type RevokeAPITokenResponse struct {
	Message   string    `json:"message"`
	TokenID   string    `json:"token_id"`
	TokenName string    `json:"token_name"`
	RevokedAt time.Time `json:"revoked_at"`
}
