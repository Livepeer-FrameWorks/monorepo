package handlers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	"frameworks/api_sidecar/internal/admission"
	"frameworks/api_sidecar/internal/config"
	"frameworks/api_sidecar/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// DVR chapter finalization. Given a range of TS segments on disk
// (with optional S3 recovery URLs for lost-local rows), build a temp
// HLS playlist that Mist's input_hls can ingest, push it to a
// canonical .mkv VOD, and let PUSH_END trip the regular result path.
//
// MistServer's input_hls reads #EXT-X-PROGRAM-DATE-TIME and sets the
// stream's UTCOffset; output_ebml writes that timestamp as DateUTC
// into the .mkv. The chain preserves wall-clock end-to-end so chapter
// playback shows the original recording time without any extra metadata
// hop. PROGRAM-DATE-TIME is rendered from segment.media_start_ms
// (absolute Unix epoch ms) directly — no offset addition.

// handleChapterFinalize runs one chapter finalization job. Mirrors the
// shape of the normal processing job: build a local input, register the
// STREAM_SOURCE override, push the new MKV, wait for PUSH_END. Returns
// nil to indicate the job result has already been sent.
func (h *ProcessingJobHandler) handleChapterFinalize(req *ipcpb.ProcessingJobRequest, send func(*ipcpb.ControlMessage)) {
	log := h.logger.WithFields(logging.Fields{
		"job_id":            req.GetJobId(),
		"chapter_id":        req.GetSourceChapterId(),
		"dvr_hash":          req.GetSourceDvrHash(),
		"playback_artifact": req.GetArtifactHash(),
		"segments":          len(req.GetSourceSegments()),
	})

	if req.GetSourceChapterId() == "" || len(req.GetSourceSegments()) == 0 {
		h.sendResult(send, req.GetJobId(), "failed",
			"invalid chapter finalize request: missing chapter_id or segments",
			map[string]string{"chapter_failure": "source_missing", "chapter_failure_detail": "invalid input"},
			"", 0)
		return
	}

	// The deadline covers EVERY phase of the job — recovery fetches,
	// admission, push, and the result wait. Without this, a chapter
	// with many S3-recovered segments could spend the entire
	// deadline_unix_ms budget in buildChapterHLS (each fetch up to
	// 5 minutes) before the push timer even started.
	deadline := chapterFinalizeDeadline(req)
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()

	streamName := "processing+" + req.GetArtifactHash()
	if HasPendingJob(streamName) {
		log.Warn("Chapter finalize: previous attempt still active, ignoring duplicate")
		return
	}

	// Stage the temp HLS playlist + recovery-fetched segments under
	// {storage}/processing/<hash>.m3u8 so HandleStreamSource picks up
	// the local file path without round-tripping to Foghorn.
	procDir := filepath.Join(h.storagePath, "processing")
	if err := os.MkdirAll(procDir, 0755); err != nil {
		h.sendResult(send, req.GetJobId(), "failed",
			fmt.Sprintf("mkdir processing dir: %v", err), nil, "", 0)
		return
	}
	manifestPath := filepath.Join(procDir, req.GetArtifactHash()+".m3u8")
	recoveryDir := filepath.Join(procDir, "chapter-"+req.GetArtifactHash())
	if err := os.MkdirAll(recoveryDir, 0755); err != nil {
		h.sendResult(send, req.GetJobId(), "failed",
			fmt.Sprintf("mkdir recovery dir: %v", err), nil, "", 0)
		return
	}
	defer func() {
		cleanupProcessingStagePath(log, manifestPath)
		if err := os.RemoveAll(recoveryDir); err != nil && !os.IsNotExist(err) {
			log.WithError(err).Warn("Chapter finalize: cleanup recovery dir failed")
		}
	}()

	segmentCount, hasGaps, mediaStartMs, mediaEndMs, terminalDetail, buildErr := h.buildChapterHLS(ctx, log, req, manifestPath, recoveryDir)
	if buildErr != nil {
		outputs := map[string]string{}
		if terminalDetail != "" {
			outputs["chapter_failure"] = "source_missing"
			outputs["chapter_failure_detail"] = terminalDetail
		}
		h.sendResult(send, req.GetJobId(), "failed", buildErr.Error(), outputs, "", 0)
		return
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

	vodDir := filepath.Join(h.storagePath, "vod")
	if err := os.MkdirAll(vodDir, 0755); err != nil {
		h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("mkdir vod dir: %v", err), nil, "", 0)
		return
	}
	outputPath := filepath.Join(vodDir, req.GetArtifactHash()+".mkv")

	if sm := GetStorageManager(); sm != nil {
		// Estimate the canonical .mkv size from summed source segment
		// durations. Remux carries through the same codecs, so total
		// bytes ≈ sum(segment_size). Without size estimates the
		// admission preflight is a no-op and a near-full disk can
		// admit a multi-GB chapter that ENOSPCs mid-push.
		var estBytes uint64
		for _, s := range req.GetSourceSegments() {
			if sz := s.GetSizeBytes(); sz > 0 {
				estBytes += uint64(sz)
			}
		}
		decision, decErr := sm.Decide(ctx, vodDir, admission.IntentDVRChapterFinalization, estBytes)
		if decErr != nil || decision == admission.CacheReject {
			log.WithError(decErr).Warn("Chapter finalize: admission rejected; chapter will retry")
			h.sendResult(send, req.GetJobId(), "failed", "admission rejected (disk pressure)", nil, "", 0)
			return
		}
	}
	originalProcessesJSON := req.GetProcessesJson()
	effectiveProcessesJSON := mist.StripLivepeerProcesses(originalProcessesJSON)
	if mist.HasLivepeerProcesses(effectiveProcessesJSON) {
		log.Warn("Chapter finalize: ignoring Livepeer process config for DVR finalization")
		effectiveProcessesJSON = "[]"
	}
	// Register the tenant's MistServer process config so the
	// STREAM_PROCESS trigger that fires when the processing+<hash>
	// stream boots picks up side work such as thumbnails. DVR
	// finalization does not start a fresh Livepeer transcode here; the
	// push below selects either already-complete rendition tracks from
	// the recorded input or the source passthrough.
	if effectiveProcessesJSON != "" {
		setProcessingProcessOverride(streamName, effectiveProcessesJSON)
		defer clearProcessingProcessOverride(streamName)
	}

	ignoredProcessExitBootCounts := map[string]int{}

	streamOutputs, _, readinessErr := h.waitForProcessingStreamReady(log, mistClient, req, streamName, effectiveProcessesJSON, processExitCh, ignoredProcessExitBootCounts)
	if readinessErr != nil {
		h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
		h.sendResult(send, req.GetJobId(), "failed",
			fmt.Sprintf("chapter finalize readiness failed: %v", readinessErr), nil, "", 0)
		return
	}

	// Unix-seconds start of the current push attempt. RECORDING_END is keyed only
	// by stream name and carries no push/generation id; re-registering the channel
	// on restart narrows but does not close the window where a late event from the
	// retired push lands in the fresh channel, so it is rejected by comparing its
	// TimeStarted against this.
	chapterSpanMs := float64(0)
	if mediaEndMs > mediaStartMs {
		chapterSpanMs = float64(mediaEndMs - mediaStartMs)
	}
	if chapterSpanMs <= 0 {
		_, chapterSpanMs = sourceFromReadinessOutputs(streamOutputs)
	}
	chapterVideoSelector := h.processingVideoSelector(log, mistClient, streamName, originalProcessesJSON, streamOutputs, chapterSpanMs)

	currentPushStartedAt := time.Now().Unix()
	_, pushErr := h.startProcessingSelectorPush(log, mistClient, streamName, outputPath, chapterVideoSelector)
	if pushErr != nil {
		log.WithError(pushErr).Error("Chapter finalize: push_start failed")
		h.sendResult(send, req.GetJobId(), "failed", fmt.Sprintf("push_start failed: %v", pushErr), nil, "", 0)
		return
	}

	progressTicker := time.NewTicker(30 * time.Second)
	defer progressTicker.Stop()
	var lastMs int64
	lastAdvance := time.Now()
	var recordingEnd *ProcessingRecordingEndEvent
	const stallTimeout = 3 * time.Minute

	recordingEndIsStale := func(evt ProcessingRecordingEndEvent) bool {
		return recordingEndPredatesPush(evt.TimeStarted, currentPushStartedAt)
	}
	terminalSignalsReady := func() (ready bool, terminalFailure bool) {
		return recordingEnd != nil, false
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
				}).Error("Chapter finalize: push ended with failure")
				h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
				h.sendResult(send, req.GetJobId(), "failed", processingPushFailureMessage(pushEnd), nil, "", 0)
				return
			}
			log.Info("Chapter finalize: PUSH_END received")
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
				}).Warn("Chapter finalize: ignoring stale RECORDING_END from a retired push")
				continue loop
			}
			recordingEnd = &recEnd
			log.WithFields(logging.Fields{
				"bytes":             recEnd.BytesWritten,
				"media_duration_ms": recEnd.MediaDurationMs,
				"file_path":         recEnd.FilePath,
				"exit_reason":       recEnd.ExitReason,
			}).Info("Chapter finalize: RECORDING_END received")
			if ready, failed := terminalSignalsReady(); failed {
				return
			} else if !ready {
				continue loop
			}
			break loop
		case evt := <-processExitCh:
			evtFields := processExitFields(evt)
			if shouldIgnoreProcessExit(evt, ignoredProcessExitBootCounts) {
				log.WithFields(evtFields).Debug("Chapter finalize: ignoring stale process exit from retired generation")
				continue
			}
			switch {
			case evt.Status == "unrecoverable" && isCriticalProcess(evt):
				log.WithFields(evtFields).Error("Chapter finalize: critical process unrecoverable")
				h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
				h.sendResult(send, req.GetJobId(), "failed",
					fmt.Sprintf("%s process failed: %s", evt.ProcessType, evt.Reason), nil, "", 0)
				return
			case evt.Status == "unrecoverable":
				log.WithFields(evtFields).Warn("Chapter finalize: non-critical process failed, continuing")
			case evt.Status == "retrying":
				log.WithFields(evtFields).Info("Chapter finalize: process retrying")
			case evt.Status == "clean":
				log.WithFields(evtFields).Info("Chapter finalize: process exited cleanly")
			}
		case <-progressTicker.C:
			currentMs := h.getStreamLastMs(mistClient, streamName)
			if currentMs > lastMs {
				lastMs = currentMs
				lastAdvance = time.Now()
			}
			if time.Since(lastAdvance) >= stallTimeout {
				log.WithField("last_ms", lastMs).Warn("Chapter finalize: push stalled")
				h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
				h.sendResult(send, req.GetJobId(), "failed",
					fmt.Sprintf("chapter finalize stalled at %dms", lastMs), nil, "", 0)
				return
			}
		case <-ctx.Done():
			// ctx covers the entire job (recovery fetches +
			// admission + push). Stalls are caught above; this
			// fires when the whole budget is gone.
			log.Warn("Chapter finalize: deadline exceeded")
			h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
			h.sendResult(send, req.GetJobId(), "failed",
				fmt.Sprintf("chapter finalize timeout (%s)", deadline), nil, "", 0)
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
			}).Error("Chapter finalize: recording validation failed")
			h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
			h.sendResult(send, req.GetJobId(), "failed",
				fmt.Sprintf("recording validation failed: %v", err), nil, "", 0)
			return
		}
	}

	outputSizeBytes, err := waitForProcessingOutput(outputPath, 5*time.Second)
	if err != nil {
		log.WithError(err).Error("Chapter finalize: output validation failed")
		h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
		h.sendResult(send, req.GetJobId(), "failed",
			fmt.Sprintf("output validation failed: %v", err), nil, "", 0)
		return
	}

	// The chapter's authoritative span is its media bounds
	// (mediaEndMs-mediaStartMs). Track selection already picked complete
	// renditions or source passthrough before push_start; this final gate
	// verifies the selected output covers the chapter.
	if recordingEnd != nil && chapterSpanMs > 0 &&
		chapterSpanMs-float64(recordingEnd.MediaDurationMs) > maxRenditionSpanShortfallMs {
		log.WithFields(logging.Fields{
			"media_duration_ms":  recordingEnd.MediaDurationMs,
			"source_duration_ms": int64(chapterSpanMs),
		}).Error("Chapter finalize: output materially shorter than source; refusing to publish")
		h.cleanupFailedProcessing(log, mistClient, streamName, outputPath)
		h.sendResult(send, req.GetJobId(), "failed",
			fmt.Sprintf("output duration %dms short of source %dms", recordingEnd.MediaDurationMs, int64(chapterSpanMs)), nil, "", 0)
		return
	}
	// Merge stream-info outputs (duration_ms, resolution, video_codec,
	// audio_codec, bitrate_kbps, width, height, fps, audio_channels,
	// audio_sample_rate — see VodPipeline.updateVodMetadata) with the
	// chapter-specific keys. Foghorn's dvr_chapter_finalize_hook
	// upserts foghorn.vod_metadata from these.
	outputs := make(map[string]string, len(streamOutputs)+2)
	for k, v := range streamOutputs {
		outputs[k] = v
	}
	outputs["chapter_segment_count"] = strconv.Itoa(int(segmentCount))
	outputs["chapter_has_gaps"] = strconv.FormatBool(hasGaps)
	// MKV span = [first owned segment's media_start, last owned segment's
	// media_end). May differ from the scheduled chapter bounds when
	// chapter boundaries don't align with segment boundaries. Foghorn
	// persists these on the chapter row so the player anchors
	// video.currentTime to wall-clock without drift.
	if mediaStartMs > 0 && mediaEndMs > mediaStartMs {
		outputs["chapter_media_start_ms"] = strconv.FormatInt(mediaStartMs, 10)
		outputs["chapter_media_end_ms"] = strconv.FormatInt(mediaEndMs, 10)
	}
	h.sendResult(send, req.GetJobId(), "completed", "", outputs, outputPath, outputSizeBytes)
	log.Info("Chapter finalize result sent, artifact registered with Foghorn")

	// Proactive DTSH generation, mirrored from the VOD processing path
	// (api_sidecar/internal/handlers/processing.go). Boot vod+<hash> in
	// Mist; the input writes a .dtsh sidecar that the freeze pipeline
	// uploads alongside the .mkv. Without this, dtsh_synced=true never
	// flips on the chapter artifact and the chapter row stays at
	// state='finalized' forever — reclaim never runs.
	//
	// GenerateDTSH already retries for ~30s. If it still fails (Mist
	// busy, transient hiccup), schedule background retries with backoff
	// so finalized chapters reach frozen without waiting for a viewer
	// to happen to boot the asset first.
	vodStreamName := "vod+" + req.GetArtifactHash()
	if err := GenerateDTSHForPath(h.mistServerURL, vodStreamName, outputPath+".dtsh", log); err != nil {
		log.WithError(err).Warn("Chapter finalize: DTSH generation failed, scheduling background retries")
		go h.retryChapterDTSH(vodStreamName, outputPath+".dtsh", log)
	}

	// Trigger storage check so the .mkv + .dtsh freeze to S3 promptly.
	TriggerStorageCheck()
}

// retryChapterDTSH retries DTSH generation for a finalized chapter
// artifact whose inline attempt failed. Without this the chapter stays
// in state='finalized' until first playback regenerates the sidecar,
// which can be never on cold archives. Bounded retries with backoff
// (1m → 5m → 15m → 30m → 60m) cover the realistic transient cases;
// a chapter that fails all attempts ends up needing operator triage.
func (h *ProcessingJobHandler) retryChapterDTSH(vodStreamName, dtshPath string, log *logrus.Entry) {
	backoffs := []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute, 60 * time.Minute}
	for i, wait := range backoffs {
		time.Sleep(wait)
		if err := GenerateDTSHForPath(h.mistServerURL, vodStreamName, dtshPath, log); err == nil {
			log.WithField("attempt", i+2).Info("Chapter finalize: DTSH generation succeeded on retry")
			TriggerStorageCheck()
			return
		}
	}
	log.Warn("Chapter finalize: DTSH generation exhausted retries; chapter remains finalized pending operator triage")
}

// buildChapterHLS writes a VOD HLS manifest covering the chapter's
// source segments. Local files are referenced by absolute path; missing
// local files with a recovery URL are fetched into recoveryDir and
// referenced from there.
//
// Returns (count, hasGaps, terminalDetail, err). terminalDetail is
// non-empty only when the failure is deterministic and not worth
// retrying — no recovery URL for a missing segment, or the request
// has no source segments at all. Transient failures (S3 5xx,
// timeouts, network errors during recovery fetch) leave terminalDetail
// empty so the caller surfaces them as a retryable error rather than
// marking the chapter failed_source_missing.
func (h *ProcessingJobHandler) buildChapterHLS(
	ctx context.Context,
	log *logrus.Entry,
	req *ipcpb.ProcessingJobRequest,
	manifestPath, recoveryDir string,
) (int32, bool, int64, int64, string, error) {
	segs := req.GetSourceSegments()
	if len(segs) == 0 {
		return 0, false, 0, 0, "no source segments", fmt.Errorf("chapter has no source segments")
	}

	// Source segments are owned by exactly one chapter (the one whose
	// range contains the segment's media_start_ms), so the chapter's
	// MKV is the contiguous span [first_seg.start, last_seg.end). Verify
	// that span has no internal gap — a missing ledger row in the middle
	// would otherwise produce a shorter MKV. Chapter-range endpoints
	// (start_ms / end_ms) may differ from the segment span when the
	// recording begins/ends mid-bucket; that's expected, not a fault.
	ordered := make([]*ipcpb.DVRChapterSegmentRef, len(segs))
	copy(ordered, segs)
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].GetMediaStartMs() < ordered[j].GetMediaStartMs()
	})
	covered := ordered[0].GetMediaEndMs()
	for i := 1; i < len(ordered); i++ {
		if ordered[i].GetMediaStartMs() > covered {
			return 0, false, 0, 0,
				fmt.Sprintf("ledger gap between %dms and %dms", covered, ordered[i].GetMediaStartMs()),
				fmt.Errorf("chapter %s has ledger gap", req.GetSourceChapterId())
		}
		if ordered[i].GetMediaEndMs() > covered {
			covered = ordered[i].GetMediaEndMs()
		}
	}
	segs = ordered
	actualMediaStartMs := ordered[0].GetMediaStartMs()
	actualMediaEndMs := covered

	var maxDuration int64
	for _, s := range segs {
		if s.GetDurationMs() > maxDuration {
			maxDuration = s.GetDurationMs()
		}
	}
	targetDuration := (maxDuration + 999) / 1000 // ceil seconds
	if targetDuration < 1 {
		targetDuration = 6
	}

	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:6\n")
	b.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n")
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", targetDuration)
	b.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")

	// Helmsman owns the DVR on-disk layout
	// (storage/dvr/<streamID>/<dvrHash>/segments/<name>). For active
	// DVRs the job's OutputDir is authoritative; for stopped DVRs
	// (chapter finalized after the recording ended) the active map is
	// empty and we fall back to a one-shot scan of storage/dvr/*/ for
	// a directory matching the dvr_hash. The scan is bounded — one
	// entry per stream — and only runs when the active lookup misses.
	storageRoot := config.GetStoragePath()
	resolveDVRDir := func() string {
		if job, ok := control.LookupActiveDVR(req.GetSourceDvrHash()); ok && job != nil {
			return job.OutputDir
		}
		dvrRoot := filepath.Join(storageRoot, "dvr")
		entries, err := os.ReadDir(dvrRoot)
		if err != nil {
			return ""
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			candidate := filepath.Join(dvrRoot, e.Name(), req.GetSourceDvrHash())
			if info, err := os.Stat(candidate); err == nil && info.IsDir() {
				return candidate
			}
		}
		return ""
	}
	dvrDir := resolveDVRDir()
	localResolver := func(segmentName string) string {
		if dvrDir == "" {
			return ""
		}
		return filepath.Join(dvrDir, "segments", segmentName)
	}

	hasGaps := false
	var count int32
	for _, s := range segs {
		path := localResolver(s.GetSegmentName())
		if path != "" {
			if _, err := os.Stat(path); err != nil {
				path = ""
			}
		}
		if path == "" {
			if s.GetPresignedRecoveryUrl() == "" {
				return count, hasGaps, 0, 0,
					fmt.Sprintf("segment %s missing locally and no recovery URL", s.GetSegmentName()),
					fmt.Errorf("source segment %q unavailable", s.GetSegmentName())
			}
			local := filepath.Join(recoveryDir, s.GetSegmentName())
			if err := fetchToFile(ctx, s.GetPresignedRecoveryUrl(), local); err != nil {
				// Recovery fetch failures (S3 5xx, timeout, network) are
				// transient — return an error with an empty
				// terminalDetail so the chapter retries on the next
				// queue tick.
				return count, hasGaps, 0, 0, "",
					fmt.Errorf("recovery fetch for %q: %w", s.GetSegmentName(), err)
			}
			path = local
			// has_gaps is a downstream signal that the output media has
			// missing timeline coverage. Recovery from S3 produces the
			// same bytes the original recording wrote — the resulting
			// MKV is complete, so this is NOT a gap.
			log.WithField("segment_name", s.GetSegmentName()).Info("Chapter finalize: recovered segment from S3")
		}

		// PROGRAM-DATE-TIME emits ISO-8601 of the segment's absolute
		// wall-clock start. Mist's input_hls forwards this to
		// UTCOffset → output_ebml writes DateUTC into the .mkv,
		// preserving the wall-clock through the remux.
		pdt := time.UnixMilli(s.GetMediaStartMs()).UTC().Format(time.RFC3339Nano)
		fmt.Fprintf(&b, "#EXT-X-PROGRAM-DATE-TIME:%s\n", pdt)
		fmt.Fprintf(&b, "#EXTINF:%.3f,\n", float64(s.GetDurationMs())/1000.0)
		fmt.Fprintf(&b, "%s\n", path)
		count++
	}
	b.WriteString("#EXT-X-ENDLIST\n")

	if err := os.WriteFile(manifestPath, []byte(b.String()), 0644); err != nil {
		return count, hasGaps, 0, 0, "", fmt.Errorf("write chapter HLS manifest: %w", err)
	}
	return count, hasGaps, actualMediaStartMs, actualMediaEndMs, "", nil
}

// fetchToFile downloads url to dest atomically (write-and-rename) so
// a failed/partial fetch never leaves a half-written segment that
// later attempts would mistake for a complete file. Bounded by the
// passed-in ctx — chapter finalize's outer ctx has the job-deadline
// timeout, so a stalled S3 GET can't outlive the whole-job budget
// and recovery fetches share the same deadline as the push phase.
func fetchToFile(ctx context.Context, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	tmp := dest + ".partial"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}

func chapterFinalizeDeadline(req *ipcpb.ProcessingJobRequest) time.Duration {
	if dl := req.GetDeadlineUnixMs(); dl > 0 {
		remaining := time.Until(time.UnixMilli(dl))
		if remaining > 0 {
			return remaining
		}
	}
	return 1 * time.Hour
}
