package mist

import "testing"

func TestCanonicalCodecName(t *testing.T) {
	cases := map[string]string{
		"libx264": "h264", "h264_nvenc": "h264", "H264": "h264", "h264_videotoolbox": "h264",
		"libx265": "hevc", "hevc_qsv": "hevc", "h265": "hevc",
		"libsvtav1": "av1", "av1_nvenc": "av1", "libaom-av1": "av1",
		"libvpx-vp9": "vp9", "libopus": "opus", "aac": "aac", "libfdk_aac": "aac",
		"mjpeg": "jpeg", "none": "none", " Custom ": "custom", "": "",
	}
	for in, want := range cases {
		if got := CanonicalCodecName(in); got != want {
			t.Errorf("CanonicalCodecName(%q) = %q, want %q", in, got, want)
		}
	}
}
