package provisioner

import (
	"context"
	"strings"
	"time"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

func zookeeperRoleVars(ctx context.Context, host inventory.Host, config ServiceConfig, helpers RoleBuildHelpers) (map[string]any, error) {
	_, arch, err := helpers.DetectRemoteOS(ctx, host)
	if err != nil {
		return nil, err
	}
	archKey := "linux-" + arch
	channel := platformChannelFromMetadata(config.Metadata)
	art, err := helpers.ResolveArtifact("zookeeper", archKey, channel, config.Metadata)
	if err != nil {
		return nil, err
	}
	nodeID, _ := config.Metadata["node_id"].(int)
	if nodeID == 0 {
		nodeID = 1
	}

	vars := map[string]any{
		"zookeeper_artifact_url":      art.URL,
		"zookeeper_artifact_checksum": art.Checksum,
		"zookeeper_version":           firstNonEmpty(config.Version, art.Version),
		"zookeeper_node_id":           nodeID,
	}
	if ensemble, ok := config.Metadata["ensemble"].([]map[string]any); ok && len(ensemble) > 0 {
		vars["zookeeper_ensemble"] = ensemble
	}
	return vars, nil
}

func zookeeperRoleDetect(ctx context.Context, host inventory.Host, helpers RoleBuildHelpers) (*detect.ServiceState, error) {
	if host.ExternalIP == "127.0.0.1" || host.ExternalIP == "localhost" {
		return &detect.ServiceState{Exists: false, Running: false}, nil
	}
	runner, err := helpers.SSHPool.Get(&ssh.ConnectionConfig{
		Address: host.ExternalIP, Port: 22, User: host.User, HostName: host.Name, Timeout: 10 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	result, _ := runner.Run(ctx, "systemctl is-active zookeeper 2>/dev/null | grep -qx active && echo RUNNING || echo NOT_RUNNING")
	running := result != nil && strings.Contains(result.Stdout, "RUNNING") && !strings.Contains(result.Stdout, "NOT_RUNNING")
	bin, _ := runner.Run(ctx, "test -x /opt/zookeeper/bin/zkServer.sh && echo EXISTS")
	exists := bin != nil && strings.Contains(bin.Stdout, "EXISTS")
	return &detect.ServiceState{Exists: exists, Running: running}, nil
}
