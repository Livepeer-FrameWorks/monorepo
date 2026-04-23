package provisioner

import (
	"context"
	"strings"
	"time"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

// clickhouseRoleVars translates the manifest's clickhouse.* config into the
// role's variable surface, which in turn feeds idealista.clickhouse.
func clickhouseRoleVars(ctx context.Context, host inventory.Host, config ServiceConfig, helpers RoleBuildHelpers) (map[string]any, error) {
	version := firstNonEmpty(config.Version, metaString(config.Metadata, "version"))
	if version == "" || version == "latest" || version == "stable" {
		version = "24.8"
	}
	port := config.Port
	if port == 0 {
		port = 9000
	}
	pwd := metaString(config.Metadata, "password")
	if pwd == "" {
		pwd = metaString(config.Metadata, "clickhouse_password")
	}

	vars := map[string]any{
		"clickhouse_version":          version,
		"clickhouse_port_tcp":         port,
		"clickhouse_default_password": pwd,
	}
	if listen, ok := config.Metadata["listen_host"].(string); ok && listen != "" {
		vars["clickhouse_listen_host"] = listen
	}
	if httpPort, ok := config.Metadata["http_port"].(int); ok && httpPort > 0 {
		vars["clickhouse_port_http"] = httpPort
	}
	if dbs, ok := config.Metadata["databases"].([]string); ok && len(dbs) > 0 {
		vars["clickhouse_databases"] = dbs
	}
	if items, ok := config.Metadata["clickhouse_seed_items"].([]map[string]any); ok && len(items) > 0 {
		vars["clickhouse_seed_items"] = items
	}
	return vars, nil
}

func clickhouseRoleDetect(ctx context.Context, host inventory.Host, helpers RoleBuildHelpers) (*detect.ServiceState, error) {
	if host.ExternalIP == "127.0.0.1" || host.ExternalIP == "localhost" {
		return &detect.ServiceState{Exists: false, Running: false}, nil
	}
	runner, err := helpers.SSHPool.Get(&ssh.ConnectionConfig{
		Address: host.ExternalIP, Port: 22, User: host.User, HostName: host.Name, Timeout: 10 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	result, err := runner.Run(ctx, "systemctl is-active clickhouse-server 2>/dev/null | grep -qx active && echo RUNNING || echo NOT_RUNNING")
	running := err == nil && strings.Contains(result.Stdout, "RUNNING") && !strings.Contains(result.Stdout, "NOT_RUNNING")
	bin, binErr := runner.Run(ctx, "command -v clickhouse-client >/dev/null && echo EXISTS")
	exists := binErr == nil && bin != nil && strings.Contains(bin.Stdout, "EXISTS")
	return &detect.ServiceState{Exists: exists, Running: running}, nil
}
