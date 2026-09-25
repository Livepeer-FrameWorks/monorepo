package handlers

import (
	"fmt"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// processingResultRetryable is the ProcessingJobResult status for an attempt
// that failed in a way a fresh attempt can succeed at. Foghorn requeues the
// job within its retry budget and fails it once the budget is spent.
const processingResultRetryable = "retryable"

// recordingEndRetryable reports whether a RECORDING_END exit reason names a
// failure that a new attempt avoids: a producer replaced after the recording
// header froze its track list (a Livepeer to local fallback mid-job) leaves
// this recording unfinishable, while a new attempt declares the new tracks
// from the start.
func recordingEndRetryable(evt ProcessingRecordingEndEvent) bool {
	return strings.TrimSpace(evt.ExitReason) == mist.ExitReasonProcessTracksChanged
}

// renditionStartToleranceMs is how far a rendition may start after its
// source: one key interval of the source, with this as the floor when the
// source reports none.
const renditionStartToleranceMs = 2000

// renditionCoverageError checks every derived video rendition in the
// recorded track set against the source video it was made from, by the span
// the recording wrote on each track. A rendition that starts later than one
// source key interval after the source, or ends more than the rendition
// shortfall allowance before it, is missing media: publishing it as READY
// would serve a ladder whose rungs disagree about what the video holds.
// Image (JPEG) tracks are thumbnails, not renditions. Tracks without a written
// span (a Mist that does not report one) are not judged.
func renditionCoverageError(tracks []*ipcpb.StreamTrack) error {
	written := func(t *ipcpb.StreamTrack) bool { return t.WrittenFirstMs != nil && t.WrittenLastMs != nil }
	var source *ipcpb.StreamTrack
	for _, t := range tracks {
		if t.GetTrackType() != "video" || t.GetSourceTrack() != "" || strings.EqualFold(t.GetCodec(), "JPEG") || !written(t) {
			continue
		}
		if source == nil || t.GetWrittenLastMs()-t.GetWrittenFirstMs() > source.GetWrittenLastMs()-source.GetWrittenFirstMs() {
			source = t
		}
	}
	if source == nil {
		return nil
	}
	startTolerance := int64(renditionStartToleranceMs)
	if k := int64(source.GetKeyframeMsMax()); k > startTolerance {
		startTolerance = k
	}
	for _, t := range tracks {
		if t.GetTrackType() != "video" || t.GetSourceTrack() == "" || strings.EqualFold(t.GetCodec(), "JPEG") || !written(t) {
			continue
		}
		if late := t.GetWrittenFirstMs() - source.GetWrittenFirstMs(); late > startTolerance {
			return fmt.Errorf("rendition %s starts %dms after the source (tolerance %dms)", t.GetTrackName(), late, startTolerance)
		}
		if early := source.GetWrittenLastMs() - t.GetWrittenLastMs(); early > maxRenditionSpanShortfallMs {
			return fmt.Errorf("rendition %s ends %dms before the source", t.GetTrackName(), early)
		}
	}
	return nil
}

// recordingDurationError rejects a recording shorter than the source by more
// than the rendition shortfall allowance. sourceDurationMs comes from the
// readiness probe, which can understate a source but never overstates it, so
// it only ever lets a short recording through, never rejects a full one.
// Unknown durations (zero) are not judged.
func recordingDurationError(mediaDurationMs, sourceDurationMs int64) error {
	if mediaDurationMs <= 0 || sourceDurationMs <= 0 {
		return nil
	}
	if short := sourceDurationMs - mediaDurationMs; short > maxRenditionSpanShortfallMs {
		return fmt.Errorf("recording holds %dms of a %dms source", mediaDurationMs, sourceDurationMs)
	}
	return nil
}

// processingStreamStopTimeout bounds how long a retryable failure waits for
// its processing stream to shut down before reporting. A retry dispatched to
// this node reuses the stream name; reported early, it attaches to the old
// buffer while that buffer is still stopping and is killed with it.
const processingStreamStopTimeout = 30 * time.Second

// activeStreamsContain reports whether an active_streams response still lists
// the stream.
func activeStreamsContain(resp map[string]interface{}, streamName string) bool {
	switch streams := resp["active_streams"].(type) {
	case map[string]interface{}:
		_, ok := streams[streamName]
		return ok
	case []interface{}:
		for _, s := range streams {
			if name, ok := s.(string); ok && name == streamName {
				return true
			}
		}
	}
	return false
}

// waitProcessingStreamStopped polls Mist until the stream is no longer active
// or the timeout passes, and reports whether it stopped.
func waitProcessingStreamStopped(mistClient *mist.Client, streamName string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		resp, err := mistClient.GetActiveStreamsFiltered([]string{streamName})
		if err == nil && !activeStreamsContain(resp, streamName) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(500 * time.Millisecond)
	}
}
