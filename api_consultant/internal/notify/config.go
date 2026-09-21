package notify

import (
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/email"
)

// Config is the notification delivery configuration built from Skipper's
// typed startup configuration.
type Config struct {
	SMTP               email.Config
	Branding           config.EmailBranding
	BrandingSource     func() config.EmailBranding
	DefaultPreferences PreferenceDefaults
	DefaultRecipient   string
	WebAppURL          string
}
