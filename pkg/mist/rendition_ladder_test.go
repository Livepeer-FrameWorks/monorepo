package mist

import (
	"fmt"
	"testing"
)

func TestExpectedVideoRenditionGeometry(t *testing.T) {
	for _, tc := range []struct {
		w, h, rotation     int
		lpW, lpH, avW, avH int
	}{
		{1920, 1080, 0, 640, 360, 640, 360},
		{2560, 1440, 0, 640, 360, 640, 360},
		{3840, 2160, 0, 640, 360, 640, 360},
		{1920, 1200, 0, 576, 360, 576, 360},
		{960, 540, 0, 640, 360, 640, 360},
		{1080, 1080, 0, 360, 360, 360, 360},
		{720, 1280, 0, 360, 640, 360, 640},
		{1080, 1920, 0, 360, 640, 360, 640},
		{1920, 1080, 90, 360, 640, 360, 640},
		{1920, 1080, -90, 360, 640, 360, 640},
		{1920, 1080, 450, 360, 640, 360, 640},
		{1920, 1080, 270, 360, 640, 360, 640},
		{1920, 1080, 180, 640, 360, 640, 360},
		{854, 480, 0, 642, 360, 640, 360},
	} {
		t.Run(fmt.Sprintf("%dx%d/%d", tc.w, tc.h, tc.rotation), func(t *testing.T) {
			src := SourceMediaInfo{Width: tc.w, Height: tc.h, Rotation: tc.rotation}
			for _, path := range []struct {
				config string
				w, h   int
			}{
				{`[{"process":"Livepeer","target_profiles":[{"name":"360p","height":360}]}]`, tc.lpW, tc.lpH},
				{`[{"process":"AV","codec":"H264","resolution":"x360"}]`, tc.avW, tc.avH},
			} {
				got, err := ExpectedVideoRenditions(path.config, src)
				if err != nil || len(got) != 1 {
					t.Fatalf("geometry = %v, %v", got, err)
				}
				if got[0].Width != path.w || got[0].Height != path.h {
					t.Errorf("geometry = %dx%d, want %dx%d", got[0].Width, got[0].Height, path.w, path.h)
				}
				if !RenditionShortSideMatches(got[0].Width, got[0].Height, 360) {
					t.Error("360p rendition did not match")
				}
			}
		})
	}
}

func TestEnsureRotatedSourceRendition(t *testing.T) {
	config := `[{"process":"Livepeer","source_mask":4,"target_profiles":[{"name":"360p","height":360,"track_inhibit":"video=<640x360"}]}]`
	source := SourceMediaInfo{Width: 320, Height: 180, Rotation: 90}
	got, err := EnsureRotatedSourceRendition(config, source)
	if err != nil {
		t.Fatal(err)
	}
	rungs, err := ExpectedVideoRenditions(got, source)
	if err != nil || len(rungs) != 1 || rungs[0].Process != "AV" || rungs[0].Width != 180 || rungs[0].Height != 320 {
		t.Fatalf("source rotation ladder = %v, %v", rungs, err)
	}
	if again, err := EnsureRotatedSourceRendition(got, source); err != nil || again != got {
		t.Fatalf("not idempotent: %s %v", again, err)
	}
	source.Rotation = 0
	if got, err := EnsureRotatedSourceRendition(config, source); err != nil || got != config {
		t.Fatalf("unrotated policy changed: %s %v", got, err)
	}
}

func TestExpectedVideoRenditionsLocalInhibitors(t *testing.T) {
	config := `[{"process":"AV","codec":"AAC"},{"process":"Thumbs"},{"process":"AV","codec":"H264","resolution":"0x720","track_inhibit":"video=<1280x720"}]`
	got, err := ExpectedVideoRenditions(config, SourceMediaInfo{Width: 640, Height: 360})
	if err != nil || len(got) != 0 {
		t.Fatalf("inhibited local ladder = %v, %v", got, err)
	}
	for _, config := range []string{`[{"process":"AV","codec":"H264","resolution":"broken"}]`, `[{"process":"Livepeer","target_profiles":[null]}]`} {
		if _, err := ExpectedVideoRenditions(config, SourceMediaInfo{}); err == nil {
			t.Fatalf("invalid config accepted: %s", config)
		}
	}
}

func TestExpectedRenditionsRefuseMalformedAVIntent(t *testing.T) {
	for _, policy := range []string{`[{"process":"AV","codec":12}]`, `[{"process":"AV","codec":"H264","resolution":720}]`, `[{"process":"AV","codec":"H264","x-LSP-name":[]}]`} {
		if _, err := ExpectedVideoRenditions(policy, SourceMediaInfo{Width: 1920, Height: 1080}); err == nil {
			t.Fatalf("malformed intent was accepted: %s", policy)
		}
	}
}
