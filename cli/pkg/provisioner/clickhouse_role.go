package provisioner

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
)

// clickhouseRoleVars translates the manifest's clickhouse.* config into the
// role's variable surface, which in turn feeds idealista.clickhouse.
func clickhouseRoleVars(ctx context.Context, host inventory.Host, config ServiceConfig, helpers RoleBuildHelpers) (map[string]any, error) {
	version, err := infrastructureVersion("clickhouse", config)
	if err != nil {
		return nil, fmt.Errorf("resolve clickhouse version: %w", err)
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
	if readonlyPwd := metaString(config.Metadata, "clickhouse_readonly_password"); readonlyPwd != "" {
		vars["clickhouse_readonly_password"] = readonlyPwd
	}
	if listenHosts := metaStringSlice(config.Metadata, "listen_hosts"); len(listenHosts) > 0 {
		vars["clickhouse_listen_hosts"] = listenHosts
	} else if listen, ok := config.Metadata["listen_host"].(string); ok && listen != "" {
		vars["clickhouse_listen_host"] = listen
	} else {
		listenHosts := []string{"127.0.0.1"}
		if advertised := strings.TrimSpace(metaString(config.Metadata, "advertised_host")); advertised != "" && advertised != "127.0.0.1" && advertised != "localhost" {
			if ip := net.ParseIP(advertised); ip == nil || !ip.IsLoopback() {
				listenHosts = append(listenHosts, advertised)
			}
		}
		vars["clickhouse_listen_hosts"] = listenHosts
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
	if items, ok := config.Metadata["named_collections"].([]map[string]any); ok && len(items) > 0 {
		vars["clickhouse_named_collections"] = items
	}
	if pw := metaString(config.Metadata, "clickhouse_analytics_password"); pw != "" {
		vars["clickhouse_analytics_password"] = pw
	}
	if items, ok := config.Metadata["clickhouse_schema_items"].([]map[string]any); ok && len(items) > 0 {
		vars["clickhouse_schema_items"] = items
	}
	if items, ok := config.Metadata["clickhouse_migrate_items"].([]map[string]any); ok && len(items) > 0 {
		vars["clickhouse_migrate_items"] = items
	}
	// Replicated-cluster topology. The CLI supplies these for every enabled
	// ClickHouse node; direct role use can omit them to run without Keeper.
	if clusterName := metaString(config.Metadata, "cluster_name"); clusterName != "" {
		vars["clickhouse_cluster_name"] = clusterName
	}
	if shard := metaString(config.Metadata, "shard"); shard != "" {
		vars["clickhouse_shard"] = shard
	}
	if replica := metaString(config.Metadata, "replica"); replica != "" {
		vars["clickhouse_replica"] = replica
	}
	if nodes, ok := config.Metadata["cluster_nodes"].([]map[string]any); ok && len(nodes) > 0 {
		vars["clickhouse_cluster_nodes"] = nodes
	}
	if keeper, ok := config.Metadata["keeper_nodes"].([]map[string]any); ok && len(keeper) > 0 {
		vars["clickhouse_keeper_nodes"] = keeper
		// Keeper ports sourced from the single servicedefs catalog so the
		// templates never hardcode them and accounting (ports.go) stays in sync.
		vars["clickhouse_keeper_client_port"] = servicedefs.ClickHouseKeeperClientPort
		vars["clickhouse_keeper_raft_port"] = servicedefs.ClickHouseKeeperRaftPort
	}
	if id, ok := config.Metadata["node_id"].(int); ok && id > 0 {
		vars["clickhouse_keeper_id"] = id
	}
	return vars, nil
}

func clickhouseRoleDetect(ctx context.Context, host inventory.Host, _ ServiceConfig, helpers RoleBuildHelpers) (*detect.ServiceState, error) {
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
