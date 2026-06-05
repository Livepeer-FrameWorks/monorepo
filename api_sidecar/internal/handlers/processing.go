package handlers

import (
	"bufio"
	"context"
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
	pendingJobs   = map[string]chan ProcessingPushEndEvent{}
	pendingJobsMu sync.Mutex
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
}

var (
	processingProcessOverrides   = map[string]string{}
	processingProcessOverridesMu sync.Mutex
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

func setProcessingProcessOverride(streamName, processesJSON string) {
	if streamName == "" || processesJSON == "" {
		return
	}
	processingProcessOverridesMu.Lock()
	processingProcessOverrides[streamName] = processesJSON
	processingProcessOverridesMu.Unlock()
}

func clearProcessingProcessOverride(streamName string) {
	processingProcessOverridesMu.Lock()
	delete(processingProcessOverrides, streamName)
	processingProcessOverridesMu.Unlock()
	processingSourceOverridesMu.Lock()
	delete(processingSourceOverrides, streamName)
	processingSourceOverridesMu.Unlock()
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

// restartProcessingStreamForLocalFallback tears down the retired generation and
// clears its broken artifact, in order: stop the push, nuke the stream, confirm
// the generation has drained, and only THEN remove the output file — so the
// retired push (still the writer until it is stopped and drained) cannot keep
// writing the path after it is cleared. Drain or remove failures abort the
// fallback (returns an error) rather than racing a half-torn-down generation
// against the local retry.
func (h *ProcessingJobHandler) restartProcessingStreamForLocalFallback(log *logrus.Entry, mistClient *mist.Client, streamName, outputPath string) error {
	h.stopProcessingPush(log, mistClient, streamName)
	if nukeErr := mistClient.NukeStream(streamName); nukeErr != nil {
		// Nuke is best-effort: the stream may already be gone. The drain below is
		// the authoritative teardown confirmation.
		log.WithError(nukeErr).Warn("NukeStream during fallback returned an error; relying on drain to confirm teardown")
	}
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
)

type processingActiveStreamsFunc func() (map[string]interface{}, error)
type processingClientsFunc func() (map[string]interface{}, error)

// drainProcessingGeneration blocks until the stream is no longer active, so a
// restarted push cannot race the retired generation. A transient stream-status
// read is retried within the window; failing to confirm teardown by the deadline
// returns an error so the caller aborts rather than restarting over a live
// generation.
func drainProcessingGeneration(log *logrus.Entry, mistClient *mist.Client, streamName string) error {
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

func drainProcessingSessions(log *logrus.Entry, mistClient *mist.Client, streamName string) error {
	return drainProcessingSessionsFromClients(log, streamName, mistClient.GetClients, processingGenerationDrainTimeout, processingGenerationDrainPollInterval)
}

func drainProcessingSessionsFromClients(log *logrus.Entry, streamName string, getClients processingClientsFunc, timeout, pollInterval time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := getClients()
		if err != nil {
			log.WithError(err).Warn("Failed to check processing session shutdown; retrying")
			time.Sleep(pollInterval)
			continue
		}
		active, err := processingClientCount(resp, streamName)
		if err != nil {
			log.WithError(err).Warn("Failed to parse processing sessions; retrying")
			time.Sleep(pollInterval)
			continue
		}
		if active == 0 {
			return nil
		}
		time.Sleep(pollInterval)
	}
	return fmt.Errorf("processing stream %s still has active sessions after drain deadline", streamName)
}

func processingClientCount(resp map[string]interface{}, streamName string) (int, error) {
	clients, ok := resp["clients"].(map[string]interface{})
	if !ok {
		return 0, fmt.Errorf("clients missing from Mist response")
	}
	fieldsRaw, ok := clients["fields"].([]interface{})
	if !ok {
		return 0, fmt.Errorf("client fields missing from Mist response")
	}
	streamIdx := -1
	for i, f := range fieldsRaw {
		if name, isString := f.(string); isString && name == "stream" {
			streamIdx = i
			break
		}
	}
	if streamIdx < 0 {
		return 0, fmt.Errorf("stream field missing from Mist clients response")
	}
	data, ok := clients["data"].([]interface{})
	if !ok {
		return 0, fmt.Errorf("client data missing from Mist response")
	}
	count := 0
	for _, rowRaw := range data {
		row, ok := rowRaw.([]interface{})
		if !ok || streamIdx >= len(row) {
			continue
		}
		if name, ok := row[streamIdx].(string); ok && name == streamName {
			count++
		}
	}
	return count, nil
}

func NewProcessingJobHandler(logger logging.Logger, mistServerURL, storagePath string) *ProcessingJobHandler {
	return &ProcessingJobHandler{
		logger:        logger,
		mistServerURL: mistServerURL,
		storagePath:   storagePath,
	}
}

// Handle executes a processing job: activates the processing+ wildcard stream,
// starts a push to local disk as MKV, waits for PUSH_END, reports result.
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

	log.Info("Processing job received")
	streamName := "processing+" + req.GetArtifactHash()
	defer clearProcessingProcessOverride(streamName)

	// If a previous attempt for this artifact is still running on this node,
	// silently drop the duplicate. Don't send a failure — the original attempt
	// is still active and will complete or fail on its own.
	if HasPendingJob(streamName) {
		log.Warn("Previous processing attempt still active, ignoring duplicate dispatch")
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
	clipSource := isClipProcessingSource(req)
	var stagedSourcePath string
	defer func() {
		cleanupProcessingStagePath(log, stagedSourcePath)
	}()
	if clipSource {
		if sourceURL == "" {
			h.sendResult(send, req.GetJobId(), "failed", "clip processing source URL unavailable", nil, "", 0)
			return
		}
		path, err := h.stageProcessingSource(log, req, sourceURL)
		if err != nil {
			h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("clip source stage failed: %v", err), nil, "", 0)
			return
		}
		stagedSourcePath = path
		setProcessingSourceOverride(streamName, path)
		log.WithField("staged_path", path).Info("Staged clip source for processing")
	} else if sourceURL != "" && req.GetSourceUrl() == "" {
		setProcessingSourceOverride(streamName, sourceURL)
		log.WithField("source_url", sourceURL).Info("Registered local processing source override")
	} else if sourceURL == "" {
		log.Warn("Processing job has no source URL or local source parameters")
	}

	if !clipSource {
		if ext := unsafeWrapperExt(req.GetSourceUrl()); ext != "" {
			path, err := h.stageUnsafeWrapper(log, req, ext)
			if err != nil {
				h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("unsafe-wrapper stage failed: %v", err), nil, "", 0)
				return
			}
			stagedSourcePath = path
			log.WithField("staged_path", path).Info("Staged unsafe-wrapper source locally")
		}
	}

	// For segmented (HLS) sources, rewrite manifest with presigned segment URLs
	var hlsManifestPath string
	if !clipSource && isHLSSource(req.GetSourceUrl(), req.GetParams()) {
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

	// Register completion channel BEFORE activating stream
	doneCh := make(chan ProcessingPushEndEvent, 1)
	pendingJobsMu.Lock()
	pendingJobs[streamName] = doneCh
	pendingJobsMu.Unlock()
	recordingEndCh := registerProcessingRecordingEndListener(streamName)

	defer func() {
		pendingJobsMu.Lock()
		delete(pendingJobs, streamName)
		pendingJobsMu.Unlock()
		unregisterProcessingRecordingEndListener(streamName)
	}()

	// Register PROCESS_EXIT routing before starting the push so an immediate boot
	// failure cannot race past the listener setup.
	processExitCh := RegisterProcessExitListener(streamName)
	defer UnregisterProcessExitListener(streamName)

	mistClient := mist.NewClient(h.logger)
	if h.mistServerURL != "" {
		mistClient.BaseURL = h.mistServerURL
	}

	// MKV is the processing output container MistServer can push and
	// re-ingest across the codec set we use. Clips land in the same
	// stream-scoped clips/ namespace Foghorn registered for playback;
	// VOD and upload processing land in vod/.
	outputDir, outputPath, outputErr := h.processingOutputPath(req, clipSource)
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

	outputs, sourceDurationMs, waitErr := h.waitForProcessingStreamReady(log, mistClient, req, streamName, processExitCh, ignoredProcessExitBootCounts)
	if waitErr != nil {
		var livepeerBootErr *livepeerReadinessFallbackError
		if errors.As(waitErr, &livepeerBootErr) && !fallbackAttempted {
			log.WithFields(processExitFields(livepeerBootErr.evt)).Warn("Livepeer unrecoverable during readiness, falling back to local MistProcAV")
			ignoreProcessExitThrough(ignoredProcessExitBootCounts, livepeerBootErr.evt.ProcessType, livepeerBootErr.evt.BootCount)
			localConfig := mist.ReplaceLivepeerWithLocal(req.GetProcessesJson())
			setProcessingProcessOverride(streamName, localConfig)
			h.updateProcessConfigCache(send, req.GetArtifactHash(), localConfig)
			if teardownErr := h.restartProcessingStreamForLocalFallback(log, mistClient, streamName, outputPath); teardownErr != nil {
				h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
				h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("livepeer fallback teardown: %v", teardownErr), nil, "", 0)
				return
			}
			doneCh = make(chan ProcessingPushEndEvent, 1)
			pendingJobsMu.Lock()
			pendingJobs[streamName] = doneCh
			pendingJobsMu.Unlock()
			recordingEndCh = registerProcessingRecordingEndListener(streamName)
			outputs, sourceDurationMs, waitErr = h.waitForProcessingStreamReady(log, mistClient, req, streamName, processExitCh, ignoredProcessExitBootCounts)
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
	if pushErr := h.startProcessingPush(log, mistClient, req, outputDir, streamName, outputPath); pushErr != nil {
		h.sendResult(send, req.GetJobId(), "failed", pushErr.Error(), nil, "", 0)
		return
	}

	// Single select loop: terminal triggers, process state, progress, and timeouts.
	progressTicker := time.NewTicker(30 * time.Second)
	defer progressTicker.Stop()
	absoluteTimeout := time.After(4 * time.Hour)

	var lastMs int64
	lastAdvance := time.Now()
	var recordingEnd *ProcessingRecordingEndEvent
	pushEndReceived := false
	var recordingEndDeadline <-chan time.Time
	const stallTimeout = 3 * time.Minute

	// restartWithLocalFallback swaps Livepeer for local MistProcAV and restarts
	// the push, returning false only after it has already reported a terminal
	// failure (caller must return). Consolidates the Livepeer→local recovery
	// shared by the unrecoverable-exit, stall, and incomplete-rendition paths.
	// ignoreType/ignoreBoot retire stale PROCESS_EXIT events from the old push.
	restartWithLocalFallback := func(ignoreType string, ignoreBoot int) bool {
		ignoreProcessExitThrough(ignoredProcessExitBootCounts, ignoreType, ignoreBoot)
		localConfig := mist.ReplaceLivepeerWithLocal(req.GetProcessesJson())
		setProcessingProcessOverride(streamName, localConfig)
		h.updateProcessConfigCache(send, req.GetArtifactHash(), localConfig)
		if teardownErr := h.restartProcessingStreamForLocalFallback(log, mistClient, streamName, outputPath); teardownErr != nil {
			h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
			h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("livepeer fallback teardown: %v", teardownErr), nil, "", 0)
			return false
		}
		// Fresh doneCh so a PUSH_END from the retired push can't satisfy the
		// restarted push's completion check.
		doneCh = make(chan ProcessingPushEndEvent, 1)
		pendingJobsMu.Lock()
		pendingJobs[streamName] = doneCh
		pendingJobsMu.Unlock()
		recordingEndCh = registerProcessingRecordingEndListener(streamName)
		// Discard any RECORDING_END captured from the retired push; the
		// restarted push produces a fresh one. Without this the post-loop
		// validation would run against the old push's bytes/duration/path.
		recordingEnd = nil
		pushEndReceived = false
		recordingEndDeadline = nil
		var waitErr error
		outputs, sourceDurationMs, waitErr = h.waitForProcessingStreamReady(log, mistClient, req, streamName, processExitCh, ignoredProcessExitBootCounts)
		if waitErr != nil {
			h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
			h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("livepeer fallback readiness: %v", waitErr), nil, "", 0)
			return false
		}
		currentPushStartedAt = time.Now().Unix()
		if pushErr := h.startProcessingPush(log, mistClient, req, outputDir, streamName, outputPath); pushErr != nil {
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
		if !pushEndReceived || recordingEnd == nil {
			return false, false
		}
		srcInfo, srcSpan := sourceFromReadinessOutputs(outputs)
		if hasLivepeer && !fallbackAttempted && !livepeerRenditionsCompleteFromTracks(log, req.GetProcessesJson(), recordingEnd.Tracks, srcInfo, srcSpan) {
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
			log.Info("Processing completed (PUSH_END received)")
			pushEndReceived = true
			recordingEndDeadline = time.After(5 * time.Second)
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
			recordingEndDeadline = nil
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
			}

		case <-progressTicker.C:
			currentMs := h.getStreamLastMs(mistClient, streamName)
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

		case <-recordingEndDeadline:
			log.Error("Processing PUSH_END received without matching RECORDING_END; failing job (RECORDING_END is required for push-to-file)")
			h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
			h.sendResult(send, req.GetJobId(), "failed", "RECORDING_END missing after PUSH_END", nil, "", 0)
			return

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
	if clipSource {
		if err := drainProcessingSessions(log, mistClient, streamName); err != nil {
			log.WithError(err).Error("Processing session drain failed before clip publication")
			h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
			h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("clip publication drain failed: %v", err), nil, "", 0)
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
			if clipSource {
				log.WithError(err).Error("Clip DTSH generation failed before publication")
				h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
				h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("clip dtsh generation failed: %v", err), nil, "", 0)
				return
			}
			log.WithError(err).Warn("DTSH generation failed (will be generated on first playback)")
		}
	}

	// Send result with output path so Foghorn can register the artifact
	// in the warm cache immediately. DTSH generation above uses a temporary
	// local source override, so playback cannot win the publish-vs-sidecar
	// race for freshly-created clips.
	h.sendResult(send, req.GetJobId(), "completed", "", outputs, outputPath, outputSizeBytes)
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
func (h *ProcessingJobHandler) startProcessingPush(log *logrus.Entry, mistClient *mist.Client, req *ipcpb.ProcessingJobRequest, vodDir, streamName, outputPath string) error {
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
			return fmt.Errorf("admission rejected: %w", decErr)
		}
	}
	targetURI := processingMuxTargetURI(outputPath)
	if err := mistClient.PushStart(streamName, targetURI); err != nil {
		log.WithError(err).Error("Failed to start push")
		return fmt.Errorf("push_start failed: %w", err)
	}
	log.WithFields(logrus.Fields{
		"output_path": outputPath,
		"target_uri":  targetURI,
	}).Info("Started push for processing stream")
	return nil
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
	outputs     map[string]string
	sourceMedia bool
}

// waitForProcessingStreamReady boots the processing+ wildcard stream and waits
// until Mist has exposed source media. Mist's file-output path owns the
// generated-track/header gate, so push_start can pin the stream before short
// VOD inputs run to EOF.
func (h *ProcessingJobHandler) waitForProcessingStreamReady(log *logrus.Entry, mistClient *mist.Client, req *ipcpb.ProcessingJobRequest, streamName string, processExitCh <-chan ProcessExitEvent, ignoredProcessExitBootCounts map[string]int) (map[string]string, int64, error) {
	requirements := expectedProcessingTracks(req.GetProcessesJson())
	deadline := time.Now().Add(45 * time.Second)
	var lastPresence processingTrackPresence
	var lastErr error
	bootTicker := time.NewTicker(500 * time.Millisecond)
	defer bootTicker.Stop()

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
			}
		}

		if err := h.bootMistStream(streamName); err != nil {
			lastErr = err
		}

		streamData, err := h.getActiveProcessingStreamData(mistClient, streamName)
		if err != nil {
			lastErr = err
		} else {
			lastPresence = inspectProcessingActiveStream(streamData)
			if lastPresence.sourceMedia {
				log.WithFields(logrus.Fields{
					"audio_codecs": mapKeys(lastPresence.audioCodecs),
					"video_codecs": mapKeys(lastPresence.videoCodecs),
					"meta_codecs":  mapKeys(lastPresence.metaCodecs),
				}).Info("Processing source stream ready for muxed output")
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

		<-bootTicker.C
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
	return outputPath + "#audio=all&video=all&meta=all&subtitle=all"
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

// renditionResolutionTolerance absorbs the ±16px rounding MistProcLivepeer
// applies when matching a requested profile resolution to a produced track.
const renditionResolutionTolerance = 16

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// livepeerProfileDim reads an integer dimension (width/height) from a normalized
// Livepeer profile, tolerating int/float64/json.Number encodings.
func livepeerProfileDim(prof mist.LivepeerJSONProfile, key string) int {
	switch v := prof[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return int(n)
		}
	}
	return 0
}

// processingMetaVideoTrack is one video track from a Mist stream's JSON
// metadata, carrying the per-track span (firstms/lastms) MistServer emits in
// DTSC::Meta::toJSON.
type processingMetaVideoTrack struct {
	codec   string
	width   int
	height  int
	firstms float64
	lastms  float64
}

func (t processingMetaVideoTrack) spanMs() float64 { return t.lastms - t.firstms }

// livepeerRenditionsComplete fetches the finished processing stream's JSON
// metadata and reports whether the Livepeer renditions are complete enough to
// publish. The caller supplies the source dims + a readiness span LOWER BOUND;
// the span baseline is then raised to the longest source-height passthrough track
// (authoritative once the source ran to EOF). The baseline is never derived from a
// transcode RENDITION track, so the check is not self-referential against the
// rendition output.
//
// Two failure modes are caught:
//   - missing/wrong renditions: a requested target-profile resolution has no
//     DISTINCT matching OUTPUT track (the source track is excluded first, and
//     each track satisfies at most one profile);
//   - truncated renditions: a matched track whose span falls more than
//     maxRenditionSpanShortfallMs below the source span (the EOF-race symptom).
//
// It does NOT fail open: when completeness cannot be proven from the final
// RECORDING_END track summary (missing/short/wrong tracks), it returns false so
// the caller runs the one local-MistProcAV fallback rather than publishing a
// possibly-incomplete Livepeer output. "No renditions requested" returns true
// because there is nothing to prove.
func livepeerRenditionsCompleteFromTracks(log *logrus.Entry, processesJSON string, tracks []processingMetaVideoTrack, source mist.SourceMediaInfo, sourceSpanMs float64) bool {
	expected, err := mist.AllLivepeerProfilesFromProcessesJSON(processesJSON, source)
	if err != nil {
		log.WithError(err).Warn("Malformed Livepeer process config; treating renditions as incomplete")
		return false
	}
	if len(expected) == 0 {
		return true
	}
	return renditionsCompleteFromTracks(log, expected, tracks, source, sourceSpanMs)
}

// renditionsCompleteFromTracks is the pure rendition-completeness decision,
// split out so it can be unit-tested without a MistServer. Renditions are keyed
// by height (the dimension a Livepeer profile actually specifies; width follows
// the source aspect), so the check does not depend on width being derivable.
// Each requested height must map to a DISTINCT output track (one source-height
// track excluded) whose span covers the source within maxRenditionSpanShortfallMs.
//
// It fails CLOSED on uncertainty: no tracks, no independent source span, a
// requested height that cannot be determined, or a missing/short track all
// return false so the caller runs the local-MistProcAV fallback rather than
// publishing.
func renditionsCompleteFromTracks(log *logrus.Entry, expected []mist.LivepeerJSONProfile, videoTracks []processingMetaVideoTrack, source mist.SourceMediaInfo, sourceSpanMs float64) bool {
	if len(videoTracks) == 0 {
		log.Warn("Finished processing stream exposes no video tracks; renditions incomplete")
		return false
	}

	// Source height for exclusion: the readiness baseline when known, otherwise
	// the tallest output track (the un-downscaled source).
	srcHeight := source.Height
	if srcHeight <= 0 {
		for _, t := range videoTracks {
			if t.height > srcHeight {
				srcHeight = t.height
			}
		}
	}

	// Pick the source passthrough deterministically: the LONGEST source-height
	// track. videoTracks comes from a Go map range, so order is nondeterministic —
	// excluding the FIRST source-height track could exclude a truncated same-height
	// rendition (e.g. a short 720p transcode of a 720p source) and then let the full
	// source track satisfy that requested 720p rendition, publishing a short same-
	// height output. The source ran to EOF (PUSH_END), so its passthrough is the
	// longest track at the source height; excluding exactly it leaves any short
	// same-height rendition in the pool to fail coverage, and its span is the
	// authoritative full source duration.
	srcIdx := -1
	for i, t := range videoTracks {
		if srcHeight > 0 && absInt(t.height-srcHeight) <= renditionResolutionTolerance {
			if srcIdx < 0 || t.spanMs() > videoTracks[srcIdx].spanMs() {
				srcIdx = i
			}
		}
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

	// Span baseline must be INDEPENDENT of the transcode renditions. Deriving it
	// from a rendition track would hide a uniform truncation where every produced
	// rendition is short. The readiness duration (captured before transcode)
	// qualifies, but it is only a snapshot — readiness can fire before a VOD input
	// runs to EOF, understating a source whose true length is longer. So raise it to
	// the source passthrough track's own span, which at PUSH_END reflects the whole
	// source and is not a rendition output. Without any source span we cannot prove
	// non-truncation, so fail closed.
	if sourceSpanMs <= 0 {
		log.Warn("No independent source span to validate rendition length; treating renditions as incomplete")
		return false
	}
	baseline := sourceSpanMs
	if sourceTrackSpanMs > baseline {
		baseline = sourceTrackSpanMs
	}

	consumed := make([]bool, len(pool))
	for _, prof := range expected {
		eh := livepeerProfileDim(prof, "height")
		if eh <= 0 {
			// A requested rendition whose height cannot be determined cannot be
			// proven present — fail closed rather than skipping it.
			log.Warn("Cannot determine a requested Livepeer rendition height; treating renditions as incomplete")
			return false
		}
		idx := -1
		for i, t := range pool {
			if !consumed[i] && absInt(t.height-eh) <= renditionResolutionTolerance {
				idx = i
				break
			}
		}
		if idx < 0 {
			log.WithField("missing_rendition_height", eh).Warn("Finished processing stream is missing a requested Livepeer rendition")
			return false
		}
		consumed[idx] = true
		if baseline-pool[idx].spanMs() > maxRenditionSpanShortfallMs {
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
		if srcHeight > 0 && absInt(t.height-srcHeight) <= renditionResolutionTolerance {
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
		t := processingMetaVideoTrack{codec: codec}
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
			codec:   codec,
			width:   int(track.GetWidth()),
			height:  int(track.GetHeight()),
			firstms: float64(track.GetFirstMs()),
			lastms:  float64(track.GetLastMs()),
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
			if _, exists := outputs["video_codec"]; exists {
				continue
			}
			if codec != "" {
				outputs["video_codec"] = codec
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
			if v, ok := track["kbits"].(float64); ok && v > 0 {
				outputs["bitrate_kbps"] = strconv.Itoa(int(v))
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

// RouteProcessExit delivers a PROCESS_EXIT event to the processing handler
// listening on the given stream. No-op if no listener is registered.
func RouteProcessExit(evt ProcessExitEvent) {
	processExitListenersMu.Lock()
	ch, ok := processExitListeners[evt.StreamName]
	processExitListenersMu.Unlock()
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

func (h *ProcessingJobHandler) sendProgress(send func(*ipcpb.ControlMessage), jobID string, progressPct int32, lastMs, sourceDurationMs int64) {
	if send == nil {
		return
	}
	send(&ipcpb.ControlMessage{
		Payload: &ipcpb.ControlMessage_ProcessingJobProgress{
			ProcessingJobProgress: &ipcpb.ProcessingJobProgress{
				JobId:            jobID,
				ProgressPct:      progressPct,
				LastMs:           lastMs,
				SourceDurationMs: sourceDurationMs,
			},
		},
		SentAt: timestamppb.Now(),
	})
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

// stopProcessingPush finds and stops the MistServer push for a processing stream.
func (h *ProcessingJobHandler) stopProcessingPush(log *logrus.Entry, mistClient *mist.Client, streamName string) {
	pushes, err := mistClient.PushList()
	if err != nil {
		log.WithError(err).Warn("Failed to list pushes for cleanup")
		return
	}
	for _, p := range pushes {
		if p.StreamName == streamName {
			if stopErr := mistClient.PushStop(p.ID); stopErr != nil {
				log.WithError(stopErr).WithField("push_id", p.ID).Warn("Failed to stop processing push")
			}
			return
		}
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
func (h *ProcessingJobHandler) rewriteHLSManifest(log *logrus.Entry, req *ipcpb.ProcessingJobRequest) (string, error) {
	params := req.GetParams()
	manifestURL := req.GetSourceUrl()

	httpReq, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, manifestURL, nil)
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("download manifest: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("manifest download returned %d", resp.StatusCode)
	}

	// segment_urls: newline-separated "relative_path=presigned_url" pairs
	segmentURLs := map[string]string{}
	if raw, ok := params["segment_urls"]; ok {
		for _, pair := range strings.Split(raw, "\n") {
			parts := strings.SplitN(pair, "=", 2)
			if len(parts) == 2 {
				segmentURLs[parts[0]] = parts[1]
			}
		}
	}

	var rewritten strings.Builder
	scanner := bufio.NewScanner(resp.Body)
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
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("reading manifest: %w", err)
	}

	procDir := filepath.Join(h.storagePath, "processing")
	if err := os.MkdirAll(procDir, 0755); err != nil {
		return "", fmt.Errorf("create processing dir: %w", err)
	}
	localPath := filepath.Join(procDir, req.GetArtifactHash()+".m3u8")
	if err := os.WriteFile(localPath, []byte(rewritten.String()), 0644); err != nil {
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
