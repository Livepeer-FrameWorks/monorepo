package mist

import (
	"encoding/json"
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
// AV process entry with the same codec, bitrate, height, and fps.
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
			}
			if bitrate, ok := prof["bitrate"].(float64); ok {
				av["bitrate"] = int(bitrate)
			}
			if height, ok := prof["height"].(float64); ok {
				av["height"] = int(height)
			}
			if fps, ok := prof["fps"].(float64); ok {
				av["fps"] = int(fps)
			}
			if inhibit, ok := prof["track_inhibit"].(string); ok {
				av["track_inhibit"] = inhibit
			}
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
