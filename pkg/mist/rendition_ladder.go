package mist

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ExpectedRendition describes the output geometry of a video process. ShortSide
// is the ladder dimension in both landscape and portrait orientations.
type ExpectedRendition struct {
	Name, Process            string
	Width, Height, ShortSide int
}

// ExpectedVideoRenditions applies Mist's source inhibitors and sizing rules to
// every Livepeer profile and local AV video process. Invalid intent fails closed.
func ExpectedVideoRenditions(processesJSON string, source SourceMediaInfo) ([]ExpectedRendition, error) {
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
		if process != "Livepeer" && processTrackInhibited(proc, source) {
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
			if processTrackInhibited(proc, source) {
				continue
			}
			for _, p := range NormalizeLivepeerProfiles(profiles, source) {
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
				name := ""
				if value, ok := p["name"].(string); ok {
					name = value
				}
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
			for key, value := range map[string]*string{"resolution": &resolution, "x-LSP-name": &name} {
				if raw := proc[key]; len(raw) > 0 {
					if parseErr := json.Unmarshal(raw, value); parseErr != nil {
						return nil, fmt.Errorf("invalid AV %s: %w", key, parseErr)
					}
				}
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
