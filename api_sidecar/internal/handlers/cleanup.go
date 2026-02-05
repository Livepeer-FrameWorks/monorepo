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

// ClipCleanupInfo holds information about an artifact candidate for cleanup
type ClipCleanupInfo struct {
	ClipHash     string
	FilePath     string
	SizeBytes    uint64
	CreatedAt    time.Time
	AccessCount  int
	LastAccessed time.Time
	Priority     float64 // Lower = higher priority for deletion
	AssetType    string  // "clip", "dvr", or "vod"
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
	// Get current storage usage (check base path - filesystem level)
	usagePercent, usedBytes, totalBytes, err := cm.getStorageUsage(cm.basePath)
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

	// Get cleanup candidates from all artifact directories
	var candidates []ClipCleanupInfo

	clipsDir := filepath.Join(cm.basePath, "clips")
	if clipCandidates, clipErr := cm.getCleanupCandidates(clipsDir, "clip"); clipErr == nil {
		candidates = append(candidates, clipCandidates...)
	}

	dvrDir := filepath.Join(cm.basePath, "dvr")
	if dvrCandidates, dvrErr := cm.getCleanupCandidates(dvrDir, "dvr"); dvrErr == nil {
		candidates = append(candidates, dvrCandidates...)
	}

	vodDir := filepath.Join(cm.basePath, "vod")
	if vodCandidates, vodErr := cm.getCleanupCandidates(vodDir, "vod"); vodErr == nil {
		candidates = append(candidates, vodCandidates...)
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
			cm.logger.WithError(err).WithFields(logging.Fields{
				"artifact_hash": candidate.ClipHash,
				"asset_type":    candidate.AssetType,
			}).Error("Failed to cleanup artifact")
			continue
		}

		totalFreed += candidate.SizeBytes
		cleanedCount++

		cm.logger.WithFields(logging.Fields{
			"artifact_hash":  candidate.ClipHash,
			"asset_type":     candidate.AssetType,
			"size_mb":        float64(candidate.SizeBytes) / (1024 * 1024),
			"total_freed_mb": float64(totalFreed) / (1024 * 1024),
		}).Info("Cleaned up artifact")
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

// getCleanupCandidates returns artifacts that are candidates for cleanup
func (cm *CleanupMonitor) getCleanupCandidates(dir string, assetType string) ([]ClipCleanupInfo, error) {
	// Check if directory exists
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, nil // Directory doesn't exist, no candidates
	}

	var candidates []ClipCleanupInfo
	minAge := time.Now().Add(-time.Duration(cm.minRetentionHours) * time.Hour)

	// Get current artifact index for access information
	var artifactIndex map[string]*ClipInfo
	if prometheusMonitor != nil {
		prometheusMonitor.mutex.RLock()
		artifactIndex = make(map[string]*ClipInfo)
		for k, v := range prometheusMonitor.artifactIndex {
			artifactIndex[k] = v
		}
		prometheusMonitor.mutex.RUnlock()
	}

	// Walk through directory and build candidate list
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			//nolint:nilerr // skip errors, continue walking
			return nil
		}

		if info.IsDir() {
			return nil // Skip directories
		}

		// Check if this is a valid artifact file for the asset type
		if !cm.isArtifactFile(path, assetType) {
			return nil
		}

		// Extract artifact hash from path
		artifactHash := cm.extractArtifactHashFromPath(path, assetType)
		if artifactHash == "" {
			return nil // Couldn't extract hash, skip
		}

		// Check minimum age
		if info.ModTime().After(minAge) {
			return nil // Too young to delete
		}

		// Build candidate info
		candidate := ClipCleanupInfo{
			ClipHash:     artifactHash,
			FilePath:     path,
			SizeBytes:    uint64(info.Size()),
			CreatedAt:    info.ModTime(),
			AccessCount:  0,
			LastAccessed: info.ModTime(), // Default to creation time
			AssetType:    assetType,
		}

		// Get additional info from artifact index if available
		if artifactIndex != nil {
			if clipInfo, exists := artifactIndex[artifactHash]; exists {
				candidate.CreatedAt = clipInfo.CreatedAt
				candidate.AccessCount = clipInfo.AccessCount
				if !clipInfo.LastAccessed.IsZero() {
					candidate.LastAccessed = clipInfo.LastAccessed
				}
			}
		}

		// Calculate cleanup priority (lower = higher priority for deletion)
		candidate.Priority = cm.calculateCleanupPriority(candidate)

		candidates = append(candidates, candidate)
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to walk %s directory: %w", assetType, err)
	}

	cm.logger.WithFields(logging.Fields{
		"asset_type":      assetType,
		"candidate_count": len(candidates),
	}).Debug("Found cleanup candidates")
	return candidates, nil
}

// isArtifactFile checks if a file path represents a valid artifact file for the given type
func (cm *CleanupMonitor) isArtifactFile(path string, assetType string) bool {
	ext := filepath.Ext(path)
	switch assetType {
	case "clip", "vod":
		return IsVideoFile(ext)
	case "dvr":
		return ext == ".m3u8"
	}
	return false
}

// extractArtifactHashFromPath extracts artifact hash from file path based on asset type
func (cm *CleanupMonitor) extractArtifactHashFromPath(path string, assetType string) string {
	filename := filepath.Base(path)
	ext := filepath.Ext(filename)

	// Remove extension
	name := filename[:len(filename)-len(ext)]

	// All artifact hashes are timestamp(14) + hex(8-16) = 22-30 chars
	// Use minimum of 18 to handle any format variations
	if len(name) >= 18 {
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

// cleanupClip removes an artifact from storage (clip, dvr, or vod)
func (cm *CleanupMonitor) cleanupClip(artifact ClipCleanupInfo) error {
	// In dual-storage mode, artifacts are expected to have an authoritative S3 copy.
	// Before evicting from local disk, ask Foghorn if it's safe (synced).
	if !control.IsConnected() {
		cm.logger.WithFields(logging.Fields{
			"artifact_hash": artifact.ClipHash,
			"asset_type":    artifact.AssetType,
		}).Warn("Cleanup skipped: Foghorn disconnected, cannot verify sync status")
		return errCleanupSkip
	}
	isEviction := false
	var warmDurationMs int64
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	safeToDelete, reason, wd, err := control.RequestCanDelete(ctx, artifact.ClipHash)
	cancel()

	if err != nil {
		cm.logger.WithError(err).WithField("artifact_hash", artifact.ClipHash).Warn("Failed to check if artifact is safe to evict")
		return errCleanupSkip
	}
	if !safeToDelete {
		cm.logger.WithFields(logging.Fields{
			"artifact_hash": artifact.ClipHash,
			"asset_type":    artifact.AssetType,
			"reason":        reason,
		}).Info("Artifact not safe to evict yet; triggering sync")
		TriggerStorageCheck()
		return errCleanupSkip
	}

	isEviction = true
	warmDurationMs = wd
	assetType := artifact.AssetType
	if assetType == "" {
		assetType = "clip"
	}

	// Remove based on asset type
	switch artifact.AssetType {
	case "dvr":
		// DVR is a directory - remove the entire directory
		dvrDir := filepath.Dir(artifact.FilePath)
		if err := os.RemoveAll(dvrDir); err != nil {
			errStr := err.Error()
			_ = control.SendStorageLifecycle(&pb.StorageLifecycleData{
				Action:    pb.StorageLifecycleData_ACTION_EVICT_FAILED,
				AssetType: assetType,
				AssetHash: artifact.ClipHash,
				SizeBytes: artifact.SizeBytes,
				Error:     &errStr,
			})
			return fmt.Errorf("failed to remove dvr directory: %w", err)
		}
	default:
		// Clip and VOD are single files
		if err := os.Remove(artifact.FilePath); err != nil {
			errStr := err.Error()
			_ = control.SendStorageLifecycle(&pb.StorageLifecycleData{
				Action:    pb.StorageLifecycleData_ACTION_EVICT_FAILED,
				AssetType: assetType,
				AssetHash: artifact.ClipHash,
				SizeBytes: artifact.SizeBytes,
				Error:     &errStr,
			})
			return fmt.Errorf("failed to remove artifact file: %w", err)
		}
		// Remove auxiliary files (.dtsh, .gop) after main file succeeds.
		_ = os.Remove(artifact.FilePath + ".dtsh")
		_ = os.Remove(artifact.FilePath + ".gop")
	}

	// Remove from artifact index
	if prometheusMonitor != nil {
		prometheusMonitor.mutex.Lock()
		delete(prometheusMonitor.artifactIndex, artifact.ClipHash)
		prometheusMonitor.mutex.Unlock()
	}

	// Notify Foghorn about the deletion
	if isEviction {
		_ = control.SendStorageLifecycle(&pb.StorageLifecycleData{
			Action:         pb.StorageLifecycleData_ACTION_EVICTED,
			AssetType:      assetType,
			AssetHash:      artifact.ClipHash,
			SizeBytes:      artifact.SizeBytes,
			WarmDurationMs: &warmDurationMs,
		})
		if err := control.SendArtifactDeleted(artifact.ClipHash, artifact.FilePath, "eviction", artifact.AssetType, artifact.SizeBytes); err != nil {
			cm.logger.WithError(err).WithField("artifact_hash", artifact.ClipHash).Warn("Failed to notify Foghorn of eviction")
		}
	} else {
		if err := control.SendArtifactDeleted(artifact.ClipHash, artifact.FilePath, "cleanup", artifact.AssetType, artifact.SizeBytes); err != nil {
			cm.logger.WithError(err).WithField("artifact_hash", artifact.ClipHash).Warn("Failed to notify Foghorn of deletion")
		}
	}

	return nil
}
