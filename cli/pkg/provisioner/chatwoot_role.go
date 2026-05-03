package provisioner

import (
	"context"

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
	dbHost := firstNonEmptyEnv(config.EnvVars, "POSTGRES_CHATWOOT_HOST", "POSTGRES_SUPPORT_HOST", "DATABASE_HOST")
	if dbHost == "" {
		dbHost = "host.docker.internal"
	}
	dbPort := firstNonEmptyEnv(config.EnvVars, "POSTGRES_CHATWOOT_PORT", "POSTGRES_SUPPORT_PORT", "DATABASE_PORT")
	if dbPort == "" {
		dbPort = "5432"
	}
	dbPassword := firstNonEmptyEnv(config.EnvVars, "POSTGRES_CHATWOOT_PASSWORD", "POSTGRES_SUPPORT_PASSWORD", "DATABASE_PASSWORD")
	env := map[string]any{
		"RAILS_ENV":                 "production",
		"RAILS_LOG_TO_STDOUT":       "true",
		"LOG_LEVEL":                 "info",
		"POSTGRES_HOST":             rewriteLoopbackForDockerHost(dbHost),
		"POSTGRES_PORT":             dbPort,
		"POSTGRES_DATABASE":         "chatwoot",
		"POSTGRES_USERNAME":         "chatwoot",
		"POSTGRES_PASSWORD":         dbPassword,
		"SMTP_AUTHENTICATION":       "login",
		"SMTP_ENABLE_STARTTLS_AUTO": "true",
		"REDIS_URL":                 chatwootRedisURL(config),
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

func firstNonEmptyEnv(env map[string]string, keys ...string) string {
	for _, key := range keys {
		if v := env[key]; v != "" {
			return v
		}
	}
	return ""
}

func chatwootRedisURL(config ServiceConfig) string {
	if addr := config.EnvVars["REDIS_CHATWOOT_ADDR"]; addr != "" {
		return "redis://" + rewriteLoopbackForDockerHost(addr)
	}
	return "redis://host.docker.internal:6380"
}

func rewriteLoopbackForDockerHost(addr string) string {
	switch {
	case addr == "127.0.0.1":
		return "host.docker.internal"
	case addr == "localhost":
		return "host.docker.internal"
	case len(addr) > len("127.0.0.1:") && addr[:len("127.0.0.1:")] == "127.0.0.1:":
		return "host.docker.internal:" + addr[len("127.0.0.1:"):]
	case len(addr) > len("localhost:") && addr[:len("localhost:")] == "localhost:":
		return "host.docker.internal:" + addr[len("localhost:"):]
	default:
		return addr
	}
}
