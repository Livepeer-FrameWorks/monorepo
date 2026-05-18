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
	"github.com/Livepeer-FrameWorks/monorepo/pkg/hls"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"

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
	MaxDVRRetries         = 10               // Maximum push recreation attempts
	InitialRetryDelay     = 5 * time.Second  // Initial delay between retries
	MaxRetryDelay         = 60 * time.Second // Maximum delay between retries
	PushMonitorInterval   = 5 * time.Second  // How often to check push status
	dvrEvictionBatchSize  = 500
	maxDVREvictionBatches = 10
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

// DVRMistClient abstracts MistServer push operations so tests can inject fakes.
type DVRMistClient interface {
	PushStart(streamName, targetURI string) error
	PushStop(pushID int) error
	PushList() ([]mist.PushInfo, error)
}

// DVRManager manages active DVR recording sessions
type DVRManager struct {
	logger      logging.Logger
	jobs        map[string]*DVRJob // DVR hash -> job
	mutex       sync.RWMutex
	storagePath string
	storageCap  uint64
	mistClient  DVRMistClient
	// diskCheck is the precondition called before starting/continuing a recording.
	// Tests inject a stub so they don't depend on host disk pressure.
	// Nil means use the production storage admission check.
	diskCheck func(path string, requiredBytes uint64) error
}

func (dm *DVRManager) hasSpaceFor(path string, requiredBytes uint64) error {
	if dm.diskCheck != nil {
		return dm.diskCheck(path, requiredBytes)
	}
	return storage.HasSpaceForWithinCapacity(path, requiredBytes, dm.storageCap)
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
			storageCap:  sidecarcfg.GetStorageCapacityBytes(),
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

// IsActiveDVR reports whether the given DVR hash is currently recording on
// this node. Required guard at every DVR cleanup site: an active DVR must
// never have its directory or its rolling manifest deleted, and unsynced
// segments may only be evicted with an explicit DVRSegmentDropped report.
func IsActiveDVR(dvrHash string) bool {
	if dvrManager == nil || dvrHash == "" {
		return false
	}
	dvrManager.mutex.RLock()
	defer dvrManager.mutex.RUnlock()
	_, ok := dvrManager.jobs[dvrHash]
	return ok
}

// LookupActiveDVR returns the active DVRJob for a hash, if any. Returns
// (nil, false) when the DVR is not active on this node.
func LookupActiveDVR(dvrHash string) (*DVRJob, bool) {
	if dvrManager == nil || dvrHash == "" {
		return nil, false
	}
	dvrManager.mutex.RLock()
	defer dvrManager.mutex.RUnlock()
	job, ok := dvrManager.jobs[dvrHash]
	return job, ok
}

// SegmentInRollingManifest reports whether a segment file is referenced by
// the active DVR's current rolling Mist manifest. Used as the third clause
// of the eviction predicate so we never delete a file that the live
// playlist still advertises.
func SegmentInRollingManifest(job *DVRJob, segmentName string) bool {
	if job == nil || job.ManifestPath == "" || segmentName == "" {
		return false
	}
	data, err := os.ReadFile(job.ManifestPath)
	if err != nil {
		return false
	}
	// Cheap substring check; the manifest writes "segments/<name>" entries.
	return strings.Contains(string(data), "/"+segmentName) || strings.Contains(string(data), segmentName+"\n")
}

// EvictUploadedSegments evicts segments from local disk for a DVR.
// Caller passes the candidate segment names. Only Foghorn-authoritative
// sources should produce candidates — chapter reclaim sweep
// (ReclaimDVRSegment control messages) or the disk-pressure fallback
// (RequestEvictableSegments). Each candidate is checked for active
// rolling-manifest membership before deletion; survivors emit a
// DVRSegmentDropped(was_uploaded=true) so Foghorn marks deleted_local.
//
// Returns the number of files actually deleted.
func (dm *DVRManager) EvictUploadedSegments(dvrHash string, candidates []string, reason string) int {
	if len(candidates) == 0 {
		return 0
	}
	job, jobActive := LookupActiveDVR(dvrHash)
	// Resolve the DVR segments directory. While the DVR job is active
	// the canonical path is on job.OutputDir; after StopDVR the job is
	// removed but the segments directory remains on disk until reclaim
	// deletes it. Fall back to scanning storage/dvr/*/<dvr_hash>/
	// segments so post-stop reclaim still works.
	var (
		segmentsDir string
		logger      = dm.logger
	)
	if jobActive {
		segmentsDir = filepath.Join(job.OutputDir, "segments")
		logger = job.Logger
	} else {
		segmentsDir = resolveDVRSegmentsDirByHash(dvrHash)
		if segmentsDir == "" {
			// No active job AND no on-disk match — nothing to evict
			// locally. Still report the eviction so Foghorn can move
			// the ledger to deleted_local and run Phase B (S3 delete).
			for _, name := range candidates {
				if dropErr := SendDVRSegmentDropped(dvrHash, name, reason, "", 0, 0, 0, 0, true); dropErr != nil {
					logger.WithError(dropErr).WithField("segment", name).Debug("Failed to report missing segment as dropped (post-stop, no dir)")
				}
			}
			return 0
		}
	}
	deleted := 0
	idx := localSegmentIndex
	for _, name := range candidates {
		// Rolling-manifest pin is only meaningful while the DVR is
		// recording. After stop the manifest is closed and every
		// segment is eligible.
		if jobActive && SegmentInRollingManifest(job, name) {
			continue
		}
		// Refuse to evict a segment currently under defrost or pinned by an
		// active view (clip harvest, in-flight finalization).
		if idx != nil && !idx.EvictionEligible(dvrHash, name, 0) {
			// Skip — caller will retry after the active view or pin clears.
			continue
		}
		segPath := filepath.Join(segmentsDir, name)
		info, statErr := os.Stat(segPath)
		if statErr != nil {
			// Already gone — still report the eviction so Foghorn's view
			// matches reality.
			if dropErr := SendDVRSegmentDropped(dvrHash, name, reason, segPath, 0, 0, 0, 0, true); dropErr != nil {
				logger.WithError(dropErr).WithField("segment", name).Debug("Failed to report missing segment as dropped")
			}
			if idx != nil {
				idx.Forget(dvrHash, name)
			}
			continue
		}
		if err := os.Remove(segPath); err != nil {
			logger.WithError(err).WithField("segment", name).Warn("Failed to evict DVR segment")
			continue
		}
		if dropErr := SendDVRSegmentDropped(dvrHash, name, reason, segPath, 0, 0, 0, uint64(info.Size()), true); dropErr != nil {
			logger.WithError(dropErr).WithField("segment", name).Debug("Failed to report segment eviction")
		}
		if jobActive {
			job.syncMutex.Lock()
			delete(job.SyncedSegments, name)
			job.syncMutex.Unlock()
		}
		if idx != nil {
			idx.Forget(dvrHash, name)
		}
		deleted++
	}
	return deleted
}

// resolveDVRSegmentsDirByHash scans storage/dvr/*/<dvr_hash>/segments for
// a matching directory. Returns "" when no DVR layout for the hash
// exists on disk. Used by post-stop reclaim where LookupActiveDVR
// misses; mirrors the resolveDVRDir helper used by chapter finalize.
func resolveDVRSegmentsDirByHash(dvrHash string) string {
	dvrRoot := filepath.Join(sidecarcfg.GetStoragePath(), "dvr")
	entries, err := os.ReadDir(dvrRoot)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		candidate := filepath.Join(dvrRoot, e.Name(), dvrHash, "segments")
		if info, statErr := os.Stat(candidate); statErr == nil && info.IsDir() {
			return candidate
		}
	}
	return ""
}

// DropUnsyncedSegment force-evicts a single segment that has NOT been
// uploaded to S3. This is the data-loss path: the segment is reported as
// lost_local; any chapter that overlaps the lost segment will be marked
// failed_source_missing by the finalization queue. Use only when no other
// option remains. Reason should be one of disk_pressure / retention_expired
// / operator_cleanup.
// ErrDropRefusedByLease is returned by DropUnsyncedSegment when the lease
// guard refuses the drop for a non-emergency reason (currently disk_pressure).
// Retention-expired and operator-cleanup callers may force the drop after
// logging escalation; disk-pressure callers must skip and let the lease
// release before retrying.
var ErrDropRefusedByLease = fmt.Errorf("drop refused by active lease")

// DropLeaseChecker, if set, decides whether DropUnsyncedSegment may proceed
// for a given reason. Returning true means "lease held" and the drop will be
// refused for disk_pressure; for retention_expired / operator_cleanup the
// check is informational only (escalated to a warning log). Wired by the
// handlers package at startup so the control package stays free of a leases
// import.
var DropLeaseChecker func(dvrHash, segmentName string) bool

func (dm *DVRManager) DropUnsyncedSegment(dvrHash, segmentName, reason string) error {
	job, ok := LookupActiveDVR(dvrHash)
	if !ok {
		return fmt.Errorf("dvr %s not active", dvrHash)
	}
	if DropLeaseChecker != nil && DropLeaseChecker(dvrHash, segmentName) {
		switch reason {
		case "disk_pressure":
			job.Logger.WithFields(map[string]any{
				"dvr_hash":     dvrHash,
				"segment_name": segmentName,
				"reason":       reason,
			}).Warn("Refusing DropUnsyncedSegment under disk_pressure: lease held")
			return ErrDropRefusedByLease
		default:
			// retention_expired / operator_cleanup: data loss is the intent;
			// log loudly but proceed.
			job.Logger.WithFields(map[string]any{
				"dvr_hash":     dvrHash,
				"segment_name": segmentName,
				"reason":       reason,
			}).Warn("Forcing DropUnsyncedSegment despite active lease (non-disk-pressure caller)")
		}
	}
	segPath := filepath.Join(job.OutputDir, "segments", segmentName)
	var sizeBytes uint64
	if info, err := os.Stat(segPath); err == nil {
		sizeBytes = uint64(info.Size())
	}
	if err := os.Remove(segPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove unsynced segment: %w", err)
	}
	job.syncMutex.Lock()
	delete(job.SyncedSegments, segmentName)
	job.syncMutex.Unlock()
	return SendDVRSegmentDropped(dvrHash, segmentName, reason, segPath, 0, 0, 0, sizeBytes, false)
}

// HandleNewSegment handles a RECORDING_SEGMENT trigger for immediate sync.
// Mist's RecordingSegmentTrigger carries media-time bounds and duration; we
// pass them to Foghorn so the per-segment ledger row records canonical
// timing without re-deriving from filenames or wall-clock.
func (dm *DVRManager) HandleNewSegment(streamName, filePath string, mediaStartMs, mediaEndMs, durationMs int64) {
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
	go dm.syncSpecificSegment(targetJob, filePath, mediaStartMs, mediaEndMs, durationMs)
}

// syncSpecificSegment uploads a recorded TS segment to S3 as a
// recovery-source durability artifact. The per-segment S3 object is NOT
// playback infrastructure: active DVR playback reads the local rolling
// manifest on the recording origin, and other edges DTSC-pull from that
// origin. The S3 object exists so chapter finalization can recover from
// local segment loss (disk corruption, eviction edge case) and so the
// recording survives a recording-node loss until the chapter
// finalization queue produces the canonical .mkv. Once the chapter's
// playback artifact reaches state='frozen', the chapter_reclaim_sweep
// deletes the local TS file and the temporary S3 object.
//
// Records a 'pending' ledger row in Foghorn, uploads the segment to S3
// against the returned presigned URL, then reports the upload to mark
// the row 'uploaded'. Foghorn's ledger is the source of truth for
// eviction decisions; the in-memory SyncedSegments map only tracks
// which uploads this process has already initiated to avoid duplicate
// RecordDVRSegment calls.
func (dm *DVRManager) syncSpecificSegment(job *DVRJob, filePath string, mediaStartMs, mediaEndMs, durationMs int64) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := RecordDVRSegment(
		ctx,
		job.DVRHash, segName, filePath,
		mediaStartMs, mediaEndMs, durationMs,
	)
	if err != nil {
		job.Logger.WithError(err).WithField("segment", segName).Warn("Failed to record DVR segment with Foghorn")
		return
	}
	if !resp.GetAccepted() {
		reason := resp.GetReason()
		// Two rejection categories from Foghorn's RecordDVRSegment:
		//   1. dvr_terminal — artifact is in a terminal state. Hard-stop the
		//      Mist push: every subsequent segment hits the same rejection,
		//      so emit DVRSegmentDropped(was_uploaded=false) for the gap and
		//      then PushStop. This is the only terminal rejection.
		//   2. Everything else (s3_client_unavailable, presign_failed,
		//      insert_failed, dvr_artifact_not_found, missing metadata) —
		//      transient or caller-side. Skip THIS segment and let the
		//      next RECORDING_SEGMENT trigger try again. The reconciliation
		//      backstop (syncNewSegments) will eventually catch up if the
		//      transient condition resolves.
		if reason == "dvr_terminal" {
			job.Logger.WithFields(logging.Fields{
				"segment":  segName,
				"reason":   reason,
				"dvr_hash": job.DVRHash,
			}).Warn("DVR segment rejected as terminal; stopping local push")
			if dropErr := SendDVRSegmentDropped(job.DVRHash, segName, "artifact_terminal", filePath, mediaStartMs, mediaEndMs, durationMs, uint64(info.Size()), false); dropErr != nil {
				job.Logger.WithError(dropErr).WithField("segment", segName).Debug("Failed to report rejected segment as lost_local")
			}
			dm.stopJobAfterTerminalRejection(job)
			return
		}
		job.Logger.WithFields(logging.Fields{
			"segment":  segName,
			"reason":   reason,
			"dvr_hash": job.DVRHash,
		}).Warn("DVR segment record rejected; will retry on next trigger")
		return
	}
	if resp.GetPresignedPutUrl() == "" {
		job.Logger.WithField("segment", segName).Warn("No presigned URL returned for DVR segment")
		return
	}

	if err := dm.uploadSegmentToS3(ctx, filePath, resp.GetPresignedPutUrl()); err != nil {
		job.Logger.WithError(err).WithField("segment", segName).Warn("Failed to upload segment to S3")
		return
	}

	if err := SendMarkDVRSegmentUploaded(job.DVRHash, segName, uint64(info.Size())); err != nil {
		job.Logger.WithError(err).WithField("segment", segName).Warn("Failed to mark DVR segment uploaded with Foghorn")
		// Don't return — local cache below still records success; Foghorn
		// will eventually reconcile via the finalize-time retry path.
	}

	job.syncMutex.Lock()
	job.SyncedSegments[segName] = true
	job.syncMutex.Unlock()

	// Update the per-segment local index. Eviction consults this index
	// to keep segments held by an active view (defrost, clip harvest,
	// in-flight finalization) out of the deletion set.
	if idx := localSegmentIndex; idx != nil {
		idx.MarkUploaded(job.DVRHash, segName, filePath, info.Size())
	}

	job.Logger.WithFields(logging.Fields{
		"segment":  segName,
		"size_kb":  info.Size() / 1024,
		"dvr_hash": job.DVRHash,
		"sequence": resp.GetSequence(),
		"trigger":  "RECORDING_SEGMENT",
	}).Debug("DVR segment synced to S3 via trigger")

	// Source TS segments are pinned to local disk until every overlapping
	// chapter is frozen/reclaimed. Foghorn's chapter reclaim sweep owns
	// deletion via ReclaimDVRSegment; disk-pressure passes (see
	// monitorActiveDVRPressure) ask Foghorn for an authoritative evictable
	// list. Helmsman does NOT routinely evict on its own — that would
	// turn S3 recovery into the normal source for chapter finalization
	// instead of a recovery bridge.
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
	if err := dm.hasSpaceFor(dm.storagePath, 0); err != nil {
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
	job, exists := dm.jobs[dvrHash]
	if !exists {
		dm.mutex.Unlock()
		return fmt.Errorf("DVR recording not found for hash %s", dvrHash)
	}

	// Stop the MistServer push if running
	if job.PushID > 0 {
		if err := dm.mistClient.PushStop(job.PushID); err != nil {
			job.Logger.WithError(err).Warn("Failed to stop MistServer push")
		}
	}

	// Mark the job finalizing. Archive playback is per-chapter VOD artifacts
	// produced by the chapter-finalization pipeline; the rolling Mist playlist
	// stays local-only and is never uploaded to S3.
	job.Status = "finalizing"
	dm.mutex.Unlock()

	// Final sync: flush remaining segments to S3. syncNewSegments uses
	// job.syncMutex internally and is idempotent (SyncedSegments tracks
	// what's already uploaded).
	dm.syncNewSegments(job)

	dm.mutex.Lock()
	dm.sendCompletion(job, "completed", "")
	delete(dm.jobs, dvrHash)
	dm.mutex.Unlock()

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

	// Live DVR window for Mist's targetAge. Foghorn resolves the effective
	// window via pkg/dvrpolicy and stamps it into DVRConfig.dvr_window_seconds.
	windowSeconds := int(job.Config.GetDvrWindowSeconds())
	if windowSeconds <= 0 {
		windowSeconds = 7200 // 2 hours default
	}
	// maxEntries caps manifest playlist size to avoid huge multi-day playlists
	// breaking HLS parsers. Foghorn-resolved value already accounts for tier
	// + cluster ceilings; fall back to ceil(window/segment) if not provided.
	maxEntries := int(job.Config.GetMaxEntries())
	if maxEntries <= 0 {
		maxEntries = (windowSeconds + segmentDuration - 1) / segmentDuration
		if maxEntries < 1 {
			maxEntries = 1
		}
	}

	// Build DVR target path
	// Segments go to {outputDir}/segments/, manifest at {outputDir}/{hash}.m3u8
	// From segments/, ../ goes to outputDir where manifest lives
	// nounlink=1 stops Mist from deleting segment files when pruning the
	// rolling playlist. With ledger + segment-level eviction in place,
	// segment removal is owned by the chapter reclaim sweep — only after the
	// covering chapter is frozen (artifact + .dtsh durable on S3) and any
	// overlapping clip leases have drained — so segments are never lost
	// silently.
	// Explicit audio/video selectors preserve parallel live audio renditions
	// such as AAC and Opus instead of relying on protocol defaults.
	targetURI := fmt.Sprintf("%s/%s/$minute_$segmentCounter.ts#m3u8=../%s.m3u8&audio=all&video=all&subtitle=none&meta=none&split=%d&targetAge=%d&maxEntries=%d&append=1&noendlist=1&nounlink=1",
		job.OutputDir,
		"segments",
		job.DVRHash,
		segmentDuration,
		windowSeconds,
		maxEntries,
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

			if err := dm.hasSpaceFor(dm.storagePath, 0); err != nil {
				// Disk pressure under an active DVR: try to reclaim local
				// space first by evicting uploaded-and-aged segments via
				// Foghorn's authoritative evictable list. Only kill the
				// push if pressure persists after the eviction pass.
				var totalEvicted int
				pressureRelieved := false
				for batch := 0; batch < maxDVREvictionBatches; batch++ {
					evictCtx, evictCancel := context.WithTimeout(context.Background(), 5*time.Second)
					resp, evictErr := RequestEvictableSegments(evictCtx, job.DVRHash, dvrEvictionBatchSize)
					evictCancel()
					if evictErr != nil || resp == nil || len(resp.GetSegmentNames()) == 0 {
						break
					}
					evicted := dm.EvictUploadedSegments(job.DVRHash, resp.GetSegmentNames(), "disk_pressure")
					totalEvicted += evicted
					if evicted == 0 {
						break
					}
					if reEvalErr := dm.hasSpaceFor(dm.storagePath, 0); reEvalErr == nil {
						job.Logger.WithFields(logging.Fields{
							"segments_evicted": totalEvicted,
							"dvr_hash":         job.DVRHash,
						}).Warn("Disk pressure under active DVR relieved by segment eviction")
						pressureRelieved = true
						break
					}
				}
				if pressureRelieved {
					continue
				}
				if totalEvicted > 0 {
					job.Logger.WithFields(logging.Fields{
						"segments_evicted": totalEvicted,
						"dvr_hash":         job.DVRHash,
					}).Warn("Disk pressure under active DVR; evicted uploaded-and-aged segments")
				}
				// Eviction didn't suffice (or there was nothing to evict).
				// Stop cleanly so Foghorn can finalize what was uploaded.
				job.Logger.WithError(err).Error("Stopping DVR recording: disk pressure persists after eviction pass")
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
	switch job.Status {
	case "finalizing", "completed", "completed_partial", "failed":
		return // Don't maintain finalizing/terminal jobs
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

		if existingJob, exists := dm.jobs[job.DVRHash]; exists {
			existingJob.Logger.WithField("retry_count", existingJob.RetryCount).Error("DVR push failed after maximum retries")
			existingJob.Status = "failed"
			dm.sendCompletion(existingJob, "failed", fmt.Sprintf("Push failed after %d retries", existingJob.RetryCount))
			delete(dm.jobs, existingJob.DVRHash)
		}
		return
	}

	// If push disappeared but we have segments, recording might have completed naturally
	if !pushFound && job.SegmentCount > 0 {
		dm.mutex.Lock()
		defer dm.mutex.Unlock()

		if existingJob, exists := dm.jobs[job.DVRHash]; exists {
			existingJob.Logger.Info("DVR recording completed successfully")
			existingJob.Status = "completed"
			dm.sendCompletion(existingJob, "success", "")
			delete(dm.jobs, existingJob.DVRHash)
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
				if stopErr := dm.mistClient.PushStop(push.ID); stopErr != nil {
					job.Logger.WithError(stopErr).WithField("old_push_id", push.ID).Debug("Failed to stop old push")
				} else {
					job.Logger.WithField("old_push_id", push.ID).Debug("Cleaned up old push")
				}
			}
		}
	}

	// Create new push
	if pushErr := dm.mistClient.PushStart(job.StreamName, job.TargetURI); pushErr != nil {
		return 0, fmt.Errorf("failed to start push: %w", pushErr)
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
	delay := min(InitialRetryDelay*time.Duration(1<<uint(retryCount)), MaxRetryDelay)
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

// syncNewSegments is the reconciliation backstop for the rare case Mist's
// RECORDING_SEGMENT trigger was missed (process restart mid-segment, hard
// network blip between Mist and the trigger HTTP endpoint). RECORDING_SEGMENT
// remains the primary writer; this path discovers segments present on disk
// but absent from the in-memory uploaded cache and routes them through the
// same ledger primitives — RecordDVRSegment + MarkDVRSegmentUploaded — so
// they appear in foghorn.dvr_segments and are visible to the chapter
// finalization queue.
//
// Media timing comes from #EXT-X-PROGRAM-DATE-TIME when Mist writes it; once
// anchored, later entries in the same playlist advance by their EXTINF
// duration. Without PDT the rolling playlist has no media-clock anchor for
// segments before its first entry, so reconciliation must not fabricate
// chapter placement.
func (dm *DVRManager) syncNewSegments(job *DVRJob) {
	if !IsConnected() {
		return
	}

	manifestBody, err := os.ReadFile(job.ManifestPath)
	if err != nil {
		// Manifest may not exist yet in the first few seconds of a recording.
		return
	}
	parsed, err := hls.Parse(string(manifestBody))
	if err != nil || parsed == nil || len(parsed.Segments) == 0 {
		return
	}
	segmentsDir := filepath.Join(job.OutputDir, "segments")
	var newCount int
	var skippedNoClock int
	var nextClockMs int64

	for _, seg := range parsed.Segments {
		durationMs := int64(seg.Duration * 1000.0)
		mediaStartMs := seg.ProgramDateTimeMs
		if mediaStartMs <= 0 && nextClockMs > 0 {
			mediaStartMs = nextClockMs
		}
		if mediaStartMs > 0 {
			nextClockMs = mediaStartMs + durationMs
		}

		job.syncMutex.Lock()
		alreadySynced := job.SyncedSegments[seg.Name]
		job.syncMutex.Unlock()
		if alreadySynced {
			continue
		}

		segPath := filepath.Join(segmentsDir, seg.Name)
		info, err := os.Stat(segPath)
		if err != nil {
			// Segment file is gone (evicted) or not yet present. Don't
			// fabricate a ledger row.
			continue
		}
		if mediaStartMs <= 0 {
			skippedNoClock++
			continue
		}
		mediaEndMs := mediaStartMs + durationMs

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		resp, err := RecordDVRSegment(ctx, job.DVRHash, seg.Name, segPath, mediaStartMs, mediaEndMs, durationMs)
		if err != nil || resp == nil {
			cancel()
			if err != nil {
				job.Logger.WithError(err).WithField("segment", seg.Name).Warn("Reconciliation: RecordDVRSegment failed")
			}
			continue
		}
		if !resp.GetAccepted() {
			cancel()
			reason := resp.GetReason()
			if reason == "dvr_terminal" {
				if dropErr := SendDVRSegmentDropped(
					job.DVRHash, seg.Name, "artifact_terminal", segPath,
					mediaStartMs, mediaEndMs, durationMs, uint64(info.Size()), false,
				); dropErr != nil {
					job.Logger.WithError(dropErr).WithField("segment", seg.Name).Debug("Reconciliation: DVRSegmentDropped emit failed")
				}
			} else {
				job.Logger.WithFields(logging.Fields{
					"segment": seg.Name,
					"reason":  reason,
				}).Warn("Reconciliation: RecordDVRSegment rejected; leaving segment retryable")
			}
			continue
		}
		if resp.GetPresignedPutUrl() == "" {
			cancel()
			continue
		}
		if upErr := dm.uploadSegmentToS3(ctx, segPath, resp.GetPresignedPutUrl()); upErr != nil {
			cancel()
			job.Logger.WithError(upErr).WithField("segment", seg.Name).Warn("Reconciliation: upload failed")
			continue
		}
		cancel()
		if markErr := SendMarkDVRSegmentUploaded(job.DVRHash, seg.Name, uint64(info.Size())); markErr != nil {
			job.Logger.WithError(markErr).WithField("segment", seg.Name).Warn("Reconciliation: MarkDVRSegmentUploaded failed")
		}
		job.syncMutex.Lock()
		job.SyncedSegments[seg.Name] = true
		job.syncMutex.Unlock()
		if idx := localSegmentIndex; idx != nil {
			idx.MarkUploaded(job.DVRHash, seg.Name, segPath, info.Size())
		}
		newCount++
	}

	if newCount > 0 {
		job.Logger.WithFields(logging.Fields{
			"reconciled": newCount,
			"dvr_hash":   job.DVRHash,
		}).Info("Reconciled DVR segments missed by RECORDING_SEGMENT trigger")
	}
	if skippedNoClock > 0 {
		job.Logger.WithFields(logging.Fields{
			"skipped":  skippedNoClock,
			"dvr_hash": job.DVRHash,
		}).Warn("Skipped DVR reconciliation segments without program-date-time")
	}
}

// parseManifestSegments extracts segment filenames from an HLS manifest
func (dm *DVRManager) parseManifestSegments(manifestPath string) ([]string, error) {
	file, err := os.Open(manifestPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

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

// UploadSegmentForRetry is the exported entrypoint used by the
// finalize-time retry handler in handlers/storage_manager.go. It is a thin
// wrapper around the internal upload primitive so the handler does not
// need to import unexported package state.
func (dm *DVRManager) UploadSegmentForRetry(ctx context.Context, filePath, presignedURL string) error {
	return dm.uploadSegmentToS3(ctx, filePath, presignedURL)
}

// stopJobAfterTerminalRejection enforces the hard invariant that follows a
// dvr_terminal RecordDVRSegment rejection. The Foghorn-side artifact is no
// longer accepting segments; keeping Mist pushing locally just produces an
// unbounded stream of rejected uploads with no archive trail. Stop the
// push (best-effort; PushStop failures are logged but the job is removed
// regardless) and drop the local DVRJob.
func (dm *DVRManager) stopJobAfterTerminalRejection(job *DVRJob) {
	if job.PushID > 0 && dm.mistClient != nil {
		if err := dm.mistClient.PushStop(job.PushID); err != nil {
			job.Logger.WithError(err).Warn("PushStop after terminal rejection failed; removing job anyway")
		}
	}
	dm.mutex.Lock()
	delete(dm.jobs, job.DVRHash)
	dm.mutex.Unlock()
}

// uploadSegmentToS3 uploads a segment file using a presigned PUT URL via
// the shared HTTP client. Streaming the *os.File body uses constant
// memory regardless of segment size; Content-Length is set explicitly so
// the client never falls back to chunked encoding (some S3 endpoints
// reject chunked PUTs against presigned URLs).
func (dm *DVRManager) uploadSegmentToS3(ctx context.Context, filePath, presignedURL string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open segment file: %w", err)
	}
	defer func() { _ = file.Close() }()

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

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to upload to S3: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("S3 upload failed with status %d", resp.StatusCode)
	}

	return nil
}

// Rolling manifest is local-only. Archive playback uses chapter VOD
// artifacts produced by the chapter finalization job.
