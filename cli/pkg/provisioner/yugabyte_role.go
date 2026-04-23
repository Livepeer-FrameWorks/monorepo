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

// yugabyteRoleVars builds the extra-vars map the frameworks.infra.yugabyte
// role expects, resolving the pinned artifact from the release manifest and
// translating manifest shape into role variables.
func yugabyteRoleVars(ctx context.Context, host inventory.Host, config ServiceConfig, helpers RoleBuildHelpers) (map[string]any, error) {
	_, remoteArch, err := helpers.DetectRemoteOS(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("detect remote arch: %w", err)
	}
	archKey := "linux-" + remoteArch

	channel := platformChannelFromMetadata(config.Metadata)
	artifact, err := helpers.ResolveArtifact("yugabyte", archKey, channel, config.Metadata)
	if err != nil {
		return nil, err
	}

	port := config.Port
	if port == 0 {
		port = 5433
	}
	rf, _ := config.Metadata["replication_factor"].(int)
	if rf == 0 {
		rf = 3
	}
	nodeID, _ := config.Metadata["node_id"].(int)
	masterAddresses, _ := config.Metadata["master_addresses"].(string)
	if masterAddresses == "" {
		masterAddresses = fmt.Sprintf("%s:7100", hostAddressFor(host))
	}

	vars := map[string]any{
		"yugabyte_artifact_url":       artifact.URL,
		"yugabyte_artifact_checksum":  artifact.Checksum,
		"yugabyte_version":            firstNonEmpty(config.Version, metaString(config.Metadata, "version")),
		"yugabyte_node_address":       hostAddressFor(host),
		"yugabyte_master_addresses":   masterAddresses,
		"yugabyte_replication_factor": rf,
		"yugabyte_ysql_port":          port,
		"yugabyte_placement_cloud":    "frameworks",
		"yugabyte_placement_region":   "eu",
		"yugabyte_placement_zone":     fmt.Sprintf("eu-%d", maxInt(nodeID, 1)),
		"yugabyte_node_id":            nodeID,
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
		vars["yugabyte_databases"] = list
	}
	// Superuser credentials. The `postgres_password` metadata key is the
	// historical name cluster_init + cluster_seed already populate.
	if pwd := metaString(config.Metadata, "postgres_password"); pwd != "" {
		vars["yugabyte_superuser_password"] = pwd
		vars["yugabyte_application_password"] = pwd
	}
	if items, ok := config.Metadata["yugabyte_seed_items"].([]map[string]any); ok && len(items) > 0 {
		vars["yugabyte_seed_items"] = items
	}
	if items, ok := config.Metadata["yugabyte_migrate_items"].([]map[string]any); ok && len(items) > 0 {
		vars["yugabyte_migrate_items"] = items
	}
	return vars, nil
}

// yugabyteRoleDetect does a pgrep-based reconnaissance over SSH before any
// playbook runs. Pre-playbook reads are cheap and avoid re-running the
// role on hosts that are already fully up.
func yugabyteRoleDetect(ctx context.Context, host inventory.Host, helpers RoleBuildHelpers) (*detect.ServiceState, error) {
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
	result, err := runner.Run(ctx, "pgrep -x yb-master >/dev/null && pgrep -x yb-tserver >/dev/null && echo RUNNING || echo NOT_RUNNING")
	running := err == nil && strings.Contains(result.Stdout, "RUNNING") && !strings.Contains(result.Stdout, "NOT_RUNNING")

	bin, _ := runner.Run(ctx, "test -x /opt/yugabyte/bin/yb-master && echo EXISTS")
	exists := bin != nil && strings.Contains(bin.Stdout, "EXISTS")

	return &detect.ServiceState{Exists: exists, Running: running}, nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
