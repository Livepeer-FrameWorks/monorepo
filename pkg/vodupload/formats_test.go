package vodupload

import "testing"

func TestUploadFormats(t *testing.T) {
	for _, extension := range []string{"mp4", "MOV", "mkv", "webm", "ts", "m2ts", "mts", "avi", "flv", "m4v"} {
		if err := ValidateFilename("video." + extension); err != nil {
			t.Errorf("%s: %v", extension, err)
		}
	}
	for _, filename := range []string{"", "video", "payload.xyz", "video.mp4.exe", "list.m3u8", "list.m3u", "video.mp4 ", ".mp3"} {
		if err := ValidateFilename(filename); err == nil {
			t.Errorf("%q accepted", filename)
		}
	}
	for _, extension := range UploadExtensions() {
		if RelaySafe(extension) == NeedsStaging(extension) {
			t.Errorf("%s must have exactly one input route", extension)
		}
	}
	list := RelayExtensions()
	list[0] = "exe"
	if !RelaySafe(".mp4") || RelaySafe("exe") {
		t.Fatal("caller mutated canonical formats")
	}
}
