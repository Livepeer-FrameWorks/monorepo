package provisioner

import (
	"context"
	"fmt"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/inventory"
)

func chatwootRoleVars(_ context.Context, _ inventory.Host, config ServiceConfig, _ RoleBuildHelpers) (map[string]any, error) {
	image := firstNonEmpty(config.Image, defaultChatwootImage)
	port := config.Port
	if port == 0 {
		port = 18092
	}
	return map[string]any{
		"chatwoot_image": image,
		"chatwoot_port":  port,
		"chatwoot_env":   chatwootEnvMap(config),
	}, nil
}

func chatwootRoleDetect(_ context.Context, _ inventory.Host, _ RoleBuildHelpers) (*detect.ServiceState, error) {
	return &detect.ServiceState{Exists: false, Running: false}, nil
}

// chatwootEnvMap assembles the non-secret environment the compose .env
// file needs. SECRET_KEY_BASE is omitted on purpose — the role reads or
// generates it on-target (tasks/secret.yml) and combines it with this map
// before writing .env.
func chatwootEnvMap(config ServiceConfig) map[string]any {
	dbUser := orElse(config.EnvVars["DATABASE_USER"], "postgres")
	env := map[string]any{
		"RAILS_ENV":                 "production",
		"RAILS_LOG_TO_STDOUT":       "true",
		"LOG_LEVEL":                 "info",
		"POSTGRES_HOST":             config.EnvVars["DATABASE_HOST"],
		"POSTGRES_PORT":             config.EnvVars["DATABASE_PORT"],
		"POSTGRES_DATABASE":         "chatwoot",
		"POSTGRES_USERNAME":         dbUser,
		"POSTGRES_PASSWORD":         config.EnvVars["DATABASE_PASSWORD"],
		"SMTP_AUTHENTICATION":       "login",
		"SMTP_ENABLE_STARTTLS_AUTO": "true",
	}
	if addr := config.EnvVars["REDIS_CHATWOOT_ADDR"]; addr != "" {
		env["REDIS_URL"] = fmt.Sprintf("redis://%s", addr)
	}
	if v := config.EnvVars["SMTP_HOST"]; v != "" {
		env["SMTP_ADDRESS"] = v
	}
	if v := config.EnvVars["SMTP_PORT"]; v != "" {
		env["SMTP_PORT"] = v
	}
	if v := config.EnvVars["SMTP_USER"]; v != "" {
		env["SMTP_USERNAME"] = v
	}
	if v := config.EnvVars["SMTP_PASSWORD"]; v != "" {
		env["SMTP_PASSWORD"] = v
	}
	if v := config.EnvVars["CHATWOOT_FRONTEND_URL"]; v != "" {
		env["FRONTEND_URL"] = v
	}
	if v := config.EnvVars["CHATWOOT_MAILER_EMAIL"]; v != "" {
		env["MAILER_SENDER_EMAIL"] = v
	}
	return env
}
