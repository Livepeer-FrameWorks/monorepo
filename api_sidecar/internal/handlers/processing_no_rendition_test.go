package handlers

import (
	"os"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

func TestNoRenditionOutputStillCoversObservedSource(t *testing.T) {
	for _, processes := range []string{"[]", `[{"process":"AV","codec":"AAC"}]`, `[{"process":"AV","codec":"H264","resolution":"0x720","track_inhibit":"video=<0x720"}]`} {
		t.Run(processes, func(t *testing.T) {
			source := mist.SourceMediaInfo{Width: 320, Height: 180}
			// The source was only two seconds long at readiness. Its later observed end is authoritative.
			if renditionsComplete(discardLog(), processes, nil, source, 60000, 0, 2000) {
				t.Fatal("a short/no recording passed without proving whole-output coverage")
			}
			if !renditionsComplete(discardLog(), processes, nil, source, 60000, 0, 60000) {
				t.Fatal("complete passthrough/audio recording was rejected")
			}
			if renditionsComplete(discardLog(), processes, nil, source, 0, 0, 2000) {
				t.Fatal("unknown source end must fail closed")
			}
		})
	}
}

func TestNoRenditionJobRefusesTruncatedRecording(t *testing.T) {
	for _, processes := range []string{"[]", `[{"process":"AV","codec":"AAC"}]`, `[{"process":"AV","codec":"H264","resolution":"0x720","track_inhibit":"video=<0x720"}]`} {
		t.Run(processes, func(t *testing.T) {
			hx := startRecordingGraceJob(t, "vod", "norenditiontruncated", recordingGeometry{
				width: 320, height: 180, sourceEnd: 2000, completedEnd: 60000, processes: processes,
			})
			if err := os.WriteFile(hx.outputPath, []byte("mkv bytes"), 0o644); err != nil {
				t.Fatal(err)
			}
			end := hx.cleanRecordingEnd()
			end.MediaDurationMs = 2000
			end.Tracks[0].lastms = 2000
			SignalProcessingRecordingEnd(end)
			result := hx.awaitResult(t, 30*time.Second)
			if result.GetStatus() != "failed" || result.GetError() != "output renditions incomplete" {
				t.Fatalf("truncated recording published: status=%q error=%q", result.GetStatus(), result.GetError())
			}
		})
	}
}

func TestNoRenditionRecordingUsesSourceSpan(t *testing.T) {
	source := mist.SourceMediaInfo{Width: 320, Height: 180}
	tracks := []processingMetaVideoTrack{{width: 320, height: 180, firstms: 90000, lastms: 150000}}
	if !renditionsComplete(discardLog(), "[]", tracks, source, 150000, 90000, 60000) {
		t.Fatal("complete 60-second recording rejected because source starts at 90 seconds")
	}
	if renditionsComplete(discardLog(), "[]", tracks, source, 150000, 90000, 2000) {
		t.Fatal("truncated recording accepted for nonzero source start")
	}
}
