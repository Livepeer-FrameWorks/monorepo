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
	bind := metaString(config.Metadata, "bind")
	if bind == "" {
		bind = "127.0.0.1"
	}
	pwd := metaString(config.Metadata, "password")
	instance := firstNonEmpty(metaString(config.Metadata, "instance"), metaString(config.Metadata, "instance_name"))
	engine := metaString(config.Metadata, "engine")
	if engine == "" {
		engine = "valkey"
	}

	vars := map[string]any{
		"redis_version":        version,
		"redis_engine":         engine,
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
	// Sentinel-mode HA: redis_role gates which template + service args the
	// Ansible role uses. Primary tasks get the default conf; replica tasks
	// add replicaof + masterauth; sentinel tasks render sentinel.conf with
	// the quorum the planner sized from the manifest.
	if role := metaString(config.Metadata, "redis_role"); role != "" {
		vars["redis_role"] = role
	}
	if primaryHost := metaString(config.Metadata, "redis_primary_host"); primaryHost != "" {
		vars["redis_primary_host"] = primaryHost
	}
	if primaryPort, ok := config.Metadata["redis_primary_port"].(int); ok && primaryPort > 0 {
		vars["redis_primary_port"] = primaryPort
	}
	if master := metaString(config.Metadata, "redis_master_name"); master != "" {
		vars["redis_master_name"] = master
	}
	if sp, ok := config.Metadata["redis_sentinel_port"].(int); ok && sp > 0 {
		vars["redis_sentinel_port"] = sp
	}
	if q, ok := config.Metadata["redis_sentinel_quorum"].(int); ok && q > 0 {
		vars["redis_sentinel_quorum"] = q
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
	bin, binErr := runner.Run(ctx, "command -v redis-cli >/dev/null && echo EXISTS")
	exists := binErr == nil && bin != nil && strings.Contains(bin.Stdout, "EXISTS")
	return &detect.ServiceState{Exists: exists, Running: running}, nil
}
