// Package validate checks tenant input to the webhook API before it is
// stored.
package validate

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"frameworks/api_webhooks/internal/ledger"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/restream"
)

const (
	maxURLLength         = 2048
	maxDescriptionLength = 500
)

// ErrInvalid wraps every rejection of tenant input.
var ErrInvalid = errors.New("invalid webhook endpoint")

// ErrResolution means the endpoint host could not be resolved right now; the
// request may succeed when retried.
var ErrResolution = errors.New("the endpoint host could not be resolved")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// URL checks an endpoint URL: https, a host, no credentials or fragment, and
// a destination the policy allows. A literal forbidden address and a host
// that resolves to one are both rejected. The same policy is applied again to
// the address of every connection, so a later change of the DNS answer is
// refused at send time. A policy that allows private destinations also
// accepts plain http and local host names, because a receiver on an isolated
// network rarely has a publicly trusted certificate; the resolved address
// still decides.
func URL(ctx context.Context, policy restream.DestinationPolicy, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", invalid("url is required")
	}
	if len(raw) > maxURLLength {
		return "", invalid("url is longer than %d characters", maxURLLength)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", invalid("url does not parse")
	}
	if parsed.Scheme != "https" && (parsed.Scheme != "http" || !policy.AllowPrivate) {
		return "", invalid("url must use https")
	}
	if parsed.User != nil {
		return "", invalid("url must not contain credentials")
	}
	if parsed.Fragment != "" || strings.Contains(raw, "#") {
		return "", invalid("url must not contain a fragment")
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "" {
		return "", invalid("url must name a host")
	}
	if host == "frameworks.network" || strings.HasSuffix(host, ".frameworks.network") {
		return "", invalid("url host is operator-internal")
	}
	if port := parsed.Port(); port != "" {
		if n, convErr := strconv.Atoi(port); convErr != nil || n <= 0 || n > 65535 {
			return "", invalid("url port is not valid")
		}
	}
	if !policy.AllowPrivate && (host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal")) {
		return "", invalid("url host is not a public destination")
	}
	if err := policy.ValidateURI(ctx, parsed); err != nil {
		if errors.Is(err, restream.ErrDestinationResolution) {
			return "", fmt.Errorf("%w: %s", ErrResolution, host)
		}
		return "", invalid("url is not a public destination: %v", err)
	}
	return parsed.String(), nil
}

// Description checks the free-text description.
func Description(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if !utf8.ValidString(raw) {
		return "", invalid("description is not valid UTF-8")
	}
	if utf8.RuneCountInString(raw) > maxDescriptionLength {
		return "", invalid("description is longer than %d characters", maxDescriptionLength)
	}
	return raw, nil
}

// EventTypes checks a subscription list: at least one entry, each a public
// event type or "*". Duplicates are dropped and order is kept.
func EventTypes(types []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(types))
	for _, t := range types {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		if t != ledger.AllEventTypes {
			spec, ok := events.Lookup(t)
			if !ok || !spec.Public() {
				return nil, invalid("%q is not a public event type", t)
			}
		}
		seen[t] = true
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil, invalid("at least one event type is required")
	}
	return out, nil
}

// APIVersion checks the pinned public event package major; empty selects v1.
func APIVersion(raw string) (string, error) {
	switch strings.TrimSpace(raw) {
	case "", ledger.APIVersionV1:
		return ledger.APIVersionV1, nil
	default:
		return "", invalid("api version %q is not supported; supported: %s", raw, ledger.APIVersionV1)
	}
}
