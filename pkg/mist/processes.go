package mist

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

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
