package leases

import "strings"

// Stream-name prefixes Mist hands us. Constants keep this in lockstep
// with the Foghorn-side definitions even though they live in different
// modules.
const (
	VodPrefix        = "vod+"
	DvrPrefix        = "dvr+"
	LivePrefix       = "live+"
	ProcessingPrefix = "processing+"
	PullPrefix       = "pull+"
)

// ParseDVRRollingPlaybackID returns the dvr_internal_name token
// encoded in a "dvr+<dvr_internal_name>" stream name. The bool is
// false when streamName does not start with the DVR prefix or the
// token is empty after trimming. Only the rolling-DVR surface uses
// dvr+; finalized chapter artifacts are served via vod+.
func ParseDVRRollingPlaybackID(streamName string) (string, bool) {
	if !strings.HasPrefix(streamName, DvrPrefix) {
		return "", false
	}
	token := strings.TrimSpace(strings.TrimPrefix(streamName, DvrPrefix))
	if token == "" {
		return "", false
	}
	return token, true
}

// ParseVODInternalName returns the internal_name encoded in a "vod+<name>"
// stream name. The bool is false when streamName does not start with vod+
// or the suffix is empty.
//
// NOTE: this is the Mist internal_name, NOT the artifact_hash. Foghorn
// resolves internal_name → artifact_hash via a separate Commodore lookup.
func ParseVODInternalName(streamName string) (string, bool) {
	if !strings.HasPrefix(streamName, VodPrefix) {
		return "", false
	}
	name := strings.TrimSpace(strings.TrimPrefix(streamName, VodPrefix))
	if name == "" {
		return "", false
	}
	return name, true
}
