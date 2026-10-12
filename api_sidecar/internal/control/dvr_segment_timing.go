package control

import "github.com/Livepeer-FrameWorks/monorepo/pkg/hls"

// dvrSegmentClock is one playlist entry's ledger timing. startMs is 0 when
// the playlist gives the entry no wall-clock anchor.
type dvrSegmentClock struct {
	name       string
	startMs    int64
	durationMs int64
}

// dvrPlaylistClocks times every entry of a DVR playlist the way the ledger
// records it. The trigger path and reconciliation both use it, so a segment
// gets the same timing whichever path records it first.
func dvrPlaylistClocks(parsed *hls.Manifest) []dvrSegmentClock {
	if parsed == nil {
		return nil
	}
	clocks := make([]dvrSegmentClock, 0, len(parsed.Segments))
	var nextClockMs int64
	for _, segment := range parsed.Segments {
		duration := int64(segment.Duration * 1000)
		start := segment.ProgramDateTimeMs
		if start <= 0 {
			start = nextClockMs
		}
		// One recording's segments cannot overlap. Mist can stamp a segment
		// earlier than the previous one ended when its clock offset shifts
		// (a publisher flushing its encoder at end of stream); the media is
		// continuous, so the segment starts where the previous one ended.
		if nextClockMs > 0 && start > 0 && start < nextClockMs {
			start = nextClockMs
		}
		if start > 0 && duration > 0 {
			nextClockMs = start + duration
		} else {
			nextClockMs = 0
		}
		clocks = append(clocks, dvrSegmentClock{name: segment.Name, startMs: start, durationMs: duration})
	}
	return clocks
}

func dvrSegmentWallClock(manifest, segmentName string) (startMs, endMs, durationMs int64, ok bool) {
	parsed, err := hls.Parse(manifest)
	if err != nil || parsed == nil {
		return 0, 0, 0, false
	}
	for _, clock := range dvrPlaylistClocks(parsed) {
		if clock.name == segmentName {
			if clock.startMs <= 0 || clock.durationMs <= 0 {
				return 0, 0, 0, false
			}
			return clock.startMs, clock.startMs + clock.durationMs, clock.durationMs, true
		}
	}
	return 0, 0, 0, false
}
