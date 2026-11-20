package handlers

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	"github.com/sirupsen/logrus"
)

func TestRenditionCheckAcceptsPortraitLadder(t *testing.T) {
	log := logrus.NewEntry(logrus.New())
	processes := `[{"process":"Livepeer","target_profiles":[{"name":"360p","height":360},{"name":"480p","height":480},{"name":"720p","height":720}]}]`
	for _, height := range []int{1280, 1920} {
		source := mist.SourceMediaInfo{Width: height * 9 / 16, Height: height}
		tracks := []processingMetaVideoTrack{
			renditionTrack(2, 360, 640, 30000),
			renditionTrack(3, 480, 854, 30000),
			renditionTrack(4, 720, 1280, 30000),
		}
		if !renditionsComplete(log, processes, tracks, source, 30000, 0, 30000) {
			t.Errorf("%dx%d: complete portrait ladder refused", source.Width, source.Height)
		}
		if renditionsComplete(log, processes, tracks[1:], source, 30000, 0, 30000) {
			t.Error("portrait ladder missing 360p was accepted")
		}
	}
}

func TestChooseProcessingVideoSelectorSelectsPortraitLadder(t *testing.T) {
	log := logrus.NewEntry(logrus.New())
	processes := `[{"process":"Livepeer","target_profiles":[{"height":360},{"height":720}]}]`
	tracks := []processingMetaVideoTrack{
		chapterTrack(1, 720, 1280, 30000),
		renditionTrack(2, 360, 640, 30000),
		renditionTrack(3, 720, 1280, 30000),
	}
	if got := chooseProcessingVideoSelector(log, processes, tracks, mist.SourceMediaInfo{Width: 720, Height: 1280}, 30000, nil); got != "i3,i2" {
		t.Fatalf("selector = %q, want i3,i2", got)
	}
}

func TestExtractActiveStreamMetadataUsesDisplaySize(t *testing.T) {
	got := extractActiveStreamMetadata(map[string]interface{}{
		"health": map[string]interface{}{
			"video_HEVC_1920x1080_0": map[string]interface{}{
				"codec": "HEVC", "width": 1920.0, "height": 1080.0, "rotation": 90.0,
			},
		},
	})
	if got["width"] != "1080" || got["height"] != "1920" || got["resolution"] != "1080x1920" || got["source_rotation"] != "90" {
		t.Fatalf("metadata = %v, want portrait display size and source_rotation=90", got)
	}
	source, _ := sourceFromReadinessOutputs(got)
	w, h := source.DisplaySize()
	if w != 1080 || h != 1920 {
		t.Fatalf("restored source displays as %dx%d, want 1080x1920", w, h)
	}
}

func TestIncompatibleSourceRequiresAAC(t *testing.T) {
	requirements := expectedProcessingTracks(`[{"process":"AV","codec":"AAC"}]`)
	p := processingTrackPresence{sourceMedia: true, outputs: map[string]string{"audio_codec": "AC3"}, audioCodecs: map[string]bool{"AC3": true}}
	if processingRequiredTracksReady(p, requirements) {
		t.Fatal("AC3 source accepted without required browser AAC audio")
	}
	p.audioCodecs["AAC"] = true
	if !processingRequiredTracksReady(p, requirements) {
		t.Fatal("AC3 source with AAC refused")
	}
	for _, codec := range []string{"aac", "opus", "MP3", "FLAC", "vorbis"} {
		p.outputs["audio_codec"] = codec
		p.audioCodecs = map[string]bool{normalizeTrackCodec(codec): true}
		if !processingRequiredTracksReady(p, requirements) {
			t.Errorf("browser source %s should not require AAC", codec)
		}
	}
	for _, codec := range []string{"eac3", "e-ac3", "EAC-3"} {
		if normalizeTrackCodec(codec) != "EAC3" || !isAudioCodec(codec) {
			t.Fatalf("%s source must classify as EAC3 audio", codec)
		}
	}
}

func TestMirrorOnlySourceRetainsSourceSizeRender(t *testing.T) {
	outputs := extractActiveStreamMetadata(map[string]interface{}{"health": map[string]interface{}{
		"video_HEVC_320x180_0": map[string]interface{}{"codec": "HEVC", "width": 320.0, "height": 180.0, "mirror": true},
	}})
	source, _ := sourceFromReadinessOutputs(outputs)
	config := `[{"process":"AV","codec":"H264","resolution":"0x360","track_inhibit":"video=<640x360"}]`
	got, err := mist.EnsureRotatedSourceRendition(config, source)
	if err != nil {
		t.Fatal(err)
	}
	rungs, err := mist.ExpectedVideoRenditions(got, source)
	if err != nil || len(rungs) != 1 || rungs[0].Width != 320 || rungs[0].Height != 180 {
		t.Fatalf("mirror-only source lacks its upright source-size render: %v, %v; metadata=%v", rungs, err, outputs)
	}
}

func TestSourceMetadataUsesSpanForNonzeroTimeline(t *testing.T) {
	outputs := extractActiveStreamMetadata(map[string]interface{}{"firstms": 90000.0, "lastms": 150000.0})
	if outputs["duration_ms"] != "60000" || outputs["source_first_ms"] != "90000" {
		t.Fatalf("source duration includes its absolute timeline offset: %v", outputs)
	}
}
