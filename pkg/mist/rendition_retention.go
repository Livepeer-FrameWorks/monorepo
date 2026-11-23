package mist

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// TrackInfo is one video track of a stream as the rendition retention rule
// reads it. Codec is Mist's codec name, Bps is Mist's track bps (bytes per
// second), Original is true for a track no process produced and Image is true
// for still-image tracks.
type TrackInfo struct {
	Index         int
	Codec         string
	Width, Height int
	Bps           uint64
	Original      bool
	Image         bool
}

// RetainRung is one ladder rung: its short side in pixels and its target
// bitrate in bits per second.
type RetainRung struct {
	ShortSide int
	Bitrate   int64
}

// RungRetention is the outcome of one rung. Track is the original that
// retains it, or -1; Transcode is true when a process renders it.
type RungRetention struct {
	Track     int
	Transcode bool
}

// RetentionPlan is the retention rule applied to a stream: Source is the
// transcode source (-1 without one), Rungs follows the rung order it was given,
// and Masked lists the originals hidden from playback and the stored VOD.
type RetentionPlan struct {
	Source int
	Rungs  []RungRetention
	Masked []int
}

// mistKbpsBytes is what Mist's "kbps" selector unit means in track bps:
// 1024 bits per second, compared against bytes per second.
const mistKbpsBytes = 1024 / 8

// RetainSelector composes the track_retain selector of a rung: an original
// H264 or HEVC track whose short side is within max(32, N/20) of the rung and
// whose bitrate is within 0.5x to 2x of the rung's target. Mist compares
// strictly and in 1024-bit kbps, so the bounds are widened to the next whole
// kbps outside the band; the evaluator uses the same integers. It returns ""
// for a rung without a short side or a bitrate.
func RetainSelector(rung RetainRung) string {
	if rung.ShortSide <= 0 || rung.Bitrate <= 0 {
		return ""
	}
	window := max(32, rung.ShortSide/20)
	low, high := retainBandKbps(rung.Bitrate)
	return fmt.Sprintf("video=h264,hevc,|source,|>0x%d,|<0x%d,|>%dkbps,|<%dkbps",
		rung.ShortSide-window-1, rung.ShortSide+window+1, low, high)
}

// retainBandKbps returns the exclusive kbps bounds whose Mist comparison
// admits every track at 0.5x..2x of bitrate (bits per second).
func retainBandKbps(bitrate int64) (low, high uint64) {
	lowBytes := (uint64(bitrate) + 15) / 16
	highBytes := uint64(bitrate) / 4
	return (lowBytes - 1) / mistKbpsBytes, highBytes/mistKbpsBytes + 1
}

// TranscodeSourceIndex returns the track Mist transcodes from: the original
// continuous-video track with the highest bitrate (Mist's maxbps), the lowest
// index on a tie or while no original has a known bitrate. It returns -1 when
// there is no original continuous-video track.
func TranscodeSourceIndex(tracks []TrackInfo) int {
	best := -1
	var bestBps uint64
	for _, track := range sortedTracks(tracks) {
		if !track.Original || track.Image {
			continue
		}
		if best < 0 || track.Bps > bestBps {
			best, bestBps = track.Index, track.Bps
		}
	}
	return best
}

// PlanRetention applies the retention rule. A rung is retained by the
// original its selector picks whose bitrate is closest to the rung's target
// (the lowest index on a tie); otherwise it is transcoded when the source's
// short side reaches the rung. Originals that retain no rung, the source
// included, are masked once a transcoded rung exists to replace them.
func PlanRetention(tracks []TrackInfo, rungs []RetainRung) RetentionPlan {
	plan := RetentionPlan{Source: TranscodeSourceIndex(tracks), Rungs: make([]RungRetention, len(rungs))}
	sourceShort := 0
	for _, track := range tracks {
		if track.Index == plan.Source {
			sourceShort = min(track.Width, track.Height)
		}
	}
	retaining := map[int]bool{}
	transcodes := false
	for i, rung := range rungs {
		plan.Rungs[i].Track = -1
		// A composed selector always parses; a rung without one retains nothing.
		if track, ok, err := retainedTrack(RetainSelector(rung), rung.Bitrate, tracks); err == nil && ok {
			plan.Rungs[i].Track = track.Index
			retaining[track.Index] = true
			continue
		}
		if plan.Source >= 0 && sourceShort >= rung.ShortSide {
			plan.Rungs[i].Transcode = true
			transcodes = true
		}
	}
	if !transcodes {
		return plan
	}
	for _, track := range sortedTracks(tracks) {
		if track.Original && !track.Image && !retaining[track.Index] {
			plan.Masked = append(plan.Masked, track.Index)
		}
	}
	return plan
}

// retainedTrack evaluates a track_retain selector the way Mist selects tracks
// and picks the selected original whose bitrate is closest to the target
// bitrate (bits per second). An empty selector retains nothing; a selector
// outside the supported grammar is an error.
func retainedTrack(selector string, bitrate int64, tracks []TrackInfo) (TrackInfo, bool, error) {
	if selector == "" {
		return TrackInfo{}, false, nil
	}
	selected, err := selectRetainTracks(selector, sortedTracks(tracks))
	if err != nil {
		return TrackInfo{}, false, err
	}
	var best TrackInfo
	found := false
	var bestDist uint64
	for _, track := range selected {
		if !track.Original || track.Image {
			continue
		}
		dist := absDiff(track.Bps*8, uint64(max(bitrate, 0)))
		if !found || dist < bestDist {
			best, bestDist, found = track, dist, true
		}
	}
	return best, found, nil
}

func absDiff(a, b uint64) uint64 {
	if a > b {
		return a - b
	}
	return b - a
}

func sortedTracks(tracks []TrackInfo) []TrackInfo {
	out := append([]TrackInfo(nil), tracks...)
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out
}

// selectRetainTracks mirrors Util::pickTracks for the video selectors a
// track_retain uses: a comma list whose plain items add to the selection and
// whose "|" items narrow it, over codec names, "source", "all", short-side
// (">0xN") and area (">WxH") thresholds and bitrate thresholds in bps, kbps
// (1024) or mbps (1024*1024). Size and bitrate thresholds skip image tracks.
func selectRetainTracks(selector string, tracks []TrackInfo) ([]TrackInfo, error) {
	value, ok := strings.CutPrefix(selector, "video=")
	if !ok || value == "" || strings.Contains(value, "&") {
		return nil, fmt.Errorf("track_retain %q is not a single video selector", selector)
	}
	var result []TrackInfo
	for _, item := range strings.Split(value, ",") {
		narrow := strings.HasPrefix(item, "|")
		pool := tracks
		if narrow {
			item, pool = item[1:], result
		}
		match, err := retainTokenMatcher(strings.ToLower(item))
		if err != nil {
			return nil, fmt.Errorf("track_retain %q: %w", selector, err)
		}
		var picked []TrackInfo
		for _, track := range pool {
			if match(track) {
				picked = append(picked, track)
			}
		}
		if narrow {
			result = picked
			continue
		}
		result = unionTracks(result, picked)
	}
	return result, nil
}

func unionTracks(a, b []TrackInfo) []TrackInfo {
	seen := map[int]bool{}
	for _, track := range a {
		seen[track.Index] = true
	}
	for _, track := range b {
		if !seen[track.Index] {
			a = append(a, track)
			seen[track.Index] = true
		}
	}
	sort.Slice(a, func(i, j int) bool { return a[i].Index < a[j].Index })
	return a
}

func retainTokenMatcher(token string) (func(TrackInfo) bool, error) {
	switch {
	case token == "all" || token == "*":
		return func(TrackInfo) bool { return true }, nil
	case token == "source":
		return func(t TrackInfo) bool { return t.Original }, nil
	case strings.HasPrefix(token, ">") || strings.HasPrefix(token, "<"):
		greater := token[0] == '>'
		compare := func(v, threshold uint64) bool {
			if greater {
				return v > threshold
			}
			return v < threshold
		}
		if bytes, ok := parseMistBitrate(token[1:]); ok {
			return func(t TrackInfo) bool { return !t.Image && compare(t.Bps, bytes) }, nil
		}
		w, h, ok := strings.Cut(token[1:], "x")
		width, errW := strconv.ParseUint(w, 10, 32)
		height, errH := strconv.ParseUint(h, 10, 32)
		if !ok || errW != nil || errH != nil || height == 0 {
			return nil, fmt.Errorf("unsupported threshold %q", token)
		}
		if width == 0 {
			return func(t TrackInfo) bool {
				return !t.Image && compare(uint64(min(t.Width, t.Height)), height)
			}, nil
		}
		return func(t TrackInfo) bool {
			return !t.Image && compare(uint64(t.Width)*uint64(t.Height), width*height)
		}, nil
	case len(token) > 2 && strings.Trim(token, "abcdefghijklmnopqrstuvwxyz0123456789") == "" && strings.Trim(token, "0123456789") != "":
		return func(t TrackInfo) bool { return strings.ToLower(t.Codec) == token }, nil
	}
	return nil, fmt.Errorf("unsupported selector %q", token)
}

// parseMistBitrate converts a Mist bitrate threshold to bytes per second.
func parseMistBitrate(value string) (uint64, bool) {
	for _, unit := range []struct {
		suffix string
		scale  uint64
	}{{"mbps", 1024 * 1024}, {"kbps", 1024}, {"bps", 1}} {
		number, ok := strings.CutSuffix(value, unit.suffix)
		if !ok {
			continue
		}
		n, err := strconv.ParseUint(number, 10, 32)
		if err != nil || n == 0 {
			return 0, false
		}
		return n * unit.scale / 8, true
	}
	return 0, false
}
