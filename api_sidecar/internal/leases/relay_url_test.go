package leases

import "testing"

func TestParseArtifactRelayURL(t *testing.T) {
	cases := []struct {
		in       string
		wantOK   bool
		wantType string
		wantHash string
	}{
		{"http://127.0.0.1:18007/internal/artifact/vod/abc123.mkv", true, "vod", "abc123"},
		{"http://127.0.0.1:18007/internal/artifact/vod/abc123.mkv.dtsh", true, "vod", "abc123"},
		{"http://127.0.0.1:18007/internal/artifact/clip/xyz.mp4", true, "clip", "xyz"},
		{"http://127.0.0.1:18007/internal/artifact/clip/streamA/xyz.mp4", true, "clip", "xyz"},
		{"http://127.0.0.1:18007/internal/artifact/clip/streamA/xyz.mp4.dtsh", true, "clip", "xyz"},
		{"http://127.0.0.1:18007/internal/artifact/upload/up1.flv", true, "upload", "up1"},
		{"https://example.com/internal/artifact/vod/abc.mkv", true, "vod", "abc"},
		{"/local/path/file.mp4", false, "", ""},
		{"s3://bucket/key", false, "", ""},
		{"http://example.com/other/path/file.mkv", false, "", ""},
		{"http://example.com/internal/artifact/unknown/abc.mkv", false, "", ""},
		{"", false, "", ""},
	}
	for _, tc := range cases {
		got, ok := ParseArtifactRelayURL(tc.in)
		if ok != tc.wantOK {
			t.Errorf("ParseArtifactRelayURL(%q) ok=%v want=%v", tc.in, ok, tc.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if got.Type != tc.wantType || got.Hash != tc.wantHash {
			t.Errorf("ParseArtifactRelayURL(%q) = %+v; want type=%s hash=%s", tc.in, got, tc.wantType, tc.wantHash)
		}
	}
}

func TestIsRelayArtifactResponse(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"http://127.0.0.1:18007/internal/artifact/vod/abc.mkv", true},
		{"https://host/internal/artifact/clip/x.mp4", true},
		{"/storage/vod/abc.mkv", false},
		{"balance:vod+abc", false},
		{"s3://bucket/key", false},
		{"http://host/other/path", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsRelayArtifactResponse(tc.in); got != tc.want {
			t.Errorf("IsRelayArtifactResponse(%q) = %v; want %v", tc.in, got, tc.want)
		}
	}
}

func TestExtFromRelayURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"http://h/internal/artifact/vod/abc.mkv", ".mkv"},
		{"http://h/internal/artifact/vod/abc.mkv.dtsh", ".mkv"},
		{"http://h/internal/artifact/upload/x.flv", ".flv"},
		{"http://h/internal/artifact/clip/streamA/abc.mp4", ".mp4"},
		{"http://h/internal/artifact/clip/streamA/abc.mp4.dtsh", ".mp4"},
		{"http://h/other/path/file.mp4", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := ExtFromRelayURL(tc.in); got != tc.want {
			t.Errorf("ExtFromRelayURL(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

// TestStreamInternalFromRelayURL pins the path-encoded stream extraction
// for the new clip relay URL shape. Stream identity moved from ?s= to a
// path segment so Mist's input + ".dtsh" sidecar mutation doesn't
// corrupt it.
func TestStreamInternalFromRelayURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"http://h/internal/artifact/clip/streamA/abc.mp4", "streamA"},
		{"http://h/internal/artifact/clip/streamA/abc.mp4.dtsh", "streamA"},
		{"http://h/internal/artifact/clip/abc.mp4", ""},   // flat clip, no stream
		{"http://h/internal/artifact/vod/abc.mkv", ""},    // VOD never has stream
		{"http://h/internal/artifact/upload/up1.flv", ""}, // upload never has stream
		{"http://h/other/path", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := StreamInternalFromRelayURL(tc.in); got != tc.want {
			t.Errorf("StreamInternalFromRelayURL(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

func TestDeterministicPathsForAsset(t *testing.T) {
	t.Run("vod", func(t *testing.T) {
		paths := DeterministicPathsForAsset("/base", AssetKey{Type: "vod", Hash: "abc"}, ".mkv", "", nil)
		want := []string{
			"/base/vod/abc.mkv",
			"/base/vod/abc.mkv.partial",
			"/base/vod/abc.mkv.dtsh",
			"/base/vod/abc.mkv.gop",
			"/base/vod/abc.mkv.blocks",
		}
		if len(paths) != len(want) {
			t.Fatalf("got %d paths, want %d: %v", len(paths), len(want), paths)
		}
		for i, p := range paths {
			if p != want[i] {
				t.Errorf("path[%d]=%q want %q", i, p, want[i])
			}
		}
	})
	t.Run("dvr_with_segments", func(t *testing.T) {
		// DVR leases pin source TS segments only.
		paths := DeterministicPathsForAsset("/base", AssetKey{Type: "dvr", Hash: "dh"}, "", "", []string{"seg1.ts", "seg2.ts"})
		wantContains := []string{
			"/base/dvr/dh/segments/seg1.ts",
			"/base/dvr/dh/segments/seg2.ts",
		}
		if len(paths) != len(wantContains) {
			t.Fatalf("got %d, want %d: %v", len(paths), len(wantContains), paths)
		}
	})
	t.Run("dvr_with_stream", func(t *testing.T) {
		paths := DeterministicPathsForAsset("/base", AssetKey{Type: "dvr", Hash: "dh"}, "", "streamA", []string{"seg1.ts"})
		// Both flat and nested layouts present so cleanup pins whichever
		// the writer chose; freeze uses the nested form.
		for _, want := range []string{
			"/base/dvr/dh/segments/seg1.ts",
			"/base/dvr/streamA/dh/segments/seg1.ts",
		} {
			found := false
			for _, p := range paths {
				if p == want {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("dvr_with_stream missing %q in %v", want, paths)
			}
		}
	})
	t.Run("clip_with_stream", func(t *testing.T) {
		paths := DeterministicPathsForAsset("/base", AssetKey{Type: "clip", Hash: "ch"}, ".mp4", "streamA", nil)
		// Clip layout is stream-nested only — the clip's source stream
		// name is required to build the path, so the flat
		// /base/clips/<hash>.<ext> shape never appears.
		for _, want := range []string{
			"/base/clips/streamA/ch.mp4",
			"/base/clips/streamA/ch.mp4.dtsh",
		} {
			found := false
			for _, p := range paths {
				if p == want {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("clip_with_stream missing %q in %v", want, paths)
			}
		}
		// And the flat path is gone.
		for _, p := range paths {
			if p == "/base/clips/ch.mp4" {
				t.Errorf("clip_with_stream: flat clip path leaked: %v", paths)
			}
		}
	})
	t.Run("clip_without_stream", func(t *testing.T) {
		// No stream → no protectable paths (the relay layout requires
		// the stream namespace).
		paths := DeterministicPathsForAsset("/base", AssetKey{Type: "clip", Hash: "ch"}, ".mp4", "", nil)
		if paths != nil {
			t.Errorf("clip_without_stream: expected nil, got %v", paths)
		}
	})
	t.Run("empty_inputs", func(t *testing.T) {
		if got := DeterministicPathsForAsset("", AssetKey{Type: "vod", Hash: "x"}, ".mkv", "", nil); got != nil {
			t.Errorf("empty basePath should return nil; got %v", got)
		}
		if got := DeterministicPathsForAsset("/base", AssetKey{Type: "vod", Hash: ""}, ".mkv", "", nil); got != nil {
			t.Errorf("empty hash should return nil; got %v", got)
		}
	})
}
