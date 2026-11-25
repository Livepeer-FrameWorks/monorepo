package handlers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/sirupsen/logrus"
)

// retentionLadder is the catalog ladder as composed for Livepeer: every rung
// carries its track_retain selector and the process masks the originals its
// transcoded rungs replace.
func retentionLadder(t *testing.T) string {
	t.Helper()
	var profiles []map[string]any
	for _, rung := range []struct {
		name    string
		short   int
		bitrate int64
	}{{"1080p", 1080, 7800000}, {"720p", 720, 4500000}, {"480p", 480, 1600000}, {"360p", 360, 900000}} {
		profiles = append(profiles, map[string]any{
			"name": rung.name, "height": rung.short, "bitrate": rung.bitrate, "profile": "H264ConstrainedHigh",
			"track_retain": mist.RetainSelector(mist.RetainRung{ShortSide: rung.short, Bitrate: rung.bitrate}),
		})
	}
	encoded, err := json.Marshal([]map[string]any{{"process": "Livepeer", "mask_replaced_originals": true, "target_profiles": profiles}})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// originalTrack is an uploaded or ingested video track; bps is Mist's track
// bps (bytes per second).
func originalTrack(id int64, width, height int, bps uint64, span float64) processingMetaVideoTrack {
	t := chapterTrack(id, width, height, span)
	t.bps = bps
	return t
}

func quietLog() *logrus.Entry {
	log := logrus.New()
	log.SetLevel(logrus.FatalLevel)
	return logrus.NewEntry(log)
}

const (
	bps1080InBand = 7800000 / 8
	bps720InBand  = 4500000 / 8
	bps360InBand  = 900000 / 8
	bps15Mbps     = 15000000 / 8
)

// 1080p, 720p and 360p H264 originals at in-band bitrates retain their rungs;
// only 480p is transcoded.
func multiRenditionUpload() []processingMetaVideoTrack {
	return []processingMetaVideoTrack{
		originalTrack(1, 1920, 1080, bps1080InBand, 30000),
		originalTrack(2, 1280, 720, bps720InBand, 30000),
		originalTrack(3, 640, 360, bps360InBand, 30000),
	}
}

func TestReadinessSourceIsHighestBitrateOriginal(t *testing.T) {
	outputs := extractActiveStreamMetadata(map[string]interface{}{
		"lastms": float64(30000),
		"health": map[string]interface{}{
			"video_H264_1920x1080_0": map[string]interface{}{
				"codec": "H264", "width": float64(1920), "height": float64(1080), "idx": float64(0), "id": float64(1), "kbits": float64(7617),
			},
			"video_H264_1280x720_1": map[string]interface{}{
				"codec": "H264", "width": float64(1280), "height": float64(720), "idx": float64(1), "id": float64(2), "kbits": float64(14648),
			},
			"video_H264_854x480_2": map[string]interface{}{
				"codec": "H264", "width": float64(854), "height": float64(480), "idx": float64(2), "id": float64(5), "kbits": float64(1562), "source": "video_H264_1280x720_1",
			},
		},
	})
	if outputs["resolution"] != "1280x720" {
		t.Fatalf("readiness source = %q, want the highest-bitrate original 1280x720 (Mist transcodes from maxbps)", outputs["resolution"])
	}
	tracks := readinessVideoTracks(outputs)
	if len(tracks) != 3 || !tracks[0].Original || !tracks[1].Original || tracks[2].Original || tracks[1].Bps != 14648*128 {
		t.Fatalf("readiness track list = %+v, want 1080p and 720p originals and a derived 480p with bps from kbits", tracks)
	}
	if mist.TranscodeSourceIndex(tracks) != 1 {
		t.Fatalf("transcode source = %d, want the 720p original", mist.TranscodeSourceIndex(tracks))
	}
}

func TestRetainedOriginalsSatisfyCompleteness(t *testing.T) {
	processes := retentionLadder(t)
	source := mist.SourceMediaInfo{Width: 1920, Height: 1080}
	tracks := append(multiRenditionUpload(), renditionTrack(5, 854, 480, 30000))
	if !renditionsComplete(quietLog(), processes, tracks, source, 30000, 0, 30000) {
		t.Fatal("1080p/720p/360p originals with a transcoded 480p were refused: retained rungs must be satisfied by their originals")
	}
	truncated := append(multiRenditionUpload(), renditionTrack(5, 854, 480, 30000))
	truncated[1].lastms = 12000
	if renditionsComplete(quietLog(), processes, truncated, source, 30000, 0, 30000) {
		t.Fatal("a retained 720p original that stops at 12 s was accepted against a 30 s source")
	}
	if renditionsComplete(quietLog(), processes, multiRenditionUpload(), source, 30000, 0, 30000) {
		t.Fatal("a ladder missing its only transcoded rung (480p) was accepted")
	}
}

func TestOutOfBandOriginalIsTranscodedAndMasked(t *testing.T) {
	processes := retentionLadder(t)
	originals := []processingMetaVideoTrack{
		originalTrack(1, 1920, 1080, bps1080InBand, 30000),
		originalTrack(2, 1280, 720, bps15Mbps, 30000),
		originalTrack(3, 640, 360, bps360InBand, 30000),
	}
	originals[1].trackIndex, originals[1].hasTrackIndex = 1, true
	// The 15 Mbps 720p original is the highest-bitrate track: the transcode source.
	source := mist.SourceMediaInfo{Width: 1280, Height: 720}
	if got := processingRecordingVideoSelector(quietLog(), processes, originals, source); got != "all,!1" {
		t.Fatalf("VOD selector = %q, want all,!1: the out-of-band 720p original is replaced by its transcode", got)
	}
	complete := append(append([]processingMetaVideoTrack{}, originals...), renditionTrack(6, 1280, 720, 30000), renditionTrack(5, 854, 480, 30000))
	if !renditionsComplete(quietLog(), processes, complete, source, 30000, 0, 30000) {
		t.Fatal("retained 1080p/360p originals plus transcoded 720p/480p were refused")
	}
	missing720 := append(append([]processingMetaVideoTrack{}, originals...), renditionTrack(5, 854, 480, 30000))
	if renditionsComplete(quietLog(), processes, missing720, source, 30000, 0, 30000) {
		t.Fatal("the out-of-band 720p original was taken for the 720p rung")
	}
}

func TestSingleSourceRetentionDecidesSourceMasking(t *testing.T) {
	processes := retentionLadder(t)
	source := mist.SourceMediaInfo{Width: 1920, Height: 1080}
	inBand := []processingMetaVideoTrack{originalTrack(1, 1920, 1080, bps1080InBand, 30000)}
	if got := processingRecordingVideoSelector(quietLog(), processes, inBand, source); got != "all" {
		t.Fatalf("VOD selector = %q, want all: an in-band source fills its own 1080p rung and stays in the VOD", got)
	}
	ladder := append(append([]processingMetaVideoTrack{}, inBand...), renditionTrack(6, 1280, 720, 30000), renditionTrack(5, 854, 480, 30000), renditionTrack(3, 640, 360, 30000))
	if !renditionsComplete(quietLog(), processes, ladder, source, 30000, 0, 30000) {
		t.Fatal("an in-band 1080p source with transcoded 720p/480p/360p was refused: the source retains 1080p")
	}

	outOfBand := []processingMetaVideoTrack{originalTrack(1, 1920, 1080, 30000000/8, 30000)}
	outOfBand[0].trackIndex, outOfBand[0].hasTrackIndex = 0, true
	if got := processingRecordingVideoSelector(quietLog(), processes, outOfBand, source); got != "all,!0" {
		t.Fatalf("VOD selector = %q, want all,!0: a 30 Mbps source retains no rung and is replaced", got)
	}
	noTopRung := append(append([]processingMetaVideoTrack{}, outOfBand...), renditionTrack(6, 1280, 720, 30000), renditionTrack(5, 854, 480, 30000), renditionTrack(3, 640, 360, 30000))
	if renditionsComplete(quietLog(), processes, noTopRung, source, 30000, 0, 30000) {
		t.Fatal("an out-of-band source was taken for the 1080p rung")
	}

	// A policy that does not mask replaced originals records every track.
	unmasked := strings.Replace(processes, `"mask_replaced_originals":true,`, "", 1)
	if got := processingRecordingVideoSelector(quietLog(), unmasked, outOfBand, source); got != "all" {
		t.Fatalf("VOD selector = %q, want all without mask_replaced_originals", got)
	}
}

func TestClipSelectorPicksRetainedOriginals(t *testing.T) {
	processes := retentionLadder(t)
	source := mist.SourceMediaInfo{Width: 1920, Height: 1080}
	tracks := append(multiRenditionUpload(), renditionTrack(5, 854, 480, 30000))
	if got := chooseProcessingVideoSelector(quietLog(), processes, tracks, source, 30000, nil); got != "i1,i2,i5,i3" {
		t.Fatalf("selector = %q, want the retained originals with the transcoded 480p (i1,i2,i5,i3)", got)
	}
	// A clip's staged cut names its source tracks by id instead of lineage.
	staged := append(multiRenditionUpload(), chapterTrack(5, 854, 480, 30000))
	if got := chooseProcessingVideoSelector(quietLog(), processes, staged, source, 30000, map[int64]bool{1: true, 2: true, 3: true}); got != "i1,i2,i5,i3" {
		t.Fatalf("clip selector = %q, want i1,i2,i5,i3", got)
	}
}

func TestLiveAdmissionReservesNothingForRetainedRungs(t *testing.T) {
	isolateProcessingOverrideState(t)
	previous := logger
	logger = discardLog().Logger
	t.Cleanup(func() { logger = previous })
	outputs := extractActiveStreamMetadata(map[string]interface{}{
		"health": map[string]interface{}{
			"video_H264_1920x1080_0": map[string]interface{}{"codec": "H264", "width": float64(1920), "height": float64(1080), "idx": float64(0), "kbits": float64(7617)},
			"video_H264_1280x720_1":  map[string]interface{}{"codec": "H264", "width": float64(1280), "height": float64(720), "idx": float64(1), "kbits": float64(4394)},
			"video_H264_640x360_2":   map[string]interface{}{"codec": "H264", "width": float64(640), "height": float64(360), "idx": float64(2), "kbits": float64(878)},
		},
	})
	source, _ := sourceFromReadinessOutputs(outputs)
	total := uint64(16*1024) * memoryMiB
	snapshot := func() (uint64, uint64, error) { return total, total, nil }
	admitted, err := reserveLiveReplacement("live+multitrack", mist.ReplaceLivepeerWithLocal(retentionLadder(t)), source, readinessVideoTracks(outputs), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var processes []map[string]any
	if err = json.Unmarshal([]byte(admitted), &processes); err != nil {
		t.Fatal(err)
	}
	var transcoded []map[string]any
	for _, process := range processes {
		if process["resolution"] == "x480" || process["resolution"] == "0x480" || process["resolution"] == "854x480" {
			transcoded = append(transcoded, process)
		}
	}
	if len(processes) != 4 || len(transcoded) != 1 {
		t.Fatalf("admitted policy = %s, want all four rungs with one 480p encoder", admitted)
	}
	only480, err := json.Marshal(transcoded)
	if err != nil {
		t.Fatal(err)
	}
	want, err := mist.EstimateProcessingMemory(string(only480), source)
	if err != nil {
		t.Fatal(err)
	}
	processingProcessOverridesMu.Lock()
	reserved := processingOverrideRecords["live+multitrack"].MemoryBytes
	processingProcessOverridesMu.Unlock()
	if reserved != want {
		t.Fatalf("live admission reserved %d bytes, want %d for the 480p encoder alone: retained rungs start no process", reserved, want)
	}
}

func TestCoverageJudgesRetainedOriginalsAgainstTheSource(t *testing.T) {
	audio := coverageTrack("audio_AAC", "audio", "AAC", "", 0, 30000)
	retained1080 := coverageTrack("video_H264_1920x1080_0", "video", "H264", "", 0, 30000)
	retained720 := coverageTrack("video_H264_1280x720_1", "video", "H264", "", 0, 12000)
	transcoded := coverageTrack("video_H264_854x480_5", "video", "H264", "video_H264_1920x1080_0", 0, 30000)
	err := renditionCoverageError([]*ipcpb.StreamTrack{audio, retained1080, retained720, transcoded})
	if err == nil || !strings.Contains(err.Error(), "video_H264_1280x720_1 ends 18000ms before the source") {
		t.Fatalf("coverage error = %v, want the retained 720p original judged against the source span", err)
	}
	retained720.WrittenLastMs = retained1080.WrittenLastMs
	if err = renditionCoverageError([]*ipcpb.StreamTrack{audio, retained1080, retained720, transcoded}); err != nil {
		t.Fatalf("full retained originals refused: %v", err)
	}
}

func TestCompletedResultDropsReadinessTrackList(t *testing.T) {
	outputs := map[string]string{"resolution": "1920x1080", processingSourceVideoTracksOutput: "[]"}
	var sent *ipcpb.ProcessingJobResult
	h := &ProcessingJobHandler{}
	h.sendCompletedResult(func(msg *ipcpb.ControlMessage) { sent = msg.GetProcessingJobResult() }, "job", outputs, "/out.mkv", 1, 0, nil, false)
	if sent == nil || sent.GetOutputs()["resolution"] != "1920x1080" {
		t.Fatalf("completed result = %+v", sent)
	}
	if _, leaked := sent.GetOutputs()[processingSourceVideoTracksOutput]; leaked {
		t.Fatal("Helmsman-internal readiness track list reached Foghorn")
	}
}

func TestReadinessRetentionPreservesEveryOriginalOrientation(t *testing.T) {
	for _, rotation := range []int{0, 90, 180, 270} {
		for _, mirror := range []bool{false, true} {
			health := map[string]interface{}{"video_H264_1920x1080_0": map[string]interface{}{
				"codec": "H264", "width": float64(1920), "height": float64(1080),
				"idx": float64(0), "id": float64(1), "kbits": float64(7617),
				"rotation": float64(rotation), "mirror": mirror,
			}}
			outputs := extractActiveStreamMetadata(map[string]interface{}{"lastms": float64(30000), "health": health})
			tracks := readinessVideoTracks(outputs)
			if len(tracks) != 1 {
				t.Fatalf("readiness lost the original track: %+v", outputs)
			}
			plan := mist.PlanRetention(tracks, []mist.RetainRung{{ShortSide: 1080, Bitrate: 7800000}})
			wantRetained := rotation == 0 && !mirror
			if got := plan.Rungs[0].Track >= 0; got != wantRetained {
				t.Fatalf("rotation=%d mirror=%t retained=%t want=%t: readiness stripped the original orientation", rotation, mirror, got, wantRetained)
			}
		}
	}
}

func TestReadinessRetentionRejectsCacheWithoutOrientation(t *testing.T) {
	legacy := `[{"Index":0,"Codec":"H264","Width":1920,"Height":1080,"Bps":975000,"Original":true,"Image":false}]`
	if tracks := readinessVideoTracks(map[string]string{processingSourceVideoTracksOutput: legacy}); len(tracks) != 0 {
		t.Fatalf("cached tracks without orientation cannot prove an upright retained original: %+v", tracks)
	}
}

func TestRecordingRetentionDoesNotReselectTransformedOriginal(t *testing.T) {
	for _, rotation := range []int{0, 90, 180, 270} {
		for _, mirror := range []bool{false, true} {
			raw := map[string]interface{}{"video_H264_1920x1080_0": map[string]interface{}{
				"type": "video", "codec": "H264", "width": float64(1920), "height": float64(1080),
				"idx": float64(0), "id": float64(1), "bps": float64(bps1080InBand), "kbits": float64(7617),
				"rotation": float64(rotation), "mirror": mirror,
			}}
			variants := [][]processingMetaVideoTrack{
				parseProcessingMetaVideoTracks(map[string]interface{}{"tracks": raw}),
				inspectProcessingActiveStream(map[string]interface{}{"health": raw}).videoTracks,
			}
			for _, tracks := range variants {
				source := mist.SourceMediaInfo{VideoCodec: "H264", Width: 1920, Height: 1080, Rotation: rotation, Mirror: mirror}
				got := processingRecordingVideoSelector(quietLog(), retentionLadder(t), tracks, source)
				want := "all,!0"
				if rotation == 0 && !mirror {
					want = "all"
				}
				if got != want {
					t.Fatalf("rotation=%d mirror=%t selector=%q want=%q", rotation, mirror, got, want)
				}
			}
		}
	}
}
