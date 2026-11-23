package mist

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ExpectedRendition describes the output geometry of a video process. ShortSide
// is the ladder dimension in both landscape and portrait orientations. A
// Retained rung starts no process: original track TrackIndex is the rendition
// and Width/Height are that track's.
type ExpectedRendition struct {
	Name, Process            string
	Width, Height, ShortSide int
	Retained                 bool
	TrackIndex               int
}

// ExpectedVideoRenditions applies Mist's retention selectors, source inhibitors
// and sizing rules to every Livepeer profile and local AV video process.
// Source is the transcode source (TranscodeSourceIndex of tracks), the only
// track a rung's size inhibitor is judged against; tracks are the stream's
// video tracks, and without them no rung is retained. Invalid intent fails
// closed.
func ExpectedVideoRenditions(processesJSON string, source SourceMediaInfo, tracks ...TrackInfo) ([]ExpectedRendition, error) {
	var processes []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(processesJSON), &processes); err != nil {
		return nil, fmt.Errorf("parse process config: %w", err)
	}
	var out []ExpectedRendition
	w, h := source.DisplaySize()
	for _, proc := range processes {
		var process string
		if err := json.Unmarshal(proc["process"], &process); err != nil {
			return nil, fmt.Errorf("missing or invalid process name")
		}
		_, retainable := proc["track_retain"]
		if process != "Livepeer" && !retainable && processTrackInhibited(proc, source) {
			continue
		}
		switch process {
		case "Livepeer":
			var profiles []LivepeerJSONProfile
			if err := json.Unmarshal(proc["target_profiles"], &profiles); err != nil || len(profiles) == 0 {
				return nil, fmt.Errorf("missing, empty or invalid Livepeer target_profiles")
			}
			for _, profile := range profiles {
				if profile == nil {
					return nil, fmt.Errorf("null Livepeer profile")
				}
			}
			inhibited := processTrackInhibited(proc, source)
			for _, profile := range profiles {
				name := ""
				if value, ok := profile["name"].(string); ok {
					name = value
				}
				selector, isString := profile["track_retain"].(string)
				if _, present := profile["track_retain"]; present && !isString {
					return nil, fmt.Errorf("invalid Livepeer track_retain")
				}
				bitrate, _ := livepeerProfileInt(profile, "bitrate")
				track, retained, err := retainedTrack(selector, int64(bitrate), tracks)
				if err != nil {
					return nil, err
				}
				if retained {
					out = append(out, retainedRendition(name, process, track))
					continue
				}
				if inhibited {
					continue
				}
				normalized := NormalizeLivepeerProfiles([]LivepeerJSONProfile{profile}, source)
				if len(normalized) == 0 {
					continue
				}
				p := normalized[0]
				pw, _ := livepeerProfileInt(p, "width")
				ph, _ := livepeerProfileInt(p, "height")
				if w <= 0 || h <= 0 || pw <= 0 || ph <= 0 {
					return nil, fmt.Errorf("unknown Livepeer rendition geometry")
				}
				profile := ""
				if value, ok := p["profile"].(string); ok {
					profile = value
				}
				minW, minH, maxDim := 146, 50, 4096
				if strings.HasPrefix(profile, "H265") || strings.HasPrefix(profile, "HEVC") || strings.HasPrefix(profile, "VP9") || strings.HasPrefix(profile, "AV1") {
					minW, minH, maxDim = 132, 40, 8192
				}
				// LPMS clamps the requested box, fits the source aspect to its
				// long side, then rounds both dimensions down to even pixels.
				d := max(clampDimension(pw, minW, maxDim), clampDimension(ph, minH, maxDim))
				if w >= h {
					pw, ph = d, d*h/w
					if ph < minH {
						pw, ph = minH*w/h, minH
					}
				} else {
					pw, ph = d*w/h, d
					if pw < minW {
						pw, ph = minW, minW*h/w
					}
				}
				pw, ph = pw/2*2, ph/2*2
				out = append(out, ExpectedRendition{Name: name, Process: process, Width: pw, Height: ph, ShortSide: min(pw, ph)})
			}
		case "AV":
			var codec, resolution, name string
			if raw := proc["codec"]; len(raw) > 0 {
				if parseErr := json.Unmarshal(raw, &codec); parseErr != nil {
					return nil, fmt.Errorf("invalid AV codec: %w", parseErr)
				}
			}
			if !isAVVideoCodec(codec) {
				continue
			}
			var selector string
			for key, value := range map[string]*string{"resolution": &resolution, "x-LSP-name": &name, "track_retain": &selector} {
				if raw := proc[key]; len(raw) > 0 {
					if parseErr := json.Unmarshal(raw, value); parseErr != nil {
						return nil, fmt.Errorf("invalid AV %s: %w", key, parseErr)
					}
				}
			}
			var bitrate float64
			if raw := proc["bitrate"]; selector != "" && len(raw) > 0 {
				if parseErr := json.Unmarshal(raw, &bitrate); parseErr != nil {
					return nil, fmt.Errorf("invalid AV bitrate: %w", parseErr)
				}
			}
			track, retained, err := retainedTrack(selector, int64(bitrate), tracks)
			if err != nil {
				return nil, err
			}
			if retained {
				out = append(out, retainedRendition(name, process, track))
				continue
			}
			if processTrackInhibited(proc, source) {
				continue
			}
			pw, ph := w, h
			if resolution != "" {
				parts := strings.Split(resolution, "x")
				if len(parts) != 2 {
					return nil, fmt.Errorf("invalid AV resolution %q", resolution)
				}
				var errW, errH error
				if parts[0] != "" {
					pw, errW = strconv.Atoi(parts[0])
				} else {
					pw = 0
				}
				if parts[1] != "" {
					ph, errH = strconv.Atoi(parts[1])
				} else {
					ph = 0
				}
				if errW != nil || errH != nil || pw < 0 || ph < 0 {
					return nil, fmt.Errorf("invalid AV resolution %q", resolution)
				}
				if pw == 0 && ph == 0 {
					pw, ph = w, h
				}
				if w <= 0 || h <= 0 {
					return nil, fmt.Errorf("unknown AV source geometry")
				}
				if pw == 0 {
					if w < h {
						pw, ph = ph, h*ph/w
					} else {
						pw = w * ph / h
					}
				} else if ph == 0 {
					ph = h * pw / w
				}
				pw, ph = evenAVDimension(pw), evenAVDimension(ph)
			}
			if pw <= 0 || ph <= 0 {
				return nil, fmt.Errorf("unknown AV rendition geometry")
			}
			out = append(out, ExpectedRendition{Name: name, Process: process, Width: pw, Height: ph, ShortSide: min(pw, ph)})
		}
	}
	return out, nil
}

func retainedRendition(name, process string, track TrackInfo) ExpectedRendition {
	return ExpectedRendition{
		Name: name, Process: process, Width: track.Width, Height: track.Height,
		ShortSide: min(track.Width, track.Height), Retained: true, TrackIndex: track.Index,
	}
}

func evenAVDimension(v int) int {
	if v > 2 {
		return v / 2 * 2
	}
	return v
}

func clampDimension(v, low, high int) int { return min(max(v, low), high) }

func isAVVideoCodec(codec string) bool {
	switch strings.ToUpper(codec) {
	case "", "H264", "H265", "HEVC", "AV1", "VP8", "VP9", "I420", "NV12", "YUYV", "UYVY":
		return true
	}
	return false
}

// RenditionShortSideMatches absorbs codec rounding without merging adjacent rungs.
func RenditionShortSideMatches(width, height, expected int) bool {
	if width <= 0 || height <= 0 || expected <= 0 {
		return false
	}
	delta := min(width, height) - expected
	return delta >= -max(32, expected/20) && delta <= max(32, expected/20)
}

// EnsureRotatedSourceRendition renders a rotated source even when every ladder
// rung is inhibited. Passthrough alone cannot carry orientation in HLS-TS.
func EnsureRotatedSourceRendition(processesJSON string, source SourceMediaInfo) (string, error) {
	if source.Rotation == 0 && !source.Mirror {
		return processesJSON, nil
	}
	expected, err := ExpectedVideoRenditions(processesJSON, source)
	if err != nil {
		return "", err
	}
	if len(expected) > 0 {
		return processesJSON, nil
	}
	w, h := source.DisplaySize()
	if w <= 0 || h <= 0 {
		return "", fmt.Errorf("rotated source dimensions unavailable")
	}
	var processes []map[string]interface{}
	if parseErr := json.Unmarshal([]byte(processesJSON), &processes); parseErr != nil {
		return "", parseErr
	}
	processes = append(processes, map[string]interface{}{
		"process": "AV", "codec": "H264", "resolution": fmt.Sprintf("%dx%d", w, h),
		"bitrate": 900000, "source_mask": 4, "restart_type": "disabled",
		"track_select": "video=maxbps&audio=none&subtitle=none&meta=none",
		"x-LSP-name":   "Upright source",
	})
	encoded, err := json.Marshal(processes)
	return string(encoded), err
}
