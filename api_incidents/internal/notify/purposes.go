package notify

import "frameworks/api_incidents/internal/incidents"

const (
	PurposeOps           = "ops"
	PurposeBusinessPulse = "business_pulse"
	PurposeNetwork       = "network"
)

func (s Settings) disabled(purpose string) bool {
	switch purpose {
	case PurposeOps:
		return s.OpsNotificationsDisabled
	case PurposeBusinessPulse:
		return s.BusinessPulseNotificationsDisabled
	default:
		return true
	}
}

func (s Settings) purposeWebhook(purpose, channel string) string {
	if s.disabled(purpose) {
		return ""
	}
	switch purpose + "." + channel {
	case "ops.slack":
		return s.OpsSlackWebhookURL
	case "ops.discord":
		return s.OpsDiscordWebhookURL
	case "business_pulse.slack":
		return s.BusinessPulseSlackWebhookURL
	case "business_pulse.discord":
		return s.BusinessPulseDiscordWebhookURL
	default:
		return ""
	}
}

func (s Settings) destinationFor(purpose, channel string) string {
	if s.disabled(purpose) {
		return ""
	}
	if channel == incidents.ChannelEmail {
		if purpose == PurposeOps && len(s.EmailRecipients) > 0 {
			return "ops.email.primary"
		}
		return ""
	}
	if s.purposeWebhook(purpose, channel) != "" {
		return purpose + "." + channel + ".primary"
	}
	if !s.DisableLegacyFallback && s.webhookURL(channel) != "" {
		return "legacy." + channel
	}
	return ""
}

// destinationURL resolves a persisted destination without applying current routing defaults.
// Empty IDs belong to rows queued before purpose routing and retain the legacy URL.
func (s Settings) destinationURL(id, channel string) string {
	if id == "" || id == "legacy."+channel {
		return s.webhookURL(channel)
	}
	for _, purpose := range []string{PurposeOps, PurposeBusinessPulse} {
		if id == purpose+"."+channel+".primary" {
			return s.purposeWebhook(purpose, channel)
		}
	}
	return ""
}

func (r Router) RoutesForPurpose(purpose string) []incidents.OperatorRoute {
	s := r.Settings.current()
	return routes(s, purpose, []string{incidents.ChannelSlack, incidents.ChannelDiscord})
}

func routes(s Settings, purpose string, channels []string) []incidents.OperatorRoute {
	var out []incidents.OperatorRoute
	for _, channel := range channels {
		if id := s.destinationFor(purpose, channel); id != "" {
			out = append(out, incidents.OperatorRoute{Channel: channel, Purpose: purpose, DestinationID: id})
		}
	}
	return out
}

func (r Router) RoutesFor(severity string) []incidents.OperatorRoute {
	channels := []string{incidents.ChannelSlack, incidents.ChannelDiscord}
	if severity == "critical" {
		channels = append([]string{incidents.ChannelEmail}, channels...)
	}
	return routes(r.Settings.current(), PurposeOps, channels)
}
