package handlers

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"frameworks/api_sidecar/internal/appconfig"
	"frameworks/api_sidecar/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// thumbnailUpdatedAt holds, per stream, when Mist last announced new thumbnail files (THUMBNAIL_UPDATED).
var thumbnailUpdatedAt sync.Map

func noteThumbnailUpdated(streamName string, at time.Time) {
	thumbnailUpdatedAt.Store(streamName, at)
}

func lastThumbnailUpdate(streamName string) time.Time {
	if v, ok := thumbnailUpdatedAt.Load(streamName); ok {
		if at, ok := v.(time.Time); ok {
			return at
		}
	}
	return time.Time{}
}

const (
	// The last announced thumbnail must stay unchanged this long before the job settles on its upload.
	thumbnailSettleQuiet = 5 * time.Second
	// Covers one announced batch's upload deadline (thumbnailUploadFileDeadline in control) plus the mint exchange.
	thumbnailSettleTimeout = 4 * time.Minute
	thumbnailSettlePoll    = time.Second
)

// thumbnailUploadSettled reports whether the thumbnails Mist last announced during this job were uploaded and
// confirmed: an announcement after the job started, a confirmation after it, and no newer announcement within the
// quiet window. Foghorn mints a thumbnail upload for a processing stream only while its job is active, so the job
// must not complete before this holds.
func thumbnailUploadSettled(jobStart, lastUpdate, lastConfirmed, now time.Time) bool {
	return lastUpdate.After(jobStart) && lastConfirmed.After(lastUpdate) && now.Sub(lastUpdate) >= thumbnailSettleQuiet
}

// handleThumbnail regenerates the thumbnails of a published clip or VOD from its stored media. The asset's media
// and state stay untouched: the source is staged to a scratch file, the recording that drives the processing
// stream to its end is a scratch file, and both are removed. Only the Thumbs processes of the job's config run.
// The job completes once the final thumbnails were uploaded, which publishes them through the normal thumbnail
// path.
func (h *ProcessingJobHandler) handleThumbnail(req *ipcpb.ProcessingJobRequest, send func(*ipcpb.ControlMessage)) { //nolint:funlen // One job lifecycle, kept in order.
	log := h.logger.WithFields(logging.Fields{
		"job_id":        req.GetJobId(),
		"job_type":      req.GetJobType(),
		"artifact_hash": req.GetArtifactHash(),
	})
	log.Info("Thumbnail processing job received")
	jobStart := time.Now()
	streamName := "processing+" + req.GetArtifactHash()

	doneCh, existingJobID, claimed := claimPendingJob(streamName, req.GetJobId())
	if !claimed {
		log.WithField("active_job_id", existingJobID).Warn("Thumbnail: previous attempt still active, ignoring duplicate dispatch")
		return
	}
	reporter := newProcessingReporter(send, req.GetJobId())
	send = reporter.Send
	stopLease := reporter.StartLease(time.Minute)
	defer stopLease()
	defer releasePendingJob(streamName, req.GetJobId())
	defer clearProcessingProcessOverride(streamName, req.GetJobId())

	processesJSON := mist.ThumbnailProcesses(req.GetProcessesJson())
	if processesJSON == "[]" {
		h.sendResult(send, req.GetJobId(), "failed", "thumbnail job carries no Thumbs process", nil, "", 0)
		return
	}
	if err := setProcessingProcessOverride(streamName, processesJSON, req.GetJobId(), processingOverrideExpiry(req)); err != nil {
		h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("persist thumbnail processing policy: %v", err), nil, "", 0)
		return
	}

	sourceURL := strings.TrimSpace(req.GetSourceUrl())
	if sourceURL == "" {
		h.sendResult(send, req.GetJobId(), "failed", "thumbnail job has no source URL", nil, "", 0)
		return
	}
	stagedSourcePath, stageErr := h.stageProcessingSource(log, req, sourceURL)
	if stageErr != nil {
		h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("thumbnail source stage failed: %v", stageErr), nil, "", 0)
		return
	}
	defer cleanupProcessingStagePath(log, stagedSourcePath)
	setProcessingSourceOverride(streamName, stagedSourcePath)
	defer clearProcessingSourceOverride(streamName)

	recordingEndCh := registerProcessingRecordingEndListener(streamName)
	defer unregisterProcessingRecordingEndListener(streamName)
	processExitCh := RegisterProcessExitListener(streamName)
	defer UnregisterProcessExitListener(streamName)

	mistClient := mist.NewClient(h.logger, appconfig.MistClient())
	if h.mistServerURL != "" {
		mistClient.BaseURL = h.mistServerURL
	}

	// The recording only drives the stream through the whole source; it is not the asset's media.
	outputDir := filepath.Join(h.storagePath, "processing")
	outputPath := filepath.Join(outputDir, req.GetArtifactHash()+".thumbnails.mkv")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("mkdir failed: %v", err), nil, "", 0)
		return
	}
	defer cleanupProcessingStagePath(log, outputPath)

	ignoredProcessExitBootCounts := map[string]int{}
	_, sourceDurationMs, readinessErr := h.waitForProcessingStreamReady(context.Background(), log, mistClient, req, streamName, processesJSON, processExitCh, nil, nil, ignoredProcessExitBootCounts)
	if readinessErr != nil {
		h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
		h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("thumbnail readiness failed: %v", readinessErr), nil, "", 0)
		return
	}
	h.sendProgress(send, req.GetJobId(), 0, 0, sourceDurationMs)

	currentPushStartedAt := time.Now().Unix()
	pushID, pushErr := h.startProcessingPush(log, mistClient, req, outputDir, streamName, outputPath)
	if pushErr != nil {
		h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
		h.sendResult(send, req.GetJobId(), "failed", pushErr.Error(), nil, "", 0)
		return
	}
	pushTargetURI := processingMuxTargetURI(outputPath)

	progressTicker := time.NewTicker(5 * time.Second)
	defer progressTicker.Stop()
	absoluteTimeout := time.After(4 * time.Hour)
	stallWatch := newRecorderStallWatch(time.Now())
	var recordingEnd *ProcessingRecordingEndEvent
	var awaitRecordingEnd recordingEndWait
	defer awaitRecordingEnd.stop()

loop:
	for {
		select {
		case pushEnd := <-doneCh:
			awaitRecordingEnd.start(log, pushEnd)
		case <-awaitRecordingEnd.expired():
			h.reportRecordingOutputVanished(log, mistClient, send, req.GetJobId(), streamName, outputPath, *awaitRecordingEnd.pushEnd)
			return
		case recEnd := <-recordingEndCh:
			if recordingEndPredatesPush(recEnd.TimeStarted, currentPushStartedAt) {
				continue loop
			}
			if pushEnd, failed := awaitRecordingEnd.failedPush(); failed {
				h.reportFailedPushWithRecording(log, mistClient, send, req.GetJobId(), streamName, outputPath, pushEnd, recEnd)
				return
			}
			recordingEnd = &recEnd
			break loop
		case evt := <-processExitCh:
			if shouldIgnoreProcessExit(evt, ignoredProcessExitBootCounts) {
				continue
			}
			if evt.Status == "unrecoverable" && evt.ProcessType == "Thumbs" {
				log.WithFields(processExitFields(evt)).Error("Thumbnail: Thumbs process failed")
				h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
				h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("Thumbs process failed: %s", evt.Reason), nil, "", 0)
				return
			}
		case <-progressTicker.C:
			recorder, stalledFor := stallWatch.observe(time.Now(), sampleProcessingRecorderProgress(mistClient, streamName, pushTargetURI, pushID, outputPath))
			var progressPct int32
			if sourceDurationMs > 0 && recorder.mediaMs > 0 {
				progressPct = int32(min(recorder.mediaMs*100/sourceDurationMs, 100))
			}
			h.sendProgress(send, req.GetJobId(), progressPct, recorder.mediaMs, sourceDurationMs)
			if !awaitRecordingEnd.active() && stalledFor >= processingRecorderStallWindow {
				log.WithFields(recorderProgressFields(recorder)).Warn("Thumbnail: recording stalled")
				h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
				h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("thumbnail run stalled at %d%%", progressPct), nil, "", 0)
				return
			}
		case <-absoluteTimeout:
			h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
			h.sendResult(send, req.GetJobId(), "failed", "absolute timeout exceeded (4h)", nil, "", 0)
			return
		}
	}

	// Thumbs samples keyframes over the media it saw; a run that stopped short covered only part of the asset.
	if sourceDurationMs > 0 && float64(sourceDurationMs-recordingEnd.MediaDurationMs) > maxRenditionSpanShortfallMs {
		h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
		h.sendResult(send, req.GetJobId(), "failed",
			fmt.Sprintf("thumbnail run covered %dms of %dms", recordingEnd.MediaDurationMs, sourceDurationMs), nil, "", 0)
		return
	}
	h.sendProgress(send, req.GetJobId(), 100, sourceDurationMs, sourceDurationMs)

	deadline := time.Now().Add(thumbnailSettleTimeout)
	for !thumbnailUploadSettled(jobStart, lastThumbnailUpdate(streamName), control.LastThumbnailUploadConfirmed(req.GetArtifactHash()), time.Now()) {
		if time.Now().After(deadline) {
			log.WithFields(logging.Fields{
				"last_update":    lastThumbnailUpdate(streamName),
				"last_confirmed": control.LastThumbnailUploadConfirmed(req.GetArtifactHash()),
			}).Warn("Thumbnail: final thumbnails were not uploaded")
			h.sendResult(send, req.GetJobId(), "failed", "thumbnail run ended without a confirmed thumbnail upload", nil, "", 0)
			return
		}
		time.Sleep(thumbnailSettlePoll)
	}
	log.WithField("media_duration_ms", recordingEnd.MediaDurationMs).Info("Thumbnail processing completed; thumbnails uploaded")
	h.sendResult(send, req.GetJobId(), "completed", "", map[string]string{"thumbnails": "uploaded"}, "", 0)
}
