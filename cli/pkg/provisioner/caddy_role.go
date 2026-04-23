package provisioner

import (
	"context"
	"strings"
	"time"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

func caddyRoleVars(ctx context.Context, host inventory.Host, config ServiceConfig, helpers RoleBuildHelpers) (map[string]any, error) {
	version := firstNonEmpty(config.Version, metaString(config.Metadata, "version"))
	if version == "" {
		version = "2.8.4"
	}

	vars := map[string]any{
		"caddy_version": version,
	}

	if sites, ok := config.Metadata["sites"].([]map[string]any); ok && len(sites) > 0 {
		out := append(make([]map[string]any, 0, len(sites)), sites...)
		vars["caddy_sites"] = out
	}
	if email, ok := config.Metadata["tls_email"].(string); ok && email != "" {
		vars["caddy_global_options"] = map[string]any{"email": email}
	}
	return vars, nil
}

func caddyRoleDetect(ctx context.Context, host inventory.Host, helpers RoleBuildHelpers) (*detect.ServiceState, error) {
	if host.ExternalIP == "127.0.0.1" || host.ExternalIP == "localhost" {
		return &detect.ServiceState{Exists: false, Running: false}, nil
	}
	runner, err := helpers.SSHPool.Get(&ssh.ConnectionConfig{
		Address: host.ExternalIP, Port: 22, User: host.User, HostName: host.Name, Timeout: 10 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	result, runErr := runner.Run(ctx, "systemctl is-active caddy 2>/dev/null | grep -qx active && echo RUNNING || echo NOT_RUNNING")
	running := runErr == nil && result != nil && strings.Contains(result.Stdout, "RUNNING") && !strings.Contains(result.Stdout, "NOT_RUNNING")
	bin, binErr := runner.Run(ctx, "command -v caddy >/dev/null && echo EXISTS")
	exists := binErr == nil && bin != nil && strings.Contains(bin.Stdout, "EXISTS")
	return &detect.ServiceState{Exists: exists, Running: running}, nil
}
