// Package validate checks tenant input to the webhook API before it is
// stored.
package validate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"frameworks/api_webhooks/internal/ledger"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/restream"
)

const maxDescriptionLength = 500

// ErrInvalid wraps every rejection of tenant input.
var ErrInvalid = errors.New("invalid webhook endpoint")

// ErrResolution means the endpoint host could not be resolved right now; the
// request may succeed when retried.
var ErrResolution = errors.New("the endpoint host could not be resolved")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// URL checks an endpoint URL with the shared webhook URL rules
// (restream.ValidateWebhookURL) and maps their outcome onto this package's
// sentinels.
func URL(ctx context.Context, policy restream.DestinationPolicy, raw string) (string, error) {
	normalized, err := restream.ValidateWebhookURL(ctx, policy, raw)
	switch {
	case err == nil:
		return normalized, nil
	case errors.Is(err, restream.ErrDestinationResolution):
		return "", fmt.Errorf("%w: %s", ErrResolution, strings.TrimPrefix(err.Error(), restream.ErrDestinationResolution.Error()+": "))
	default:
		return "", fmt.Errorf("%w: %s", ErrInvalid, strings.TrimPrefix(err.Error(), restream.ErrInvalidWebhookURL.Error()+": "))
	}
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
