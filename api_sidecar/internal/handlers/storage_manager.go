package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"frameworks/api_sidecar/internal/control"
	"frameworks/api_sidecar/internal/storage"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"
)

// NOTE: This storage manager uses presigned URLs for S3 operations.
// S3 credentials are held by Foghorn (trusted infrastructure) only.
// Helmsman (untrusted edge) receives time-limited presigned URLs.

// AssetType represents the type of storage asset
type AssetType string

const (
	AssetTypeClip AssetType = "clip"
	AssetTypeDVR  AssetType = "dvr"
	AssetTypeVOD  AssetType = "vod"
)

// FreezeCandidate holds information about an asset candidate for freezing
type FreezeCandidate struct {
	AssetType    AssetType
	AssetHash    string
	TenantID     string
	StreamID     string // Stream UUID (from directory structure)
	FilePath     string // For clips: file path; for DVR: directory path
	SizeBytes    uint64
	CreatedAt    time.Time
	AccessCount  int
	LastAccessed time.Time
	Priority     float64 // Lower = higher priority for freezing
}

// DefrostJob tracks an in-flight defrost operation
type DefrostJob struct {
	RequestID string
	AssetHash string
	AssetType AssetType
	StartedAt time.Time
	Done      chan struct{}
	closeOnce sync.Once // Prevents double-close panic
	Err       error
	LocalPath string
	SizeBytes uint64
	Waiters   int32 // Number of concurrent requests waiting
}

// DefrostProgress tracks progress of a DVR streaming defrost
type DefrostProgress struct {
	DVRHash           string   `json:"dvr_hash"`
	TotalSegments     int      `json:"total_segments"`
	CompletedSegments []string `json:"completed_segments"`
	StartedAt         int64    `json:"started_at"`
	LastUpdated       int64    `json:"last_updated"`
}

// ParsedManifest holds data extracted from an HLS manifest
type ParsedManifest struct {
	TargetDuration int
	Segments       []ParsedSegment
}

// ParsedSegment holds a single segment's metadata from the manifest
type ParsedSegment struct {
	Name     string
	Duration float64
}

// StorageManager manages cold storage operations (freeze/defrost)
type StorageManager struct {
	logger   logging.Logger
	basePath string
	nodeID   string
	running  bool
	stopCh   chan struct{}

	// Presigned URL client (NO S3 credentials - uses presigned URLs from Foghorn)
	presignedClient *storage.PresignedClient

	// Thresholds
	freezeThreshold   float64       // Start freezing at this % (default: 85%)
	targetThreshold   float64       // Target usage after freeze (default: 70%)
	deleteThreshold   float64       // Delete even frozen assets if above this % (default: 95%)
	minRetentionHours int           // Never freeze assets younger than this
	checkInterval     time.Duration // Normal polling interval

	// Hybrid trigger mechanism
	urgentFreezeCh  chan struct{}
	lastUrgentCheck time.Time
	urgentDebounce  time.Duration

	// Defrost tracking
	defrostTracker struct {
		mu       sync.RWMutex
		inFlight map[string]*DefrostJob
	}

	// Freeze tracking
	freezeTracker struct {
		mu       sync.RWMutex
		inFlight map[string]bool // assetHash -> true if freezing
	}
}

var (
	storageManager *StorageManager
	storageLogger  logging.Logger
)

// InitStorageManager initializes the storage manager for cold storage operations.
// NOTE: S3 credentials are held by Foghorn, not here. We use presigned URLs.
func InitStorageManager(logger logging.Logger, basePath, nodeID string, thresholds StorageThresholds) error {
	if storageManager != nil {
		return nil // Already initialized
	}

	storageLogger = logger

	// Create presigned URL client (no S3 credentials needed!)
	presignedClient := storage.NewPresignedClient(logger)

	storageManager = &StorageManager{
		logger:            logger,
		basePath:          basePath,
		nodeID:            nodeID,
		running:           false,
		stopCh:            make(chan struct{}),
		presignedClient:   presignedClient,
		freezeThreshold:   thresholds.FreezeThreshold,
		targetThreshold:   thresholds.TargetThreshold,
		deleteThreshold:   0.95, // 95%
		minRetentionHours: 1,
		checkInterval:     5 * time.Minute,
		urgentFreezeCh:    make(chan struct{}, 1),
		urgentDebounce:    2 * time.Second,
	}

	storageManager.defrostTracker.inFlight = make(map[string]*DefrostJob)
	storageManager.freezeTracker.inFlight = make(map[string]bool)

	// Start monitoring in background
	go storageManager.start()

	// Register handlers for cold storage operations from Foghorn
	control.SetDefrostRequestHandler(func(req *pb.DefrostRequest) {
		ctx := context.Background()
		if req.GetAssetType() == "clip" {
			_, _ = storageManager.DefrostClip(ctx, req)
		} else if req.GetAssetType() == "dvr" {
			_, _ = storageManager.DefrostDVR(ctx, req)
		} else if req.GetAssetType() == "vod" {
			_, _ = storageManager.DefrostVOD(ctx, req)
		}
	})

	control.SetDtshSyncRequestHandler(func(req *pb.DtshSyncRequest) {
		ctx := context.Background()
		_ = storageManager.SyncDtshOnly(ctx, req)
	})

	logger.WithFields(logging.Fields{
		"base_path":        basePath,
		"node_id":          nodeID,
		"freeze_threshold": storageManager.freezeThreshold,
		"target_threshold": storageManager.targetThreshold,
		"check_interval":   storageManager.checkInterval,
	}).Info("Storage manager initialized (presigned URL mode)")

	return nil
}

// StorageThresholds holds configurable thresholds for storage management
type StorageThresholds struct {
	FreezeThreshold float64
	TargetThreshold float64
}

// StopStorageManager stops the storage manager
func StopStorageManager() {
	if storageManager != nil && storageManager.running {
		close(storageManager.stopCh)
		storageLogger.Info("Storage manager stopped")
	}
}

// TriggerStorageCheck triggers an urgent storage check (debounced)
// Call this after writing new clips or DVR segments
func TriggerStorageCheck() {
	if storageManager == nil || !storageManager.running {
		return
	}
	select {
	case storageManager.urgentFreezeCh <- struct{}{}:
	default:
		// Already pending
	}
}

// start begins the storage management loop with hybrid triggering
func (sm *StorageManager) start() {
	sm.running = true
	ticker := time.NewTicker(sm.checkInterval)
	defer ticker.Stop()

	sm.logger.Info("Storage manager started")

	for {
		select {
		case <-sm.stopCh:
			sm.running = false
			return
		case <-ticker.C:
			if err := sm.checkAndManageStorage(); err != nil {
				sm.logger.WithError(err).Error("Storage management check failed")
			}
		case <-sm.urgentFreezeCh:
			// Debounce urgent checks
			if time.Since(sm.lastUrgentCheck) < sm.urgentDebounce {
				continue
			}
			sm.lastUrgentCheck = time.Now()
			sm.logger.Info("Urgent storage check triggered")
			if err := sm.checkAndManageStorage(); err != nil {
				sm.logger.WithError(err).Error("Urgent storage management check failed")
			}
		}
	}
}

// checkAndManageStorage checks storage usage and performs freeze/cleanup if needed
func (sm *StorageManager) checkAndManageStorage() error {
	// Check clips directory
	clipsDir := filepath.Join(sm.basePath, "clips")
	dvrDir := filepath.Join(sm.basePath, "dvr")
	vodDir := filepath.Join(sm.basePath, "vod")

	// Get current storage usage
	usagePercent, usedBytes, totalBytes, err := sm.getStorageUsage(sm.basePath)
	if err != nil {
		return fmt.Errorf("failed to get storage usage: %w", err)
	}

	sm.logger.WithFields(logging.Fields{
		"usage_percent": usagePercent,
		"used_gb":       float64(usedBytes) / (1024 * 1024 * 1024),
		"total_gb":      float64(totalBytes) / (1024 * 1024 * 1024),
	}).Info("Storage usage check")

	if usagePercent >= sm.deleteThreshold {
		sm.logger.WithFields(logging.Fields{
			"usage_percent":    usagePercent,
			"delete_threshold": sm.deleteThreshold,
		}).Warn("Storage above delete threshold, starting emergency cleanup")
		return sm.fallbackCleanup(clipsDir, usedBytes, totalBytes)
	}

	// Check if freeze is needed
	if usagePercent < sm.freezeThreshold {
		return nil // No action needed
	}

	// Check if cold storage is available (requires Foghorn connection)
	if !control.IsConnected() {
		sm.logger.Warn("Storage above threshold but Foghorn not connected, falling back to cleanup")
		return sm.fallbackCleanup(clipsDir, usedBytes, totalBytes)
	}

	sm.logger.WithFields(logging.Fields{
		"usage_percent":    usagePercent,
		"freeze_threshold": sm.freezeThreshold,
	}).Info("Storage usage above threshold, starting freeze operation")

	// Calculate how much space we need to free
	targetBytes := uint64(float64(totalBytes) * sm.targetThreshold)
	bytesToFree := usedBytes - targetBytes

	// Get freeze candidates from clips and DVR
	var candidates []FreezeCandidate

	clipCandidates, err := sm.getFreezeCandidates(clipsDir, AssetTypeClip)
	if err != nil {
		sm.logger.WithError(err).Warn("Failed to get clip freeze candidates")
	} else {
		candidates = append(candidates, clipCandidates...)
	}

	dvrCandidates, err := sm.getFreezeCandidates(dvrDir, AssetTypeDVR)
	if err != nil {
		sm.logger.WithError(err).Warn("Failed to get DVR freeze candidates")
	} else {
		candidates = append(candidates, dvrCandidates...)
	}

	vodCandidates, err := sm.getFreezeCandidates(vodDir, AssetTypeVOD)
	if err != nil {
		sm.logger.WithError(err).Warn("Failed to get VOD freeze candidates")
	} else {
		candidates = append(candidates, vodCandidates...)
	}

	if len(candidates) == 0 {
		sm.logger.Warn("No freeze candidates found despite high storage usage")
		return nil
	}

	// Sort candidates by priority (lowest = first to freeze)
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Priority < candidates[j].Priority
	})

	// Freeze assets until we reach target threshold
	var totalFreed uint64
	var frozenCount int

	for _, candidate := range candidates {
		if totalFreed >= bytesToFree {
			break
		}

		// Skip if already being frozen
		sm.freezeTracker.mu.RLock()
		alreadyFreezing := sm.freezeTracker.inFlight[candidate.AssetHash]
		sm.freezeTracker.mu.RUnlock()
		if alreadyFreezing {
			continue
		}

		if err := sm.freezeAsset(context.Background(), candidate); err != nil {
			sm.logger.WithError(err).WithField("asset_hash", candidate.AssetHash).Error("Failed to freeze asset")
			continue
		}

		totalFreed += candidate.SizeBytes
		frozenCount++
	}

	sm.logger.WithFields(logging.Fields{
		"frozen_count":  frozenCount,
		"freed_gb":      float64(totalFreed) / (1024 * 1024 * 1024),
		"initial_usage": usagePercent,
	}).Info("Freeze operation completed")

	return nil
}

// getFreezeCandidates returns assets that are candidates for freezing
func (sm *StorageManager) getFreezeCandidates(dir string, assetType AssetType) ([]FreezeCandidate, error) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, nil
	}

	var candidates []FreezeCandidate
	minAge := time.Now().Add(-time.Duration(sm.minRetentionHours) * time.Hour)

	if assetType == AssetTypeClip {
		// Walk clips directory
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil //nolint:nilerr // skip errors, continue walking
			}
			if !sm.isClipFile(path) {
				return nil
			}

			clipHash := sm.extractHashFromPath(path)
			if clipHash == "" || info.ModTime().After(minAge) {
				return nil
			}

			candidate := FreezeCandidate{
				AssetType:    AssetTypeClip,
				AssetHash:    clipHash,
				FilePath:     path,
				SizeBytes:    uint64(info.Size()),
				CreatedAt:    info.ModTime(),
				LastAccessed: info.ModTime(),
			}
			candidate.Priority = sm.calculateFreezePriority(candidate)
			candidates = append(candidates, candidate)
			return nil
		})
		if err != nil {
			return nil, err
		}
	} else if assetType == AssetTypeDVR {
		// DVR directories are structured as dvr/{internal_name}/{dvr_hash}/
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, nil //nolint:nilerr // directory missing = no candidates
		}

		for _, streamDir := range entries {
			if !streamDir.IsDir() {
				continue
			}
			streamPath := filepath.Join(dir, streamDir.Name())
			dvrEntries, err := os.ReadDir(streamPath)
			if err != nil {
				continue
			}

			for _, dvrDir := range dvrEntries {
				if !dvrDir.IsDir() {
					continue
				}
				dvrPath := filepath.Join(streamPath, dvrDir.Name())
				info, err := dvrDir.Info()
				if err != nil {
					continue
				}

				// Check minimum age
				if info.ModTime().After(minAge) {
					continue
				}

				// Calculate total size of DVR directory
				dvrSize := sm.calculateDirSize(dvrPath)

				candidate := FreezeCandidate{
					AssetType:    AssetTypeDVR,
					AssetHash:    dvrDir.Name(),
					StreamID:     streamDir.Name(),
					FilePath:     dvrPath,
					SizeBytes:    dvrSize,
					CreatedAt:    info.ModTime(),
					LastAccessed: info.ModTime(),
				}
				candidate.Priority = sm.calculateFreezePriority(candidate)
				candidates = append(candidates, candidate)
			}
		}
	} else if assetType == AssetTypeVOD {
		// VOD files are stored as vod/{assetHash}.{format}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, nil //nolint:nilerr // directory missing = no candidates
		}

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}

			filename := entry.Name()
			ext := filepath.Ext(filename)
			if ext == "" {
				continue
			}

			// Extract hash from filename (remove extension)
			vodHash := strings.TrimSuffix(filename, ext)
			if len(vodHash) < 18 {
				continue // Not a valid artifact hash
			}

			info, err := entry.Info()
			if err != nil || info.ModTime().After(minAge) {
				continue
			}

			candidate := FreezeCandidate{
				AssetType:    AssetTypeVOD,
				AssetHash:    vodHash,
				FilePath:     filepath.Join(dir, filename),
				SizeBytes:    uint64(info.Size()),
				CreatedAt:    info.ModTime(),
				LastAccessed: info.ModTime(),
			}
			candidate.Priority = sm.calculateFreezePriority(candidate)
			candidates = append(candidates, candidate)
		}
	}

	return candidates, nil
}

// freezeAsset uploads an asset to S3 via presigned URLs and deletes local copy
// Flow: Helmsman requests presigned URL from Foghorn → uploads directly → notifies completion
func (sm *StorageManager) freezeAsset(ctx context.Context, asset FreezeCandidate) error {
	// Mark as freezing
	sm.freezeTracker.mu.Lock()
	sm.freezeTracker.inFlight[asset.AssetHash] = true
	sm.freezeTracker.mu.Unlock()

	defer func() {
		sm.freezeTracker.mu.Lock()
		delete(sm.freezeTracker.inFlight, asset.AssetHash)
		sm.freezeTracker.mu.Unlock()
	}()

	// Collect filenames (needed for presigned URL generation)
	var filenames []string
	if asset.AssetType == AssetTypeDVR {
		manifestName := asset.AssetHash + ".m3u8"
		filenames = append(filenames, manifestName)

		// Parse manifest to get segment names
		localManifestPath := filepath.Join(asset.FilePath, manifestName)
		localManifestContent, err := os.ReadFile(localManifestPath)
		if err == nil {
			parsedManifest, err := parseHLSManifest(string(localManifestContent))
			if err == nil {
				for _, seg := range parsedManifest.Segments {
					filenames = append(filenames, seg.Name)
				}
			} else {
				sm.logger.WithError(err).Warn("Failed to parse DVR manifest for freeze")
			}
		} else {
			sm.logger.WithError(err).Warn("Failed to read DVR manifest for freeze")
		}

		// Also check for any .dtsh files in the DVR directory
		entries, _ := os.ReadDir(asset.FilePath)
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".dtsh") {
				filenames = append(filenames, entry.Name())
			}
		}
	} else if asset.AssetType == AssetTypeClip || asset.AssetType == AssetTypeVOD {
		// Clip and VOD are single-file uploads
		filenames = append(filenames, filepath.Base(asset.FilePath))
		// Include .dtsh if it exists
		if _, err := os.Stat(asset.FilePath + ".dtsh"); err == nil {
			filenames = append(filenames, filepath.Base(asset.FilePath)+".dtsh")
		}
	}

	// Request permission and presigned URL from Foghorn
	// This is a blocking call that waits for Foghorn's response
	permResp, err := control.RequestFreezePermission(ctx, string(asset.AssetType), asset.AssetHash, asset.FilePath, asset.SizeBytes, filenames)
	if err != nil {
		return fmt.Errorf("failed to get freeze permission: %w", err)
	}

	if !permResp.Approved {
		reason := permResp.Reason
		if reason == "" {
			reason = "unknown"
		}
		return fmt.Errorf("freeze not approved: %s", reason)
	}

	requestID := permResp.RequestId

	// Notify sync started
	_ = control.SendStorageLifecycle(&pb.StorageLifecycleData{
		Action:    pb.StorageLifecycleData_ACTION_SYNC_STARTED,
		AssetType: string(asset.AssetType),
		AssetHash: asset.AssetHash,
		TenantId:  &asset.TenantID,
		StreamId:  &asset.StreamID,
		SizeBytes: asset.SizeBytes,
	})

	startTime := time.Now()
	var uploadErr error
	dtshIncluded := false // Track whether .dtsh was successfully uploaded

	if asset.AssetType == AssetTypeClip || asset.AssetType == AssetTypeVOD {
		// Clip and VOD are single-file uploads
		// Check for SegmentUrls first (multi-file support for clip/vod + dtsh)
		if len(permResp.SegmentUrls) > 0 {
			baseName := filepath.Base(asset.FilePath)

			// Upload main file
			if url, ok := permResp.SegmentUrls[baseName]; ok {
				err := sm.presignedClient.UploadFileToPresignedURL(ctx, url, asset.FilePath, func(uploaded int64) {
					// Only track main file progress for now
					percent := uint32((uploaded * 100) / int64(asset.SizeBytes))
					_ = control.SendFreezeProgress(requestID, asset.AssetHash, percent, uint64(uploaded))
				})
				if err != nil {
					uploadErr = fmt.Errorf("failed to upload %s: %w", asset.AssetType, err)
				}
			} else {
				uploadErr = fmt.Errorf("no URL provided for main %s file", asset.AssetType)
			}

			// Upload .dtsh if exists and URL provided
			dtshName := baseName + ".dtsh"
			if url, ok := permResp.SegmentUrls[dtshName]; ok && uploadErr == nil {
				dtshPath := asset.FilePath + ".dtsh"
				if err := sm.presignedClient.UploadFileToPresignedURL(ctx, url, dtshPath, nil); err != nil {
					sm.logger.WithError(err).Warn("Failed to upload .dtsh file")
					// Non-fatal - dtshIncluded stays false
				} else {
					dtshIncluded = true
				}
			}
		} else {
			// Legacy/Single file fallback using presigned PUT URL
			presignedURL := permResp.PresignedPutUrl
			if presignedURL == "" {
				return fmt.Errorf("no presigned URL provided for %s freeze", asset.AssetType)
			}

			uploadErr = sm.presignedClient.UploadFileToPresignedURL(ctx, presignedURL, asset.FilePath, func(uploaded int64) {
				percent := uint32((uploaded * 100) / int64(asset.SizeBytes))
				_ = control.SendFreezeProgress(requestID, asset.AssetHash, percent, uint64(uploaded))
			})
		}
	} else if asset.AssetType == AssetTypeDVR {
		// DVR: Stream upload with progressive manifest updates
		// This allows playback to begin from S3 before freeze completes
		segmentURLs := permResp.SegmentUrls
		if len(segmentURLs) == 0 {
			return fmt.Errorf("no segment URLs provided for DVR freeze")
		}

		// Parse local manifest for segment order and durations
		manifestName := asset.AssetHash + ".m3u8"
		localManifestPath := filepath.Join(asset.FilePath, manifestName)
		localManifestContent, err := os.ReadFile(localManifestPath)
		if err != nil {
			return fmt.Errorf("failed to read local DVR manifest: %w", err)
		}

		parsedManifest, err := parseHLSManifest(string(localManifestContent))
		if err != nil {
			return fmt.Errorf("failed to parse local DVR manifest: %w", err)
		}

		manifestURL, hasManifestURL := segmentURLs[manifestName]
		if !hasManifestURL {
			return fmt.Errorf("no presigned URL for manifest")
		}

		// Upload initial EVENT manifest (empty, no ENDLIST)
		initialManifest := sm.createLiveManifest(asset.AssetHash, parsedManifest.TargetDuration)
		manifestBytes := []byte(initialManifest)
		if err := sm.presignedClient.UploadToPresignedURL(ctx, manifestURL,
			bytes.NewReader(manifestBytes), int64(len(manifestBytes)), nil); err != nil {
			return fmt.Errorf("failed to upload initial manifest: %w", err)
		}

		// Upload segments in manifest order, updating manifest after each
		var totalUploaded int64
		var uploadedManifest strings.Builder
		uploadedManifest.WriteString(initialManifest)

		segmentsDir := filepath.Join(asset.FilePath, "segments")

		for _, seg := range parsedManifest.Segments {
			segPath := filepath.Join(segmentsDir, seg.Name)
			presignedURL, ok := segmentURLs[seg.Name]
			if !ok {
				sm.logger.WithField("segment", seg.Name).Warn("No presigned URL for segment, skipping")
				continue
			}

			// Upload segment
			if err := sm.presignedClient.UploadFileToPresignedURL(ctx, presignedURL, segPath, nil); err != nil {
				uploadErr = fmt.Errorf("failed to upload segment %s: %w", seg.Name, err)
				break
			}

			info, _ := os.Stat(segPath)
			if info != nil {
				totalUploaded += info.Size()
			}

			// Append to manifest buffer
			uploadedManifest.WriteString(fmt.Sprintf("#EXTINF:%.3f,\nsegments/%s\n", seg.Duration, seg.Name))

			// Re-upload updated manifest
			manifestData := []byte(uploadedManifest.String())
			if err := sm.presignedClient.UploadToPresignedURL(ctx, manifestURL,
				bytes.NewReader(manifestData), int64(len(manifestData)), nil); err != nil {
				sm.logger.WithError(err).Warn("Failed to update manifest during freeze")
			}

			percent := uint32((totalUploaded * 100) / int64(asset.SizeBytes))
			_ = control.SendFreezeProgress(requestID, asset.AssetHash, percent, uint64(totalUploaded))
		}

		// After segments, check for and upload any .dtsh files in the DVR directory
		if uploadErr == nil {
			entries, _ := os.ReadDir(asset.FilePath)
			for _, entry := range entries {
				if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".dtsh") {
					dtshName := entry.Name()
					if url, ok := segmentURLs[dtshName]; ok {
						dtshPath := filepath.Join(asset.FilePath, dtshName)
						if err := sm.presignedClient.UploadFileToPresignedURL(ctx, url, dtshPath, nil); err != nil {
							sm.logger.WithError(err).WithField("file", dtshName).Warn("Failed to upload DVR .dtsh file")
						} else {
							dtshIncluded = true
						}
					}
				}
			}
		}

		// Finalize manifest with ENDLIST (converts EVENT -> VOD)
		uploadedManifest.WriteString("#EXT-X-ENDLIST\n")
		finalManifest := []byte(uploadedManifest.String())
		if err := sm.presignedClient.UploadToPresignedURL(ctx, manifestURL,
			bytes.NewReader(finalManifest), int64(len(finalManifest)), nil); err != nil {
			sm.logger.WithError(err).Warn("Failed to finalize manifest")
		}
	} else {
		return fmt.Errorf("unsupported asset type for freeze: %s", asset.AssetType)
	}

	duration := time.Since(startTime)

	if uploadErr != nil {
		durationMs := duration.Milliseconds()
		errStr := uploadErr.Error()
		_ = control.SendStorageLifecycle(&pb.StorageLifecycleData{
			Action:     pb.StorageLifecycleData_ACTION_SYNCED, // Sync finished with error
			AssetType:  string(asset.AssetType),
			AssetHash:  asset.AssetHash,
			Error:      &errStr,
			DurationMs: &durationMs,
		})
		// Notify Foghorn of failure
		_ = control.SendFreezeComplete(requestID, asset.AssetHash, "failed", "", 0, uploadErr.Error())
		return fmt.Errorf("failed to upload to S3: %w", uploadErr)
	}

	// Dual-storage model: Keep local copy after sync (deletion handled separately by cleanup)
	// Local copy is retained as cache; S3 is authoritative backup
	// Old model deleted here; new model only evicts during cleanup when disk pressure requires it

	// Notify completion via lifecycle event (synced, not frozen - local copy retained)
	durationMs := duration.Milliseconds()
	_ = control.SendStorageLifecycle(&pb.StorageLifecycleData{
		Action:     pb.StorageLifecycleData_ACTION_SYNCED,
		AssetType:  string(asset.AssetType),
		AssetHash:  asset.AssetHash,
		TenantId:   &asset.TenantID,
		StreamId:   &asset.StreamID,
		SizeBytes:  asset.SizeBytes,
		DurationMs: &durationMs,
	})

	// Send SyncComplete to Foghorn (it will mark asset as synced and track this node as cached)
	_ = control.SendSyncComplete(requestID, asset.AssetHash, "success", "", asset.SizeBytes, "", dtshIncluded)

	sm.logger.WithFields(logging.Fields{
		"asset_hash": asset.AssetHash,
		"asset_type": asset.AssetType,
		"size_mb":    float64(asset.SizeBytes) / (1024 * 1024),
		"duration":   duration,
	}).Info("Asset synced to S3 (local copy retained)")

	return nil
}

func (sm *StorageManager) defrostSingleFile(ctx context.Context, req *pb.DefrostRequest, assetType AssetType) (*pb.DefrostComplete, error) {
	// Check if already defrosting
	job, shouldInitiate := sm.getOrCreateDefrostJob(req.AssetHash, assetType, req.RequestId)
	if !shouldInitiate {
		// Wait for existing defrost
		select {
		case <-job.Done:
			if job.Err != nil {
				return nil, job.Err
			}
			return &pb.DefrostComplete{
				RequestId: req.RequestId,
				AssetHash: req.AssetHash,
				Status:    "success",
				LocalPath: job.LocalPath,
				SizeBytes: job.SizeBytes,
			}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	defer sm.completeDefrostJob(req.AssetHash)

	// Check if already local
	if _, err := os.Stat(req.LocalPath); err == nil {
		var sizeBytes uint64
		if info, statErr := os.Stat(req.LocalPath); statErr == nil {
			sizeBytes = uint64(info.Size())
		}
		_ = control.SendStorageLifecycle(&pb.StorageLifecycleData{
			Action:    pb.StorageLifecycleData_ACTION_CACHED,
			AssetType: string(assetType),
			AssetHash: req.AssetHash,
			SizeBytes: sizeBytes,
			LocalPath: &req.LocalPath,
		})
		_ = control.SendDefrostComplete(req.RequestId, req.AssetHash, "success", req.LocalPath, sizeBytes, "")
		sm.markDefrostJobDone(req.AssetHash, nil, req.LocalPath, sizeBytes)
		return &pb.DefrostComplete{
			RequestId: req.RequestId,
			AssetHash: req.AssetHash,
			Status:    "success",
			LocalPath: req.LocalPath,
			SizeBytes: sizeBytes,
		}, nil
	}

	// Validate presigned URL
	presignedURL := req.PresignedGetUrl
	if presignedURL == "" {
		err := fmt.Errorf("no presigned GET URL provided for defrost")
		sm.markDefrostJobDone(req.AssetHash, err, "", 0)
		return nil, err
	}

	// Ensure destination directory exists
	if err := os.MkdirAll(filepath.Dir(req.LocalPath), 0755); err != nil {
		sm.markDefrostJobDone(req.AssetHash, err, "", 0)
		return nil, fmt.Errorf("failed to create directory: %w", err)
	}

	// Notify cache refill started
	_ = control.SendStorageLifecycle(&pb.StorageLifecycleData{
		Action:    pb.StorageLifecycleData_ACTION_CACHE_STARTED,
		AssetType: string(assetType),
		AssetHash: req.AssetHash,
	})

	startTime := time.Now()

	// Download .dtsh if available (check SegmentUrls)
	// Filenames are typically hash.ext. .dtsh is hash.ext.dtsh
	dtshName := filepath.Base(req.LocalPath) + ".dtsh"
	if url, ok := req.SegmentUrls[dtshName]; ok {
		dtshPath := req.LocalPath + ".dtsh"
		if err := sm.presignedClient.DownloadToFileFromPresignedURL(ctx, url, dtshPath, nil); err != nil {
			sm.logger.WithError(err).Warn("Failed to download .dtsh file")
			// Non-fatal, MistServer will regenerate it
		} else {
			sm.logger.WithField("file", dtshPath).Info("Defrosted .dtsh file")
		}
	}

	// Download from S3 using presigned URL
	err := sm.presignedClient.DownloadToFileFromPresignedURL(ctx, presignedURL, req.LocalPath, func(downloaded int64) {
		_ = control.SendDefrostProgress(req.RequestId, req.AssetHash, 0, uint64(downloaded), 0, 0, "downloading")
	})

	duration := time.Since(startTime)

	if err != nil {
		sm.markDefrostJobDone(req.AssetHash, err, "", 0)
		_ = control.SendDefrostComplete(req.RequestId, req.AssetHash, "failed", "", 0, err.Error())
		return nil, fmt.Errorf("failed to download from S3: %w", err)
	}

	// Get file size
	info, _ := os.Stat(req.LocalPath)
	var sizeBytes uint64
	if info != nil {
		sizeBytes = uint64(info.Size())
	}

	// Notify completion (asset now cached locally from S3)
	durationMs := duration.Milliseconds()
	_ = control.SendStorageLifecycle(&pb.StorageLifecycleData{
		Action:     pb.StorageLifecycleData_ACTION_CACHED,
		AssetType:  string(assetType),
		AssetHash:  req.AssetHash,
		SizeBytes:  sizeBytes,
		LocalPath:  &req.LocalPath,
		DurationMs: &durationMs,
	})

	sm.markDefrostJobDone(req.AssetHash, nil, req.LocalPath, sizeBytes)

	// Send completion to Foghorn
	_ = control.SendDefrostComplete(req.RequestId, req.AssetHash, "success", req.LocalPath, sizeBytes, "")

	// TODO: Warm up the stream in MistServer to generate .dtsh file immediately.
	// This avoids latency when the first viewer connects.
	// We can do this by starting a push or requesting the stream header.

	sm.logger.WithFields(logging.Fields{
		"asset_hash": req.AssetHash,
		"size_mb":    float64(sizeBytes) / (1024 * 1024),
		"duration":   duration,
	}).Info("Asset defrosted from S3")

	return &pb.DefrostComplete{
		RequestId: req.RequestId,
		AssetHash: req.AssetHash,
		Status:    "success",
		LocalPath: req.LocalPath,
		SizeBytes: sizeBytes,
	}, nil
}

// DefrostClip downloads a clip from S3 back to local storage using presigned GET URL
func (sm *StorageManager) DefrostClip(ctx context.Context, req *pb.DefrostRequest) (*pb.DefrostComplete, error) {
	return sm.defrostSingleFile(ctx, req, AssetTypeClip)
}

// DefrostVOD downloads a VOD asset from S3 back to local storage using presigned GET URL
func (sm *StorageManager) DefrostVOD(ctx context.Context, req *pb.DefrostRequest) (*pb.DefrostComplete, error) {
	return sm.defrostSingleFile(ctx, req, AssetTypeVOD)
}

// DefrostDVR downloads a DVR recording from S3 using streaming defrost (HLS live mode)
// Uses presigned GET URLs provided by Foghorn for each segment
func (sm *StorageManager) DefrostDVR(ctx context.Context, req *pb.DefrostRequest) (*pb.DefrostComplete, error) {
	// Check if already defrosting
	job, shouldInitiate := sm.getOrCreateDefrostJob(req.AssetHash, AssetTypeDVR, req.RequestId)
	if !shouldInitiate {
		select {
		case <-job.Done:
			if job.Err != nil {
				return nil, job.Err
			}
			return &pb.DefrostComplete{
				RequestId: req.RequestId,
				AssetHash: req.AssetHash,
				Status:    "success",
				LocalPath: job.LocalPath,
				SizeBytes: job.SizeBytes,
			}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	defer sm.completeDefrostJob(req.AssetHash)

	// Check if already local (manifest exists)
	manifestPath := filepath.Join(req.LocalPath, req.AssetHash+".m3u8")
	if _, err := os.Stat(manifestPath); err == nil {
		var totalBytes uint64
		_ = filepath.Walk(req.LocalPath, func(_ string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if info.IsDir() {
				return nil
			}
			totalBytes += uint64(info.Size())
			return nil
		})
		_ = control.SendStorageLifecycle(&pb.StorageLifecycleData{
			Action:    pb.StorageLifecycleData_ACTION_CACHED,
			AssetType: string(AssetTypeDVR),
			AssetHash: req.AssetHash,
			SizeBytes: totalBytes,
			LocalPath: &manifestPath,
		})
		_ = control.SendDefrostComplete(req.RequestId, req.AssetHash, "success", manifestPath, totalBytes, "")
		sm.markDefrostJobDone(req.AssetHash, nil, manifestPath, totalBytes)
		return &pb.DefrostComplete{
			RequestId: req.RequestId,
			AssetHash: req.AssetHash,
			Status:    "success",
			LocalPath: manifestPath,
			SizeBytes: totalBytes,
		}, nil
	}

	// Validate segment URLs provided by Foghorn
	segmentURLs := req.SegmentUrls
	if len(segmentURLs) == 0 {
		err := fmt.Errorf("no segment URLs provided for DVR defrost")
		sm.markDefrostJobDone(req.AssetHash, err, "", 0)
		return nil, err
	}

	// Download and parse original manifest first to get correct segment durations
	manifestKey := req.AssetHash + ".m3u8"
	manifestURL, hasManifest := segmentURLs[manifestKey]
	if !hasManifest {
		err := fmt.Errorf("no manifest URL provided for DVR defrost")
		sm.markDefrostJobDone(req.AssetHash, err, "", 0)
		return nil, err
	}

	var manifestBuf bytes.Buffer
	_, err := sm.presignedClient.DownloadFromPresignedURL(ctx, manifestURL, &manifestBuf, nil)
	if err != nil {
		sm.markDefrostJobDone(req.AssetHash, err, "", 0)
		return nil, fmt.Errorf("failed to download original manifest: %w", err)
	}

	parsedManifest, err := parseHLSManifest(manifestBuf.String())
	if err != nil {
		sm.logger.WithError(err).Warn("Failed to parse manifest, using defaults")
		parsedManifest = &ParsedManifest{TargetDuration: 6}
	}

	// Build duration lookup map for segment downloads
	segmentDurations := make(map[string]float64)
	for _, seg := range parsedManifest.Segments {
		segmentDurations[seg.Name] = seg.Duration
	}

	// Notify cache refill started
	_ = control.SendStorageLifecycle(&pb.StorageLifecycleData{
		Action:    pb.StorageLifecycleData_ACTION_CACHE_STARTED,
		AssetType: string(AssetTypeDVR),
		AssetHash: req.AssetHash,
	})

	startTime := time.Now()

	// Create local directory
	if err := os.MkdirAll(req.LocalPath, 0755); err != nil {
		sm.markDefrostJobDone(req.AssetHash, err, "", 0)
		_ = control.SendDefrostComplete(req.RequestId, req.AssetHash, "failed", "", 0, err.Error())
		return nil, fmt.Errorf("failed to create directory: %w", err)
	}

	// Use segments from parsed manifest for correct order (instead of sort.Strings)
	// Fall back to URL keys if manifest parsing yielded no segments
	var segments []string
	if len(parsedManifest.Segments) > 0 {
		for _, seg := range parsedManifest.Segments {
			segments = append(segments, seg.Name)
		}
	} else {
		// Fallback: extract from URLs and sort
		for segName := range segmentURLs {
			if segName == manifestKey {
				continue
			}
			segments = append(segments, segName)
		}
		sort.Strings(segments)
	}
	totalSegments := len(segments)

	// Try to resume from progress file
	progress, _ := sm.loadDefrostProgress(req.AssetHash, req.LocalPath)
	completedSet := make(map[string]bool)
	if progress != nil {
		for _, seg := range progress.CompletedSegments {
			completedSet[seg] = true
		}
	} else {
		progress = &DefrostProgress{
			DVRHash:       req.AssetHash,
			TotalSegments: totalSegments,
			StartedAt:     time.Now().Unix(),
		}
	}

	// Download .dtsh if available (often named stream.dtsh or similar, check URLs)
	// For DVR, we check keys ending in .dtsh
	for name, url := range segmentURLs {
		if strings.HasSuffix(name, ".dtsh") {
			dtshPath := filepath.Join(req.LocalPath, name)
			if err := sm.presignedClient.DownloadToFileFromPresignedURL(ctx, url, dtshPath, nil); err != nil {
				sm.logger.WithError(err).WithField("file", name).Warn("Failed to download DVR .dtsh file")
			} else {
				sm.logger.WithField("file", dtshPath).Info("Defrosted DVR .dtsh file")
			}
		}
	}

	// Create initial manifest WITHOUT #EXT-X-ENDLIST (live DVR mode)
	localSegmentsDir := filepath.Join(req.LocalPath, "segments")
	if err := os.MkdirAll(localSegmentsDir, 0755); err != nil {
		sm.markDefrostJobDone(req.AssetHash, err, "", 0)
		_ = control.SendDefrostComplete(req.RequestId, req.AssetHash, "failed", "", 0, err.Error())
		return nil, err
	}

	// Write initial manifest header with correct target duration from original manifest
	manifest := sm.createLiveManifest(req.AssetHash, parsedManifest.TargetDuration)
	if err := os.WriteFile(manifestPath, []byte(manifest), 0644); err != nil {
		sm.markDefrostJobDone(req.AssetHash, err, "", 0)
		_ = control.SendDefrostComplete(req.RequestId, req.AssetHash, "failed", "", 0, err.Error())
		return nil, err
	}

	// Signal ready for playback (manifest exists)
	_ = control.SendDefrostProgress(req.RequestId, req.AssetHash, 0, 0, 0, int32(totalSegments), "ready")

	// Download segments in order using presigned URLs, appending to manifest
	var totalBytes uint64
	for i, segName := range segments {
		if completedSet[segName] {
			continue // Already downloaded (resume)
		}

		presignedURL, ok := segmentURLs[segName]
		if !ok {
			sm.logger.WithField("segment", segName).Warn("No presigned URL for segment, skipping")
			continue
		}

		localSegPath := filepath.Join(localSegmentsDir, segName)

		err := sm.presignedClient.DownloadToFileFromPresignedURL(ctx, presignedURL, localSegPath, nil)
		if err != nil {
			// Save progress for resume
			sm.saveDefrostProgress(progress, req.LocalPath)
			sm.markDefrostJobDone(req.AssetHash, err, "", 0)
			_ = control.SendDefrostComplete(req.RequestId, req.AssetHash, "failed", "", 0, err.Error())
			return nil, fmt.Errorf("failed to download segment %s: %w", segName, err)
		}

		// Get segment size
		info, _ := os.Stat(localSegPath)
		if info != nil {
			totalBytes += uint64(info.Size())
		}

		// Update manifest with new segment using duration from original manifest
		segDuration := segmentDurations[segName]
		if segDuration == 0 {
			segDuration = 6.0 // fallback if not found
		}
		sm.appendSegmentToManifest(manifestPath, segName, segDuration)

		// Update progress
		progress.CompletedSegments = append(progress.CompletedSegments, segName)
		progress.LastUpdated = time.Now().Unix()

		// Send progress
		percent := uint32(((i + 1) * 100) / totalSegments)
		_ = control.SendDefrostProgress(req.RequestId, req.AssetHash, percent, totalBytes, int32(i+1), int32(totalSegments), "downloading")
	}

	// Finalize manifest with #EXT-X-ENDLIST (becomes VOD)
	sm.finalizeManifest(manifestPath)

	// Remove progress file
	sm.removeDefrostProgress(req.LocalPath)

	duration := time.Since(startTime)
	durationMs := duration.Milliseconds()

	// Notify completion (DVR now cached locally from S3)
	_ = control.SendStorageLifecycle(&pb.StorageLifecycleData{
		Action:     pb.StorageLifecycleData_ACTION_CACHED,
		AssetType:  string(AssetTypeDVR),
		AssetHash:  req.AssetHash,
		SizeBytes:  totalBytes,
		LocalPath:  &manifestPath,
		DurationMs: &durationMs,
	})

	sm.markDefrostJobDone(req.AssetHash, nil, manifestPath, totalBytes)

	// Send completion to Foghorn
	_ = control.SendDefrostComplete(req.RequestId, req.AssetHash, "success", manifestPath, totalBytes, "")

	sm.logger.WithFields(logging.Fields{
		"asset_hash":     req.AssetHash,
		"total_segments": totalSegments,
		"size_mb":        float64(totalBytes) / (1024 * 1024),
		"duration":       duration,
	}).Info("DVR defrosted from S3")

	return &pb.DefrostComplete{
		RequestId: req.RequestId,
		AssetHash: req.AssetHash,
		Status:    "success",
		LocalPath: manifestPath,
		SizeBytes: totalBytes,
	}, nil
}

// Helper methods for defrost job tracking

func (sm *StorageManager) getOrCreateDefrostJob(assetHash string, assetType AssetType, requestID string) (*DefrostJob, bool) {
	sm.defrostTracker.mu.Lock()
	defer sm.defrostTracker.mu.Unlock()

	if job, exists := sm.defrostTracker.inFlight[assetHash]; exists {
		atomic.AddInt32(&job.Waiters, 1)
		return job, false // Don't initiate, wait for existing
	}

	job := &DefrostJob{
		RequestID: requestID,
		AssetHash: assetHash,
		AssetType: assetType,
		StartedAt: time.Now(),
		Done:      make(chan struct{}),
		Waiters:   1,
	}
	sm.defrostTracker.inFlight[assetHash] = job
	return job, true // Should initiate
}

func (sm *StorageManager) markDefrostJobDone(assetHash string, err error, localPath string, sizeBytes uint64) {
	sm.defrostTracker.mu.Lock()
	defer sm.defrostTracker.mu.Unlock()

	if job, exists := sm.defrostTracker.inFlight[assetHash]; exists {
		job.Err = err
		job.LocalPath = localPath
		job.SizeBytes = sizeBytes
		job.closeOnce.Do(func() {
			close(job.Done)
		})
	}
}

func (sm *StorageManager) completeDefrostJob(assetHash string) {
	sm.defrostTracker.mu.Lock()
	defer sm.defrostTracker.mu.Unlock()
	delete(sm.defrostTracker.inFlight, assetHash)
}

// HLS manifest helpers

func (sm *StorageManager) createLiveManifest(dvrHash string, targetDuration int) string {
	return fmt.Sprintf(`#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:%d
#EXT-X-MEDIA-SEQUENCE:0
#EXT-X-PLAYLIST-TYPE:EVENT
`, targetDuration)
}

func (sm *StorageManager) appendSegmentToManifest(manifestPath, segmentName string, duration float64) {
	f, err := os.OpenFile(manifestPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		sm.logger.WithError(err).WithField("manifest", manifestPath).Warn("Failed to open manifest for append")
		return
	}
	defer f.Close()

	segment := fmt.Sprintf("#EXTINF:%.3f,\nsegments/%s\n", duration, segmentName)
	if _, err := f.WriteString(segment); err != nil {
		sm.logger.WithError(err).WithField("manifest", manifestPath).Warn("Failed to write segment to manifest")
	}
}

func (sm *StorageManager) finalizeManifest(manifestPath string) {
	f, err := os.OpenFile(manifestPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		sm.logger.WithError(err).WithField("manifest", manifestPath).Warn("Failed to open manifest for finalization")
		return
	}
	defer f.Close()
	if _, err := f.WriteString("#EXT-X-ENDLIST\n"); err != nil {
		sm.logger.WithError(err).WithField("manifest", manifestPath).Warn("Failed to write ENDLIST to manifest")
	}
}

// parseHLSManifest parses an HLS manifest to extract segment names and durations.
// This is used during freeze/defrost to preserve the original manifest metadata
// instead of regenerating with hardcoded values.
func parseHLSManifest(content string) (*ParsedManifest, error) {
	result := &ParsedManifest{
		TargetDuration: 6, // default fallback
	}

	lines := strings.Split(content, "\n")
	var pendingDuration float64

	for _, line := range lines {
		line = strings.TrimSpace(line)

		if strings.HasPrefix(line, "#EXT-X-TARGETDURATION:") {
			val := strings.TrimPrefix(line, "#EXT-X-TARGETDURATION:")
			if d, err := strconv.Atoi(strings.TrimSpace(val)); err == nil {
				result.TargetDuration = d
			}
		} else if strings.HasPrefix(line, "#EXTINF:") {
			// Parse duration from "#EXTINF:6.000," or "#EXTINF:6,"
			val := strings.TrimPrefix(line, "#EXTINF:")
			val = strings.Split(val, ",")[0] // Remove trailing comma and title
			if d, err := strconv.ParseFloat(strings.TrimSpace(val), 64); err == nil {
				pendingDuration = d
			}
		} else if !strings.HasPrefix(line, "#") && line != "" && pendingDuration > 0 {
			// This is a segment filename
			segName := filepath.Base(line) // Handle "segments/foo.ts" paths
			// Strip query params if present (e.g., "foo.ts?someParam=value" -> "foo.ts")
			if idx := strings.Index(segName, "?"); idx > 0 {
				segName = segName[:idx]
			}
			result.Segments = append(result.Segments, ParsedSegment{
				Name:     segName,
				Duration: pendingDuration,
			})
			pendingDuration = 0
		}
	}

	return result, nil
}

// Progress file helpers for resume

func (sm *StorageManager) loadDefrostProgress(dvrHash, localPath string) (*DefrostProgress, error) {
	progressFile := filepath.Join(localPath, ".defrost.json")
	data, err := os.ReadFile(progressFile)
	if err != nil {
		return nil, err
	}
	var progress DefrostProgress
	if err := json.Unmarshal(data, &progress); err != nil {
		return nil, err
	}
	return &progress, nil
}

func (sm *StorageManager) saveDefrostProgress(progress *DefrostProgress, localPath string) {
	progressFile := filepath.Join(localPath, ".defrost.json")
	data, _ := json.Marshal(progress)
	if err := os.WriteFile(progressFile, data, 0644); err != nil {
		sm.logger.WithError(err).Warn("Failed to save defrost progress file")
	}
}

func (sm *StorageManager) removeDefrostProgress(localPath string) {
	progressFile := filepath.Join(localPath, ".defrost.json")
	os.Remove(progressFile)
}

// Storage utility methods

func (sm *StorageManager) getStorageUsage(path string) (float64, uint64, uint64, error) {
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

func (sm *StorageManager) calculateDirSize(path string) uint64 {
	var size uint64
	_ = filepath.Walk(path, func(_ string, info os.FileInfo, _ error) error { //nolint:errcheck // size defaults to 0 on walk failure
		if info != nil && !info.IsDir() {
			size += uint64(info.Size())
		}
		return nil
	})
	return size
}

func (sm *StorageManager) isClipFile(path string) bool {
	return IsVideoFile(filepath.Ext(path))
}

func (sm *StorageManager) extractHashFromPath(path string) string {
	filename := filepath.Base(path)
	ext := filepath.Ext(filename)
	name := filename[:len(filename)-len(ext)]
	if len(name) >= 18 {
		return name
	}
	return ""
}

func (sm *StorageManager) calculateFreezePriority(asset FreezeCandidate) float64 {
	now := time.Now()

	ageHours := now.Sub(asset.CreatedAt).Hours()
	ageFactor := ageHours / 24.0

	sizeMB := float64(asset.SizeBytes) / (1024 * 1024)
	sizeFactor := sizeMB / 100.0

	accessFactor := float64(asset.AccessCount + 1)

	lastAccessHours := now.Sub(asset.LastAccessed).Hours()
	recentAccessFactor := 1.0
	if lastAccessHours < 24 {
		recentAccessFactor = 10.0
	} else if lastAccessHours < 168 {
		recentAccessFactor = 2.0
	}

	priority := (ageFactor + sizeFactor*0.1) / (accessFactor * recentAccessFactor)
	return priority
}

// fallbackCleanup performs deletion-based cleanup when S3 is not configured
// In dual-storage mode, it asks Foghorn before deleting to ensure asset is synced
func (sm *StorageManager) fallbackCleanup(clipsDir string, usedBytes, totalBytes uint64) error {
	targetBytes := uint64(float64(totalBytes) * sm.targetThreshold)
	bytesToFree := usedBytes - targetBytes

	candidates, err := sm.getFreezeCandidates(clipsDir, AssetTypeClip)
	if err != nil {
		return err
	}

	// Also get DVR candidates
	dvrDir := filepath.Join(sm.basePath, "dvr")
	dvrCandidates, err := sm.getFreezeCandidates(dvrDir, AssetTypeDVR)
	if err == nil {
		candidates = append(candidates, dvrCandidates...)
	}

	// Also get VOD candidates
	vodDir := filepath.Join(sm.basePath, "vod")
	vodCandidates, err := sm.getFreezeCandidates(vodDir, AssetTypeVOD)
	if err == nil {
		candidates = append(candidates, vodCandidates...)
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Priority < candidates[j].Priority
	})

	var totalFreed uint64
	var syncTriggered int

	for _, candidate := range candidates {
		if totalFreed >= bytesToFree {
			break
		}

		// Dual-storage: Ask Foghorn if it's safe to delete (i.e., synced to S3)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		safeToDelete, reason, warmDurationMs, err := control.RequestCanDelete(ctx, candidate.AssetHash)
		cancel()

		if err != nil {
			sm.logger.WithError(err).WithField("asset_hash", candidate.AssetHash).Warn("Failed to check if asset can be deleted")
			// If Foghorn is unreachable, don't delete (data safety first)
			continue
		}

		if safeToDelete {
			// Asset is synced to S3, safe to delete local copy
			var deleteErr error
			if candidate.AssetType == AssetTypeClip || candidate.AssetType == AssetTypeVOD {
				deleteErr = os.Remove(candidate.FilePath)
				if deleteErr == nil {
					// Clean up auxiliary files after main file deletion succeeds.
					os.Remove(candidate.FilePath + ".dtsh")
					os.Remove(candidate.FilePath + ".gop")
				}
			} else {
				// DVR: remove entire directory
				deleteErr = os.RemoveAll(candidate.FilePath)
			}

			if deleteErr != nil {
				sm.logger.WithError(deleteErr).WithField("asset_hash", candidate.AssetHash).Warn("Failed to delete local copy")
				errStr := deleteErr.Error()
				_ = control.SendStorageLifecycle(&pb.StorageLifecycleData{
					Action:    pb.StorageLifecycleData_ACTION_EVICTED,
					AssetType: string(candidate.AssetType),
					AssetHash: candidate.AssetHash,
					SizeBytes: candidate.SizeBytes,
					Error:     &errStr,
				})
				continue
			}

			// Notify deletion (eviction from local cache) with warm duration metric
			_ = control.SendStorageLifecycle(&pb.StorageLifecycleData{
				Action:         pb.StorageLifecycleData_ACTION_EVICTED,
				AssetType:      string(candidate.AssetType),
				AssetHash:      candidate.AssetHash,
				SizeBytes:      candidate.SizeBytes,
				WarmDurationMs: &warmDurationMs,
			})
			_ = control.SendArtifactDeleted(candidate.AssetHash, candidate.FilePath, "eviction", string(candidate.AssetType), candidate.SizeBytes)

			totalFreed += candidate.SizeBytes
			sm.logger.WithFields(logging.Fields{
				"asset_hash":       candidate.AssetHash,
				"asset_type":       candidate.AssetType,
				"size_mb":          float64(candidate.SizeBytes) / (1024 * 1024),
				"warm_duration_ms": warmDurationMs,
			}).Info("Evicted synced asset from local storage")
		} else {
			// Asset not synced - trigger sync instead of deleting
			sm.logger.WithFields(logging.Fields{
				"asset_hash": candidate.AssetHash,
				"reason":     reason,
			}).Info("Asset not safe to delete, triggering sync")

			// Trigger freeze/sync operation (this will upload to S3)
			go func(c FreezeCandidate) {
				ctx := context.Background()
				if err := sm.freezeAsset(ctx, c); err != nil {
					sm.logger.WithError(err).WithField("asset_hash", c.AssetHash).Error("Failed to sync asset for eviction")
				}
			}(candidate)
			syncTriggered++

			// Don't count as freed yet - will be available for eviction after sync
		}
	}

	if syncTriggered > 0 {
		sm.logger.WithField("sync_triggered", syncTriggered).Info("Triggered sync for unsynced assets during cleanup")
	}

	return nil
}

// GetStorageManager returns the global storage manager instance
func GetStorageManager() *StorageManager {
	return storageManager
}

// ColdStorageAvailable returns true if cold storage operations are possible
// This checks if Foghorn connection is available (which provides presigned URLs)
func (sm *StorageManager) ColdStorageAvailable() bool {
	return control.IsConnected()
}

// SyncDtshOnly handles incremental .dtsh file sync requests from Foghorn.
// This is called when .dtsh appeared after the main asset was already synced to S3.
func (sm *StorageManager) SyncDtshOnly(ctx context.Context, req *pb.DtshSyncRequest) error {
	if sm.presignedClient == nil {
		return fmt.Errorf("presigned client not configured")
	}

	assetType := req.GetAssetType()
	assetHash := req.GetAssetHash()
	localPath := req.GetLocalPath()
	requestID := req.GetRequestId()

	sm.logger.WithFields(logging.Fields{
		"request_id": requestID,
		"asset_type": assetType,
		"asset_hash": assetHash,
		"local_path": localPath,
	}).Info("Processing incremental .dtsh sync request")

	var uploadErr error
	dtshUploaded := false

	if assetType == "clip" {
		// For clips: single .dtsh file next to the main file
		dtshPath := localPath + ".dtsh"
		presignedURL := req.GetPresignedPutUrl()

		if presignedURL == "" {
			return fmt.Errorf("no presigned URL provided for clip .dtsh")
		}

		// Check if .dtsh file exists locally
		if _, err := os.Stat(dtshPath); err != nil {
			return fmt.Errorf(".dtsh file not found at %s: %w", dtshPath, err)
		}

		// Upload the .dtsh file
		if err := sm.presignedClient.UploadFileToPresignedURL(ctx, presignedURL, dtshPath, nil); err != nil {
			uploadErr = fmt.Errorf("failed to upload clip .dtsh: %w", err)
		} else {
			dtshUploaded = true
			sm.logger.WithFields(logging.Fields{
				"asset_hash": assetHash,
				"dtsh_path":  dtshPath,
			}).Info("Uploaded clip .dtsh file")
		}
	} else if assetType == "dvr" {
		// For DVR: may have multiple .dtsh files in the directory
		dtshURLs := req.GetDtshUrls()
		if len(dtshURLs) == 0 {
			return fmt.Errorf("no presigned URLs provided for DVR .dtsh files")
		}

		// Check what .dtsh files exist locally and upload them
		for dtshName, presignedURL := range dtshURLs {
			dtshPath := filepath.Join(localPath, dtshName)
			if _, err := os.Stat(dtshPath); err != nil {
				// This particular .dtsh file doesn't exist, skip
				continue
			}

			if err := sm.presignedClient.UploadFileToPresignedURL(ctx, presignedURL, dtshPath, nil); err != nil {
				sm.logger.WithError(err).WithField("dtsh_name", dtshName).Warn("Failed to upload DVR .dtsh file")
				continue
			}

			dtshUploaded = true
			sm.logger.WithFields(logging.Fields{
				"asset_hash": assetHash,
				"dtsh_name":  dtshName,
			}).Info("Uploaded DVR .dtsh file")
		}

		if !dtshUploaded {
			uploadErr = fmt.Errorf("no DVR .dtsh files found or uploaded")
		}
	}

	if uploadErr != nil {
		// Send failure notification
		_ = control.SendSyncComplete(requestID, assetHash, "failed", "", 0, uploadErr.Error(), false)
		return uploadErr
	}

	// Send success notification with dtsh_included=true
	_ = control.SendSyncComplete(requestID, assetHash, "success", "", 0, "", dtshUploaded)

	sm.logger.WithFields(logging.Fields{
		"request_id": requestID,
		"asset_hash": assetHash,
		"asset_type": assetType,
	}).Info("Incremental .dtsh sync completed")

	return nil
}
