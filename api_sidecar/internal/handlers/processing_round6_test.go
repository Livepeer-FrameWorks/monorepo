package handlers

import (
	"encoding/json"
	"fmt"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRenditionSpanWithNonzeroTimestampOrigin(t *testing.T) {
	for _, includeSource := range []bool{false, true} {
		t.Run(fmt.Sprint(includeSource), func(t *testing.T) {
			rung := processingMetaVideoTrack{codec: "H264", source: "input", width: 640, height: 360, firstms: 90000, lastms: 150000}
			tracks := []processingMetaVideoTrack{rung}
			if includeSource {
				tracks = append(tracks, processingMetaVideoTrack{codec: "H264", width: 1920, height: 1080, firstms: 90000, lastms: 150000})
			}
			if !renditionsCompleteFromTracks(discardLog(), []int{360}, tracks, 60000) {
				t.Fatal("complete nonzero-origin rung refused")
			}
			tracks[0].lastms = 92000
			if renditionsCompleteFromTracks(discardLog(), []int{360}, tracks, 60000) {
				t.Fatal("two-second rung accepted against a sixty-second source with nonzero origin")
			}
		})
	}
}

func TestProcessingDisplayMetadataNormalizesRotation(t *testing.T) {
	for _, rotation := range []int{-90, 450, -270} {
		t.Run(fmt.Sprint(rotation), func(t *testing.T) {
			got := extractActiveStreamMetadata(map[string]interface{}{"health": map[string]interface{}{"video_H264": map[string]interface{}{"codec": "H264", "width": 1920.0, "height": 1080.0, "rotation": float64(rotation)}}})
			if got["width"] != "1080" || got["height"] != "1920" || got["resolution"] != "1080x1920" {
				t.Fatalf("rotation=%d metadata=%v, want portrait", rotation, got)
			}
			source, _ := sourceFromReadinessOutputs(map[string]string{"width": "1080", "height": "1920", "source_rotation": fmt.Sprint(rotation)})
			if source.Width != 1920 || source.Height != 1080 {
				t.Fatalf("rotation=%d coded source=%+v, want 1920x1080", rotation, source)
			}
		})
	}
}

func TestUpstreamEAC3ProcessingMetadata(t *testing.T) {
	got := extractActiveStreamMetadata(map[string]interface{}{"health": map[string]interface{}{"audio_eac3": map[string]interface{}{"codec": "EAC-3", "channels": 6.0, "rate": 48000.0}}})
	if got["audio_codec"] != "EAC3" || !isAudioCodec("EAC-3") {
		t.Fatalf("upstream EAC-3 metadata=%v", got)
	}
}

func TestNeverFitsProcessingMemoryIsNotTransient(t *testing.T) {
	isolateProcessingOverrideState(t)
	const stream, job = "processing+never-fits", "never-fits"
	if err := setProcessingProcessOverride(stream, processingSourceProbePolicy, job, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	total := uint64(1536) * memoryMiB
	h := &ProcessingJobHandler{memoryControls: map[string]bool{"AV": true, "Thumbs": true}, memorySnapshot: func() (uint64, uint64, error) { return total, total, nil }}
	policy := mist.ReplaceLivepeerWithLocal(`[{"process":"Livepeer","target_profiles":[{"height":2160,"bitrate":16000000},{"height":1440,"bitrate":8000000},{"height":1080,"bitrate":5000000},{"height":720,"bitrate":3000000},{"height":480,"bitrate":1200000},{"height":360,"bitrate":800000}]}]`)
	_, err := h.admitProcessingPolicy(discardLog(), &ipcpb.ProcessingJobRequest{JobId: job}, stream, policy, mist.SourceMediaInfo{Width: 3840, Height: 2160}, func(*ipcpb.ControlMessage) {})
	if err == nil || processingMemoryFailureOutputs(err)[mist.ProcessingResultRetryCause] == mist.ProcessingRetryCauseMemoryPressure {
		t.Fatalf("work that cannot fit the largest node was classified as a transient memory wait: %v", err)
	}
	if !strings.Contains(err.Error(), "cannot fit") {
		t.Fatalf("permanent memory refusal lacks a clear reason: %v", err)
	}
}

func TestOldMistUsesConservativeMemoryAdmission(t *testing.T) {
	isolateProcessingOverrideState(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"authorize": map[string]any{"status": "OK"}, "capabilities": map[string]any{"processes": map[string]any{"AV": map[string]any{"optional": map[string]any{}}, "Thumbs": map[string]any{"optional": map[string]any{}}}}})
	}))
	defer server.Close()
	const stream, job = "processing+old-mist", "old-mist"
	if err := setProcessingProcessOverride(stream, processingSourceProbePolicy, job, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	total := uint64(3072) * memoryMiB
	h := &ProcessingJobHandler{logger: logging.NewLogger(), mistServerURL: server.URL, memorySnapshot: func() (uint64, uint64, error) { return total, total, nil }}
	desired := `[{"process":"AV","codec":"H264","resolution":"x2160","bitrate":16000000,"preset":"faster"},{"process":"AV","codec":"H264","resolution":"x360","bitrate":800000,"preset":"faster"}]`
	policy, err := h.admitProcessingPolicy(discardLog(), &ipcpb.ProcessingJobRequest{JobId: job}, stream, desired, mist.SourceMediaInfo{Width: 3840, Height: 2160}, func(*ipcpb.ControlMessage) {})
	if calls.Load() == 0 {
		t.Error("memory admission ignored the Mist capability inventory")
	}
	if err == nil {
		t.Fatalf("old Mist admitted a reduced-thread estimate it cannot enforce: %s", policy)
	}
	total = uint64(128) * 1024 * memoryMiB
	policy, err = h.admitProcessingPolicy(discardLog(), &ipcpb.ProcessingJobRequest{JobId: job}, stream, desired, mist.SourceMediaInfo{Width: 3840, Height: 2160}, func(*ipcpb.ControlMessage) {})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"memory_budgeted", "encode_threads", "decode_threads", "lookahead"} {
		if strings.Contains(policy, key) {
			t.Fatalf("unsupported %s published to old Mist: %s", key, policy)
		}
	}
	modern, err := mist.PlanProcessingMemory(desired, mist.SourceMediaInfo{Width: 3840, Height: 2160}, total, 2)
	if err != nil {
		t.Fatal(err)
	}
	processingProcessOverridesMu.Lock()
	bytes := processingOverrideRecords[stream].MemoryBytes
	processingProcessOverridesMu.Unlock()
	if bytes <= modern.Bytes {
		t.Fatalf("legacy estimate %d is no larger than controlled estimate %d", bytes, modern.Bytes)
	}
}
