package clips

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// GenerateClipHash generates a secure opaque identifier for clips
// Uses SHA256(stream_name + start_time + duration + random_salt)[:16]
// This ensures no tenant information is exposed in the hash
func GenerateClipHash(streamName string, startTimeMs, durationMs int64) (string, error) {
	// Generate random salt for uniqueness
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("failed to generate random salt: %w", err)
	}

	// Create hash input combining stream metadata with salt
	hashInput := fmt.Sprintf("%s_%d_%d_%x_%d",
		streamName,
		startTimeMs,
		durationMs,
		salt,
		time.Now().UnixNano()) // Additional entropy

	// Generate SHA256 hash and take first 16 characters
	hasher := sha256.New()
	hasher.Write([]byte(hashInput))
	fullHash := hex.EncodeToString(hasher.Sum(nil))

	// Return first 32 characters (16 bytes) as hex string
	return fullHash[:32], nil
}

// ValidateClipHash validates that a clip hash is properly formatted
func ValidateClipHash(hash string) bool {
	if len(hash) != 32 {
		return false
	}

	// Ensure it's valid hex
	_, err := hex.DecodeString(hash)
	return err == nil
}

// BuildClipStoragePath creates the storage path for a clip on edge nodes
// Format: clips/{stream_name}/{clip_hash}.{format}
func BuildClipStoragePath(streamName, clipHash, format string) string {
	return fmt.Sprintf("clips/%s/%s.%s", streamName, clipHash, format)
}

// ParseClipStoragePath extracts components from a clip storage path
func ParseClipStoragePath(storagePath string) (streamName, clipHash, format string, err error) {
	// Simple parsing of clips/{stream_name}/{clip_hash}.{format}
	// This is a basic implementation - could be improved with regex

	if len(storagePath) < 7 || storagePath[:6] != "clips/" {
		return "", "", "", fmt.Errorf("invalid clip storage path format")
	}

	// Remove "clips/" prefix
	remainder := storagePath[6:]

	// Find last slash to separate stream_name from filename
	lastSlash := -1
	for i := len(remainder) - 1; i >= 0; i-- {
		if remainder[i] == '/' {
			lastSlash = i
			break
		}
	}

	if lastSlash == -1 {
		return "", "", "", fmt.Errorf("invalid clip storage path format: no stream separator")
	}

	streamName = remainder[:lastSlash]
	filename := remainder[lastSlash+1:]

	// Find last dot to separate clip_hash from format
	lastDot := -1
	for i := len(filename) - 1; i >= 0; i-- {
		if filename[i] == '.' {
			lastDot = i
			break
		}
	}

	if lastDot == -1 {
		return "", "", "", fmt.Errorf("invalid clip storage path format: no format extension")
	}

	clipHash = filename[:lastDot]
	format = filename[lastDot+1:]

	return streamName, clipHash, format, nil
}

// ClipStorageConfig holds configuration for clip storage
type ClipStorageConfig struct {
	LocalPath     string // Base local storage path
	S3Bucket      string // Optional S3 bucket for cloud storage
	S3Prefix      string // Optional S3 prefix
	DefaultFormat string // Default clip format (mp4)
}

// DefaultClipStorageConfig returns sensible defaults for clip storage
func DefaultClipStorageConfig() ClipStorageConfig {
	return ClipStorageConfig{
		LocalPath:     "/var/lib/frameworks/clips",
		DefaultFormat: "mp4",
	}
}
