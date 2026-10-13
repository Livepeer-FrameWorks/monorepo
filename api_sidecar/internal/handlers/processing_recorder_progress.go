package handlers

import (
	"os"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

// processingRecorderStallWindow is how long a processing recording may go
// without writing before its attempt fails as stalled.
const processingRecorderStallWindow = 3 * time.Minute

// processingRecorderProgress is one observation of a processing push's
// recording output: the position and bytes Mist's push status reports, and
// the output file's size on disk. pushSeen and fileSeen say which of the two
// this sample observed.
type processingRecorderProgress struct {
	mediaMs   int64
	bytes     uint64
	fileBytes int64
	pushSeen  bool
	fileSeen  bool
}

// sampleProcessingRecorderProgress measures the recording, not the processing
// stream's source: the source's lastms reaches the end while the buffer holds
// its feed for a recorder that is still writing. The push is matched by id,
// else by target.
func sampleProcessingRecorderProgress(client processingPushLister, streamName, targetURI string, pushID int, outputPath string) processingRecorderProgress {
	var progress processingRecorderProgress
	if pushes, err := client.PushList(); err == nil {
		for _, p := range pushes {
			if p.StreamName != streamName {
				continue
			}
			if (pushID > 0 && p.ID == pushID) || (pushID <= 0 && (p.TargetURI == targetURI || p.ActualURI == targetURI)) {
				status := mist.ParsePushProgress(p.Status)
				progress.mediaMs, progress.bytes, progress.pushSeen = status.MediaMs, status.Bytes, true
				break
			}
		}
	}
	if info, err := os.Stat(outputPath); err == nil {
		progress.fileBytes, progress.fileSeen = info.Size(), true
	}
	return progress
}

func recorderProgressFields(p processingRecorderProgress) logging.Fields {
	return logging.Fields{
		"recorder_media_ms": p.mediaMs,
		"recorder_bytes":    p.bytes,
		"output_bytes":      p.fileBytes,
	}
}

// recorderStallWatch tracks when a processing recording last advanced.
type recorderStallWatch struct {
	last        processingRecorderProgress
	lastAdvance time.Time
}

func newRecorderStallWatch(now time.Time) recorderStallWatch {
	return recorderStallWatch{lastAdvance: now}
}

// observe records a sample and returns the recording's latest known progress
// and how long it has gone without advancing. A part the sample could not
// observe (a failed push_list, the push not listed yet, no file yet) keeps its
// last known value, so a missed read neither counts as an advance on the next
// one nor resets the reported position.
func (w *recorderStallWatch) observe(now time.Time, p processingRecorderProgress) (processingRecorderProgress, time.Duration) {
	advanced := false
	if p.pushSeen {
		advanced = p.mediaMs > w.last.mediaMs || p.bytes > w.last.bytes
		w.last.mediaMs, w.last.bytes = p.mediaMs, p.bytes
	}
	if p.fileSeen {
		advanced = advanced || p.fileBytes > w.last.fileBytes
		w.last.fileBytes = p.fileBytes
	}
	if advanced {
		w.lastAdvance = now
	}
	return w.last, now.Sub(w.lastAdvance)
}
