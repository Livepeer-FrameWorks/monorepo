package provisioner

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/inventory"
)

func chatwootRoleVars(_ context.Context, host inventory.Host, config ServiceConfig, _ RoleBuildHelpers) (map[string]any, error) {
	image, err := resolvePinnedImage("chatwoot", config)
	if err != nil {
		return nil, err
	}
	port := config.Port
	if port == 0 {
		port = 18092
	}
	env, err := chatwootEnvMap(config, host)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"chatwoot_image":         image,
		"chatwoot_port":          port,
		"chatwoot_publish_addrs": composePublishAddrs(host),
		"chatwoot_env":           env,
	}, nil
}

func chatwootRoleDetect(ctx context.Context, host inventory.Host, config ServiceConfig, helpers RoleBuildHelpers) (*detect.ServiceState, error) {
	return serviceRoleDetect("chatwoot")(ctx, host, config, helpers)
}

// chatwootEnvMap assembles the non-secret environment the compose .env
// file needs. SECRET_KEY_BASE is omitted on purpose — the role reads or
// generates it on-target (tasks/secret.yml) and combines it with this map
// before writing .env.
func chatwootEnvMap(config ServiceConfig, host inventory.Host) (map[string]any, error) {
	dbHost, err := dockerHostAddr(firstNonEmptyEnv(config.EnvVars, "POSTGRES_CHATWOOT_HOST", "POSTGRES_SUPPORT_HOST", "DATABASE_HOST"), host)
	if err != nil {
		return nil, fmt.Errorf("chatwoot Postgres: %w", err)
	}
	redisURL, err := chatwootRedisURL(config, host)
	if err != nil {
		return nil, fmt.Errorf("chatwoot Redis: %w", err)
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
		"POSTGRES_HOST":             dbHost,
		"POSTGRES_PORT":             dbPort,
		"POSTGRES_DATABASE":         "chatwoot",
		"POSTGRES_USERNAME":         "chatwoot",
		"POSTGRES_PASSWORD":         dbPassword,
		"SMTP_AUTHENTICATION":       "login",
		"SMTP_ENABLE_STARTTLS_AUTO": "true",
		"REDIS_URL":                 redisURL,
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
	if v := firstNonEmptyEnv(config.EnvVars, "CHATWOOT_MAILER_EMAIL", "FROM_EMAIL"); v != "" {
		env["MAILER_SENDER_EMAIL"] = v
	}
	return env, nil
}

func firstNonEmptyEnv(env map[string]string, keys ...string) string {
	for _, key := range keys {
		if v := env[key]; v != "" {
			return v
		}
	}
	return ""
}

func chatwootRedisURL(config ServiceConfig, host inventory.Host) (string, error) {
	addr, err := dockerHostAddr(firstNonEmpty(config.EnvVars["REDIS_CHATWOOT_ADDR"], "127.0.0.1:6380"), host)
	if err != nil {
		return "", err
	}
	if password := config.EnvVars["REDIS_CHATWOOT_PASSWORD"]; password != "" {
		return "redis://:" + url.QueryEscape(password) + "@" + addr, nil
	}
	return "redis://" + addr, nil
}

// dockerHostAddr is addr, a host or host:port, as a container on host reaches
// it. Postgres and Redis on the host listen only on loopback and the host's
// WireGuard address, and a container reaches neither host loopback nor a
// listener on the Docker gateway, so a loopback or empty host becomes the
// WireGuard address. A host without one cannot serve such a container.
func dockerHostAddr(addr string, host inventory.Host) (string, error) {
	hostPart, port, splitErr := net.SplitHostPort(addr)
	if splitErr != nil {
		hostPart, port = addr, ""
	}
	if hostPart != "" && hostPart != "localhost" && hostPart != "host.docker.internal" {
		if ip := net.ParseIP(hostPart); ip == nil || !ip.IsLoopback() {
			return addr, nil
		}
	}
	wireguardIP := strings.TrimSpace(host.WireguardIP)
	if wireguardIP == "" {
		return "", fmt.Errorf("host %s has no wireguard_ip: a container reaches the host's Postgres and Redis only on the WireGuard address, since they do not listen on the Docker gateway", host.Name)
	}
	if port == "" {
		return wireguardIP, nil
	}
	return net.JoinHostPort(wireguardIP, port), nil
}
