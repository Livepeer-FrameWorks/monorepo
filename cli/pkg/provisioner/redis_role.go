package provisioner

import (
	"context"
	"strings"
	"time"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

// redisRoleVars translates the manifest's redis.* config into the role's var
// surface. Multi-instance manifests invoke Provision per named instance, so
// each call here produces vars for exactly one instance.
func redisRoleVars(ctx context.Context, host inventory.Host, config ServiceConfig, helpers RoleBuildHelpers) (map[string]any, error) {
	version := firstNonEmpty(config.Version, metaString(config.Metadata, "version"))
	if version == "" {
		version = "7.2"
	}
	port := config.Port
	if port == 0 {
		port = 6379
	}
	bind, _ := config.Metadata["bind"].(string)
	if bind == "" {
		bind = "127.0.0.1"
	}
	pwd, _ := config.Metadata["password"].(string)
	instance, _ := config.Metadata["instance"].(string)

	vars := map[string]any{
		"redis_version":        version,
		"redis_port":           port,
		"redis_bind_interface": bind,
		"redis_password":       pwd,
		"redis_instance":       instance,
	}
	if mem, ok := config.Metadata["maxmemory"].(string); ok && mem != "" {
		vars["redis_maxmemory"] = mem
	}
	if appendonly, ok := config.Metadata["appendonly"].(string); ok && appendonly != "" {
		vars["redis_appendonly"] = appendonly
	}
	return vars, nil
}

func redisRoleDetect(ctx context.Context, host inventory.Host, helpers RoleBuildHelpers) (*detect.ServiceState, error) {
	if host.ExternalIP == "127.0.0.1" || host.ExternalIP == "localhost" {
		return &detect.ServiceState{Exists: false, Running: false}, nil
	}
	runner, err := helpers.SSHPool.Get(&ssh.ConnectionConfig{
		Address: host.ExternalIP, Port: 22, User: host.User, HostName: host.Name, Timeout: 10 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	result, err := runner.Run(ctx, "(pgrep -x redis-server || pgrep -x valkey-server) >/dev/null && echo RUNNING || echo NOT_RUNNING")
	running := err == nil && strings.Contains(result.Stdout, "RUNNING") && !strings.Contains(result.Stdout, "NOT_RUNNING")
	bin, _ := runner.Run(ctx, "command -v redis-cli >/dev/null && echo EXISTS")
	exists := bin != nil && strings.Contains(bin.Stdout, "EXISTS")
	return &detect.ServiceState{Exists: exists, Running: running}, nil
}
