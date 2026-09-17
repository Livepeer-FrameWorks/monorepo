// Package notify delivers Lookout's outbox rows: operator notifications to
// email, Slack, and Discord, and lookout.incidents records to Kafka.
package notify

import (
	"frameworks/api_incidents/internal/config"
	"frameworks/api_incidents/internal/incidents"
)

// Router routes platform-scope incident notifications to the operator
// channels whose destination is configured. It reads the environment on every
// call so an env-file reload changes routing without a restart.
type Router struct{}

// Enabled reports whether a channel has a configured destination. Kafka is
// always enabled because the topic is required infrastructure.
func (Router) Enabled(channel string) bool {
	switch channel {
	case incidents.ChannelEmail:
		return len(config.NotifyEmailRecipients()) > 0
	case incidents.ChannelSlack:
		return config.SlackWebhookURL() != ""
	case incidents.ChannelDiscord:
		return config.DiscordWebhookURL() != ""
	case incidents.ChannelKafka:
		return true
	default:
		return false
	}
}

// ChannelsFor sends critical incidents to every configured channel and all
// other severities to the configured chat channels.
func (r Router) ChannelsFor(severity string) []string {
	candidates := []string{incidents.ChannelSlack, incidents.ChannelDiscord}
	if severity == "critical" {
		candidates = []string{incidents.ChannelEmail, incidents.ChannelSlack, incidents.ChannelDiscord}
	}
	var out []string
	for _, channel := range candidates {
		if r.Enabled(channel) {
			out = append(out, channel)
		}
	}
	return out
}
