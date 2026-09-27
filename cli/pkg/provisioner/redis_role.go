package provisioner

import (
	"context"
	"fmt"
	"slices"
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
	// seed replicaof on first install; sentinel tasks render sentinel.conf with
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

func redisRoleDetect(ctx context.Context, host inventory.Host, config ServiceConfig, helpers RoleBuildHelpers) (*detect.ServiceState, error) {
	if host.ExternalIP == "127.0.0.1" || host.ExternalIP == "localhost" {
		return &detect.ServiceState{Exists: false, Running: false}, nil
	}
	runner, err := helpers.SSHPool.Get(&ssh.ConnectionConfig{
		Address: host.ExternalIP, Port: 22, User: host.User, HostName: host.Name, Timeout: 10 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	if serviceName := redisSystemdServiceName(config); serviceName != "" {
		return detectSystemdUnit(ctx, runner, serviceName)
	}
	result, err := runner.Run(ctx, "(pgrep -x redis-server || pgrep -x valkey-server) >/dev/null && echo RUNNING || echo NOT_RUNNING")
	running := err == nil && strings.Contains(result.Stdout, "RUNNING") && !strings.Contains(result.Stdout, "NOT_RUNNING")
	bin, binErr := runner.Run(ctx, "command -v redis-cli >/dev/null && echo EXISTS")
	exists := binErr == nil && bin != nil && strings.Contains(bin.Stdout, "EXISTS")
	return &detect.ServiceState{Exists: exists, Running: running}, nil
}

func redisSystemdServiceName(config ServiceConfig) string {
	instance := firstNonEmpty(metaString(config.Metadata, "instance"), metaString(config.Metadata, "instance_name"))
	if instance == "" {
		return ""
	}
	if metaString(config.Metadata, "redis_role") == "sentinel" {
		instance += "-sentinel"
	}
	return "frameworks-redis-" + instance
}

// RedisCLICommand returns a shell command that runs valkey-cli (or redis-cli)
// with args against the named server or Sentinel the config renders, on its
// host. The password is read on the host from the file the role renders
// requirepass into, so it never appears on a command line; reading that file
// needs root or passwordless sudo.
func RedisCLICommand(config ServiceConfig, args ...string) string {
	instance := firstNonEmpty(metaString(config.Metadata, "instance"), metaString(config.Metadata, "instance_name"))
	port := config.Port
	if port == 0 {
		port = 6379
	}
	passwordFile := "/etc/redis.conf"
	if instance != "" {
		passwordFile = "/etc/frameworks/redis/" + instance + ".conf"
	}
	if metaString(config.Metadata, "redis_role") == "sentinel" {
		port = 26379
		if sp, ok := config.Metadata["redis_sentinel_port"].(int); ok && sp > 0 {
			port = sp
		}
		passwordFile = "/var/lib/frameworks/redis/" + instance + "-sentinel/sentinel.conf"
	}
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, ssh.ShellQuote(arg))
	}
	script := fmt.Sprintf(`set -eu
cli="$(command -v valkey-cli || command -v redis-cli || true)"
if [ -z "$cli" ]; then echo "neither valkey-cli nor redis-cli is installed" >&2; exit 127; fi
pass="$(awk '$1 == "requirepass" { p = $2 } END { print p }' %s 2>/dev/null | tr -d '"' || true)"
if [ -n "$pass" ]; then export REDISCLI_AUTH="$pass"; fi
"$cli" -h %s -p %d %s`, ssh.ShellQuote(passwordFile), ssh.ShellQuote(redisLocalHost(metaString(config.Metadata, "bind"))), port, strings.Join(quoted, " "))
	return `if [ "$(id -u)" = 0 ]; then sh -c ` + ssh.ShellQuote(script) + `; else sudo -n sh -c ` + ssh.ShellQuote(script) + `; fi`
}

// redisLocalHost picks the address a local client reaches the instance on,
// matching the role's redis_local_host: loopback when bound, else the first
// bind address.
func redisLocalHost(bind string) string {
	addrs := strings.Fields(bind)
	for _, loopback := range []string{"127.0.0.1", "::1"} {
		if slices.Contains(addrs, loopback) {
			return loopback
		}
	}
	if len(addrs) > 0 {
		return addrs[0]
	}
	return "127.0.0.1"
}
