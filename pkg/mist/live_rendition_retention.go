package mist

import (
	"encoding/json"
	"strings"
)

const liveH264RetentionFilter = ",|h264,!bframes"

// ConstrainLiveRenditionRetention keeps an original in place of a live H264
// rendition only when Mist's WebRTC output can select it. An incompatible
// original still feeds the encoder; retaining it would remove the stream's
// compatible video output.
// Offline processing keeps its own retention contract.
func ConstrainLiveRenditionRetention(processesJSON string) string {
	var processes []map[string]any
	if err := json.Unmarshal([]byte(processesJSON), &processes); err != nil {
		return processesJSON
	}
	changed := false
	constrain := func(profile map[string]any) {
		selector, ok := profile["track_retain"].(string)
		if !ok || strings.TrimSpace(selector) == "" || strings.HasSuffix(selector, liveH264RetentionFilter) {
			return
		}
		profile["track_retain"] = selector + liveH264RetentionFilter
		changed = true
	}
	for _, process := range processes {
		switch process["process"] {
		case "Livepeer":
			profiles, ok := process["target_profiles"].([]any)
			if !ok {
				continue
			}
			for _, value := range profiles {
				profile, ok := value.(map[string]any)
				if ok && livepeerProfileUsesH264(profile) {
					constrain(profile)
				}
			}
		case "AV":
			codec, ok := process["codec"].(string)
			if ok && strings.EqualFold(codec, "H264") {
				constrain(process)
			}
		}
	}
	if !changed {
		return processesJSON
	}
	encoded, err := json.Marshal(processes)
	if err != nil {
		return processesJSON
	}
	return string(encoded)
}

func livepeerProfileUsesH264(profile map[string]any) bool {
	if encoder, ok := nonEmptyString(profile["encoder"]); ok {
		return strings.EqualFold(encoder, "H264")
	}
	if name, ok := nonEmptyString(profile["profile"]); ok {
		return strings.HasPrefix(strings.ToUpper(name), "H264")
	}
	return true
}
