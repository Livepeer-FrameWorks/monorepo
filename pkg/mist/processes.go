package mist

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// LivepeerJSONProfile is the go-livepeer JSON profile shape embedded in
// MistServer Livepeer process configs.
type LivepeerJSONProfile map[string]interface{}

// SourceMediaInfo is the source video track metadata MistServer uses when it
// expands Livepeer target_profiles before sending them to go-livepeer.
type SourceMediaInfo struct {
	Width  int
	Height int
	FPS    float64
}

// StripLivepeerProcesses removes Livepeer process entries from a MistServer
// processes JSON array. Used when no Livepeer gateway is available in the cluster
// (self-hosted without transcoding). Audio transcode and thumbnail processes are kept.
func StripLivepeerProcesses(processesJSON string) string {
	var processes []map[string]interface{}
	if err := json.Unmarshal([]byte(processesJSON), &processes); err != nil {
		return processesJSON
	}
	var filtered []map[string]interface{}
	for _, p := range processes {
		if procType, ok := p["process"].(string); ok && procType == "Livepeer" {
			continue
		}
		filtered = append(filtered, p)
	}
	out, err := json.Marshal(filtered)
	if err != nil {
		return processesJSON
	}
	return string(out)
}

// ThumbsOnlyProcesses keeps only MistProcThumbs entries from a process config.
// Live-derived assets already carry their A/V renditions; their processing
// pass only needs fresh thumbnails for the asset's own timeline.
func ThumbsOnlyProcesses(processesJSON string) string {
	var processes []map[string]interface{}
	if err := json.Unmarshal([]byte(processesJSON), &processes); err != nil {
		return "[]"
	}
	filtered := make([]map[string]interface{}, 0, len(processes))
	for _, p := range processes {
		if procType, ok := p["process"].(string); ok && procType == "Thumbs" {
			filtered = append(filtered, p)
		}
	}
	if len(filtered) == 0 {
		return "[]"
	}
	out, err := json.Marshal(filtered)
	if err != nil {
		return "[]"
	}
	return string(out)
}

// ReplaceLivepeerWithLocal converts Livepeer process entries into equivalent
// local MistProcAV entries. Each Livepeer target_profile becomes a separate
// AV process entry using MistProcAV's native option names.
// Non-Livepeer entries are preserved as-is.
func ReplaceLivepeerWithLocal(processesJSON string) string {
	var processes []map[string]interface{}
	if err := json.Unmarshal([]byte(processesJSON), &processes); err != nil {
		return processesJSON
	}

	var result []map[string]interface{}
	for _, p := range processes {
		procType, ok := p["process"].(string)
		if !ok || procType != "Livepeer" {
			result = append(result, p)
			continue
		}

		profilesRaw, ok := p["target_profiles"]
		if !ok {
			continue
		}
		profiles, ok := profilesRaw.([]interface{})
		if !ok {
			continue
		}

		for _, profRaw := range profiles {
			prof, ok := profRaw.(map[string]interface{})
			if !ok {
				continue
			}
			av := map[string]interface{}{
				"process": "AV",
			}
			if profile, ok := prof["profile"].(string); ok {
				av["codec"] = livepeerProfileToCodec(profile)
				if localProfile := livepeerProfileToAVProfile(profile); localProfile != "" {
					av["profile"] = localProfile
				}
			}
			if bitrate, ok := prof["bitrate"].(float64); ok {
				av["bitrate"] = int(bitrate)
			}
			if resolution := livepeerProfileResolution(prof); resolution != "" {
				av["resolution"] = resolution
			}
			if fps, ok := prof["fps"].(float64); ok {
				if fps > 0 {
					av["framerate"] = int(math.Round(fps))
				}
			}
			if trackSelect, ok := prof["track_select"].(string); ok && trackSelect != "" {
				av["track_select"] = trackSelect
			} else {
				av["track_select"] = "video=maxbps&audio=none&subtitle=none"
			}
			if inhibit, ok := prof["track_inhibit"].(string); ok {
				av["track_inhibit"] = inhibit
			}
			copyProcessOption(av, prof, "inconsequential")
			copyProcessOption(av, prof, "exit_unmask")
			copyProcessOption(av, prof, "source_mask")
			copyProcessOption(av, prof, "target_mask")
			copyProcessOption(av, prof, "source_track")
			copyProcessOption(av, prof, "gopsize")
			if name, ok := prof["name"].(string); ok {
				av["x-LSP-name"] = "Local " + name
			}
			result = append(result, av)
		}
	}

	out, err := json.Marshal(result)
	if err != nil {
		return processesJSON
	}
	return string(out)
}

// HasLivepeerProcesses returns true if the config contains a Livepeer process entry.
func HasLivepeerProcesses(processesJSON string) bool {
	return strings.Contains(processesJSON, `"Livepeer"`)
}

// LivepeerProfilesFromProcessesJSON extracts the first Livepeer target_profiles
// entry and normalizes it to match MistProcLivepeer's request header.
func LivepeerProfilesFromProcessesJSON(processesJSON string, source SourceMediaInfo) []LivepeerJSONProfile {
	var processes []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(processesJSON), &processes); err != nil {
		return nil
	}
	for _, proc := range processes {
		var processName string
		if err := json.Unmarshal(proc["process"], &processName); err != nil || processName != "Livepeer" {
			continue
		}
		var profiles []LivepeerJSONProfile
		if err := json.Unmarshal(proc["target_profiles"], &profiles); err != nil {
			return nil
		}
		return NormalizeLivepeerProfiles(profiles, source)
	}
	return nil
}

// NormalizeLivepeerProfiles mirrors MistProcLivepeer's target_profiles mutation
// before it sets the Livepeer-Transcode-Configuration header.
func NormalizeLivepeerProfiles(profiles []LivepeerJSONProfile, source SourceMediaInfo) []LivepeerJSONProfile {
	if len(profiles) == 0 {
		return nil
	}
	out := make([]LivepeerJSONProfile, 0, len(profiles))
	for _, profile := range profiles {
		p := copyLivepeerProfile(profile)
		if _, ok := p["gop"]; !ok {
			p["gop"] = "0.0"
		}
		if !livepeerProfileNumberSet(p, "fps") {
			fpks := int(math.Round(source.FPS * 1000))
			if fpks == 0 {
				fpks = 25000
			}
			p["fps"] = fpks
			p["fpsDen"] = 1000
		}
		if _, ok := p["profile"]; !ok {
			p["profile"] = "H264High"
		}
		if source.Width > 0 && source.Height > 0 {
			width, hasWidth := livepeerProfileInt(p, "width")
			height, hasHeight := livepeerProfileInt(p, "height")
			switch {
			case (!hasWidth || width == 0) && (!hasHeight || height == 0):
				width = source.Width
				height = source.Height
			case !hasWidth || width == 0:
				if source.Width < source.Height {
					width = height
					height = source.Height * width / source.Width
				} else {
					width = source.Width * height / source.Height
				}
			case !hasHeight || height == 0:
				height = source.Height * width / source.Width
			}
			width = (width / 16) * 16
			height = (height / 16) * 16
			if width > 0 {
				p["width"] = width
			}
			if height > 0 {
				p["height"] = height
			}
		}
		if shouldInhibitLivepeerProfile(p, source) {
			continue
		}
		delete(p, "track_inhibit")
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ValidateProcessConfigShape checks that explicit MistServer process configs use
// option names consumed by the target process binary. Livepeer target_profiles are
// intentionally exempt: those use go-livepeer's profile schema and are converted
// separately only when local MistProcAV fallback is needed.
func ValidateProcessConfigShape(processesJSON string) error {
	var processes []map[string]interface{}
	if err := json.Unmarshal([]byte(processesJSON), &processes); err != nil {
		return fmt.Errorf("parse process config: %w", err)
	}
	for idx, proc := range processes {
		processName, ok := proc["process"].(string)
		if !ok || processName != "AV" {
			continue
		}
		for _, badKey := range []string{"height", "width", "fps"} {
			if _, ok := proc[badKey]; ok {
				return fmt.Errorf("process[%d] AV uses %q; MistProcAV expects resolution/framerate", idx, badKey)
			}
		}
		if profile, ok := proc["profile"].(string); ok {
			switch profile {
			case "", "high", "main", "baseline":
			default:
				return fmt.Errorf("process[%d] AV profile %q is not a local MistProcAV profile", idx, profile)
			}
		}
	}
	return nil
}

func livepeerProfileResolution(profile map[string]interface{}) string {
	if resolution, ok := profile["resolution"].(string); ok && resolution != "" {
		return resolution
	}
	width, widthOK := numberAsInt(profile["width"])
	height, heightOK := numberAsInt(profile["height"])
	if !heightOK || height <= 0 {
		return ""
	}
	if !widthOK || width <= 0 {
		width = evenInt(float64(height) * 16 / 9)
	}
	return fmt.Sprintf("%dx%d", width, height)
}

func numberAsInt(v interface{}) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(math.Round(n)), true
	case float32:
		return int(math.Round(float64(n))), true
	case int:
		return n, true
	case int64:
		return int(n), true
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return 0, false
		}
		return int(math.Round(f)), true
	default:
		return 0, false
	}
}

func evenInt(v float64) int {
	n := int(math.Round(v))
	if n%2 != 0 {
		n++
	}
	return n
}

func copyProcessOption(dst, src map[string]interface{}, key string) {
	if value, ok := src[key]; ok {
		dst[key] = value
	}
}

func copyLivepeerProfile(profile LivepeerJSONProfile) LivepeerJSONProfile {
	out := make(LivepeerJSONProfile, len(profile))
	for k, v := range profile {
		out[k] = v
	}
	return out
}

func livepeerProfileNumberSet(profile LivepeerJSONProfile, key string) bool {
	v, ok := livepeerProfileFloat(profile, key)
	return ok && v != 0
}

func livepeerProfileInt(profile LivepeerJSONProfile, key string) (int, bool) {
	v, ok := livepeerProfileFloat(profile, key)
	return int(v), ok
}

func livepeerProfileFloat(profile LivepeerJSONProfile, key string) (float64, bool) {
	switch v := profile[key].(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func shouldInhibitLivepeerProfile(profile LivepeerJSONProfile, source SourceMediaInfo) bool {
	if source.Width <= 0 || source.Height <= 0 {
		return false
	}
	raw, ok := profile["track_inhibit"].(string)
	if !ok || !strings.HasPrefix(raw, "video=<") {
		return false
	}
	dims := strings.TrimPrefix(raw, "video=<")
	parts := strings.SplitN(dims, "x", 2)
	if len(parts) != 2 {
		return false
	}
	maxWidth, errW := strconv.Atoi(parts[0])
	maxHeight, errH := strconv.Atoi(parts[1])
	if errW != nil || errH != nil {
		return false
	}
	return source.Width < maxWidth && source.Height < maxHeight
}

// livepeerProfileToCodec maps Livepeer profile names to MistProcAV codec names.
func livepeerProfileToCodec(profile string) string {
	switch {
	case strings.HasPrefix(profile, "H264"):
		return "H264"
	case strings.HasPrefix(profile, "VP9"):
		return "VP9"
	case strings.HasPrefix(profile, "VP8"):
		return "VP8"
	case strings.HasPrefix(profile, "AV1"):
		return "AV1"
	case strings.HasPrefix(profile, "H265"), strings.HasPrefix(profile, "HEVC"):
		return "H265"
	default:
		return profile
	}
}

func livepeerProfileToAVProfile(profile string) string {
	switch {
	case strings.Contains(profile, "ConstrainedHigh"), strings.Contains(profile, "High"):
		return "high"
	case strings.Contains(profile, "ConstrainedBaseline"), strings.Contains(profile, "Baseline"):
		return "baseline"
	case strings.Contains(profile, "Main"):
		return "main"
	default:
		return ""
	}
}
