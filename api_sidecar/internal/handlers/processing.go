package handlers

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"frameworks/api_sidecar/internal/admission"
	"frameworks/api_sidecar/internal/dtsh"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ProcessingJobHandler handles VOD processing jobs from Foghorn.
// Activates the processing+{hash} wildcard stream in MistServer.
// STREAM_SOURCE returns a local staged file for clip sources and
// unsafe-wrapper uploads, and the Helmsman read-through relay URL for
// regular safe-wrapper uploads. STREAM_PROCESS provides the MistProc*
// config (VP9/thumbs/audio) from Commodore.
type ProcessingJobHandler struct {
	logger        logging.Logger
	mistServerURL string
	storagePath   string
}

// pendingJobs tracks in-flight processing jobs, signaled by PUSH_END.
var (
	pendingJobs        = map[string]chan ProcessingPushEndEvent{}
	pendingJobIDs      = map[string]string{}
	pendingJobCancels  = map[string]context.CancelFunc{}
	pendingJobReleased = map[string]chan struct{}{}
	pendingJobsMu      sync.Mutex
)

var (
	pendingRecordingEnds   = map[string]chan ProcessingRecordingEndEvent{}
	pendingRecordingEndsMu sync.Mutex
)

// ProcessingPushEndEvent is the subset of Mist's PUSH_END trigger the
// processing pipeline needs before treating a push as terminal.
type ProcessingPushEndEvent struct {
	StreamName     string
	PushID         int64
	TargetBefore   string
	TargetAfter    string
	LogMessages    string
	PushStatus     string
	PushStatusText string
}

type ProcessingRecordingEndEvent struct {
	StreamName      string
	FilePath        string
	OutputProtocol  string
	BytesWritten    int64
	SecondsWriting  int64
	TimeStarted     int64
	TimeEnded       int64
	MediaDurationMs int64
	ExitReason      string
	HumanExitReason string
	Tracks          []processingMetaVideoTrack
	// FullTracks is the unreduced A/V track set from the RECORDING_END (codec/geometry/fps/
	// bitrate/channels/sample_rate). Tracks above is a video-only reduction for completeness
	// checks; FullTracks is what a completed job reports to Foghorn for durable persistence.
	FullTracks      []*ipcpb.StreamTrack
	ProcessingSpeed *ipcpb.ProcessingSpeedStats // Mist feeder speed stats, when reported
}

// processingSpeedSampler derives throughput multipliers from successive
// lastms observations (Δmedia/Δwall) on the progress ticker. Mist's own
// controller stats (RECORDING_END "speed" object) are authoritative when
// present; this sampler is the fallback and cross-check.
type processingSpeedSampler struct {
	prevMediaMs int64
	prevWallMs  int64
	minX, maxX  float64
	sumX        float64
	samples     int
}

func (s *processingSpeedSampler) observe(wallMs, mediaMs int64) {
	if s.prevWallMs > 0 && wallMs > s.prevWallMs && mediaMs > s.prevMediaMs {
		x := float64(mediaMs-s.prevMediaMs) / float64(wallMs-s.prevWallMs)
		if s.samples == 0 || x < s.minX {
			s.minX = x
		}
		if x > s.maxX {
			s.maxX = x
		}
		s.sumX += x
		s.samples++
	}
	s.prevWallMs = wallMs
	s.prevMediaMs = mediaMs
}

// processingSpeedTelemetry merges the job's speed observations into the
// result outputs map (string-valued, rides ProcessingJobResult.outputs to
// Foghorn for lifecycle enrichment) and returns the map plus matching log
// fields. Always assign the returned map: the input may be nil.
func processingSpeedTelemetry(outputs map[string]string, evt *ProcessingRecordingEndEvent, sampler *processingSpeedSampler, pushStartWallMs int64) (map[string]string, logging.Fields) {
	if outputs == nil {
		outputs = map[string]string{}
	}
	wallMs := time.Now().UnixMilli() - pushStartWallMs
	fields := logging.Fields{
		"processing_wall_ms": wallMs,
	}
	outputs["processing_wall_ms"] = strconv.FormatInt(wallMs, 10)
	var speedStats *ipcpb.ProcessingSpeedStats
	if evt != nil {
		fields["media_duration_ms"] = evt.MediaDurationMs
		speedStats = evt.ProcessingSpeed
	}
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }
	if sp := speedStats; sp != nil && sp.GetTicks() > 0 {
		outputs["speed_source"] = "mist"
		outputs["speed_min_x"] = f(sp.GetSpeedMin())
		outputs["speed_avg_x"] = f(sp.GetSpeedAvg())
		outputs["speed_max_x"] = f(sp.GetSpeedMax())
		outputs["speed_ticks"] = strconv.FormatUint(uint64(sp.GetTicks()), 10)
		outputs["hard_slow_ticks"] = strconv.FormatUint(uint64(sp.GetHardSlowTicks()), 10)
		outputs["regular_slow_ticks"] = strconv.FormatUint(uint64(sp.GetRegularSlowTicks()), 10)
		outputs["ramp_ups"] = strconv.FormatUint(uint64(sp.GetRampUps()), 10)
		outputs["lockout_ticks"] = strconv.FormatUint(uint64(sp.GetLockoutTicks()), 10)
		outputs["stale_hold_ticks"] = strconv.FormatUint(uint64(sp.GetStaleHoldTicks()), 10)
		fields["speed_source"] = "mist"
		fields["speed_min_x"] = sp.GetSpeedMin()
		fields["speed_avg_x"] = sp.GetSpeedAvg()
		fields["speed_max_x"] = sp.GetSpeedMax()
		fields["hard_slow_ticks"] = sp.GetHardSlowTicks()
		fields["stale_hold_ticks"] = sp.GetStaleHoldTicks()
		if sp.DrainMs != nil {
			outputs["drain_ms"] = strconv.FormatInt(sp.GetDrainMs(), 10)
			fields["drain_ms"] = sp.GetDrainMs()
		}
	} else if sampler != nil && sampler.samples > 0 {
		avg := sampler.sumX / float64(sampler.samples)
		outputs["speed_source"] = "sampled"
		outputs["speed_min_x"] = f(sampler.minX)
		outputs["speed_avg_x"] = f(avg)
		outputs["speed_max_x"] = f(sampler.maxX)
		fields["speed_source"] = "sampled"
		fields["speed_min_x"] = sampler.minX
		fields["speed_avg_x"] = avg
		fields["speed_max_x"] = sampler.maxX
	}
	return outputs, fields
}

var (
	processingProcessOverrides   = map[string]string{}
	processingOverrideRecords    = map[string]processingOverrideRecord{}
	processingProcessOverridesMu sync.Mutex
	processingOverrideStateDir   string
	processingOverrideLogger     logging.Logger
)

var (
	processingSourceOverrides   = map[string]string{}
	processingSourceOverridesMu sync.Mutex
)

// HasPendingJob returns true if a processing job is currently in-flight for the stream.
func HasPendingJob(streamName string) bool {
	pendingJobsMu.Lock()
	_, ok := pendingJobs[streamName]
	pendingJobsMu.Unlock()
	return ok
}

// claimPendingJob atomically checks and reserves a processing stream. Keeping
// the check and insert under one lock prevents concurrent dispatches from
// booting the same Mist stream and writing the same output.
func claimPendingJob(streamName, jobID string, cancel ...context.CancelFunc) (ch chan ProcessingPushEndEvent, existingJobID string, claimed bool) {
	pendingJobsMu.Lock()
	defer pendingJobsMu.Unlock()
	if _, exists := pendingJobs[streamName]; exists {
		return nil, pendingJobIDs[streamName], false
	}
	ch = make(chan ProcessingPushEndEvent, 1)
	pendingJobs[streamName] = ch
	pendingJobIDs[streamName] = jobID
	pendingJobReleased[streamName] = make(chan struct{})
	if len(cancel) > 0 && cancel[0] != nil {
		pendingJobCancels[streamName] = cancel[0]
	}
	return ch, "", true
}

func releasePendingJob(streamName, jobID string) {
	pendingJobsMu.Lock()
	defer pendingJobsMu.Unlock()
	if pendingJobIDs[streamName] != jobID {
		return
	}
	if released := pendingJobReleased[streamName]; released != nil {
		close(released)
	}
	delete(pendingJobs, streamName)
	delete(pendingJobIDs, streamName)
	delete(pendingJobCancels, streamName)
	delete(pendingJobReleased, streamName)
}

// replacePendingJobChannel swaps only the PUSH_END delivery channel while
// retaining the reservation owner. Processing fallback restarts a push within
// the same job; it must not reopen the stream to a competing dispatch.
func replacePendingJobChannel(streamName, jobID string) (chan ProcessingPushEndEvent, bool) {
	pendingJobsMu.Lock()
	defer pendingJobsMu.Unlock()
	if pendingJobIDs[streamName] != jobID {
		return nil, false
	}
	ch := make(chan ProcessingPushEndEvent, 1)
	pendingJobs[streamName] = ch
	return ch, true
}

func chapterFinalizeAttemptFromJobID(jobID string) (int64, bool) {
	const prefix = "chapter-finalize-v2-"
	if !strings.HasPrefix(jobID, prefix) {
		return 0, false
	}
	rest := strings.TrimPrefix(jobID, prefix)
	separator := strings.IndexByte(rest, '-')
	if separator <= 0 || separator == len(rest)-1 {
		return 0, false
	}
	attempt, err := strconv.ParseInt(rest[:separator], 10, 32)
	return attempt, err == nil && attempt > 0
}

// supersedePendingChapterJob cancels and waits out an older chapter attempt.
// An attempt-less legacy owner is also retired during the rolling job-ID
// transition: Foghorn rejects its attempt-zero result, while waiting for local
// release prevents it from writing the same output as the replacement.
func supersedePendingChapterJob(ctx context.Context, streamName, jobID string) (duplicate bool, err error) {
	newAttempt, newChapter := chapterFinalizeAttemptFromJobID(jobID)
	waitCtx, cancelWait := context.WithTimeout(ctx, time.Minute)
	defer cancelWait()
	for {
		pendingJobsMu.Lock()
		_, exists := pendingJobs[streamName]
		existingID := pendingJobIDs[streamName]
		if !exists {
			pendingJobsMu.Unlock()
			return false, nil
		}
		if existingID == jobID {
			pendingJobsMu.Unlock()
			return true, nil
		}
		// An attempt-less dispatch from an older Foghorn may claim an idle
		// upgraded worker during rollout, but it has no ordering identity and
		// therefore may never displace different work that is already active.
		if !newChapter {
			pendingJobsMu.Unlock()
			return false, fmt.Errorf("chapter job ID %q has no attempt identity and cannot supersede active job %q", jobID, existingID)
		}
		oldAttempt, oldChapter := chapterFinalizeAttemptFromJobID(existingID)
		legacyChapter := strings.HasPrefix(existingID, "chapter-finalize-") && !oldChapter
		cancel := pendingJobCancels[streamName]
		released := pendingJobReleased[streamName]
		pendingJobsMu.Unlock()

		if (!oldChapter && !legacyChapter) || (oldChapter && newAttempt <= oldAttempt) || cancel == nil || released == nil {
			return false, fmt.Errorf("active processing job %q cannot be superseded by %q", existingID, jobID)
		}
		cancel()
		select {
		case <-released:
		case <-waitCtx.Done():
			return false, fmt.Errorf("timed out waiting for superseded chapter job %q to release: %w", existingID, waitCtx.Err())
		}
	}
}

// CountPendingJobs returns the number of processing jobs currently in-flight on
// this node (VOD, clip, and DVR finalize alike, since they all register here).
// Reported to Foghorn as the live processing-slot usage that drives load-aware
// job routing.
func CountPendingJobs() int {
	pendingJobsMu.Lock()
	n := len(pendingJobs)
	pendingJobsMu.Unlock()
	return n
}

// SignalProcessingComplete is called from HandlePushEnd when a processing+ push ends.
func SignalProcessingComplete(streamName string) {
	SignalProcessingPushEnd(ProcessingPushEndEvent{StreamName: streamName, PushStatus: "0"})
}

// SignalProcessingPushEnd is called from HandlePushEnd when a processing+ push
// ends. Mist reports failed mux/sink exits through PUSH_END status; treating
// every PUSH_END as success makes the job fail later as a vague missing output.
func SignalProcessingPushEnd(evt ProcessingPushEndEvent) {
	pendingJobsMu.Lock()
	ch, ok := pendingJobs[evt.StreamName]
	pendingJobsMu.Unlock()
	if ok {
		select {
		case ch <- evt:
		default:
		}
	}
}

func SignalProcessingRecordingEnd(evt ProcessingRecordingEndEvent) {
	pendingRecordingEndsMu.Lock()
	ch, ok := pendingRecordingEnds[evt.StreamName]
	pendingRecordingEndsMu.Unlock()
	if ok {
		select {
		case ch <- evt:
		default:
		}
	}
}

// recordingEndListenerBuffer holds more than one event because RECORDING_END is
// keyed only by stream name: after a Livepeer→local fallback a late event from
// the retired push can land in the freshly-registered channel alongside the
// restarted push's own event. A single slot would let the stale event starve the
// real one (the non-blocking send in SignalProcessingRecordingEnd drops on a full
// channel). One retired push emits one RECORDING_END; the headroom also absorbs
// Mist trigger redelivery. recordingEndPredatesPush discards the stale ones on
// receive.
const recordingEndListenerBuffer = 8

func registerProcessingRecordingEndListener(streamName string) chan ProcessingRecordingEndEvent {
	ch := make(chan ProcessingRecordingEndEvent, recordingEndListenerBuffer)
	pendingRecordingEndsMu.Lock()
	pendingRecordingEnds[streamName] = ch
	pendingRecordingEndsMu.Unlock()
	return ch
}

// recordingEndPredatesPush reports whether a RECORDING_END demonstrably belongs to
// a push that started before the current attempt. After a Livepeer→local fallback
// the retired push has usually run for seconds (rendition shortfall is detected at
// its PUSH_END; stalls hit a minute timeout), so its Mist recording-start is
// clearly older than pushStartedAt and this rejects it. Helmsman is a sidecar on
// the Mist node, so both values use one host clock and the current push's recording
// can only start at or after pushStartedAt, so strict `<` never rejects the live
// event. This is a best-effort discriminator, not a generation identity: both are
// Unix seconds, so a retired push starting in the same second (or reporting
// time_started=0) is not caught here. Correctness does not depend on it: the
// published bytes come from the on-disk file (waitForProcessingOutput), and after a
// fallback the completeness gate validates the produced rendition tracks of the
// finished stream (livepeerRenditionsComplete), not the accepted event's reported
// duration — so a stale event cannot bless a truncated retry.
func recordingEndPredatesPush(timeStarted, pushStartedAt int64) bool {
	return timeStarted > 0 && pushStartedAt > 0 && timeStarted < pushStartedAt
}

func unregisterProcessingRecordingEndListener(streamName string) {
	pendingRecordingEndsMu.Lock()
	delete(pendingRecordingEnds, streamName)
	pendingRecordingEndsMu.Unlock()
}

func processingPushSucceeded(evt ProcessingPushEndEvent) bool {
	status := strings.TrimSpace(evt.PushStatus)
	if status == "" || status == "0" {
		return true
	}
	if strings.HasPrefix(status, "{") {
		// Mist's PUSH_END status field is always a JSON stats object for a
		// completed push; treat any well-formed object as non-failure.
		// Authoritative completion validation lives in RECORDING_END
		// (validateProcessingRecordingEnd: bytes>0, duration>0), not here.
		var parsed map[string]interface{}
		return json.Unmarshal([]byte(status), &parsed) == nil
	}
	return false
}

func validateProcessingRecordingEnd(evt ProcessingRecordingEndEvent, outputPath string) error {
	if strings.TrimSpace(evt.FilePath) != "" && strings.TrimSpace(outputPath) != "" {
		reported := strings.Split(strings.TrimSpace(evt.FilePath), "#")[0]
		if filepath.Clean(reported) != filepath.Clean(outputPath) {
			return fmt.Errorf("recording target mismatch: got %s, want %s", evt.FilePath, outputPath)
		}
	}
	// Mist's machine exit reason is the authority for output success; the byte
	// and duration counts below are only sanity checks. A partially-flushed file
	// can report positive bytes/duration yet still have aborted (WRITE_FAILURE,
	// SEGFAULT, ...), so a non-CLEAN_* reason fails the recording outright.
	if !mist.IsCleanExitReason(evt.ExitReason) {
		reason := strings.TrimSpace(evt.ExitReason)
		if reason == "" {
			reason = "unknown"
		}
		if detail := strings.TrimSpace(evt.HumanExitReason); detail != "" {
			return fmt.Errorf("recording did not finish cleanly: %s (%s)", reason, detail)
		}
		return fmt.Errorf("recording did not finish cleanly: %s", reason)
	}
	if evt.BytesWritten <= 0 {
		return fmt.Errorf("recording wrote no bytes")
	}
	if evt.MediaDurationMs <= 0 {
		return fmt.Errorf("recording wrote no media duration")
	}
	return nil
}

func processingPushFailureMessage(evt ProcessingPushEndEvent) string {
	status := strings.TrimSpace(evt.PushStatus)
	if status == "" {
		status = "unknown"
	}
	msg := fmt.Sprintf("processing push failed: status=%s", status)
	if detail := strings.TrimSpace(evt.PushStatusText); detail != "" {
		msg += " " + detail
	}
	if logs := strings.TrimSpace(evt.LogMessages); logs != "" {
		msg += ": " + logs
	}
	return msg
}

type processingOverrideRecord struct {
	Version       int       `json:"version"`
	StreamName    string    `json:"stream_name"`
	ProcessesJSON string    `json:"processes_json"`
	JobID         string    `json:"job_id,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	ExpiresAt     time.Time `json:"expires_at"`
}

const processingOverrideMaxLifetime = 5 * time.Hour

const processingOverrideReconcileInterval = time.Minute

func configureProcessingOverridePersistence(stateDir string, logger logging.Logger) error {
	stateDir = strings.TrimSpace(stateDir)
	if stateDir == "" {
		return errors.New("processing override persistence requires HELMSMAN_STATE_DIR")
	}
	dir := filepath.Join(stateDir, "processing-overrides")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create processing override directory: %w", err)
	}
	processingProcessOverridesMu.Lock()
	processingOverrideStateDir = dir
	processingOverrideLogger = logger
	processingProcessOverridesMu.Unlock()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), ".processing-override-") {
			if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				logger.WithError(err).WithField("file", entry.Name()).Warn("Could not remove interrupted processing override write")
			}
			continue
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		encoded, readErr := os.ReadFile(path)
		if readErr != nil {
			logger.WithError(readErr).WithField("file", entry.Name()).Error("Removing unreadable durable processing override")
			_ = os.Remove(path)
			continue
		}
		var record processingOverrideRecord
		if unmarshalErr := json.Unmarshal(encoded, &record); unmarshalErr != nil || strings.TrimSpace(record.StreamName) == "" || strings.TrimSpace(record.ProcessesJSON) == "" {
			logger.WithError(unmarshalErr).WithField("file", entry.Name()).Error("Removing invalid durable processing override")
			_ = os.Remove(path)
			continue
		}
		if record.ExpiresAt.IsZero() {
			if info, statErr := entry.Info(); statErr == nil {
				record.ExpiresAt = info.ModTime().Add(processingOverrideMaxLifetime)
			}
		}
		if record.ExpiresAt.IsZero() || (!record.ExpiresAt.After(time.Now()) && strings.TrimSpace(record.JobID) == "") {
			logger.WithFields(logging.Fields{"stream_name": record.StreamName, "job_id": record.JobID}).Warn("Removing expired durable processing override")
			_ = os.Remove(path)
			continue
		}
		processingProcessOverridesMu.Lock()
		processingProcessOverrides[record.StreamName] = record.ProcessesJSON
		processingOverrideRecords[record.StreamName] = record
		processingProcessOverridesMu.Unlock()
	}
	return nil
}

func processingOverridePath(dir, streamName string) string {
	sum := sha256.Sum256([]byte(streamName))
	return filepath.Join(dir, hex.EncodeToString(sum[:])+".json")
}

func setProcessingProcessOverride(streamName, processesJSON, jobID string, expiresAt time.Time) error {
	if streamName == "" || processesJSON == "" {
		return errors.New("processing override requires stream name and processes_json")
	}
	processingProcessOverridesMu.Lock()
	defer processingProcessOverridesMu.Unlock()
	now := time.Now().UTC()
	if !expiresAt.After(now) {
		expiresAt = now.Add(processingOverrideMaxLifetime)
	}
	record := processingOverrideRecord{
		Version: 1, StreamName: streamName, ProcessesJSON: processesJSON,
		JobID: strings.TrimSpace(jobID), CreatedAt: now, ExpiresAt: expiresAt.UTC(),
	}
	dir := processingOverrideStateDir
	if dir != "" {
		encoded, marshalErr := json.Marshal(record)
		if marshalErr != nil {
			return marshalErr
		}
		path := processingOverridePath(dir, streamName)
		tmp, createErr := os.CreateTemp(dir, ".processing-override-*")
		if createErr != nil {
			return createErr
		}
		tmpPath := tmp.Name()
		defer func() { _ = os.Remove(tmpPath) }()
		if chmodErr := tmp.Chmod(0o600); chmodErr != nil {
			_ = tmp.Close()
			return chmodErr
		}
		if _, writeErr := tmp.Write(encoded); writeErr != nil {
			_ = tmp.Close()
			return writeErr
		}
		if syncErr := tmp.Sync(); syncErr != nil {
			_ = tmp.Close()
			return syncErr
		}
		if closeErr := tmp.Close(); closeErr != nil {
			return closeErr
		}
		if renameErr := os.Rename(tmpPath, path); renameErr != nil {
			return renameErr
		}
		dirHandle, openErr := os.Open(dir)
		if openErr != nil {
			return openErr
		}
		if syncErr := dirHandle.Sync(); syncErr != nil {
			_ = dirHandle.Close()
			return syncErr
		}
		if closeErr := dirHandle.Close(); closeErr != nil {
			return closeErr
		}
	}
	processingProcessOverrides[streamName] = processesJSON
	processingOverrideRecords[streamName] = record
	return nil
}

func reconcileExpiredProcessingOverrides(activeStreams map[string]interface{}, now time.Time) {
	processingProcessOverridesMu.Lock()
	stale := make(map[string]processingOverrideRecord)
	for streamName, record := range processingOverrideRecords {
		if record.ExpiresAt.IsZero() || record.ExpiresAt.After(now) {
			continue
		}
		if _, active := activeStreams[streamName]; !active {
			stale[streamName] = record
		}
	}
	processingProcessOverridesMu.Unlock()
	for streamName, expected := range stale {
		if HasPendingJob(streamName) {
			continue
		}
		clearExpiredProcessingProcessOverride(streamName, expected, now)
	}
}

func reconcileProcessingOverridePersistence(mistServerURL string, logger logging.Logger) {
	processingProcessOverridesMu.Lock()
	hasExpired := false
	now := time.Now()
	for _, record := range processingOverrideRecords {
		if !record.ExpiresAt.IsZero() && !record.ExpiresAt.After(now) {
			hasExpired = true
			break
		}
	}
	processingProcessOverridesMu.Unlock()
	if !hasExpired || strings.TrimSpace(mistServerURL) == "" {
		return
	}
	client := mist.NewClient(logger)
	client.BaseURL = mistServerURL
	response, err := client.GetActiveStreams()
	if err != nil {
		logger.WithError(err).Warn("Could not reconcile expired processing policies with Mist; preserving local authority")
		return
	}
	activeStreams, ok := response["active_streams"].(map[string]interface{})
	if !ok {
		logger.Warn("Mist active-stream response was incomplete; preserving expired processing policies")
		return
	}
	reconcileExpiredProcessingOverrides(activeStreams, now)
}

func startProcessingOverridePersistenceReconciler(mistServerURL string, logger logging.Logger) {
	if strings.TrimSpace(mistServerURL) == "" {
		return
	}
	go func() {
		reconcileProcessingOverridePersistence(mistServerURL, logger)
		ticker := time.NewTicker(processingOverrideReconcileInterval)
		defer ticker.Stop()
		for range ticker.C {
			reconcileProcessingOverridePersistence(mistServerURL, logger)
		}
	}()
}

func processingOverrideExpiry(req *ipcpb.ProcessingJobRequest) time.Time {
	if req != nil && req.GetDeadlineUnixMs() > 0 {
		deadline := time.UnixMilli(req.GetDeadlineUnixMs()).UTC()
		if deadline.After(time.Now()) {
			return deadline.Add(5 * time.Minute)
		}
	}
	return time.Now().UTC().Add(processingOverrideMaxLifetime)
}

func clearProcessingProcessOverride(streamName string, ownerJobID ...string) {
	processingProcessOverridesMu.Lock()
	defer processingProcessOverridesMu.Unlock()
	dir := processingOverrideStateDir
	if len(ownerJobID) > 0 && strings.TrimSpace(ownerJobID[0]) != "" {
		owner := strings.TrimSpace(ownerJobID[0])
		if record, ok := processingOverrideRecords[streamName]; ok && strings.TrimSpace(record.JobID) != owner {
			return
		}
		if dir != "" {
			encoded, readErr := os.ReadFile(processingOverridePath(dir, streamName))
			if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
				logProcessingOverrideCleanupError(readErr, streamName, "read durable processing override before owner cleanup")
				return
			}
			if readErr == nil {
				var durable processingOverrideRecord
				if err := json.Unmarshal(encoded, &durable); err != nil {
					logProcessingOverrideCleanupError(err, streamName, "parse durable processing override before owner cleanup")
					return
				}
				if strings.TrimSpace(durable.JobID) != owner {
					return
				}
			}
		}
	}
	removeProcessingProcessOverrideLocked(streamName)
}

func clearExpiredProcessingProcessOverride(streamName string, expected processingOverrideRecord, now time.Time) {
	processingProcessOverridesMu.Lock()
	defer processingProcessOverridesMu.Unlock()
	current, ok := processingOverrideRecords[streamName]
	if !ok || !processingOverrideRecordsEqual(current, expected) ||
		current.ExpiresAt.IsZero() || current.ExpiresAt.After(now) {
		return
	}
	if dir := processingOverrideStateDir; dir != "" {
		encoded, readErr := os.ReadFile(processingOverridePath(dir, streamName))
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			logProcessingOverrideCleanupError(readErr, streamName, "read durable processing override before expiry cleanup")
			return
		}
		if readErr == nil {
			var durable processingOverrideRecord
			if err := json.Unmarshal(encoded, &durable); err != nil {
				logProcessingOverrideCleanupError(err, streamName, "parse durable processing override before expiry cleanup")
				return
			}
			if !processingOverrideRecordsEqual(durable, expected) {
				return
			}
		}
	}
	removeProcessingProcessOverrideLocked(streamName)
}

func processingOverrideRecordsEqual(a, b processingOverrideRecord) bool {
	return a.Version == b.Version && a.StreamName == b.StreamName && a.ProcessesJSON == b.ProcessesJSON &&
		a.JobID == b.JobID && a.CreatedAt.Equal(b.CreatedAt) && a.ExpiresAt.Equal(b.ExpiresAt)
}

func removeProcessingProcessOverrideLocked(streamName string) {
	dir := processingOverrideStateDir
	if dir != "" {
		if err := os.Remove(processingOverridePath(dir, streamName)); err != nil && !errors.Is(err, os.ErrNotExist) {
			logProcessingOverrideCleanupError(err, streamName, "remove durable processing override")
		}
	}
	delete(processingProcessOverrides, streamName)
	delete(processingOverrideRecords, streamName)
	processingSourceOverridesMu.Lock()
	delete(processingSourceOverrides, streamName)
	processingSourceOverridesMu.Unlock()
}

func logProcessingOverrideCleanupError(err error, streamName, operation string) {
	if processingOverrideLogger != nil {
		processingOverrideLogger.WithError(err).WithFields(logging.Fields{
			"stream_name": streamName,
			"operation":   operation,
		}).Warn("Processing override cleanup failed closed")
	}
}

func getProcessingProcessOverride(streamName string) (string, bool) {
	processingProcessOverridesMu.Lock()
	processesJSON, ok := processingProcessOverrides[streamName]
	processingProcessOverridesMu.Unlock()
	return processesJSON, ok
}

func setProcessingSourceOverride(streamName, sourceURL string) {
	if streamName == "" || sourceURL == "" {
		return
	}
	processingSourceOverridesMu.Lock()
	processingSourceOverrides[streamName] = sourceURL
	processingSourceOverridesMu.Unlock()
}

func getProcessingSourceOverride(streamName string) (string, bool) {
	processingSourceOverridesMu.Lock()
	sourceURL, ok := processingSourceOverrides[streamName]
	processingSourceOverridesMu.Unlock()
	return sourceURL, ok
}

func clearProcessingSourceOverride(streamName string) {
	if streamName == "" {
		return
	}
	processingSourceOverridesMu.Lock()
	delete(processingSourceOverrides, streamName)
	processingSourceOverridesMu.Unlock()
}

type processingRuntimeClient interface {
	PushList() ([]mist.PushInfo, error)
	PushKill(pushID int) error
	NukeStream(name string) error
	StopSessions(streamName string) error
	GetActiveStreams() (map[string]interface{}, error)
}

type processingPushLister interface {
	PushList() ([]mist.PushInfo, error)
}

// restartProcessingStreamForLocalFallback tears down the retired generation and
// clears its broken artifact, in order: kill the push, stop sessions, nuke the
// stream, confirm the generation has drained, and only THEN remove the output
// file. The retired push is still the writer until it is stopped and drained;
// drain or remove failures abort the fallback rather than racing a half-torn-down
// generation against the local retry.
func (h *ProcessingJobHandler) restartProcessingStreamForLocalFallback(log *logrus.Entry, mistClient processingRuntimeClient, streamName, outputPath string, pushID int) error {
	targetURI := processingMuxTargetURI(outputPath)
	h.killProcessingPush(log, mistClient, streamName, targetURI, pushID)
	h.stopProcessingSessions(log, mistClient, streamName)
	if nukeErr := mistClient.NukeStream(streamName); nukeErr != nil {
		// Nuke is best-effort: the stream may already be gone. The drain below is
		// the authoritative teardown confirmation.
		log.WithError(nukeErr).Warn("NukeStream during fallback returned an error; relying on drain to confirm teardown")
	}
	h.stopProcessingSessions(log, mistClient, streamName)
	if err := drainProcessingGeneration(log, mistClient, streamName); err != nil {
		return fmt.Errorf("drain retired generation: %w", err)
	}
	if err := os.Remove(outputPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove retired output %s: %w", outputPath, err)
	}
	return nil
}

const (
	processingGenerationDrainTimeout      = 30 * time.Second
	processingGenerationDrainPollInterval = 200 * time.Millisecond
	processingPushIDLookupTimeout         = 2 * time.Second
	processingPushIDLookupPollInterval    = 50 * time.Millisecond
)

type processingActiveStreamsFunc func() (map[string]interface{}, error)

// drainProcessingGeneration blocks until the stream is no longer active, so a
// restarted push cannot race the retired generation. A transient stream-status
// read is retried within the window; failing to confirm teardown by the deadline
// returns an error so the caller aborts rather than restarting over a live
// generation.
func drainProcessingGeneration(log *logrus.Entry, mistClient processingRuntimeClient, streamName string) error {
	return drainProcessingGenerationFromActiveStreams(log, streamName, mistClient.GetActiveStreams, processingGenerationDrainTimeout, processingGenerationDrainPollInterval)
}

func drainProcessingGenerationFromActiveStreams(log *logrus.Entry, streamName string, getActiveStreams processingActiveStreamsFunc, timeout, pollInterval time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := getActiveStreams()
		if err != nil {
			log.WithError(err).Warn("Failed to check processing stream shutdown; retrying")
			time.Sleep(pollInterval)
			continue
		}
		active, ok := resp["active_streams"].(map[string]interface{})
		if !ok {
			return nil
		}
		if _, ok := active[streamName]; !ok {
			return nil
		}
		time.Sleep(pollInterval)
	}
	return fmt.Errorf("processing stream %s still active after drain deadline", streamName)
}

func NewProcessingJobHandler(logger logging.Logger, mistServerURL, storagePath, stateDir string) *ProcessingJobHandler {
	if err := configureProcessingOverridePersistence(stateDir, logger); err != nil {
		logger.WithError(err).Error("Failed to restore durable processing overrides")
	}
	// Restored authority is safe to serve while Mist ownership is checked. Do
	// not make Helmsman startup depend on a Mist API round trip (which has a
	// bounded but comparatively long transport timeout).
	startProcessingOverridePersistenceReconciler(mistServerURL, logger)
	return &ProcessingJobHandler{
		logger:        logger,
		mistServerURL: mistServerURL,
		storagePath:   storagePath,
	}
}

// Handle executes a processing job: activates the processing+ wildcard stream,
// starts a push to local disk as MKV, validates RECORDING_END, reports result.
func (h *ProcessingJobHandler) Handle(req *ipcpb.ProcessingJobRequest, send func(*ipcpb.ControlMessage)) {
	log := h.logger.WithFields(logging.Fields{
		"job_id":        req.GetJobId(),
		"job_type":      req.GetJobType(),
		"artifact_hash": req.GetArtifactHash(),
	})

	if req.GetJobType() == "dvr_chapter_finalize" {
		log.Info("Processing job received (chapter finalize)")
		h.handleChapterFinalize(req, send)
		return
	}

	// Clips are live artifacts: complete renditions already present or source
	// passthrough, never a fresh transcode. Handled separately so the VOD path
	// below stays transcode-only.
	if isClipProcessingSource(req) {
		log.Info("Processing job received (clip)")
		h.handleClip(req, send)
		return
	}

	log.Info("Processing job received")
	streamName := "processing+" + req.GetArtifactHash()

	// Reserve before staging or booting anything. All processing job types use
	// this single atomic boundary, so concurrent deliveries cannot both write
	// the same output.
	doneCh, existingJobID, claimed := claimPendingJob(streamName, req.GetJobId())
	if !claimed {
		log.WithField("active_job_id", existingJobID).Warn("Previous processing attempt still active, ignoring duplicate dispatch")
		return
	}
	reporter := newProcessingReporter(send, req.GetJobId())
	send = reporter.Send
	stopLease := reporter.StartLease(time.Minute)
	defer stopLease()
	defer releasePendingJob(streamName, req.GetJobId())
	defer clearProcessingProcessOverride(streamName, req.GetJobId())
	processesJSON := strings.TrimSpace(req.GetProcessesJson())
	if processesJSON == "" {
		h.sendResult(send, req.GetJobId(), "failed", "processing job is missing processes_json", nil, "", 0)
		return
	}
	if err := setProcessingProcessOverride(streamName, processesJSON, req.GetJobId(), processingOverrideExpiry(req)); err != nil {
		h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("persist processing policy: %v", err), nil, "", 0)
		return
	}

	// Stage unsafe-wrapper sources to local disk before Mist tries to open
	// them. Mist's FLV input is fopen-only and the AV input only auto-matches
	// local paths, so .avi/.flv/.m4v inputs cannot stream via HTTP/relay —
	// they must materialize locally first. Safe wrappers skip this branch:
	// Foghorn's resolveProcessSource returns a Helmsman relay URL and Mist
	// reads via HTTP::URIReader.
	sourceURL := strings.TrimSpace(req.GetSourceUrl())
	if sourceURL == "" {
		sourceURL = h.buildLocalProcessingSourceURL(req)
	}
	var stagedSourcePath string
	defer func() {
		cleanupProcessingStagePath(log, stagedSourcePath)
	}()
	if sourceURL != "" && req.GetSourceUrl() == "" {
		setProcessingSourceOverride(streamName, sourceURL)
		log.WithField("source_url", sourceURL).Info("Registered local processing source override")
	} else if sourceURL == "" {
		log.Warn("Processing job has no source URL or local source parameters")
	}

	if ext := unsafeWrapperExt(req.GetSourceUrl()); ext != "" {
		path, err := h.stageUnsafeWrapper(log, req, ext)
		if err != nil {
			h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("unsafe-wrapper stage failed: %v", err), nil, "", 0)
			return
		}
		stagedSourcePath = path
		log.WithField("staged_path", path).Info("Staged unsafe-wrapper source locally")
	}

	// For segmented (HLS) sources, rewrite manifest with presigned segment URLs
	var hlsManifestPath string
	if isHLSSource(req.GetSourceUrl(), req.GetParams()) {
		var err error
		hlsManifestPath, err = h.rewriteHLSManifest(log, req)
		if err != nil {
			h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("HLS manifest rewrite failed: %v", err), nil, "", 0)
			return
		}
		log.WithField("local_manifest", hlsManifestPath).Info("Rewrote HLS manifest for processing")
		defer func() {
			cleanupProcessingStagePath(log, hlsManifestPath)
		}()
	}

	// Register terminal listeners before activating the stream. The PUSH_END
	// channel was installed atomically with the reservation above.
	recordingEndCh := registerProcessingRecordingEndListener(streamName)

	defer func() {
		unregisterProcessingRecordingEndListener(streamName)
	}()

	// Register PROCESS_EXIT routing before starting the push so an immediate boot
	// failure cannot race past the listener setup.
	processExitCh := RegisterProcessExitListener(streamName)
	defer UnregisterProcessExitListener(streamName)
	processAVCh := RegisterProcessAVSegmentCompleteListener(streamName)
	defer UnregisterProcessAVSegmentCompleteListener(streamName)
	livepeerSegmentCh := RegisterLivepeerSegmentCompleteListener(streamName)
	defer UnregisterLivepeerSegmentCompleteListener(streamName)

	mistClient := mist.NewClient(h.logger)
	if h.mistServerURL != "" {
		mistClient.BaseURL = h.mistServerURL
	}

	// MKV is the processing output container MistServer can push and
	// re-ingest across the codec set we use. VOD and upload processing
	// land in vod/.
	outputDir, outputPath, outputErr := h.processingOutputPath(req, false)
	if outputErr != nil {
		h.sendResult(send, req.GetJobId(), "failed", outputErr.Error(), nil, "", 0)
		return
	}
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		log.WithError(err).Error("Failed to create processing output directory")
		h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("mkdir failed: %v", err), nil, "", 0)
		return
	}

	fallbackAttempted := false
	hasLivepeer := mist.HasLivepeerProcesses(req.GetProcessesJson())
	ignoredProcessExitBootCounts := map[string]int{}
	activePushID := 0
	effectiveProcessesJSON := processesJSON

	outputs, sourceDurationMs, waitErr := h.waitForProcessingStreamReady(context.Background(), log, mistClient, req, streamName, effectiveProcessesJSON, processExitCh, processAVCh, livepeerSegmentCh, ignoredProcessExitBootCounts)
	if waitErr != nil {
		var livepeerBootErr *livepeerReadinessFallbackError
		if errors.As(waitErr, &livepeerBootErr) && !fallbackAttempted {
			log.WithFields(processExitFields(livepeerBootErr.evt)).Warn("Livepeer unrecoverable during readiness, falling back to local MistProcAV")
			ignoreProcessExitThrough(ignoredProcessExitBootCounts, livepeerBootErr.evt.ProcessType, livepeerBootErr.evt.BootCount)
			localConfig := mist.ReplaceLivepeerWithLocal(req.GetProcessesJson())
			if err := setProcessingProcessOverride(streamName, localConfig, req.GetJobId(), processingOverrideExpiry(req)); err != nil {
				h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("persist fallback processing policy: %v", err), nil, "", 0)
				return
			}
			effectiveProcessesJSON = localConfig
			h.updateProcessConfigCache(send, req.GetArtifactHash(), localConfig)
			if teardownErr := h.restartProcessingStreamForLocalFallback(log, mistClient, streamName, outputPath, activePushID); teardownErr != nil {
				h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
				h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("livepeer fallback teardown: %v", teardownErr), nil, "", 0)
				return
			}
			var replaced bool
			doneCh, replaced = replacePendingJobChannel(streamName, req.GetJobId())
			if !replaced {
				h.sendResult(send, req.GetJobId(), "failed", "processing reservation lost during fallback", nil, "", 0)
				return
			}
			recordingEndCh = registerProcessingRecordingEndListener(streamName)
			outputs, sourceDurationMs, waitErr = h.waitForProcessingStreamReady(context.Background(), log, mistClient, req, streamName, effectiveProcessesJSON, processExitCh, processAVCh, livepeerSegmentCh, ignoredProcessExitBootCounts)
			if waitErr != nil {
				h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
				h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("livepeer fallback readiness: %v", waitErr), nil, "", 0)
				return
			}
			fallbackAttempted = true
			hasLivepeer = false
		} else {
			h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
			h.sendResult(send, req.GetJobId(), "failed", waitErr.Error(), nil, "", 0)
			return
		}
	}
	h.sendProgress(send, req.GetJobId(), 0, 0, sourceDurationMs)

	// Unix-seconds start of the current push attempt. RECORDING_END is keyed only
	// by stream name and carries no push/generation id, so after a fallback
	// restart a delayed event from the retired push can land in the new channel;
	// it is rejected by comparing its TimeStarted against this.
	currentPushStartedAt := time.Now().Unix()
	var pushErr error
	activePushID, pushErr = h.startProcessingPush(log, mistClient, req, outputDir, streamName, outputPath)
	if pushErr != nil {
		h.sendResult(send, req.GetJobId(), "failed", pushErr.Error(), nil, "", 0)
		return
	}

	// Single select loop: terminal triggers, process state, progress, and timeouts.
	// 5s cadence feeds the throughput sampler; the lastms read is
	// controller-API-only (no stream wake, no viewer count).
	progressTicker := time.NewTicker(5 * time.Second)
	defer progressTicker.Stop()
	absoluteTimeout := time.After(4 * time.Hour)

	var lastMs int64
	lastAdvance := time.Now()
	var recordingEnd *ProcessingRecordingEndEvent
	const stallTimeout = 3 * time.Minute
	pushStartWallMs := time.Now().UnixMilli()
	speedSampler := &processingSpeedSampler{}

	// restartWithLocalFallback swaps Livepeer for local MistProcAV and restarts
	// the push, returning false only after it has already reported a terminal
	// failure (caller must return). Consolidates the Livepeer→local recovery
	// shared by the unrecoverable-exit, stall, and incomplete-rendition paths.
	// ignoreType/ignoreBoot retire stale PROCESS_EXIT events from the old push.
	restartWithLocalFallback := func(ignoreType string, ignoreBoot int) bool {
		ignoreProcessExitThrough(ignoredProcessExitBootCounts, ignoreType, ignoreBoot)
		localConfig := mist.ReplaceLivepeerWithLocal(req.GetProcessesJson())
		if err := setProcessingProcessOverride(streamName, localConfig, req.GetJobId(), processingOverrideExpiry(req)); err != nil {
			h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("persist fallback processing policy: %v", err), nil, "", 0)
			return false
		}
		effectiveProcessesJSON = localConfig
		h.updateProcessConfigCache(send, req.GetArtifactHash(), localConfig)
		if teardownErr := h.restartProcessingStreamForLocalFallback(log, mistClient, streamName, outputPath, activePushID); teardownErr != nil {
			h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
			h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("livepeer fallback teardown: %v", teardownErr), nil, "", 0)
			return false
		}
		// Fresh doneCh so a PUSH_END from the retired push can't satisfy the
		// restarted push's completion check.
		var replaced bool
		doneCh, replaced = replacePendingJobChannel(streamName, req.GetJobId())
		if !replaced {
			h.sendResult(send, req.GetJobId(), "failed", "processing reservation lost during fallback", nil, "", 0)
			return false
		}
		recordingEndCh = registerProcessingRecordingEndListener(streamName)
		// Discard any RECORDING_END captured from the retired push; the
		// restarted push produces a fresh one. Without this the post-loop
		// validation would run against the old push's bytes/duration/path.
		recordingEnd = nil
		var waitErr error
		outputs, sourceDurationMs, waitErr = h.waitForProcessingStreamReady(context.Background(), log, mistClient, req, streamName, effectiveProcessesJSON, processExitCh, processAVCh, livepeerSegmentCh, ignoredProcessExitBootCounts)
		if waitErr != nil {
			h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
			h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("livepeer fallback readiness: %v", waitErr), nil, "", 0)
			return false
		}
		currentPushStartedAt = time.Now().Unix()
		activePushID, pushErr = h.startProcessingPush(log, mistClient, req, outputDir, streamName, outputPath)
		if pushErr != nil {
			h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
			h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("livepeer fallback restart: %v", pushErr), nil, "", 0)
			return false
		}
		lastMs = 0
		lastAdvance = time.Now()
		fallbackAttempted = true
		hasLivepeer = false
		h.sendProgress(send, req.GetJobId(), 0, 0, sourceDurationMs)
		return true
	}

	recordingEndIsStale := func(evt ProcessingRecordingEndEvent) bool {
		return recordingEndPredatesPush(evt.TimeStarted, currentPushStartedAt)
	}
	terminalSignalsReady := func() (ready bool, terminalFailure bool) {
		if recordingEnd == nil {
			return false, false
		}
		srcInfo, srcSpan := sourceFromReadinessOutputs(outputs)
		if hasLivepeer && !fallbackAttempted && !livepeerRenditionsCompleteFromTracks(log, req.GetProcessesJson(), recordingEnd.Tracks, srcInfo, srcSpan) {
			h.logProcessingTrackDivergence(log, mistClient, streamName, recordingEnd.Tracks)
			log.Warn("Livepeer produced an incomplete rendition set, falling back to local MistProcAV before publish")
			if !restartWithLocalFallback("Livepeer", 0) {
				return false, true
			}
			return false, false
		}
		return true, false
	}

loop:
	for {
		select {
		case pushEnd := <-doneCh:
			if !processingPushSucceeded(pushEnd) {
				log.WithFields(logging.Fields{
					"push_id":       pushEnd.PushID,
					"push_status":   pushEnd.PushStatus,
					"target_before": pushEnd.TargetBefore,
					"target_after":  pushEnd.TargetAfter,
					"push_logs":     pushEnd.LogMessages,
				}).Error("Processing push ended with failure")
				h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
				h.sendResult(send, req.GetJobId(), "failed", processingPushFailureMessage(pushEnd), nil, "", 0)
				return
			}
			log.Info("Processing PUSH_END received")
			if ready, failed := terminalSignalsReady(); failed {
				return
			} else if !ready {
				continue loop
			}
			break loop
		case recEnd := <-recordingEndCh:
			if recordingEndIsStale(recEnd) {
				log.WithFields(logging.Fields{
					"time_started":    recEnd.TimeStarted,
					"push_started_at": currentPushStartedAt,
					"file_path":       recEnd.FilePath,
				}).Warn("Ignoring stale RECORDING_END from a retired push")
				continue loop
			}
			recordingEnd = &recEnd
			log.WithFields(logging.Fields{
				"bytes":             recEnd.BytesWritten,
				"media_duration_ms": recEnd.MediaDurationMs,
				"file_path":         recEnd.FilePath,
				"exit_reason":       recEnd.ExitReason,
			}).Info("Processing RECORDING_END received")
			if ready, failed := terminalSignalsReady(); failed {
				return
			} else if !ready {
				continue loop
			}
			break loop

		case evt := <-processExitCh:
			evtFields := processExitFields(evt)
			if shouldIgnoreProcessExit(evt, ignoredProcessExitBootCounts) {
				log.WithFields(evtFields).Debug("Ignoring stale process exit from retired generation")
				continue
			}

			switch {
			case evt.Status == "unrecoverable" && evt.ProcessType == "Livepeer" && !fallbackAttempted:
				log.WithFields(evtFields).Warn("Livepeer unrecoverable, falling back to local MistProcAV")
				if !restartWithLocalFallback(evt.ProcessType, evt.BootCount) {
					return
				}

			case evt.Status == "unrecoverable" && isCriticalProcess(evt):
				log.WithFields(evtFields).Error("Critical process unrecoverable")
				h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
				h.sendResult(send, req.GetJobId(), "failed",
					fmt.Sprintf("%s process failed: %s", evt.ProcessType, evt.Reason), nil, "", 0)
				return

			case evt.Status == "unrecoverable":
				// Non-critical: Thumbs, audio AV (codec=opus/aac with track_select=video=none)
				log.WithFields(evtFields).Warn("Non-critical process failed, continuing")

			case evt.Status == "retrying":
				log.WithFields(evtFields).Info("Process retrying (MistServer handling restart)")

			case evt.Status == "clean":
				log.WithField("process", evt.ProcessType).Info("Process exited cleanly")

			default:
				// Includes "stopped": the Mist supervisor deliberately retired the
				// process and the event reason names the guard that did it.
				log.WithFields(evtFields).Warn("Process exit event")
			}

		case <-progressTicker.C:
			currentMs := h.getStreamLastMs(mistClient, streamName)
			speedSampler.observe(time.Now().UnixMilli(), currentMs)
			if currentMs > lastMs {
				lastMs = currentMs
				lastAdvance = time.Now()
			}

			var progressPct int32
			if sourceDurationMs > 0 && currentMs > 0 {
				progressPct = int32(currentMs * 100 / sourceDurationMs)
				if progressPct > 100 {
					progressPct = 100
				}
			}

			h.sendProgress(send, req.GetJobId(), progressPct, currentMs, sourceDurationMs)

			if time.Since(lastAdvance) >= stallTimeout {
				if hasLivepeer && !fallbackAttempted {
					log.WithField("progress_pct", progressPct).Warn("Livepeer stalled, falling back to local MistProcAV")
					if !restartWithLocalFallback("Livepeer", 0) {
						return
					}
					continue loop
				}
				log.WithField("progress_pct", progressPct).Warn("Processing stalled")
				h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
				h.sendResult(send, req.GetJobId(), "failed",
					fmt.Sprintf("processing stalled at %d%%", progressPct), nil, "", 0)
				return
			}

		case <-absoluteTimeout:
			log.Warn("Processing absolute timeout exceeded")
			h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
			h.sendResult(send, req.GetJobId(), "failed", "absolute timeout exceeded (4h)", nil, "", 0)
			return
		}
	}

	if recordingEnd != nil {
		if err := validateProcessingRecordingEnd(*recordingEnd, outputPath); err != nil {
			log.WithError(err).WithFields(logging.Fields{
				"bytes":             recordingEnd.BytesWritten,
				"media_duration_ms": recordingEnd.MediaDurationMs,
				"file_path":         recordingEnd.FilePath,
				"exit_reason":       recordingEnd.ExitReason,
				"human_reason":      recordingEnd.HumanExitReason,
			}).Error("Processing recording validation failed")
			h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
			h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("recording validation failed: %v", err), nil, "", 0)
			return
		}
	}

	outputSizeBytes, err := waitForProcessingOutput(outputPath, 5*time.Second)
	if err != nil {
		log.WithError(err).Error("Processed output validation failed")
		h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
		h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("output validation failed: %v", err), nil, "", 0)
		return
	}

	// Final whole-output completeness gate. The source span used here is raised from
	// the readiness snapshot (which can fire before a VOD input runs to EOF, so it
	// can understate the true length) to the source passthrough track span probed at
	// completion, authoritative once the source has been fully read.
	if fallbackAttempted {
		// After a Livepeer→local fallback the in-loop per-rendition check no longer
		// runs (hasLivepeer=false), and a stale RECORDING_END from the retired push
		// could otherwise bless a truncated retry — so validate the produced
		// renditions directly. The local AV output mirrors the original
		// target_profiles 1:1, so livepeerRenditionsComplete (which excludes one
		// source-height track and requires every rendition to cover the source span)
		// validates it; fail closed if incomplete.
		srcInfo, srcSpan := sourceFromReadinessOutputs(outputs)
		if !livepeerRenditionsCompleteFromTracks(log, req.GetProcessesJson(), recordingEnd.Tracks, srcInfo, srcSpan) {
			h.logProcessingTrackDivergence(log, mistClient, streamName, recordingEnd.Tracks)
			log.Error("Post-fallback local output is missing or has truncated renditions; refusing to publish")
			h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
			h.sendResult(send, req.GetJobId(), "failed", "post-fallback output renditions incomplete", nil, "", 0)
			return
		}
	} else if !hasLivepeer {
		// Pure local-AV job (no Livepeer process ever ran, so the in-loop rendition
		// check never validated coverage): this whole-output duration gate is the only
		// completeness proof. Prove the output spans the authoritative completion-time
		// source span and fail closed if that span cannot be determined — trusting the
		// partial readiness snapshot is exactly how a 2s-readiness 2s-output truncation
		// of a longer source would slip through. A Livepeer job that reached here
		// without a fallback already passed the in-loop rendition check, so it needs
		// nothing more.
		srcInfo, _ := sourceFromReadinessOutputs(outputs)
		authoritativeSpan, ok := authoritativeSourceSpanFromTracks(log, recordingEnd.Tracks, sourceDurationMs, srcInfo.Height)
		if !ok {
			log.Error("Could not determine authoritative source span for local-AV output; refusing to publish")
			h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
			h.sendResult(send, req.GetJobId(), "failed", "could not determine source span for completeness check", nil, "", 0)
			return
		}
		if recordingEnd != nil && authoritativeSpan-recordingEnd.MediaDurationMs > maxRenditionSpanShortfallMs {
			log.WithFields(logging.Fields{
				"media_duration_ms":  recordingEnd.MediaDurationMs,
				"source_duration_ms": authoritativeSpan,
			}).Error("Processed output is materially shorter than source; refusing to publish")
			h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
			h.sendResult(send, req.GetJobId(), "failed",
				fmt.Sprintf("output duration %dms short of source %dms", recordingEnd.MediaDurationMs, authoritativeSpan), nil, "", 0)
			return
		}
	}
	h.sendProgress(send, req.GetJobId(), 100, sourceDurationMs, sourceDurationMs)

	vodStreamName := strings.TrimSpace(req.GetOutputRuntimeName())
	if vodStreamName == "" {
		log.Warn("ProcessingJobRequest missing output_runtime_name; skipping DTSH generation (will be generated on first playback)")
	} else {
		setProcessingSourceOverride(vodStreamName, outputPath)
		err := GenerateDTSHForPath(h.mistServerURL, vodStreamName, outputPath+".dtsh", log)
		clearProcessingSourceOverride(vodStreamName)
		if err != nil {
			log.WithError(err).Warn("DTSH generation failed (will be generated on first playback)")
		}
	}

	// Send result with output path so Foghorn can register the artifact
	// in the warm cache immediately. DTSH generation above uses a temporary
	// local source override, so playback cannot win the publish-vs-sidecar
	// race for freshly-created clips.
	outputs, speedFields := processingSpeedTelemetry(outputs, recordingEnd, speedSampler, pushStartWallMs)
	log.WithFields(speedFields).Info("Processing completed")
	var vodTracks []*ipcpb.StreamTrack
	var vodDurationMs int64
	vodTracksPresent := recordingEnd != nil // captured a validated RECORDING_END → tracks authoritative
	if recordingEnd != nil {
		vodTracks = recordingEnd.FullTracks
		vodDurationMs = recordingEnd.MediaDurationMs // real output duration → catalog duration
	}
	h.sendCompletedResult(send, req.GetJobId(), outputs, outputPath, outputSizeBytes, vodDurationMs, vodTracks, vodTracksPresent)
	log.Info("Processing job result sent, artifact registered with Foghorn")

	// Trigger storage check so the .mkv + .dtsh freeze to S3 promptly
	TriggerStorageCheck()
}

// startProcessingPush runs admission for the processing output and starts
// the Mist push. Called for the initial push and for every fallback
// restart (Livepeer→local-MistProcAV swap, stall recovery) so disk
// pressure that developed during the first attempt — DVR recordings
// rolling forward, parallel processing jobs — gets reconciled before
// each restart instead of failing late with ENOSPC.
//
// Returns an error that is safe to surface to the caller's sendResult
// "failed" path; admission rejection and push failure are both fatal
// to the job.
func (h *ProcessingJobHandler) startProcessingPush(log *logrus.Entry, mistClient *mist.Client, req *ipcpb.ProcessingJobRequest, vodDir, streamName, outputPath string) (int, error) {
	if sm := GetStorageManager(); sm != nil {
		var estSize uint64
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if hint, ok := headContentLength(ctx, req.GetSourceUrl()); ok {
			estSize = hint
		}
		cancel()
		decision, decErr := sm.Decide(context.Background(), vodDir, admission.IntentProcessingOutput, estSize)
		if decErr != nil || decision == admission.CacheReject {
			log.WithError(decErr).WithField("est_size", estSize).Error("Processing output admission rejected")
			return 0, fmt.Errorf("admission rejected: %w", decErr)
		}
	}
	targetURI := processingMuxTargetURI(outputPath)
	if err := mistClient.PushStart(streamName, targetURI); err != nil {
		log.WithError(err).Error("Failed to start push")
		return 0, fmt.Errorf("push_start failed: %w", err)
	}
	pushID := findProcessingPushID(log, mistClient, streamName, targetURI)
	log.WithFields(logrus.Fields{
		"output_path": outputPath,
		"target_uri":  targetURI,
		"push_id":     pushID,
	}).Info("Started push for processing stream")
	return pushID, nil
}

func findProcessingPushID(log *logrus.Entry, mistClient processingPushLister, streamName, targetURI string) int {
	deadline := time.Now().Add(processingPushIDLookupTimeout)
	for {
		pushes, err := mistClient.PushList()
		if err != nil {
			log.WithError(err).Warn("Failed to list pushes after push_start")
		} else {
			for _, p := range pushes {
				if p.StreamName == streamName && (p.TargetURI == targetURI || p.ActualURI == targetURI) {
					return p.ID
				}
			}
		}
		if time.Now().After(deadline) {
			log.WithFields(logrus.Fields{
				"stream":     streamName,
				"target_uri": targetURI,
			}).Warn("Could not recover processing push id after push_start; fallback teardown will match by stream")
			return 0
		}
		time.Sleep(processingPushIDLookupPollInterval)
	}
}

type processingTrackRequirements struct {
	requiredAudioCodecs map[string]bool
	requiredVideoCodecs map[string]bool
	expectedAudioCodecs map[string]bool
	expectedVideoCodecs map[string]bool
	expectThumbs        bool
	requireThumbs       bool
}

type processingTrackPresence struct {
	audioCodecs map[string]bool
	videoCodecs map[string]bool
	metaCodecs  map[string]bool
	videoTracks []processingMetaVideoTrack
	outputs     map[string]string
	sourceMedia bool
}

// waitForProcessingStreamReady boots the processing+ wildcard stream and waits
// until Mist has exposed source media. The file-output path owns the
// process-output wait before writing the recording header; holding the push here
// lets short VOD inputs reach EOF and tear down before any file output exists.
func (h *ProcessingJobHandler) waitForProcessingStreamReady(ctx context.Context, log *logrus.Entry, mistClient *mist.Client, req *ipcpb.ProcessingJobRequest, streamName string, processesJSON string, processExitCh <-chan ProcessExitEvent, processAVCh <-chan ProcessAVSegmentCompleteEvent, livepeerSegmentCh <-chan LivepeerSegmentCompleteEvent, ignoredProcessExitBootCounts map[string]int) (map[string]string, int64, error) {
	requirements := expectedProcessingTracks(processesJSON)
	deadline := time.Now().Add(45 * time.Second)
	var lastPresence processingTrackPresence
	var lastErr error
	bootTicker := time.NewTicker(500 * time.Millisecond)
	defer bootTicker.Stop()
	// Tracks whether the last active_streams check saw the stream. The boot
	// request (a /json_ fetch) counts as a Mist viewer and wakes idle
	// streams, so it must only fire while the stream is actually down;
	// readiness polling itself is controller-API-only.
	streamActive := false

	for {
		if evt, ok := nextProcessExitEvent(processExitCh, ignoredProcessExitBootCounts); ok {
			evtFields := processExitFields(evt)
			switch {
			case evt.Status == "unrecoverable" && evt.ProcessType == "Livepeer":
				log.WithFields(evtFields).Warn("Livepeer unrecoverable while waiting for processing readiness")
				return nil, 0, &livepeerReadinessFallbackError{evt: evt}
			case evt.Status == "unrecoverable" && isCriticalProcess(evt):
				log.WithFields(evtFields).Error("Critical process unrecoverable while waiting for processing readiness")
				return nil, 0, fmt.Errorf("%s process failed during readiness: %s", evt.ProcessType, evt.Reason)
			case evt.Status == "unrecoverable":
				log.WithFields(evtFields).Warn("Non-critical process failed while waiting for processing readiness")
			case evt.Status == "retrying":
				log.WithFields(evtFields).Info("Process retrying while waiting for processing readiness")
			case evt.Status == "clean":
				log.WithFields(evtFields).Info("Process exited cleanly while waiting for processing readiness")
			default:
				// Includes "stopped": deliberate Mist supervisor stop; the reason
				// names the guard that retired the process.
				log.WithFields(evtFields).Warn("Process exit event while waiting for processing readiness")
			}
		}
		for {
			evt, ok := nextProcessAVSegmentCompleteEvent(processAVCh)
			if !ok {
				break
			}
			if processAVVideoProgress(evt) {
				deadline = time.Now().Add(45 * time.Second)
			}
			if processAVFinalVideoReady(evt) {
				log.WithFields(logrus.Fields{
					"output_codec":  evt.OutputCodec,
					"output_width":  evt.OutputWidth,
					"output_height": evt.OutputHeight,
				}).Info("Local MistProcAV video output finalized during readiness")
			}
		}
		for {
			evt, ok := nextLivepeerSegmentCompleteEvent(livepeerSegmentCh)
			if !ok {
				break
			}
			if evt.RenditionCount > 0 {
				deadline = time.Now().Add(45 * time.Second)
				log.WithFields(logrus.Fields{
					"livepeer_session_id": evt.LivepeerSessionID,
					"segment_num":         evt.SegmentNumber,
					"rendition_count":     evt.RenditionCount,
					"turnaround_ms":       evt.TurnaroundMs,
					"speed_factor":        evt.SpeedFactor,
				}).Info("Livepeer segment output observed during readiness")
			}
		}

		if !streamActive {
			if err := h.bootMistStream(streamName); err != nil {
				lastErr = err
			}
		}

		streamData, err := h.getActiveProcessingStreamData(mistClient, streamName)
		if err != nil {
			lastErr = err
			streamActive = false
		} else {
			streamActive = true
			lastPresence = inspectProcessingActiveStream(streamData)
			if lastPresence.sourceMedia {
				log.WithFields(logrus.Fields{
					"audio_codecs": mapKeys(lastPresence.audioCodecs),
					"video_codecs": mapKeys(lastPresence.videoCodecs),
					"meta_codecs":  mapKeys(lastPresence.metaCodecs),
				}).Info("Processing source stream ready")
				return lastPresence.outputs, sourceDurationFromOutputs(lastPresence.outputs), nil
			}
		}

		if time.Now().After(deadline) {
			if lastErr != nil && len(lastPresence.outputs) == 0 {
				return nil, 0, fmt.Errorf("processing stream did not boot: %w", lastErr)
			}
			if !processingRequiredTracksReady(lastPresence, requirements) {
				return nil, 0, fmt.Errorf("processing stream missing required tracks: have audio=%v video=%v meta=%v want audio=%v video=%v thumbs=%t",
					mapKeys(lastPresence.audioCodecs), mapKeys(lastPresence.videoCodecs), mapKeys(lastPresence.metaCodecs),
					mapKeys(requirements.requiredAudioCodecs), mapKeys(requirements.requiredVideoCodecs), requirements.requireThumbs)
			}
			missing := missingProcessingTracks(lastPresence, requirements)
			outputs := cloneStringMap(lastPresence.outputs)
			if len(missing) > 0 {
				outputs["processing_degraded"] = "true"
				outputs["processing_missing_tracks"] = strings.Join(missing, ",")
			}
			log.WithFields(logrus.Fields{
				"audio_codecs":    mapKeys(lastPresence.audioCodecs),
				"video_codecs":    mapKeys(lastPresence.videoCodecs),
				"meta_codecs":     mapKeys(lastPresence.metaCodecs),
				"missing_tracks":  missing,
				"required_tracks": requiredTrackSummary(requirements),
			}).Warn("Processing stream proceeding with degraded enrichment")
			return outputs, sourceDurationFromOutputs(outputs), nil
		}

		select {
		case <-bootTicker.C:
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		}
	}
}

func sourceDurationFromOutputs(outputs map[string]string) int64 {
	if durStr, ok := outputs["duration_ms"]; ok {
		if dur, err := strconv.ParseInt(durStr, 10, 64); err == nil && dur > 0 {
			return dur
		}
	}
	return 0
}

func (h *ProcessingJobHandler) bootMistStream(streamName string) error {
	if h.mistServerURL == "" {
		return fmt.Errorf("MISTSERVER_URL not configured")
	}
	url := mistJSONURL(h.mistServerURL, streamName, "metaeverywhere=1&inclzero=1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	return nil
}

func mistJSONURL(mistServerURL, streamName, rawQuery string) string {
	url := strings.TrimRight(deriveProcessingMistHTTPBase(mistServerURL), "/") + "/json_" + streamName + ".js"
	if rawQuery != "" {
		url += "?" + rawQuery
	}
	return url
}

func processingMuxTargetURI(outputPath string) string {
	return processingMuxTargetURIWithVideo(outputPath, "all")
}

func processingMuxTargetURIWithVideo(outputPath, videoSelector string) string {
	return processingMuxTargetURIWithSelectors(outputPath, videoSelector, "all")
}

func processingMuxTargetURIWithSelectors(outputPath, videoSelector, metaSelector string) string {
	videoSelector = strings.TrimSpace(videoSelector)
	if videoSelector == "" {
		videoSelector = "all"
	}
	metaSelector = strings.TrimSpace(metaSelector)
	if metaSelector == "" {
		metaSelector = "all"
	}
	return outputPath + "#audio=all&video=" + videoSelector + "&meta=" + metaSelector + "&subtitle=all"
}

func expectedProcessingTracks(processesJSON string) processingTrackRequirements {
	req := processingTrackRequirements{
		requiredAudioCodecs: map[string]bool{},
		requiredVideoCodecs: map[string]bool{},
		expectedAudioCodecs: map[string]bool{},
		expectedVideoCodecs: map[string]bool{},
	}
	var processes []map[string]interface{}
	if err := json.Unmarshal([]byte(processesJSON), &processes); err != nil {
		return req
	}
	for _, proc := range processes {
		processName, processOK := proc["process"].(string)
		if !processOK {
			continue
		}
		switch processName {
		case "Thumbs":
			req.expectThumbs = true
			req.requireThumbs = processRequired(proc)
		case "AV":
			codec, codecOK := proc["codec"].(string)
			if !codecOK {
				continue
			}
			codec = normalizeTrackCodec(codec)
			if codec == "" {
				continue
			}
			if isAudioCodec(codec) {
				req.expectedAudioCodecs[codec] = true
				if processRequired(proc) {
					req.requiredAudioCodecs[codec] = true
				}
			} else {
				req.expectedVideoCodecs[codec] = true
				if processRequired(proc) {
					req.requiredVideoCodecs[codec] = true
				}
			}
		}
	}
	return req
}

func expectedLocalAVVideoProcesses(processesJSON string) int {
	var processes []map[string]interface{}
	if err := json.Unmarshal([]byte(processesJSON), &processes); err != nil {
		return 0
	}
	count := 0
	for _, proc := range processes {
		processName, processOK := proc["process"].(string)
		if !processOK || processName != "AV" {
			continue
		}
		codec, codecOK := proc["codec"].(string)
		if !codecOK || isAudioCodec(normalizeTrackCodec(codec)) {
			continue
		}
		if trackSelect, ok := proc["track_select"].(string); ok && strings.Contains(strings.ToLower(trackSelect), "video=none") {
			continue
		}
		count++
	}
	return count
}

func processAVFinalVideoReady(evt ProcessAVSegmentCompleteEvent) bool {
	if !evt.IsFinal || strings.ToLower(evt.TrackType) != "video" {
		return false
	}
	return evt.OutputFrames > 0 && evt.OutputWidth > 0 && evt.OutputHeight > 0
}

func processAVVideoProgress(evt ProcessAVSegmentCompleteEvent) bool {
	return strings.ToLower(evt.TrackType) == "video" && evt.OutputFrames > 0
}

func visibleProcessingVideoTracksReady(p processingTrackPresence, want int) bool {
	if want <= 0 {
		return true
	}
	return visibleProcessingVideoTrackCount(p) >= want
}

func visibleProcessingVideoTrackCount(p processingTrackPresence) int {
	if len(p.videoTracks) > 0 {
		return len(p.videoTracks)
	}
	count := 0
	for codec := range p.videoCodecs {
		if codec != "JPEG" {
			count++
		}
	}
	return count
}

func processRequired(proc map[string]interface{}) bool {
	if required, ok := proc["required"].(bool); ok {
		return required
	}
	if consequential, ok := proc["consequential"].(bool); ok {
		return consequential
	}
	if inconsequential, ok := proc["inconsequential"].(bool); ok && inconsequential {
		return false
	}
	return false
}

func inspectProcessingActiveStream(streamData map[string]interface{}) processingTrackPresence {
	p := processingTrackPresence{
		audioCodecs: map[string]bool{},
		videoCodecs: map[string]bool{},
		metaCodecs:  map[string]bool{},
		outputs:     extractActiveStreamMetadata(streamData),
	}

	health, ok := streamData["health"].(map[string]interface{})
	if !ok {
		return p
	}
	for name, trackRaw := range health {
		track, ok := trackRaw.(map[string]interface{})
		if !ok {
			continue
		}
		codec := ""
		if v, ok := track["codec"].(string); ok {
			codec = normalizeTrackCodec(v)
		}
		switch {
		case isAudioCodec(codec) || strings.HasPrefix(name, "audio_"):
			if codec != "" {
				p.audioCodecs[codec] = true
				p.sourceMedia = true
			}
		case codec == "JPEG":
			p.videoCodecs[codec] = true
		case strings.HasPrefix(name, "meta_") || codec == "thumbvtt" || codec == "JSON":
			if codec != "" {
				p.metaCodecs[codec] = true
			}
		case strings.HasPrefix(name, "video_") || codec != "":
			if codec != "" {
				p.videoCodecs[codec] = true
				p.sourceMedia = true
				t := processingMetaVideoTrack{codec: codec, name: name}
				if v, ok := track["width"].(float64); ok {
					t.width = int(v)
				}
				if v, ok := track["height"].(float64); ok {
					t.height = int(v)
				}
				if v, ok := track["source"].(string); ok {
					t.source = v
				}
				if id, ok := mapInt64(track, "id", "track_id"); ok {
					t.trackID = id
					t.hasTrackID = true
				}
				if idx, ok := mapInt64(track, "idx", "track_index"); ok {
					t.trackIndex = int(idx)
					t.hasTrackIndex = true
				}
				p.videoTracks = append(p.videoTracks, t)
			}
		}
	}
	if metaTracks := parseProcessingMetaVideoTracks(streamData); len(metaTracks) > 0 {
		p.videoTracks = metaTracks
		for _, t := range metaTracks {
			if t.codec != "" {
				p.videoCodecs[t.codec] = true
				p.sourceMedia = true
			}
		}
	}
	return p
}

// maxRenditionSpanShortfallMs is the absolute amount (ms) a rendition track may
// fall short of the source span before it counts as truncated — sized to a
// segment/GOP boundary, not a fraction of duration (a ratio would let a long
// VOD lose many seconds).
const maxRenditionSpanShortfallMs = 2000

// renditionResolutionToleranceMin absorbs codec/profile rounding around requested
// rendition dimensions while still keeping distinct ladder rungs separate.
const renditionResolutionToleranceMin = 32

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func renditionHeightsClose(actual, expected int) bool {
	if actual <= 0 || expected <= 0 {
		return false
	}
	tolerance := renditionResolutionToleranceMin
	if pct := expected / 20; pct > tolerance {
		tolerance = pct
	}
	return absInt(actual-expected) <= tolerance
}

func videoTrackHeights(tracks []processingMetaVideoTrack) []int {
	heights := make([]int, 0, len(tracks))
	for _, t := range tracks {
		heights = append(heights, t.height)
	}
	return heights
}

// processingMetaVideoTrack is one video track from a Mist stream's JSON
// metadata, carrying the per-track span (firstms/lastms) MistServer emits in
// DTSC::Meta::toJSON.
type processingMetaVideoTrack struct {
	codec         string
	name          string
	source        string
	width         int
	height        int
	firstms       float64
	lastms        float64
	trackID       int64
	hasTrackID    bool
	trackIndex    int
	hasTrackIndex bool
}

func (t processingMetaVideoTrack) spanMs() float64 { return t.lastms - t.firstms }

func (t processingMetaVideoTrack) selector() string {
	if t.hasTrackID {
		return "i" + strconv.FormatInt(t.trackID, 10)
	}
	if t.hasTrackIndex {
		return strconv.Itoa(t.trackIndex)
	}
	return ""
}

func mapInt64(values map[string]interface{}, keys ...string) (int64, bool) {
	for _, key := range keys {
		raw, ok := values[key]
		if !ok {
			continue
		}
		switch v := raw.(type) {
		case float64:
			return int64(v), true
		case int:
			return int64(v), true
		case int64:
			return v, true
		case json.Number:
			if i, err := v.Int64(); err == nil {
				return i, true
			}
		case string:
			if i, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
				return i, true
			}
		}
	}
	return 0, false
}

// livepeerRenditionsComplete fetches the finished processing stream's JSON
// metadata and reports whether the requested renditions are present enough to
// publish. The caller supplies source dims + a readiness span lower bound when
// available; outputs that intentionally hide the source track still validate by
// the requested rendition heights.
//
// Two failure modes are caught:
//   - missing/wrong renditions: a requested target-profile resolution has no
//     distinct matching output track (a known source track is excluded first,
//     and each track satisfies at most one profile);
//   - truncated renditions: a matched track whose span falls more than
//     maxRenditionSpanShortfallMs below a known source span.
//
// Missing/wrong tracks return false so the caller can run the one
// local-MistProcAV fallback rather than publishing an incomplete Livepeer output.
// "No renditions requested" returns true because there is nothing to prove.
func livepeerRenditionsCompleteFromTracks(log *logrus.Entry, processesJSON string, tracks []processingMetaVideoTrack, source mist.SourceMediaInfo, sourceSpanMs float64) bool {
	expectedHeights, err := mist.RequestedRenditionHeights(processesJSON, source)
	if err != nil {
		log.WithError(err).Warn("Malformed Livepeer process config; treating renditions as incomplete")
		return false
	}
	if len(expectedHeights) == 0 {
		return true
	}
	return renditionsCompleteFromTracks(log, expectedHeights, tracks, source, sourceSpanMs)
}

// renditionsCompleteFromTracks is the pure rendition-completeness decision,
// split out so it can be unit-tested without a MistServer. It takes the requested
// ladder intent as raw heights (the dimension a Livepeer profile actually
// specifies; width follows the source aspect and is Mist's authority), so the
// check never depends on Mist's normalized pixel math. Each requested height must
// map to a distinct output track. When a source passthrough track can be
// identified, it is excluded from the candidate pool and its span tightens the
// truncation check.
//
// It fails closed on missing/invalid heights and missing tracks. Span validation
// is applied only when a source-span baseline is known; a normalized output that
// omits the source track should not be rejected solely because there is no
// separate source track in the final artifact.
func renditionsCompleteFromTracks(log *logrus.Entry, expectedHeights []int, videoTracks []processingMetaVideoTrack, source mist.SourceMediaInfo, sourceSpanMs float64) bool {
	if len(videoTracks) == 0 {
		log.Warn("Finished processing stream exposes no video tracks; renditions incomplete")
		return false
	}

	srcIdx := -1
	if source.Height > 0 {
		srcIdx = sourceVideoTrackIndex(videoTracks, source)
	}
	var sourceTrackSpanMs float64
	if srcIdx >= 0 {
		sourceTrackSpanMs = videoTracks[srcIdx].spanMs()
	}
	pool := make([]processingMetaVideoTrack, 0, len(videoTracks))
	for i, t := range videoTracks {
		if i == srcIdx {
			continue
		}
		pool = append(pool, t)
	}

	baseline := sourceSpanMs
	if sourceTrackSpanMs > baseline {
		baseline = sourceTrackSpanMs
	}
	spanBaselineKnown := baseline > 0

	consumed := make([]bool, len(pool))
	for _, eh := range expectedHeights {
		if eh <= 0 {
			// A requested rendition whose height cannot be determined cannot be
			// proven present — fail closed rather than skipping it.
			log.Warn("Cannot determine a requested Livepeer rendition height; treating renditions as incomplete")
			return false
		}
		idx := -1
		for i, t := range pool {
			if !consumed[i] && renditionHeightsClose(t.height, eh) {
				idx = i
				break
			}
		}
		if idx < 0 {
			log.WithFields(logrus.Fields{
				"available_video_heights":  videoTrackHeights(pool),
				"missing_rendition_height": eh,
			}).Warn("Finished processing stream is missing a requested Livepeer rendition")
			return false
		}
		consumed[idx] = true
		if spanBaselineKnown && baseline-pool[idx].spanMs() > maxRenditionSpanShortfallMs {
			log.WithFields(logrus.Fields{
				"rendition_height": eh,
				"track_span_ms":    int64(pool[idx].spanMs()),
				"source_span_ms":   int64(baseline),
			}).Warn("Finished processing stream has a truncated Livepeer rendition")
			return false
		}
	}
	return true
}

// logProcessingTrackDivergence logs the recorded (RECORDING_END) video track
// heights next to the live stream-metadata heights at the moment a
// completeness check fails. Renditions present in the stream but absent from
// the recording point at a recording-side loss (push raced the transcode);
// renditions absent from both mean they were never produced.
func (h *ProcessingJobHandler) logProcessingTrackDivergence(log *logrus.Entry, mistClient *mist.Client, streamName string, recorded []processingMetaVideoTrack) {
	fields := logrus.Fields{"recorded_video_heights": videoTrackHeights(recorded)}
	if streamData, err := h.getActiveProcessingStreamData(mistClient, streamName); err != nil {
		fields["stream_meta_error"] = err.Error()
	} else {
		fields["stream_video_heights"] = videoTrackHeights(inspectProcessingActiveStream(streamData).videoTracks)
	}
	log.WithFields(fields).Warn("Rendition completeness failed: recorded vs live stream video tracks")
}

// sourceFromReadinessOutputs builds the pre-transcode source baseline (dims +
// span) from the readiness metadata map. The span is a LOWER BOUND, not
// authoritative: readiness can fire before a VOD input runs to EOF, so callers
// raise it to the completion-time source passthrough track span.
func sourceFromReadinessOutputs(outputs map[string]string) (mist.SourceMediaInfo, float64) {
	var src mist.SourceMediaInfo
	if w, err := strconv.Atoi(outputs["width"]); err == nil {
		src.Width = w
	}
	if ht, err := strconv.Atoi(outputs["height"]); err == nil {
		src.Height = ht
	}
	if f, err := strconv.ParseFloat(outputs["fps"], 64); err == nil {
		src.FPS = f
	}
	var span float64
	if d, err := strconv.Atoi(outputs["duration_ms"]); err == nil {
		span = float64(d)
	}
	return src, span
}

func authoritativeSourceSpanFromTracks(log *logrus.Entry, tracks []processingMetaVideoTrack, readinessSpanMs int64, sourceHeight int) (int64, bool) {
	srcHeight := sourceHeight
	if srcHeight <= 0 {
		for _, t := range tracks {
			if t.height > srcHeight {
				srcHeight = t.height
			}
		}
	}
	sourceTrackSpanMs := int64(-1)
	for _, t := range tracks {
		if srcHeight > 0 && renditionHeightsClose(t.height, srcHeight) {
			if span := int64(t.spanMs()); span > sourceTrackSpanMs {
				sourceTrackSpanMs = span
			}
		}
	}
	if sourceTrackSpanMs < 0 {
		log.Warn("RECORDING_END did not include a source-height video track; refusing to prove output completeness")
		return 0, false
	}
	if readinessSpanMs > sourceTrackSpanMs {
		return readinessSpanMs, true
	}
	return sourceTrackSpanMs, true
}

func sourceVideoTrackIndex(videoTracks []processingMetaVideoTrack, source mist.SourceMediaInfo) int {
	srcHeight := source.Height
	if srcHeight <= 0 {
		for _, t := range videoTracks {
			if t.height > srcHeight {
				srcHeight = t.height
			}
		}
	}
	srcIdx := -1
	for i, t := range videoTracks {
		if srcHeight > 0 && renditionHeightsClose(t.height, srcHeight) {
			if srcIdx < 0 ||
				(t.source == "" && videoTracks[srcIdx].source != "") ||
				(t.source == videoTracks[srcIdx].source && t.spanMs() > videoTracks[srcIdx].spanMs()) {
				srcIdx = i
			}
		}
	}
	return srcIdx
}

// parseProcessingMetaVideoTracks extracts renderable video tracks (excluding
// thumbnail JPEG tracks) from Mist stream JSON metadata.
func parseProcessingMetaVideoTracks(meta map[string]interface{}) []processingMetaVideoTrack {
	tracksRaw := meta["tracks"]
	if inner, ok := meta["meta"].(map[string]interface{}); ok {
		if t, ok := inner["tracks"]; ok {
			tracksRaw = t
		}
	}
	tracks, ok := tracksRaw.(map[string]interface{})
	if !ok {
		return nil
	}
	var out []processingMetaVideoTrack
	for name, raw := range tracks {
		track, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		codec := ""
		if v, ok := track["codec"].(string); ok {
			codec = normalizeTrackCodec(v)
		}
		if codec == "" || codec == "JPEG" {
			continue
		}
		typ := ""
		if v, ok := track["type"].(string); ok {
			typ = v
		}
		if typ != "video" && !strings.HasPrefix(name, "video") {
			continue
		}
		t := processingMetaVideoTrack{codec: codec, name: name}
		if v, ok := track["width"].(float64); ok {
			t.width = int(v)
		}
		if v, ok := track["height"].(float64); ok {
			t.height = int(v)
		}
		if v, ok := track["firstms"].(float64); ok {
			t.firstms = v
		}
		if v, ok := track["lastms"].(float64); ok {
			t.lastms = v
		}
		if v, ok := track["source"].(string); ok {
			t.source = v
		}
		if id, ok := mapInt64(track, "id", "track_id"); ok {
			t.trackID = id
			t.hasTrackID = true
		}
		if idx, ok := mapInt64(track, "idx", "track_index"); ok {
			t.trackIndex = int(idx)
			t.hasTrackIndex = true
		}
		out = append(out, t)
	}
	return out
}

func processingTracksFromProto(tracks []*ipcpb.StreamTrack) []processingMetaVideoTrack {
	out := make([]processingMetaVideoTrack, 0, len(tracks))
	for _, track := range tracks {
		if track == nil {
			continue
		}
		codec := normalizeTrackCodec(track.GetCodec())
		if codec == "" || codec == "JPEG" {
			continue
		}
		if track.GetTrackType() != "video" && track.GetWidth() <= 0 && track.GetHeight() <= 0 {
			continue
		}
		out = append(out, processingMetaVideoTrack{
			codec:         codec,
			name:          track.GetTrackName(),
			width:         int(track.GetWidth()),
			height:        int(track.GetHeight()),
			firstms:       float64(track.GetFirstMs()),
			lastms:        float64(track.GetLastMs()),
			trackID:       track.GetTrackId(),
			hasTrackID:    track.TrackId != nil,
			trackIndex:    int(track.GetTrackIndex()),
			hasTrackIndex: track.TrackIndex != nil,
		})
	}
	return out
}

func processingRequiredTracksReady(p processingTrackPresence, req processingTrackRequirements) bool {
	if !p.sourceMedia {
		return false
	}
	for codec := range req.requiredAudioCodecs {
		if !p.audioCodecs[codec] {
			return false
		}
	}
	for codec := range req.requiredVideoCodecs {
		if !p.videoCodecs[codec] {
			return false
		}
	}
	if req.requireThumbs && (!p.videoCodecs["JPEG"] || !p.metaCodecs["thumbvtt"]) {
		return false
	}
	return true
}

func processingTracksComplete(p processingTrackPresence, req processingTrackRequirements) bool {
	return processingRequiredTracksReady(p, req) && len(missingProcessingTracks(p, req)) == 0
}

func processingLivepeerRenditionsReady(p processingTrackPresence, processesJSON string) (bool, []int, error) {
	source, _ := sourceFromReadinessOutputs(p.outputs)
	expectedHeights, err := mist.RequestedRenditionHeights(processesJSON, source)
	if err != nil {
		return false, nil, err
	}
	if len(expectedHeights) == 0 {
		return true, nil, nil
	}
	missing := missingRenditionHeightsForPush(expectedHeights, p.videoTracks, source)
	return len(missing) == 0, missing, nil
}

func missingRenditionHeightsForPush(expectedHeights []int, videoTracks []processingMetaVideoTrack, source mist.SourceMediaInfo) []int {
	srcHeight := source.Height
	if srcHeight <= 0 {
		for _, t := range videoTracks {
			if t.height > srcHeight {
				srcHeight = t.height
			}
		}
	}

	srcIdx := sourceVideoTrackIndex(videoTracks, mist.SourceMediaInfo{Height: srcHeight})

	pool := make([]processingMetaVideoTrack, 0, len(videoTracks))
	for i, t := range videoTracks {
		if i == srcIdx {
			continue
		}
		pool = append(pool, t)
	}

	consumed := make([]bool, len(pool))
	var missing []int
	for _, expected := range expectedHeights {
		found := false
		for i, track := range pool {
			if !consumed[i] && renditionHeightsClose(track.height, expected) {
				consumed[i] = true
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, expected)
		}
	}
	return missing
}

func missingProcessingTracks(p processingTrackPresence, req processingTrackRequirements) []string {
	var missing []string
	for codec := range req.expectedAudioCodecs {
		if !p.audioCodecs[codec] {
			missing = append(missing, "audio:"+codec)
		}
	}
	for codec := range req.expectedVideoCodecs {
		if !p.videoCodecs[codec] {
			missing = append(missing, "video:"+codec)
		}
	}
	if req.expectThumbs {
		if !p.videoCodecs["JPEG"] {
			missing = append(missing, "video:JPEG")
		}
		if !p.metaCodecs["thumbvtt"] {
			missing = append(missing, "meta:thumbvtt")
		}
	}
	sort.Strings(missing)
	return missing
}

func requiredTrackSummary(req processingTrackRequirements) map[string][]string {
	return map[string][]string{
		"audio":  mapKeys(req.requiredAudioCodecs),
		"video":  mapKeys(req.requiredVideoCodecs),
		"thumbs": boolKeys(req.requireThumbs),
	}
}

func boolKeys(enabled bool) []string {
	if !enabled {
		return nil
	}
	return []string{"required"}
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func normalizeTrackCodec(codec string) string {
	switch strings.ToLower(strings.TrimSpace(codec)) {
	case "aac":
		return "AAC"
	case "h264":
		return "H264"
	case "h265", "hevc":
		return "HEVC"
	case "opus":
		return "opus"
	case "jpeg", "mjpeg":
		return "JPEG"
	case "thumbvtt":
		return "thumbvtt"
	default:
		return strings.TrimSpace(codec)
	}
}

func isAudioCodec(codec string) bool {
	switch normalizeTrackCodec(codec) {
	case "AAC", "opus", "MP3", "MP2", "AC3", "FLAC", "PCM":
		return true
	default:
		return false
	}
}

func mapKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func waitForProcessingOutput(outputPath string, timeout time.Duration) (int64, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		fi, err := os.Stat(outputPath)
		switch {
		case err == nil && fi.Size() > 0:
			return fi.Size(), nil
		case err == nil:
			lastErr = fmt.Errorf("output file is empty: %s", outputPath)
		case os.IsNotExist(err):
			lastErr = fmt.Errorf("output file missing: %s", outputPath)
		default:
			lastErr = err
		}

		if time.Now().After(deadline) {
			if lastErr == nil {
				lastErr = fmt.Errorf("output validation timed out: %s", outputPath)
			}
			return 0, lastErr
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// extractTrackMetadata extracts video/audio metadata from MistServer's stream info.
func extractTrackMetadata(meta map[string]interface{}) map[string]string {
	outputs := map[string]string{}

	metaRaw, ok := meta["meta"]
	if !ok {
		return outputs
	}
	metaMap, ok := metaRaw.(map[string]interface{})
	if !ok {
		return outputs
	}
	tracksRaw, ok := metaMap["tracks"]
	if !ok {
		return outputs
	}
	tracks, ok := tracksRaw.(map[string]interface{})
	if !ok {
		return outputs
	}

	for name, trackRaw := range tracks {
		track, ok := trackRaw.(map[string]interface{})
		if !ok {
			continue
		}

		if strings.HasPrefix(name, "video") {
			if v, ok := track["codec"].(string); ok && normalizeTrackCodec(v) == "JPEG" {
				continue
			}
			if _, exists := outputs["video_codec"]; exists {
				continue
			}
			if v, ok := track["codec"].(string); ok {
				outputs["video_codec"] = normalizeTrackCodec(v)
			}
			if v, ok := track["width"].(float64); ok {
				outputs["width"] = strconv.Itoa(int(v))
			}
			if v, ok := track["height"].(float64); ok {
				outputs["height"] = strconv.Itoa(int(v))
			}
			if w, ok := outputs["width"]; ok {
				if ht, ok := outputs["height"]; ok {
					outputs["resolution"] = w + "x" + ht
				}
			}
			if v, ok := track["fpks"].(float64); ok && v > 0 {
				outputs["fps"] = fmt.Sprintf("%.2f", v/1000.0)
			}
			if v, ok := track["bps"].(float64); ok && v > 0 {
				outputs["bitrate_kbps"] = strconv.Itoa(int(v / 1000))
			}
		}

		if strings.HasPrefix(name, "audio") {
			if v, ok := track["codec"].(string); ok {
				outputs["audio_codec"] = v
			}
			if v, ok := track["channels"].(float64); ok {
				outputs["audio_channels"] = strconv.Itoa(int(v))
			}
			if v, ok := track["rate"].(float64); ok {
				outputs["audio_sample_rate"] = strconv.Itoa(int(v))
			}
		}
	}

	if v, ok := metaMap["lastms"].(float64); ok && v > 0 {
		outputs["duration_ms"] = strconv.Itoa(int(v))
	}

	return outputs
}

func extractActiveStreamMetadata(streamData map[string]interface{}) map[string]string {
	outputs := map[string]string{}
	health, ok := streamData["health"].(map[string]interface{})
	if !ok {
		return outputs
	}
	bestVideo := map[string]string{}
	bestVideoHeight := -1
	for name, trackRaw := range health {
		track, ok := trackRaw.(map[string]interface{})
		if !ok {
			continue
		}
		codec := ""
		if v, ok := track["codec"].(string); ok {
			codec = normalizeTrackCodec(v)
		}
		if strings.HasPrefix(name, "video_") && codec != "JPEG" {
			candidate := map[string]string{}
			if codec != "" {
				candidate["video_codec"] = codec
			}
			height := 0
			if v, ok := track["width"].(float64); ok {
				candidate["width"] = strconv.Itoa(int(v))
			}
			if v, ok := track["height"].(float64); ok {
				height = int(v)
				candidate["height"] = strconv.Itoa(height)
			}
			if w, ok := candidate["width"]; ok {
				if ht, ok := candidate["height"]; ok {
					candidate["resolution"] = w + "x" + ht
				}
			}
			if v, ok := track["fpks"].(float64); ok && v > 0 {
				candidate["fps"] = fmt.Sprintf("%.2f", v/1000.0)
			}
			if v, ok := track["kbits"].(float64); ok && v > 0 {
				candidate["bitrate_kbps"] = strconv.Itoa(int(v))
			}
			if height > bestVideoHeight {
				bestVideoHeight = height
				bestVideo = candidate
			}
		}
		if strings.HasPrefix(name, "audio_") {
			if codec != "" {
				outputs["audio_codec"] = codec
			}
			if v, ok := track["channels"].(float64); ok {
				outputs["audio_channels"] = strconv.Itoa(int(v))
			}
			if v, ok := track["rate"].(float64); ok {
				outputs["audio_sample_rate"] = strconv.Itoa(int(v))
			}
		}
	}
	if v, ok := streamData["lastms"].(float64); ok && v > 0 {
		outputs["duration_ms"] = strconv.Itoa(int(v))
	}
	for k, v := range bestVideo {
		outputs[k] = v
	}
	return outputs
}

// ProcessExitEvent represents a PROCESS_EXIT trigger from MistServer.
type ProcessExitEvent struct {
	StreamName  string
	ProcessType string // "AV", "Livepeer", "Thumbs"
	Config      string // process config JSON
	PID         int
	ExitCode    int
	BootCount   int
	Status      string // "clean", "retrying", "unrecoverable"
	ShortReason string // ER_* constant
	Reason      string // human-readable
}

type ProcessAVSegmentCompleteEvent struct {
	StreamName   string
	TrackType    string
	OutputCodec  string
	OutputWidth  int
	OutputHeight int
	OutputFrames int64
	IsFinal      bool
}

type LivepeerSegmentCompleteEvent struct {
	StreamName        string
	LivepeerSessionID string
	SegmentNumber     int
	RenditionCount    int
	TurnaroundMs      int64
	SpeedFactor       float64
}

const ignoreAllProcessExitBootCounts = -1

type livepeerReadinessFallbackError struct {
	evt ProcessExitEvent
}

func (e *livepeerReadinessFallbackError) Error() string {
	if e.evt.Reason != "" {
		return fmt.Sprintf("livepeer process failed during readiness: %s", e.evt.Reason)
	}
	return "livepeer process failed during readiness"
}

// processExitListeners routes PROCESS_EXIT triggers to processing handlers.
var (
	processExitListeners   = map[string]chan ProcessExitEvent{}
	processExitListenersMu sync.Mutex
)

var (
	processAVSegmentCompleteListeners   = map[string]chan ProcessAVSegmentCompleteEvent{}
	processAVSegmentCompleteListenersMu sync.Mutex
)

var (
	livepeerSegmentCompleteListeners   = map[string]chan LivepeerSegmentCompleteEvent{}
	livepeerSegmentCompleteListenersMu sync.Mutex
)

func ignoreProcessExitThrough(ignored map[string]int, processType string, bootCount int) {
	if processType == "" {
		return
	}
	if bootCount <= 0 {
		ignored[processType] = ignoreAllProcessExitBootCounts
		return
	}
	if current, ok := ignored[processType]; ok {
		if current == ignoreAllProcessExitBootCounts || current >= bootCount {
			return
		}
	}
	ignored[processType] = bootCount
}

func shouldIgnoreProcessExit(evt ProcessExitEvent, ignored map[string]int) bool {
	cutoff, ok := ignored[evt.ProcessType]
	if !ok {
		return false
	}
	if cutoff == ignoreAllProcessExitBootCounts {
		return true
	}
	if evt.BootCount <= 0 {
		return true
	}
	return evt.BootCount <= cutoff
}

func processExitFields(evt ProcessExitEvent) logging.Fields {
	return logging.Fields{
		"process":    evt.ProcessType,
		"exit_code":  evt.ExitCode,
		"boot_count": evt.BootCount,
		"status":     evt.Status,
		"reason":     evt.Reason,
	}
}

func nextProcessExitEvent(processExitCh <-chan ProcessExitEvent, ignored map[string]int) (ProcessExitEvent, bool) {
	if processExitCh == nil {
		return ProcessExitEvent{}, false
	}
	for {
		select {
		case evt := <-processExitCh:
			if shouldIgnoreProcessExit(evt, ignored) {
				continue
			}
			return evt, true
		default:
			return ProcessExitEvent{}, false
		}
	}
}

func nextProcessAVSegmentCompleteEvent(processAVCh <-chan ProcessAVSegmentCompleteEvent) (ProcessAVSegmentCompleteEvent, bool) {
	if processAVCh == nil {
		return ProcessAVSegmentCompleteEvent{}, false
	}
	select {
	case evt := <-processAVCh:
		return evt, true
	default:
		return ProcessAVSegmentCompleteEvent{}, false
	}
}

func nextLivepeerSegmentCompleteEvent(livepeerCh <-chan LivepeerSegmentCompleteEvent) (LivepeerSegmentCompleteEvent, bool) {
	if livepeerCh == nil {
		return LivepeerSegmentCompleteEvent{}, false
	}
	select {
	case evt := <-livepeerCh:
		return evt, true
	default:
		return LivepeerSegmentCompleteEvent{}, false
	}
}

func RegisterProcessExitListener(streamName string) chan ProcessExitEvent {
	processExitListenersMu.Lock()
	defer processExitListenersMu.Unlock()
	ch := make(chan ProcessExitEvent, 4)
	processExitListeners[streamName] = ch
	return ch
}

func UnregisterProcessExitListener(streamName string) {
	processExitListenersMu.Lock()
	defer processExitListenersMu.Unlock()
	delete(processExitListeners, streamName)
}

// RouteProcessExit delivers a PROCESS_EXIT event to the processing handler listening on the stream.
func RouteProcessExit(evt ProcessExitEvent) {
	processExitListenersMu.Lock()
	ch, ok := processExitListeners[evt.StreamName]
	processExitListenersMu.Unlock()
	if !ok {
		incMistWebhook("PROCESS_EXIT", "listener_missing")
		logger.WithField("stream_name", evt.StreamName).Warn("PROCESS_EXIT has no processing listener")
		return
	}
	select {
	case ch <- evt:
	default:
		incMistWebhook("PROCESS_EXIT", "listener_full")
		logger.WithField("stream_name", evt.StreamName).Error("PROCESS_EXIT listener queue full; event dropped")
	}
}

func RegisterProcessAVSegmentCompleteListener(streamName string) chan ProcessAVSegmentCompleteEvent {
	processAVSegmentCompleteListenersMu.Lock()
	defer processAVSegmentCompleteListenersMu.Unlock()
	ch := make(chan ProcessAVSegmentCompleteEvent, 16)
	processAVSegmentCompleteListeners[streamName] = ch
	return ch
}

func UnregisterProcessAVSegmentCompleteListener(streamName string) {
	processAVSegmentCompleteListenersMu.Lock()
	defer processAVSegmentCompleteListenersMu.Unlock()
	delete(processAVSegmentCompleteListeners, streamName)
}

func RouteProcessAVSegmentComplete(evt ProcessAVSegmentCompleteEvent) {
	processAVSegmentCompleteListenersMu.Lock()
	ch, ok := processAVSegmentCompleteListeners[evt.StreamName]
	processAVSegmentCompleteListenersMu.Unlock()
	if ok {
		select {
		case ch <- evt:
		default:
		}
	}
}

func RegisterLivepeerSegmentCompleteListener(streamName string) chan LivepeerSegmentCompleteEvent {
	livepeerSegmentCompleteListenersMu.Lock()
	defer livepeerSegmentCompleteListenersMu.Unlock()
	ch := make(chan LivepeerSegmentCompleteEvent, 16)
	livepeerSegmentCompleteListeners[streamName] = ch
	return ch
}

func UnregisterLivepeerSegmentCompleteListener(streamName string) {
	livepeerSegmentCompleteListenersMu.Lock()
	defer livepeerSegmentCompleteListenersMu.Unlock()
	delete(livepeerSegmentCompleteListeners, streamName)
}

func RouteLivepeerSegmentComplete(evt LivepeerSegmentCompleteEvent) {
	livepeerSegmentCompleteListenersMu.Lock()
	ch, ok := livepeerSegmentCompleteListeners[evt.StreamName]
	livepeerSegmentCompleteListenersMu.Unlock()
	if ok {
		select {
		case ch <- evt:
		default:
		}
	}
}

// ParseProcessExitTrigger parses the newline-separated PROCESS_EXIT trigger payload.
// Format: stream_name\nprocess_type\nconfig_json\npid\nexit_code\nboot_count\nstatus\nshort_reason\nlong_reason
func ParseProcessExitTrigger(body []byte) (ProcessExitEvent, error) {
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) < 7 {
		return ProcessExitEvent{}, fmt.Errorf("PROCESS_EXIT payload too short: %d lines", len(lines))
	}
	evt := ProcessExitEvent{
		StreamName:  lines[0],
		ProcessType: lines[1],
		Config:      lines[2],
	}
	if v, err := strconv.Atoi(lines[3]); err == nil {
		evt.PID = v
	}
	if v, err := strconv.Atoi(lines[4]); err == nil {
		evt.ExitCode = v
	}
	if v, err := strconv.Atoi(lines[5]); err == nil {
		evt.BootCount = v
	}
	evt.Status = lines[6]
	if len(lines) > 7 {
		evt.ShortReason = lines[7]
	}
	if len(lines) > 8 {
		evt.Reason = lines[8]
	}
	return evt, nil
}

type processingReporter struct {
	mu               sync.Mutex
	send             func(*ipcpb.ControlMessage)
	jobID            string
	terminal         bool
	progressPct      int32
	lastMs           int64
	sourceDurationMs int64
	lastProgressAt   time.Time
}

func newProcessingReporter(send func(*ipcpb.ControlMessage), jobID string) *processingReporter {
	return &processingReporter{send: send, jobID: jobID}
}

// Send serializes job reports, remembers the newest progress, and closes the
// lease when a terminal result is emitted. The background lease sender and the
// normal processing loop therefore cannot reorder a heartbeat after a result.
func (r *processingReporter) Send(msg *ipcpb.ControlMessage) {
	if msg == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if progress := msg.GetProcessingJobProgress(); progress != nil {
		if r.terminal {
			return
		}
		if progress.GetProgressPct() < r.progressPct {
			progress.ProgressPct = r.progressPct
		}
		if progress.GetLastMs() < r.lastMs {
			progress.LastMs = r.lastMs
		}
		if progress.GetSourceDurationMs() == 0 && r.sourceDurationMs > 0 {
			progress.SourceDurationMs = r.sourceDurationMs
		}
		r.progressPct = progress.GetProgressPct()
		r.lastMs = progress.GetLastMs()
		r.sourceDurationMs = progress.GetSourceDurationMs()
		r.lastProgressAt = time.Now()
	}
	if result := msg.GetProcessingJobResult(); result != nil && result.GetStatus() != "cache_update" {
		r.terminal = true
	}
	if r.send != nil {
		r.send(msg)
	}
}

func (r *processingReporter) renewLease(interval time.Duration, force bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.terminal || r.send == nil {
		return
	}
	now := time.Now()
	if !force && !r.lastProgressAt.IsZero() && now.Sub(r.lastProgressAt) < interval {
		return
	}
	r.send(processingProgressMessage(r.jobID, r.progressPct, r.lastMs, r.sourceDurationMs))
	r.lastProgressAt = now
}

// StartLease keeps Foghorn's durable ownership clock current during blocking
// phases such as source staging and readiness. It resends the latest observed
// progress only when the normal loop has been quiet for a full interval.
func (r *processingReporter) StartLease(interval time.Duration) func() {
	if interval <= 0 {
		interval = time.Minute
	}
	r.renewLease(interval, true)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				r.renewLease(interval, false)
			case <-stop:
				return
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(stop)
			<-done
		})
	}
}

func processingProgressMessage(jobID string, progressPct int32, lastMs, sourceDurationMs int64) *ipcpb.ControlMessage {
	return &ipcpb.ControlMessage{
		Payload: &ipcpb.ControlMessage_ProcessingJobProgress{
			ProcessingJobProgress: &ipcpb.ProcessingJobProgress{
				JobId:            jobID,
				ProgressPct:      progressPct,
				LastMs:           lastMs,
				SourceDurationMs: sourceDurationMs,
			},
		},
		SentAt: timestamppb.Now(),
	}
}

func (h *ProcessingJobHandler) sendProgress(send func(*ipcpb.ControlMessage), jobID string, progressPct int32, lastMs, sourceDurationMs int64) {
	if send != nil {
		send(processingProgressMessage(jobID, progressPct, lastMs, sourceDurationMs))
	}
}

// updateProcessConfigCache tells Foghorn to update the STREAM_PROCESS cache
// for this artifact with the given processes_json (used for Livepeer fallback).
func (h *ProcessingJobHandler) updateProcessConfigCache(send func(*ipcpb.ControlMessage), artifactHash, processesJSON string) {
	if send == nil {
		return
	}
	send(&ipcpb.ControlMessage{
		Payload: &ipcpb.ControlMessage_ProcessingJobResult{
			ProcessingJobResult: &ipcpb.ProcessingJobResult{
				JobId:  "cache_update:" + artifactHash,
				Status: "cache_update",
				Outputs: map[string]string{
					"artifact_hash":  artifactHash,
					"processes_json": processesJSON,
				},
			},
		},
		SentAt: timestamppb.Now(),
	})
}

// getStreamLastMs queries MistServer's active_streams for the current lastms
// of the given stream. Returns 0 if the stream is not found or query fails.
func (h *ProcessingJobHandler) getStreamLastMs(mistClient *mist.Client, streamName string) int64 {
	streamData, err := h.getActiveProcessingStreamData(mistClient, streamName)
	if err != nil {
		return 0
	}
	if lastms, ok := streamData["lastms"].(float64); ok {
		return int64(lastms)
	}
	return 0
}

func (h *ProcessingJobHandler) getActiveProcessingStreamData(mistClient *mist.Client, streamName string) (map[string]interface{}, error) {
	resp, err := mistClient.GetActiveStreams()
	if err != nil {
		return nil, err
	}
	activeStreams, ok := resp["active_streams"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("active_streams missing from Mist response")
	}
	streamData, ok := activeStreams[streamName].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("stream %s not active", streamName)
	}
	return streamData, nil
}

// isCriticalProcess returns true if the dying process is essential for output.
// Video transcode AV processes are critical. Thumbs and audio AV are not.
// Distinguishes by codec in the process config: video codecs (H264, VP9, AV1,
// H265, VP8) vs audio codecs (opus, AAC).
func isCriticalProcess(evt ProcessExitEvent) bool {
	if evt.ProcessType == "Thumbs" {
		return false
	}
	if evt.ProcessType == "AV" {
		cfg := strings.ToLower(evt.Config)
		for _, vc := range []string{`"h264"`, `"vp9"`, `"av1"`, `"h265"`, `"hevc"`, `"vp8"`} {
			if strings.Contains(cfg, vc) {
				return true
			}
		}
		return false
	}
	return true
}

// cleanupFailedProcessing nukes the stream (kills input buffer + all processes)
// and removes the partial output file. Used on terminal failure with no fallback.
func (h *ProcessingJobHandler) cleanupFailedProcessing(log *logrus.Entry, mistClient *mist.Client, streamName, outputPath string) {
	if err := mistClient.NukeStream(streamName); err != nil {
		log.WithError(err).Warn("Failed to nuke stream during cleanup")
	}
	if outputPath != "" {
		if err := os.Remove(outputPath); err != nil && !os.IsNotExist(err) {
			log.WithError(err).Warn("Failed to remove partial output file")
		}
	}
}

func (h *ProcessingJobHandler) killProcessingPush(log *logrus.Entry, mistClient processingRuntimeClient, streamName, targetURI string, pushID int) {
	if pushID > 0 {
		if killErr := mistClient.PushKill(pushID); killErr == nil {
			return
		} else {
			log.WithError(killErr).WithField("push_id", pushID).Warn("Failed to kill tracked processing push; falling back to stream lookup")
		}
	}
	pushes, err := mistClient.PushList()
	if err != nil {
		log.WithError(err).Warn("Failed to list pushes for cleanup")
		return
	}
	for _, p := range pushes {
		if p.StreamName == streamName && (p.TargetURI == targetURI || p.ActualURI == targetURI) {
			if killErr := mistClient.PushKill(p.ID); killErr != nil {
				log.WithError(killErr).WithField("push_id", p.ID).Warn("Failed to kill processing push")
			}
			return
		}
	}
	for _, p := range pushes {
		if p.StreamName == streamName {
			if killErr := mistClient.PushKill(p.ID); killErr != nil {
				log.WithError(killErr).WithField("push_id", p.ID).Warn("Failed to kill processing push by stream fallback")
			} else {
				log.WithField("push_id", p.ID).Warn("Killed processing push by stream fallback after target URI lookup missed")
			}
			return
		}
	}
}

func (h *ProcessingJobHandler) stopProcessingSessions(log *logrus.Entry, mistClient processingRuntimeClient, streamName string) {
	if err := mistClient.StopSessions(streamName); err != nil {
		log.WithError(err).Warn("Failed to stop processing stream sessions during fallback")
	}
}

// GenerateDTSH boots a stream via the /json_{streamName}.js endpoint to trigger
// DTSH generation. MistServer's input module reads headers and writes the .dtsh
// file as a side effect. Works for any stream type (vod+, processing+, etc.)
// because our fork boots offline streams on HTTP GET.
func GenerateDTSH(mistServerURL, streamName string, log *logrus.Entry) error {
	if mistServerURL == "" {
		return fmt.Errorf("MISTSERVER_URL not configured")
	}
	url := mistJSONURL(mistServerURL, streamName, "")

	for i := 0; i < 15; i++ {
		if i > 0 {
			time.Sleep(2 * time.Second)
		}
		httpReq, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
		resp, err := http.DefaultClient.Do(httpReq)
		if err != nil {
			log.WithError(err).Debug("DTSH generation: json endpoint not ready")
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			log.WithField("status", resp.StatusCode).Debug("DTSH generation: json endpoint returned error")
			continue
		}

		var data map[string]interface{}
		if err := json.Unmarshal(body, &data); err != nil {
			continue
		}
		if _, hasError := data["error"]; hasError {
			log.WithField("error", data["error"]).Debug("DTSH generation: stream not ready")
			continue
		}
		log.Info("DTSH generation completed via json endpoint")
		return nil
	}
	return fmt.Errorf("timed out waiting for DTSH generation")
}

func GenerateDTSHForPath(mistServerURL, streamName, dtshPath string, log *logrus.Entry) error {
	if err := GenerateDTSH(mistServerURL, streamName, log); err != nil {
		return err
	}
	if dtshPath == "" {
		return nil
	}
	return waitForDTSHFile(dtshPath, 10*time.Second)
}

func waitForDTSHFile(dtshPath string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		info, err := os.Stat(dtshPath)
		switch {
		case err == nil && info.Mode().IsRegular() && info.Size() > 0:
			if validateErr := dtsh.ValidateFile(dtshPath); validateErr != nil {
				lastErr = fmt.Errorf("dtsh file invalid: %s: %w", dtshPath, validateErr)
				break
			}
			return nil
		case err == nil:
			lastErr = fmt.Errorf("dtsh file is empty: %s", dtshPath)
		default:
			lastErr = err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("dtsh file not ready at %s: %w", dtshPath, lastErr)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// isHLSSource detects if the source is an HLS manifest (segmented).
func isHLSSource(sourceURL string, params map[string]string) bool {
	if strings.HasSuffix(strings.ToLower(sourceURL), ".m3u8") {
		return true
	}
	_, ok := params["segment_urls"]
	return ok
}

// rewriteHLSManifest downloads the HLS manifest, rewrites segment paths
// to presigned HTTPS URLs, and saves to local disk for MistServer to read.
// parseSegmentURLs decodes the segment_urls param: newline-separated
// "relative_path=presigned_url" pairs. Malformed lines (no "=") are skipped.
func parseSegmentURLs(raw string) map[string]string {
	segmentURLs := map[string]string{}
	if raw == "" {
		return segmentURLs
	}
	for _, pair := range strings.Split(raw, "\n") {
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) == 2 {
			segmentURLs[parts[0]] = parts[1]
		}
	}
	return segmentURLs
}

// rewriteHLSManifestBody remaps every segment and tag-URI reference in an HLS
// manifest to its presigned URL. A reference with no mapping is left verbatim;
// any reference that should be remapped but isn't would break playback, so this
// is the load-bearing correctness core, kept pure for exhaustive testing.
func rewriteHLSManifestBody(body string, segmentURLs map[string]string) string {
	var rewritten strings.Builder
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "#") {
			// Rewrite URIs inside HLS tags (#EXT-X-KEY, #EXT-X-MAP, etc.)
			if uri := extractHLSTagURI(line); uri != "" {
				if presigned, ok := segmentURLs[strings.TrimSpace(uri)]; ok {
					line = strings.Replace(line, `URI="`+uri+`"`, `URI="`+presigned+`"`, 1)
				}
			}
			rewritten.WriteString(line)
		} else if strings.TrimSpace(line) != "" {
			segName := strings.TrimSpace(line)
			if presigned, ok := segmentURLs[segName]; ok {
				rewritten.WriteString(presigned)
			} else {
				rewritten.WriteString(line)
			}
		} else {
			rewritten.WriteString(line)
		}
		rewritten.WriteString("\n")
	}
	return rewritten.String()
}

func (h *ProcessingJobHandler) rewriteHLSManifest(log *logrus.Entry, req *ipcpb.ProcessingJobRequest) (string, error) {
	params := req.GetParams()
	manifestURL := req.GetSourceUrl()

	httpReq, err := http.NewRequestWithContext(context.Background(), http.MethodGet, manifestURL, nil)
	if err != nil {
		return "", fmt.Errorf("build manifest request: %w", err)
	}
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("download manifest: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("manifest download returned %d", resp.StatusCode)
	}

	segmentURLs := parseSegmentURLs(params["segment_urls"])

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading manifest: %w", err)
	}
	rewrittenBody := rewriteHLSManifestBody(string(body), segmentURLs)

	procDir := filepath.Join(h.storagePath, "processing")
	if err := os.MkdirAll(procDir, 0755); err != nil {
		return "", fmt.Errorf("create processing dir: %w", err)
	}
	localPath := filepath.Join(procDir, req.GetArtifactHash()+".m3u8")
	if err := os.WriteFile(localPath, []byte(rewrittenBody), 0644); err != nil {
		return "", fmt.Errorf("write rewritten manifest: %w", err)
	}

	log.WithFields(logging.Fields{
		"segments_mapped": len(segmentURLs),
		"local_path":      localPath,
	}).Info("Rewrote HLS manifest with presigned segment URLs")

	return localPath, nil
}

// extractHLSTagURI extracts the URI value from HLS tags like
// #EXT-X-KEY:METHOD=AES-128,URI="key.bin" or #EXT-X-MAP:URI="init.mp4".
func extractHLSTagURI(line string) string {
	idx := strings.Index(line, `URI="`)
	if idx < 0 {
		return ""
	}
	start := idx + 5
	end := strings.Index(line[start:], `"`)
	if end < 0 {
		return ""
	}
	return line[start : start+end]
}

func (h *ProcessingJobHandler) sendResult(send func(*ipcpb.ControlMessage), jobID, status, errMsg string, outputs map[string]string, outputPath string, outputSizeBytes int64) {
	if send == nil {
		return
	}
	send(&ipcpb.ControlMessage{
		Payload: &ipcpb.ControlMessage_ProcessingJobResult{ProcessingJobResult: &ipcpb.ProcessingJobResult{
			JobId:           jobID,
			Status:          status,
			Error:           errMsg,
			Outputs:         outputs,
			OutputPath:      outputPath,
			OutputSizeBytes: outputSizeBytes,
		}},
		SentAt: timestamppb.Now(),
	})
}

// sendCompletedResult reports a VALIDATED, completed processing job. It carries the accepted
// full A/V track set (from the completion-gated RECORDING_END) and, when known, the output
// media duration. This is the sole authoritative track-capture point — behind Helmsman's
// stale/failed/retired-generation rejection — so Foghorn persists tracks by the already-resolved
// artifact_hash alongside size/duration/readiness, rather than trusting a raw RECORDING_END.
func (h *ProcessingJobHandler) sendCompletedResult(send func(*ipcpb.ControlMessage), jobID string, outputs map[string]string, outputPath string, outputSizeBytes, mediaDurationMs int64, tracks []*ipcpb.StreamTrack, tracksPresent bool) {
	if send == nil {
		return
	}
	res := &ipcpb.ProcessingJobResult{
		JobId:           jobID,
		Status:          "completed",
		Outputs:         outputs,
		OutputPath:      outputPath,
		OutputSizeBytes: outputSizeBytes,
		Tracks:          tracks,
		TracksPresent:   tracksPresent,
	}
	if mediaDurationMs > 0 {
		res.MediaDurationMs = &mediaDurationMs
	}
	send(&ipcpb.ControlMessage{
		Payload: &ipcpb.ControlMessage_ProcessingJobResult{ProcessingJobResult: res},
		SentAt:  timestamppb.Now(),
	})
}
