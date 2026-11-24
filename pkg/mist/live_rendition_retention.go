package mist

import (
	"encoding/json"
	"strings"
)

const (
	h264RetentionFilter     = ",|h264"
	liveH264RetentionFilter = h264RetentionFilter + ",!bframes"
)

// ConstrainRenditionRetention keeps an original only when it supplies the
// requested H264 codec. Live playback also requires no frame reordering;
// offline H264 retention can preserve B-frames.
func ConstrainRenditionRetention(processesJSON, workload string) string {
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
		if workload == WorkloadVOD {
			if strings.HasSuffix(selector, h264RetentionFilter) {
				return
			}
			profile["track_retain"] = selector + h264RetentionFilter
		} else if strings.HasSuffix(selector, h264RetentionFilter) {
			profile["track_retain"] = selector + ",!bframes"
		} else {
			profile["track_retain"] = selector + liveH264RetentionFilter
		}
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
