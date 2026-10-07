package restream

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

const maxWebhookURLLength = 2048

// ErrInvalidWebhookURL wraps every rejection of a tenant-supplied webhook URL,
// including a host that does not exist. A resolver failure that may clear on
// retry is reported with ErrDestinationResolution instead.
var ErrInvalidWebhookURL = errors.New("invalid webhook url")

// WebhookDestinationPolicy is the destination policy for tenant-supplied
// webhook endpoints: Bosun's outbound webhooks and playback-auth webhooks.
// Operator restream CIDR exceptions never apply to it. allowPrivate admits
// private (RFC 1918 / ULA) addresses for an isolated cluster whose receivers
// live on its own network. It never opens the platform itself: loopback,
// link-local, cloud metadata, this host's own addresses, and private
// addresses routed into the service mesh (IsPlatformAddress) stay blocked
// either way, as do platform host names (isPlatformHostName).
func WebhookDestinationPolicy(allowPrivate bool) DestinationPolicy {
	policy := PublicDestinationPolicy()
	policy.AllowPrivate = allowPrivate
	policy.PlatformAddress = IsPlatformAddress
	return policy
}

// isPlatformHostName reports host names that only ever name the platform:
// the operator domain, the Privateer mesh namespace (*.internal, where every
// platform service is registered), and the local host. They are refused
// whether or not private destinations are allowed, because a customer
// receiver on an isolated network is never addressed through them.
func isPlatformHostName(host string) bool {
	switch {
	case host == "frameworks.network", strings.HasSuffix(host, ".frameworks.network"):
		return true
	case host == "internal", strings.HasSuffix(host, ".internal"):
		return true
	case host == "localhost", strings.HasSuffix(host, ".localhost"):
		return true
	}
	return false
}

func invalidWebhookURL(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidWebhookURL, fmt.Sprintf(format, args...))
}

// ValidateWebhookURL checks a webhook endpoint URL before it is stored and
// returns its normalized form: https, a host, no credentials or fragment, no
// platform host name, a host that resolves, and a destination the policy
// allows. A literal
// forbidden address and a host that resolves to one are both rejected. The
// sender applies the same policy to the address of every connection, so a
// later change of the DNS answer is refused at send time. A policy that allows
// private destinations also accepts plain http and mDNS (.local) host names,
// because a receiver on an isolated network rarely has a publicly trusted
// certificate; the resolved address still decides.
func ValidateWebhookURL(ctx context.Context, policy DestinationPolicy, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", invalidWebhookURL("url is required")
	}
	if len(raw) > maxWebhookURLLength {
		return "", invalidWebhookURL("url is longer than %d characters", maxWebhookURLLength)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", invalidWebhookURL("url does not parse")
	}
	if parsed.Scheme != "https" && (parsed.Scheme != "http" || !policy.AllowPrivate) {
		return "", invalidWebhookURL("url must use https")
	}
	if parsed.User != nil {
		return "", invalidWebhookURL("url must not contain credentials; requests are authenticated by their signature")
	}
	if parsed.Fragment != "" || strings.Contains(raw, "#") {
		return "", invalidWebhookURL("url must not contain a fragment")
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "" {
		return "", invalidWebhookURL("url must name a host")
	}
	if isPlatformHostName(host) {
		return "", invalidWebhookURL("url host is operator-internal")
	}
	if port := parsed.Port(); port != "" {
		if n, convErr := strconv.Atoi(port); convErr != nil || n <= 0 || n > 65535 {
			return "", invalidWebhookURL("url port is not valid")
		}
	}
	if !policy.AllowPrivate && strings.HasSuffix(host, ".local") {
		return "", invalidWebhookURL("url host is not a public destination")
	}
	if err := policy.ValidateURI(ctx, parsed); err != nil {
		if errors.Is(err, ErrDestinationNotFound) {
			return "", invalidWebhookURL("url host %s does not resolve to an address", host)
		}
		if errors.Is(err, ErrDestinationResolution) {
			return "", fmt.Errorf("%w: %s", ErrDestinationResolution, host)
		}
		return "", invalidWebhookURL("url is not a public destination: %v", err)
	}
	return parsed.String(), nil
}
