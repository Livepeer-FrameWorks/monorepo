package handlers

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

func TestProcessingTrackSelectorUsesIndexWhenProducerIDsCollide(t *testing.T) {
	for _, index := range []int{0, 1, 4, 5} {
		track := processingMetaVideoTrack{
			trackID: 1, hasTrackID: true,
			trackIndex: index, hasTrackIndex: true,
		}
		want := map[int]string{0: "0", 1: "1", 4: "4", 5: "5"}[index]
		if got := track.selector(); got != want {
			t.Errorf("track index %d sharing producer ID 1: selector = %q, want %q", index, got, want)
		}
	}
}

func TestProcessingRecordingExcludesOnlyTheReplacedOriginalIndex(t *testing.T) {
	originals := []processingMetaVideoTrack{
		originalTrack(0, 1920, 1080, bps1080InBand, 12000),
		originalTrack(1, 1280, 720, bps15Mbps, 12000),
		originalTrack(2, 640, 360, bps360InBand, 12000),
	}
	for i := range originals {
		originals[i].trackIndex, originals[i].hasTrackIndex = i, true
	}
	// ProcAV assigns ID 1 independently to each generated video track. An ID
	// exclusion for the replaced original would also exclude both future rungs.
	if got := processingRecordingVideoSelector(quietLog(), retentionLadder(t), originals,
		mist.SourceMediaInfo{Width: 1280, Height: 720}); got != "all,!1" {
		t.Fatalf("recording selector = %q, want all,!1, preserving generated tracks with ID 1", got)
	}
}

func TestProcessingRecordingWithoutIndexUsesProcessMasks(t *testing.T) {
	originals := []processingMetaVideoTrack{originalTrack(1, 1280, 720, bps15Mbps, 12000)}
	if got := processingRecordingVideoSelector(quietLog(), retentionLadder(t), originals,
		mist.SourceMediaInfo{Width: 1280, Height: 720}); got != "all" {
		t.Fatalf("recording selector without an index = %q, want all with native visibility masks", got)
	}
}
