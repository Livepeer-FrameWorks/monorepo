package pullsource

import (
	"fmt"
	"net/netip"
	"net/url"
	"strings"
)

// ValidateURI checks whether raw is a supported MistServer pull-input URI.
func ValidateURI(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("parse source_uri: %w", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme == "" {
		return fmt.Errorf("source_uri scheme is required")
	}
	if !hostAllowed(parsed) {
		return fmt.Errorf("source_uri host is not allowed")
	}

	path := strings.ToLower(parsed.Path)
	switch scheme {
	case "rtsp", "srt", "rist", "dtsc":
		return nil
	case "http-ts", "https-ts", "tsudp":
		return nil
	case "http-hls", "https-hls":
		return nil
	case "http", "https":
		switch {
		case strings.HasSuffix(path, ".m3u8") || strings.HasSuffix(path, ".m3u"):
			return nil
		case strings.HasSuffix(path, ".ts"):
			return nil
		case strings.HasSuffix(path, ".mkv") || strings.HasSuffix(path, ".webm"):
			return nil
		default:
			return fmt.Errorf("http(s) pull source must end in .m3u8, .m3u, .ts, .mkv, or .webm")
		}
	default:
		return fmt.Errorf("unsupported pull source scheme %q", scheme)
	}
}

func Validate(raw string) bool {
	return ValidateURI(raw) == nil
}

func Redact(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" {
		return "pull_upstream"
	}
	if parsed.Host == "" {
		return strings.ToLower(parsed.Scheme) + "://"
	}
	return strings.ToLower(parsed.Scheme) + "://" + parsed.Host
}

func hostAllowed(parsed *url.URL) bool {
	switch strings.ToLower(parsed.Scheme) {
	case "tsudp":
		return udpHostAllowed(parsed)
	case "http", "https", "http-hls", "https-hls", "http-ts", "https-ts", "rtsp", "srt", "rist", "dtsc":
	default:
		return true
	}

	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "" {
		return false
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return false
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() &&
			!ip.IsLinkLocalMulticast() && !ip.IsUnspecified() && !ip.IsMulticast()
	}
	return true
}

func udpHostAllowed(parsed *url.URL) bool {
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "" {
		return false
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return false
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return !ip.IsLoopback() && !ip.IsLinkLocalUnicast() &&
			!ip.IsLinkLocalMulticast() && !ip.IsUnspecified()
	}
	return true
}
