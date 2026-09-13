package control

import (
	"testing"
	"time"
)

func TestDVRSegmentTimingUsesPlaylistUnixClock(t *testing.T) {
	const manifest = "#EXTM3U\n#EXT-X-PROGRAM-DATE-TIME:2026-09-12T22:00:00.000Z\n#EXTINF:6.021,\nsegments/first.ts\n#EXTINF:6,\nsegments/second.ts\n#EXT-X-PROGRAM-DATE-TIME:2026-09-12T22:01:00.000Z\n#EXTINF:3,\nsegments/third.ts\n"
	base := time.Date(2026, 9, 12, 22, 0, 0, 0, time.UTC).UnixMilli()
	for _, tc := range []struct {
		name            string
		start, duration int64
	}{
		{"first.ts", base, 6021},
		{"second.ts", base + 6021, 6000},
		{"third.ts", base + 60000, 3000},
	} {
		start, end, duration, ok := dvrSegmentWallClock(manifest, tc.name)
		if !ok || start != tc.start || end != tc.start+tc.duration || duration != tc.duration {
			t.Fatalf("%s: got %d..%d duration=%d anchored=%v", tc.name, start, end, duration, ok)
		}
	}
}

func TestDVRSegmentTimingNeverInventsMissingClock(t *testing.T) {
	for _, manifest := range []string{
		"", "#EXTM3U\n#EXTINF:6,\nsegment.ts\n",
		"#EXTM3U\n#EXT-X-PROGRAM-DATE-TIME:invalid\n#EXTINF:6,\nsegment.ts\n",
		"#EXTM3U\n#EXT-X-PROGRAM-DATE-TIME:2026-09-12T22:00:00Z\n#EXTINF:6,\nother.ts\n",
	} {
		if _, _, _, ok := dvrSegmentWallClock(manifest, "segment.ts"); ok {
			t.Fatal("unanchored or absent segment received ledger timing")
		}
	}
}
