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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"frameworks/api_sidecar/internal/admission"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"

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
	pendingJobs   = map[string]chan struct{}{}
	pendingJobsMu sync.Mutex
)

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
		if stagedSourcePath != "" {
			if err := os.Remove(stagedSourcePath); err != nil && !os.IsNotExist(err) {
				log.WithError(err).Warn("Failed to remove staged processing source")
			}
		}
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

	outputs, sourceDurationMs, waitErr := h.waitForProcessingStreamReady(log, mistClient, req, streamName)
	if waitErr != nil {
		h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
		h.sendResult(send, req.GetJobId(), "failed", waitErr.Error(), nil, "", 0)
		return
	}
	h.sendProgress(send, req.GetJobId(), 0, 0, sourceDurationMs)

	if pushErr := h.startProcessingPush(log, mistClient, req, outputDir, streamName, outputPath); pushErr != nil {
		h.sendResult(send, req.GetJobId(), "failed", pushErr.Error(), nil, "", 0)
		return
	}

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
				if deleteErr := mistClient.DeleteStream(streamName); deleteErr != nil {
					log.WithError(deleteErr).Warn("Failed to delete stream for Livepeer fallback")
				}
				// Fresh doneCh so any PUSH_END from the old push doesn't
				// satisfy the completion check for the restarted push.
				doneCh = make(chan struct{}, 1)
				pendingJobsMu.Lock()
				pendingJobs[streamName] = doneCh
				pendingJobsMu.Unlock()
				outputs, sourceDurationMs, waitErr = h.waitForProcessingStreamReady(log, mistClient, req, streamName)
				if waitErr != nil {
					h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("livepeer fallback readiness: %v", waitErr), nil, "", 0)
					return
				}
				if pushErr := h.startProcessingPush(log, mistClient, req, outputDir, streamName, outputPath); pushErr != nil {
					h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("livepeer fallback restart: %v", pushErr), nil, "", 0)
					return
				}
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
					if deleteErr := mistClient.DeleteStream(streamName); deleteErr != nil {
						log.WithError(deleteErr).Warn("Failed to delete stream for stall fallback")
					}
					doneCh = make(chan struct{}, 1)
					pendingJobsMu.Lock()
					pendingJobs[streamName] = doneCh
					pendingJobsMu.Unlock()
					outputs, sourceDurationMs, waitErr = h.waitForProcessingStreamReady(log, mistClient, req, streamName)
					if waitErr != nil {
						h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("stall fallback readiness: %v", waitErr), nil, "", 0)
						return
					}
					if pushErr := h.startProcessingPush(log, mistClient, req, outputDir, streamName, outputPath); pushErr != nil {
						h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("stall fallback restart: %v", pushErr), nil, "", 0)
						return
					}
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
	h.sendProgress(send, req.GetJobId(), 100, sourceDurationMs, sourceDurationMs)

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
func (h *ProcessingJobHandler) startProcessingPush(log *logrus.Entry, mistClient *mist.Client, req *pb.ProcessingJobRequest, vodDir, streamName, outputPath string) error {
	if sm := GetStorageManager(); sm != nil {
		var estSize uint64
		if hint, ok := headContentLength(req.GetSourceUrl()); ok {
			estSize = hint
		}
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
// until Mist has exposed the source tracks plus the configured MistProc output
// tracks. Starting the file push before this point can permanently exclude
// generated tracks from the muxed artifact.
func (h *ProcessingJobHandler) waitForProcessingStreamReady(log *logrus.Entry, mistClient *mist.Client, req *pb.ProcessingJobRequest, streamName string) (map[string]string, int64, error) {
	requirements := expectedProcessingTracks(req.GetProcessesJson())
	deadline := time.Now().Add(45 * time.Second)
	var lastPresence processingTrackPresence
	var lastErr error
	bootTicker := time.NewTicker(500 * time.Millisecond)
	defer bootTicker.Stop()

	for {
		if err := h.bootMistStream(streamName); err != nil {
			lastErr = err
		}

		info, err := mistClient.GetStreamInfo(streamName)
		if err != nil {
			lastErr = err
		} else if info.Metadata != nil {
			lastPresence = inspectProcessingTracks(info.Metadata)
			if processingTracksComplete(lastPresence, requirements) {
				log.WithFields(logrus.Fields{
					"audio_codecs": mapKeys(lastPresence.audioCodecs),
					"video_codecs": mapKeys(lastPresence.videoCodecs),
					"meta_codecs":  mapKeys(lastPresence.metaCodecs),
				}).Info("Processing stream ready for muxed output")
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

func inspectProcessingTracks(meta map[string]interface{}) processingTrackPresence {
	p := processingTrackPresence{
		audioCodecs: map[string]bool{},
		videoCodecs: map[string]bool{},
		metaCodecs:  map[string]bool{},
		outputs:     extractTrackMetadata(meta),
	}

	metaRaw, ok := meta["meta"]
	if !ok {
		return p
	}
	metaMap, ok := metaRaw.(map[string]interface{})
	if !ok {
		return p
	}
	tracksRaw, ok := metaMap["tracks"]
	if !ok {
		return p
	}
	tracks, ok := tracksRaw.(map[string]interface{})
	if !ok {
		return p
	}
	for name, trackRaw := range tracks {
		track, ok := trackRaw.(map[string]interface{})
		if !ok {
			continue
		}
		codec := ""
		if v, ok := track["codec"].(string); ok {
			codec = normalizeTrackCodec(v)
		}
		trackType := ""
		if v, ok := track["type"].(string); ok {
			trackType = strings.ToLower(v)
		}
		if trackType == "" {
			switch {
			case strings.HasPrefix(name, "audio"):
				trackType = "audio"
			case strings.HasPrefix(name, "video"):
				trackType = "video"
			case strings.HasPrefix(name, "meta"), strings.HasPrefix(name, "subtitle"):
				trackType = "meta"
			}
		}
		switch trackType {
		case "audio":
			if codec != "" {
				p.audioCodecs[codec] = true
				p.sourceMedia = true
			}
		case "video":
			if codec != "" {
				p.videoCodecs[codec] = true
				if codec != "JPEG" {
					p.sourceMedia = true
				}
			}
		case "meta", "subtitle":
			if codec != "" {
				p.metaCodecs[codec] = true
			}
		default:
			switch {
			case isAudioCodec(codec):
				p.audioCodecs[codec] = true
				p.sourceMedia = true
			case codec == "JPEG":
				p.videoCodecs[codec] = true
			case codec != "":
				p.videoCodecs[codec] = true
				p.sourceMedia = true
			}
		}
	}
	return p
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
