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

// KafkaMirrorMakerJMXPort is the loopback port each MirrorMaker2 worker's JMX
// exporter serves connector metrics on; the host-local vmagent scrapes it.
const KafkaMirrorMakerJMXPort = 9404

// kafkaMirrorMakerJMXExporterArtifact is the release-manifest infrastructure
// entry pinning the Prometheus JMX exporter javaagent jar.
const kafkaMirrorMakerJMXExporterArtifact = "jmx-prometheus-javaagent"

// kafkaMirrorMakerRESTAddress is where a worker's internode REST server binds
// and what it advertises to the other workers of its target: the WireGuard
// mesh IP, so forwarding works across hosts without exposing the unauthenticated
// endpoint on a public interface. A host without a mesh IP stays on loopback.
func kafkaMirrorMakerRESTAddress(host inventory.Host) string {
	if ip := strings.TrimSpace(host.WireguardIP); ip != "" {
		return ip
	}
	return "127.0.0.1"
}

// kafkaMirrorMakerRoleVars renders vars for a dedicated MM2 worker. The target
// is the worker host's own Kafka region and the sources are the links declared
// into it (passed via metadata at task-build time).
func kafkaMirrorMakerRoleVars(ctx context.Context, host inventory.Host, config ServiceConfig, helpers RoleBuildHelpers) (map[string]any, error) {
	if metaBool(config.Metadata, "_cleanup_only", false) {
		return map[string]any{}, nil
	}
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
	jmxExporter, err := helpers.ResolveArtifact(kafkaMirrorMakerJMXExporterArtifact, archKey, channel, config.Metadata)
	if err != nil {
		return nil, fmt.Errorf("kafka-mirrormaker: resolve JMX exporter artifact: %w", err)
	}

	vars := map[string]any{
		"kafka_mm_artifact_url":                   art.URL,
		"kafka_mm_artifact_checksum":              art.Checksum,
		"kafka_mm_version":                        releaseVersion(config.Version, art.Version),
		"kafka_mm_heap_opts":                      "-Xmx1G -Xms1G",
		"kafka_mm_rest_port":                      8083,
		"kafka_mm_rest_address":                   kafkaMirrorMakerRESTAddress(host),
		"kafka_mm_task_count":                     2,
		"kafka_mm_jmx_exporter_artifact_url":      jmxExporter.URL,
		"kafka_mm_jmx_exporter_artifact_checksum": jmxExporter.Checksum,
		"kafka_mm_jmx_port":                       KafkaMirrorMakerJMXPort,
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
	if pattern, ok := config.Metadata["exclude_pattern"].(string); ok && pattern != "" {
		vars["kafka_mm_exclude_pattern"] = pattern
	}
	if metaBool(config.Metadata, KafkaMirrorMakerDeferRestartKey, false) {
		vars["kafka_mm_defer_restart"] = true
	}
	return vars, nil
}

// KafkaMirrorMakerDeferRestartKey is the ServiceConfig metadata flag that makes
// the role record a pending restart (KafkaMirrorMakerRestartPendingPath)
// instead of restarting the worker on a change.
const KafkaMirrorMakerDeferRestartKey = "kafka_mm_defer_restart"

// KafkaMirrorMakerRestartPendingPath is the role's kafka_mm_restart_pending_path:
// present while the running worker predates its converged config or binaries.
const KafkaMirrorMakerRestartPendingPath = "/var/lib/kafka-mirrormaker/restart-pending"

// KafkaMirrorMakerRestartPendingProbe prints PENDING when the worker has a
// deferred restart outstanding.
const KafkaMirrorMakerRestartPendingProbe = "test -e " + KafkaMirrorMakerRestartPendingPath + " && echo PENDING || echo CLEAR"

// KafkaMirrorMakerCleanupPlaybook removes a worker without the install inputs
// the role's main entry point validates, so a host that is no longer a link
// worker can be cleaned up from its host name alone.
const KafkaMirrorMakerCleanupPlaybook = "playbooks/kafka_mirrormaker_cleanup.yml"

// kafkaMirrorMakerUnitPath is the worker's systemd unit. Worker presence is
// keyed on this unit, not on connect-mirror-maker.sh: MirrorMaker2 shares the
// /opt/kafka install with brokers, so every broker host carries the script.
const kafkaMirrorMakerUnitPath = "/etc/systemd/system/frameworks-kafka-mirrormaker.service"

func kafkaMirrorMakerPlaybookSelector(config ServiceConfig) string {
	if metaBool(config.Metadata, "_cleanup_only", false) {
		return KafkaMirrorMakerCleanupPlaybook
	}
	return "playbooks/kafka_mirrormaker.yml"
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
	unit, unitErr := runner.Run(ctx, "test -f "+kafkaMirrorMakerUnitPath+" && echo EXISTS || echo MISSING")
	if unitErr != nil {
		return nil, fmt.Errorf("kafka-mirrormaker: probe worker unit on %s: %w", host.Name, unitErr)
	}
	exists := unit != nil && strings.Contains(unit.Stdout, "EXISTS")
	return &detect.ServiceState{Exists: exists, Running: running}, nil
}
