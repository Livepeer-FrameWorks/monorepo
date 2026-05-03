package provisioner

import (
	"context"
	"fmt"
	"strings"
	"time"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

// postgresRoleVars turns the manifest shape into the variable surface the
// frameworks.infra.postgres role (wrapping geerlingguy.postgresql) expects.
func postgresRoleVars(ctx context.Context, host inventory.Host, config ServiceConfig, helpers RoleBuildHelpers) (map[string]any, error) {
	version := firstNonEmpty(config.Version, metaString(config.Metadata, "version"))
	if version == "" {
		version = "16"
	}
	port := config.Port
	if port == 0 {
		port = 5432
	}
	pwd := metaString(config.Metadata, "postgres_password")
	if pwd == "" {
		pwd = metaString(config.Metadata, "password")
	}
	if pwd == "" {
		return nil, fmt.Errorf("postgres: no password in metadata (postgres_password/password)")
	}

	vars := map[string]any{
		"postgres_version":          version,
		"postgres_port":             port,
		"postgres_admin_password":   pwd,
		"postgres_listen_addresses": "*",
		"postgres_instance_name":    sanitizePostgresInstanceName(firstNonEmpty(config.DeployName, "postgres")),
	}

	if tuning, ok := config.Metadata["tuning"].(map[string]any); ok {
		if v, ok := tuning["max_connections"].(int); ok {
			vars["postgres_max_connections"] = v
		}
		if v, ok := tuning["shared_buffers"].(string); ok && v != "" {
			vars["postgres_shared_buffers"] = v
		}
	}

	if dbs, ok := config.Metadata["databases"].([]map[string]string); ok && len(dbs) > 0 {
		list := make([]map[string]any, 0, len(dbs))
		for _, db := range dbs {
			entry := map[string]any{"name": db["name"]}
			if owner := db["owner"]; owner != "" {
				entry["owner"] = owner
			}
			list = append(list, entry)
		}
		vars["postgres_databases"] = list
	}
	if items, ok := config.Metadata["postgres_seed_items"].([]map[string]any); ok && len(items) > 0 {
		vars["postgres_seed_items"] = items
	}
	if items, ok := config.Metadata["postgres_schema_items"].([]map[string]any); ok && len(items) > 0 {
		vars["postgres_schema_items"] = items
	}
	if items, ok := config.Metadata["postgres_migrate_items"].([]map[string]any); ok && len(items) > 0 {
		vars["postgres_migrate_items"] = items
	}
	return vars, nil
}

func sanitizePostgresInstanceName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if valid {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "postgres"
	}
	return out
}

// postgresRoleDetect checks whether a postgresql server is running on the host.
// Cheap SSH probe — runs before any playbook.
func postgresRoleDetect(ctx context.Context, host inventory.Host, helpers RoleBuildHelpers) (*detect.ServiceState, error) {
	if host.ExternalIP == "127.0.0.1" || host.ExternalIP == "localhost" {
		return &detect.ServiceState{Exists: false, Running: false}, nil
	}
	runner, err := helpers.SSHPool.Get(&ssh.ConnectionConfig{
		Address:  host.ExternalIP,
		Port:     22,
		User:     host.User,
		HostName: host.Name,
		Timeout:  10 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	result, err := runner.Run(ctx, "systemctl is-active postgresql 2>/dev/null | grep -qx active && pg_isready -h 127.0.0.1 -q && echo RUNNING || echo NOT_RUNNING")
	running := err == nil && strings.Contains(result.Stdout, "RUNNING") && !strings.Contains(result.Stdout, "NOT_RUNNING")

	bin, binErr := runner.Run(ctx, "command -v psql >/dev/null && echo EXISTS")
	exists := binErr == nil && bin != nil && strings.Contains(bin.Stdout, "EXISTS")

	return &detect.ServiceState{Exists: exists, Running: running}, nil
}
