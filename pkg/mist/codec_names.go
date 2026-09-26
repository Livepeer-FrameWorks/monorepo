package mist

import "strings"

// hardwareEncoderSuffixes are the FFmpeg suffixes for hardware encoder
// variants of a codec (h264_nvenc, hevc_qsv, av1_vaapi, ...).
var hardwareEncoderSuffixes = []string{"_nvenc", "_qsv", "_vaapi", "_videotoolbox", "_amf", "_v4l2m2m", "_mf", "_omx", "_rkmpp", "_mediacodec", "_cuvid"}

var encoderCodecs = map[string]string{
	"x264": "h264", "x264rgb": "h264", "openh264": "h264", "h264": "h264", "avc": "h264",
	"x265": "hevc", "hevc": "hevc", "h265": "hevc", "kvazaar": "hevc",
	"svtav1": "av1", "aom": "av1", "aom-av1": "av1", "rav1e": "av1", "dav1d": "av1", "av1": "av1",
	"vpx-vp9": "vp9", "vp9": "vp9", "vpx-vp8": "vp8", "vp8": "vp8",
	"fdk_aac": "aac", "aac": "aac",
	"opus":  "opus",
	"mjpeg": "jpeg", "jpeg": "jpeg",
}

// CanonicalCodecName maps a codec or FFmpeg encoder name (as MistProcAV
// reports it: libx264, h264_nvenc, libsvtav1, ...) to the codec name billing
// selectors and analytics use (h264, hevc, av1, vp9, aac, opus, jpeg).
// Unknown names are returned lowercased and trimmed.
func CanonicalCodecName(name string) string {
	codec := strings.ToLower(strings.TrimSpace(name))
	for _, suffix := range hardwareEncoderSuffixes {
		if trimmed, ok := strings.CutSuffix(codec, suffix); ok {
			codec = trimmed
			break
		}
	}
	codec = strings.TrimPrefix(codec, "lib")
	if canonical, ok := encoderCodecs[codec]; ok {
		return canonical
	}
	return codec
}
