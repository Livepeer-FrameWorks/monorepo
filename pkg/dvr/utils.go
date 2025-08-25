package dvr

import (
	"crypto/rand"
	"fmt"
	"path/filepath"
	"time"
)

// GenerateDVRHash creates a 32-character hex string like clip hashes
func GenerateDVRHash() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}
	return fmt.Sprintf("%x", bytes), nil
}

// BuildDVRStoragePath builds the storage path for DVR files on a node
func BuildDVRStoragePath(nodeID, dvrHash, streamName string) string {
	return filepath.Join("dvr", nodeID, dvrHash, streamName)
}

// BuildDVRManifestPath builds the manifest path for DVR playback
func BuildDVRManifestPath(storageBasePath, streamName string) string {
	return filepath.Join(storageBasePath, fmt.Sprintf("%s.m3u8", streamName))
}

// BuildDVRSegmentPath builds a path for DVR segments
func BuildDVRSegmentPath(storageBasePath, streamName string, segmentNum int) string {
	return filepath.Join(storageBasePath, fmt.Sprintf("%s_%06d.ts", streamName, segmentNum))
}

// DVRConfig represents the JSONB recording configuration
type DVRConfig struct {
	Enabled         bool   `json:"enabled"`
	RetentionDays   int    `json:"retention_days"`
	Format          string `json:"format"`           // "ts", "mp4", etc.
	SegmentDuration int    `json:"segment_duration"` // seconds
}

// DefaultDVRConfig returns the default DVR configuration
func DefaultDVRConfig() DVRConfig {
	return DVRConfig{
		Enabled:         false,
		RetentionDays:   30,
		Format:          "ts",
		SegmentDuration: 6,
	}
}

// IsRecordingEnabled checks if DVR recording is enabled in the config
func (c DVRConfig) IsRecordingEnabled() bool {
	return c.Enabled
}

// GetRetentionTime returns the retention duration
func (c DVRConfig) GetRetentionTime() time.Duration {
	return time.Duration(c.RetentionDays) * 24 * time.Hour
}

// GetSegmentDuration returns the segment duration
func (c DVRConfig) GetSegmentDuration() time.Duration {
	return time.Duration(c.SegmentDuration) * time.Second
}
