package control

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	sidecarcfg "frameworks/api_sidecar/internal/config"
	"frameworks/api_sidecar/internal/storage"
	"frameworks/pkg/logging"
	"frameworks/pkg/mist"
	pb "frameworks/pkg/proto"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// HTTP client for S3 presigned URL uploads
var httpClient = &http.Client{
	Timeout: 2 * time.Minute,
}

// newHTTPRequest creates an HTTP request with context
func newHTTPRequest(ctx context.Context, method, url string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	return req, nil
}

// DVR push retry constants
const (
	MaxDVRRetries       = 10               // Maximum push recreation attempts
	InitialRetryDelay   = 5 * time.Second  // Initial delay between retries
	MaxRetryDelay       = 60 * time.Second // Maximum delay between retries
	PushMonitorInterval = 5 * time.Second  // How often to check push status
)

// DVRJob represents a running DVR recording session
type DVRJob struct {
	DVRHash      string
	InternalName string
	SourceURL    string
	Config       *pb.DVRConfig
	StartTime    time.Time
	PushID       int // MistServer push ID
	OutputDir    string
	ManifestPath string
	SendFunc     func(*pb.ControlMessage)
	Logger       logging.Logger

	// Progress tracking
	SegmentCount   int
	TotalSizeBytes uint64
	Status         string

	// Retry logic
	RetryCount      int
	LastPushAttempt time.Time
	MaxRetries      int
	TargetURI       string // Store for recreation
	StreamName      string // Store for recreation

	// Dual-storage: Incremental sync tracking
	SyncedSegments map[string]bool // Track which segments already synced to S3
	syncMutex      sync.Mutex      // Protects SyncedSegments
}

// DVRManager manages active DVR recording sessions
type DVRManager struct {
	logger      logging.Logger
	jobs        map[string]*DVRJob // DVR hash -> job
	mutex       sync.RWMutex
	storagePath string
	mistClient  *mist.Client
}

// Global DVR manager instance
var dvrManager *DVRManager
var dvrManagerOnce sync.Once

// initDVRManager initializes the global DVR manager
func initDVRManager() {
	dvrManagerOnce.Do(func() {
		logger := logging.NewLoggerWithService("dvr-manager")
		storagePath := sidecarcfg.GetStoragePath()

		dvrManager = &DVRManager{
			logger:      logger,
			jobs:        make(map[string]*DVRJob),
			storagePath: storagePath,
			mistClient:  mist.NewClient(logger),
		}

		logger.WithField("storage_path", storagePath).Info("DVR manager initialized")
	})
}

// GetDVRManager returns the global DVR manager instance
func GetDVRManager() *DVRManager {
	return dvrManager
}

// GetActiveDVRHashes returns DVR hashes that are currently recording.
func GetActiveDVRHashes() map[string]bool {
	if dvrManager == nil {
		return map[string]bool{}
	}

	dvrManager.mutex.RLock()
	defer dvrManager.mutex.RUnlock()

	hashes := make(map[string]bool, len(dvrManager.jobs))
	for hash := range dvrManager.jobs {
		hashes[hash] = true
	}
	return hashes
}

// HandleNewSegment handles a RECORDING_SEGMENT trigger for immediate sync
func (dm *DVRManager) HandleNewSegment(streamName, filePath string) {
	dm.mutex.RLock()
	defer dm.mutex.RUnlock()

	// Find job matching the stream name
	var targetJob *DVRJob
	for _, job := range dm.jobs {
		if job.StreamName == streamName {
			targetJob = job
			break
		}
	}

	if targetJob == nil {
		// Not tracking this stream or not an active DVR
		return
	}

	// Verify file is within output directory to avoid path traversal
	if !strings.HasPrefix(filePath, targetJob.OutputDir) {
		dm.logger.WithFields(logging.Fields{
			"stream":     streamName,
			"file_path":  filePath,
			"output_dir": targetJob.OutputDir,
		}).Warn("Received RECORDING_SEGMENT for file outside DVR output directory")
		return
	}

	// Trigger sync for this specific segment
	go dm.syncSpecificSegment(targetJob, filePath)
}

// syncSpecificSegment syncs a single segment file to S3
func (dm *DVRManager) syncSpecificSegment(job *DVRJob, filePath string) {
	if !IsConnected() {
		return
	}

	segName := filepath.Base(filePath)

	// Check if already synced
	job.syncMutex.Lock()
	if job.SyncedSegments[segName] {
		job.syncMutex.Unlock()
		return
	}
	job.syncMutex.Unlock()

	// Get segment size
	info, err := os.Stat(filePath)
	if err != nil {
		job.Logger.WithError(err).WithField("segment", segName).Warn("Segment file not found for sync")
		return
	}

	// Request sync permission
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Use relative path for hash construction to match polling logic: dvrHash/filename
	resp, err := RequestFreezePermission(ctx, "dvr_segment", job.DVRHash+"/"+segName, filePath, uint64(info.Size()), []string{segName})
	if err != nil {
		job.Logger.WithError(err).WithField("segment", segName).Warn("Failed to request segment sync permission")
		return
	}

	if !resp.Approved {
		if resp.Reason == "already_synced" {
			job.syncMutex.Lock()
			job.SyncedSegments[segName] = true
			job.syncMutex.Unlock()
		}
		return
	}

	presignedURL := resp.SegmentUrls[segName]
	if presignedURL == "" {
		presignedURL = resp.PresignedPutUrl
	}

	if presignedURL == "" {
		job.Logger.WithField("segment", segName).Warn("No presigned URL provided for segment sync")
		return
	}

	// Upload
	err = dm.uploadSegmentToS3(ctx, filePath, presignedURL)
	if err != nil {
		job.Logger.WithError(err).WithField("segment", segName).Warn("Failed to upload segment to S3")
		return
	}

	// Mark as synced
	job.syncMutex.Lock()
	job.SyncedSegments[segName] = true
	job.syncMutex.Unlock()

	job.Logger.WithFields(logging.Fields{
		"segment":  segName,
		"size_kb":  info.Size() / 1024,
		"dvr_hash": job.DVRHash,
		"trigger":  "RECORDING_SEGMENT",
	}).Debug("DVR segment synced to S3 via trigger")

	// Opportunistically sync manifest
	dm.syncManifest(job)
}

// StartRecording starts a new DVR recording job
func (dm *DVRManager) StartRecording(dvrHash, streamID, internalName, sourceURL string, config *pb.DVRConfig, sendFunc func(*pb.ControlMessage)) error {
	dm.mutex.Lock()
	defer dm.mutex.Unlock()

	// Check if already recording
	if _, exists := dm.jobs[dvrHash]; exists {
		return fmt.Errorf("DVR recording already active for hash %s", dvrHash)
	}

	if err := os.MkdirAll(dm.storagePath, 0755); err != nil {
		return err
	}
	if err := storage.HasSpaceFor(dm.storagePath, 0); err != nil {
		return fmt.Errorf("insufficient disk space for DVR recording: %w", err)
	}

	// Create output directory: /storage/dvr/{stream_id}/{dvr_hash}/
	outputDir := filepath.Join(dm.storagePath, "dvr", streamID, dvrHash)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create DVR output directory: %w", err)
	}

	// Create DVR job
	job := &DVRJob{
		DVRHash:        dvrHash,
		InternalName:   internalName,
		SourceURL:      sourceURL,
		Config:         config,
		StartTime:      time.Now(),
		OutputDir:      outputDir,
		ManifestPath:   filepath.Join(outputDir, fmt.Sprintf("%s.m3u8", dvrHash)),
		SendFunc:       sendFunc,
		Logger:         dm.logger,
		Status:         "starting",
		MaxRetries:     MaxDVRRetries,
		RetryCount:     0,
		SyncedSegments: make(map[string]bool), // Initialize sync tracking
	}

	// Start the recording process via MistServer push
	if err := dm.startDVRPush(job); err != nil {
		// Cleanup created directory on failure
		if rmErr := os.RemoveAll(outputDir); rmErr != nil {
			dm.logger.WithError(rmErr).WithField("path", outputDir).Warn("Failed to cleanup DVR directory after failed start")
		}
		return fmt.Errorf("failed to start DVR push: %w", err)
	}

	// Store job
	dm.jobs[dvrHash] = job

	// Start progress monitoring
	go dm.monitorJob(job)

	job.Logger.Info("DVR recording job started")
	return nil
}

// StopRecording stops a DVR recording job
func (dm *DVRManager) StopRecording(dvrHash string) error {
	dm.mutex.Lock()
	defer dm.mutex.Unlock()

	job, exists := dm.jobs[dvrHash]
	if !exists {
		return fmt.Errorf("DVR recording not found for hash %s", dvrHash)
	}

	// Stop the MistServer push if running
	if job.PushID > 0 {
		if err := dm.mistClient.PushStop(job.PushID); err != nil {
			job.Logger.WithError(err).Warn("Failed to stop MistServer push")
		}
	}

	// Update status
	job.Status = "stopped"

	// Send completion notification
	dm.sendCompletion(job, "stopped", "")

	// Remove from active jobs
	delete(dm.jobs, dvrHash)

	job.Logger.Info("DVR recording job stopped")
	return nil
}

// startDVRPush starts DVR recording via MistServer push API
func (dm *DVRManager) startDVRPush(job *DVRJob) error {
	// Build DVR target URI with proper parameters
	segmentDuration := int(job.Config.GetSegmentDuration())
	if segmentDuration <= 0 {
		segmentDuration = 6 // Default 6 seconds
	}

	retentionSeconds := 7200 // 2 hours default
	if retention := int(job.Config.GetRetentionDays()); retention > 0 {
		retentionSeconds = retention * 24 * 3600
	}

	// Build DVR target path
	// Segments go to {outputDir}/segments/, manifest at {outputDir}/{hash}.m3u8
	// From segments/, ../ goes to outputDir where manifest lives
	targetURI := fmt.Sprintf("%s/%s/$minute_$segmentCounter.ts?m3u8=../%s.m3u8&split=%d&targetAge=%d&append=1&noendlist=1",
		job.OutputDir,
		"segments",
		job.DVRHash,
		segmentDuration,
		retentionSeconds,
	)

	// Store for recreation attempts
	streamName := fmt.Sprintf("live+%s", job.InternalName)
	job.TargetURI = targetURI
	job.StreamName = streamName

	// Attempt to create push
	pushID, err := dm.createOrRecreatePush(job)
	if err != nil {
		return fmt.Errorf("failed to create initial push: %w", err)
	}

	job.PushID = pushID
	job.Status = "recording"
	job.Logger.WithFields(logging.Fields{
		"push_id": pushID,
		"stream":  streamName,
		"target":  targetURI,
	}).Info("Started DVR recording via MistServer push")

	return nil
}

// monitorJob monitors a DVR job's progress and performs incremental sync
func (dm *DVRManager) monitorJob(job *DVRJob) {
	progressTicker := time.NewTicker(30 * time.Second) // Progress updates every 30s
	pushTicker := time.NewTicker(PushMonitorInterval)  // Push monitoring every 5s
	syncTicker := time.NewTicker(10 * time.Second)     // Incremental sync every 10s
	defer progressTicker.Stop()
	defer pushTicker.Stop()
	defer syncTicker.Stop()

	for {
		select {
		case <-progressTicker.C:
			dm.mutex.RLock()
			_, exists := dm.jobs[job.DVRHash]
			dm.mutex.RUnlock()

			if !exists {
				return // Job completed or stopped
			}

			if err := storage.HasSpaceFor(dm.storagePath, 0); err != nil {
				job.Logger.WithError(err).Error("Stopping DVR recording due to insufficient disk space")
				if job.PushID > 0 {
					if stopErr := dm.mistClient.PushStop(job.PushID); stopErr != nil {
						job.Logger.WithError(stopErr).Warn("Failed to stop MistServer push during disk-full shutdown")
					}
				}
				job.Status = "failed"
				dm.sendCompletion(job, "failed", sanitizeDvrStorageError(err))
				dm.mutex.Lock()
				delete(dm.jobs, job.DVRHash)
				dm.mutex.Unlock()
				return
			}

			// Update progress and send notifications
			dm.updateProgress(job)

		case <-pushTicker.C:
			dm.mutex.RLock()
			_, exists := dm.jobs[job.DVRHash]
			dm.mutex.RUnlock()

			if !exists {
				return // Job completed or stopped
			}

			// Check and maintain push status
			dm.maintainPushStatus(job)

		case <-syncTicker.C:
			dm.mutex.RLock()
			_, exists := dm.jobs[job.DVRHash]
			dm.mutex.RUnlock()

			if !exists {
				return // Job completed or stopped
			}

			// Dual-storage: Sync new segments to S3
			dm.syncNewSegments(job)

		case <-time.After(5 * time.Minute): // Timeout if no updates
			if job.Status == "starting" {
				job.Logger.Warn("DVR job timeout during startup")
				if err := dm.StopRecording(job.DVRHash); err != nil {
					job.Logger.WithError(err).Warn("Failed to stop timed-out DVR job")
				}
				return
			}
		}
	}
}

// updateProgress updates job progress and sends notification
func (dm *DVRManager) updateProgress(job *DVRJob) {
	// Check output directory for segments
	segmentCount := 0
	totalSize := uint64(0)

	// Check segments directory specifically
	segmentsDir := filepath.Join(job.OutputDir, "segments")
	if entries, err := os.ReadDir(segmentsDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				info, err := entry.Info()
				if err == nil {
					totalSize += uint64(info.Size())
					if filepath.Ext(entry.Name()) == ".ts" || filepath.Ext(entry.Name()) == ".m4s" {
						segmentCount++
					}
				}
			}
		}
	}

	job.SegmentCount = segmentCount
	job.TotalSizeBytes = totalSize

	// Send progress update
	if job.SendFunc != nil {
		progress := &pb.DVRProgress{
			DvrHash:      job.DVRHash,
			Status:       job.Status,
			SegmentCount: int32(segmentCount),
			SizeBytes:    totalSize,
			Message:      fmt.Sprintf("Recording %d segments", segmentCount),
		}

		msg := &pb.ControlMessage{
			SentAt:  timestamppb.Now(),
			Payload: &pb.ControlMessage_DvrProgress{DvrProgress: progress},
		}
		job.SendFunc(msg)
	}
}

// maintainPushStatus intelligently maintains push status with retry logic
func (dm *DVRManager) maintainPushStatus(job *DVRJob) {
	if job.Status == "stopped" || job.Status == "completed" || job.Status == "failed" {
		return // Don't maintain completed/failed jobs
	}

	// Check if push still exists in push list
	pushes, err := dm.mistClient.PushList()
	if err != nil {
		job.Logger.WithError(err).Warn("Failed to check push status")
		return
	}

	// Look for our push
	pushFound := false
	pushHasErrors := false
	for _, push := range pushes {
		if push.ID == job.PushID {
			pushFound = true

			// Check push logs for recoverable vs non-recoverable errors
			for _, log := range push.Logs {
				logLower := strings.ToLower(log)
				if strings.Contains(logLower, "error") || strings.Contains(logLower, "failed") {
					// Log error but don't immediately fail - could be transient DTSC issue
					job.Logger.WithField("push_log", log).Debug("Push error detected, may retry")
					pushHasErrors = true
				}
			}
			break
		}
	}

	// If push missing or has errors, attempt recreation (unless we've exceeded retries)
	if (!pushFound || pushHasErrors) && job.RetryCount < job.MaxRetries {
		// Calculate backoff delay
		retryDelay := dm.calculateRetryDelay(job.RetryCount)
		if time.Since(job.LastPushAttempt) < retryDelay {
			return // Not time to retry yet
		}

		job.Logger.WithFields(logging.Fields{
			"retry_count": job.RetryCount,
			"push_found":  pushFound,
			"has_errors":  pushHasErrors,
		}).Info("Recreating DVR push due to failure or absence")

		// Attempt to recreate push
		newPushID, err := dm.createOrRecreatePush(job)
		if err != nil {
			job.Logger.WithError(err).WithField("retry_count", job.RetryCount).Warn("Failed to recreate push")
			job.RetryCount++
			job.LastPushAttempt = time.Now()
			return
		}

		// Update job with new push ID
		job.PushID = newPushID
		job.RetryCount++
		job.LastPushAttempt = time.Now()
		job.Logger.WithFields(logging.Fields{
			"new_push_id": newPushID,
			"retry_count": job.RetryCount,
		}).Info("Successfully recreated DVR push")

		return
	}

	// If push disappeared and we've exhausted retries, fail the job
	if !pushFound && job.RetryCount >= job.MaxRetries {
		dm.mutex.Lock()
		defer dm.mutex.Unlock()

		if job, exists := dm.jobs[job.DVRHash]; exists {
			job.Logger.WithField("retry_count", job.RetryCount).Error("DVR push failed after maximum retries")
			job.Status = "failed"
			dm.sendCompletion(job, "failed", fmt.Sprintf("Push failed after %d retries", job.RetryCount))
			delete(dm.jobs, job.DVRHash)
		}
		return
	}

	// If push disappeared but we have segments, recording might have completed naturally
	if !pushFound && job.SegmentCount > 0 {
		dm.mutex.Lock()
		defer dm.mutex.Unlock()

		if job, exists := dm.jobs[job.DVRHash]; exists {
			job.Logger.Info("DVR recording completed successfully")
			job.Status = "completed"
			dm.sendCompletion(job, "success", "")
			delete(dm.jobs, job.DVRHash)
		}
	}
}

// createOrRecreatePush creates or recreates a MistServer push for DVR recording
func (dm *DVRManager) createOrRecreatePush(job *DVRJob) (int, error) {
	// First, try to stop any existing push with the same stream/target
	pushes, err := dm.mistClient.PushList()
	if err != nil {
		job.Logger.WithError(err).Debug("Could not list existing pushes for cleanup")
	} else {
		// Clean up any existing pushes for this stream/target combination
		for _, push := range pushes {
			if push.StreamName == job.StreamName && push.TargetURI == job.TargetURI {
				if err := dm.mistClient.PushStop(push.ID); err != nil {
					job.Logger.WithError(err).WithField("old_push_id", push.ID).Debug("Failed to stop old push")
				} else {
					job.Logger.WithField("old_push_id", push.ID).Debug("Cleaned up old push")
				}
			}
		}
	}

	// Create new push
	if err := dm.mistClient.PushStart(job.StreamName, job.TargetURI); err != nil {
		return 0, fmt.Errorf("failed to start push: %w", err)
	}

	// Find the newly created push ID
	pushes, err = dm.mistClient.PushList()
	if err != nil {
		return 0, fmt.Errorf("failed to get push list after creation: %w", err)
	}

	// Find our push by stream name and target
	for _, push := range pushes {
		if push.StreamName == job.StreamName && push.TargetURI == job.TargetURI {
			return push.ID, nil
		}
	}

	return 0, fmt.Errorf("failed to find created push in push list")
}

// calculateRetryDelay calculates exponential backoff delay for push retries
func (dm *DVRManager) calculateRetryDelay(retryCount int) time.Duration {
	// Exponential backoff: 5s, 10s, 20s, 40s, 60s (max)
	delay := InitialRetryDelay * time.Duration(1<<uint(retryCount))
	if delay > MaxRetryDelay {
		delay = MaxRetryDelay
	}
	return delay
}

// sendCompletion sends DVR completion notification
func (dm *DVRManager) sendCompletion(job *DVRJob, status string, errorMsg string) {
	if job.SendFunc == nil {
		return
	}

	durationSeconds := int32(time.Since(job.StartTime).Seconds())

	stopped := &pb.DVRStopped{
		DvrHash:         job.DVRHash,
		Status:          status,
		Error:           errorMsg,
		ManifestPath:    job.ManifestPath,
		DurationSeconds: durationSeconds,
		SizeBytes:       job.TotalSizeBytes,
	}

	msg := &pb.ControlMessage{
		SentAt:  timestamppb.Now(),
		Payload: &pb.ControlMessage_DvrStopped{DvrStopped: stopped},
	}
	job.SendFunc(msg)
}

func sanitizeDvrStorageError(err error) string {
	if storage.IsInsufficientSpace(err) {
		return "Recording stopped: storage node out of space"
	}
	return "Recording stopped: storage error"
}

// GetActiveJobs returns information about active DVR jobs
func (dm *DVRManager) GetActiveJobs() map[string]string {
	dm.mutex.RLock()
	defer dm.mutex.RUnlock()

	jobs := make(map[string]string)
	for hash, job := range dm.jobs {
		jobs[hash] = job.Status
	}
	return jobs
}

// syncNewSegments performs incremental sync of DVR segments to S3
// MistServer writes playlist BEFORE starting next segment, so segments in manifest are "sealed"
func (dm *DVRManager) syncNewSegments(job *DVRJob) {
	if !IsConnected() {
		return // No Foghorn connection, skip sync
	}

	// Read and parse manifest to get segment list
	segments, err := dm.parseManifestSegments(job.ManifestPath)
	if err != nil {
		// Manifest may not exist yet during early recording
		return
	}

	if len(segments) == 0 {
		return
	}

	// Check for new segments (not yet synced)
	job.syncMutex.Lock()
	var newSegments []string
	for _, seg := range segments {
		if !job.SyncedSegments[seg] {
			newSegments = append(newSegments, seg)
		}
	}
	job.syncMutex.Unlock()

	if len(newSegments) == 0 {
		return // All segments already synced
	}

	job.Logger.WithFields(logging.Fields{
		"new_segments": len(newSegments),
		"total_synced": len(job.SyncedSegments),
	}).Debug("Syncing new DVR segments to S3")

	// Request sync for each new segment
	segmentsDir := filepath.Join(job.OutputDir, "segments")
	for _, segName := range newSegments {
		segPath := filepath.Join(segmentsDir, segName)

		// Get segment size
		info, err := os.Stat(segPath)
		if err != nil {
			job.Logger.WithError(err).WithField("segment", segName).Debug("Segment not ready, skipping")
			continue
		}

		// Request sync permission and presigned URL from Foghorn
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		resp, err := RequestFreezePermission(ctx, "dvr_segment", job.DVRHash+"/"+segName, segPath, uint64(info.Size()), []string{segName})
		cancel()

		if err != nil {
			job.Logger.WithError(err).WithField("segment", segName).Warn("Failed to request segment sync permission")
			continue
		}

		if !resp.Approved {
			// Could be already synced or rate limited - check reason
			if resp.Reason == "already_synced" {
				job.syncMutex.Lock()
				job.SyncedSegments[segName] = true
				job.syncMutex.Unlock()
			}
			continue
		}

		// Get presigned URL for segment
		presignedURL := resp.SegmentUrls[segName]
		if presignedURL == "" {
			presignedURL = resp.PresignedPutUrl // Fallback for single file
		}

		if presignedURL == "" {
			job.Logger.WithField("segment", segName).Warn("No presigned URL provided for segment sync")
			continue
		}

		// Upload segment using presigned URL
		// NOTE: We use the storage package's presigned client for actual upload
		// For now, we mark intent to sync - actual upload delegated to storage manager
		err = dm.uploadSegmentToS3(ctx, segPath, presignedURL)
		if err != nil {
			job.Logger.WithError(err).WithField("segment", segName).Warn("Failed to upload segment to S3")
			continue
		}

		// Mark as synced
		job.syncMutex.Lock()
		job.SyncedSegments[segName] = true
		job.syncMutex.Unlock()

		job.Logger.WithFields(logging.Fields{
			"segment":  segName,
			"size_kb":  info.Size() / 1024,
			"dvr_hash": job.DVRHash,
		}).Debug("DVR segment synced to S3")
	}

	// Also sync the manifest itself periodically (every 5 segments or so)
	if len(job.SyncedSegments) > 0 && len(job.SyncedSegments)%5 == 0 {
		dm.syncManifest(job)
	}
}

// parseManifestSegments extracts segment filenames from an HLS manifest
func (dm *DVRManager) parseManifestSegments(manifestPath string) ([]string, error) {
	file, err := os.Open(manifestPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var segments []string
	scanner := bufio.NewScanner(file)
	var pendingExtinf bool

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if strings.HasPrefix(line, "#EXTINF:") {
			pendingExtinf = true
			continue
		}

		if pendingExtinf && !strings.HasPrefix(line, "#") && line != "" {
			// This is a segment filename (may have path like "segments/foo.ts")
			segName := filepath.Base(line)
			// Strip query params if present
			if idx := strings.Index(segName, "?"); idx > 0 {
				segName = segName[:idx]
			}
			segments = append(segments, segName)
			pendingExtinf = false
		}
	}

	return segments, scanner.Err()
}

// uploadSegmentToS3 uploads a segment file using a presigned PUT URL
func (dm *DVRManager) uploadSegmentToS3(ctx context.Context, filePath, presignedURL string) error {
	// Use the storage package's presigned client
	// For now, defer to a simple HTTP PUT implementation
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open segment file: %w", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat segment file: %w", err)
	}

	req, err := newHTTPRequest(ctx, "PUT", presignedURL, file)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.ContentLength = info.Size()
	req.Header.Set("Content-Type", "video/MP2T")

	// Use storage presigned client if available, otherwise fall back to http.DefaultClient
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to upload to S3: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("S3 upload failed with status %d", resp.StatusCode)
	}

	return nil
}

// syncManifest uploads the current manifest to S3
func (dm *DVRManager) syncManifest(job *DVRJob) {
	if !IsConnected() {
		return
	}

	// Request presigned URL for manifest
	manifestName := job.DVRHash + ".m3u8"
	info, err := os.Stat(job.ManifestPath)
	if err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := RequestFreezePermission(ctx, "dvr_manifest", job.DVRHash+"/"+manifestName, job.ManifestPath, uint64(info.Size()), []string{manifestName})
	if err != nil || !resp.Approved {
		return
	}

	presignedURL := resp.SegmentUrls[manifestName]
	if presignedURL == "" {
		presignedURL = resp.PresignedPutUrl
	}

	if presignedURL == "" {
		return
	}

	// Upload manifest
	if err := dm.uploadManifestToS3(ctx, job.ManifestPath, presignedURL); err != nil {
		job.Logger.WithError(err).Debug("Failed to sync manifest to S3")
	}
}

// uploadManifestToS3 uploads a manifest file using a presigned PUT URL
func (dm *DVRManager) uploadManifestToS3(ctx context.Context, filePath, presignedURL string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return err
	}

	req, err := newHTTPRequest(ctx, "PUT", presignedURL, file)
	if err != nil {
		return err
	}
	req.ContentLength = info.Size()
	req.Header.Set("Content-Type", "application/vnd.apple.mpegurl")

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("manifest upload failed with status %d", resp.StatusCode)
	}

	return nil
}
