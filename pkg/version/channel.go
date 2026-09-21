package version

import (
	"fmt"
	"regexp"
	"strings"
)

// Channel is a release channel. The release workflow, the CLI, and the release
// catalog all classify tags through ChannelForTag so they cannot disagree about
// where a tag is published.
type Channel string

const (
	// ChannelStable carries plain vX.Y.Z tags.
	ChannelStable Channel = "stable"
	// ChannelCandidate follows the newest release, including prereleases.
	ChannelCandidate Channel = "candidate"
	// ChannelRC carries every prerelease tag (vX.Y.Z-rc1, vX.Y.Z-beta.2, ...).
	ChannelRC Channel = "rc"
)

var releaseTagPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

// Channels lists every release channel in display order.
func Channels() []Channel {
	return []Channel{ChannelStable, ChannelCandidate, ChannelRC}
}

// ParseChannel validates a channel name.
func ParseChannel(name string) (Channel, error) {
	for _, c := range Channels() {
		if string(c) == name {
			return c, nil
		}
	}
	return "", fmt.Errorf("unknown release channel %q (want stable, candidate or rc)", name)
}

// ChannelForTag classifies a release tag. Any SemVer prerelease identifier
// selects rc; build metadata alone does not. Malformed tags are rejected.
func ChannelForTag(tag string) (Channel, error) {
	m := releaseTagPattern.FindStringSubmatch(tag)
	if m == nil {
		return "", fmt.Errorf("%q is not a release tag (want vX.Y.Z[-prerelease][+build])", tag)
	}
	prerelease := m[4]
	if prerelease == "" {
		return ChannelStable, nil
	}
	for ident := range strings.SplitSeq(prerelease, ".") {
		if len(ident) > 1 && ident[0] == '0' && isNumeric(ident) {
			return "", fmt.Errorf("%q has a numeric prerelease identifier with a leading zero", tag)
		}
	}
	return ChannelRC, nil
}

// ImageTrack is the moving container image tag a channel's releases update.
func (c Channel) ImageTrack() string {
	switch c {
	case ChannelStable:
		return "latest"
	case ChannelRC:
		return "rc"
	default:
		return ""
	}
}

func isNumeric(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
