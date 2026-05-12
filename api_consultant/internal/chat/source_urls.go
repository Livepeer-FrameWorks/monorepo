package chat

import (
	"net/url"
	"strings"
)

func isHTTPURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https")
}

func sourceCitationParts(source Source) (string, string, bool) {
	label := strings.TrimSpace(source.Title)
	rawURL := strings.TrimSpace(source.URL)
	if isHTTPURL(rawURL) {
		if label == "" {
			label = rawURL
		}
		return label, rawURL, true
	}
	return label, "", label != ""
}
