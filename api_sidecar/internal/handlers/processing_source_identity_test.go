package handlers

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	"github.com/sirupsen/logrus"
)

// Fixtures are the first 4 KiB of MistServer v0.3.26 MKV responses for a VoD
// stream whose tracks are idx 0 video 320x240 (id 1), idx 1 AAC (id 2), idx 2
// video 320x240 (id 3), idx 3 video 160x120 (id 4):
//   - mist_probe_explicit: ?video=i4,i1&audio=none&subtitle=none&meta=none
//   - mist_probe_source:   ?video=source&audio=source&subtitle=none&meta=none
//   - mist_cut:            ?audio=all&video=all,!JPEG&meta=all&subtitle=all&rate=0
func readMistFixture(t *testing.T, name string) []mkvTrack {
	t.Helper()
	tracks, err := readStagedMKVTracks(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return tracks
}

func trackNumbers(tracks []mkvTrack) []uint64 {
	out := make([]uint64, 0, len(tracks))
	for _, t := range tracks {
		out = append(out, t.number)
	}
	return out
}

func ebmlElement(id uint64, data []byte) []byte {
	var out []byte
	for shift := 24; shift >= 0; shift -= 8 {
		if b := byte(id >> uint(shift)); b != 0 || len(out) > 0 {
			out = append(out, b)
		}
	}
	// 8-byte size field, as valid for any length.
	out = append(out, 0x01)
	for shift := 48; shift >= 0; shift -= 8 {
		out = append(out, byte(uint64(len(data))>>uint(shift)))
	}
	return append(out, data...)
}

func ebmlUintElement(id, v uint64) []byte {
	return ebmlElement(id, []byte{byte(v >> 8), byte(v)})
}

// fakeMistCutBody is a Mist-shaped MKV cut of a stream holding one 640x360
// video track (idx 0) and one audio track (idx 1), as the clip harnesses'
// fake Mist serves it for both the source probe and the staged cut.
func fakeMistCutBody() []byte {
	video := append(ebmlUintElement(ebmlIDTrackNumber, 1), ebmlUintElement(ebmlIDTrackType, mkvTrackTypeVideo)...)
	video = append(video, ebmlElement(ebmlIDCodecID, []byte("V_MPEG4/ISO/AVC"))...)
	video = append(video, ebmlElement(ebmlIDVideo, append(ebmlUintElement(ebmlIDPixelWidth, 640), ebmlUintElement(ebmlIDPixelHeight, 360)...))...)
	audio := append(ebmlUintElement(ebmlIDTrackNumber, 2), ebmlUintElement(ebmlIDTrackType, 2)...)
	tracks := append(ebmlElement(ebmlIDTrackEntry, video), ebmlElement(ebmlIDTrackEntry, audio)...)
	header := ebmlElement(0x1A45DFA3, ebmlElement(0x4282, []byte("matroska")))
	segment := append([]byte{0x18, 0x53, 0x80, 0x67, 0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}, ebmlElement(ebmlIDTracks, tracks)...)
	return append(append(header, segment...), ebmlElement(ebmlIDCluster, []byte{0x00})...)
}

func TestFakeMistCutBodyParses(t *testing.T) {
	tracks, err := readMKVTracks(bytes.NewReader(fakeMistCutBody()))
	if err != nil {
		t.Fatal(err)
	}
	if got := trackNumbers(tracks); !reflect.DeepEqual(got, []uint64{1, 2}) || tracks[0].height != 360 || !tracks[0].isPlayableVideo() {
		t.Fatalf("tracks = %+v", tracks)
	}
}

// An explicit selection does not renumber: Mist writes each track as its
// stream index + 1, in ascending index order, whatever order was requested.
func TestMistMKVTrackNumbersAreStreamIndexPlusOne(t *testing.T) {
	explicit := readMistFixture(t, "mist_probe_explicit.head.mkv")
	if got := trackNumbers(explicit); !reflect.DeepEqual(got, []uint64{1, 4}) {
		t.Fatalf("video=i4,i1 track numbers = %v, want [1 4]", got)
	}
	if explicit[0].height != 240 || explicit[1].height != 120 {
		t.Fatalf("heights = %d,%d, want 240,120", explicit[0].height, explicit[1].height)
	}

	cut := readMistFixture(t, "mist_cut.head.mkv")
	if got := trackNumbers(cut); !reflect.DeepEqual(got, []uint64{1, 2, 3, 4}) {
		t.Fatalf("full cut track numbers = %v, want [1 2 3 4]", got)
	}
	if cut[1].isPlayableVideo() || !cut[0].isPlayableVideo() || !cut[2].isPlayableVideo() {
		t.Fatalf("track types misread: %+v", cut)
	}
}

func TestProbeClipSourceTrackNumbersReadsMistHeader(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "mist_probe_source.head.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	cutURL := srv.URL + "/live+abc.mkv?audio=all&video=all%2C%21JPEG&meta=all%2C%21thumbvtt&subtitle=all&rate=0&token=cred&tkn=cut-session&startunix=-30&duration=20"
	numbers, err := probeClipSourceTrackNumbers(context.Background(), cutURL)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !reflect.DeepEqual(numbers, map[uint64]bool{1: true, 3: true, 4: true}) {
		t.Fatalf("source video track numbers = %v, want 1,3,4", numbers)
	}
	for key, want := range map[string]string{
		"video": "source", "audio": "source", "subtitle": "none", "meta": "none",
		"token": "cred", "startunix": "-30", "duration": "20", "rate": "0",
	} {
		if got := gotQuery.Get(key); got != want {
			t.Errorf("probe %s = %q, want %q", key, got, want)
		}
	}
	if tkn := gotQuery.Get("tkn"); tkn == "cut-session" || !strings.HasPrefix(tkn, mist.ProcessingReadSessionPrefix) {
		t.Errorf("probe session token = %q, want its own processing read session", tkn)
	}
}

func TestReadMKVTracksRejectsMediaBeforeTracks(t *testing.T) {
	// EBML header, Segment of unknown size, then a Cluster.
	data := []byte{0x1A, 0x45, 0xDF, 0xA3, 0x80, 0x18, 0x53, 0x80, 0x67, 0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x1F, 0x43, 0xB6, 0x75, 0x80}
	if _, err := readMKVTracks(bytes.NewReader(data)); err == nil {
		t.Fatal("a cluster before Tracks must be refused")
	}
	if _, err := readMKVTracks(bytes.NewReader(nil)); err == nil {
		t.Fatal("an empty body must be refused")
	}
}

// The R18-4 staging stream as the clip sees it: the live stream holds the
// 1080p source (idx 0), AAC (idx 1), thumbnails (idx 5, not cut), a 1080p
// rendition (idx 9) and a 720p rendition (idx 10). The staged MKV declares
// them as TrackNumbers 1, 2, 10, 11; processing+ numbers the file's tracks
// 0..3 and adds its own thumbnail track.
func r184StagedClip(renditionBuffer float64) (map[string]interface{}, *clipSourceIdentity) {
	health := map[string]interface{}{
		"video_H264_1920x1080_30fps_0": map[string]interface{}{
			"codec": "H264", "idx": float64(0), "id": float64(0), "width": float64(1920), "height": float64(1080), "buffer": float64(20000),
		},
		"audio_AAC_2ch_48000hz_1": map[string]interface{}{
			"codec": "AAC", "idx": float64(1), "id": float64(1), "buffer": float64(21370),
		},
		"video_H264_1920x1080_30fps_2": map[string]interface{}{
			"codec": "H264", "idx": float64(2), "id": float64(2), "width": float64(1920), "height": float64(1080), "buffer": renditionBuffer,
		},
		"video_H264_1280x720_30fps_3": map[string]interface{}{
			"codec": "H264", "idx": float64(3), "id": float64(3), "width": float64(1280), "height": float64(720), "buffer": float64(1000),
		},
		"video_JPEG_320x180_4": map[string]interface{}{
			"codec": "JPEG", "idx": float64(4), "id": float64(4), "width": float64(320), "height": float64(180), "source": "video_H264_1920x1080_30fps_0",
		},
	}
	staged := []mkvTrack{
		{number: 1, trackTy: mkvTrackTypeVideo, codecID: "V_MPEG4/ISO/AVC", width: 1920, height: 1080},
		{number: 2, trackTy: 2, codecID: "A_AAC"},
		{number: 10, trackTy: mkvTrackTypeVideo, codecID: "V_MPEG4/ISO/AVC", width: 1920, height: 1080},
		{number: 11, trackTy: mkvTrackTypeVideo, codecID: "V_MPEG4/ISO/AVC", width: 1280, height: 720},
	}
	return map[string]interface{}{"health": health}, newClipSourceIdentity(staged, map[uint64]bool{1: true})
}

func TestClipSourceIdentityMapsStagedOrderOntoProcessingIDs(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.FatalLevel)
	processes := `[{"process":"Livepeer","target_profiles":[{"name":"1080p","height":1080},{"name":"720p","height":720}]}]`
	source := mist.SourceMediaInfo{Width: 1920, Height: 1080}
	// The rendition's span and Go's map order must not matter.
	for _, renditionBuffer := range []float64{0, 20000, 21370} {
		for i := 0; i < 16; i++ {
			streamData, identity := r184StagedClip(renditionBuffer)
			tracks := inspectProcessingActiveStream(streamData).videoTracks
			ids, err := identity.sourceTrackIDs(tracks)
			if err != nil {
				t.Fatalf("map identity: %v", err)
			}
			if !reflect.DeepEqual(ids, map[int64]bool{0: true}) {
				t.Fatalf("source ids = %v, want {0}", ids)
			}
			// The 720p rendition is short, so the clip publishes source.
			if got := chooseProcessingVideoSelector(logrus.NewEntry(log), processes, tracks, source, 21370, ids); got != "i0" {
				t.Fatalf("selector = %q, want the recorded source i0", got)
			}
		}
	}
}

func TestClipSourceIdentityFailsClosedWhenStagedTracksDoNotLineUp(t *testing.T) {
	streamData, identity := r184StagedClip(21370)
	health := streamData["health"].(map[string]interface{})

	// Mist skipped a frameless staged track: positions no longer line up.
	delete(health, "video_H264_1280x720_30fps_3")
	if _, err := identity.sourceTrackIDs(inspectProcessingActiveStream(streamData).videoTracks); err == nil {
		t.Fatal("a missing staged video track must fail the mapping")
	}

	streamData, identity = r184StagedClip(21370)
	health = streamData["health"].(map[string]interface{})
	health["video_H264_1920x1080_30fps_2"].(map[string]interface{})["height"] = float64(480)
	if _, err := identity.sourceTrackIDs(inspectProcessingActiveStream(streamData).videoTracks); err == nil {
		t.Fatal("a height that disagrees with the staged entry must fail the mapping")
	}
}

// DVR and chapter material carries only source tracks, so Mist's "source"
// selector on it names every video track and the clip keeps them all.
func TestClipOfSourceOnlyMaterialKeepsEveryTrack(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.FatalLevel)
	staged := []mkvTrack{
		{number: 1, trackTy: mkvTrackTypeVideo, height: 1080},
		{number: 2, trackTy: mkvTrackTypeVideo, height: 720},
	}
	identity := newClipSourceIdentity(staged, map[uint64]bool{1: true, 2: true})
	tracks := []processingMetaVideoTrack{chapterTrack(0, 1920, 1080, 30000), chapterTrack(1, 1280, 720, 30000)}
	ids, err := identity.sourceTrackIDs(tracks)
	if err != nil {
		t.Fatal(err)
	}
	processes := `[{"process":"Livepeer","target_profiles":[{"name":"720p","height":720}]}]`
	got := chooseProcessingVideoSelector(logrus.NewEntry(log), processes, tracks, mist.SourceMediaInfo{Width: 1920, Height: 1080}, 30000, ids)
	if got != "i0,i1" {
		t.Fatalf("selector = %q, want every source-only track", got)
	}
}
