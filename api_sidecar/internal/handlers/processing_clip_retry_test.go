package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/sirupsen/logrus"
)

// A clip recording that Mist ends with PROCESS_TRACKS_CHANGED (a thumbnail track
// that appeared after the recording header) is reported retryable, so Foghorn
// requeues the clip within its retry budget instead of failing it, and the
// report waits for the nuked processing stream to stop.
func TestClipRecordingWithChangedProcessTracksIsRetryable(t *testing.T) {
	isolateProcessingOverrideState(t)
	const artifactHash = "clipretryhash"
	const streamName = "processing+" + artifactHash
	var pushStarted, nuked atomic.Bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/source.mkv":
			_, _ = w.Write([]byte("cut source bytes"))
			return
		case strings.HasPrefix(r.URL.Path, "/json_"):
			_, _ = w.Write([]byte("{}"))
			return
		}
		body, _ := io.ReadAll(r.Body)
		request := string(body) + r.URL.RawQuery
		if strings.Contains(request, "push_start") {
			pushStarted.Store(true)
		}
		if strings.Contains(request, "nuke_stream") {
			nuked.Store(true)
		}
		active := map[string]any{}
		if !nuked.Load() {
			active[streamName] = map[string]any{
				"health": map[string]any{
					"video_H264_640x360_15fps_0": map[string]any{"codec": "H264", "width": 640.0, "height": 360.0},
					"audio_AAC_1ch_48000hz_1":    map[string]any{"codec": "AAC", "channels": 1.0, "rate": 48000.0},
				},
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"authorize": map[string]any{"status": "OK"}, "active_streams": active})
	}))
	t.Cleanup(srv.Close)

	storage := t.TempDir()
	h := NewProcessingJobHandler(logrus.New(), srv.URL, storage, t.TempDir())
	req := &ipcpb.ProcessingJobRequest{
		JobId:             "clip-retry-job",
		JobType:           "clip",
		ArtifactHash:      artifactHash,
		SourceUrl:         srv.URL + "/source.mkv",
		ProcessesJson:     "[]",
		OutputRuntimeName: "vod+" + artifactHash,
		Params:            map[string]string{"output_stream_name": "clipstream"},
	}
	results := make(chan *ipcpb.ProcessingJobResult, 1)
	go h.handleClip(req, func(msg *ipcpb.ControlMessage) {
		if result := msg.GetProcessingJobResult(); result != nil {
			results <- result
		}
	})

	deadline := time.Now().Add(20 * time.Second)
	for !pushStarted.Load() {
		if time.Now().After(deadline) {
			t.Fatal("the clip never started its recording push")
		}
		time.Sleep(20 * time.Millisecond)
	}
	SignalProcessingRecordingEnd(ProcessingRecordingEndEvent{
		StreamName:      streamName,
		FilePath:        filepath.Join(storage, "clips", "clipstream", artifactHash+".mkv"),
		BytesWritten:    993988,
		MediaDurationMs: 9867,
		TimeStarted:     time.Now().Unix(),
		ExitReason:      mist.ExitReasonProcessTracksChanged,
		HumanExitReason: "EBML track 6 (JPEG) was not declared in the recording header; declared: 0 1 3 4",
	})

	select {
	case result := <-results:
		if result.GetStatus() != processingResultRetryable {
			t.Fatalf("clip result status = %q (%s), want %q", result.GetStatus(), result.GetError(), processingResultRetryable)
		}
		if !nuked.Load() {
			t.Fatal("the retryable result was reported before the processing stream was stopped")
		}
	case <-time.After(60 * time.Second):
		t.Fatal("the clip reported no result")
	}
}
