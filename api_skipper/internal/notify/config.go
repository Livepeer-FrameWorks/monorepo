package notify

import (
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

	return Config{
		SMTP: email.Config{
			Host:     config.GetEnv("SMTP_HOST", ""),
			Port:     config.GetEnv("SMTP_PORT", "587"),
			User:     config.GetEnv("SMTP_USER", ""),
			Password: config.GetEnv("SMTP_PASSWORD", ""),
			From:     fromEmail,
			FromName: fromName,
		},
		DefaultPreferences: PreferenceDefaults{
			Email: config.GetEnvBool("SKIPPER_NOTIFY_EMAIL", true),
			MCP:   config.GetEnvBool("SKIPPER_NOTIFY_MCP", true),
		},
		WebAppURL: config.GetEnv("WEBAPP_PUBLIC_URL", ""),
	}
}
