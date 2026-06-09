package handlers

import (
	"io"
	"reflect"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	"github.com/sirupsen/logrus"
)

func discardLog() *logrus.Entry {
	l := logrus.New()
	l.SetOutput(io.Discard)
	return logrus.NewEntry(l)
}

// livepeerRenditionsCompleteFromTracks is the wrapper that parses the requested
// Livepeer ladder from the process config and delegates to the pure
// completeness check. It must fail closed on malformed config, return true when
// nothing is requested, and return true when every requested rendition has a
// matching output track.
func TestLivepeerRenditionsCompleteFromTracks(t *testing.T) {
	log := discardLog()
	source := mist.SourceMediaInfo{Width: 1920, Height: 1080}

	t.Run("malformed process config fails closed", func(t *testing.T) {
		if livepeerRenditionsCompleteFromTracks(log, "not-json", nil, source, 30000) {
			t.Fatal("malformed config must be treated as incomplete")
		}
	})

	t.Run("no renditions requested is complete", func(t *testing.T) {
		// An empty process array requests no Livepeer transcode.
		if !livepeerRenditionsCompleteFromTracks(log, "[]", nil, source, 30000) {
			t.Fatal("no requested renditions has nothing to prove → complete")
		}
	})

	t.Run("requested rendition with matching track is complete", func(t *testing.T) {
		processes := `[{"process":"Livepeer","target_profiles":[{"height":720}]}]`
		tracks := []processingMetaVideoTrack{chapterTrack(2, 1280, 720, 30000)}
		if !livepeerRenditionsCompleteFromTracks(log, processes, tracks, source, 30000) {
			t.Fatal("a 720 request matched by a 720 track must be complete")
		}
	})

	t.Run("requested rendition with no matching track is incomplete", func(t *testing.T) {
		processes := `[{"process":"Livepeer","target_profiles":[{"height":720}]}]`
		if livepeerRenditionsCompleteFromTracks(log, processes, nil, source, 30000) {
			t.Fatal("a requested rendition with no tracks must be incomplete")
		}
	})
}

// requiredTrackSummary projects the required-codec sets into a stable, sorted
// map for logging; boolKeys / mapKeys are its building blocks.
func TestRequiredTrackSummary(t *testing.T) {
	req := processingTrackRequirements{
		requiredAudioCodecs: map[string]bool{"AAC": true},
		requiredVideoCodecs: map[string]bool{"HEVC": true, "H264": true},
		requireThumbs:       true,
	}
	got := requiredTrackSummary(req)
	want := map[string][]string{
		"audio":  {"AAC"},
		"video":  {"H264", "HEVC"}, // sorted
		"thumbs": {"required"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("requiredTrackSummary = %#v, want %#v", got, want)
	}

	empty := requiredTrackSummary(processingTrackRequirements{})
	if empty["thumbs"] != nil {
		t.Fatalf("thumbs not required → nil, got %#v", empty["thumbs"])
	}
	if len(empty["audio"]) != 0 || len(empty["video"]) != 0 {
		t.Fatalf("empty requirements → empty key lists, got %#v", empty)
	}
}

func TestBoolKeys(t *testing.T) {
	if got := boolKeys(false); got != nil {
		t.Fatalf("boolKeys(false) = %#v, want nil", got)
	}
	if got := boolKeys(true); !reflect.DeepEqual(got, []string{"required"}) {
		t.Fatalf("boolKeys(true) = %#v, want [required]", got)
	}
}

func TestMapKeys(t *testing.T) {
	if got := mapKeys(nil); len(got) != 0 {
		t.Fatalf("mapKeys(nil) = %#v, want empty", got)
	}
	got := mapKeys(map[string]bool{"c": true, "a": true, "b": true})
	if !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("mapKeys = %#v, want sorted [a b c]", got)
	}
}

// cloneStringMap must return an independent copy — mutating the clone must not
// touch the source.
func TestCloneStringMap(t *testing.T) {
	src := map[string]string{"k": "v"}
	clone := cloneStringMap(src)
	if !reflect.DeepEqual(clone, src) {
		t.Fatalf("clone = %#v, want equal to src %#v", clone, src)
	}
	clone["k"] = "mutated"
	clone["new"] = "x"
	if src["k"] != "v" {
		t.Fatal("mutating the clone leaked into the source")
	}
	if _, ok := src["new"]; ok {
		t.Fatal("adding to the clone leaked into the source")
	}

	if got := cloneStringMap(nil); len(got) != 0 {
		t.Fatalf("cloneStringMap(nil) = %#v, want empty", got)
	}
}
