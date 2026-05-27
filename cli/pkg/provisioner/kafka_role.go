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
			"kafka_version":           releaseVersion(config.Version, art.Version),
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
		if bindHost, ok := config.Metadata["bind_host"].(string); ok && bindHost != "" {
			vars["kafka_bind_host"] = bindHost
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
		ha := kafkaInternalTopicHA(config.Metadata)
		vars["kafka_min_insync_replicas"] = ha.minISR
		vars["kafka_offsets_topic_replication_factor"] = ha.offsetsRF
		vars["kafka_transaction_state_log_replication_factor"] = ha.transactionRF
		vars["kafka_transaction_state_log_min_isr"] = ha.transactionMinISR
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

type kafkaHASettings struct {
	minISR            int
	offsetsRF         int
	transactionRF     int
	transactionMinISR int
}

func kafkaInternalTopicHA(metadata map[string]any) kafkaHASettings {
	brokers := metaIntOr(metadata, "broker_count", 1)
	if brokers < 1 {
		brokers = 1
	}

	rf := brokers
	if rf > 3 {
		rf = 3
	}
	minISR := 1
	if rf >= 3 {
		minISR = 2
	}

	if override := metaIntOr(metadata, "min_insync_replicas", 0); override > 0 && override <= rf {
		minISR = override
	}
	offsetsRF := metaIntOr(metadata, "offsets_topic_replication_factor", rf)
	if offsetsRF < 1 {
		offsetsRF = 1
	}
	if offsetsRF > brokers {
		offsetsRF = brokers
	}

	transactionRF := metaIntOr(metadata, "transaction_state_log_replication_factor", rf)
	if transactionRF < 1 {
		transactionRF = 1
	}
	if transactionRF > brokers {
		transactionRF = brokers
	}
	transactionMinISR := metaIntOr(metadata, "transaction_state_log_min_isr", minISR)
	if transactionMinISR < 1 {
		transactionMinISR = 1
	}
	if transactionMinISR > transactionRF {
		transactionMinISR = transactionRF
	}

	return kafkaHASettings{
		minISR:            minISR,
		offsetsRF:         offsetsRF,
		transactionRF:     transactionRF,
		transactionMinISR: transactionMinISR,
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
	return func(ctx context.Context, host inventory.Host, _ ServiceConfig, helpers RoleBuildHelpers) (*detect.ServiceState, error) {
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
