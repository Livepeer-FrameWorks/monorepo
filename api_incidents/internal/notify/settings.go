package notify

import (
	"frameworks/api_incidents/internal/incidents"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/email"
)

// Settings is the operator notification configuration in effect for one
// routing decision or delivery.
type Settings struct {
	// EmailRecipients are the operator addresses that receive incident emails.
	EmailRecipients   []string
	SlackWebhookURL   string
	DiscordWebhookURL string
	// WebappURL is the public web application base URL without a trailing
	// slash. Incident links are built under it.
	WebappURL string
	SMTP      email.Config
	Branding  config.EmailBranding
}

// SettingsSource returns the current Settings. Lookout backs it with its live
// configuration, so an env-file reload changes routing and delivery without a
// restart. A nil source yields the zero Settings, in which no operator channel
// is configured.
type SettingsSource func() Settings

func (s SettingsSource) current() Settings {
	if s == nil {
		return Settings{}
	}
	return s()
}

// webhookURL returns the chat webhook destination of a channel, or "" for a
// channel that is not delivered by webhook.
func (s Settings) webhookURL(channel string) string {
	switch channel {
	case incidents.ChannelSlack:
		return s.SlackWebhookURL
	case incidents.ChannelDiscord:
		return s.DiscordWebhookURL
	default:
		return ""
	}
}
