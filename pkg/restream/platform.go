package restream

import "strings"

var supportedPlatforms = map[string]struct{}{
	"twitch":   {},
	"youtube":  {},
	"facebook": {},
	"kick":     {},
	"x":        {},
	"custom":   {},
}

// NormalizePlatform returns the bounded platform dimension used by API,
// analytics, and billing boundaries. Empty input is the supported custom
// platform; unknown input is mapped to custom with ok=false.
func NormalizePlatform(value string) (platform string, ok bool) {
	platform = strings.ToLower(strings.TrimSpace(value))
	if platform == "" {
		return "custom", true
	}
	if _, ok := supportedPlatforms[platform]; !ok {
		return "custom", false
	}
	return platform, true
}
