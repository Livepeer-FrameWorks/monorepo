package leases

import "strings"

// Stream-name prefixes Mist hands us. Constants keep this in lockstep with
// the Foghorn-side definitions even though they live in different modules.
const (
	VodPrefix        = "vod+"
	DvrChapterPrefix = "dvr+"
	LivePrefix       = "live+"
	ProcessingPrefix = "processing+"
	PullPrefix       = "pull+"
)

// ParseDVRChapterPlaybackID returns the chapter_id encoded in a "dvr+<chapter>"
// stream name. The bool is false when streamName does not start with the DVR
// chapter prefix or the chapter id is empty after trimming.
func ParseDVRChapterPlaybackID(streamName string) (string, bool) {
	if !strings.HasPrefix(streamName, DvrChapterPrefix) {
		return "", false
	}
	chapterID := strings.TrimSpace(strings.TrimPrefix(streamName, DvrChapterPrefix))
	if chapterID == "" {
		return "", false
	}
	return chapterID, true
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
