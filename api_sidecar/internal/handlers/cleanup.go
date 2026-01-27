package handlers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"frameworks/api_sidecar/internal/control"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"
)

// ClipCleanupInfo holds information about a clip candidate for cleanup
type ClipCleanupInfo struct {
	ClipHash     string
	FilePath     string
	SizeBytes    uint64
	CreatedAt    time.Time
	AccessCount  int
	LastAccessed time.Time
	Priority     float64 // Lower = higher priority for deletion
}

// CleanupMonitor manages storage cleanup operations
type CleanupMonitor struct {
	logger   logging.Logger
	basePath string
	running  bool
	stopCh   chan bool

	// Thresholds (configurable)
	cleanupThreshold  float64       // Start cleanup when storage > this % (default: 90%)
	targetThreshold   float64       // Clean until storage < this % (default: 80%)
	minRetentionHours int           // Never delete clips younger than this (default: 1 hour)
	checkInterval     time.Duration // How often to check storage (default: 5 minutes)
}

var (
	cleanupMonitor *CleanupMonitor
	cleanupLogger  logging.Logger
	errCleanupSkip = errors.New("cleanup skipped (not safe to evict)")
)

// InitCleanupMonitor initializes the cleanup monitor
func InitCleanupMonitor(logger logging.Logger, basePath string) {
	if cleanupMonitor != nil {
		return // Already initialized
	}

	cleanupLogger = logger

	cleanupMonitor = &CleanupMonitor{
		logger:            cleanupLogger,
		basePath:          basePath,
		running:           false,
		stopCh:            make(chan bool, 1),
		cleanupThreshold:  0.90, // 90%
		targetThreshold:   0.80, // 80%
		minRetentionHours: 1,    // 1 hour minimum
		checkInterval:     5 * time.Minute,
	}

	// Start monitoring in background
	go cleanupMonitor.start()

	cleanupLogger.WithFields(logging.Fields{
		"base_path":         basePath,
		"cleanup_threshold": cleanupMonitor.cleanupThreshold,
		"target_threshold":  cleanupMonitor.targetThreshold,
		"min_retention":     cleanupMonitor.minRetentionHours,
		"check_interval":    cleanupMonitor.checkInterval,
	}).Info("Cleanup monitor initialized")
}

// StopCleanupMonitor stops the cleanup monitor
func StopCleanupMonitor() {
	if cleanupMonitor != nil && cleanupMonitor.running {
		cleanupMonitor.stopCh <- true
		cleanupLogger.Info("Cleanup monitor stopped")
	}
}

// start begins the cleanup monitoring loop
func (cm *CleanupMonitor) start() {
	cm.running = true
	ticker := time.NewTicker(cm.checkInterval)
	defer ticker.Stop()

	cm.logger.Info("Cleanup monitor started")

	for {
		select {
		case <-cm.stopCh:
			cm.running = false
			return
		case <-ticker.C:
			if err := cm.checkAndCleanup(); err != nil {
				cm.logger.WithError(err).Error("Cleanup check failed")
			}
		}
	}
}

// checkAndCleanup checks storage usage and performs cleanup if needed
func (cm *CleanupMonitor) checkAndCleanup() error {
	clipsDir := filepath.Join(cm.basePath, "clips")

	// Check if clips directory exists
	if _, err := os.Stat(clipsDir); os.IsNotExist(err) {
		return nil // No clips directory, nothing to clean
	}

	// Get current storage usage
	usagePercent, usedBytes, totalBytes, err := cm.getStorageUsage(clipsDir)
	if err != nil {
		return fmt.Errorf("failed to get storage usage: %w", err)
	}

	cm.logger.WithFields(logging.Fields{
		"usage_percent": usagePercent,
		"used_gb":       float64(usedBytes) / (1024 * 1024 * 1024),
		"total_gb":      float64(totalBytes) / (1024 * 1024 * 1024),
	}).Debug("Storage usage check")

	// Check if cleanup is needed
	if usagePercent < cm.cleanupThreshold {
		return nil // No cleanup needed
	}

	cm.logger.WithFields(logging.Fields{
		"usage_percent":     usagePercent,
		"cleanup_threshold": cm.cleanupThreshold,
	}).Info("Storage usage above threshold, starting cleanup")

	// Calculate how much space we need to free
	targetBytes := uint64(float64(totalBytes) * cm.targetThreshold)
	bytesToFree := usedBytes - targetBytes

	// Get cleanup candidates
	candidates, err := cm.getCleanupCandidates(clipsDir)
	if err != nil {
		return fmt.Errorf("failed to get cleanup candidates: %w", err)
	}

	if len(candidates) == 0 {
		cm.logger.Warn("No cleanup candidates found despite high storage usage")
		return nil
	}

	// Sort candidates by priority (lowest priority = first to delete)
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Priority < candidates[j].Priority
	})

	// Clean up clips until we reach target threshold
	var totalFreed uint64
	var cleanedCount int

	for _, candidate := range candidates {
		if totalFreed >= bytesToFree {
			break // Reached target
		}

		if err := cm.cleanupClip(candidate); err != nil {
			if errors.Is(err, errCleanupSkip) {
				continue
			}
			cm.logger.WithError(err).WithField("clip_hash", candidate.ClipHash).Error("Failed to cleanup clip")
			continue
		}

		totalFreed += candidate.SizeBytes
		cleanedCount++

		cm.logger.WithFields(logging.Fields{
			"clip_hash":      candidate.ClipHash,
			"size_mb":        float64(candidate.SizeBytes) / (1024 * 1024),
			"total_freed_mb": float64(totalFreed) / (1024 * 1024),
		}).Info("Cleaned up clip")
	}

	// Final usage check
	finalUsagePercent, finalUsedBytes, _, err := cm.getStorageUsage(clipsDir)
	if err != nil {
		cm.logger.WithError(err).Error("Failed to check final storage usage")
	} else {
		cm.logger.WithFields(logging.Fields{
			"cleaned_count": cleanedCount,
			"freed_gb":      float64(totalFreed) / (1024 * 1024 * 1024),
			"initial_usage": usagePercent,
			"final_usage":   finalUsagePercent,
			"final_used_gb": float64(finalUsedBytes) / (1024 * 1024 * 1024),
		}).Info("Cleanup completed")
	}

	return nil
}

// getStorageUsage returns storage usage percentage, used bytes, and total bytes
func (cm *CleanupMonitor) getStorageUsage(path string) (float64, uint64, uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0, 0, err
	}

	totalBytes := stat.Blocks * uint64(stat.Bsize)
	freeBytes := stat.Bavail * uint64(stat.Bsize)
	usedBytes := totalBytes - freeBytes

	usagePercent := float64(usedBytes) / float64(totalBytes)

	return usagePercent, usedBytes, totalBytes, nil
}

// getCleanupCandidates returns clips that are candidates for cleanup
func (cm *CleanupMonitor) getCleanupCandidates(clipsDir string) ([]ClipCleanupInfo, error) {
	var candidates []ClipCleanupInfo
	minAge := time.Now().Add(-time.Duration(cm.minRetentionHours) * time.Hour)

	// Get current artifact index for access information
	if prometheusMonitor == nil {
		return nil, fmt.Errorf("prometheus monitor not initialized")
	}

	prometheusMonitor.mutex.RLock()
	artifactIndex := make(map[string]*ClipInfo)
	for k, v := range prometheusMonitor.artifactIndex {
		artifactIndex[k] = v
	}
	prometheusMonitor.mutex.RUnlock()

	// Walk through clips and build candidate list
	err := filepath.Walk(clipsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			//nolint:nilerr // skip errors, continue walking
			return nil
		}

		if info.IsDir() {
			return nil // Skip directories
		}

		// Check if this is a clip file
		if !cm.isClipFile(path) {
			return nil
		}

		// Extract clip hash from path
		clipHash := cm.extractClipHashFromPath(path)
		if clipHash == "" {
			return nil // Couldn't extract hash, skip
		}

		// Check minimum age
		if info.ModTime().After(minAge) {
			return nil // Too young to delete
		}

		// Build candidate info
		candidate := ClipCleanupInfo{
			ClipHash:     clipHash,
			FilePath:     path,
			SizeBytes:    uint64(info.Size()),
			CreatedAt:    info.ModTime(),
			AccessCount:  0,
			LastAccessed: info.ModTime(), // Default to creation time
		}

		// Get additional info from artifact index if available
		if clipInfo, exists := artifactIndex[clipHash]; exists {
			// Use creation time from file system as it's more reliable
			candidate.CreatedAt = clipInfo.CreatedAt
			candidate.AccessCount = clipInfo.AccessCount
			if !clipInfo.LastAccessed.IsZero() {
				candidate.LastAccessed = clipInfo.LastAccessed
			}
		}

		// Calculate cleanup priority (lower = higher priority for deletion)
		candidate.Priority = cm.calculateCleanupPriority(candidate)

		candidates = append(candidates, candidate)
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to walk clips directory: %w", err)
	}

	cm.logger.WithField("candidate_count", len(candidates)).Debug("Found cleanup candidates")
	return candidates, nil
}

// isClipFile checks if a file path represents a clip file
func (cm *CleanupMonitor) isClipFile(path string) bool {
	ext := filepath.Ext(path)
	switch ext {
	case ".mp4", ".webm", ".mkv", ".avi":
		return true
	default:
		return false
	}
}

// extractClipHashFromPath extracts clip hash from file path
func (cm *CleanupMonitor) extractClipHashFromPath(path string) string {
	filename := filepath.Base(path)
	ext := filepath.Ext(filename)

	// Remove extension
	name := filename[:len(filename)-len(ext)]

	// Check if this looks like a clip hash (32 characters)
	if len(name) == 32 {
		return name
	}

	return ""
}

// calculateCleanupPriority calculates cleanup priority for a clip
// Lower priority = higher likelihood of deletion
func (cm *CleanupMonitor) calculateCleanupPriority(clip ClipCleanupInfo) float64 {
	now := time.Now()

	// Age factor: older clips have lower priority (more likely to be deleted)
	ageHours := now.Sub(clip.CreatedAt).Hours()
	ageFactor := ageHours / 24.0 // Convert to days

	// Size factor: larger clips have slightly lower priority
	sizeMB := float64(clip.SizeBytes) / (1024 * 1024)
	sizeFactor := sizeMB / 100.0 // Normalize to ~100MB

	// Access factor: less accessed clips have lower priority
	accessFactor := float64(clip.AccessCount + 1) // +1 to avoid division by zero

	// Recent access factor: recently accessed clips have higher priority
	lastAccessHours := now.Sub(clip.LastAccessed).Hours()
	recentAccessFactor := 1.0
	if lastAccessHours < 24 {
		recentAccessFactor = 10.0 // Much higher priority if accessed in last 24h
	} else if lastAccessHours < 168 { // 7 days
		recentAccessFactor = 2.0
	}

	// Combined priority: lower = more likely to be deleted
	// Formula: (age + size) / (access_count * recent_access_bonus)
	priority := (ageFactor + sizeFactor*0.1) / (accessFactor * recentAccessFactor)

	return priority
}

// cleanupClip removes a clip file from storage
func (cm *CleanupMonitor) cleanupClip(clip ClipCleanupInfo) error {
	// In dual-storage mode, clips are expected to have an authoritative S3 copy.
	// Before evicting from local disk, ask Foghorn if it's safe (synced).
	// If it's not safe, trigger a storage check (which will sync via presigned URLs)
	// and skip local deletion for now.
	isEviction := false
	var warmDurationMs int64
	if control.IsConnected() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		safeToDelete, reason, wd, err := control.RequestCanDelete(ctx, clip.ClipHash)
		cancel()

		if err != nil {
			cm.logger.WithError(err).WithField("clip_hash", clip.ClipHash).Warn("Failed to check if clip is safe to evict")
			return errCleanupSkip
		}
		if !safeToDelete {
			cm.logger.WithFields(logging.Fields{
				"clip_hash": clip.ClipHash,
				"reason":    reason,
			}).Info("Clip not safe to evict yet; triggering sync")
			TriggerStorageCheck()
			return errCleanupSkip
		}

		isEviction = true
		warmDurationMs = wd
	}

	// Remove the file
	if err := os.Remove(clip.FilePath); err != nil {
		return fmt.Errorf("failed to remove clip file: %w", err)
	}

	// Try to remove any VOD symlink if it exists
	clipsDir := filepath.Join(cm.basePath, "clips")
	vodLinkPath := filepath.Join(clipsDir, clip.ClipHash+filepath.Ext(clip.FilePath))
	if _, err := os.Lstat(vodLinkPath); err == nil {
		if err := os.Remove(vodLinkPath); err != nil {
			cm.logger.WithError(err).WithField("vod_path", vodLinkPath).Warn("Failed to remove VOD symlink")
		}
	}

	// Remove auxiliary files (.dtsh, .gop)
	os.Remove(clip.FilePath + ".dtsh")
	os.Remove(clip.FilePath + ".gop")

	// Remove from artifact index
	if prometheusMonitor != nil {
		prometheusMonitor.mutex.Lock()
		delete(prometheusMonitor.artifactIndex, clip.ClipHash)
		prometheusMonitor.mutex.Unlock()
	}

	// Notify Foghorn about the deletion
	if isEviction {
		_ = control.SendStorageLifecycle(&pb.StorageLifecycleData{
			Action:         pb.StorageLifecycleData_ACTION_EVICTED,
			AssetType:      string(AssetTypeClip),
			AssetHash:      clip.ClipHash,
			SizeBytes:      clip.SizeBytes,
			WarmDurationMs: &warmDurationMs,
		})
		if err := control.SendArtifactDeleted(clip.ClipHash, clip.FilePath, "eviction", clip.SizeBytes); err != nil {
			cm.logger.WithError(err).WithField("clip_hash", clip.ClipHash).Warn("Failed to notify Foghorn of clip eviction")
		}
	} else {
		if err := control.SendArtifactDeleted(clip.ClipHash, clip.FilePath, "cleanup", clip.SizeBytes); err != nil {
			cm.logger.WithError(err).WithField("clip_hash", clip.ClipHash).Warn("Failed to notify Foghorn of artifact deletion")
		}
	}

	return nil
}
