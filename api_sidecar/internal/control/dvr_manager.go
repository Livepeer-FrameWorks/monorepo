package control

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"frameworks/pkg/logging"
	"frameworks/pkg/mist"
	pb "frameworks/pkg/proto"

	"google.golang.org/protobuf/types/known/timestamppb"
)

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
		storagePath := os.Getenv("HELMSMAN_STORAGE_LOCAL_PATH")
		if storagePath == "" {
			storagePath = "/tmp/helmsman_storage"
		}

		dvrManager = &DVRManager{
			logger:      logger,
			jobs:        make(map[string]*DVRJob),
			storagePath: storagePath,
			mistClient:  mist.NewClient(logger),
		}

		logger.WithField("storage_path", storagePath).Info("DVR manager initialized")
	})
}

// StartRecording starts a new DVR recording job
func (dm *DVRManager) StartRecording(dvrHash, internalName, sourceURL string, config *pb.DVRConfig, sendFunc func(*pb.ControlMessage)) error {
	dm.mutex.Lock()
	defer dm.mutex.Unlock()

	// Check if already recording
	if _, exists := dm.jobs[dvrHash]; exists {
		return fmt.Errorf("DVR recording already active for hash %s", dvrHash)
	}

	// Create output directory
	outputDir := filepath.Join(dm.storagePath, "dvr", internalName)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create DVR output directory: %v", err)
	}

	// Create DVR job
	job := &DVRJob{
		DVRHash:      dvrHash,
		InternalName: internalName,
		SourceURL:    sourceURL,
		Config:       config,
		StartTime:    time.Now(),
		OutputDir:    outputDir,
		ManifestPath: filepath.Join(outputDir, fmt.Sprintf("%s.m3u8", dvrHash)),
		SendFunc:     sendFunc,
		Logger:       dm.logger,
		Status:       "starting",
		MaxRetries:   MaxDVRRetries,
		RetryCount:   0,
	}

	// Start the recording process via MistServer push
	if err := dm.startDVRPush(job); err != nil {
		return fmt.Errorf("failed to start DVR push: %v", err)
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
	// Format: /path/segments/$minute_$segmentCounter.ts?m3u8=../../dvrhash.m3u8&split=6&targetAge=7200&append=1&noendlist=1
	targetURI := fmt.Sprintf("%s/%s/$minute_$segmentCounter.ts?m3u8=../../%s.m3u8&split=%d&targetAge=%d&append=1&noendlist=1",
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
		return fmt.Errorf("failed to create initial push: %v", err)
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

// monitorJob monitors a DVR job's progress
func (dm *DVRManager) monitorJob(job *DVRJob) {
	progressTicker := time.NewTicker(30 * time.Second) // Progress updates every 30s
	pushTicker := time.NewTicker(PushMonitorInterval)  // Push monitoring every 5s
	defer progressTicker.Stop()
	defer pushTicker.Stop()

	for {
		select {
		case <-progressTicker.C:
			dm.mutex.RLock()
			_, exists := dm.jobs[job.DVRHash]
			dm.mutex.RUnlock()

			if !exists {
				return // Job completed or stopped
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

		case <-time.After(5 * time.Minute): // Timeout if no updates
			if job.Status == "starting" {
				job.Logger.Warn("DVR job timeout during startup")
				dm.StopRecording(job.DVRHash)
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

// logWriter implements io.Writer for process logging
type logWriter struct {
	logger logging.Logger
	level  string
}

func (lw *logWriter) Write(p []byte) (n int, err error) {
	msg := string(p)
	switch lw.level {
	case "error":
		lw.logger.Error(msg)
	default:
		lw.logger.Info(msg)
	}
	return len(p), nil
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
