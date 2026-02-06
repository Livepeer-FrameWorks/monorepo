package notify

import (
	"fmt"

	"frameworks/pkg/config"
	"frameworks/pkg/email"
)

type Config struct {
	SMTP               email.Config
	DefaultPreferences PreferenceDefaults
	WebAppURL          string
}

func LoadConfig() Config {
	fromEmail := config.GetEnv("FROM_EMAIL", "noreply@frameworks.network")
	fromName := config.GetEnv("FROM_NAME", "")
	from := fromEmail
	if fromName != "" {
		from = fmt.Sprintf("%s <%s>", fromName, fromEmail)
	}

	return Config{
		SMTP: email.Config{
			Host:     config.GetEnv("SMTP_HOST", ""),
			Port:     config.GetEnv("SMTP_PORT", "587"),
			User:     config.GetEnv("SMTP_USER", ""),
			Password: config.GetEnv("SMTP_PASSWORD", ""),
			From:     from,
		},
		DefaultPreferences: PreferenceDefaults{
			Email: config.GetEnvBool("SKIPPER_NOTIFY_EMAIL", true),
			MCP:   config.GetEnvBool("SKIPPER_NOTIFY_MCP", true),
		},
		WebAppURL: config.GetEnv("WEBAPP_PUBLIC_URL", ""),
	}
}
