package handlers

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"frameworks/pkg/logging"
	"frameworks/pkg/mist"
	pb "frameworks/pkg/proto"

	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ProcessingJobHandler handles VOD processing jobs from Foghorn.
// Activates the processing+{hash} wildcard stream in MistServer.
// STREAM_SOURCE provides the presigned S3 URL for the source.
// STREAM_PROCESS provides the MistProc* config (VP9/thumbs/audio) from Commodore.
type ProcessingJobHandler struct {
	logger        logging.Logger
	mistServerURL string
	storagePath   string
}

// pendingJobs tracks in-flight processing jobs, signaled by PUSH_END.
var (
	pendingJobs   = map[string]chan struct{}{}
	pendingJobsMu sync.Mutex
)

var (
	processingProcessOverrides   = map[string]string{}
	processingProcessOverridesMu sync.Mutex
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
	pendingJobsMu.Lock()
	ch, ok := pendingJobs[streamName]
	pendingJobsMu.Unlock()
	if ok {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
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
}

func getProcessingProcessOverride(streamName string) (string, bool) {
	processingProcessOverridesMu.Lock()
	processesJSON, ok := processingProcessOverrides[streamName]
	processingProcessOverridesMu.Unlock()
	return processesJSON, ok
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
func (h *ProcessingJobHandler) Handle(req *pb.ProcessingJobRequest, send func(*pb.ControlMessage)) {
	log := h.logger.WithFields(logging.Fields{
		"job_id":        req.GetJobId(),
		"job_type":      req.GetJobType(),
		"artifact_hash": req.GetArtifactHash(),
	})

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
			if err := os.Remove(hlsManifestPath); err != nil && !os.IsNotExist(err) {
				log.WithError(err).Warn("Failed to remove rewritten HLS manifest")
			}
		}()
	}

	// Register completion channel BEFORE activating stream
	doneCh := make(chan struct{}, 1)
	pendingJobsMu.Lock()
	pendingJobs[streamName] = doneCh
	pendingJobsMu.Unlock()

	defer func() {
		pendingJobsMu.Lock()
		delete(pendingJobs, streamName)
		pendingJobsMu.Unlock()
	}()

	// Register PROCESS_EXIT routing before starting the push so an immediate boot
	// failure cannot race past the listener setup.
	processExitCh := RegisterProcessExitListener(streamName)
	defer UnregisterProcessExitListener(streamName)

	mistClient := mist.NewClient(h.logger)
	if h.mistServerURL != "" {
		mistClient.BaseURL = h.mistServerURL
	}

	// MKV: only container MistServer can push to AND re-ingest that supports
	// the full codec set (H264/VP9/JPEG/AAC/opus). Raw absolute path — MistServer
	// matches against push_urls pattern "/*.mkv" → MistOutEBML.
	// Output goes to vod/ so the artifact poller discovers it for Foghorn registration.
	vodDir := filepath.Join(h.storagePath, "vod")
	if err := os.MkdirAll(vodDir, 0755); err != nil {
		log.WithError(err).Error("Failed to create vod directory")
		h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("mkdir failed: %v", err), nil, "", 0)
		return
	}
	outputPath := filepath.Join(vodDir, req.GetArtifactHash()+".mkv")
	if err := mistClient.PushStart(streamName, outputPath); err != nil {
		log.WithError(err).Error("Failed to start push")
		h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("push_start failed: %v", err), nil, "", 0)
		return
	}
	log.WithField("output_path", outputPath).Info("Started push for processing stream")

	// Poll for stream metadata while processing runs
	outputs := h.pollStreamMetadata(log, mistClient, streamName)

	// Extract source duration for progress calculation
	sourceDurationMs := sourceDurationFromOutputs(outputs)

	// Single select loop: 4 signal sources, one goroutine, no races.
	progressTicker := time.NewTicker(30 * time.Second)
	defer progressTicker.Stop()
	absoluteTimeout := time.After(4 * time.Hour)

	var lastMs int64
	lastAdvance := time.Now()
	const stallTimeout = 3 * time.Minute
	fallbackAttempted := false
	hasLivepeer := mist.HasLivepeerProcesses(req.GetProcessesJson())
	ignoredProcessExitBootCounts := map[string]int{}

loop:
	for {
		select {
		case <-doneCh:
			log.Info("Processing completed (PUSH_END received)")
			break loop

		case evt := <-processExitCh:
			evtFields := logging.Fields{
				"process":    evt.ProcessType,
				"exit_code":  evt.ExitCode,
				"boot_count": evt.BootCount,
				"status":     evt.Status,
				"reason":     evt.Reason,
			}
			if shouldIgnoreProcessExit(evt, ignoredProcessExitBootCounts) {
				log.WithFields(evtFields).Debug("Ignoring stale process exit from retired generation")
				continue
			}

			switch {
			case evt.Status == "unrecoverable" && evt.ProcessType == "Livepeer" && !fallbackAttempted:
				log.WithFields(evtFields).Warn("Livepeer unrecoverable, falling back to local MistProcAV")
				ignoreProcessExitThrough(ignoredProcessExitBootCounts, evt.ProcessType, evt.BootCount)
				h.stopProcessingPush(log, mistClient, streamName)
				os.Remove(outputPath)
				localConfig := mist.ReplaceLivepeerWithLocal(req.GetProcessesJson())
				setProcessingProcessOverride(streamName, localConfig)
				h.updateProcessConfigCache(send, req.GetArtifactHash(), localConfig)
				if err := mistClient.DeleteStream(streamName); err != nil {
					log.WithError(err).Warn("Failed to delete stream for Livepeer fallback")
				}
				// Fresh doneCh so any PUSH_END from the old push doesn't
				// satisfy the completion check for the restarted push.
				doneCh = make(chan struct{}, 1)
				pendingJobsMu.Lock()
				pendingJobs[streamName] = doneCh
				pendingJobsMu.Unlock()
				if err := mistClient.PushStart(streamName, outputPath); err != nil {
					log.WithError(err).Error("Failed to restart push after Livepeer fallback")
					h.sendResult(send, req.GetJobId(), "failed", "livepeer fallback restart failed", nil, "", 0)
					return
				}
				outputs, sourceDurationMs = h.refreshProcessingMetadata(log, mistClient, streamName, outputs)
				lastMs = 0
				lastAdvance = time.Now()
				fallbackAttempted = true
				hasLivepeer = false
				h.sendProgress(send, req.GetJobId(), 0, 0, sourceDurationMs)

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
					ignoreProcessExitThrough(ignoredProcessExitBootCounts, "Livepeer", 0)
					h.stopProcessingPush(log, mistClient, streamName)
					os.Remove(outputPath)
					localConfig := mist.ReplaceLivepeerWithLocal(req.GetProcessesJson())
					setProcessingProcessOverride(streamName, localConfig)
					h.updateProcessConfigCache(send, req.GetArtifactHash(), localConfig)
					if err := mistClient.DeleteStream(streamName); err != nil {
						log.WithError(err).Warn("Failed to delete stream for stall fallback")
					}
					doneCh = make(chan struct{}, 1)
					pendingJobsMu.Lock()
					pendingJobs[streamName] = doneCh
					pendingJobsMu.Unlock()
					if err := mistClient.PushStart(streamName, outputPath); err != nil {
						log.WithError(err).Error("Failed to restart push after stall fallback")
						h.sendResult(send, req.GetJobId(), "failed", "stall fallback restart failed", nil, "", 0)
						return
					}
					outputs, sourceDurationMs = h.refreshProcessingMetadata(log, mistClient, streamName, outputs)
					lastMs = 0
					lastAdvance = time.Now()
					fallbackAttempted = true
					hasLivepeer = false
					h.sendProgress(send, req.GetJobId(), 0, 0, sourceDurationMs)
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

	outputSizeBytes, err := waitForProcessingOutput(outputPath, 5*time.Second)
	if err != nil {
		log.WithError(err).Error("Processed output validation failed")
		h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
		h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("output validation failed: %v", err), nil, "", 0)
		return
	}

	// Send result with output path so Foghorn can register the artifact
	// in the warm cache immediately (same pattern as defrost completion).
	// This must happen BEFORE DTSH generation because vod+ STREAM_SOURCE
	// resolves via Foghorn's in-memory state.
	h.sendResult(send, req.GetJobId(), "completed", "", outputs, outputPath, outputSizeBytes)
	log.Info("Processing job result sent, artifact registered with Foghorn")

	// Generate DTSH by booting the output as vod+ (no MistProc* re-trigger).
	// Foghorn now has the artifact registered, so vod+ STREAM_SOURCE resolves.
	vodStreamName := "vod+" + req.GetInternalName()
	if err := GenerateDTSH(h.mistServerURL, vodStreamName, log); err != nil {
		log.WithError(err).Warn("DTSH generation failed (will be generated on first playback)")
	}

	// Trigger storage check so the .mkv + .dtsh freeze to S3 promptly
	TriggerStorageCheck()
}

// pollStreamMetadata waits for MistServer to parse the stream headers and
// extracts codec, resolution, duration, etc. from the stream info API.
func (h *ProcessingJobHandler) pollStreamMetadata(log *logrus.Entry, mistClient *mist.Client, streamName string) map[string]string {
	outputs := map[string]string{}

	for i := 0; i < 30; i++ {
		time.Sleep(1 * time.Second)

		info, err := mistClient.GetStreamInfo(streamName)
		if err != nil {
			continue
		}
		if info.Metadata == nil {
			continue
		}

		extracted := extractTrackMetadata(info.Metadata)
		if len(extracted) > 0 {
			return extracted
		}
	}

	log.Warn("Timed out waiting for stream metadata")
	return outputs
}

func (h *ProcessingJobHandler) refreshProcessingMetadata(log *logrus.Entry, mistClient *mist.Client, streamName string, current map[string]string) (map[string]string, int64) {
	refreshed := h.pollStreamMetadata(log, mistClient, streamName)
	if len(refreshed) == 0 {
		return current, sourceDurationFromOutputs(current)
	}
	return refreshed, sourceDurationFromOutputs(refreshed)
}

func sourceDurationFromOutputs(outputs map[string]string) int64 {
	if durStr, ok := outputs["duration_ms"]; ok {
		if dur, err := strconv.ParseInt(durStr, 10, 64); err == nil && dur > 0 {
			return dur
		}
	}
	return 0
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
			if v, ok := track["codec"].(string); ok {
				outputs["video_codec"] = v
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

func (h *ProcessingJobHandler) sendProgress(send func(*pb.ControlMessage), jobID string, progressPct int32, lastMs, sourceDurationMs int64) {
	if send == nil {
		return
	}
	send(&pb.ControlMessage{
		Payload: &pb.ControlMessage_ProcessingJobProgress{
			ProcessingJobProgress: &pb.ProcessingJobProgress{
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
func (h *ProcessingJobHandler) updateProcessConfigCache(send func(*pb.ControlMessage), artifactHash, processesJSON string) {
	if send == nil {
		return
	}
	send(&pb.ControlMessage{
		Payload: &pb.ControlMessage_ProcessingJobResult{
			ProcessingJobResult: &pb.ProcessingJobResult{
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
	resp, err := mistClient.GetActiveStreams()
	if err != nil {
		return 0
	}
	activeStreams, ok := resp["active_streams"].(map[string]interface{})
	if !ok {
		return 0
	}
	streamData, ok := activeStreams[streamName].(map[string]interface{})
	if !ok {
		return 0
	}
	if lastms, ok := streamData["lastms"].(float64); ok {
		return int64(lastms)
	}
	return 0
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
	if err := mistClient.DeleteStream(streamName); err != nil {
		log.WithError(err).Warn("Failed to delete stream during cleanup")
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
	url := strings.TrimRight(mistServerURL, "/") + "/json_" + streamName + ".js"

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
func (h *ProcessingJobHandler) rewriteHLSManifest(log *logrus.Entry, req *pb.ProcessingJobRequest) (string, error) {
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

func (h *ProcessingJobHandler) sendResult(send func(*pb.ControlMessage), jobID, status, errMsg string, outputs map[string]string, outputPath string, outputSizeBytes int64) {
	if send == nil {
		return
	}
	send(&pb.ControlMessage{
		Payload: &pb.ControlMessage_ProcessingJobResult{ProcessingJobResult: &pb.ProcessingJobResult{
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
