package pullsource

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
)

// Class is the cluster-eligibility category of a pull-source URI.
//
//   - ClassBlocked: always-rejected regardless of cluster policy. Includes
//     unsupported schemes, loopback (127.0.0.0/8, ::1), link-local +
//     cloud-metadata (169.254.0.0/16 inc. 169.254.169.254), .internal,
//     frameworks.network, .localhost, .local, and (for non-tsudp schemes)
//     multicast literals.
//   - ClassPrivate: usable only on a media cluster whose
//     allow_private_pull_sources flag is true. RFC1918 / ULA literals, plus
//     non-link-local multicast on tsudp.
//   - ClassPublic: usable on any media cluster. Public IP literals or
//     hostnames (we do not resolve hostnames here; reachability of a
//     hostname that resolves to a private IP is the operator's
//     responsibility per docs/architecture/pull-streams.md).
type Class int

const (
	ClassBlocked Class = iota
	ClassPrivate
	ClassPublic
)

func (c Class) String() string {
	switch c {
	case ClassBlocked:
		return "blocked"
	case ClassPrivate:
		return "private"
	case ClassPublic:
		return "public"
	default:
		return "unknown"
	}
}

// Classify analyses a pull-source URI and returns its eligibility class. The
// returned error is non-nil only when Class == ClassBlocked and explains the
// rejection in human-readable form.
func Classify(raw string) (Class, error) {
	parsed, parseErr := url.Parse(strings.TrimSpace(raw))
	if parseErr != nil {
		return ClassBlocked, fmt.Errorf("parse source_uri: %w", parseErr)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme == "" {
		return ClassBlocked, errors.New("source_uri scheme is required")
	}
	if schemeErr := validateSchemePath(scheme, parsed); schemeErr != nil {
		return ClassBlocked, schemeErr
	}

	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "" {
		return ClassBlocked, errors.New("source_uri host is required")
	}

	// Always-blocked hostnames. These apply regardless of cluster policy.
	if host == "localhost" ||
		strings.HasSuffix(host, ".localhost") ||
		strings.HasSuffix(host, ".local") ||
		strings.HasSuffix(host, ".internal") ||
		strings.HasSuffix(host, "frameworks.network") {
		return ClassBlocked, fmt.Errorf("source_uri host %q is operator-internal or unreachable", host)
	}

	// netip.ParseAddr returns an error for hostnames; that is the signal we
	// use to distinguish "literal IP" from "DNS hostname". A hostname is
	// always classified as Public; reachability of a hostname that resolves
	// to a private IP from the media edge is the operator's responsibility
	// (see docs/architecture/pull-streams.md).
	ip, ipErr := netip.ParseAddr(host)
	if ipErr != nil {
		return ClassPublic, nil //nolint:nilerr // ipErr is the "this is a hostname" signal, not a failure
	}

	// Always-blocked literals.
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return ClassBlocked, fmt.Errorf("source_uri host %s is in the always-blocked range", ip)
	}

	// Multicast is only meaningful on tsudp; reject on every other scheme.
	if !strings.EqualFold(scheme, "tsudp") && ip.IsMulticast() {
		return ClassBlocked, fmt.Errorf("multicast destinations are only supported on tsudp")
	}

	if ip.IsPrivate() || ip.IsMulticast() {
		return ClassPrivate, nil
	}
	return ClassPublic, nil
}

// Validate returns nil iff Classify yields a class other than ClassBlocked.
// Convenience for callers that don't need the eligibility class.
func Validate(raw string) error {
	class, err := Classify(raw)
	if class == ClassBlocked {
		if err == nil {
			err = errors.New("source_uri rejected")
		}
		return err
	}
	return nil
}

// IsValid is a bool helper; returns true iff the URI parses to a non-blocked
// class. Useful as a runtime predicate where the rejection reason is not
// needed (e.g. scoring functions). Errcheck N/A: the bool is the contract.
func IsValid(raw string) bool {
	class, _ := Classify(raw) //nolint:errcheck // class encodes the rejection
	return class != ClassBlocked
}

// Redact returns "scheme://host" with credentials stripped. Used for logs and
// telemetry that should not leak in-URI auth.
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

// ClusterCapability is the slice of cluster state EligiblePullClusters needs.
// Bootstrap render builds these from the rendered manifest; runtime paths
// build them from Quartermaster ListClusters.
type ClusterCapability struct {
	ID                      string
	AllowPrivatePullSources bool
}

// EligiblePullClusters returns the candidate subset that can run a pull of
// the given class. ClassBlocked yields the empty set unconditionally;
// ClassPrivate filters to AllowPrivatePullSources=true; ClassPublic returns
// every candidate.
//
// This is the single eligibility chokepoint. Future cluster predicates
// (capacity, billing entitlement, region) extend this function so callers
// stay one-line; the per-call-site math never duplicates.
func EligiblePullClusters(class Class, candidates []ClusterCapability) []ClusterCapability {
	if class == ClassBlocked {
		return nil
	}
	out := make([]ClusterCapability, 0, len(candidates))
	for _, c := range candidates {
		if class == ClassPrivate && !c.AllowPrivatePullSources {
			continue
		}
		out = append(out, c)
	}
	return out
}

// validateSchemePath enforces the supported MistServer pull-input scheme +
// path-suffix matrix. Centralised here so Classify keeps its host logic
// linear.
func validateSchemePath(scheme string, parsed *url.URL) error {
	switch scheme {
	case "rtsp", "srt", "rist", "dtsc":
		return nil
	case "http-ts", "https-ts", "tsudp":
		return nil
	case "http-hls", "https-hls":
		return nil
	case "http", "https":
		path := strings.ToLower(parsed.Path)
		switch {
		case strings.HasSuffix(path, ".m3u8") || strings.HasSuffix(path, ".m3u"):
			return nil
		case strings.HasSuffix(path, ".ts"):
			return nil
		case strings.HasSuffix(path, ".mkv") || strings.HasSuffix(path, ".webm"):
			return nil
		default:
			return errors.New("http(s) pull source must end in .m3u8, .m3u, .ts, .mkv, or .webm")
		}
	default:
		return fmt.Errorf("unsupported pull source scheme %q", scheme)
	}
}
