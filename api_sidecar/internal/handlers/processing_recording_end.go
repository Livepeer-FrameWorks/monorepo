package handlers

import (
	"fmt"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/sirupsen/logrus"
)

// processingRecordingEndGrace bounds how long a processing job waits for the
// RECORDING_END of a push that has already ended. The recording output posts
// RECORDING_END itself as it exits; the controller posts PUSH_END once it reaps
// that exited process on its once-per-second push check. Both are asynchronous
// triggers, so PUSH_END can be handled first, but a RECORDING_END that was sent
// lands within seconds, including a post that needed connection retries. An
// output that crashed or was killed never sends one, and waiting for the
// 3-minute stall timeout would only delay its retry.
var processingRecordingEndGrace = 20 * time.Second

// recordingEndWait holds a push that ended before its recording reported its
// end, and the grace timer for that report.
type recordingEndWait struct {
	pushEnd *ProcessingPushEndEvent
	timer   *time.Timer
}

// start begins the grace window for a push that ended without a RECORDING_END
// for the current push.
func (w *recordingEndWait) start(log *logrus.Entry, evt ProcessingPushEndEvent) {
	w.stop()
	w.pushEnd = &evt
	w.timer = time.NewTimer(processingRecordingEndGrace)
	fields := logging.Fields{
		"push_id":     evt.PushID,
		"push_status": evt.PushStatus,
		"push_logs":   evt.LogMessages,
		"grace":       processingRecordingEndGrace.String(),
	}
	if processingPushSucceeded(evt) {
		log.WithFields(fields).Info("Processing PUSH_END received before RECORDING_END; waiting for the recording's end")
		return
	}
	log.WithFields(fields).Warn("Processing push ended with a failure status before RECORDING_END; waiting for the recording's end")
}

// stop discards the window; a restarted push starts without one.
func (w *recordingEndWait) stop() {
	if w.timer != nil {
		w.timer.Stop()
	}
	w.timer = nil
	w.pushEnd = nil
}

// active reports whether the push has ended and the job is waiting only for
// its RECORDING_END. Stall detection does not apply then: the push has nothing
// left to advance.
func (w *recordingEndWait) active() bool { return w.pushEnd != nil }

// expired fires when the grace window passes; nil (never ready) without one.
func (w *recordingEndWait) expired() <-chan time.Time {
	if w.timer == nil {
		return nil
	}
	return w.timer.C
}

// failedPush returns the ended push when it ended with a failure status.
func (w *recordingEndWait) failedPush() (ProcessingPushEndEvent, bool) {
	if w.pushEnd == nil || processingPushSucceeded(*w.pushEnd) {
		return ProcessingPushEndEvent{}, false
	}
	return *w.pushEnd, true
}

// recordingOutputVanishedMessage is the ProcessingJobResult error for a push
// whose recording output ended without reporting its end.
func recordingOutputVanishedMessage(evt ProcessingPushEndEvent) string {
	status := strings.TrimSpace(evt.PushStatus)
	if status == "" {
		status = "unknown"
	}
	return fmt.Sprintf("recording output ended without reporting its end (push status: %s)", status)
}

// reportRecordingOutputVanished reports a push that ended with no RECORDING_END
// for it within the grace window. Mist's output sends RECORDING_END on every
// exit it controls, error exits included, so its absence means the output
// process was killed or crashed: nothing about the job's input or settings
// failed, and a fresh attempt can succeed. The result waits for the processing
// stream to stop, since a retry on this node reuses its name.
func (h *ProcessingJobHandler) reportRecordingOutputVanished(log *logrus.Entry, mistClient *mist.Client, send func(*ipcpb.ControlMessage), jobID, streamName, outputPath string, evt ProcessingPushEndEvent) {
	log.WithFields(logging.Fields{
		"push_id":       evt.PushID,
		"push_status":   evt.PushStatus,
		"target_before": evt.TargetBefore,
		"target_after":  evt.TargetAfter,
		"push_logs":     evt.LogMessages,
		"grace":         processingRecordingEndGrace.String(),
	}).Error("Processing recording output ended without RECORDING_END; reporting the attempt retryable")
	h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
	if !waitProcessingStreamStopped(mistClient, streamName, processingStreamStopTimeout) {
		log.Warn("Processing stream still active after cleanup; the retry may attach to it")
	}
	h.sendResult(send, jobID, processingResultRetryable, recordingOutputVanishedMessage(evt), nil, "", 0)
}

// reportFailedPushWithRecording reports a push that ended with a failure status
// and whose recording did report its end: the push failure is the attempt's
// error, and the recording's exit reason decides retryability as it does for a
// recording that fails validation.
func (h *ProcessingJobHandler) reportFailedPushWithRecording(log *logrus.Entry, mistClient *mist.Client, send func(*ipcpb.ControlMessage), jobID, streamName, outputPath string, evt ProcessingPushEndEvent, recEnd ProcessingRecordingEndEvent) {
	log.WithFields(logging.Fields{
		"push_id":       evt.PushID,
		"push_status":   evt.PushStatus,
		"target_before": evt.TargetBefore,
		"target_after":  evt.TargetAfter,
		"push_logs":     evt.LogMessages,
		"exit_reason":   recEnd.ExitReason,
	}).Error("Processing push ended with failure")
	h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
	status := failedRecordingStatus(log, mistClient, streamName, recEnd)
	h.sendResult(send, jobID, status, processingPushFailureMessage(evt), nil, "", 0)
}
