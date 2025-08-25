package clips

import (
	"time"
)

// ClipStatus represents the current state of a clip
type ClipStatus string

const (
	ClipStatusRequested  ClipStatus = "requested"  // Clip creation requested
	ClipStatusProcessing ClipStatus = "processing" // Currently being processed
	ClipStatusReady      ClipStatus = "ready"      // Ready for viewing
	ClipStatusFailed     ClipStatus = "failed"     // Processing failed
	ClipStatusLost       ClipStatus = "lost"       // File exists in DB but not on storage
)

// ClipFormat represents supported clip formats
type ClipFormat string

const (
	ClipFormatMP4  ClipFormat = "mp4"
	ClipFormatWebM ClipFormat = "webm"
	ClipFormatTS   ClipFormat = "ts"
	ClipFormatFLV  ClipFormat = "flv"
)

// ClipMetadata represents internal clip metadata (used by Foghorn/Commodore)
// This struct contains tenant information and should never be sent to edge nodes
type ClipMetadata struct {
	ID           string     `json:"id" db:"id"`
	TenantID     string     `json:"tenant_id" db:"tenant_id"`
	StreamID     string     `json:"stream_id" db:"stream_id"`
	UserID       string     `json:"user_id" db:"user_id"`
	ClipHash     string     `json:"clip_hash" db:"clip_hash"`
	StreamName   string     `json:"stream_name" db:"stream_name"`
	Title        string     `json:"title,omitempty" db:"title"`
	StartTime    int64      `json:"start_time" db:"start_time"`
	Duration     int64      `json:"duration" db:"duration"`
	NodeID       string     `json:"node_id,omitempty" db:"node_id"`
	StoragePath  string     `json:"storage_path,omitempty" db:"storage_path"`
	BaseURL      string     `json:"base_url,omitempty" db:"base_url"`
	SizeBytes    int64      `json:"size_bytes,omitempty" db:"size_bytes"`
	Status       ClipStatus `json:"status" db:"status"`
	ErrorMessage string     `json:"error_message,omitempty" db:"error_message"`
	RequestID    string     `json:"request_id,omitempty" db:"request_id"`
	CreatedAt    time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at" db:"updated_at"`
}

// ClipInfo represents public clip information (safe for API responses)
// This struct excludes sensitive internal details
type ClipInfo struct {
	ID        string     `json:"id"`
	Title     string     `json:"title,omitempty"`
	StartTime int64      `json:"start_time"`
	Duration  int64      `json:"duration"`
	SizeBytes int64      `json:"size_bytes,omitempty"`
	Status    ClipStatus `json:"status"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// ClipURL represents a signed URL for accessing a clip
type ClipURL struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
	Format    string    `json:"format"`
}

// ClipCreateRequest represents a request to create a new clip
type ClipCreateRequest struct {
	StreamID    string `json:"stream_id" validate:"required,uuid"`
	Title       string `json:"title,omitempty"`
	StartTimeMs int64  `json:"start_time_ms" validate:"required,min=0"`
	DurationMs  int64  `json:"duration_ms" validate:"required,min=1000"` // Minimum 1 second
	Format      string `json:"format,omitempty" validate:"omitempty,oneof=mp4 webm ts flv"`
}

// ClipCreateResponse represents the response after creating a clip
type ClipCreateResponse struct {
	ClipID    string     `json:"clip_id"`
	Status    ClipStatus `json:"status"`
	CreatedAt time.Time  `json:"created_at"`
}

// EdgeClipInfo represents clip information stored on edge nodes
// This excludes all tenant information for security
type EdgeClipInfo struct {
	ClipHash   string `json:"clip_hash"`
	StreamName string `json:"stream_name"`
	FilePath   string `json:"file_path"`
	SizeBytes  int64  `json:"size_bytes,omitempty"`
	Format     string `json:"format"`
	CreatedAt  int64  `json:"created_at_unix,omitempty"`
}

// NodeOutputs represents parsed MistServer output configuration
type NodeOutputs struct {
	NodeID      string            `json:"node_id" db:"node_id"`
	Outputs     map[string]string `json:"outputs" db:"outputs"` // JSON stored as JSONB
	BaseURL     string            `json:"base_url" db:"base_url"`
	LastUpdated time.Time         `json:"last_updated" db:"last_updated"`
}

// ArtifactRegistryEntry represents an entry in the artifact registry
type ArtifactRegistryEntry struct {
	ID         string    `json:"id" db:"id"`
	NodeID     string    `json:"node_id" db:"node_id"`
	ClipHash   string    `json:"clip_hash" db:"clip_hash"`
	StreamName string    `json:"stream_name" db:"stream_name"`
	FilePath   string    `json:"file_path" db:"file_path"`
	SizeBytes  int64     `json:"size_bytes" db:"size_bytes"`
	CreatedAt  time.Time `json:"created_at" db:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at" db:"last_seen_at"`
	IsOrphaned bool      `json:"is_orphaned" db:"is_orphaned"`
}

// StoragePolicy represents JIT cleanup policy configuration
type StoragePolicy struct {
	ID                      string    `json:"id" db:"id"`
	NodeID                  string    `json:"node_id,omitempty" db:"node_id"`     // NULL = global policy
	TenantID                string    `json:"tenant_id,omitempty" db:"tenant_id"` // NULL = all tenants
	MinRetentionHours       int       `json:"min_retention_hours" db:"min_retention_hours"`
	MaxStoragePercent       int       `json:"max_storage_percent" db:"max_storage_percent"`
	CleanupThresholdPercent int       `json:"cleanup_threshold_percent" db:"cleanup_threshold_percent"`
	PriorityRules           []string  `json:"priority_rules" db:"priority_rules"`
	IsActive                bool      `json:"is_active" db:"is_active"`
	CreatedAt               time.Time `json:"created_at" db:"created_at"`
	UpdatedAt               time.Time `json:"updated_at" db:"updated_at"`
}

// CleanupCandidate represents a clip that can be cleaned up
type CleanupCandidate struct {
	ClipHash     string    `json:"clip_hash"`
	StreamName   string    `json:"stream_name"`
	SizeBytes    int64     `json:"size_bytes"`
	CreatedAt    time.Time `json:"created_at"`
	LastAccessed time.Time `json:"last_accessed"`
	AccessCount  int       `json:"access_count"`
	Priority     int       `json:"priority"` // Lower = higher priority for deletion
}

// Default values
const (
	DefaultClipFormat              = "mp4"
	DefaultMinRetentionHours       = 24
	DefaultMaxStoragePercent       = 80
	DefaultCleanupThresholdPercent = 90
	DefaultClipTitlePrefix         = "Clip"

	// URL signing
	DefaultURLExpirationMinutes = 60
	DefaultAccessTokenLength    = 32
)
