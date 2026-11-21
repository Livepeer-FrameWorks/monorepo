package handlers

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
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
			tracks[0].firstms = 148000
			tracks[0].lastms = 150000
			if renditionsCompleteFromTracks(discardLog(), []int{360}, tracks, 60000) {
				t.Fatal("rendition missing its first 58 seconds was accepted")
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

func TestNodeCapacityRefusalCarriesPlacementRequirement(t *testing.T) {
	isolateProcessingOverrideState(t)
	const stream, job = "processing+never-fits", "never-fits"
	if err := setProcessingProcessOverride(stream, processingSourceProbePolicy, job, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	total := uint64(1536) * memoryMiB
	h := &ProcessingJobHandler{memorySnapshot: func() (uint64, uint64, error) { return total, total, nil }}
	policy := mist.ReplaceLivepeerWithLocal(`[{"process":"Livepeer","target_profiles":[{"height":2160,"bitrate":16000000},{"height":1440,"bitrate":8000000},{"height":1080,"bitrate":5000000},{"height":720,"bitrate":3000000},{"height":480,"bitrate":1200000},{"height":360,"bitrate":800000}]}]`)
	_, err := h.admitProcessingPolicy(discardLog(), &ipcpb.ProcessingJobRequest{JobId: job}, stream, policy, mist.SourceMediaInfo{Width: 3840, Height: 2160}, func(*ipcpb.ControlMessage) {})
	if err == nil {
		t.Fatal("oversized local work was admitted")
	}
	outputs := processingMemoryFailureOutputs(err)
	if outputs[mist.ProcessingResultRetryCause] != mist.ProcessingRetryCauseMemoryNodeTooSmall ||
		outputs[mist.ProcessingRequiredMemoryBytes] == "" || outputs[mist.ProcessingMemoryPolicy] != policy {
		t.Fatalf("node refusal lost its placement requirement: %v, %v", err, outputs)
	}
	if processingMemoryFailureStatus(err) != processingResultRetryable {
		t.Fatalf("a node refusal must let Foghorn consider the other cluster nodes: %v", err)
	}
	if !strings.Contains(err.Error(), "memory budget") {
		t.Fatalf("memory refusal lacks a clear reason: %v", err)
	}
}
