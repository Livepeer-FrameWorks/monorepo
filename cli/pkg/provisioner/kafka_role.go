package provisioner

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

func kafkaRoleVarsFor(role string) RoleVarsBuilder {
	return func(ctx context.Context, host inventory.Host, config ServiceConfig, helpers RoleBuildHelpers) (map[string]any, error) {
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
		nodeID := metaIntOr(config.Metadata, "node_id", 1)
		clusterID := metaString(config.Metadata, "cluster_id")

		vars := map[string]any{
			"kafka_artifact_url":      art.URL,
			"kafka_artifact_checksum": art.Checksum,
			"kafka_version":           firstNonEmpty(config.Version, art.Version),
			"kafka_role":              role,
			"kafka_node_id":           nodeID,
			"kafka_cluster_id":        clusterID,
			"kafka_advertised_host":   meshOrExternal(config.Metadata, host),
		}
		if controllers, ok := config.Metadata["controllers"].([]map[string]any); ok {
			vars["kafka_controllers"] = controllers
		}
		if controllerQuorumVoters, ok := config.Metadata["controller_quorum_voters"].(string); ok && controllerQuorumVoters != "" {
			vars["kafka_controller_quorum_voters"] = controllerQuorumVoters
		}
		if brokers, ok := config.Metadata["brokers"].([]map[string]any); ok {
			vars["kafka_bootstrap_brokers"] = brokers
		}
		if topics, ok := config.Metadata["topics"].([]map[string]any); ok {
			cleanTopics, err := sanitizeKafkaTopics(topics)
			if err != nil {
				return nil, err
			}
			vars["kafka_topics"] = cleanTopics
		}
		if p := config.Port; p > 0 {
			if role == "controller" {
				vars["kafka_controller_port"] = p
			} else {
				vars["kafka_broker_port"] = p
			}
		}
		return vars, nil
	}
}

func sanitizeKafkaTopics(topics []map[string]any) ([]map[string]any, error) {
	clean := make([]map[string]any, 0, len(topics))
	for _, topic := range topics {
		sanitized := make(map[string]any, len(topic))
		for k, v := range topic {
			if v == nil {
				continue
			}
			if k == "config" {
				cfg, err := normalizeKafkaTopicConfig(v)
				if err != nil {
					return nil, err
				}
				if len(cfg) == 0 {
					continue
				}
				sanitized[k] = cfg
				continue
			}
			sanitized[k] = v
		}
		clean = append(clean, sanitized)
	}
	return clean, nil
}

func normalizeKafkaTopicConfig(v any) (map[string]any, error) {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Map {
		return nil, fmt.Errorf("kafka topic config must be a map, got %T", v)
	}

	cfg := make(map[string]any, rv.Len())
	for _, key := range rv.MapKeys() {
		if key.Kind() != reflect.String {
			return nil, fmt.Errorf("kafka topic config keys must be strings, got %s", key.Kind())
		}
		cfg[key.String()] = rv.MapIndex(key).Interface()
	}

	return cfg, nil
}

func kafkaRoleDetectFor(role string) RoleDetector {
	return func(ctx context.Context, host inventory.Host, helpers RoleBuildHelpers) (*detect.ServiceState, error) {
		if host.ExternalIP == "127.0.0.1" || host.ExternalIP == "localhost" {
			return &detect.ServiceState{Exists: false, Running: false}, nil
		}
		svc := "frameworks-kafka"
		if role == "controller" {
			svc = "frameworks-kafka-controller"
		}
		runner, err := helpers.SSHPool.Get(&ssh.ConnectionConfig{
			Address: host.ExternalIP, Port: 22, User: host.User, HostName: host.Name, Timeout: 10 * time.Second,
		})
		if err != nil {
			return nil, err
		}
		result, runErr := runner.Run(ctx, "systemctl is-active "+svc+" 2>/dev/null | grep -qx active && echo RUNNING || echo NOT_RUNNING")
		running := runErr == nil && result != nil && strings.Contains(result.Stdout, "RUNNING") && !strings.Contains(result.Stdout, "NOT_RUNNING")
		bin, binErr := runner.Run(ctx, "test -x /opt/kafka/bin/kafka-server-start.sh && echo EXISTS")
		exists := binErr == nil && bin != nil && strings.Contains(bin.Stdout, "EXISTS")
		return &detect.ServiceState{Exists: exists, Running: running}, nil
	}
}
