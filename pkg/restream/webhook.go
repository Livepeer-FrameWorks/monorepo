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

// ErrInvalidWebhookURL wraps every rejection of a tenant-supplied webhook URL.
// A transient DNS failure is reported with ErrDestinationResolution instead.
var ErrInvalidWebhookURL = errors.New("invalid webhook url")

// WebhookDestinationPolicy is the destination policy for tenant-supplied
// webhook endpoints: Bosun's outbound webhooks and playback-auth webhooks.
// Operator restream CIDR exceptions never apply to it. allowPrivate admits
// private (RFC 1918 / ULA) addresses for an isolated cluster whose receivers
// live on its own network; loopback, link-local, and cloud metadata addresses
// stay blocked either way.
func WebhookDestinationPolicy(allowPrivate bool) DestinationPolicy {
	policy := PublicDestinationPolicy()
	policy.AllowPrivate = allowPrivate
	return policy
}

func invalidWebhookURL(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidWebhookURL, fmt.Sprintf(format, args...))
}

// ValidateWebhookURL checks a webhook endpoint URL before it is stored and
// returns its normalized form: https, a host, no credentials or fragment, no
// platform host name, and a destination the policy allows. A literal
// forbidden address and a host that resolves to one are both rejected. The
// sender applies the same policy to the address of every connection, so a
// later change of the DNS answer is refused at send time. A policy that allows
// private destinations also accepts plain http and local host names, because a
// receiver on an isolated network rarely has a publicly trusted certificate;
// the resolved address still decides.
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
	if host == "frameworks.network" || strings.HasSuffix(host, ".frameworks.network") {
		return "", invalidWebhookURL("url host is operator-internal")
	}
	if port := parsed.Port(); port != "" {
		if n, convErr := strconv.Atoi(port); convErr != nil || n <= 0 || n > 65535 {
			return "", invalidWebhookURL("url port is not valid")
		}
	}
	if !policy.AllowPrivate && (host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal")) {
		return "", invalidWebhookURL("url host is not a public destination")
	}
	if err := policy.ValidateURI(ctx, parsed); err != nil {
		if errors.Is(err, ErrDestinationResolution) {
			return "", fmt.Errorf("%w: %s", ErrDestinationResolution, host)
		}
		return "", invalidWebhookURL("url is not a public destination: %v", err)
	}
	return parsed.String(), nil
}
