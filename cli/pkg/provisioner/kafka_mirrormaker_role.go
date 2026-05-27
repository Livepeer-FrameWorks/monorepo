package provisioner

import (
	"context"
	"strings"
	"time"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

// kafkaMirrorMakerRoleVars renders vars for a dedicated MM2 worker. Source
// clusters and the aggregator target are derived from the manifest's
// KafkaConfig.Regional list (passed via metadata at task-build time).
func kafkaMirrorMakerRoleVars(ctx context.Context, host inventory.Host, config ServiceConfig, helpers RoleBuildHelpers) (map[string]any, error) {
	_, arch, err := helpers.DetectRemoteOS(ctx, host)
	if err != nil {
		return nil, err
	}
	archKey := "linux-" + arch
	channel := platformChannelFromMetadata(config.Metadata)
	art, err := helpers.ResolveArtifact("kafka", archKey, channel, config.Metadata)
	if err != nil {
		return nil, err
	}

	vars := map[string]any{
		"kafka_mm_artifact_url":      art.URL,
		"kafka_mm_artifact_checksum": art.Checksum,
		"kafka_mm_version":           releaseVersion(config.Version, art.Version),
		"kafka_mm_heap_opts":         "-Xmx1G -Xms1G",
		"kafka_mm_rest_port":         8083,
		"kafka_mm_task_count":        2,
	}
	if config.Port > 0 {
		vars["kafka_mm_rest_port"] = config.Port
	}
	if heap, ok := config.Metadata["heap_opts"].(string); ok && heap != "" {
		vars["kafka_mm_heap_opts"] = heap
	}
	if t, ok := config.Metadata["task_count"].(int); ok && t > 0 {
		vars["kafka_mm_task_count"] = t
	}
	if sources, ok := config.Metadata["sources"].([]map[string]any); ok {
		vars["kafka_mm_sources"] = sources
	}
	if target, ok := config.Metadata["target"].(map[string]any); ok {
		vars["kafka_mm_target"] = target
	}
	if alias, ok := config.Metadata["local_cluster_alias"].(string); ok && alias != "" {
		vars["kafka_mm_local_cluster_alias"] = alias
	}
	if topics, ok := config.Metadata["topics_pattern"].(string); ok && topics != "" {
		vars["kafka_mm_topics_pattern"] = topics
	}
	return vars, nil
}

func kafkaMirrorMakerRoleDetect(ctx context.Context, host inventory.Host, _ ServiceConfig, helpers RoleBuildHelpers) (*detect.ServiceState, error) {
	if host.ExternalIP == "127.0.0.1" || host.ExternalIP == "localhost" {
		return &detect.ServiceState{Exists: false, Running: false}, nil
	}
	runner, err := helpers.SSHPool.Get(&ssh.ConnectionConfig{
		Address: host.ExternalIP, Port: 22, User: host.User, HostName: host.Name, Timeout: 10 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	svc := "frameworks-kafka-mirrormaker"
	result, runErr := runner.Run(ctx, "systemctl is-active "+svc+" 2>/dev/null | grep -qx active && echo RUNNING || echo NOT_RUNNING")
	running := runErr == nil && result != nil && strings.Contains(result.Stdout, "RUNNING") && !strings.Contains(result.Stdout, "NOT_RUNNING")
	bin, binErr := runner.Run(ctx, "test -x /opt/kafka/bin/connect-mirror-maker.sh && echo EXISTS")
	exists := binErr == nil && bin != nil && strings.Contains(bin.Stdout, "EXISTS")
	return &detect.ServiceState{Exists: exists, Running: running}, nil
}
