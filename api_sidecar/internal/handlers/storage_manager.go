package handlers

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"io"

	"frameworks/api_sidecar/internal/config"
	"frameworks/api_sidecar/internal/control"
	"frameworks/api_sidecar/internal/dtsh"
	"frameworks/api_sidecar/internal/leases"
	"frameworks/api_sidecar/internal/storage"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// PresignedTransfer abstracts presigned-URL upload/download so tests can
// inject fakes without hitting the network.
type PresignedTransfer interface {
	UploadFileToPresignedURL(ctx context.Context, url, localPath string, onProgress storage.ProgressCallback) error
	UploadToPresignedURL(ctx context.Context, url string, reader io.Reader, size int64, onProgress storage.ProgressCallback) error
	DownloadToFileFromPresignedURL(ctx context.Context, url, localPath string, onProgress storage.ProgressCallback) error
	DownloadFromPresignedURL(ctx context.Context, url string, writer io.Writer, onProgress storage.ProgressCallback) (int64, error)
}

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

// StorageManager manages cold storage operations (freeze)
type StorageManager struct {
	logger   logging.Logger
	basePath string
	nodeID   string
	capacity uint64
	running  bool
	stopCh   chan struct{}

	// Presigned URL client (NO S3 credentials - uses presigned URLs from Foghorn)
	presignedClient PresignedTransfer

	// Control IPC — function fields so tests can inject fakes
	requestFreezePermission func(ctx context.Context, assetType, assetHash, localPath string, sizeBytes uint64, filenames []string) (*ipcpb.FreezePermissionResponse, error)
	sendSyncComplete        func(requestID, assetHash, status, s3URL string, sizeBytes uint64, errMsg string, dtshIncluded bool, localMissing bool) error
	sendFreezeComplete      func(requestID, assetHash, status, s3URL string, sizeBytes uint64, errMsg string, localMissing bool) error
	sendFreezeProgress      func(requestID, assetHash string, percent uint32, bytesUploaded uint64) error
	sendStorageLifecycle    func(data *ipcpb.StorageLifecycleData) error
	requestCanDelete        func(ctx context.Context, assetHash string) (bool, string, int64, error)
	sendArtifactDeleted     func(assetHash, filePath, reason, assetType string, sizeBytes uint64) error

	// Thresholds
	freezeThreshold      float64       // Start freezing at this % (default: 85%)
	targetThreshold      float64       // Target usage after freeze (default: 70%)
	deleteThreshold      float64       // Delete even frozen assets if above this % (default: 95%)
	softCleanupThreshold float64       // Projected-usage trigger for proactive background cleanup (default: freezeThreshold)
	minRetentionHours    int           // Never freeze assets younger than this
	checkInterval        time.Duration // Normal polling interval

	// Hybrid trigger mechanism
	urgentFreezeCh  chan struct{}
	lastUrgentCheck time.Time
	urgentDebounce  time.Duration

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
		logger:               logger,
		basePath:             basePath,
		nodeID:               nodeID,
		capacity:             thresholds.CapacityBytes,
		running:              false,
		stopCh:               make(chan struct{}),
		presignedClient:      presignedClient,
		freezeThreshold:      thresholds.FreezeThreshold,
		targetThreshold:      thresholds.TargetThreshold,
		deleteThreshold:      0.95, // 95%
		softCleanupThreshold: thresholds.SoftCleanupThreshold,
		minRetentionHours:    1,
		checkInterval:        5 * time.Minute,
		urgentFreezeCh:       make(chan struct{}, 1),
		urgentDebounce:       2 * time.Second,

		requestFreezePermission: control.RequestFreezePermission,
		sendSyncComplete:        control.SendSyncComplete,
		sendFreezeComplete:      control.SendFreezeComplete,
		sendFreezeProgress:      control.SendFreezeProgress,
		sendStorageLifecycle:    control.SendStorageLifecycle,
		requestCanDelete:        control.RequestCanDelete,
		sendArtifactDeleted:     control.SendArtifactDeleted,
	}

	storageManager.freezeTracker.inFlight = make(map[string]bool)

	// SoftCleanupThreshold defaults to freezeThreshold when caller didn't set
	// it. Both gate "85% is getting full"; operators can tune the soft tier
	// independently if they want to start proactive cleanup earlier.
	if storageManager.softCleanupThreshold <= 0 {
		storageManager.softCleanupThreshold = storageManager.freezeThreshold
	}

	// Start monitoring in background
	go storageManager.start()

	// Register handlers for cold storage operations from Foghorn.
	control.SetFreezeRequestHandler(storageManager.HandleFreezeRequest)

	control.SetDtshSyncRequestHandler(func(req *ipcpb.DtshSyncRequest) {
		ctx := context.Background()
		if err := storageManager.SyncDtshOnly(ctx, req); err != nil {
			logger.WithError(err).WithFields(logging.Fields{
				"request_id": req.GetRequestId(),
				"asset_type": req.GetAssetType(),
				"asset_hash": req.GetAssetHash(),
			}).Warn("Incremental .dtsh sync failed")
		}
	})

	// Register processing job handler
	procHandler := NewProcessingJobHandler(logger, os.Getenv("MISTSERVER_URL"), basePath)
	control.SetProcessingJobHandler(func(req *ipcpb.ProcessingJobRequest, send func(*ipcpb.ControlMessage)) {
		procHandler.Handle(req, send)
	})

	// DVR finalize-time retry: Foghorn pushes RetryDVRSegmentUpload listing
	// segments still pending/failed. For each, look up the local file under
	// the active DVR's segments directory, the local segment index, or the
	// on-disk DVR tree; if present, request a fresh presigned URL via
	// RecordDVRSegment and re-upload. If absent, emit
	// DVRSegmentDropped(was_uploaded=false) so Foghorn classifies it as
	// lost_local — any chapter overlapping the row will then move to
	// failed_source_missing at finalization. Transient presign/upload
	// failures are not classified here; FinalizeDVR owns the retry
	// deadline and marks remaining pending rows lost after the budget.
	control.SetRetryDVRSegmentHandler(func(req *ipcpb.RetryDVRSegmentUpload) {
		dvrHash := req.GetDvrHash()
		dm := control.GetDVRManager()
		if dm == nil {
			return
		}
		job, ok := control.LookupActiveDVR(dvrHash)
		var outputDir string
		var jobLogger logging.Logger
		if ok && job != nil {
			outputDir = job.OutputDir
			jobLogger = job.Logger
		} else {
			jobLogger = logger
		}
		refs := req.GetSegments()
		if len(refs) == 0 && len(req.GetSegmentNames()) > 0 {
			restoreCtx, restoreCancel := context.WithTimeout(context.Background(), 30*time.Second)
			resp, restoreErr := control.SendRestoreLocalSegmentIndex(restoreCtx, dvrHash, req.GetSegmentNames())
			restoreCancel()
			if restoreErr != nil {
				jobLogger.WithError(restoreErr).WithField("dvr_hash", dvrHash).Debug("Retry ledger lookup unavailable; leaving segments pending")
				return
			}
			refs = resp.GetSegments()
		}
		for _, ref := range refs {
			name := ref.GetSegmentName()
			if name == "" {
				continue
			}
			segPath := resolveRetryDVRSegmentPath(basePath, dvrHash, name, outputDir, logger)
			info, statErr := os.Stat(segPath)
			if statErr != nil {
				if dropErr := control.SendDVRSegmentDropped(dvrHash, name, "upload_failed", segPath,
					ref.GetMediaStartMs(), ref.GetMediaEndMs(), ref.GetDurationMs(), 0, false); dropErr != nil {
					jobLogger.WithError(dropErr).WithField("segment", name).Debug("Failed to report missing-local-file as lost")
				}
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			// Request a fresh presigned URL. RecordDVRSegment is idempotent
			// on (artifact_hash, segment_name) but still requires exact
			// ledger timing so a wrong file with the same name cannot heal a
			// gap or claim another segment's sequence.
			resp, recErr := control.RecordDVRSegment(ctx, dvrHash, name, segPath,
				ref.GetMediaStartMs(), ref.GetMediaEndMs(), ref.GetDurationMs())
			if recErr != nil || resp == nil || !resp.GetAccepted() || resp.GetPresignedPutUrl() == "" {
				cancel()
				jobLogger.WithFields(logging.Fields{
					"dvr_hash": dvrHash,
					"segment":  name,
				}).Debug("Retry presign unavailable; leaving segment pending for next finalize retry tick")
				continue
			}
			if upErr := dm.UploadSegmentForRetry(ctx, segPath, resp.GetPresignedPutUrl()); upErr != nil {
				cancel()
				jobLogger.WithError(upErr).WithField("segment", name).Warn("Retry upload failed; leaving segment pending for next finalize retry tick")
				continue
			}
			cancel()
			if markErr := control.SendMarkDVRSegmentUploaded(dvrHash, name, uint64(info.Size())); markErr != nil {
				jobLogger.WithError(markErr).WithField("segment", name).Warn("Failed to mark segment uploaded after retry")
			}
			if idx := control.LocalSegmentIndexInstance(logger); idx != nil {
				idx.MarkUploaded(dvrHash, name, segPath, info.Size())
			}
		}
	})

	// Chapter reclaim: Foghorn issues ReclaimDVRSegment once every
	// overlapping chapter has reached state='frozen' (canonical .mkv +
	// .dtsh durably on S3). The local TS files are now redundant and
	// can be deleted. Foghorn deletes the recovery-bridge S3 objects
	// directly; this handler only touches the local filesystem.
	control.SetReclaimDVRSegmentHandler(func(req *ipcpb.ReclaimDVRSegment) {
		dm := control.GetDVRManager()
		if dm == nil {
			return
		}
		names := req.GetSegmentNames()
		if len(names) == 0 {
			return
		}
		deleted := dm.EvictUploadedSegments(req.GetDvrHash(), names, "chapter_reclaim")
		logger.WithFields(logging.Fields{
			"dvr_hash": req.GetDvrHash(),
			"deleted":  deleted,
			"asked":    len(names),
		}).Info("Chapter reclaim: removed local DVR segments")
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
	CapacityBytes   uint64
	// SoftCleanupThreshold is the projected post-write usage at which the
	// admission path kicks off proactive background cleanup. 0 means default
	// to FreezeThreshold.
	SoftCleanupThreshold float64
}

// StopStorageManager stops the storage manager
func StopStorageManager() {
	if storageManager != nil && storageManager.running {
		close(storageManager.stopCh)
		storageLogger.Info("Storage manager stopped")
	}
}

func resolveRetryDVRSegmentPath(basePath, dvrHash, segmentName, outputDir string, logger logging.Logger) string {
	if outputDir != "" {
		if p := filepath.Join(outputDir, "segments", segmentName); fileExists(p) {
			return p
		}
	}
	if idx := control.LocalSegmentIndexInstance(logger); idx != nil {
		if p, ok := idx.LocalPath(dvrHash, segmentName); ok && fileExists(p) {
			return p
		}
	}
	dvrRoot := filepath.Join(basePath, "dvr")
	streamDirs, err := os.ReadDir(dvrRoot)
	if err != nil {
		return ""
	}
	for _, streamDir := range streamDirs {
		if !streamDir.IsDir() {
			continue
		}
		p := filepath.Join(dvrRoot, streamDir.Name(), dvrHash, "segments", segmentName)
		if fileExists(p) {
			return p
		}
	}
	return ""
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info != nil && !info.IsDir()
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

	// Get freeze candidates from clips and VOD. DVR uses ledger-backed
	// per-segment eviction only; whole-directory DVR freeze would recreate an
	// edge-authored archive manifest.
	var candidates []FreezeCandidate

	clipCandidates, err := sm.getFreezeCandidates(clipsDir, AssetTypeClip)
	if err != nil {
		sm.logger.WithError(err).Warn("Failed to get clip freeze candidates")
	} else {
		candidates = append(candidates, clipCandidates...)
	}

	// Skip VOD freeze candidates while any degraded VOD source lease is
	// held: a degraded lease has no path mapping (boot rebuild couldn't
	// resolve internal_name → artifact_hash on this node), so the freeze
	// path's exact-path-lease check at the candidate level cannot
	// protect the right file. Without this gate, skip_upload responses
	// would happily evict the backing file of an active VOD stream.
	if tracker := leases.GlobalTracker(); tracker == nil || !tracker.DegradedVodCleanupActive() {
		vodCandidates, err := sm.getFreezeCandidates(vodDir, AssetTypeVOD)
		if err != nil {
			sm.logger.WithError(err).Warn("Failed to get VOD freeze candidates")
		} else {
			candidates = append(candidates, vodCandidates...)
		}
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
	var uncatalogedCount int
	uncatalogedSamples := make([]string, 0, 5)

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
			if strings.Contains(err.Error(), "freeze not approved: asset_not_found") {
				uncatalogedCount++
				if len(uncatalogedSamples) < cap(uncatalogedSamples) {
					uncatalogedSamples = append(uncatalogedSamples, candidate.AssetHash)
				}
				continue
			}
			sm.logger.WithError(err).WithField("asset_hash", candidate.AssetHash).Error("Failed to freeze asset")
			continue
		}

		totalFreed += candidate.SizeBytes
		frozenCount++
	}
	if uncatalogedCount > 0 {
		sm.logger.WithFields(logging.Fields{
			"candidate_count": uncatalogedCount,
			"sample_hashes":   uncatalogedSamples,
		}).Warn("Skipped freeze candidates that are not cataloged")
	}

	sm.logger.WithFields(logging.Fields{
		"frozen_count":  frozenCount,
		"freed_gb":      float64(totalFreed) / (1024 * 1024 * 1024),
		"initial_usage": usagePercent,
	}).Info("Freeze operation completed")

	return nil
}

// dropPressuredDVRSegments asks Foghorn for the authoritative list of
// safe-to-evict segments for an active DVR and deletes the matching local
// files. Used during storage-pressure passes so the choice respects the
// effective live window even if the local uploaded cache has drifted.
// Returns the number of files deleted.
func (sm *StorageManager) dropPressuredDVRSegments(dvrHash string) int {
	dm := control.GetDVRManager()
	if dm == nil {
		return 0
	}
	const batchSize int32 = 500
	const maxBatches = 10
	total := 0
	for batch := 0; batch < maxBatches; batch++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		resp, err := control.RequestEvictableSegments(ctx, dvrHash, batchSize)
		cancel()
		if err != nil || resp == nil {
			if err != nil {
				sm.logger.WithError(err).WithField("dvr_hash", dvrHash).Warn("Failed to query evictable segments")
			}
			break
		}
		if len(resp.GetSegmentNames()) == 0 {
			break
		}
		evicted := dm.EvictUploadedSegments(dvrHash, resp.GetSegmentNames(), "disk_pressure")
		total += evicted
		if evicted == 0 || len(resp.GetSegmentNames()) < int(batchSize) {
			break
		}
	}
	return total
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

			// Skip files currently leased by an active Mist source or viewer.
			if tracker := leases.GlobalTracker(); tracker != nil && tracker.IsPathLeased(path) {
				return nil
			}

			lastAccessed := info.ModTime()
			accessCount := 0
			if heat := leases.GlobalHeat(); heat != nil {
				if h, ok := heat.Lookup(path); ok {
					lastAccessed = h.LastAccessed
					accessCount = int(h.AccessCount)
				}
			}

			candidate := FreezeCandidate{
				AssetType:    AssetTypeClip,
				AssetHash:    clipHash,
				FilePath:     path,
				SizeBytes:    uint64(info.Size()),
				CreatedAt:    info.ModTime(),
				LastAccessed: lastAccessed,
				AccessCount:  accessCount,
			}
			candidate.Priority = sm.calculateFreezePriority(candidate)
			candidates = append(candidates, candidate)
			return nil
		})
		if err != nil {
			return nil, err
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

			fullPath := filepath.Join(dir, filename)
			if tracker := leases.GlobalTracker(); tracker != nil && tracker.IsPathLeased(fullPath) {
				continue
			}

			lastAccessed := info.ModTime()
			accessCount := 0
			if heat := leases.GlobalHeat(); heat != nil {
				if h, ok := heat.Lookup(fullPath); ok {
					lastAccessed = h.LastAccessed
					accessCount = int(h.AccessCount)
				}
			}

			candidate := FreezeCandidate{
				AssetType:    AssetTypeVOD,
				AssetHash:    vodHash,
				FilePath:     fullPath,
				SizeBytes:    uint64(info.Size()),
				CreatedAt:    info.ModTime(),
				LastAccessed: lastAccessed,
				AccessCount:  accessCount,
			}
			candidate.Priority = sm.calculateFreezePriority(candidate)
			candidates = append(candidates, candidate)
		}
	}

	return candidates, nil
}

// HandleFreezeRequest processes a proactive freeze command from Foghorn.
// For clip/vod, Foghorn already generated presigned URLs so we upload directly.
func (sm *StorageManager) HandleFreezeRequest(req *ipcpb.FreezeRequest) {
	ctx := context.Background()

	if req.AssetType == "dvr" {
		errMsg := "whole-DVR freeze is unsupported; use ledger segment eviction"
		sm.logger.WithField("asset_hash", req.AssetHash).Warn(errMsg)
		if err := sm.sendSyncComplete(req.RequestId, req.AssetHash, "failed", "", 0, errMsg, false, false); err != nil {
			sm.logger.WithError(err).WithField("asset_hash", req.AssetHash).Warn("Failed to report rejected DVR freeze")
		}
		return
	}

	info, err := os.Stat(req.LocalPath)
	if err != nil {
		sm.logger.WithError(err).WithField("path", req.LocalPath).Error("Freeze request: local path not found")
		// ENOENT here is the same terminal lost_local condition as inside the
		// upload path: caller asked us to freeze a file that's gone.
		_ = sm.sendSyncComplete(req.RequestId, req.AssetHash, "failed", "", 0, "local file not found: "+err.Error(), false, errors.Is(err, fs.ErrNotExist)) //nolint:errcheck // best-effort report; reconnect retries on stream loss
		return
	}

	var sizeBytes uint64
	if info.IsDir() {
		sizeBytes = sm.calculateDirSize(req.LocalPath)
	} else {
		sizeBytes = uint64(info.Size())
	}

	asset := FreezeCandidate{
		AssetType: AssetType(req.AssetType),
		AssetHash: req.AssetHash,
		FilePath:  req.LocalPath,
		StreamID:  req.InternalName,
		SizeBytes: sizeBytes,
	}

	permResp := &ipcpb.FreezePermissionResponse{
		RequestId:        req.RequestId,
		AssetHash:        req.AssetHash,
		Approved:         true,
		PresignedPutUrl:  req.PresignedPutUrl,
		UrlExpirySeconds: req.UrlExpirySeconds,
		SegmentUrls:      req.SegmentUrls,
	}

	if err := sm.uploadAsset(ctx, asset, permResp); err != nil {
		sm.logger.WithError(err).WithField("asset_hash", req.AssetHash).Error("Proactive freeze failed")
	}
}

// freezeAsset handles Helmsman-initiated freezes: collects filenames, requests
// permission from Foghorn, handles remote-artifact eviction, then delegates
// the actual upload to uploadAsset.
func (sm *StorageManager) freezeAsset(ctx context.Context, asset FreezeCandidate) error {
	if asset.AssetType == AssetTypeDVR {
		return fmt.Errorf("whole-DVR freeze is unsupported; DVR cleanup is ledger segment eviction only")
	}

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
	switch asset.AssetType {
	case AssetTypeClip, AssetTypeVOD:
		// Clip and VOD are single-file uploads
		filenames = append(filenames, filepath.Base(asset.FilePath))
		if err := dtsh.ValidateFile(asset.FilePath + ".dtsh"); err == nil {
			filenames = append(filenames, filepath.Base(asset.FilePath)+".dtsh")
		} else if !os.IsNotExist(err) {
			sm.logger.WithError(err).WithField("asset_hash", asset.AssetHash).Warn("Skipping invalid .dtsh during freeze permission request")
		}
	}

	// Request permission and presigned URL from Foghorn
	permResp, err := sm.requestFreezePermission(ctx, string(asset.AssetType), asset.AssetHash, asset.FilePath, asset.SizeBytes, filenames)
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

	// Remote artifact: the origin/storage cluster's S3 holds the authoritative
	// copy, so there is nothing to upload. FreezePermission only mints upload
	// permission — it is not an eviction authority. The local warm copy is a
	// cache; dropping it is owned solely by the pressure-cleanup pass, which is
	// gated on CanDelete (it returns remote_synced for exactly this case). Doing
	// nothing here keeps eviction in one authority and avoids the freeze path
	// racing cleanup on the same file.
	if permResp.GetSkipUpload() {
		sm.logger.WithFields(logging.Fields{
			"asset_hash": asset.AssetHash,
			"asset_type": asset.AssetType,
		}).Debug("Remote artifact skip_upload — nothing to freeze; eviction deferred to CanDelete-gated cleanup")
		return nil
	}

	return sm.uploadAsset(ctx, asset, permResp)
}

// uploadAsset performs the actual S3 upload using presigned URLs from the
// permission response and reports completion/failure back to Foghorn.
// Shared by both Helmsman-initiated (freezeAsset) and Foghorn-initiated (HandleFreezeRequest) paths.
func (sm *StorageManager) uploadAsset(ctx context.Context, asset FreezeCandidate, permResp *ipcpb.FreezePermissionResponse) error {
	if asset.AssetType == AssetTypeDVR {
		return fmt.Errorf("whole-DVR upload is unsupported; DVR archive playlists are generated by Foghorn chapters")
	}

	// Track in-flight (idempotent if already tracked by freezeAsset)
	sm.freezeTracker.mu.Lock()
	sm.freezeTracker.inFlight[asset.AssetHash] = true
	sm.freezeTracker.mu.Unlock()

	defer func() {
		sm.freezeTracker.mu.Lock()
		delete(sm.freezeTracker.inFlight, asset.AssetHash)
		sm.freezeTracker.mu.Unlock()
	}()

	requestID := permResp.RequestId

	_ = sm.sendStorageLifecycle(&ipcpb.StorageLifecycleData{ //nolint:errcheck // best-effort report
		Action:    ipcpb.StorageLifecycleData_ACTION_SYNC_STARTED,
		AssetType: string(asset.AssetType),
		AssetHash: asset.AssetHash,
		SizeBytes: asset.SizeBytes,
	})

	startTime := time.Now()
	var uploadErr error
	dtshIncluded := false

	if asset.AssetType == AssetTypeClip || asset.AssetType == AssetTypeVOD {
		if len(permResp.SegmentUrls) > 0 {
			baseName := filepath.Base(asset.FilePath)

			if url, ok := permResp.SegmentUrls[baseName]; ok {
				err := sm.presignedClient.UploadFileToPresignedURL(ctx, url, asset.FilePath, func(uploaded int64) {
					percent := uint32((uploaded * 100) / int64(asset.SizeBytes))
					_ = sm.sendFreezeProgress(requestID, asset.AssetHash, percent, uint64(uploaded))
				})
				if err != nil {
					uploadErr = fmt.Errorf("failed to upload %s: %w", asset.AssetType, err)
				}
			} else {
				uploadErr = fmt.Errorf("no URL provided for main %s file", asset.AssetType)
			}

			dtshName := baseName + ".dtsh"
			if url, ok := permResp.SegmentUrls[dtshName]; ok && uploadErr == nil {
				dtshPath := asset.FilePath + ".dtsh"
				if err := dtsh.ValidateFile(dtshPath); err != nil {
					sm.logger.WithError(err).Warn("Skipping invalid .dtsh file")
				} else if err := sm.presignedClient.UploadFileToPresignedURL(ctx, url, dtshPath, nil); err != nil {
					sm.logger.WithError(err).Warn("Failed to upload .dtsh file")
				} else {
					dtshIncluded = true
				}
			}
		} else {
			presignedURL := permResp.PresignedPutUrl
			if presignedURL == "" {
				return fmt.Errorf("no presigned URL provided for %s freeze", asset.AssetType)
			}

			uploadErr = sm.presignedClient.UploadFileToPresignedURL(ctx, presignedURL, asset.FilePath, func(uploaded int64) {
				percent := uint32((uploaded * 100) / int64(asset.SizeBytes))
				_ = sm.sendFreezeProgress(requestID, asset.AssetHash, percent, uint64(uploaded))
			})
		}
	} else {
		return fmt.Errorf("unsupported asset type for freeze: %s", asset.AssetType)
	}

	duration := time.Since(startTime)
	freezeUploadSeconds.WithLabelValues(string(asset.AssetType)).Observe(duration.Seconds())

	if uploadErr != nil {
		freezeUploads.WithLabelValues(string(asset.AssetType), "failed").Inc()
		durationMs := duration.Milliseconds()
		errStr := uploadErr.Error()
		// Distinguish "local source file is gone" (terminal: no S3 copy, no
		// local copy, retries cannot recover) from a transient sync failure.
		// Foghorn maps ACTION_LOCAL_MISSING to sync_status='lost_local' and
		// stops the retry loop.
		action := ipcpb.StorageLifecycleData_ACTION_SYNC_FAILED
		localMissing := errors.Is(uploadErr, fs.ErrNotExist)
		if localMissing {
			action = ipcpb.StorageLifecycleData_ACTION_LOCAL_MISSING
		}
		localPath := asset.FilePath
		_ = sm.sendStorageLifecycle(&ipcpb.StorageLifecycleData{ //nolint:errcheck // best-effort report
			Action:     action,
			AssetType:  string(asset.AssetType),
			AssetHash:  asset.AssetHash,
			LocalPath:  &localPath,
			Error:      &errStr,
			DurationMs: &durationMs,
		})
		_ = sm.sendFreezeComplete(requestID, asset.AssetHash, "failed", "", 0, uploadErr.Error(), localMissing) //nolint:errcheck // best-effort report
		return fmt.Errorf("failed to upload to S3: %w", uploadErr)
	}

	actualSizeBytes := asset.SizeBytes
	switch asset.AssetType {
	case AssetTypeClip, AssetTypeVOD:
		if info, err := os.Stat(asset.FilePath); err == nil {
			actualSizeBytes = uint64(info.Size())
		}
	}

	durationMs := duration.Milliseconds()
	_ = sm.sendStorageLifecycle(&ipcpb.StorageLifecycleData{ //nolint:errcheck // best-effort report
		Action:       ipcpb.StorageLifecycleData_ACTION_SYNCED,
		AssetType:    string(asset.AssetType),
		AssetHash:    asset.AssetHash,
		SizeBytes:    actualSizeBytes,
		DurationMs:   &durationMs,
		DtshIncluded: &dtshIncluded,
	})

	_ = sm.sendSyncComplete(requestID, asset.AssetHash, "success", "", actualSizeBytes, "", dtshIncluded, false) //nolint:errcheck // best-effort report

	freezeUploads.WithLabelValues(string(asset.AssetType), "success").Inc()
	freezeUploadBytes.WithLabelValues(string(asset.AssetType)).Add(float64(actualSizeBytes))

	sm.logger.WithFields(logging.Fields{
		"asset_hash": asset.AssetHash,
		"asset_type": asset.AssetType,
		"size_mb":    float64(asset.SizeBytes) / (1024 * 1024),
		"duration":   duration,
	}).Info("Asset synced to S3 (local copy retained)")

	return nil
}

// evictBlockCaches walks vod/ and clips/ for *.blocks/ directories and
// RemoveAll's them in oldest-mtime-first order until bytesTarget is met
// or the supply is exhausted. Returns the actual byte count freed.
// Leased paths are skipped. Used by fallbackCleanupWithTarget as the
// priority-zero eviction set before walking warm files through the
// freeze candidate flow.
func (sm *StorageManager) evictBlockCaches(bytesTarget uint64) uint64 {
	type blockDirCandidate struct {
		path    string
		bytes   uint64
		modTime time.Time
	}
	var candidates []blockDirCandidate
	for _, sub := range []string{"vod", "clips"} {
		root := filepath.Join(sm.basePath, sub)
		_ = filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error { //nolint:errcheck // missing/unreadable dirs just mean no candidates
			if walkErr != nil || info == nil {
				return nil //nolint:nilerr // skip unreadable nodes, continue walking siblings
			}
			if !info.IsDir() || !strings.HasSuffix(path, ".blocks") {
				if info.IsDir() && path != root {
					return nil
				}
				return nil
			}
			if tracker := leases.GlobalTracker(); tracker != nil && tracker.IsPathLeased(path) {
				return filepath.SkipDir
			}
			var dirBytes uint64
			_ = filepath.Walk(path, func(_ string, fi os.FileInfo, innerErr error) error { //nolint:errcheck // size defaults to 0 on walk failure
				if innerErr == nil && fi != nil && !fi.IsDir() {
					dirBytes += uint64(fi.Size())
				}
				return nil
			})
			// Use HeatTracker.LastAccessed when the .blocks dir has been
			// read warm — repeated playback should keep block caches
			// off the eviction list ahead of cold caches with newer
			// mtime but no actual viewer interest.
			lastAccessed := info.ModTime()
			if heat := leases.GlobalHeat(); heat != nil {
				if h, ok := heat.Lookup(path); ok && h.LastAccessed.After(lastAccessed) {
					lastAccessed = h.LastAccessed
				}
			}
			candidates = append(candidates, blockDirCandidate{path: path, bytes: dirBytes, modTime: lastAccessed})
			return filepath.SkipDir
		})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].modTime.Before(candidates[j].modTime) })

	localEvictionPasses.Inc()
	var freed uint64
	for _, c := range candidates {
		if freed >= bytesTarget {
			break
		}
		if err := os.RemoveAll(c.path); err != nil {
			sm.logger.WithError(err).WithField("path", c.path).Warn("Failed to evict relay block cache dir")
			continue
		}
		freed += c.bytes
		localEvictionBytes.Add(float64(c.bytes))
		sm.logger.WithFields(logging.Fields{
			"path":  c.path,
			"bytes": c.bytes,
		}).Info("Evicted relay block cache under pressure")
	}
	return freed
}

// parseHLSManifest parses an HLS manifest to extract segment names and durations.
// This is used during freeze to preserve the original manifest metadata
// instead of regenerating with hardcoded values.
func parseHLSManifest(content string) (*ParsedManifest, error) {
	result := &ParsedManifest{
		TargetDuration: 6, // default fallback
	}

	lines := strings.Split(content, "\n")
	var pendingDuration float64

	for _, line := range lines {
		line = strings.TrimSpace(line)

		if val, ok := strings.CutPrefix(line, "#EXT-X-TARGETDURATION:"); ok {
			if d, err := strconv.Atoi(strings.TrimSpace(val)); err == nil {
				result.TargetDuration = d
			}
		} else if val, ok := strings.CutPrefix(line, "#EXTINF:"); ok {
			// Parse duration from "#EXTINF:6.000," or "#EXTINF:6,"
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

func (sm *StorageManager) getStorageUsage(path string) (float64, uint64, uint64, error) {
	space, err := storage.EffectiveDiskSpace(path, sm.capacity)
	if err != nil {
		return 0, 0, 0, err
	}
	totalBytes := space.TotalBytes
	freeBytes := space.AvailableBytes
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
	if !leases.IsDestructiveCleanupAllowed() {
		sm.logger.Debug("fallbackCleanup skipped: destructive cleanup paused")
		return nil
	}
	targetBytes := uint64(float64(totalBytes) * sm.targetThreshold)
	if usedBytes <= targetBytes {
		return nil
	}
	bytesToFree := usedBytes - targetBytes
	return sm.fallbackCleanupWithTarget(clipsDir, bytesToFree)
}

// fallbackCleanupWithTarget runs the same eviction loop as fallbackCleanup but
// with an explicit byte target. Used by the disk-write admission path
// (admitDiskWrite / ensureRoomForDiskWrite) which knows exactly how much room
// it needs and does not want to aggressively trim back to targetThreshold.
func (sm *StorageManager) fallbackCleanupWithTarget(clipsDir string, bytesToFree uint64) error {
	if !leases.IsDestructiveCleanupAllowed() {
		sm.logger.Debug("fallbackCleanupWithTarget skipped: destructive cleanup paused")
		return nil
	}
	if bytesToFree == 0 {
		return nil
	}

	// First pass: evict relay block caches under vod/ and clips/. These
	// are best-effort local caches rebuildable from S3; per the admission
	// priority order (DVRRecording / ProcessingOutput > PlaybackCache),
	// they must lose first when a higher-priority intent needs disk. No
	// Foghorn safe-to-delete check is needed — block caches are not
	// authoritative storage. Skips leased paths.
	if freed := sm.evictBlockCaches(bytesToFree); freed > 0 {
		if freed >= bytesToFree {
			return nil
		}
		bytesToFree -= freed
	}

	// Active-DVR-first pass. getFreezeCandidates skips active DVR hashes (so
	// emergency cleanup never RemoveAlls an active recording's directory),
	// which means any "evict from active DVR under pressure" decision must
	// happen here, before the candidate loop. We ask Foghorn for the
	// authoritative list of safe-to-evict segments per active DVR and let
	// EvictUploadedSegments delete individual .ts files.
	for activeHash := range control.GetActiveDVRHashes() {
		evicted := sm.dropPressuredDVRSegments(activeHash)
		if evicted > 0 {
			sm.logger.WithFields(logging.Fields{
				"dvr_hash":         activeHash,
				"segments_evicted": evicted,
			}).Info("Evicted segments from active DVR under storage pressure")
		}
	}

	candidates, err := sm.getFreezeCandidates(clipsDir, AssetTypeClip)
	if err != nil {
		return err
	}

	// Skip VOD when any source lease is degraded: the lease cannot point
	// at a specific file (internal_name → artifact_hash unresolved on
	// this node) and the candidate list is LRU/heat-ordered, so we'd
	// happily evict the file Mist is actively reading. Clips/DVR can
	// still be reclaimed.
	if tracker := leases.GlobalTracker(); tracker == nil || !tracker.DegradedVodCleanupActive() {
		vodDir := filepath.Join(sm.basePath, "vod")
		vodCandidates, err := sm.getFreezeCandidates(vodDir, AssetTypeVOD)
		if err == nil {
			candidates = append(candidates, vodCandidates...)
		}
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
		safeToDelete, reason, warmDurationMs, err := sm.requestCanDelete(ctx, candidate.AssetHash)
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
				deleteErr = leases.DeleteFileIfUnleased(candidate.FilePath)
				if errors.Is(deleteErr, leases.ErrLeaseHeld) {
					sm.logger.WithField("file", candidate.FilePath).Info("fallbackCleanup skipped: lease held")
					continue
				}
				if deleteErr == nil {
					// Clean up auxiliary files after main file deletion succeeds.
					_ = os.Remove(candidate.FilePath + ".dtsh")
					_ = os.Remove(candidate.FilePath + ".gop")
				}
			} else {
				// DVR: never RemoveAll an active DVR directory. For inactive
				// DVRs the whole tree may be reclaimed; for active ones, only
				// individual uploaded segments outside the rolling window are
				// safe to evict; drive that via the segment-level path.
				if control.IsActiveDVR(candidate.AssetHash) {
					evicted := sm.dropPressuredDVRSegments(candidate.AssetHash)
					if evicted > 0 {
						sm.logger.WithFields(logging.Fields{
							"dvr_hash":         candidate.AssetHash,
							"segments_evicted": evicted,
						}).Info("Evicted DVR segments under storage pressure")
					}
					// Skip the directory-level delete and keep iterating. The
					// freed-bytes accounting catches up via subsequent passes.
					continue
				}
				deleteErr = leases.DeleteDVRDirIfUnleased(candidate.FilePath, candidate.AssetHash)
				if errors.Is(deleteErr, leases.ErrLeaseHeld) {
					sm.logger.WithField("dvr_hash", candidate.AssetHash).Info("fallbackCleanup skipped: DVR chapter lease held")
					continue
				}
			}

			if deleteErr != nil {
				sm.logger.WithError(deleteErr).WithField("asset_hash", candidate.AssetHash).Warn("Failed to delete local copy")
				errStr := deleteErr.Error()
				_ = sm.sendStorageLifecycle(&ipcpb.StorageLifecycleData{ //nolint:errcheck // best-effort report
					Action:    ipcpb.StorageLifecycleData_ACTION_EVICT_FAILED,
					AssetType: string(candidate.AssetType),
					AssetHash: candidate.AssetHash,
					SizeBytes: candidate.SizeBytes,
					Error:     &errStr,
				})
				continue
			}

			// Notify deletion (eviction from local cache) with warm duration metric
			_ = sm.sendStorageLifecycle(&ipcpb.StorageLifecycleData{ //nolint:errcheck // best-effort report
				Action:         ipcpb.StorageLifecycleData_ACTION_EVICTED,
				AssetType:      string(candidate.AssetType),
				AssetHash:      candidate.AssetHash,
				SizeBytes:      candidate.SizeBytes,
				WarmDurationMs: &warmDurationMs,
			})
			_ = sm.sendArtifactDeleted(candidate.AssetHash, candidate.FilePath, "eviction", string(candidate.AssetType), candidate.SizeBytes)

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
func (sm *StorageManager) SyncDtshOnly(ctx context.Context, req *ipcpb.DtshSyncRequest) error {
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

		if err := dtsh.ValidateFile(dtshPath); err != nil {
			return fmt.Errorf(".dtsh file not valid at %s: %w", dtshPath, err)
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
	} else if assetType == "vod" {
		// Foghorn may send a storage-relative path (vod/<hash>.<ext>) for
		// catch-up triggers where there's no warm artifact report yet to
		// supply an absolute one. Join against the local storage root so
		// the stat and any GenerateDTSH side-effects all land in the
		// same place Mist writes to.
		resolvedPath := localPath
		if !filepath.IsAbs(resolvedPath) {
			resolvedPath = filepath.Join(config.GetStoragePath(), resolvedPath)
		}
		dtshPath := resolvedPath + ".dtsh"
		presignedURL := req.GetPresignedPutUrl()
		if presignedURL == "" {
			return fmt.Errorf("no presigned URL provided for vod .dtsh")
		}
		// On-demand generation: if Foghorn is asking us to sync .dtsh for
		// a VOD artifact we haven't generated one for yet (chapter
		// finalization where the inline DTSH boot missed), or the local
		// sidecar is corrupt, boot the asset now so Mist rewrites it.
		if err := dtsh.ValidateFile(dtshPath); err != nil {
			reason := "missing"
			if !os.IsNotExist(err) {
				reason = "invalid"
				sm.logger.WithError(err).WithField("dtsh_path", dtshPath).Warn("Removing invalid VOD .dtsh before on-demand regeneration")
				if removeErr := os.Remove(dtshPath); removeErr != nil && !os.IsNotExist(removeErr) {
					return fmt.Errorf("remove invalid .dtsh at %s: %w", dtshPath, removeErr)
				}
			}
			vodStreamName := "vod+" + assetHash
			if genErr := GenerateDTSHForPath(os.Getenv("MISTSERVER_URL"), vodStreamName, dtshPath, sm.logger.WithField("asset_hash", assetHash)); genErr != nil {
				return fmt.Errorf("dtsh %s and on-demand generation failed: %w", reason, genErr)
			}
		}
		if err := dtsh.ValidateFile(dtshPath); err != nil {
			return fmt.Errorf(".dtsh file not valid at %s: %w", dtshPath, err)
		}
		if err := sm.presignedClient.UploadFileToPresignedURL(ctx, presignedURL, dtshPath, nil); err != nil {
			uploadErr = fmt.Errorf("failed to upload vod .dtsh: %w", err)
		} else {
			dtshUploaded = true
			sm.logger.WithFields(logging.Fields{
				"asset_hash": assetHash,
				"dtsh_path":  dtshPath,
			}).Info("Uploaded vod .dtsh file")
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
			if err := dtsh.ValidateFile(dtshPath); err != nil {
				if !os.IsNotExist(err) {
					sm.logger.WithError(err).WithField("dtsh_name", dtshName).Warn("Skipping invalid DVR .dtsh file")
				}
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
		// .dtsh sync — if the source file is gone, surface as local_missing.
		_ = sm.sendSyncComplete(requestID, assetHash, "failed", "", 0, uploadErr.Error(), false, errors.Is(uploadErr, fs.ErrNotExist)) //nolint:errcheck // best-effort report
		return uploadErr
	}

	// Send success notification with dtsh_included=true
	_ = sm.sendSyncComplete(requestID, assetHash, "success", "", 0, "", dtshUploaded, false) //nolint:errcheck // best-effort report

	sm.logger.WithFields(logging.Fields{
		"request_id": requestID,
		"asset_hash": assetHash,
		"asset_type": assetType,
	}).Info("Incremental .dtsh sync completed")

	return nil
}
