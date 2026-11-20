package mist

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/transcode/contract"
)

// ParseTranscodeRenditions validates the catalog's MistProcAV rendition shape.
func ParseTranscodeRenditions(raw []byte) ([]contract.RenditionSpec, error) {
	var renditions []contract.RenditionSpec
	if err := json.Unmarshal(raw, &renditions); err != nil {
		return nil, err
	}
	if err := contract.ValidatePolicy(renditions); err != nil {
		return nil, err
	}
	return renditions, nil
}

// LPMSProfiles preserves catalog profiles, including their persisted selectors.
func LPMSProfiles(renditions []contract.RenditionSpec) []LivepeerJSONProfile {
	return lpmsProfiles(renditions, false)
}

// LadderProfiles gives named ladders short-side thresholds without changing
// explicit area selectors in independently persisted process configurations.
func LadderProfiles(renditions []contract.RenditionSpec) []LivepeerJSONProfile {
	return lpmsProfiles(renditions, true)
}

func lpmsProfiles(renditions []contract.RenditionSpec, shortSide bool) []LivepeerJSONProfile {
	out := make([]LivepeerJSONProfile, 0, len(renditions))
	for _, r := range renditions {
		p := LivepeerJSONProfile{"name": r.Name, "bitrate": r.Bitrate, "fps": 0}
		w, h, err := r.Requested()
		if err != nil {
			continue
		}
		if w > 0 {
			p["width"] = w
		}
		if h > 0 {
			p["height"] = h
		}
		if r.GOPSize == 1 {
			p["gop"] = "intra"
		} else if r.GOPSize > 1 {
			p["gop"] = strconv.FormatFloat(float64(r.GOPSize)/1000, 'f', -1, 64)
		}
		if r.Codec == "H264" {
			switch r.Profile {
			case "main":
				p["profile"] = "H264Main"
			case "baseline":
				p["profile"] = "H264Baseline"
			default:
				p["profile"] = "H264ConstrainedHigh"
			}
		} else {
			p["encoder"] = r.Codec
		}
		if r.TrackInhibit != "" {
			inhibit := r.TrackInhibit
			// Named height-only rungs use the short side in every orientation.
			// Preserve explicit operator area selectors on other profiles.
			catalogWidth := h * 16 / 9
			if h == 480 {
				catalogWidth = 850
			}
			if shortSide && w == 0 && h > 0 && inhibit == fmt.Sprintf("video=<%dx%d", catalogWidth, h) {
				inhibit = fmt.Sprintf("video=<0x%d", h)
			}
			p["track_inhibit"] = inhibit
		}
		out = append(out, p)
	}
	return out
}

// SetTranscodeLadder replaces only the video profiles, preserving routing and
// source masks. Audio and thumbnail processes keep their own policy.
func SetTranscodeLadder(processesJSON string, renditions []contract.RenditionSpec) (string, error) {
	if err := contract.ValidatePolicy(renditions); err != nil {
		return "", err
	}
	var processes []map[string]any
	if err := json.Unmarshal([]byte(processesJSON), &processes); err != nil {
		return "", fmt.Errorf("parse process config: %w", err)
	}
	changed := false
	for _, proc := range processes {
		if proc["process"] == "Livepeer" {
			proc["target_profiles"] = LadderProfiles(renditions)
			if proc["track_inhibit"] == "video=<640x360" {
				proc["track_inhibit"] = "video=<0x360"
			}
			changed = true
		}
	}
	if !changed {
		return processesJSON, nil
	}
	encoded, err := json.Marshal(processes)
	return string(encoded), err
}

// HasTranscodeProcesses reports whether a config carries a video ladder.
func HasTranscodeProcesses(processesJSON string) bool { return HasLivepeerProcesses(processesJSON) }
