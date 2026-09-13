package control

import "github.com/Livepeer-FrameWorks/monorepo/pkg/hls"

func dvrSegmentWallClock(manifest, segmentName string) (startMs, endMs, durationMs int64, ok bool) {
	parsed, err := hls.Parse(manifest)
	if err != nil || parsed == nil {
		return 0, 0, 0, false
	}
	var nextClockMs int64
	for _, segment := range parsed.Segments {
		duration := int64(segment.Duration * 1000)
		start := segment.ProgramDateTimeMs
		if start <= 0 {
			start = nextClockMs
		}
		if start > 0 && duration > 0 {
			nextClockMs = start + duration
		} else {
			nextClockMs = 0
		}
		if segment.Name == segmentName {
			if start <= 0 || duration <= 0 {
				return 0, 0, 0, false
			}
			return start, start + duration, duration, true
		}
	}
	return 0, 0, 0, false
}
