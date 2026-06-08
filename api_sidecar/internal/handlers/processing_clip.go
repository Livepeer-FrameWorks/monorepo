package handlers

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// handleClip finalizes a clip — a live artifact cut from a stream's time range.
// Like DVR chapter finalize, a clip never runs a fresh transcode: it publishes
// the complete transcoded rendition set already present in the cut source
// material, otherwise the source passthrough, never a partial rendition set.
// The job's Livepeer/transcode config is stripped; the push selects tracks
// instead of muxing everything (video=all), and the whole-output duration gate
// fails the clip when neither a complete ladder nor a complete source exists.
func (h *ProcessingJobHandler) handleClip(req *ipcpb.ProcessingJobRequest, send func(*ipcpb.ControlMessage)) {
	log := h.logger.WithFields(logging.Fields{
		"job_id":        req.GetJobId(),
		"job_type":      req.GetJobType(),
		"artifact_hash": req.GetArtifactHash(),
	})
	log.Info("Clip processing job received")

	streamName := "processing+" + req.GetArtifactHash()
	defer clearProcessingProcessOverride(streamName)

	if HasPendingJob(streamName) {
		log.Warn("Clip: previous attempt still active, ignoring duplicate dispatch")
		return
	}

	// Resolve + stage the clip source (a Mist /view cut) to local disk; Mist
	// reads the staged file through the STREAM_SOURCE override.
	sourceURL := strings.TrimSpace(req.GetSourceUrl())
	if sourceURL == "" {
		sourceURL = h.buildLocalProcessingSourceURL(req)
	}
	if sourceURL == "" {
		h.sendResult(send, req.GetJobId(), "failed", "clip processing source URL unavailable", nil, "", 0)
		return
	}
	stagedSourcePath, stageErr := h.stageProcessingSource(log, req, sourceURL)
	if stageErr != nil {
		h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("clip source stage failed: %v", stageErr), nil, "", 0)
		return
	}
	defer cleanupProcessingStagePath(log, stagedSourcePath)
	setProcessingSourceOverride(streamName, stagedSourcePath)
	log.WithField("staged_path", stagedSourcePath).Info("Staged clip source for processing")

	// Clips are live artifacts: strip the transcode so no fresh transcode runs.
	// Side processes (thumbnails) are kept; the push below selects either the
	// complete renditions already present in the cut or the source passthrough.
	effectiveProcessesJSON := mist.StripLivepeerProcesses(req.GetProcessesJson())
	if mist.HasLivepeerProcesses(effectiveProcessesJSON) {
		log.Warn("Clip: ignoring Livepeer process config")
		effectiveProcessesJSON = "[]"
	}
	if effectiveProcessesJSON != "" {
		setProcessingProcessOverride(streamName, effectiveProcessesJSON)
	}

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
	processExitCh := RegisterProcessExitListener(streamName)
	defer UnregisterProcessExitListener(streamName)

	mistClient := mist.NewClient(h.logger)
	if h.mistServerURL != "" {
		mistClient.BaseURL = h.mistServerURL
	}

	// Clips land in the stream-scoped clips/ namespace Foghorn registered for
	// playback.
	outputDir, outputPath, outErr := h.processingOutputPath(req, true)
	if outErr != nil {
		h.sendResult(send, req.GetJobId(), "failed", outErr.Error(), nil, "", 0)
		return
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		log.WithError(err).Error("Clip: failed to create output directory")
		h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("mkdir failed: %v", err), nil, "", 0)
		return
	}

	ignoredProcessExitBootCounts := map[string]int{}
	streamOutputs, sourceDurationMs, readinessErr := h.waitForProcessingStreamReady(log, mistClient, req, streamName, effectiveProcessesJSON, processExitCh, ignoredProcessExitBootCounts)
	if readinessErr != nil {
		h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
		h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("clip readiness failed: %v", readinessErr), nil, "", 0)
		return
	}
	h.sendProgress(send, req.GetJobId(), 0, 0, sourceDurationMs)

	// Clip span: the requested range when known, else the readiness duration.
	// Used to pick complete (non-truncated) renditions and as the final
	// whole-output coverage gate.
	clipSpanMs := clipRequestedSpanMs(req)
	if clipSpanMs <= 0 {
		clipSpanMs = float64(sourceDurationMs)
	}
	videoSelector := h.processingVideoSelector(log, mistClient, streamName, req.GetProcessesJson(), streamOutputs, clipSpanMs)

	// Unix-seconds start of the current push; RECORDING_END carries no
	// generation id, so a delayed event from a retired push is rejected by
	// comparing its TimeStarted against this.
	currentPushStartedAt := time.Now().Unix()
	_, pushErr := h.startProcessingSelectorPush(log, mistClient, streamName, outputPath, videoSelector)
	if pushErr != nil {
		log.WithError(pushErr).Error("Clip: push_start failed")
		h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
		h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("push_start failed: %v", pushErr), nil, "", 0)
		return
	}

	progressTicker := time.NewTicker(30 * time.Second)
	defer progressTicker.Stop()
	absoluteTimeout := time.After(4 * time.Hour)
	var lastMs int64
	lastAdvance := time.Now()
	var recordingEnd *ProcessingRecordingEndEvent
	const stallTimeout = 3 * time.Minute

	recordingEndIsStale := func(evt ProcessingRecordingEndEvent) bool {
		return recordingEndPredatesPush(evt.TimeStarted, currentPushStartedAt)
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
				}).Error("Clip: push ended with failure")
				h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
				h.sendResult(send, req.GetJobId(), "failed", processingPushFailureMessage(pushEnd), nil, "", 0)
				return
			}
			log.Info("Clip: PUSH_END received")
			// RECORDING_END is the authoritative completion signal; keep waiting
			// for it (the recordingEndCh case breaks the loop). A stall/timeout
			// backstops a successful PUSH_END that is never followed by one.
			continue loop
		case recEnd := <-recordingEndCh:
			if recordingEndIsStale(recEnd) {
				log.WithFields(logging.Fields{
					"time_started":    recEnd.TimeStarted,
					"push_started_at": currentPushStartedAt,
					"file_path":       recEnd.FilePath,
				}).Warn("Clip: ignoring stale RECORDING_END from a retired push")
				continue loop
			}
			recordingEnd = &recEnd
			log.WithFields(logging.Fields{
				"bytes":             recEnd.BytesWritten,
				"media_duration_ms": recEnd.MediaDurationMs,
				"file_path":         recEnd.FilePath,
				"exit_reason":       recEnd.ExitReason,
			}).Info("Clip: RECORDING_END received")
			break loop
		case evt := <-processExitCh:
			evtFields := processExitFields(evt)
			if shouldIgnoreProcessExit(evt, ignoredProcessExitBootCounts) {
				log.WithFields(evtFields).Debug("Clip: ignoring stale process exit from retired generation")
				continue
			}
			switch {
			case evt.Status == "unrecoverable" && isCriticalProcess(evt):
				log.WithFields(evtFields).Error("Clip: critical process unrecoverable")
				h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
				h.sendResult(send, req.GetJobId(), "failed",
					fmt.Sprintf("%s process failed: %s", evt.ProcessType, evt.Reason), nil, "", 0)
				return
			case evt.Status == "unrecoverable":
				log.WithFields(evtFields).Warn("Clip: non-critical process failed, continuing")
			case evt.Status == "retrying":
				log.WithFields(evtFields).Info("Clip: process retrying")
			case evt.Status == "clean":
				log.WithFields(evtFields).Info("Clip: process exited cleanly")
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
				log.WithField("progress_pct", progressPct).Warn("Clip: processing stalled")
				h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
				h.sendResult(send, req.GetJobId(), "failed",
					fmt.Sprintf("clip stalled at %d%%", progressPct), nil, "", 0)
				return
			}
		case <-absoluteTimeout:
			log.Warn("Clip: absolute timeout exceeded")
			h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
			h.sendResult(send, req.GetJobId(), "failed", "absolute timeout exceeded (4h)", nil, "", 0)
			return
		}
	}

	// recordingEnd is guaranteed set here: the loop only breaks from the
	// recordingEndCh case; every other terminal path returns.
	if err := validateProcessingRecordingEnd(*recordingEnd, outputPath); err != nil {
		log.WithError(err).WithFields(logging.Fields{
			"bytes":             recordingEnd.BytesWritten,
			"media_duration_ms": recordingEnd.MediaDurationMs,
			"file_path":         recordingEnd.FilePath,
			"exit_reason":       recordingEnd.ExitReason,
		}).Error("Clip: recording validation failed")
		h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
		h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("recording validation failed: %v", err), nil, "", 0)
		return
	}

	outputSizeBytes, err := waitForProcessingOutput(outputPath, 5*time.Second)
	if err != nil {
		log.WithError(err).Error("Clip: output validation failed")
		h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
		h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("output validation failed: %v", err), nil, "", 0)
		return
	}

	// Whole-output coverage gate. Track selection already chose complete
	// renditions or the source passthrough before push_start; this verifies the
	// selected output actually spans the clip. A source that is itself too short
	// (neither complete renditions nor complete source) fails here.
	if clipSpanMs > 0 &&
		clipSpanMs-float64(recordingEnd.MediaDurationMs) > maxRenditionSpanShortfallMs {
		log.WithFields(logging.Fields{
			"media_duration_ms":  recordingEnd.MediaDurationMs,
			"source_duration_ms": int64(clipSpanMs),
		}).Error("Clip: output materially shorter than source; refusing to publish")
		h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
		h.sendResult(send, req.GetJobId(), "failed",
			fmt.Sprintf("output duration %dms short of source %dms", recordingEnd.MediaDurationMs, int64(clipSpanMs)), nil, "", 0)
		return
	}
	h.sendProgress(send, req.GetJobId(), 100, sourceDurationMs, sourceDurationMs)

	// DTSH generation is fatal for clips: a freshly-created clip must publish
	// with its sidecar so playback never wins the publish-vs-sidecar race. A
	// missing output_runtime_name leaves no runtime name to boot for sidecar
	// generation, so fail closed rather than publish a clip without one.
	vodStreamName := strings.TrimSpace(req.GetOutputRuntimeName())
	if vodStreamName == "" {
		log.Error("Clip: missing output_runtime_name; cannot generate DTSH before publication")
		h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
		h.sendResult(send, req.GetJobId(), "failed", "clip missing output_runtime_name for DTSH generation", nil, "", 0)
		return
	}
	setProcessingSourceOverride(vodStreamName, outputPath)
	dtshErr := GenerateDTSHForPath(h.mistServerURL, vodStreamName, outputPath+".dtsh", log)
	clearProcessingSourceOverride(vodStreamName)
	if dtshErr != nil {
		log.WithError(dtshErr).Error("Clip DTSH generation failed before publication")
		h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
		h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("clip dtsh generation failed: %v", dtshErr), nil, "", 0)
		return
	}

	h.sendResult(send, req.GetJobId(), "completed", "", streamOutputs, outputPath, outputSizeBytes)
	log.Info("Clip processing result sent, artifact registered with Foghorn")

	// Trigger storage check so the .mkv + .dtsh freeze to S3 promptly.
	TriggerStorageCheck()
}

// clipRequestedSpanMs returns the clip's requested duration in ms from the
// source range params, or 0 when unavailable.
func clipRequestedSpanMs(req *ipcpb.ProcessingJobRequest) float64 {
	params := req.GetParams()
	startUnix, startErr := strconv.ParseInt(params["source_start_unix"], 10, 64)
	stopUnix, stopErr := strconv.ParseInt(params["source_stop_unix"], 10, 64)
	if startErr == nil && stopErr == nil && stopUnix > startUnix {
		return float64((stopUnix - startUnix) * 1000)
	}
	return 0
}
